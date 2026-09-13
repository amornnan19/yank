package ui

import (
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/amornnan19/yank/internal/ytdlp"
)

func TestSpeedSparklineGraphsTheLastReports(t *testing.T) {
	r := rig(newSpeedSparkline)
	r.send(evShown)
	spark := func() string { return r.mo.paintDownload(downloadFrame{cw: 80}).spark }
	speed := func(bps float64, known bool) {
		r.dl.prog = ytdlp.Progress{Phase: ytdlp.PhaseDownloading, Speed: bps, SpeedKnown: known}
		r.send(evReport)
	}
	if spark() != "" {
		t.Fatalf("a graph before any report: %q", spark())
	}
	speed(1_000_000, true)
	if got := spark(); got != "█" {
		t.Errorf("one report graphs %q, want one full bar", got)
	}
	speed(500_000, true)
	speed(0, true)
	if got := spark(); got != "█▅▁" {
		t.Errorf("reports at 1, 0.5 and 0 MB/s graph %q, want █▅▁", got)
	}
	for i := range 20 {
		speed(float64(i+1)*100_000, true)
	}
	got := []rune(spark())
	if len(got) != sparkLength {
		t.Errorf("after 23 reports the graph is %d bars, want %d", len(got), sparkLength)
	}
	if got[len(got)-1] != '█' {
		t.Errorf("the fastest, latest report is not the tallest bar: %q", string(got))
	}
	// Time does not move it: a tick changes nothing.
	before := spark()
	r.tickAt(0)
	r.tickAt(10 * spinPeriod)
	if spark() != before || !r.idle() {
		t.Errorf("ticks changed the graph or it asked for ticks")
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1} {
		speed(bad, true)
		if spark() != "" {
			t.Errorf("a speed of %v still shows a graph", bad)
		}
	}
	speed(1, false)
	if spark() != "" {
		t.Errorf("an unknown speed still shows a graph")
	}
	speed(1, true)
	r.send(evLeft)
	if spark() != "" {
		t.Errorf("the graph outlived the screen")
	}
}

func TestTheSparklineFollowsTheStatsLineOnlyWithRoom(t *testing.T) {
	for _, width := range []int{80, 44, 30} {
		m, _ := downloadMotion(t, "Me at the zoo", width, 24, newMotionOf([]effect{newSpeedSparkline()}, 1))
		for i := int64(1); i <= 5; i++ {
			m = send(m, speedReport(m, i*10, float64(i)*1_000_000))
		}
		stats := m.statsLine()
		line := m.animatedUnderLine(m.downloadFrame())
		room := lipgloss.Width(stats)+2+5 <= m.contentWidth()
		if has := strings.ContainsAny(line, "▁▂▃▄▅▆▇█"); has != room {
			t.Errorf("at width %d the graph shows %v with room %v: %q", width, has, room, line)
		}
		if lipgloss.Width(line) > m.contentWidth() || !strings.HasPrefix(line, strings.TrimSuffix(truncate(stats, m.contentWidth()), ellipsis)) {
			t.Errorf("at width %d the stats line is %q", width, line)
		}
		merging := send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{Phase: ytdlp.PhaseMerging}})
		if strings.ContainsAny(merging.View(), "▁▂▃▄▅▆▇") {
			t.Errorf("at width %d the graph shows on the merging line:\n%s", width, merging.View())
		}
	}
	// Static screens never show it.
	m, _ := downloadMotion(t, "Me at the zoo", 80, 24, motion{})
	m = send(m, speedReport(m, 10, 1_000_000))
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if strings.ContainsAny(m.View(), "▁▂▃▄▅▆▇") {
		t.Errorf("the static screen has a graph:\n%s", m.View())
	}
}
