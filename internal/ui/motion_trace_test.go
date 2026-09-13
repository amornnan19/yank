package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestBorderTraceDrawsTheBoxClockwise(t *testing.T) {
	r := rig(newBorderTrace)
	r.done = doneFacts{seq: 3}
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown, the trace is not waiting for its first tick")
	}
	trace := func() float64 { return r.mo.paintDone(doneFrame{trace: 1}).trace }
	if trace() != 0 {
		t.Errorf("before the first tick %v of the border is drawn", trace())
	}
	last := -1.0
	for at := time.Duration(0); at < traceDuration; at += motionFrame {
		r.tickAt(at)
		if got := trace(); got < last || got >= 1 {
			t.Errorf("at %v the trace is %v after %v", at, got, last)
		} else {
			last = got
		}
	}
	r.tickAt(traceDuration)
	if trace() != 1 || !r.idle() {
		t.Errorf("after %v the trace is %v and idle %v", traceDuration, trace(), r.idle())
	}
	r.send(evShown)
	if !r.idle() || trace() != 1 {
		t.Errorf("the trace played again for the same outcome")
	}
}

func TestTraceBoxIsThePathBoxWithCellsMissing(t *testing.T) {
	trueColour(t)
	m, _ := downloadMotion(t, "x", 80, 24, motion{})
	const path = "/Users/x/Downloads/Me at the zoo.mp4"
	static := strings.Split(sgr.ReplaceAllString(m.pathView(path), ""), "\n")
	cw := m.contentWidth()
	inner := cw - boxOverhead
	perimeter := boxPerimeter(inner)

	whole := strings.Split(sgr.ReplaceAllString(traceBox(path, inner, perimeter), ""), "\n")
	if !equalLines(whole, static) {
		t.Fatalf("the whole trace is not pathView's box:\n%s\n--- want ---\n%s", strings.Join(whole, "\n"), strings.Join(static, "\n"))
	}
	drawn := func(lines []string) int {
		n := 0
		for _, l := range lines {
			n += strings.Count(l, "─") + strings.Count(l, "│") + strings.Count(l, "╭") + strings.Count(l, "╮") + strings.Count(l, "╰") + strings.Count(l, "╯")
		}
		return n
	}
	for cells := 0; cells <= perimeter; cells++ {
		rendered := traceBox(path, inner, cells)
		lines := strings.Split(sgr.ReplaceAllString(rendered, ""), "\n")
		if len(lines) != 3 {
			t.Fatalf("%d cells: the box is %d rows", cells, len(lines))
		}
		for n, l := range lines {
			if lipgloss.Width(l) != lipgloss.Width(static[n]) {
				t.Fatalf("%d cells: row %d is %d wide, want %d", cells, n, lipgloss.Width(l), lipgloss.Width(static[n]))
			}
		}
		if !strings.Contains(lines[1], path) {
			t.Fatalf("%d cells: the path is not in the box: %q", cells, lines[1])
		}
		if got := drawn(lines); got != cells {
			t.Fatalf("%d cells: %d border cells drawn", cells, got)
		}
		for n, l := range strings.Split(rendered, "\n") {
			assertLineIsPaletteSafe(t, "trace", 80, n, l)
		}
	}
	// Clockwise from the top-left: the corner first, the top-right corner
	// before the right side, the bottom-right before the bottom-left, and the
	// left side last.
	order := []struct {
		cells int
		row   int
		glyph string
	}{{1, 0, "╭"}, {inner + 4, 0, "╮"}, {inner + 5, 1, "│"}, {inner + 6, 2, "╯"}, {2*(inner+4) + 1, 2, "╰"}}
	for _, o := range order {
		before := strings.Split(sgr.ReplaceAllString(traceBox(path, inner, o.cells-1), ""), "\n")
		after := strings.Split(sgr.ReplaceAllString(traceBox(path, inner, o.cells), ""), "\n")
		if strings.Count(after[o.row], o.glyph)-strings.Count(before[o.row], o.glyph) != 1 {
			t.Errorf("cell %d did not draw %s on row %d:\n%s\n%s", o.cells, o.glyph, o.row, strings.Join(before, "\n"), strings.Join(after, "\n"))
		}
	}
	last := strings.Split(sgr.ReplaceAllString(traceBox(path, inner, perimeter-1), ""), "\n")
	if strings.HasPrefix(last[1], "│") {
		t.Errorf("the left side was drawn before the last cell")
	}
}
