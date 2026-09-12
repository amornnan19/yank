package ui

import (
	"math"
	"strconv"
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
		return styles().doc.Render(styles().app.Render(appName) + "\n\n" + m.quittingView())
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
	return styles().doc.Render(styles().app.Render(appName) + "\n\n" + body)
}

// --- screens ----------------------------------------------------------------

func (m Model) inputView() string {
	cw := m.contentWidth()
	lines := []string{styles().box.Width(cw - 2).Render(m.input.View())}
	if m.hint != "" {
		lines = append(lines, fit(styles().faint, m.hint, cw))
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
// "checking" covers PATH and the cache, which on darwin can itself be a ~10s
// unpack (measured in #16), and "first run" is shown only once Resolve has
// reported that it is downloading a release — a spinner with no explanation in
// front of a 40 MB download reads as a hang, and the same words in front of a
// cache hit are a lie. The switch is knowledge from Resolve, never a timer.
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
	head := []string{fit(styles().title, m.videoTitle(), cw)}
	if u := strings.TrimSpace(m.info().Uploader); u != "" {
		head = append(head, fit(styles().faint, u, cw))
	}

	if len(m.rows) == 0 {
		return join(head...) + "\n\n" + wrap(m.noRowsExplanation(), cw) + "\n\n" +
			m.help("esc  back", "ctrl+c  quit")
	}

	body := []string{join(head...), ""}
	for i, line := range pickerLines(m.rows, m.cursor, cw) {
		if i == m.cursor {
			body = append(body, fit(styles().selected, line, cw))
			continue
		}
		body = append(body, line)
	}
	if !m.bin.HasFFmpeg {
		body = append(body, "", styles().faint.Render(wrap(noFFmpegHint, cw)))
	}
	return join(body...) + "\n\n" +
		m.help("↑↓ jk  move", "1-9  jump", "enter  download", "esc  back")
}

// pickerLines is every picker row as unstyled text, one string per row, each
// at most cw cells wide.
//
// The rows are laid out as columns — quality, container, size — with each
// column as wide as its widest value in this list and two spaces between
// them. The size is right-aligned: sizes are compared down the column, and
// "~742 KB" over "~1.2 GB" only lines up on the unit if the digits do. Row.Label
// arrives pre-joined from internal/ytdlp, but Row also carries the fields it
// was joined from, so the columns are built here without an API change.
//
// If the aligned layout does not fit cw for every row, the whole list falls
// back to Label truncated to cw, so no row is ever wider than it was before
// the columns existed. The choice is made for the list rather than per row: a
// list with some rows aligned and some not is ragged in a way that reads as a
// bug, and the columns are only ever wider than the label they replace.
//
// The row under the cursor is padded with spaces to exactly cw, so that the
// reverse-video block its style puts on it spans the full row instead of
// stopping at the last character.
//
// It exists as a function so the text handed to fit is something a test can
// assert on directly. Under `go test` the colour profile is Ascii and
// Style.Render is the identity, so the damage a style-then-truncate would do is
// invisible in the rendered view; the unstyled line is where it is visible.
func pickerLines(rows []ytdlp.Row, cursor, cw int) []string {
	cells := make([]rowCells, len(rows))
	var widths rowCells
	for i, row := range rows {
		cells[i] = splitRow(row)
		widths = widths.widen(cells[i])
	}

	lines := make([]string, len(rows))
	fits := true
	for i, c := range cells {
		lines[i] = strings.TrimRight(pickerRowText(i, i == cursor, c.aligned(widths)), " ")
		if lipgloss.Width(lines[i]) > cw {
			fits = false
		}
	}
	if !fits {
		for i, row := range rows {
			lines[i] = pickerRowText(i, i == cursor, row.Label)
		}
	}
	for i := range lines {
		lines[i] = truncate(lines[i], cw)
	}
	if cursor >= 0 && cursor < len(lines) {
		lines[cursor] = padRight(lines[cursor], cw)
	}
	return lines
}

// rowCells is one picker row split into the columns the layout aligns. As a
// width record, each field holds that column's width in cells instead.
type rowCells struct {
	quality, ext, size string
}

// maxRenderableFPS mirrors the bound internal/ytdlp puts on the fps suffix.
// FPS is whatever the site said, and converting a float64 outside int's range
// is implementation-defined, so a frame rate past this is not printed.
const maxRenderableFPS = 1000.0

// splitRow spells each column the way Row.Label already does, so the aligned
// row and the Label it falls back to never disagree about a value.
func splitRow(row ytdlp.Row) rowCells {
	c := rowCells{ext: sanitise(row.Ext)}
	switch {
	case row.AudioOnly:
		c.quality = "audio only"
		if c.ext == "" {
			// The extract-audio row stands for no single format, so it has
			// no container of its own; the transcode target is what the
			// download will be, and what its Label says.
			c.ext = "mp3"
		}
		// Its Label carries no size unless one was worked out, and "~?"
		// would claim an unknown the row never stated.
		if row.SizeKnown {
			c.size = "~" + humanBytes(row.Bytes)
		}
	default:
		c.quality = strconv.Itoa(row.Height) + "p"
		if row.FPS > 30 && row.FPS < maxRenderableFPS {
			c.quality += strconv.Itoa(int(math.Round(row.FPS)))
		}
		if c.ext == "" {
			c.ext = "?"
		}
		c.size = "~?"
		if row.SizeKnown {
			c.size = "~" + humanBytes(row.Bytes)
		}
	}
	return c
}

// widen returns the width record grown to hold c's cells.
func (w rowCells) widen(c rowCells) rowCells {
	return rowCells{
		quality: wider(w.quality, c.quality),
		ext:     wider(w.ext, c.ext),
		size:    wider(w.size, c.size),
	}
}

// wider is the wider of two strings by display width, used to carry column
// widths as the widest value seen so far.
func wider(a, b string) string {
	if lipgloss.Width(b) > lipgloss.Width(a) {
		return b
	}
	return a
}

// aligned joins the cells into one row, each padded to its column's width.
func (c rowCells) aligned(widths rowCells) string {
	const gap = "  "
	return padRight(c.quality, lipgloss.Width(widths.quality)) + gap +
		padRight(c.ext, lipgloss.Width(widths.ext)) + gap +
		padLeft(c.size, lipgloss.Width(widths.size))
}

// pickerRowText is one picker line before any styling: the cursor marker, the
// digit that jumps to it, and the row's body — its aligned columns, or its
// Label when the columns do not fit.
func pickerRowText(i int, selected bool, body string) string {
	marker := unselectedMarker
	if selected {
		marker = selectedMarker
	}
	return marker + rowNumber(i) + body
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
		fit(styles().title, m.videoTitle(), cw),
		fit(styles().faint, m.row.Label, cw),
		"",
		m.barLine(),
	}

	switch {
	case m.retrying:
		// The saved info-json's media URLs expired while the picker was up.
		// Nothing has failed; a fresh extraction is on its way.
		lines = append(lines, m.phaseSpinner()+" "+truncate(retryingMessage, cw-2))
	case m.hasProg && m.prog.Phase == ytdlp.PhaseMerging:
		lines = append(lines, m.phaseSpinner()+" "+truncate("merging video and audio…", cw-2))
	case m.hasProg && m.prog.Phase == ytdlp.PhaseConverting:
		lines = append(lines, m.phaseSpinner()+" "+truncate("converting audio…", cw-2))
	default:
		lines = append(lines, fit(styles().faint, m.statsLine(), cw))
	}

	return join(lines...) + "\n\n" + m.help("esc  cancel", "ctrl+c  quit")
}

