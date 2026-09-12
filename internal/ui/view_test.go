package ui

import (
	"github.com/charmbracelet/bubbles/spinner"
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

	// At 80x24 the name is the wordmark, drawn, not spelled.
	for _, row := range wordmarkRows {
		if !strings.Contains(view, row) {
			t.Fatalf("the wordmark row %q is missing:\n%s", row, view)
		}
	}
	if strings.Contains(view, appName) {
		t.Fatalf("the one-line name is drawn under the wordmark:\n%s", view)
	}
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("the input is not in a framed box:\n%s", view)
	}
	if !strings.Contains(view, "enter") {
		t.Fatalf("the key legend is missing:\n%s", view)
	}
}

func TestWordmarkRowsAreAllTheSameWidth(t *testing.T) {
	w := wordmarkWidth()
	for i, row := range wordmarkRows {
		if got := lipgloss.Width(row); got != w {
			t.Errorf("wordmark row %d is %d cells, want %d like the widest: %q", i, got, w, row)
		}
	}
	if len(wordmarkRows) < 4 || len(wordmarkRows) > 5 {
		t.Errorf("the wordmark is %d rows, want 4 or 5", len(wordmarkRows))
	}
}

// inputHeader is the lines of an input screen above its box: the wordmark, or
// the one-line name.
func inputHeader(view string) []string {
	var out []string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "╭") {
			break
		}
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func TestWordmarkCollapsesToTheOneLineHeaderWithoutRoom(t *testing.T) {
	collapsed := []string{appName}
	cases := []struct {
		name          string
		width, height int
		want          []string
	}{
		{"room for it", 80, 24, nil},
		{"tall", 80, 30, nil},
		{"just tall enough", 80, minWordmarkHeight, nil},
		{"one row too short", 80, minWordmarkHeight - 1, collapsed},
		{"height unknown", 80, 0, collapsed},
		{"just wide enough", wordmarkWidth() + 4, 24, nil},
		{"one column too narrow", wordmarkWidth() + 3, 24, collapsed},
		{"narrow", 24, 24, collapsed},
	}
	for _, tc := range cases {
		want := tc.want
		if want == nil {
			for _, row := range wordmarkRows {
				want = append(want, strings.TrimSpace(row))
			}
		}
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t, &fakes{}, tc.width)
			m.height = tc.height
			m.layout()
			view := m.View()
			// inputHeader is every non-blank line above the box, so a rule
			// under the collapsed name would show up as a second line here;
			// the collapsed header is meant to be exactly as it was before.
			if got := inputHeader(view); !equalLines(got, want) {
				t.Fatalf("at %dx%d the header is %q, want %q:\n%s", tc.width, tc.height, got, want, view)
			}
			if got := widest(view); got > max(tc.width, minContentWidth+4) {
				t.Fatalf("at %dx%d the input screen is %d cells wide:\n%s", tc.width, tc.height, got, view)
			}
		})
	}

	// A resize is the ordinary route to a height: the wordmark follows it
	// both ways.
	m := testModel(t, &fakes{}, 80)
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 10})
	if got := inputHeader(m.View()); !equalLines(got, collapsed) {
		t.Fatalf("after a resize to 10 rows the header is %q, want the one-line name", got)
	}
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 30})
	if got := inputHeader(m.View()); len(got) != len(wordmarkRows) {
		t.Fatalf("after a resize to 30 rows the header is %q, want the wordmark", got)
	}
}

func TestEveryOtherScreenRulesUnderItsHeader(t *testing.T) {
	for name, m := range screens(t) {
		m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
		lines := strings.Split(m.View(), "\n")
		// Row 0 is the doc padding, row 1 the name, row 2 the rule.
		if len(lines) < 3 {
			t.Fatalf("%s rendered %d lines", name, len(lines))
		}
		rule := strings.TrimSpace(lines[2])
		if name == "input" {
			// The box's own border is drawn with the same character, so the
			// check is on the header rows alone.
			if got := inputHeader(m.View()); len(got) != len(wordmarkRows) {
				t.Errorf("the input screen's header is %q, want the wordmark and nothing under it", got)
			}
			continue
		}
		if strings.TrimSpace(lines[1]) != appName {
			t.Errorf("%s does not open on the one-line name: %q", name, lines[1])
		}
		if rule != strings.Repeat("─", m.contentWidth()) {
			t.Errorf("%s has no rule the width of the content under its name: %q", name, rule)
		}
	}
}

