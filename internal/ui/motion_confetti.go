package ui

import "time"

// confetti is a one-shot burst of pieces falling through the free rows below
// the done screen's legend, and then gone. Only on a Saved screen: an Already
// there screen downloaded nothing. It plays once for each outcome, and not at
// all on a terminal with no free rows to fall through.
type confetti struct {
	cue    cue
	seq    int
	played bool
	rows   int
	pieces [confettiCount]confettiPiece
}

// confettiPiece is one piece: where it starts, as a fraction of the free rows
// (negative is above them), how many of the rows' heights it falls a second,
// and what it looks like.
type confettiPiece struct {
	y0, speed float64
	col       int
	glyph     rune
	colour    string
}

const (
	confettiCount    = 24
	confettiDuration = 1200 * time.Millisecond
)

var (
	confettiGlyphs = []rune{'✦', '*', '·', '˙'}
	// confettiColours are the palette's colours that read on a light
	// background and a dark one alike: the six hues, plain and bright.
	confettiColours = []string{"1", "2", "3", "4", "5", "6", "9", "10", "11", "12", "13", "14"}
)

func newConfetti() effect { return confetti{} }

func (e confetti) step(ev motionEvent) effect {
	e.rows = ev.freeRows
	switch ev.kind {
	case evShown:
		if e.seq == ev.done.seq && (e.played || e.cue.live()) {
			break
		}
		e = confetti{seq: ev.done.seq, rows: ev.freeRows}
		if !ev.done.already {
			e.cue.arm()
		}
	case evLayout:
		if e.rows < 1 && e.cue.live() {
			// Nowhere left to fall: nothing it did could be seen.
			e.cue.stop()
			e.played = true
		}
	case evTick:
		if e.cue.waiting {
			if e.rows < 1 {
				e.cue.stop()
				e.played = true
				break
			}
			e.cue.settle(ev.now)
			for i := range e.pieces {
				e.pieces[i] = confettiPiece{
					y0:     -0.6 + 0.8*ev.rng.Float64(),
					speed:  0.7 + 0.8*ev.rng.Float64(),
					col:    ev.rng.IntN(1 << 16),
					glyph:  confettiGlyphs[ev.rng.IntN(len(confettiGlyphs))],
					colour: confettiColours[ev.rng.IntN(len(confettiColours))],
				}
			}
			break
		}
		if e.cue.running && e.cue.elapsed(ev.now) >= confettiDuration {
			e.cue.stop()
			e.played = true
		}
	}
	return e
}

func (e confetti) busy() bool { return e.cue.live() }

func (e confetti) wake() time.Time { return time.Time{} }

func (e confetti) paint(f *doneFrame) {
	if !e.cue.running {
		return
	}
	t := e.cue.elapsed(f.now)
	if t >= confettiDuration {
		return
	}
	for _, p := range e.pieces {
		f.confetti = append(f.confetti, confettiMark{
			y:      p.y0 + p.speed*t.Seconds(),
			col:    p.col,
			glyph:  p.glyph,
			colour: p.colour,
		})
	}
}
