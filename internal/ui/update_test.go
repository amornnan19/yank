package ui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// resolvingModel is the input screen with a URL submitted and Resolve's answer
// fed back: the moment the background check may start. cmd is what
// handleResolved returned.
func resolvingModel(t *testing.T, f *fakes) (Model, tea.Cmd) {
	t.Helper()
	m := testModel(t, f, 80)
	m = typeURL(m, "https://example.com/v")
	m, _ = step(m, keyOf(tea.KeyEnter))
	if m.state != stateProbing || m.step != stepResolving {
		t.Fatalf("state = %v step = %v, want resolving", m.state, m.step)
	}
	return step(m, resolvedMsg{seq: m.seq, res: f.resolveRes})
}

func TestTheBackgroundCheckStartsOnlyForTheCachedCopy(t *testing.T) {
	for _, tt := range []struct {
		source ytdlp.Source
		starts bool
	}{
		{ytdlp.SourceCache, true},
		{ytdlp.SourcePATH, false},
		{ytdlp.SourceDownload, false},
	} {
		t.Run(string(tt.source), func(t *testing.T) {
			f := &fakes{
				resolveRes: ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.07.01", Source: tt.source},
				probes:     []probeOutcome{{probe: newProbe(t, "t", "u")}},
			}
			m, cmd := resolvingModel(t, f)
			msgs := collect(t, cmd)
			if _, ok := findMsg[probeDoneMsg](msgs); !ok {
				t.Error("the probe did not run beside the check")
			}
			_, sawUpdate := findMsg[updateDoneMsg](msgs)
			if sawUpdate != tt.starts || (f.updates() == 1) != tt.starts {
				t.Fatalf("check started = %t (%d calls), want %t", sawUpdate, f.updates(), tt.starts)
			}
			if m.updateStarted != tt.starts {
				t.Errorf("updateStarted = %t, want %t", m.updateStarted, tt.starts)
			}
		})
	}
}

func TestTheBackgroundCheckDoesNotHoldUpTheProbeAndOutlivesEsc(t *testing.T) {
	f := &fakes{
		resolveRes: ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.07.01", Source: ytdlp.SourceCache},
		probes:     []probeOutcome{{probe: newProbe(t, "t", "u")}},
	}
	release := make(chan struct{})
	defer close(release)
	var gotCtx atomic.Value
	deps := f.deps()
	deps.Update = func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		gotCtx.Store(ctx)
		<-release
		return ytdlp.UpdateResult{}, nil
	}
	m := New(context.Background(), deps, "")
	m.width, m.height = 80, 24
	m.layout()
	m = typeURL(m, "https://example.com/v")
	m, _ = step(m, keyOf(tea.KeyEnter))
	m, cmd := step(m, resolvedMsg{seq: m.seq, res: f.resolveRes})

	// The check blocks until the test ends; the probe still lands.
	m, _ = advance(t, m, cmd)
	if m.state != statePicker {
		t.Fatalf("state = %v, want the picker while the check is still running", m.state)
	}

	m = send(m, keyOf(tea.KeyEsc))
	ctx, _ := gotCtx.Load().(context.Context)
	if ctx == nil {
		t.Fatal("the check was never started")
	}
	if ctx.Err() != nil {
		t.Errorf("esc cancelled the check (%v): it belongs to the session, not the attempt", ctx.Err())
	}

	// A second attempt has the binary already and starts no second check.
	m = typeURL(m, "https://example.com/w")
	m, cmd = step(m, keyOf(tea.KeyEnter))
	_, _ = advance(t, m, cmd)
	if !m.updateStarted {
		t.Error("updateStarted was lost across esc")
	}
}

func TestAStagedUpdateIsNotedOnTheInputScreenUntilItIsLeft(t *testing.T) {
	f := &fakes{probes: []probeOutcome{{err: hardErr()}}}
	m := testModel(t, f, 80)
	m.hasBin, m.updateStarted = true, true
	m.bin = ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.07.01", Source: ytdlp.SourceCache}
	m = typeURL(m, "https://example.com/v")
	m, _ = step(m, keyOf(tea.KeyEnter))

	const notice = "yt-dlp 2026.08.19 will be used next launch"
	m = send(m, updateDoneMsg{res: ytdlp.UpdateResult{
		Status: ytdlp.UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19",
	}})
	if strings.Contains(m.View(), notice) {
		t.Errorf("the probing screen shows the notice:\n%s", m.View())
	}

	m = send(m, keyOf(tea.KeyEsc))
	view := m.View()
	legend := lineContaining(view, "enter  fetch")
	if at := lineContaining(view, notice); at < 0 || at != legend+1 {
		t.Fatalf("notice on row %d, want the row under the legend (%d):\n%s", at, legend, view)
	}
	if strings.Contains(view, "yt-dlp updated") {
		t.Errorf("a staged release is described as installed:\n%s", view)
	}

	// The error screen after a staged update does not tell the user to run
	// --update: the next launch already has it.
	m = typeURL(m, "https://example.com/v")
	m, cmd := step(m, keyOf(tea.KeyEnter))
	if strings.Contains(m.View(), notice) {
		t.Errorf("the notice survived leaving the input screen:\n%s", m.View())
	}
	m, _ = advance(t, m, cmd)
	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if strings.Contains(m.View(), "yank --update") {
		t.Errorf("error screen suggests --update after an update was staged:\n%s", m.View())
	}
	m = send(m, keyOf(tea.KeyEsc))
	if strings.Contains(m.View(), notice) {
		t.Errorf("the notice came back after the screen changed:\n%s", m.View())
	}
}

