package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestPercentPulseBrightensTheLabelAtEachQuarter(t *testing.T) {
	r := rig(newPercentPulse)
	at := func(pct int64) {
		r.dl.prog = ytdlp.Progress{Phase: ytdlp.PhaseDownloading, Downloaded: pct, DownloadedKnown: true, Total: 100, TotalKnown: true}
		r.send(evReport)
	}
	bright := func() bool {
		f := r.mo.paintDownload(downloadFrame{cw: 80, label: " 30%"})
		if f.label != " 30%" {
			t.Errorf("the pulse changed the label's text to %q", f.label)
		}
		return f.labelBright
	}
	r.send(evShown)
	at(20)
	if bright() {
		t.Fatalf("the label is bright before a mark was crossed")
	}
	at(30)
	if !bright() || !r.idle() {
		t.Fatalf("crossing 25%% did not pulse, or the pulse asked for ticks")
	}
	r.tickAt(0)
	r.tickAt(pulseDuration - spinPeriod)
	if !bright() {
		t.Errorf("the pulse ended before %v", pulseDuration)
	}
	r.tickAt(pulseDuration)
	if bright() {
		t.Errorf("the pulse outlasted %v", pulseDuration)
	}
	at(49)
	if bright() {
		t.Errorf("a report short of the next mark pulsed")
	}
	at(75) // across 50 and 75 at once: one pulse
	r.tickAt(time.Second)
	if !bright() {
		t.Errorf("crossing 50%% and 75%% in one report did not pulse")
	}
	r.tickAt(time.Second + pulseDuration)
	at(100)
	if bright() {
		t.Errorf("100%% pulsed; the marks are 25, 50 and 75")
	}

	// A screen shown mid-way has crossed nothing yet; an unknown total never
	// crosses anything.
	mid := rig(newPercentPulse)
	mid.dl.prog = ytdlp.Progress{Downloaded: 60, DownloadedKnown: true, Total: 100, TotalKnown: true}
	mid.send(evShown)
	mid.dl.prog.Downloaded = 70
	mid.send(evReport)
	if mid.mo.paintDownload(downloadFrame{}).labelBright {
		t.Errorf("a screen shown at 60%% pulsed at 70%%")
	}
	unknown := rig(newPercentPulse)
	unknown.send(evShown)
	unknown.dl.prog = ytdlp.Progress{Downloaded: 90, DownloadedKnown: true}
	unknown.send(evReport)
	if unknown.mo.paintDownload(downloadFrame{}).labelBright {
		t.Errorf("a download of unknown size pulsed")
	}
}

func TestThePulsedLabelReadsTheSame(t *testing.T) {
	forceColour(t)
	m, _ := downloadMotion(t, "Me at the zoo", 80, 24, newMotionOf([]effect{newPercentPulse()}, 1))
	m = send(m, report(m, 20))
	m = send(m, report(m, 26))
	line := m.animatedBarLine(m.downloadFrame())
	if !m.downloadFrame().labelBright {
		t.Fatalf("crossing 25%% did not pulse on the model")
	}
	if plain := sgr.ReplaceAllString(line, ""); !strings.HasSuffix(plain, percentLabel(26, true)) {
		t.Errorf("the pulsed label reads %q, want percentLabel's %q", plain, percentLabel(26, true))
	}
	if params := sgr.FindAllStringSubmatch(line, -1); !containsParam(params, "1") || !containsParam(params, "97") {
		t.Errorf("the pulsed label is not drawn bold bright white: %q", line)
	}
}
