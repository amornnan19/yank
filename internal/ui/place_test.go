package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// --- reading a placed view --------------------------------------------------

// cellGrid is a rendered view as terminal cells, the styling stripped: one
// string per cell, and "" for the cell a wide character's second half takes.
func cellGrid(view string) [][]string {
	lines := strings.Split(view, "\n")
	grid := make([][]string, len(lines))
	for i, line := range lines {
		for c, w := range graphemes(sgr.ReplaceAllString(line, "")) {
			grid[i] = append(grid[i], c)
			for range w - 1 {
				grid[i] = append(grid[i], "")
			}
		}
	}
	return grid
}

// cellAt is the cell at row r, column c of a grid, or a space past the end of
// a line.
func cellAt(grid [][]string, r, c int) string {
	if r < 0 || r >= len(grid) || c < 0 || c >= len(grid[r]) {
		return " "
	}
	return grid[r][c]
}

// blockRows is the first and last non-blank row of a view, or -1, -1.
func blockRows(view string) (first, last int) {
	first, last = -1, -1
	for i, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(sgr.ReplaceAllString(line, "")) != "" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// blockLeft is the leftmost column any non-blank line of a view starts at.
func blockLeft(view string) int {
	left := -1
	for _, line := range strings.Split(view, "\n") {
		plain := sgr.ReplaceAllString(line, "")
		if strings.TrimSpace(plain) == "" {
			continue
		}
		if n := len(plain) - len(strings.TrimLeft(plain, " ")); left < 0 || n < left {
			left = n
		}
	}
	return left
}

// boxCorner is the row and column of the input box's top-left corner.
func boxCorner(view string) (row, col int) {
	grid := cellGrid(view)
	for r, cells := range grid {
		for c, cell := range cells {
			if cell == "╭" {
				return r, c
			}
		}
	}
	return -1, -1
}

// --- centring ---------------------------------------------------------------

func TestEveryScreenIsCentred(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		width, height := size[0], size[1]
		for name, m := range screens(t) {
			m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
			view := m.View()
			where := name + " at " + itoa(width) + "x" + itoa(height)
			lines := strings.Split(view, "\n")
			if len(lines) > height {
				t.Errorf("%s is %d rows:\n%s", where, len(lines), view)
			}
			if got := widest(view); got > width {
				t.Errorf("%s is %d cells wide:\n%s", where, got, view)
			}

			first, last := blockRows(view)
			rows := last - first + 1
			if want := (height - rows) / 2; first < want-1 || first > want+1 {
				t.Errorf("%s starts on row %d; a %d-row block is centred on row %d:\n%s", where, first, rows, want, view)
			}
			// The block is the content width wide wherever its widest line
			// ends, so its left margin and its right margin are the same.
			left := blockLeft(view)
			if right := width - left - m.contentWidth(); left < right-1 || left > right+1 {
				t.Errorf("%s starts at column %d, leaving %d columns to the right of the content:\n%s", where, left, right, view)
			}
		}
	}
}