func TestAResolveThatPromotedAStagedReleaseIsNoted(t *testing.T) {
	for _, tt := range []struct {
		name string
		res  ytdlp.Result
		want string
	}{
		{"promoted", ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.08.19", Source: ytdlp.SourceCache, Updated: true}, "yt-dlp updated to 2026.08.19"},
		{"not promoted", ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.08.19", Source: ytdlp.SourceCache}, ""},
		{"not a version", ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.08.19\x1b[2J", Source: ytdlp.SourceCache, Updated: true}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakes{resolveRes: tt.res, probes: []probeOutcome{{err: cancelledErr()}}}
			m, cmd := resolvingModel(t, f)
			if m.updateNotice != tt.want {
				t.Fatalf("notice = %q, want %q", m.updateNotice, tt.want)
			}
			m, _ = advance(t, m, cmd)
			if m.state != stateInput {
				t.Fatalf("state = %v, want the input screen after a cancelled probe", m.state)
			}
			if tt.want != "" && !strings.Contains(m.View(), tt.want) {
				t.Errorf("the input screen does not say %q:\n%s", tt.want, m.View())
			}
			if tt.want == "" && strings.Contains(m.View(), "yt-dlp updated") {
				t.Errorf("an update notice is shown:\n%s", m.View())
			}
		})
	}
}

func TestTheNoticeSurvivesDoneAndEnter(t *testing.T) {
	m := downloadingModel(t, &fakes{})
	m = send(m, updateDoneMsg{res: ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Staged: "2026.08.19", Latest: "2026.08.19"}})
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})
	m = send(m, keyOf(tea.KeyEnter))
	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	if !strings.Contains(m.View(), "yt-dlp 2026.08.19 will be used next launch") {
		t.Errorf("the notice was lost in reset:\n%s", m.View())
	}
}

func TestANoticeWithAnInvalidVersionIsNotShown(t *testing.T) {
	for _, res := range []ytdlp.UpdateResult{
		{Status: ytdlp.UpdateInstalled, Version: "2026.08.19\x1b[2J"},
		{Status: ytdlp.UpdateStaged, Staged: "2026.08.19\x1b[2J"},
	} {
		m := testModel(t, &fakes{}, 80)
		m = send(m, updateDoneMsg{res: res})
		if view := m.View(); m.updateNotice != "" || strings.Contains(view, "will be used") || strings.Contains(view, "yt-dlp updated") {
			t.Errorf("notice %q shown for a version that is not one:\n%s", m.updateNotice, m.View())
		}
	}
}

func TestTheNoticeKeepsTheInputScreenCentredAndInTheTerminal(t *testing.T) {
	// Matched on its start: at the narrowest sizes fit cuts the end off.
	const notice = "yt-dlp 2026"
	for _, tt := range []struct {
		width, height int
		shown         bool
	}{
		{80, 24, true},
		{120, 40, true},
		{200, 60, true},
		// The screen with the line under the box is 11 rows here: the notice
		// needs a 13-row terminal to be drawn with its reserve kept.
		{wordmarkWidth() + 4, minWordmarkHeight, false},
		{wordmarkWidth() + 4, minWordmarkHeight + 1, true},
		{40, 9, false},
	} {
		at := itoa(tt.width) + "x" + itoa(tt.height)
		for _, animated := range []bool{false, true} {
			var m Model
			if animated {
				m = motionModel(t, &fakes{}, tt.width, tt.height)
				m = runMotion(t, m, 2*time.Second, nil)
			} else {
				m = testModel(t, &fakes{}, tt.width)
				m = send(m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			}
			m = send(m, updateDoneMsg{res: ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Staged: "2026.08.19"}})

			view := m.View()
			if shown := strings.Contains(view, notice); shown != tt.shown {
				t.Errorf("at %s (animated %t) notice shown = %t, want %t:\n%s", at, animated, shown, tt.shown, view)
			}
			row, col := boxCorner(view)
			for _, what := range []string{"typing", "the hint"} {
				if what == "typing" {
					m = send(m, runes("x"))
				} else {
					m = send(m, keyOf(tea.KeyEnter))
				}
				view := m.View()
				if r, c := boxCorner(view); r != row || c != col {
					t.Errorf("at %s (animated %t) %s moved the box from %d,%d to %d,%d:\n%s", at, animated, what, row, col, r, c, view)
				}
				if strings.Contains(view, notice) != tt.shown {
					t.Errorf("at %s (animated %t) the notice came or went with %s:\n%s", at, animated, what, view)
				}
				if tt.shown && (lineCount(view) > tt.height || widest(view) > tt.width) {
					t.Errorf("at %s (animated %t) %s is %dx%d:\n%s", at, animated, what, widest(view), lineCount(view), view)
				}
			}
		}
	}
}

