package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStarfieldTwinklesOnWakesOnly(t *testing.T) {
	r := rig(newStarfield)
	r.mo.starRows = 8
	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown, the starfield is not waiting for its first tick")
	}
	r.tickAt(0)
	at := time.Duration(0)
	changed := false
	prev := r.frame(76).stars
	for range 200 {
		if r.effect().busy() {
			t.Fatalf("at %v the starfield wants frame ticks", at)
		}
		next := r.effect().wake().Sub(motionEpoch)
		if next <= at {
			t.Fatalf("at %v the next twinkle is at %v", at, next)
		}
		at = next
		r.tickAt(at)
		stars := r.frame(76).stars
		if len(stars) != len(prev) {
			changed = true
		} else {
			for i := range stars {
				if stars[i] != prev[i] {
					changed = true
				}
			}
		}
		prev = stars
	}
	if !changed {
		t.Errorf("200 wakes and no star changed")
	}
}

func TestStarsOnlyFallOnEmptyRowsTheTerminalHas(t *testing.T) {
	for _, height := range []int{minWordmarkHeight, 14, 15, 16, 24, 50} {
		m := motionModel(t, &fakes{}, 80, height, newStarfield)
		static := testModel(t, &fakes{}, 80)
		static = send(static, tea.WindowSizeMsg{Width: 80, Height: height})
		staticLines := strings.Split(static.View(), "\n")

		seenStars := false
		m = runMotion(t, m, 20*time.Second, func(m Model) {
			lines := strings.Split(m.View(), "\n")
			if len(lines) > max(height, len(staticLines)) {
				t.Fatalf("at height %d the starfield made the screen %d rows", height, len(lines))
			}
			// Every row the static screen uses — content and the padding
			// above it — is drawn exactly as it is there; stars only ever
			// arrive in rows past it, and never in the one under the legend.
			content := len(staticLines) - 1
			for i := 0; i < content && i < len(lines); i++ {
				if lines[i] != staticLines[i] {
					t.Fatalf("at height %d row %d changed:\n%q\nwant\n%q", height, i, lines[i], staticLines[i])
				}
			}
			if len(lines) > content && strings.TrimSpace(lines[content]) != "" {
				t.Fatalf("at height %d the row under the legend holds %q", height, lines[content])
			}
			if height >= len(staticLines)+3 && m.motion.now.Sub(motionEpoch) > 3*time.Second {
				stars := 0
				for _, line := range lines[content:] {
					stars += strings.Count(line, "·") + strings.Count(line, "˙")
				}
				seenStars = seenStars || stars > 0
			}
		})
		if height >= 16 && !seenStars {
			t.Errorf("at height %d no star was ever drawn", height)
		}
	}
}

func TestTheStarfieldSleepsWithNoRowToDrawIn(t *testing.T) {
	m := motionModel(t, &fakes{}, 80, minWordmarkHeight, newStarfield)
	if n := strings.Count(m.View(), "\n") + 1; n < minWordmarkHeight {
		t.Fatalf("the screen is %d rows at height %d; this test needs one with no spare row", n, minWordmarkHeight)
	}
	m = runMotion(t, m, time.Minute, nil)
	if m.motion.pending != tickNone {
		t.Fatalf("with nowhere to draw a star a %v tick is outstanding at %v", m.motion.pending, clock(m))
	}

	// A resize that makes room wakes it again.
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.motion.pending == tickNone {
		t.Fatalf("a resize that made room scheduled nothing")
	}
	m = runMotion(t, m, clock(m)+motionFrame, nil)
	if m.motion.pending != tickWake {
		t.Errorf("with room for stars the outstanding tick is %v, want a wake", m.motion.pending)
	}
}
