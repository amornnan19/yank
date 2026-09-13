package ui

import "testing"

// brightCells counts the highlighted cells of a frame's wordmark.
func brightCells(f inputFrame) int {
	n := 0
	for _, row := range f.wordmark {
		for _, c := range row {
			if c.ink == inkBright {
				n++
			}
		}
	}
	return n
}

func TestShimmerCrossesEveryPeriodAndWaitsBetween(t *testing.T) {
	r := rig(newShimmer)
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown, the shimmer is not waiting for its first tick")
	}
	r.tickAt(0)
	if r.effect().busy() {
		t.Errorf("the shimmer wants frames before its first crossing")
	}
	if got := r.effect().wake(); !got.Equal(motionEpoch.Add(shimmerPeriod)) {
		t.Errorf("the first crossing wakes at %v, want %v", got.Sub(motionEpoch), shimmerPeriod)
	}
	if brightCells(r.frame(76)) != 0 {
		t.Errorf("the idle shimmer highlighted cells")
	}

	lit := 0
	for at := shimmerPeriod; at < shimmerPeriod+shimmerCross; at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("mid-crossing at %v the shimmer does not want frames", at)
		}
		n := brightCells(r.frame(76))
		if n > shimmerBand*len(wordmarkRows) {
			t.Errorf("at %v the band is %d cells, wider than %d columns", at, n, shimmerBand)
		}
		if n > 0 {
			lit++
		}
	}
	if lit == 0 {
		t.Errorf("the crossing highlighted nothing")
	}
	r.tickAt(shimmerPeriod + shimmerCross)
	if r.effect().busy() {
		t.Errorf("after the crossing the shimmer still wants frames")
	}
	if got := r.effect().wake(); !got.Equal(motionEpoch.Add(2 * shimmerPeriod)) {
		t.Errorf("the next crossing wakes at %v, want %v", got.Sub(motionEpoch), 2*shimmerPeriod)
	}

	r.send(evSubmit)
	if !r.idle() {
		t.Errorf("enter did not stop the shimmer")
	}
}
