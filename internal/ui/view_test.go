package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// widest is the display width of the widest line of a rendered screen. Every
// assertion about the layout is made on this rather than on len(), because a
// CJK title is two cells per rune and a byte count would pass where the
// terminal wraps.
func widest(view string) int {
	w := 0
	for _, line := range strings.Split(view, "\n") {
		w = max(w, lipgloss.Width(line))
	}
	return w
}

const longTitle = "Never Gonna Give You Up (Official Music Video) [Remastered 4K 60fps] — Rick Astley, 1987, the whole thing"

const longPath = "/Users/somebody/Downloads/Never Gonna Give You Up (Official Music Video) [Remastered 4K 60fps].mp4"

func TestWindowSizeTruncatesALongTitle(t *testing.T) {
	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, longTitle, "Rick Astley")}}}
	m := pickerModel(t, f, true, threeRows())

	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 24})

	view := m.View()
	if widest(view) > 40 {
		t.Fatalf("the picker is %d cells wide in a 40-column terminal:\n%s", widest(view), view)
	}
	if !strings.Contains(view, ellipsis) {
		t.Fatalf("a title far wider than the terminal was not truncated:\n%s", view)
	}
	if strings.Contains(view, "the whole thing") {
		t.Fatalf("the tail of the title survived truncation:\n%s", view)
	}
	// And it must be cut, not wrapped onto a second line.
	if strings.Contains(view, "Never Gonna Give You Up (Official Music") &&
		strings.Contains(view, "Video)") {
		t.Fatalf("the title wrapped instead of truncating:\n%s", view)
	}
}

func TestWindowSizeTruncatesALongPath(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 24})
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: longPath}})

	view := m.View()
	if widest(view) > 40 {
		t.Fatalf("the done screen is %d cells wide in a 40-column terminal:\n%s", widest(view), view)
	}
	if !strings.Contains(view, ellipsis) {
		t.Fatalf("a path far wider than the terminal was not truncated:\n%s", view)
	}
	if strings.Contains(view, ".mp4") {
		t.Fatalf("the whole path is still on screen at 40 columns:\n%s", view)
	}

	// Widen the terminal and the whole path comes back.
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	if !strings.Contains(m.View(), longPath) {
		t.Fatalf("a path that fits was still truncated:\n%s", m.View())
	}
}

func TestEveryScreenFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 40, 24} {
		t.Run(itoa(width), func(t *testing.T) {
			for name, m := range screens(t) {
				m = send(m, tea.WindowSizeMsg{Width: width, Height: 24})
				view := m.View()
				if got := widest(view); got > width {
					t.Errorf("%s is %d cells wide at width %d:\n%s", name, got, width, view)
				}
				if strings.TrimSpace(view) == "" {
					t.Errorf("%s rendered nothing at width %d", name, width)
				}
			}
		})
	}
}

func TestAVeryNarrowTerminalDoesNotBreakTheLayout(t *testing.T) {
	// Below minContentWidth the layout stops shrinking rather than collapsing
	// into punctuation, so the bound is the floor, not the terminal.
	const floor = minContentWidth + 4
	for name, m := range screens(t) {
		m = send(m, tea.WindowSizeMsg{Width: 8, Height: 4})
		view := m.View()
		if got := widest(view); got > floor {
			t.Errorf("%s is %d cells wide at width 8, want at most the %d floor:\n%s", name, got, floor, view)
		}
		if strings.TrimSpace(view) == "" {
			t.Errorf("%s rendered nothing at width 8", name)
		}
	}
}

// screens builds one model per state, so a layout assertion can be made against
// all of them at once.
func screens(t *testing.T) map[string]Model {
	t.Helper()
	out := map[string]Model{}

	f := &fakes{}
	out["input"] = testModel(t, f, 80)

	probing := testModel(t, &fakes{}, 80)
	probing.hasBin = true
	probing = typeURL(probing, "https://example.com/v")
	probing = send(probing, keyOf(tea.KeyEnter))
	out["probing"] = probing

	out["picker"] = pickerModel(t, &fakes{probes: []probeOutcome{{probe: newProbe(t, longTitle, "Rick Astley")}}}, true, threeRows())
	out["picker without ffmpeg"] = pickerModel(t, &fakes{}, false, threeRows())
	out["empty picker"] = pickerModel(t, &fakes{}, false, nil)

	downloading := downloadingModel(t, &fakes{})
	downloading = send(downloading, progressMsg{seq: downloading.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 30_100_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true, Speed: 3_200_000, SpeedKnown: true,
	}})
	out["downloading"] = downloading

	merging := downloadingModel(t, &fakes{})
	merging = send(merging, progressMsg{seq: merging.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseMerging, Downloaded: 64_000_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true,
	}})
	out["merging"] = merging

	retrying := downloadingModel(t, &fakes{})
	retrying = send(retrying, downloadDoneMsg{seq: retrying.seq, err: staleErr()})
	out["retrying"] = retrying

	done := downloadingModel(t, &fakes{})
	done = send(done, downloadDoneMsg{seq: done.seq, res: &ytdlp.DownloadResult{Path: longPath, UsedWorkingDir: true}})
	out["done"] = done

	failed := downloadingModel(t, &fakes{})
	failed = send(failed, downloadDoneMsg{seq: failed.seq, err: hardErr()})
	out["error"] = failed

	quitting := downloadingModel(t, &fakes{})
	quitting, _ = step(quitting, keyOf(tea.KeyCtrlC))
	out["quitting"] = quitting

	return out
}

