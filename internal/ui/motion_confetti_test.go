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
	already.mo.freeRows = 8
	already.done = doneFacts{seq: 1, already: true}
	already.send(evShown)
	if !already.idle() {
		t.Errorf("confetti on an Already there screen")
	}

	r := rig(newConfetti)
	r.mo.freeRows = 8
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
		r.mo.freeRows = 8
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

func TestConfettiOnlyFallsThroughEmptyRowsTheTerminalHas(t *testing.T) {
	trueColour(t)
	for _, height := range []int{12, 14, 16, 24, 40} {
		m, _ := downloadMotion(t, "Me at the zoo", 80, height, newMotionOf([]effect{newConfetti()}, 1))
		res := &ytdlp.DownloadResult{Path: "/Users/x/Downloads/Me at the zoo.mp4"}
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})
		staticLines := strings.Split(staticView(m), "\n")
		content := len(staticLines) - 1
		midFall := false
		m = play(t, m, 0, 3*time.Second, func(m Model) {
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) > max(height, len(staticLines)) {
				t.Fatalf("at height %d the confetti made the screen %d rows", height, len(lines))
			}
			for i := 0; i < content && i < len(lines); i++ {
				if lines[i] != staticLines[i] {
					t.Fatalf("at height %d row %d changed:\n%q\nwant\n%q", height, i, lines[i], staticLines[i])
				}
			}
			if len(lines) > content && strings.TrimSpace(lines[content]) != "" {
				t.Fatalf("at height %d the row under the legend holds %q", height, lines[content])
			}
			for n, line := range lines {
				assertLineIsPaletteSafe(t, "confetti at "+clock(m).String(), 80, n, line)
			}
			pieces := 0
			for _, line := range lines[min(content+1, len(lines)):] {
				pieces += len(strings.TrimSpace(sgr.ReplaceAllString(line, "")))
			}
			midFall = midFall || pieces > 0
		})
		if room := height - len(staticLines); room >= 3 && !midFall {
			t.Errorf("at height %d no confetti was ever drawn", height)
		}
		if m.motion.pending != tickNone {
			t.Errorf("at height %d the confetti left a %v tick", height, m.motion.pending)
		}
		// enter and q act mid-fall.
		m, _ = downloadMotion(t, "Me at the zoo", 80, height, newMotionOf([]effect{newConfetti()}, 1))
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})
		m = play(t, m, 0, 600*time.Millisecond, nil)
		if enter := send(m, keyOf(tea.KeyEnter)); enter.state != stateInput {
			t.Errorf("at height %d enter mid-fall went to %v", height, enter.state)
		}
		if _, cmd := step(m, runes("q")); !quitsNow(cmd) {
			t.Errorf("at height %d q mid-fall did not quit", height)
		}
	}
}
