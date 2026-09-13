package ui

import "time"

// alreadyNudge brightens the "already there" note once, from faint to normal
// and back, so the eye finds the explanation for a screen that did not say
// Saved. Only on that screen, and once for each outcome.
type alreadyNudge struct {
	cue    cue
	seq    int
	played bool
}

const (
	// nudgeDelay lets the title arrive first; nudgeDuration is the nudge
	// itself, of which the middle nudgeBright shows the note at normal.
	nudgeDelay    = 300 * time.Millisecond
	nudgeDuration = 600 * time.Millisecond
	nudgeBright   = 300 * time.Millisecond
)

func newAlreadyNudge() effect { return alreadyNudge{} }

func (e alreadyNudge) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		if e.seq == ev.done.seq && (e.played || e.cue.live()) {
			break
		}
		e = alreadyNudge{seq: ev.done.seq}
		if ev.done.already {
			e.cue.arm()
		}
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= nudgeDelay+nudgeDuration {
			e.cue.stop()
			e.played = true
		}
	}
	return e
}

func (e alreadyNudge) busy() bool { return e.cue.live() }

func (e alreadyNudge) wake() time.Time { return time.Time{} }

func (e alreadyNudge) paint(f *doneFrame) {
	if !e.cue.running {
		return
	}
	into := e.cue.elapsed(f.now) - nudgeDelay - (nudgeDuration-nudgeBright)/2
	if into >= 0 && into < nudgeBright {
		f.noteFaint = false
	}
}