func TestDoneAndErrorScreensCarryTheirGlyphs(t *testing.T) {
	// The glyph is the signal a terminal rendering no colour still shows; the
	// words after it are the ones #7 settled and must not move.
	f := &fakes{}
	done := downloadingModel(t, f)
	done = send(done, downloadDoneMsg{seq: done.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})
	if !strings.Contains(done.View(), "✓ Saved") {
		t.Errorf("the done screen does not read \"✓ Saved\":\n%s", done.View())
	}

	failed := downloadingModel(t, &fakes{})
	failed = send(failed, downloadDoneMsg{seq: failed.seq, err: hardErr()})
	if !strings.Contains(failed.View(), "✗ That did not work") {
		t.Errorf("the error screen does not read \"✗ That did not work\":\n%s", failed.View())
	}
}

func TestInputScreenFramesTheBoxAndNamesTheApp(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	view := m.View()

	if !strings.Contains(view, appName) {
		t.Fatalf("the app name is missing:\n%s", view)
	}
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("the input is not in a framed box:\n%s", view)
	}
	if !strings.Contains(view, "enter") {
		t.Fatalf("the key legend is missing:\n%s", view)
	}
}

func TestPickerMarksTheSelectionAndNumbersTheRows(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	m = send(m, runes("j"))

	view := m.View()
	if !strings.Contains(view, selectedMarker+"2. 720p") {
		t.Fatalf("the row under the cursor is not marked:\n%s", view)
	}
	if !strings.Contains(view, unselectedMarker+"1. 1080p60") {
		t.Fatalf("the unselected rows are not aligned with the selected one:\n%s", view)
	}
	if !strings.Contains(view, "Rick Astley") {
		t.Fatalf("the uploader is missing:\n%s", view)
	}
}

func TestPickerLaysTheRowsOutInColumns(t *testing.T) {
	// Each column is as wide as its widest value: "audio only" sets the
	// quality column, and the sizes line up on their unit because the column
	// is right-aligned. The audio row's Label carries no size, so its cell is
	// blank rather than "~?".
	const cw = 76
	want := []string{
		padRight("▸ 1. 1080p60     mp4  ~142 MB", cw),
		"  2. 720p        mp4   ~64 MB",
		"  3. audio only  mp3",
	}
	if got := pickerLines(threeRows(), 0, cw); !equalLines(got, want) {
		t.Errorf("pickerLines:\n got %q\nwant %q", got, want)
	}

	m := pickerModel(t, &fakes{}, true, threeRows())
	view := m.View()
	for _, line := range want[1:] {
		if !strings.Contains(view, line) {
			t.Errorf("the picker does not show %q:\n%s", line, view)
		}
	}
	if !strings.Contains(view, want[0]) {
		t.Errorf("the selected row is not padded to the content width:\n%s", view)
	}
}

func TestPickerFallsBackToTheURLWhenThereIsNoTitle(t *testing.T) {
	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, "", "")}}}
	m := pickerModel(t, f, true, threeRows())

	if !strings.Contains(m.View(), "https://example.com/v") {
		t.Fatalf("with no title the URL is not shown instead:\n%s", m.View())
	}
}

func TestHelpDropsEntriesRatherThanWrapping(t *testing.T) {
	m := testModel(t, &fakes{}, 80)
	wide := m.help("aaa", "bbb", "ccc")
	if !strings.Contains(wide, "ccc") {
		t.Fatalf("help = %q, want every entry at 80 columns", wide)
	}

	m = send(m, tea.WindowSizeMsg{Width: 24, Height: 24})
	narrow := m.help("aaaaaaaa", "bbbbbbbb", "cccccccc")
	if strings.Contains(narrow, "\n") {
		t.Fatalf("help = %q, want one line", narrow)
	}
	if lipgloss.Width(narrow) > m.contentWidth() {
		t.Fatalf("help is %d cells wide, want at most %d", lipgloss.Width(narrow), m.contentWidth())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestViewNeverReachesDeps(t *testing.T) {
	// Rendering is pure. Anything that shells out, touches the network or reads
	// a file belongs in a tea.Cmd; called from View it would run once per
	// frame, on the loop's own goroutine.
	for name, m := range screens(t) {
		f := &fakes{}
		m.deps = f.deps()
		for range 3 {
			_ = m.View()
		}
		probes, downloads, cleanups := f.counts()
		f.mu.Lock()
		resolves, ranks := f.resolveCalls, f.rankCalls
		f.mu.Unlock()
		if probes+downloads+cleanups+resolves+ranks != 0 {
			t.Errorf("rendering %s called into ytdlp: resolve %d, probe %d, rank %d, download %d, cleanup %d",
				name, resolves, probes, ranks, downloads, cleanups)
		}
	}
}
