package ui

import (
	"testing"
	"time"
)

func TestAlreadyNudgeBrightensTheNoteOnceOnAlreadyThereOnly(t *testing.T) {
	saved := rig(newAlreadyNudge)
	saved.done = doneFacts{seq: 1}
	saved.send(evShown)
	if !saved.idle() {
		t.Errorf("the nudge played on a Saved screen")
	}

	r := rig(newAlreadyNudge)
	r.done = doneFacts{seq: 1, already: true}
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown on Already there, the nudge is not waiting")
	}
	faint := func() bool { return r.mo.paintDone(doneFrame{noteFaint: true}).noteFaint }
	var steps []bool
	for at := time.Duration(0); at < nudgeDelay+nudgeDuration; at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("at %v the nudge stopped", at)
		}
		if f := faint(); len(steps) == 0 || steps[len(steps)-1] != f {
			steps = append(steps, f)
		}
	}
	if len(steps) != 3 || !steps[0] || steps[1] || !steps[2] {
		t.Errorf("the note went %v, want faint, normal, faint", steps)
	}
	r.tickAt(nudgeDelay + nudgeDuration)
	if !faint() || !r.idle() {
		t.Errorf("finished, the note is faint %v and the nudge idle %v", faint(), r.idle())
	}
	r.send(evShown)
	if !r.idle() {
		t.Errorf("the nudge played again for the same outcome")
	}
}
