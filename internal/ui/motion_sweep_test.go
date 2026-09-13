package ui

import (
	"strings"
	"testing"
)

func TestRevealSweepCrossesOnceAfterItsDelay(t *testing.T) {
	r := rig(newRevealSweep)
	r.send(evShown)
	r.tickAt(0)
	if r.effect().busy() {
		t.Errorf("the sweep wants frames during its pause")
	}
	if got := r.effect().wake(); !got.Equal(motionEpoch.Add(sweepDelay)) {
		t.Errorf("the sweep wakes at %v, want %v", got.Sub(motionEpoch), sweepDelay)
	}

	last := -1
	for at := sweepDelay; at < sweepDelay+sweepCross; at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("mid-sweep at %v it does not want frames", at)
		}
		rows := plainWordmark(r.frame(76))
		col := strings.IndexRune(rows[0], sweepGlyph)
		if col < 0 {
			t.Fatalf("at %v there is no bar:\n%s", at, strings.Join(rows, "\n"))
		}
		col = len([]rune(rows[0][:col]))
		if col <= last {
			t.Errorf("at %v the bar is at %d, not right of %d", at, col, last)
		}
		last = col
		for _, row := range rows {
			if strings.Count(row, string(sweepGlyph)) != 1 {
				t.Errorf("at %v a row does not carry exactly one bar: %q", at, row)
			}
		}
	}
	r.tickAt(sweepDelay + sweepCross)
	if !r.idle() {
		t.Errorf("the sweep still wants ticks after crossing")
	}
	r.send(evShown)
	if !r.idle() {
		t.Errorf("the sweep replays when the screen is shown again")
	}

	cancelled := rig(newRevealSweep)
	cancelled.send(evShown)
	cancelled.tickAt(0)
	cancelled.send(evSubmit)
	if !cancelled.idle() {
		t.Errorf("enter did not cancel the sweep")
	}
}
