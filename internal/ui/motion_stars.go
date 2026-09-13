package ui

import "time"

// starfield twinkles faint dots in the empty rows below the legend, on a
// terminal tall enough to have any. Each star steps through off, ·, ˙, · and off
// again at its own random pace, and moves somewhere new while it is off. Nothing
// here is a frame animation: every change is a single wake.
type starfield struct {
	cue   cue
	stars [starCount]star
	// rows is the rows the screen leaves for stars, from the latest event.
	// With none, nothing the stars do can be seen, so nothing is scheduled.
	rows int
}

// star is one dot. row and col are raw draws; View maps them onto the rows and
// columns the frame has.
type star struct {
	row, col int
	phase    int
	due      time.Time
}

const starCount = 14

// starPhases is the glyph for each phase of a twinkle; 0 is a star that is off.
var starPhases = []rune{0, '·', '˙', '·'}

const (
	starMinHold = 350 * time.Millisecond
	starMaxHold = 1800 * time.Millisecond
)

func newStarfield() effect { return starfield{} }

func (e starfield) step(ev motionEvent) effect {
	e.rows = ev.freeRows
	switch ev.kind {
	case evShown:
		if !e.cue.live() {
			e.cue.arm()
		}
	case evTick:
		if e.cue.waiting {
			e.cue.settle(ev.now)
			for i := range e.stars {
				e.stars[i] = star{
					row:   ev.rng.IntN(1 << 16),
					col:   ev.rng.IntN(1 << 16),
					phase: ev.rng.IntN(len(starPhases)),
					due:   ev.now.Add(starHold(ev)),
				}
			}
			break
		}
		for i := range e.stars {
			s := &e.stars[i]
			if ev.now.Before(s.due) {
				continue
			}
			s.phase = (s.phase + 1) % len(starPhases)
			if s.phase == 0 {
				s.row, s.col = ev.rng.IntN(1<<16), ev.rng.IntN(1<<16)
			}
			s.due = ev.now.Add(starHold(ev))
		}
	}
	return e
}

// starHold is a random time for a star to hold its phase.
func starHold(ev motionEvent) time.Duration {
	return starMinHold + time.Duration(ev.rng.Int64N(int64(starMaxHold-starMinHold)))
}

func (e starfield) busy() bool { return e.cue.waiting }

// wake is the next star due to change, or never while there is no row to draw
// one in. A layout that makes room brings the wake back, already due.
func (e starfield) wake() time.Time {
	if !e.cue.running || e.rows < 1 {
		return time.Time{}
	}
	var next time.Time
	for _, s := range e.stars {
		if next.IsZero() || s.due.Before(next) {
			next = s.due
		}
	}
	return next
}

func (e starfield) paint(f *inputFrame) {
	if !e.cue.running {
		return
	}
	for _, s := range e.stars {
		if g := starPhases[s.phase]; g != 0 {
			f.stars = append(f.stars, starMark{row: s.row, col: s.col, glyph: g})
		}
	}
}
