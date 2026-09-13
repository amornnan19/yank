package ui

import "time"

// barShine is a bright band gliding along the filled part of the progress bar
// every couple of seconds, like a macOS progress bar. It is drawn over the
// bar's cells and nothing else: it never re-aims the bar, so the spring and
// its frames are exactly as they would be without it.
//
// It rides the spinner's tick. At 10 fps the band moves shineSpeed/10 cells a
// frame, which its width covers, so it glides rather than hops.
type barShine struct {
	cue cue
}

const (
	// shinePeriod is the time from one pass's start to the next's.
	shinePeriod = 2 * time.Second
	// shineSpeed is how fast the band travels, in cells a second. A pass
	// along the widest bar the content allows ends inside the period.
	shineSpeed = 60
)

// shineBand is the band, left to right: a glint either side of a bright core.
var shineBand = []ink{inkGlint, inkBright, inkBright, inkBright, inkBright, inkGlint}

func newBarShine() effect { return barShine{} }

func (e barShine) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		e = barShine{}
		e.cue.arm()
	case evLeft:
		e = barShine{}
	case evTick:
		e.cue.settle(ev.now)
	}
	return e
}

func (e barShine) busy() bool { return false }

func (e barShine) wake() time.Time { return time.Time{} }

func (e barShine) paint(f *downloadFrame) {
	if !e.cue.running || f.filled < 1 {
		return
	}
	into := e.cue.elapsed(f.now) % shinePeriod
	lead := int(into.Seconds()*shineSpeed) - len(shineBand)
	for i, k := range shineBand {
		if c := lead + i; c >= 0 && c < f.filled && c < len(f.bar) {
			f.bar[c].ink = k
		}
	}
}
