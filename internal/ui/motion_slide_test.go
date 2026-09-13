package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestPhaseSlideBringsANewLineInFromTheRight(t *testing.T) {
	const cw = 60
	r := rig(newPhaseSlide)
	r.send(evShown)
	shift := func() int { return r.mo.paintDownload(downloadFrame{cw: cw}).lineShift }
	if shift() != 0 || !r.idle() {
		t.Fatalf("the line on show when the screen appeared slid in")
	}
	r.send(evUpdate)
	if !r.idle() {
		t.Fatalf("an update that did not change the line started a slide")
	}

	r.dl.line = lineMerging
	r.send(evReport)
	if !r.effect().busy() {
		t.Fatalf("the line changed and the slide wants no frames")
	}
	if shift() != cw {
		t.Errorf("before the first tick the new line is shifted %d, want it wholly past the edge at %d", shift(), cw)
	}
	r.tickAt(0)
	last := cw + 1
	for at := time.Duration(0); at < slideDuration; at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("at %v the slide stopped wanting frames", at)
		}
		s := shift()
		if s >= last && s != 0 {
			t.Errorf("at %v the line did not move left: %d after %d", at, s, last)
		}
		last = s
	}
	r.tickAt(slideDuration)
	if shift() != 0 || !r.idle() {
		t.Errorf("after %v the line is shifted %d and the slide busy %v", slideDuration, shift(), r.effect().busy())
	}
	if slideDuration > 250*time.Millisecond {
		t.Errorf("the slide takes %v, want about 200ms", slideDuration)
	}

	// Into the retry message, and back.
	for _, line := range []underLine{lineRetrying, lineStats} {
		r.dl.line = line
		r.send(evUpdate)
		if !r.effect().busy() {
			t.Errorf("a change to line %v did not slide", line)
		}
		r.tickAt(time.Second)
		r.tickAt(time.Second + slideDuration)
	}
	r.dl.line = lineConverting
	r.send(evUpdate)
	r.send(evLeft)
	if !r.idle() {
		t.Errorf("leaving the screen mid-slide did not stop it")
	}
}

func TestTheSlidingLineIsCutAtTheEdge(t *testing.T) {
	trueColour(t)
	for _, width := range []int{80, 24} {
		m, _ := downloadMotion(t, "Me at the zoo", width, 24, newMotionOf([]effect{newPhaseSlide()}, 1))
		m = send(m, speedReport(m, 40, 1_000_000))
		m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{Phase: ytdlp.PhaseMerging}})
		seen := map[int]bool{}
		m = play(t, m, 0, time.Second, func(m Model) {
			f := m.downloadFrame()
			line := m.animatedUnderLine(f)
			seen[f.lineShift] = true
			if w := lipgloss.Width(line); w > m.contentWidth() {
				t.Errorf("at width %d, shift %d the line is %d cells: %q", width, f.lineShift, w, line)
			}
			for n, l := range strings.Split(m.View(), "\n") {
				assertLineIsPaletteSafe(t, "slide", width, n, l)
			}
			if f.lineShift > 0 && f.lineShift < m.contentWidth()-4 {
				if plain := sgr.ReplaceAllString(line, ""); !strings.HasPrefix(plain, strings.Repeat(" ", f.lineShift)) || strings.TrimSpace(plain) == "" {
					t.Errorf("at width %d, shift %d the line is %q", width, f.lineShift, plain)
				}
			}
		})
		if len(seen) < 3 || !seen[0] {
			t.Errorf("at width %d the slide went through shifts %v", width, seen)
		}
		if m.motion.pending != tickNone {
			t.Errorf("at width %d the slide left a %v tick outstanding", width, m.motion.pending)
		}
	}
}
