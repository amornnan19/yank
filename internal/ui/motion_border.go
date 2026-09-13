package ui

import "time"

// breathingBorder steps the URL box's border between cyan and bright cyan,
// slowly, while the box is empty. The first character typed stops it and the
// border is cyan again; emptying the box starts it over.
type breathingBorder struct {
	cue  cue
	last time.Time
}

// breathStep is how long the border holds each of its two colours.
const breathStep = 1200 * time.Millisecond

func newBreathingBorder() effect { return breathingBorder{} }

func (e breathingBorder) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown, evEdit:
		switch {
		case ev.value != "":
			e.cue.stop()
		case !e.cue.live():
			e.cue.arm()
		}
	case evTick:
		e.cue.settle(ev.now)
		e.last = ev.now
	}
	return e
}

func (e breathingBorder) busy() bool { return e.cue.waiting }

// wake is the next change of colour.
func (e breathingBorder) wake() time.Time {
	if !e.cue.running {
		return time.Time{}
	}
	n := e.cue.elapsed(e.last) / breathStep
	return e.cue.at.Add((n + 1) * breathStep)
}

func (e breathingBorder) paint(f *inputFrame) {
	if !e.cue.running {
		return
	}
	if (e.cue.elapsed(f.now)/breathStep)%2 == 1 {
		f.border = ansiBrightCyan
	} else {
		f.border = ansiCyan
	}
}
