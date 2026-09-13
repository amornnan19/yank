package ui

import "time"

// percentPulse brightens the percent label for a moment when a report carries
// the download across a quarter mark. Only the label's style changes: its
// text is percentLabel's on every frame. It rides the spinner's tick.
type percentPulse struct {
	cue  cue
	last float64
	// seen is whether last holds a known percentage. A download that starts
	// past a mark — a resumed one, or a screen shown mid-way — has crossed
	// nothing.
	seen bool
}

// pulseDuration is how long the label stays bright.
const pulseDuration = 400 * time.Millisecond

// pulseMarks are the percentages whose crossing pulses the label.
var pulseMarks = []float64{25, 50, 75}

func newPercentPulse() effect { return percentPulse{} }

func (e percentPulse) step(ev motionEvent) effect {
	pct, known := ev.dl.prog.Percent()
	switch ev.kind {
	case evShown:
		e = percentPulse{last: pct, seen: known}
	case evLeft:
		e = percentPulse{}
	case evReport, evUpdate:
		if !known {
			break
		}
		if e.seen {
			for _, mark := range pulseMarks {
				if e.last < mark && pct >= mark {
					e.cue.arm()
				}
			}
		}
		e.last, e.seen = pct, true
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= pulseDuration {
			e.cue.stop()
		}
	}
	return e
}

func (e percentPulse) busy() bool { return false }

func (e percentPulse) wake() time.Time { return time.Time{} }

func (e percentPulse) paint(f *downloadFrame) {
	if e.cue.waiting || (e.cue.running && e.cue.elapsed(f.now) < pulseDuration) {
		f.labelBright = true
	}
}
