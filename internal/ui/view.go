package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// appName heads every screen.
const appName = "yank"

// View renders the current state. It is pure: no exec, no files, no clock. Any
// work a screen needs has already happened in a tea.Cmd and landed in a field.
func (m Model) View() string {
	if m.quitting {
		return docStyle.Render(appStyle.Render(appName) + "\n\n" + m.quittingView())
	}

	var body string
	switch m.state {
	case stateProbing:
		body = m.probingView()
	case statePicker:
		body = m.pickerView()
	case stateDownloading:
		body = m.downloadingView()
	case stateDone:
		body = m.doneView()
	case stateError:
		body = m.errorView()
	default:
		body = m.inputView()
	}
	return docStyle.Render(appStyle.Render(appName) + "\n\n" + body)
}

// --- screens ----------------------------------------------------------------

func (m Model) inputView() string {
	cw := m.contentWidth()
	lines := []string{boxStyle.Width(cw - 2).Render(m.input.View())}
	if m.hint != "" {
		lines = append(lines, fit(faintStyle, m.hint, cw))
	}
	return join(lines...) + "\n\n" + m.help("enter  fetch", "ctrl+c  quit")
}

// quittingView is what ctrl+c shows while the run it cancelled finishes dying.
// The spinner keeps moving so the screen does not read as a hang, and the
// legend says the way out is the key that was just pressed.
func (m Model) quittingView() string {
	return m.spin.View() + " " + truncate("stopping…", m.contentWidth()-2) + "\n\n" +
		m.help("ctrl+c  quit now")
}

func (m Model) probingView() string {
	// The spinner and its space cost two cells; the status line gets the rest.
	return m.spin.View() + " " + truncate(m.statusLine(), m.contentWidth()-2) + "\n\n" +
		m.help("esc  cancel", "ctrl+c  quit")
}

// statusLine says what the spinner is waiting for. Resolving gets two wordings:
// the first run has to download and unpack a release, and a spinner with no
// explanation in front of a 40 MB download reads as a hang.
func (m Model) statusLine() string {
	switch {
	case m.step == stepResolving && m.firstRun:
		return "first run: fetching yt-dlp…"
	case m.step == stepResolving:
		return "checking yt-dlp…"
	default:
		return "fetching video info…"
	}
}

func (m Model) pickerView() string {
	cw := m.contentWidth()
	head := []string{fit(titleStyle, m.videoTitle(), cw)}
	if u := strings.TrimSpace(m.info().Uploader); u != "" {
		head = append(head, fit(faintStyle, u, cw))
	}

	if len(m.rows) == 0 {
		return join(head...) + "\n\n" + wrap(m.noRowsExplanation(), cw) + "\n\n" +
			m.help("esc  back", "ctrl+c  quit")
	}

	body := []string{join(head...), ""}
	for i, row := range m.rows {
		selected := i == m.cursor
		text := pickerRowText(i, selected, row.Label)
		if selected {
			body = append(body, fit(selectedStyle, text, cw))
			continue
		}
		body = append(body, truncate(text, cw))
	}
	if !m.bin.HasFFmpeg {
		body = append(body, "", faintStyle.Render(wrap(noFFmpegHint, cw)))
	}
	return join(body...) + "\n\n" +
		m.help("↑↓ jk  move", "1-9  jump", "enter  download", "esc  back")
}

// pickerRowText is one picker line before any styling: the cursor marker, the
// digit that jumps to it, and the row's own label.
//
// It exists as a function so the text handed to fit is something a test can
// assert on directly. Under `go test` the colour profile is Ascii and
// Style.Render is the identity, so the damage a style-then-truncate would do is
// invisible in the rendered view; the unstyled line is where it is visible.
func pickerRowText(i int, selected bool, label string) string {
	marker := unselectedMarker
	if selected {
		marker = selectedMarker
	}
	return marker + rowNumber(i) + label
}

// rowNumber is the "1-9 jump" affordance printed where it is used. Rank never
// returns more than seven rows, so the blank branch is defensive.
func rowNumber(i int) string {
	if i > 8 {
		return "   "
	}
	return string(rune('1'+i)) + ". "
}

// noFFmpegHint is shown when ffmpeg is missing but some formats survived
// ranking, so the user knows the list is short for a reason they can fix.
const noFFmpegHint = "ffmpeg was not found, so merged video+audio and mp3 rows are hidden. Install ffmpeg to see them."

// noRowsExplanation says why the picker is empty.
//
// Zero rows is a normal outcome, not a failure. On today's YouTube with no
// ffmpeg it is the usual one: every high-quality stream carries video or audio
// and not both, and without ffmpeg there is nothing to merge them with. An
// empty box would read as a bug in yank.
func (m Model) noRowsExplanation() string {
	if !m.bin.HasFFmpeg {
		return "Nothing here can be downloaded on its own: this site keeps video and audio in separate streams, and joining them needs ffmpeg, which was not found. Install ffmpeg and try this URL again."
	}
	return "yt-dlp listed formats for this video, but none of them is something yank can offer as a single download."
}

