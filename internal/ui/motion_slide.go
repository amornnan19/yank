package ui

import (
	"math"
	"time"
)

// phaseSlide brings a new line under the bar in from the right when the line
// changes — the stats giving way to merging, merging to converting, anything
// to the retry message and back. It is a short burst, too quick for the
// spinner's 10 fps, so it runs the motion clock while it plays and not a frame
// longer.
type phaseSlide struct {
	cue  cue
	line underLine
}

// slideDuration is the whole slide.
const slideDuration = 200 * time.Millisecond

func newPhaseSlide() effect { return phaseSlide{} }

func (e phaseSlide) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		// The line already on screen is where it belongs.
		e = phaseSlide{line: ev.dl.line}
	case evLeft:
		e = phaseSlide{}
	case evReport, evUpdate:
		if ev.dl.line != e.line {
			e.line = ev.dl.line
			e.cue.arm()
		}
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= slideDuration {
			e.cue.stop()
		}
	}
	return e
}

func (e phaseSlide) busy() bool { return e.cue.live() }

func (e phaseSlide) wake() time.Time { return time.Time{} }

func (e phaseSlide) paint(f *downloadFrame) {
	if !e.cue.live() {
		return
	}
	// Eased out: fast from the edge, settling into place. Until the first
	// tick the new line is still wholly past the edge.
	left := 1 - min(float64(e.cue.elapsed(f.now))/float64(slideDuration), 1)
	f.lineShift = int(math.Round(left * left * float64(f.cw)))
}
