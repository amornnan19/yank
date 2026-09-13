package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStarfieldTwinklesOnWakesOnly(t *testing.T) {
	r := rig(newStarfield)
	r.mo.freeCells = 8
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

func TestStarsOnlyFallOnEmptyCellsTheTerminalHas(t *testing.T) {
	for _, size := range [][2]int{{80, minWordmarkHeight}, {80, 14}, {80, 15}, {80, 16}, {80, 24}, {80, 50}, {120, 40}, {200, 60}} {
		width, height := size[0], size[1]
		at := itoa(width) + "x" + itoa(height)
		m := motionModel(t, &fakes{}, width, height, newStarfield)
		static := testModel(t, &fakes{}, width)
		static = send(static, tea.WindowSizeMsg{Width: width, Height: height})
		reserve := static.inputRows()

		seen, seenBeside := 0, 0
		m = runMotion(t, m, 20*time.Second, func(m Model) {
			// Every cell the static screen draws is drawn exactly as it is
			// there; stars only ever arrive clear of the block, its reserved
			// row and its margin.
			marks, beside := assertDecorationOnlyAround(t, "the starfield at "+at+" "+clock(m).String(),
				static.View(), m.View(), width, height, reserve, "·˙")
			seen += marks
			seenBeside += beside
		})
		if height >= 16 && seen == 0 {
			t.Errorf("at %s no star was ever drawn", at)
		}
		// Wider than the content, the margins beside the block are free too.
		if width >= 120 && seenBeside == 0 {
			t.Errorf("at %s no star was ever drawn beside the block", at)
		}
	}
}

func TestTheStarfieldSleepsWithNoCellToDrawIn(t *testing.T) {
	m := motionModel(t, &fakes{}, 80, minWordmarkHeight, newStarfield)
	if n := m.motion.freeCells; n != 0 {
		t.Fatalf("the screen leaves %d free cells at height %d; this test needs one with none", n, minWordmarkHeight)
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
