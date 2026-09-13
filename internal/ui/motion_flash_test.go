package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestFinishFlashFillsTheBarGreenAndLetsGo(t *testing.T) {
	r := rig(newFinishFlash)
	r.send(evShown)
	holds := func() bool { return r.effect().(screenHolder).holds() }
	if !r.idle() || holds() {
		t.Fatalf("the flash is playing before the download finished")
	}
	r.send(evFinish)
	if !r.effect().busy() || !holds() {
		t.Fatalf("the finish did not start the flash")
	}
	base := downloadFrame{cw: 60, label: " --%", sweep: true}
	inks := map[ink]bool{}
	for at := time.Duration(0); at < flashDuration; at += motionFrame {
		r.tickAt(at)
		f := r.mo.paintDownload(base)
		if f.sweep || f.label != "100%" || f.filled != len(f.bar) || len(f.bar) != 60-4-2 {
			t.Fatalf("at %v the flash drew sweep %v, label %q, %d of %d cells", at, f.sweep, f.label, f.filled, len(f.bar))
		}
		for _, c := range f.bar {
			if c.r != '█' || (c.ink != inkFlash && c.ink != inkFlashDim) {
				t.Fatalf("at %v a flash cell is %q in ink %v", at, c.r, c.ink)
			}
			inks[c.ink] = true
		}
		if !holds() {
			t.Fatalf("at %v the flash let go early", at)
		}
	}
	if !inks[inkFlash] || !inks[inkFlashDim] {
		t.Errorf("the flash never went from bright green to green: %v", inks)
	}
	r.tickAt(flashDuration)
	if !r.idle() || holds() {
		t.Errorf("the flash is still playing after %v", flashDuration)
	}
	if f := r.mo.paintDownload(base); !f.sweep {
		t.Errorf("the finished flash still draws the bar")
	}
	if flashDuration+motionFrame > 300*time.Millisecond {
		t.Errorf("the flash takes %v and its first frame up to %v more, over 300ms", flashDuration, motionFrame)
	}
	r.send(evFinish)
	r.send(evLeft)
	if holds() {
		t.Errorf("leaving did not end the flash")
	}
}

func TestTheFinishFlashHoldsOnlyWhatViewDraws(t *testing.T) {
	m, _ := downloadMotion(t, "Me at the zoo", 80, 24, newMotionOf([]effect{newFinishFlash()}, 1))
	m = send(m, report(m, 70))
	m = spinAt(m, 0)
	res := &ytdlp.DownloadResult{Path: "/Users/x/Downloads/Me at the zoo.mp4"}
	m = send(m, downloadDoneMsg{seq: m.seq, res: res})

	// The model is done at once; only the picture is the download screen.
	if m.state != stateDone || m.result != res || m.probe != nil {
		t.Fatalf("after the finish the model is in %v with result %v and probe %v", m.state, m.result, m.probe)
	}
	view := m.View()
	if !strings.Contains(view, "Me at the zoo") || !strings.Contains(view, "100%") || strings.Contains(view, "Saved") {
		t.Fatalf("the first frame after the finish is not the flash on the download screen:\n%s", view)
	}
	start := clock(m)
	var last string
	m = play(t, m, start, start+time.Second, func(m Model) { last = m.View() })
	if !strings.Contains(last, "✓ Saved") {
		t.Errorf("after the flash the done screen is not showing:\n%s", last)
	}
	if m.motion.pending != tickNone {
		t.Errorf("the flash left a %v tick outstanding", m.motion.pending)
	}

	// enter and q act on the done screen from the flash's first frame.
	for _, at := range []time.Duration{0, motionFrame, 4 * motionFrame} {
		m, _ := downloadMotion(t, "Me at the zoo", 80, 24, newMotion(noEnv, 1))
		m = spinAt(m, 0)
		m = send(m, downloadDoneMsg{seq: m.seq, res: res})
		m = play(t, m, 0, at, nil)
		if !m.motion.holding(screenDownloading) {
			t.Fatalf("at %v the flash is not playing", at)
		}
		enter := send(m, keyOf(tea.KeyEnter))
		if enter.state != stateInput || enter.motion.holding(screenDownloading) {
			t.Errorf("at %v enter mid-flash went to %v, flash still holding %v", at, enter.state, enter.motion.holding(screenDownloading))
		}
		if _, cmd := step(m, runes("q")); !quitsNow(cmd) {
			t.Errorf("at %v q mid-flash did not quit", at)
		}
		if esc := send(m, keyOf(tea.KeyEsc)); esc.state != stateDone {
			t.Errorf("at %v esc mid-flash did something the done screen does not: %v", at, esc.state)
		}
	}

	// A failure is not a finish, and neither is a cancel.
	for name, err := range map[string]error{"failure": hardErr(), "cancel": cancelledErr()} {
		m, _ := downloadMotion(t, "Me at the zoo", 80, 24, newMotionOf([]effect{newFinishFlash()}, 1))
		m = send(m, downloadDoneMsg{seq: m.seq, err: err})
		if m.motion.holding(screenDownloading) || m.motion.pending != tickNone {
			t.Errorf("a %s flashed: pending %v", name, m.motion.pending)
		}
	}
}