func TestTheBlockStaysPutOnItsScreen(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		width, height := size[0], size[1]
		at := itoa(width) + "x" + itoa(height)

		// The static input screen: typing, and the hint coming and going.
		m := testModel(t, &fakes{}, width)
		m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
		row, col := boxCorner(m.View())
		still := func(what string, view string) {
			t.Helper()
			if r, c := boxCorner(view); r != row || c != col {
				t.Errorf("at %s %s moved the box from %d,%d to %d,%d:\n%s", at, what, row, col, r, c, view)
			}
		}
		for _, r := range "not a url" {
			m = send(m, runes(string(r)))
			still("typing", m.View())
		}
		m = send(m, keyOf(tea.KeyEnter))
		if m.hint == "" {
			t.Fatalf("an invalid URL set no hint")
		}
		still("the hint", m.View())
		m = send(m, runes("x"))
		if m.hint != "" {
			t.Fatalf("typing did not clear the hint")
		}
		still("the hint going", m.View())

		// With motion: the site badge fading in under the box.
		mm := motionModel(t, &fakes{}, width, height)
		mm = runMotion(t, mm, 2*time.Second, nil)
		row, col = boxCorner(mm.View())
		mm.input.SetValue("https://www.youtube.com/watch?v=")
		mm = send(mm, runes("x"))
		badged := false
		runMotion(t, mm, 4*time.Second, func(mm Model) {
			view := mm.View()
			badged = badged || lineContaining(view, "▶ youtube.com") > row
			still("the badge", view)
		})
		if !badged {
			t.Errorf("at %s the badge never showed; this test is not exercising it", at)
		}

		// The download screen: the stats line giving way to the phase line.
		dl := downloadingModel(t, &fakes{})
		dl = send(dl, tea.WindowSizeMsg{Width: width, Height: height})
		first, _ := blockRows(dl.View())
		left := blockLeft(dl.View())
		for _, p := range []ytdlp.Progress{
			{Phase: ytdlp.PhaseDownloading, Downloaded: 1_000_000, DownloadedKnown: true, Total: 64_000_000, TotalKnown: true, Speed: 3_200_000, SpeedKnown: true},
			{Phase: ytdlp.PhaseMerging, Downloaded: 64_000_000, DownloadedKnown: true, Total: 64_000_000, TotalKnown: true},
			{Phase: ytdlp.PhaseConverting},
		} {
			dl = send(dl, progressMsg{seq: dl.seq, p: p})
			if f, _ := blockRows(dl.View()); f != first || blockLeft(dl.View()) != left {
				t.Errorf("at %s the %v line moved the block from %d,%d to %d,%d", at, p.Phase, first, left, f, blockLeft(dl.View()))
			}
		}

		// The done screen: its notes do not move the title or the path.
		var titleRow, pathRow int
		for i, res := range []ytdlp.DownloadResult{
			{Path: "/x/a.mp4"},
			{Path: "/x/a.mp4", UsedWorkingDir: true},
			{Path: "/x/a.mp4", AlreadyExisted: true},
			{Path: "/x/a.mp4", AlreadyExisted: true, UsedWorkingDir: true},
		} {
			done := downloadingModel(t, &fakes{})
			done = send(done, tea.WindowSizeMsg{Width: width, Height: height})
			done = send(done, downloadDoneMsg{seq: done.seq, res: &res})
			view := done.View()
			title := lineContaining(view, "✓")
			path := lineContaining(view, "/x/a.mp4")
			if i == 0 {
				titleRow, pathRow = title, path
				continue
			}
			if title != titleRow || path != pathRow {
				t.Errorf("at %s the done screen for %+v has its title on row %d and path on %d, want %d and %d:\n%s",
					at, res, title, path, titleRow, pathRow, view)
			}
		}
	}
}

func TestAnUnknownOrTooSmallTerminalIsDrawnTopLeft(t *testing.T) {
	topLeft := func(t *testing.T, where string, m Model) {
		t.Helper()
		view := m.View()
		block, _, _ := m.screenBlock()
		if view != styles().doc.Render(block) {
			t.Errorf("%s is not the top-left layout:\n%s", where, view)
		}
		if first, _ := blockRows(view); first != marginRows {
			t.Errorf("%s starts on row %d, want %d", where, first, marginRows)
		}
		if left := blockLeft(view); left != marginCols {
			t.Errorf("%s starts at column %d, want %d", where, left, marginCols)
		}
	}

	for name, m := range screens(t) {
		unknown := m
		unknown.width, unknown.height = 0, 0
		unknown.layout()
		topLeft(t, name+" before any size", unknown)

		noHeight := m
		noHeight.width, noHeight.height = 200, 0
		noHeight.layout()
		topLeft(t, name+" with no height", noHeight)

		narrow := send(m, tea.WindowSizeMsg{Width: minContentWidth + 2*marginCols - 1, Height: 60})
		topLeft(t, name+" narrower than the content and its margins", narrow)
	}

	// Height: at every height, centred while the lines the block draws are
	// fewer than the terminal's rows, and top-left once they are as many or
	// more; the rows its screen reserves never decide it. A centred block is
	// centred on its reserve while the reserve is shorter than the terminal,
	// and on its own lines once it is not. Measured on a wide terminal, where
	// the two layouts also differ sideways.
	for name, m := range screens(t) {
		var sawTopLeft, sawCentred bool
		for height := 2; height <= 60; height++ {
			m := send(m, tea.WindowSizeMsg{Width: 200, Height: height})
			block, reserve, _ := m.screenBlock()
			lines := lineCount(block)
			where := name + " at 200x" + itoa(height)
			if lines >= height {
				sawTopLeft = true
				topLeft(t, where+", "+itoa(lines)+" rows tall", m)
				continue
			}
			rows := lines
			if reserve < height {
				rows = max(lines, reserve)
			}
			sawCentred = true
			view := m.View()
			if first, _ := blockRows(view); first != (height-rows)/2 {
				t.Errorf("%s starts on row %d, want %d for a %d-row block:\n%s", where, first, (height-rows)/2, rows, view)
			}
			if left := blockLeft(view); left != (200-m.contentWidth())/2 {
				t.Errorf("%s starts at column %d, want %d", where, left, (200-m.contentWidth())/2)
			}
		}
		if !sawTopLeft || !sawCentred {
			t.Errorf("%s: top-left seen %v, centred seen %v; the heights do not cross the boundary", name, sawTopLeft, sawCentred)
		}
	}
}

