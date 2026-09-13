package ui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

func TestBarShineGlidesAlongTheFillEveryPeriod(t *testing.T) {
	r := rig(newBarShine)
	if f := r.mo.paintDownload(barFrame(60, 40)); len(litCells(f)) != 0 {
		t.Fatalf("the shine drew before the screen was shown")
	}
	r.send(evShown)
	if !r.idle() {
		t.Fatalf("the shine asks for ticks of its own; it rides the spinner")
	}
	r.tickAt(0)

	lastLead, moved, paused := -1, false, false
	for at := time.Duration(0); at < shinePeriod; at += spinPeriod {
		r.tickAt(at)
		if !r.idle() {
			t.Fatalf("at %v the shine asks for ticks", at)
		}
		f := r.mo.paintDownload(barFrame(60, 40))
		lit := litCells(f)
		if len(lit) == 0 {
			if lastLead >= 0 {
				paused = true
			}
			continue
		}
		if paused {
			t.Errorf("at %v the band came back inside one period", at)
		}
		for _, c := range lit {
			if c >= 40 {
				t.Errorf("at %v the shine lit cell %d, past the fill of 40", at, c)
			}
		}
		if lit[0] < lastLead {
			t.Errorf("at %v the band went back from %d to %d", at, lastLead, lit[0])
		}
		if lastLead >= 0 && lit[0] > lastLead {
			moved = true
		}
		// At 10 fps the band's width covers a frame's travel: it glides.
		if lastLead >= 0 && lit[0]-lastLead > len(shineBand) {
			t.Errorf("at %v the band jumped %d cells in one spinner tick", at, lit[0]-lastLead)
		}
		lastLead = lit[0]
	}
	if !moved || !paused {
		t.Errorf("over one period the band moved %v and paused %v", moved, paused)
	}

	// The next period is the same pass again.
	for _, at := range []time.Duration{300 * time.Millisecond, 700 * time.Millisecond} {
		r.tickAt(at)
		a := litCells(r.mo.paintDownload(barFrame(60, 40)))
		r.tickAt(at + shinePeriod)
		b := litCells(r.mo.paintDownload(barFrame(60, 40)))
		if len(a) == 0 || !equalInts(a, b) {
			t.Errorf("at %v and one period later the band is %v and %v", at, a, b)
		}
	}

	// Nothing filled, a sweep, or the screen left: nothing lit.
	if lit := litCells(r.mo.paintDownload(barFrame(60, 0))); len(lit) != 0 {
		t.Errorf("the shine lit %v on an empty bar", lit)
	}
	if f := r.mo.paintDownload(downloadFrame{cw: 60, sweep: true}); f.bar != nil {
		t.Errorf("the shine drew cells over the indeterminate sweep")
	}
	r.send(evLeft)
	if lit := litCells(r.mo.paintDownload(barFrame(60, 40))); len(lit) != 0 {
		t.Errorf("the shine still drew after the screen was left")
	}
}

func TestBarShineNeverTouchesTheSpring(t *testing.T) {
	plain := downloadingModel(t, &fakes{})
	shiny := plain
	shiny.motion = newMotionOf([]effect{newBarShine()}, 1)
	shiny = send(shiny, tea.WindowSizeMsg{Width: 80, Height: 24})
	plain = send(plain, tea.WindowSizeMsg{Width: 80, Height: 24})

	var cmds [2]tea.Cmd
	plain, cmds[0] = step(plain, report(plain, 50))
	shiny, cmds[1] = step(shiny, report(shiny, 50))
	for i := range 12 {
		shiny = spinAt(shiny, time.Duration(i)*spinPeriod)
		pf, pok := findMsg[progress.FrameMsg](collect(t, cmds[0]))
		sf, sok := findMsg[progress.FrameMsg](collect(t, cmds[1]))
		if pok != sok {
			t.Fatalf("frame %d: the spring's chain is alive %v without the shine and %v with it", i, pok, sok)
		}
		if !pok {
			break
		}
		plain, cmds[0] = step(plain, pf)
		shiny, cmds[1] = step(shiny, sf)
		if plain.bar.Percent() != shiny.bar.Percent() || plain.bar.View() != shiny.bar.View() {
			t.Fatalf("frame %d: the shine moved the bar: %v %q vs %v %q", i, plain.bar.Percent(), plain.bar.View(), shiny.bar.Percent(), shiny.bar.View())
		}
	}
}