// phaseSpinner is the spinner as drawn on the phase lines: the same frames as
// everywhere else, in the phase colour. It is a style applied at render time,
// not a second spinner model, so there is one tick to keep in step.
func (m Model) phaseSpinner() string {
	spin := m.spin
	spin.Style = styles().phase
	return spin.View()
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
	lines := []string{fit(styles().success, savedTitle, cw), ""}
	if m.result != nil {
		lines = append(lines, fit(styles().path, m.result.Path, m.pathWidth()))
		if m.result.UsedWorkingDir {
			// DownloadsDir fell back. Saying "check your Downloads folder"
			// would send the user to a directory the file is not in.
			lines = append(lines, "", styles().faint.Render(wrap(workingDirNote, cw)))
		}
	}
	return join(lines...) + "\n\n" + m.help("enter  another", "q  quit")
}

// savedTitle and failedTitle head the two ending screens. The glyph carries
// the same signal as the colour on a terminal rendering none, for two cells.
const (
	savedTitle  = "✓ Saved"
	failedTitle = "✗ That did not work"
)

// workingDirNote explains a path that is not where downloads normally go.
const workingDirNote = "Your home directory could not be found, so this went to the directory yank was started in rather than to Downloads."

func (m Model) errorView() string {
	cw := m.contentWidth()
	return join(
		fit(styles().failure, failedTitle, cw),
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
	return styles().faint.Render(line)
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

// padRight and padLeft pad s with spaces to w cells, measured the way truncate
// measures. A string already w or wider is returned as it is: cutting is
// truncate's job, and these are only ever called on text it has passed.
func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}
