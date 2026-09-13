package ui

import "time"

// revealSweep is one bright bar sweeping left to right across the wordmark as
// the intro's last letter settles. It plays once a session, and enter cancels
// it.
//
// It keeps its own delay rather than reading the intro's state: sweepDelay is
// timed to the intro's length, and with the intro removed the sweep simply
// plays on its own after the same pause.
type revealSweep struct {
	cue    cue
	played bool
	last   time.Time
}

const (
	// sweepDelay is when the sweep starts after the screen is first shown:
	// as the intro's last letter comes to rest.
	sweepDelay = 630 * time.Millisecond
	// sweepCross is how long the bar takes to cross.
	sweepCross = 360 * time.Millisecond
	// sweepGlyph is the bar.
	sweepGlyph = '▌'
)

func newRevealSweep() effect { return revealSweep{} }

func (e revealSweep) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		if !e.played && !e.cue.live() {
			e.cue.arm()
		}
	case evSubmit:
		e.cue.stop()
		e.played = true
	case evTick:
		e.cue.settle(ev.now)
		e.last = ev.now
		if e.cue.running && e.cue.elapsed(ev.now) >= sweepDelay+sweepCross {
			e.cue.stop()
			e.played = true
		}
	}
	return e
}

func (e revealSweep) busy() bool {
	return e.cue.waiting || (e.cue.running && e.cue.elapsed(e.last) >= sweepDelay)
}

// wake is the end of the pause before the bar starts, which is not motion and
// is not worth a frame tick of its own.
func (e revealSweep) wake() time.Time {
	if e.cue.running && e.cue.elapsed(e.last) < sweepDelay {
		return e.cue.at.Add(sweepDelay)
	}
	return time.Time{}
}

func (e revealSweep) paint(f *inputFrame) {
	if !e.cue.running || len(f.wordmark) == 0 {
		return
	}
	t := e.cue.elapsed(f.now) - sweepDelay
	if t < 0 || t >= sweepCross {
		return
	}
	width := len(f.wordmark[0])
	col := int(t * time.Duration(width) / sweepCross)
	for _, row := range f.wordmark {
		if col < len(row) {
			row[col] = cell{r: sweepGlyph, ink: inkBright}
		}
	}
}
