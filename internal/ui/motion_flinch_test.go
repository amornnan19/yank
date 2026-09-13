package ui

import (
	"strings"
	"testing"
	"time"
)

func TestFlinchJerksTheKAndSpringsBack(t *testing.T) {
	r := rig(newFlinch)
	r.send(evShown)
	if !r.idle() {
		t.Fatalf("the flinch wants ticks before enter")
	}
	r.send(evSubmit)
	if !r.effect().busy() {
		t.Fatalf("enter did not start the flinch")
	}

	k := wordmarkLetters[len(wordmarkLetters)-1]
	kAt := func(f inputFrame) int {
		// The k's leftmost column is its stem on every row but the last.
		row := []rune(plainWordmark(f)[1])
		for c := k[0] - 2; c < len(row); c++ {
			if row[c] != ' ' {
				return c - k[0]
			}
		}
		return -99
	}

	for _, cw := range []int{76, wordmarkWidth(), wordmarkWidth() + 1} {
		r := rig(newFlinch)
		r.send(evSubmit)
		r.tickAt(0)
		f := r.frame(cw)
		if got := kAt(f); got != min(flinchJerk, len(f.wordmark[0])-1-k[0]) && cw >= wordmarkWidth()+wordmarkSlack {
			t.Errorf("at cw %d the first frame moved the k by %d, want %d", cw, got, flinchJerk)
		}
		for _, row := range plainWordmark(f) {
			if n := len([]rune(row)); n > cw {
				t.Errorf("at cw %d the flinch drew a %d-cell row", cw, n)
			}
		}
		// Nothing but the k moved.
		for i, row := range plainWordmark(f) {
			if got, want := string([]rune(row)[:k[0]]), string([]rune(wordmarkRows[i])[:k[0]]); got != want {
				t.Errorf("at cw %d the flinch moved %q to %q", cw, want, got)
			}
		}
	}

	r.tickAt(0)
	moved := false
	for at := time.Duration(0); at < flinchDuration; at += motionFrame {
		r.tickAt(at)
		if kAt(r.frame(76)) != 0 {
			moved = true
		}
	}
	if !moved {
		t.Errorf("the k never moved")
	}
	r.tickAt(flinchDuration)
	if !r.idle() {
		t.Errorf("the flinch still wants ticks after %v", flinchDuration)
	}
	if got := plainWordmark(r.frame(76)); !equalLines(got, staticWordmark(76)) {
		t.Errorf("after the flinch the wordmark is\n%s", strings.Join(got, "\n"))
	}
}
