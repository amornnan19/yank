package ui

import (
	"math"
	"time"

	"github.com/charmbracelet/harmonica"
)

// introDrop is the wordmark's entrance: the letters drop in one after another
// from above, bounce once on a spring, and show random shade cells until each
// one settles. It plays once a session, the first time the wordmark is shown
// with motion on, and enter cuts it short.
type introDrop struct {
	cue    cue
	played bool
	// scramble is the shade cell each drawn cell of each letter shows this
	// frame, drawn from the model's source on the tick. An array, so a copy
	// of the effect is a copy of it.
	scramble [introMaxLetters][introMaxRows][introMaxCols]rune
}

const (
	// introStagger is the delay between one letter starting to drop and the
	// next.
	introStagger = 110 * time.Millisecond
	// introSettle is how long one letter takes from starting to drop to being
	// drawn at rest. The spring has come to rest inside it; the cut to the
	// exact position at the end is the spring's last sub-cell of travel.
	introSettle = 300 * time.Millisecond

	introMaxLetters = 4
	introMaxRows    = 5
	introMaxCols    = 6
)

// introShades are the cells a letter is drawn in while it is still moving.
var introShades = []rune("░▒▓█")

// introSpring is the drop's spring: underdamped enough for one bounce.
var introSpring = harmonica.NewSpring(harmonica.FPS(30), 18, 0.35)

func newIntroDrop() effect { return introDrop{} }

// introDuration is the whole entrance: the last letter's start plus its
// settle.
func introDuration() time.Duration {
	return time.Duration(len(wordmarkLetters)-1)*introStagger + introSettle
}

func (e introDrop) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		if !e.played && !e.cue.live() {
			e.cue.arm()
		}
	case evSubmit:
		// Enter mid-intro skips straight to the exit.
		e.cue.stop()
		e.played = true
	case evTick:
		e.cue.settle(ev.now)
		if !e.cue.running {
			break
		}
		if e.cue.elapsed(ev.now) >= introDuration() {
			e.cue.stop()
			e.played = true
			break
		}
		for l := range e.scramble {
			for r := range e.scramble[l] {
				for c := range e.scramble[l][r] {
					e.scramble[l][r][c] = introShades[ev.rng.IntN(len(introShades))]
				}
			}
		}
	}
	return e
}

func (e introDrop) busy() bool { return e.cue.live() }

func (e introDrop) wake() time.Time { return time.Time{} }

// introOffset is how many rows below its resting place a letter t into its
// drop is drawn: negative while it is still above, positive on the bounce.
func introOffset(t time.Duration) int {
	pos, vel := -float64(len(wordmarkRows)), 0.0
	for range int(t / motionFrame) {
		pos, vel = introSpring.Update(pos, vel, 0)
	}
	return int(math.Round(pos))
}

func (e introDrop) paint(f *inputFrame) {
	if !e.cue.live() {
		return
	}
	elapsed := e.cue.elapsed(f.now)
	rows := len(f.wordmark)
	for l, span := range wordmarkLetters {
		t := elapsed - time.Duration(l)*introStagger
		if e.cue.running && t >= introSettle {
			continue
		}
		// Lift the letter off the canvas, then draw it where it has got to.
		// Rows that fall outside the drawing are clipped: a dropping letter
		// never adds a row to the screen.
		for r := range rows {
			for c := span[0]; c < span[1] && c < len(f.wordmark[r]); c++ {
				f.wordmark[r][c] = cell{r: ' '}
			}
		}
		if e.cue.waiting || t < 0 {
			continue
		}
		off := introOffset(t)
		for r, row := range wordmarkRows {
			dst := r + off
			if dst < 0 || dst >= rows {
				continue
			}
			rs := []rune(row)
			for c := span[0]; c < span[1] && c < len(rs) && c < len(f.wordmark[dst]); c++ {
				if rs[c] == ' ' {
					continue
				}
				shade := rs[c]
				if l < introMaxLetters && r < introMaxRows && c-span[0] < introMaxCols {
					if s := e.scramble[l][r][c-span[0]]; s != 0 {
						shade = s
					}
				}
				f.wordmark[dst][c] = cell{r: shade}
			}
		}
	}
}