func TestAPathWiderThanTheContentStaysInTheTerminal(t *testing.T) {
	path := "/Users/somebody/Downloads/" + strings.Repeat("very long directory name/", 12) + "clip.mp4"
	for _, width := range []int{101, 120, 160, 200, 300} {
		m := downloadingModel(t, &fakes{})
		m = send(m, tea.WindowSizeMsg{Width: width, Height: 40})
		m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: path}})
		view := m.View()
		if got := widest(view); got > width {
			t.Errorf("at width %d the done screen is %d cells wide:\n%s", width, got, view)
		}
		row := lineContaining(view, "/Users/somebody")
		if row < 0 || !strings.Contains(strings.Split(view, "\n")[row], ellipsis) {
			t.Errorf("at width %d the path is not cut on its own row:\n%s", width, view)
		}
		if left := blockLeft(view); left != m.marginLeft() {
			t.Errorf("at width %d the path moved the block to column %d, want %d", width, left, m.marginLeft())
		}
	}
}

// --- the free space ---------------------------------------------------------

func TestFreeAreaCountsAndEnumeratesEveryFreeCell(t *testing.T) {
	areas := []freeArea{
		{},
		{height: 24, width: 80, top: 5, bottom: 19, left: 0, right: 80},
		{height: 60, width: 200, top: 20, bottom: 40, left: 50, right: 150},
		{height: 10, width: 30, top: 0, bottom: 10, left: 0, right: 30},
		{height: 10, width: 30, top: 0, bottom: 4, left: 3, right: 30},
		{height: 10, width: 30, top: 6, bottom: 10, left: 0, right: 29},
	}
	for _, a := range areas {
		var want [][2]int
		for r := range a.height {
			for c := range a.width {
				if a.free(r, c) {
					want = append(want, [2]int{r, c})
				}
			}
		}
		if a.cells() != len(want) {
			t.Errorf("%+v: cells() = %d, want %d", a, a.cells(), len(want))
			continue
		}
		for i, w := range want {
			if r, c := a.cell(i); r != w[0] || c != w[1] {
				t.Errorf("%+v: cell(%d) = %d,%d, want %d,%d", a, i, r, c, w[0], w[1])
				break
			}
		}
	}
}

// assertDecorationOnlyAround checks one frame of a screen with decoration
// against the same screen with none: the frame is no taller or wider than the
// terminal, and every cell in which the two differ holds one of glyphs and
// lies outside the block — off its rows, the row either side of them and the
// rows the static screen reserves below them, or two columns clear of its
// sides. It reports how many decorated cells it found, and how many of those
// were on the block's own rows.
func assertDecorationOnlyAround(t *testing.T, where, static, view string, width, height, reserve int, glyphs string) (marks, beside int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Fatalf("%s is %d rows in a %d-row terminal", where, len(lines), height)
	}
	if got := widest(view); got > width {
		t.Fatalf("%s is %d cells wide in a %d-column terminal", where, got, width)
	}
	first, last := blockRows(static)
	last = max(last, first+reserve-1)
	left := blockLeft(static)
	right := widest(static) // one past the end of the widest line
	sg, vg := cellGrid(static), cellGrid(view)
	for r := range height {
		for c := range width {
			s, v := cellAt(sg, r, c), cellAt(vg, r, c)
			if s == v {
				continue
			}
			if s != " " {
				t.Fatalf("%s drew %q over %q at row %d, column %d:\n%s", where, v, s, r, c, view)
			}
			if v == " " || !strings.Contains(glyphs, v) {
				t.Fatalf("%s has %q at row %d, column %d, where the static screen has nothing:\n%s", where, v, r, c, view)
			}
			onRows := r >= first-marginRows && r <= last+marginRows
			if onRows && c >= left-marginCols && c < right+marginCols {
				t.Fatalf("%s drew %q at row %d, column %d, inside the block's margin (rows %d-%d, columns %d-%d):\n%s",
					where, v, r, c, first, last, left, right-1, view)
			}
			marks++
			if onRows {
				beside++
			}
		}
	}
	return marks, beside
}