func TestSweepBarIsAlwaysTheWidthAskedForAndMoves(t *testing.T) {
	for _, width := range []int{1, 2, 5, 6, 7, 12, 30, 71, 92} {
		for _, frame := range []int{0, 1, 2, 7, 100, 1001} {
			if got := lipgloss.Width(sweepBar(width, frame)); got != width {
				t.Errorf("sweepBar(%d, %d) is %d cells wide", width, frame, got)
			}
		}
	}
	if sweepBar(0, 3) != "" {
		t.Errorf("sweepBar(0, 3) = %q, want nothing for no width", sweepBar(0, 3))
	}

	// The block's position over a full period: out to the far end, back to
	// the near one, never off the track, and never the same two ticks running.
	const width = 30
	block := width / 6
	travel := width - block
	var positions []int
	for frame := 0; frame <= 2*((travel+sweepStep-1)/sweepStep); frame++ {
		plain := sgr.ReplaceAllString(sweepBar(width, frame), "")
		pos := strings.Index(plain, "█")
		if pos < 0 || strings.Count(plain, "█") != block {
			t.Fatalf("frame %d: %q has no block of %d cells", frame, plain, block)
		}
		positions = append(positions, lipgloss.Width(plain[:pos]))
	}
	if positions[0] != 0 {
		t.Errorf("the sweep starts at %d, want the left end: %v", positions[0], positions)
	}
	peak := 0
	for i, p := range positions {
		if p < 0 || p > travel {
			t.Errorf("frame %d puts the block at %d, off a track with %d cells of room", i, p, travel)
		}
		if p > positions[peak] {
			peak = i
		}
	}
	if peak == 0 || peak == len(positions)-1 {
		t.Fatalf("the sweep never turned round: %v", positions)
	}
	for i := 1; i < len(positions); i++ {
		switch {
		case i <= peak && positions[i] <= positions[i-1]:
			t.Errorf("on the way out the block did not advance between frames %d and %d: %v", i-1, i, positions)
		case i > peak && positions[i] >= positions[i-1]:
			t.Errorf("on the way back the block did not retreat between frames %d and %d: %v", i-1, i, positions)
		}
	}
}

func TestSweepOffsetTurnsAtBothEnds(t *testing.T) {
	// Five cells of room at two cells a frame: out in three frames, the last
	// of them shortened to touch the end, back in three, and round again.
	if sweepStep != 2 {
		t.Fatalf("sweepStep = %d; these cases are written for 2", sweepStep)
	}
	cases := []struct{ travel, frame, want int }{
		{5, 0, 0}, {5, 1, 2}, {5, 2, 4}, {5, 3, 5}, {5, 4, 3}, {5, 5, 1}, {5, 6, 0}, {5, 7, 2}, {5, 13, 2},
		{4, 0, 0}, {4, 1, 2}, {4, 2, 4}, {4, 3, 2}, {4, 4, 0},
		{0, 7, 0}, {-3, 7, 0}, {5, -1, 1},
	}
	for _, tc := range cases {
		if got := sweepOffset(tc.travel, tc.frame); got != tc.want {
			t.Errorf("sweepOffset(%d, %d) = %d, want %d", tc.travel, tc.frame, got, tc.want)
		}
	}
}

func TestMergingSweepsOnTheSpinnerTick(t *testing.T) {
	m := downloadingModel(t, &fakes{})
	m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseMerging, Downloaded: 64_000_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true,
	}})
	before := m.barLine()
	m = send(m, m.spin.Tick())
	after := m.barLine()

	if before == after {
		t.Fatalf("a spinner tick did not move the sweep:\n%s", before)
	}
	// The label slot is percentLabel's, untouched: a merge of a download whose
	// bytes were all counted reads 100%.
	for _, line := range []string{before, after} {
		if !strings.HasSuffix(line, "100%") {
			t.Errorf("the sweep line does not keep the percent label: %q", line)
		}
		if strings.Count(line, "█") != (m.contentWidth()-lipgloss.Width("100%")-2)/6 {
			t.Errorf("the sweep block is not a sixth of the bar: %q", line)
		}
	}

	// A tick from an abandoned chain is not a frame.
	stale := m.spin.Tick().(spinner.TickMsg)
	m = send(m, m.spin.Tick())
	moved := m.barLine()
	if again := send(m, stale).barLine(); again != moved {
		t.Errorf("a stale tick moved the sweep:\n%s\n%s", moved, again)
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
