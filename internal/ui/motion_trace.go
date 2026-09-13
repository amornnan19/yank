package ui

import "time"

// borderTrace draws the box around the saved path clockwise from its top-left
// corner, and then leaves it drawn. It plays once for each outcome.
type borderTrace struct {
	cue    cue
	seq    int
	played bool
}

// traceDuration is the whole trace.
const traceDuration = 400 * time.Millisecond

func newBorderTrace() effect { return borderTrace{} }

func (e borderTrace) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		if e.seq == ev.done.seq && (e.played || e.cue.live()) {
			break
		}
		e = borderTrace{seq: ev.done.seq}
		e.cue.arm()
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= traceDuration {
			e.cue.stop()
			e.played = true
		}
	}
	return e
}

func (e borderTrace) busy() bool { return e.cue.live() }

func (e borderTrace) wake() time.Time { return time.Time{} }

func (e borderTrace) paint(f *doneFrame) {
	if !e.cue.live() {
		return
	}
	f.trace = min(float64(e.cue.elapsed(f.now))/float64(traceDuration), 1)
}
