package ui

import (
	"math"
	"time"

	"github.com/charmbracelet/harmonica"
)

// flinch is the wordmark reacting to enter: the last letter, the k, jerks to
// the right and springs back.
type flinch struct {
	cue cue
}

const (
	// flinchJerk is how many cells right the k is thrown.
	flinchJerk = 2
	// flinchDuration is the jerk and the spring back to rest.
	flinchDuration = 260 * time.Millisecond
)

// flinchSpring pulls the k back, with a small overshoot.
var flinchSpring = harmonica.NewSpring(harmonica.FPS(30), 18, 0.35)

func newFlinch() effect { return flinch{} }

func (e flinch) step(ev motionEvent) effect {
	switch ev.kind {
	case evSubmit:
		e.cue.arm()
	case evShown:
		e.cue.stop()
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= flinchDuration {
			e.cue.stop()
		}
	}
	return e
}

func (e flinch) busy() bool { return e.cue.live() }

func (e flinch) wake() time.Time { return time.Time{} }

// flinchOffset is how many cells right of its place the k is drawn t into the
// flinch.
func flinchOffset(t time.Duration) int {
	pos, vel := float64(flinchJerk), 0.0
	for range int(t / motionFrame) {
		pos, vel = flinchSpring.Update(pos, vel, 0)
	}
	return int(math.Round(pos))
}

func (e flinch) paint(f *inputFrame) {
	if !e.cue.live() || len(wordmarkLetters) == 0 {
		return
	}
	off := flinchOffset(e.cue.elapsed(f.now))
	if off == 0 {
		return
	}
	span := wordmarkLetters[len(wordmarkLetters)-1]
	for r, row := range f.wordmark {
		moved := make([]cell, 0, span[1]-span[0])
		for c := span[0]; c < span[1] && c < len(row); c++ {
			moved = append(moved, row[c])
			row[c] = cell{r: ' '}
		}
		// Cells pushed past the canvas are clipped, so the jerk never
		// widens the line.
		for i, cl := range moved {
			if c := span[0] + i + off; c >= 0 && c < len(row) && cl.r != ' ' {
				f.wordmark[r][c] = cl
			}
		}
	}
}