func (m Model) downloadingView() string {
	cw := m.contentWidth()
	lines := []string{
		fit(titleStyle, m.videoTitle(), cw),
		fit(faintStyle, m.row.Label, cw),
		"",
		m.barLine(),
	}

	switch {
	case m.retrying:
		// The saved info-json's media URLs expired while the picker was up.
		// Nothing has failed; a fresh extraction is on its way.
		lines = append(lines, m.spin.View()+" "+truncate(retryingMessage, cw-2))
	case m.hasProg && m.prog.Phase == ytdlp.PhaseMerging:
		lines = append(lines, m.spin.View()+" "+truncate("merging video and audio…", cw-2))
	case m.hasProg && m.prog.Phase == ytdlp.PhaseConverting:
		lines = append(lines, m.spin.View()+" "+truncate("converting audio…", cw-2))
	default:
		lines = append(lines, fit(faintStyle, m.statsLine(), cw))
	}

	return join(lines...) + "\n\n" + m.help("esc  cancel", "ctrl+c  quit")
}

// retryingMessage is the one line the stale-info retry shows. It says what
// happened rather than what failed, because nothing did.
const retryingMessage = "the download link had expired — fetching fresh video info…"

// barLine is the progress bar with its percentage. The bar is drawn even when
// the percentage is unknown: an empty bar beside "--%" says "no idea how far
// along", where no bar at all says "nothing is happening".
func (m Model) barLine() string {
	pct, known := m.prog.Percent()
	label := percentLabel(pct, known)
	// The label and its separator come off the bar's width so the whole line
	// still fits the terminal.
	bar := m.bar
	bar.Width = max(1, m.contentWidth()-lipgloss.Width(label)-2)
	fill := 0.0
	if known {
		fill = pct / 100
	}
	return bar.ViewAs(fill) + "  " + label
}

func (m Model) statsLine() string {
	p := m.prog
	got := "?"
	if p.DownloadedKnown {
		got = humanBytes(p.Downloaded)
	}
	total := "?"
	if p.TotalKnown {
		total = humanBytes(p.Total)
		if p.TotalEstimated {
			// An estimate stated as a fact is a number the user will notice
			// being wrong at the end of the download.
			total = "~" + total
		}
	}
	line := got + " / " + total
	if p.SpeedKnown {
		line += "  ·  " + humanSpeed(p.Speed)
	}
	return line
}

func (m Model) doneView() string {
	cw := m.contentWidth()
	lines := []string{fit(titleStyle, "Saved", cw), ""}
	if m.result != nil {
		lines = append(lines, truncate(m.result.Path, m.pathWidth()))
		if m.result.UsedWorkingDir {
			// DownloadsDir fell back. Saying "check your Downloads folder"
			// would send the user to a directory the file is not in.
			lines = append(lines, "", faintStyle.Render(wrap(workingDirNote, cw)))
		}
	}
	return join(lines...) + "\n\n" + m.help("enter  another", "q  quit")
}

// workingDirNote explains a path that is not where downloads normally go.
const workingDirNote = "Your home directory could not be found, so this went to the directory yank was started in rather than to Downloads."

func (m Model) errorView() string {
	cw := m.contentWidth()
	return join(
		fit(titleStyle, "That did not work", cw),
		"",
		wrap(m.errMsg, cw),
	) + "\n\n" + m.help("esc  back", "ctrl+c  quit")
}

// --- shared bits ------------------------------------------------------------

// help is the key legend at the foot of every screen. It drops entries rather
// than wrapping when the terminal is too narrow to hold them all: a legend that
// takes four lines pushes the content off a short terminal.
func (m Model) help(entries ...string) string {
	const sep = " · "
	cw := m.contentWidth()
	line := ""
	for _, e := range entries {
		next := e
		if line != "" {
			next = line + sep + e
		}
		if lipgloss.Width(next) > cw {
			break
		}
		line = next
	}
	if line == "" && len(entries) > 0 {
		line = truncate(entries[0], cw)
	}
	return faintStyle.Render(line)
}

// videoTitle is the title to show, falling back to the URL for the extractors
// that return an empty one.
func (m Model) videoTitle() string {
	if t := strings.TrimSpace(m.info().Title); t != "" {
		return t
	}
	return m.url
}

// info is the probed VideoInfo, or a zero one when there is no probe — which
// happens on a frame rendered between states.
func (m Model) info() ytdlp.VideoInfo {
	if m.probe == nil {
		return ytdlp.VideoInfo{}
	}
	return m.probe.Info
}

// wrap word-wraps s to w cells.
//
// It sanitises for the same reason truncate does: the error screen renders
// yt-dlp's own words, which carry whatever the page said, and a newline inside
// them would break the wrap into lines lipgloss never measured.
func wrap(s string, w int) string {
	return lipgloss.NewStyle().Width(max(1, w)).Render(sanitise(s))
}

// join stacks lines, skipping nothing: an empty string is a blank line and is
// used as one.
func join(lines ...string) string { return strings.Join(lines, "\n") }
