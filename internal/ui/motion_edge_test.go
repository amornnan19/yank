package ui

import (
	"testing"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestLeadingEdgeLightsWhileBytesArrive(t *testing.T) {
	r := rig(newLeadingEdge)
	r.send(evShown)
	head := func() ink {
		f := r.mo.paintDownload(barFrame(60, 30))
		for i, c := range f.bar {
			if i != 29 && c.ink != inkFill && c.ink != inkPlain {
				t.Errorf("the edge lit cell %d, not the head of the fill", i)
			}
		}
		return f.bar[29].ink
	}
	if head() != inkFill {
		t.Fatalf("the edge is lit before any report")
	}
	grow := func(n int64) {
		r.dl.prog = ytdlp.Progress{Phase: ytdlp.PhaseDownloading, Downloaded: n, DownloadedKnown: true, Total: 100, TotalKnown: true}
		r.send(evReport)
	}
	grow(10)
	if head() != inkGlint || !r.idle() {
		t.Fatalf("a report that grew did not light the edge, or asked for ticks: %v", head())
	}
	r.tickAt(0)
	r.tickAt(edgeHold - spinPeriod)
	if head() != inkGlint {
		t.Errorf("the edge went out before %v", edgeHold)
	}
	// A report that counted nothing new does not keep it lit.
	grow(10)
	r.tickAt(edgeHold)
	if head() != inkFill {
		t.Errorf("the edge is still lit %v after the last growth", edgeHold)
	}
	grow(20)
	r.tickAt(edgeHold + spinPeriod)
	if head() != inkGlint {
		t.Errorf("growth after a pause did not light the edge again")
	}
	// Merging counts no bytes.
	r.dl.prog.Phase = ytdlp.PhaseMerging
	r.send(evReport)
	if head() != inkFill {
		t.Errorf("the edge is lit through a merge")
	}
	if f := r.mo.paintDownload(barFrame(60, 0)); f.bar[0].ink != inkPlain {
		t.Errorf("the edge lit an empty bar")
	}
	grow(30)
	r.send(evLeft)
	if head() != inkFill {
		t.Errorf("the edge stayed lit after the screen was left")
	}
	if !r.idle() {
		t.Errorf("the edge asks for ticks")
	}
}

func TestTheLeadingEdgeIsBrightBlueOnScreen(t *testing.T) {
	trueColour(t)
	m, _ := downloadMotion(t, "Me at the zoo", 80, 24, newMotionOf([]effect{newLeadingEdge()}, 1))
	m.bar = newBar()
	m = send(m, report(m, 100))
	m = spinAt(m, 0)
	line := m.animatedBarLine(m.downloadFrame())
	if got := sgr.FindAllStringSubmatch(line, -1); len(got) == 0 || !containsParam(got, "94") {
		t.Errorf("the lit edge is not drawn in bright blue (SGR 94): %q", line)
	}
}
