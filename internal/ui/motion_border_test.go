package ui

import (
	"testing"
	"time"
)

func TestBreathingBorderStepsWhileTheBoxIsEmpty(t *testing.T) {
	r := rig(newBreathingBorder)
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown empty, the border is not waiting for its first tick")
	}
	r.tickAt(0)
	if r.effect().busy() {
		t.Errorf("the border wants frame ticks; it steps on wakes")
	}
	seen := map[string]bool{}
	for at := time.Duration(0); at < 4*breathStep; at += breathStep {
		r.tickAt(at)
		seen[r.frame(76).border] = true
		if got := r.effect().wake(); !got.Equal(motionEpoch.Add(at + breathStep)) {
			t.Errorf("at %v the border wakes at %v, want %v", at, got.Sub(motionEpoch), at+breathStep)
		}
	}
	if !seen[ansiCyan] || !seen[ansiBrightCyan] || len(seen) != 2 {
		t.Errorf("the border showed %v, want exactly 6 and 14", seen)
	}

	r.value = "h"
	r.send(evEdit)
	if !r.idle() {
		t.Errorf("typing did not stop the border")
	}
	if got := r.frame(76).border; got != ansiCyan {
		t.Errorf("with text in the box the border is %q, want cyan", got)
	}

	r.value = ""
	r.send(evEdit)
	if !r.effect().busy() {
		t.Errorf("emptying the box did not start the border again")
	}
}