func TestTheErrorScreenSaysANewerYtDlpExists(t *testing.T) {
	tests := []struct {
		name   string
		update ytdlp.UpdateResult
		// download fails the download instead of the probe.
		download bool
		want     bool
	}{
		{"probe failed, newer known", ytdlp.UpdateResult{Latest: "2026.08.19"}, false, true},
		{"download failed, newer known", ytdlp.UpdateResult{Latest: "2026.08.19"}, true, true},
		{"newer by revision", ytdlp.UpdateResult{Latest: "2026.07.01.1"}, false, true},
		{"same version", ytdlp.UpdateResult{Latest: "2026.07.01"}, false, false},
		{"older", ytdlp.UpdateResult{Latest: "2026.06.30"}, false, false},
		{"nothing known", ytdlp.UpdateResult{}, false, false},
		{"not a version", ytdlp.UpdateResult{Latest: "2026.08.19\x1b[31m"}, false, false},
		{"already installed this run", ytdlp.UpdateResult{Status: ytdlp.UpdateInstalled, Version: "2026.08.19", Latest: "2026.08.19"}, false, false},
		{"already staged this run", ytdlp.UpdateResult{Status: ytdlp.UpdateStaged, Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"}, false, false},
		{"the newer release failed to install", ytdlp.UpdateResult{Latest: "2026.08.19", Failed: "2026.08.19"}, false, false},
		{"download failed, the newer release failed to install", ytdlp.UpdateResult{Latest: "2026.08.19", Failed: "2026.08.19"}, true, false},
		{"an older release failed, a newer one is known", ytdlp.UpdateResult{Latest: "2026.08.20", Failed: "2026.08.19"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Model
			if tt.download {
				m = downloadingModel(t, &fakes{})
				m.bin.Version, m.updateStarted = "2026.07.01", true
				m = send(m, updateDoneMsg{res: tt.update})
				m = send(m, downloadDoneMsg{seq: m.seq, err: hardErr()})
			} else {
				f := &fakes{probes: []probeOutcome{{err: hardErr()}}}
				m = testModel(t, f, 80)
				m.hasBin, m.updateStarted = true, true
				m.bin = ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.07.01", Source: ytdlp.SourceCache}
				m = send(m, updateDoneMsg{res: tt.update})
				m = typeURL(m, "https://example.com/v")
				var cmd tea.Cmd
				m, cmd = step(m, keyOf(tea.KeyEnter))
				m, _ = advance(t, m, cmd)
			}
			if m.state != stateError {
				t.Fatalf("state = %v, want stateError", m.state)
			}
			view := m.View()
			hint := "a newer yt-dlp (" + tt.update.Latest + ") is available — run yank --update"
			if got := strings.Contains(view, hint); got != tt.want {
				t.Errorf("hint shown = %t, want %t:\n%s", got, tt.want, view)
			}
			if !tt.want && strings.Contains(view, "yank --update") {
				t.Errorf("some other --update hint is shown:\n%s", view)
			}
			if lineCount(view) > m.height {
				t.Errorf("error screen is %d rows in %d:\n%s", lineCount(view), m.height, view)
			}
		})
	}
}

// errorScreen is a probe that failed on a cached yt-dlp at width x height,
// with the background check started and not yet finished.
func errorScreen(t *testing.T, width, height int) Model {
	t.Helper()
	f := &fakes{probes: []probeOutcome{{err: hardErr()}}}
	m := testModel(t, f, width)
	m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
	m.hasBin, m.updateStarted = true, true
	m.bin = ytdlp.Result{Path: "/tmp/yt-dlp", Version: "2026.07.01", Source: ytdlp.SourceCache}
	m = typeURL(m, "https://example.com/v")
	m, cmd := step(m, keyOf(tea.KeyEnter))
	m, _ = advance(t, m, cmd)
	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	return m
}

func TestALateHintNeitherMovesTheErrorScreenNorOverflowsTheTerminal(t *testing.T) {
	const hint = "a newer yt-dlp"
	for _, width := range []int{80, 120, 40, 24} {
		// The heights round the reserve are where the rule bites, so they are
		// measured rather than assumed.
		reserve := errorScreen(t, width, 200).errorRows()
		if reserve == 0 {
			t.Fatalf("at width %d the error screen reserves nothing with the check started", width)
		}
		for _, tt := range []struct {
			height int
			shown  bool
		}{
			{200, true},
			{reserve + 1, true},
			{reserve, false},
			{reserve - 1, false},
			{reserve - 2, false},
		} {
			at := itoa(width) + "x" + itoa(tt.height)
			m := errorScreen(t, width, tt.height)
			before := m.View()
			titleRow := lineContaining(before, failedTitle)
			legendRow := lineContaining(before, "esc  back")

			m = send(m, updateDoneMsg{res: ytdlp.UpdateResult{Previous: "2026.07.01", Version: "2026.07.01", Latest: "2026.08.19.123456789"}})
			after := m.View()

			if shown := strings.Contains(after, hint); shown != tt.shown {
				t.Errorf("at %s hint shown = %t, want %t:\n%s", at, shown, tt.shown, after)
			}
			if r := lineContaining(after, failedTitle); r != titleRow {
				t.Errorf("at %s the hint moved the title from row %d to %d:\nbefore\n%s\nafter\n%s", at, titleRow, r, before, after)
			}
			if tt.shown && lineContaining(after, "esc  back") <= legendRow {
				t.Errorf("at %s the legend did not move down to make room for the hint", at)
			}
			if !tt.shown && after != before {
				t.Errorf("at %s a hint that does not fit changed the screen:\nbefore\n%s\nafter\n%s", at, before, after)
			}
			if lineCount(after) > max(tt.height, lineCount(before)) {
				t.Errorf("at %s the screen with the hint is %d rows in a %d-row terminal:\n%s", at, lineCount(after), tt.height, after)
			}
		}
	}
}

func TestAResolveFailureGetsNoUpdateHint(t *testing.T) {
	m := testModel(t, &fakes{}, 80)
	m = send(m, updateDoneMsg{res: ytdlp.UpdateResult{Latest: "2026.08.19"}})
	m = typeURL(m, "https://example.com/v")
	m, _ = step(m, keyOf(tea.KeyEnter))
	m = send(m, resolvedMsg{seq: m.seq, err: errString("could not fetch yt-dlp: boom")})
	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if strings.Contains(m.View(), "yank --update") {
		t.Errorf("a Resolve failure suggests --update:\n%s", m.View())
	}
}

// --- Run's ownership of the check --------------------------------------------

func TestBackgroundStopCancelsTheCheckAndWaitsForIt(t *testing.T) {
	bg := newBackground(context.Background())
	var returned atomic.Bool
	started := make(chan struct{})
	update := bg.wrap(func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		close(started)
		<-ctx.Done()
		// Removing a temp file, say.
		time.Sleep(100 * time.Millisecond)
		returned.Store(true)
		return ytdlp.UpdateResult{}, ctx.Err()
	})
	// The context the model passes is the program's, which is never cancelled
	// here: only stop may end the check.
	go update(context.Background(), ytdlp.Result{})
	<-started

	bg.stop(5 * time.Second)
	if !returned.Load() {
		t.Error("stop returned before the check it cancelled had finished")
	}
}

func TestBackgroundStopIsBoundedAndLaterCallsDoNotRun(t *testing.T) {
	bg := newBackground(context.Background())
	started := make(chan struct{})
	stuck := make(chan struct{})
	defer close(stuck)
	update := bg.wrap(func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		close(started)
		<-stuck
		return ytdlp.UpdateResult{}, nil
	})
	go update(context.Background(), ytdlp.Result{})
	<-started

	start := time.Now()
	bg.stop(200 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("stop waited %s on a check that ignores its context", elapsed)
	}

	ran := false
	late := bg.wrap(func(ctx context.Context, res ytdlp.Result) (ytdlp.UpdateResult, error) {
		ran = true
		return ytdlp.UpdateResult{}, nil
	})
	if _, err := late(context.Background(), ytdlp.Result{}); err == nil || !ytdlp.IsCancelled(err) {
		t.Errorf("a call after stop returned %v, want a cancellation", err)
	}
	if ran {
		t.Error("a check started after stop ran")
	}
	if bg.wrap(nil) != nil {
		t.Error("wrap(nil) is not nil: the model would start a check that does not exist")
	}
}
