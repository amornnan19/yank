package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestYankOutPullsTheURLOutOfTheBox(t *testing.T) {
	const url = "https://www.youtube.com/watch?v=jNQXAC9IVRw"
	r := rig(newYankOut)
	r.value = url
	r.send(evShown)
	if !r.idle() || r.effect().(screenHolder).holds() {
		t.Fatalf("the exit is playing before enter")
	}
	r.send(evSubmit)
	if !r.effect().busy() || !r.effect().(screenHolder).holds() {
		t.Fatalf("enter did not start the exit")
	}

	for _, cw := range []int{76, 26} {
		r := rig(newYankOut)
		r.value = url
		r.send(evSubmit)
		start, pulled := 0, false
		for at := time.Duration(0); at < yankDuration; at += motionFrame {
			r.tickAt(at)
			f := r.frame(cw)
			if !f.boxTextSet {
				t.Fatalf("at %v the exit does not draw the box", at)
			}
			if w := lipgloss.Width(f.boxText); w > f.boxWidth {
				t.Errorf("at cw %d, %v the box text is %d cells, over %d", cw, at, w, f.boxWidth)
			}
			// Past the debris at the left edge, what is left is the URL from
			// some point on, and that point only ever moves right.
			rs := []rune(strings.TrimSuffix(f.boxText, ellipsis))
			if len(rs) <= len(yankDebris) {
				pulled = true
				continue
			}
			i := 0
			if !strings.HasPrefix(f.boxText, url[:4]) {
				i = strings.LastIndex(url, string(rs[len(yankDebris):]))
			}
			switch {
			case i < 0:
				t.Errorf("at %v the box shows %q, which is not what is left of the URL", at, f.boxText)
			case i < start:
				t.Errorf("at %v the URL moved back into the box: %q", at, f.boxText)
			case i > 0:
				pulled = true
			}
			start = max(start, i)
		}
		if !pulled {
			t.Errorf("at cw %d the URL was never pulled out", cw)
		}
		r.tickAt(yankDuration)
		if !r.idle() || r.effect().(screenHolder).holds() {
			t.Errorf("the exit is still playing after %v", yankDuration)
		}
		if r.frame(cw).boxTextSet {
			t.Errorf("the finished exit still draws the box")
		}
	}
	if yankDuration >= 300*time.Millisecond {
		t.Errorf("the exit takes %v, want under 300ms", yankDuration)
	}

	// esc back to the input screen ends it.
	back := rig(newYankOut)
	back.value = url
	back.send(evSubmit)
	back.tickAt(0)
	back.send(evShown)
	if !back.idle() {
		t.Errorf("the exit kept playing on return to the input screen")
	}
}

func TestYankOutDropsWholeGraphemeClusters(t *testing.T) {
	// A URL pasted with emoji in its path: a rune at a time, the families and
	// hearts would come apart at the left edge as they went.
	// The Devanagari and Bengali conjuncts are one cluster each to the
	// segmenter lipgloss.Width uses; an older one drops the क् and leaves the
	// ष standing alone.
	const url = "https://example.com/\U0001f468\u200d\U0001f469\u200d\U0001f467\u2764\ufe0f\U0001f1f9\U0001f1ed\U0001f468\u200d\U0001f469\u200d\U0001f467\u2764\ufe0f\U0001f1f9\U0001f1ed" +
		"/\u0915\u094d\u0937\u092e\u093e\u0928\u092e\u0938\u094d\u0924\u0947\u0995\u09cd\u09b7\u09b8\u09cd\u09a4\u09cb"
	allowed := map[string]bool{ellipsis: true}
	for _, c := range clustersOf(url) {
		allowed[c] = true
	}
	for _, d := range yankDebris {
		allowed[string(d)] = true
	}
	for _, cw := range []int{76, 26} {
		r := rig(newYankOut)
		r.value = url
		r.send(evSubmit)
		for at := time.Duration(0); at < yankDuration; at += motionFrame / 4 {
			r.tickAt(at)
			f := r.frame(cw)
			if w := lipgloss.Width(f.boxText); w > f.boxWidth {
				t.Errorf("at cw %d, %v the box text is %d cells, over %d", cw, at, w, f.boxWidth)
			}
			for _, c := range clustersOf(f.boxText) {
				if !allowed[c] {
					t.Fatalf("at cw %d, %v the box shows %q, which carries %q, a piece of a cluster", cw, at, f.boxText, c)
				}
			}
		}
	}
}

