package ui

import (
	"testing"
	"time"
)

func TestTypingPlaceholderTypesErasesAndStopsForGood(t *testing.T) {
	r := rig(newTypingPlaceholder)
	r.send(evShown)
	if f := r.frame(76); !f.placeholderSet || f.placeholder != "" {
		t.Errorf("before the first tick the placeholder is %q (set %v), want an empty one", f.placeholder, f.placeholderSet)
	}
	r.tickAt(0)
	if r.effect().busy() {
		t.Errorf("the placeholder wants frame ticks; it types on wakes")
	}

	seen := map[string]bool{}
	at := time.Duration(0)
	for range 400 {
		r.tickAt(at)
		f := r.frame(76)
		seen[f.placeholder] = true
		next := r.effect().wake().Sub(motionEpoch)
		if next <= at {
			t.Fatalf("at %v the next step is at %v, not later", at, next)
		}
		at = next
	}
	for _, ex := range placeholderExamples {
		if !seen[ex] {
			t.Errorf("the rotation never showed %q in full", ex)
		}
	}
	if !seen["h"] || !seen["ht"] {
		t.Errorf("the rotation did not type character by character")
	}

	r.value = "x"
	r.send(evEdit)
	if !r.idle() {
		t.Errorf("typing did not stop the placeholder")
	}
	if r.frame(76).placeholderSet {
		t.Errorf("the stopped placeholder still paints")
	}
	r.value = ""
	r.send(evEdit)
	r.send(evShown)
	if !r.idle() || r.frame(76).placeholderSet {
		t.Errorf("the placeholder came back after the box was emptied; it stops for good")
	}
}

func TestPlaceholderTimelineIsContinuous(t *testing.T) {
	prev := ""
	for at := time.Duration(0); at < 30*time.Second; at += 5 * time.Millisecond {
		text, next := placeholderAt(at)
		if next <= 0 {
			t.Fatalf("at %v the next change is %v away", at, next)
		}
		if d := len([]rune(text)) - len([]rune(prev)); d > 1 || d < -1 && text != "" {
			t.Fatalf("at %v the placeholder jumped from %q to %q", at, prev, text)
		}
		prev = text
	}
}
