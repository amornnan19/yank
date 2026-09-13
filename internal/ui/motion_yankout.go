package ui

import (
	"strings"
	"time"
)

// yankOut is the exit on enter: the URL is pulled left out of the box, its
// leading characters dropping away, and then the probing screen appears.
//
// It holds the screen, it does not hold the work. The model is already on the
// probing screen with the probe running when this starts; the frames only
// decide what View draws for yankDuration, and any message that moves the
// model off the probing screen ends them.
type yankOut struct {
	cue  cue
	text string
}

// yankDuration is the whole exit.
const yankDuration = 240 * time.Millisecond

// yankDebris is what the characters at the left edge turn into as they go,
// nearest the edge first.
var yankDebris = []rune{'.', ','}

func newYankOut() effect { return yankOut{} }

func (e yankOut) step(ev motionEvent) effect {
	switch ev.kind {
	case evSubmit:
		e.text = strings.TrimSpace(ev.value)
		e.cue.arm()
	case evShown:
		e = yankOut{}
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= yankDuration {
			e = yankOut{}
		}
	}
	return e
}

func (e yankOut) busy() bool { return e.cue.live() }

func (e yankOut) wake() time.Time { return time.Time{} }

func (e yankOut) holds() bool { return e.cue.live() }

func (e yankOut) paint(f *inputFrame) {
	if !e.cue.live() {
		return
	}
	// Sanitised before it is measured or cut, like every other line.
	rs := []rune(sanitise(e.text))
	p := float64(e.cue.elapsed(f.now)) / float64(yankDuration)
	shift := min(int(p*p*float64(len(rs)+1)), len(rs))
	rest := rs[shift:]
	if shift > 0 {
		rest = append([]rune(nil), rest...)
		for i := 0; i < len(yankDebris) && i < len(rest); i++ {
			rest[i] = yankDebris[i]
		}
	}
	f.boxText = truncate(string(rest), f.boxWidth)
	f.boxTextSet = true
}
