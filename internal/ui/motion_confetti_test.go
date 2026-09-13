package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestConfettiFallsOnceOnSavedOnly(t *testing.T) {
	already := rig(newConfetti)
	already.mo.freeCells = 8
	already.done = doneFacts{seq: 1, already: true}
	already.send(evShown)
	if !already.idle() {
		t.Errorf("confetti on an Already there screen")
	}

	r := rig(newConfetti)
	r.mo.freeCells = 8
	r.done = doneFacts{seq: 1}
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown on Saved, the confetti is not waiting")
	}
	r.tickAt(0)
	first := r.mo.paintDone(doneFrame{}).confetti
	if len(first) != confettiCount {
		t.Fatalf("the burst has %d pieces, want %d", len(first), confettiCount)
	}
	prev := first
	for at := motionFrame; at < confettiDuration; at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("at %v the confetti stopped", at)
		}
		cur := r.mo.paintDone(doneFrame{}).confetti
		for i := range cur {
			if cur[i].y <= prev[i].y || cur[i].col != prev[i].col || cur[i].glyph != prev[i].glyph {
				t.Fatalf("at %v piece %d did not fall straight down: %+v after %+v", at, i, cur[i], prev[i])
			}
		}
		prev = cur
	}
	for _, p := range prev {
		if !strings.ContainsRune(string(confettiGlyphs), p.glyph) || !contains(confettiColours, p.colour) {
			t.Errorf("a piece is %q in colour %q", p.glyph, p.colour)
		}
	}
	r.tickAt(confettiDuration)
	if len(r.mo.paintDone(doneFrame{}).confetti) != 0 || !r.idle() {
		t.Errorf("after %v the confetti is still falling", confettiDuration)
	}
	r.send(evShown)
	if !r.idle() {
		t.Errorf("the confetti fell again for the same outcome")
	}

	// No row to fall through: it stops at its first tick.
	none := rig(newConfetti)
	none.done = doneFacts{seq: 1}
	none.send(evShown)
	none.tickAt(0)
	if !none.idle() {
		t.Errorf("with no free rows the confetti is still running")
	}

	// The seed decides the burst.
	burst := func(seed uint64) []confettiMark {
		r := &effectRig{mo: newMotionOf([]effect{newConfetti()}, seed), done: doneFacts{seq: 1}}
		r.mo.freeCells = 8
		r.send(evShown)
		r.tickAt(0)
		return r.mo.paintDone(doneFrame{}).confetti
	}
	a, b, c := burst(3), burst(3), burst(4)
	if !equalMarks(a, b) || equalMarks(a, c) {
		t.Errorf("the burst is not decided by the seed: same seed equal %v, other seed equal %v", equalMarks(a, b), equalMarks(a, c))
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func equalMarks(a, b []confettiMark) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestConfettiOnlyFallsThroughEmptyCellsTheTerminalHas(t *testing.T) {
	trueColour(t)
	for _, size := range [][2]int{{80, 12}, {80, 14}, {80, 16}, {80, 24}, {80, 40}, {120, 40}, {200, 60}} {
		width, height := size[0], size[1]
		at := itoa(width) + "x" + itoa(height)
		m, _ := downloadMotion(t, "Me at the zoo", width, height, newMotionOf([]effect{newConfetti()}, 1))
		res := &ytdlp.DownloadResult{Path: "/Users/x/Downloads/Me at the zoo.mp4"}
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})
		static := staticView(m)
		// The rows kept clear: the reserve where the terminal is taller than
		// it, and the block's own lines where it is not.
		reserve := m.placement(m.screenBlockSize()).rows
		seen, seenBeside := 0, 0
		m = play(t, m, 0, 3*time.Second, func(m Model) {
			view := m.View()
			for n, line := range strings.Split(view, "\n") {
				assertLineIsPaletteSafe(t, "confetti at "+clock(m).String(), width, n, line)
			}
			marks, beside := assertDecorationOnlyAround(t, "the confetti at "+at+" "+clock(m).String(),
				static, view, width, height, reserve, string(confettiGlyphs))
			seen += marks
			seenBeside += beside
		})
		if first, last := blockRows(static); height-(last-first+1) >= 8 && seen == 0 {
			t.Errorf("at %s no confetti was ever drawn", at)
		}
		if width >= 120 && seenBeside == 0 {
			t.Errorf("at %s no confetti was ever drawn beside the block", at)
		}
		if m.motion.pending != tickNone {
			t.Errorf("at %s the confetti left a %v tick", at, m.motion.pending)
		}
		// enter and q act mid-fall.
		m, _ = downloadMotion(t, "Me at the zoo", width, height, newMotionOf([]effect{newConfetti()}, 1))
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})
		m = play(t, m, 0, 600*time.Millisecond, nil)
		if enter := send(m, keyOf(tea.KeyEnter)); enter.state != stateInput {
			t.Errorf("at %s enter mid-fall went to %v", at, enter.state)
		}
		if _, cmd := step(m, runes("q")); !quitsNow(cmd) {
			t.Errorf("at %s q mid-fall did not quit", at)
		}
	}
}

func TestConfettiPlaysOnATerminalShorterThanTheDoneScreenReserves(t *testing.T) {
	trueColour(t)
	for _, size := range [][2]int{{80, 16}, {21, 24}} {
		width, height := size[0], size[1]
		at := itoa(width) + "x" + itoa(height)
		m, _ := downloadMotion(t, "Me at the zoo", width, height, newMotionOf([]effect{newConfetti()}, 1))
		res := &ytdlp.DownloadResult{Path: "/Users/x/Downloads/Me at the zoo.mp4"}
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})

		// The terminal is too short for the tallest done screen, but not for
		// this one: the case the reserve must not push out of centring.
		static := staticView(m)
		block, reserve, _ := m.screenBlock()
		if reserve < height || lineCount(block) >= height {
			t.Fatalf("at %s the block is %d rows and reserves %d: not a screen that fits under a reserve that does not", at, lineCount(block), reserve)
		}
		p := m.placement(block, reserve)
		if !p.centred {
			t.Errorf("at %s a %d-row Saved screen is drawn top-left", at, lineCount(block))
		}
		if n := m.freeArea(p).cells(); n < 1 {
			t.Errorf("at %s a centred Saved screen leaves %d free cells", at, n)
		}

		seen := 0
		play(t, m, 0, 3*time.Second, func(m Model) {
			marks, _ := assertDecorationOnlyAround(t, "the confetti at "+at+" "+clock(m).String(),
				static, m.View(), width, height, 0, string(confettiGlyphs))
			seen += marks
		})
		if seen == 0 {
			t.Errorf("at %s no confetti was ever drawn", at)
		}
	}
}
