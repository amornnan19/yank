package ui

import "time"

// shimmer is a bright band crossing the wordmark every few seconds while the
// screen sits idle. It stops on enter and starts its cadence over when the
// screen is shown again.
type shimmer struct {
	cue cue
	// pass is when the next (or current) crossing starts; zero until the cue
	// has learned the time.
	pass time.Time
	// last is the latest tick the shimmer has seen.
	last time.Time
}

const (
	// shimmerPeriod is the time from one crossing's start to the next's, and
	// from the screen being shown to the first.
	shimmerPeriod = 4 * time.Second
	// shimmerCross is how long one crossing takes.
	shimmerCross = 600 * time.Millisecond
	// shimmerBand is the band's width in cells.
	shimmerBand = 3
)

func newShimmer() effect { return shimmer{} }

func (e shimmer) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		e = shimmer{}
		e.cue.arm()
	case evSubmit:
		e = shimmer{}
	case evTick:
		if e.cue.waiting {
			e.cue.settle(ev.now)
			e.pass = ev.now.Add(shimmerPeriod)
		}
		e.last = ev.now
		for e.cue.running && !ev.now.Before(e.pass.Add(shimmerCross)) {
			e.pass = e.pass.Add(shimmerPeriod)
		}
	}
	return e
}

// crossing reports whether a crossing is under way at now.
func (e shimmer) crossing(now time.Time) bool {
	return e.cue.running && !now.Before(e.pass) && now.Before(e.pass.Add(shimmerCross))
}

func (e shimmer) busy() bool { return e.cue.waiting || e.crossing(e.last) }

func (e shimmer) wake() time.Time {
	if !e.cue.running {
		return time.Time{}
	}
	return e.pass
}

func (e shimmer) paint(f *inputFrame) {
	if !e.crossing(f.now) || len(f.wordmark) == 0 {
		return
	}
	width := len(f.wordmark[0])
	travel := width + shimmerBand
	lead := int(f.now.Sub(e.pass)*time.Duration(travel)/shimmerCross) - shimmerBand
	for _, row := range f.wordmark {
		for c := max(lead, 0); c < lead+shimmerBand && c < len(row); c++ {
			if row[c].r != ' ' {
				row[c].ink = inkBright
			}
		}
	}
}