func TestTheExitShowsTheInputScreenThenTheProbingScreen(t *testing.T) {
	f := &fakes{}
	m := motionModel(t, f, 80, 24)
	m.hasBin = true
	m = runMotion(t, m, 2*time.Second, nil)
	m.input.SetValue("https://www.youtube.com/watch?v=x")
	m = send(m, runes("y"))
	m = send(m, keyOf(tea.KeyEnter))
	start := clock(m)

	if !strings.Contains(m.View(), "╭") || strings.Contains(m.View(), "fetching video info") {
		t.Fatalf("the first frame after enter is not the exit:\n%s", m.View())
	}
	var last string
	m = runMotion(t, m, time.Hour, func(m Model) { last = m.View() })
	if clock(m)-start > yankDurationBound {
		t.Errorf("the exit took %v, want under %v", clock(m)-start, yankDurationBound)
	}
	if !strings.Contains(last, "fetching video info") {
		t.Errorf("after the exit the probing screen is not showing:\n%s", last)
	}

	// A probe that answers mid-exit wins: the picker shows, the exit ends.
	m = motionModel(t, &fakes{probes: []probeOutcome{{probe: newProbe(t, "T", "U")}}}, 80, 24)
	m.hasBin = true
	m.input.SetValue("https://example.com/v")
	m = send(m, runes("v"))
	m, cmd := step(m, keyOf(tea.KeyEnter))
	m, _ = advance(t, m, cmd)
	if m.state != statePicker {
		t.Fatalf("the probe did not land: state %v", m.state)
	}
	if m.motion.pending != tickNone || strings.Contains(m.View(), "╭") {
		t.Errorf("the exit outlived the probing screen: pending %v\n%s", m.motion.pending, m.View())
	}
}

func TestEscMidExitReturnsToAWholeInputScreen(t *testing.T) {
	const url = "https://example.com/some/video"
	m := motionModel(t, &fakes{}, 80, 24)
	m.hasBin = true
	m = runMotion(t, m, 2*time.Second, nil)
	m.input.SetValue(url)
	m = send(m, runes("x"))
	m = send(m, keyOf(tea.KeyEnter))
	m = runMotion(t, m, clock(m)+3*motionFrame, nil)
	if !m.motionShowing() || m.state != stateProbing {
		t.Fatalf("the exit is not playing three frames after enter")
	}
	m = send(m, keyOf(tea.KeyEsc))
	m = runMotion(t, m, clock(m)+2*motionFrame, nil)
	if m.state != stateInput {
		t.Fatalf("esc went to %v", m.state)
	}
	if !strings.Contains(m.View(), url) {
		t.Errorf("back on the input screen the box does not show the URL whole:\n%s", m.View())
	}
	if m.motion.holding(screenInput) {
		t.Errorf("the exit is still playing on the input screen")
	}
}

func TestAURLFromTheCommandLineSkipsTheExit(t *testing.T) {
	const url = "https://example.com/v"
	// The first resize lands before Init's batch, so motion is already on
	// the input screen when the command line's URL is submitted.
	m := motionModel(t, &fakes{}, 80, 24)
	m.hasBin = true
	m.input.SetValue(url)
	m = send(m, startURLMsg{})
	if m.state != stateProbing {
		t.Fatalf("the command line's URL went to %v", m.state)
	}
	if m.motion.holding(screenInput) || m.motionShowing() || m.motion.pending != tickNone {
		t.Errorf("launching with a URL played the exit: holding %v, pending %v", m.motion.holding(screenInput), m.motion.pending)
	}
	static := m
	static.motion.enabled = false
	if m.View() != static.View() {
		t.Errorf("launching with a URL did not go straight to the probing screen:\n%s\n--- want ---\n%s", m.View(), static.View())
	}

	// Enter on the same screen still plays it.
	m = motionModel(t, &fakes{}, 80, 24)
	m.hasBin = true
	m.input.SetValue(url)
	m = send(m, keyOf(tea.KeyEnter))
	if m.state != stateProbing || !m.motion.holding(screenInput) || !m.motionShowing() {
		t.Errorf("enter did not play the exit: state %v, holding %v", m.state, m.motion.holding(screenInput))
	}
}
