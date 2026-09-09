package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// --- the input screen -------------------------------------------------------

func TestInputHintsInsteadOfAdvancing(t *testing.T) {
	// Empty and obviously-not-a-URL both stay on the input screen with
	// something to read, and neither reaches yt-dlp.
	cases := []struct {
		name  string
		typed string
	}{
		{"empty", ""},
		{"only spaces", "   "},
		{"a search phrase", "rick astley never gonna"},
		{"a bare word", "youtube"},
		{"a video id", "dQw4w9WgXcQ"},
		{"a local file", "file:///etc/passwd"},
		{"a scheme yt-dlp is not given", "ftp://example.com/a.mp4"},
		{"a scheme with no host", "https://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakes{}
			m := testModel(t, f, 80)
			m.hasBin = true
			m = typeURL(m, tc.typed)

			m = send(m, keyOf(tea.KeyEnter))

			if m.state != stateInput {
				t.Fatalf("state = %v, want stateInput", m.state)
			}
			if m.hint == "" {
				t.Fatal("no hint shown for input that cannot be a URL")
			}
			if !strings.Contains(m.View(), m.hint) {
				t.Fatalf("hint %q is not in the rendered screen:\n%s", m.hint, m.View())
			}
			if probes, _, _ := f.counts(); probes != 0 {
				t.Fatalf("Probe called %d times for %q, want 0", probes, tc.typed)
			}
		})
	}
}

func TestInputAcceptsAURL(t *testing.T) {
	cases := []struct {
		typed string
		want  string
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"  https://youtu.be/dQw4w9WgXcQ  ", "https://youtu.be/dQw4w9WgXcQ"},
		{"youtube.com/watch?v=dQw4w9WgXcQ", "https://youtube.com/watch?v=dQw4w9WgXcQ"},
		{"HTTP://Example.com/a", "HTTP://Example.com/a"},
	}
	for _, tc := range cases {
		t.Run(tc.typed, func(t *testing.T) {
			f := &fakes{}
			m := testModel(t, f, 80)
			m.hasBin = true
			m = typeURL(m, tc.typed)

			m = send(m, keyOf(tea.KeyEnter))

			if m.state != stateProbing {
				t.Fatalf("state = %v, want stateProbing", m.state)
			}
			if m.url != tc.want {
				t.Fatalf("url = %q, want %q", m.url, tc.want)
			}
			if m.hint != "" {
				t.Fatalf("hint = %q, want none", m.hint)
			}
		})
	}
}

func TestTypingClearsTheHint(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m.hint = "paste a video URL first"

	m = send(m, runes("h"))

	if m.hint != "" {
		t.Fatalf("hint = %q, want it cleared as soon as the user types", m.hint)
	}
}

// --- probing ----------------------------------------------------------------

func TestSubmitResolvesThenProbes(t *testing.T) {
	f := &fakes{
		resolveRes: ytdlp.Result{Path: "/tmp/yt-dlp", HasFFmpeg: true},
		probes:     []probeOutcome{{probe: newProbe(t, "A Video", "Someone")}},
		rows:       []ytdlp.Row{{Key: "video-1080", Label: "1080p  mp4  ~10 MB"}},
	}
	m := testModel(t, f, 80)
	m = typeURL(m, "https://example.com/v")

	m, cmd := step(m, keyOf(tea.KeyEnter))
	if m.step != stepResolving {
		t.Fatalf("step = %v, want stepResolving before a binary is known", m.step)
	}
	if !strings.Contains(m.View(), "checking yt-dlp") {
		t.Fatalf("probing screen does not name the step:\n%s", m.View())
	}

	resolved, ok := findMsg[resolvedMsg](collect(t, cmd))
	if !ok {
		t.Fatal("submitting did not run Resolve")
	}
	m, cmd = step(m, resolved)
	if !m.hasBin || m.bin.Path != "/tmp/yt-dlp" {
		t.Fatalf("binary = %+v, want it remembered", m.bin)
	}
	if m.step != stepFetchingInfo {
		t.Fatalf("step = %v, want stepFetchingInfo", m.step)
	}
	if !strings.Contains(m.View(), "fetching video info") {
		t.Fatalf("probing screen does not say what it is fetching:\n%s", m.View())
	}

	probed, ok := findMsg[probeDoneMsg](collect(t, cmd))
	if !ok {
		t.Fatal("resolving did not lead to a probe")
	}
	m = send(m, probed)
	if m.state != statePicker {
		t.Fatalf("state = %v, want statePicker", m.state)
	}
	if got := f.probeURLs; len(got) != 1 || got[0] != "https://example.com/v" {
		t.Fatalf("probed %v, want the submitted URL once", got)
	}
	if len(f.rankFFmpeg) != 1 || !f.rankFFmpeg[0] {
		t.Fatalf("Rank saw hasFFmpeg %v, want the value Resolve reported", f.rankFFmpeg)
	}
}

func TestSecondURLSkipsResolving(t *testing.T) {
	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, "A", "B")}}}
	m := testModel(t, f, 80)
	m.hasBin = true
	m.bin = ytdlp.Result{Path: "/tmp/yt-dlp"}
	m = typeURL(m, "https://example.com/v")

	m, cmd := step(m, keyOf(tea.KeyEnter))
	if m.step != stepFetchingInfo {
		t.Fatalf("step = %v, want stepFetchingInfo when the binary is known", m.step)
	}
	if _, ok := findMsg[probeDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("no probe ran")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveCalls != 0 {
		t.Fatalf("Resolve called %d times, want 0 once a binary is known", f.resolveCalls)
	}
}

func TestFirstRunNotice(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))

	if strings.Contains(m.View(), "first run") {
		t.Fatalf("the first-run notice is showing before the delay:\n%s", m.View())
	}

	m = send(m, firstRunNoticeMsg{seq: m.seq})
	if !strings.Contains(m.View(), "first run: fetching yt-dlp") {
		t.Fatalf("the first-run notice never appeared:\n%s", m.View())
	}
}

func TestFirstRunNoticeFromAnAbandonedAttemptIsIgnored(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))
	stale := m.seq

	m = send(m, keyOf(tea.KeyEsc))
	m = typeURL(m, "https://example.com/other")
	m = send(m, keyOf(tea.KeyEnter))

	m = send(m, firstRunNoticeMsg{seq: stale})
	if m.firstRun {
		t.Fatal("a notice from an abandoned attempt changed the current one")
	}
}

func TestResolveFailureLandsInError(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))

	m = send(m, resolvedMsg{seq: m.seq, err: errString("downloaded yt-dlp does not run")})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if !strings.Contains(m.View(), "does not run") {
		t.Fatalf("the reason is not on screen:\n%s", m.View())
	}
}

// --- the three probe sentinels ----------------------------------------------

func TestProbeSentinelsEachGetTheirOwnMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"playlist", fmtErrorf("%w: Mix (50 entries)", ytdlp.ErrPlaylist), "playlist"},
		{"live stream", fmtErrorf("%w: Some Stream", ytdlp.ErrLiveStream), "live stream"},
		{"no formats", fmtErrorf("%w: A Page", ytdlp.ErrNoFormatsExtracted), "no video on it"},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakes{}
			m := testModel(t, f, 80)
			m.hasBin = true
			m = typeURL(m, "https://example.com/v")
			m = send(m, keyOf(tea.KeyEnter))

			m = send(m, probeDoneMsg{seq: m.seq, err: tc.err})

			if m.state != stateError {
				t.Fatalf("state = %v, want stateError", m.state)
			}
			if !strings.Contains(m.errMsg, tc.want) {
				t.Fatalf("message %q does not mention %q", m.errMsg, tc.want)
			}
			seen[tc.name] = m.errMsg
		})
	}

	// Distinct situations with distinct next moves. Collapsing them into one
	// wording is the failure this test exists for.
	for a, msgA := range seen {
		for b, msgB := range seen {
			if a != b && msgA == msgB {
				t.Fatalf("%s and %s produced the same message %q", a, b, msgA)
			}
		}
	}
}

func TestExtractErrorShowsYtdlpsOwnWords(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))

	m = send(m, probeDoneMsg{seq: m.seq, err: hardErr()})

	if m.errMsg != "Video unavailable. This video is private." {
		t.Fatalf("errMsg = %q, want yt-dlp's own line", m.errMsg)
	}
}

func TestCancelledProbeIsNotAnError(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))

	m = send(m, probeDoneMsg{seq: m.seq, err: cancelledErr()})

	if m.state == stateError {
		t.Fatalf("a cancelled probe landed in stateError with %q", m.errMsg)
	}
	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
}

// --- the picker -------------------------------------------------------------

// pickerModel drives the real path as far as the picker: the probe command runs
// and its result is fed back, so the call counts a test reads are honest.
func pickerModel(t *testing.T, f *fakes, hasFFmpeg bool, rows []ytdlp.Row) Model {
	t.Helper()
	f.rows = rows
	if f.probes == nil {
		f.probes = []probeOutcome{{probe: newProbe(t, "Never Gonna Give You Up", "Rick Astley")}}
	}
	m := testModel(t, f, 80)
	m.hasBin = true
	m.bin = ytdlp.Result{Path: "/tmp/yt-dlp", HasFFmpeg: hasFFmpeg}
	m = typeURL(m, "https://example.com/v")
	m, cmd := step(m, keyOf(tea.KeyEnter))
	m, _ = advance(t, m, cmd)
	if m.state != statePicker {
		t.Fatalf("state = %v, want statePicker", m.state)
	}
	return m
}

func threeRows() []ytdlp.Row {
	return []ytdlp.Row{
		{Key: "video-1080", Label: "1080p60  mp4  ~142 MB", Args: []string{"-f", "137+ba"}},
		{Key: "video-720", Label: "720p  mp4  ~64 MB", Args: []string{"-f", "22"}},
		{Key: "audio-mp3", Label: "audio only  mp3", Args: []string{"-x"}, AudioOnly: true},
	}
}

func TestPickerNavigation(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())

	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m = send(m, keyOf(tea.KeyDown))
	m = send(m, runes("j"))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d after down and j, want 2", m.cursor)
	}
	// No wrapping: an over-press stays on the last row.
	m = send(m, runes("j"))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want it to stop at the last row", m.cursor)
	}
	m = send(m, runes("k"))
	m = send(m, keyOf(tea.KeyUp))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d after k and up, want 0", m.cursor)
	}
	m = send(m, keyOf(tea.KeyUp))
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want it to stop at the first row", m.cursor)
	}
}

func TestPickerDigitJumpsAndSelects(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())

	m = send(m, runes("2"))

	if m.state != stateDownloading {
		t.Fatalf("state = %v, want stateDownloading", m.state)
	}
	if m.row.Key != "video-720" {
		t.Fatalf("row = %q, want the second one", m.row.Key)
	}
}

func TestPickerDigitPastTheEndDoesNothing(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())

	m = send(m, runes("9"))

	if m.state != statePicker {
		t.Fatalf("state = %v, want statePicker", m.state)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want it unmoved", m.cursor)
	}
}

func TestPickerEnterStartsTheSelectedRow(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	m = send(m, runes("j"))

	m, cmd := step(m, keyOf(tea.KeyEnter))

	if m.state != stateDownloading {
		t.Fatalf("state = %v, want stateDownloading", m.state)
	}
	if _, ok := findMsg[downloadDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("enter did not run Download")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.downloadRows) != 1 || f.downloadRows[0].Key != "video-720" {
		t.Fatalf("downloaded %v, want the row under the cursor", f.downloadRows)
	}
}

func TestZeroRowsExplainsInsteadOfAnEmptyBox(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, false, nil)

	view := m.View()
	if !strings.Contains(view, "ffmpeg") {
		t.Fatalf("the empty picker does not say why it is empty:\n%s", view)
	}
	if !strings.Contains(view, "separate streams") {
		t.Fatalf("the empty picker does not explain the cause:\n%s", view)
	}
	// Enter must not start a download of a row that does not exist.
	m2, cmd := step(m, keyOf(tea.KeyEnter))
	if m2.state != statePicker {
		t.Fatalf("enter on an empty picker moved to %v", m2.state)
	}
	if cmd != nil {
		t.Fatal("enter on an empty picker produced a command")
	}
}

func TestFFmpegHintOnlyWhenAbsentAndRowsExist(t *testing.T) {
	t.Run("absent with rows", func(t *testing.T) {
		f := &fakes{}
		m := pickerModel(t, f, false, threeRows())
		if !strings.Contains(m.View(), "ffmpeg was not found") {
			t.Fatalf("no hint about the hidden rows:\n%s", m.View())
		}
	})
	t.Run("present with rows", func(t *testing.T) {
		f := &fakes{}
		m := pickerModel(t, f, true, threeRows())
		if strings.Contains(m.View(), "ffmpeg was not found") {
			t.Fatalf("the hint is showing although ffmpeg is present:\n%s", m.View())
		}
	})
	t.Run("absent with no rows", func(t *testing.T) {
		f := &fakes{}
		m := pickerModel(t, f, false, nil)
		// The empty-picker explanation replaces the one-line hint; showing
		// both would say the same thing twice.
		if strings.Contains(m.View(), "rows are hidden") {
			t.Fatalf("the one-line hint is showing next to the full explanation:\n%s", m.View())
		}
	})
}

// --- downloading ------------------------------------------------------------

// downloadingModel leaves the model on the download screen with Download really
// called once and its progress channel already closed, which is the state a
// running download is in from the model's side.
//
// The outcome is deliberately not fed back: each test injects the
// downloadDoneMsg it wants to exercise, and those are the same messages the
// command would have produced.
func downloadingModel(t *testing.T, f *fakes) Model {
	t.Helper()
	m := pickerModel(t, f, true, threeRows())
	m, cmd := step(m, keyOf(tea.KeyEnter))
	if m.state != stateDownloading {
		t.Fatalf("state = %v, want stateDownloading", m.state)
	}
	if _, ok := findMsg[downloadDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("entering the download screen did not run Download")
	}
	return m
}

func TestProgressUpdatesTheBarAndReschedulesTheDrain(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m, cmd := step(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase:           ytdlp.PhaseDownloading,
		Downloaded:      30_100_000,
		DownloadedKnown: true,
		Total:           64_000_000,
		TotalKnown:      true,
		Speed:           3_200_000,
		SpeedKnown:      true,
	}})

	view := m.View()
	for _, want := range []string{"30 MB / 64 MB", "3.2 MB/s", "47%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the download frame is missing %q:\n%s", want, view)
		}
	}

	// The drain must reschedule itself, or the second update never arrives.
	// The fixture's Download has already closed the channel, so the follow-up
	// answers with the closed message rather than blocking.
	if cmd == nil {
		t.Fatal("a progress update produced no follow-up drain")
	}
	if _, ok := cmd().(progressClosedMsg); !ok {
		t.Fatal("the follow-up command is not another drain of the progress channel")
	}
}

func TestProgressChannelClosingMidDownloadIsHandled(t *testing.T) {
	// waitProgress is the whole drain. Download closes the channel before
	// returning, so the closed case is on every single download's path.
	ch := make(chan ytdlp.Progress, 1)
	ch <- ytdlp.Progress{Phase: ytdlp.PhaseDownloading, Downloaded: 5, DownloadedKnown: true}
	cmd := waitProgress(ch, 7)

	msg := cmd()
	got, ok := msg.(progressMsg)
	if !ok {
		t.Fatalf("first drain produced %T, want progressMsg", msg)
	}
	if got.seq != 7 || got.p.Downloaded != 5 {
		t.Fatalf("drained %+v, want the value that was sent with seq 7", got)
	}

	close(ch)
	closedMsg := cmd()
	closed, ok := closedMsg.(progressClosedMsg)
	if !ok {
		t.Fatalf("draining a closed channel produced %T, want progressClosedMsg", closedMsg)
	}
	if closed.seq != 7 {
		t.Fatalf("closed seq = %d, want 7", closed.seq)
	}

	// And the model must not reschedule past it: a drain that kept going would
	// spin on a closed channel forever.
	f := &fakes{}
	m := downloadingModel(t, f)
	m2, cmd2 := step(m, progressClosedMsg{seq: m.seq})
	if cmd2 != nil {
		t.Fatal("the model rescheduled the drain after the channel closed")
	}
	if m2.state != stateDownloading {
		t.Fatalf("state = %v, want the download still running", m2.state)
	}
}

func TestPostProcessingMessages(t *testing.T) {
	cases := []struct {
		phase ytdlp.Phase
		want  string
	}{
		{ytdlp.PhaseMerging, "merging video and audio"},
		{ytdlp.PhaseConverting, "converting audio"},
	}
	for _, tc := range cases {
		t.Run(string(tc.phase), func(t *testing.T) {
			f := &fakes{}
			m := downloadingModel(t, f)
			m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
				Phase: tc.phase, Downloaded: 64_000_000, DownloadedKnown: true,
				Total: 64_000_000, TotalKnown: true,
			}})
			if !strings.Contains(m.View(), tc.want) {
				t.Fatalf("the post-processing frame does not say %q:\n%s", tc.want, m.View())
			}
		})
	}
}

func TestUnknownTotalRendersWithoutPretendingToKnow(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 1_500_000, DownloadedKnown: true,
	}})

	view := m.View()
	if !strings.Contains(view, "--%") {
		t.Fatalf("an unknown total rendered as a percentage:\n%s", view)
	}
	if !strings.Contains(view, "1.5 MB / ?") {
		t.Fatalf("an unknown total is not marked as unknown:\n%s", view)
	}
}

func TestEstimatedTotalIsHedged(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 1_000_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true, TotalEstimated: true,
	}})

	if !strings.Contains(m.View(), "/ ~64 MB") {
		t.Fatalf("an estimated total is stated as a fact:\n%s", m.View())
	}
}

func TestDownloadSuccessShowsThePath(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	infoJSON := m.probe.InfoJSONPath

	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{
		Path:      "/Users/x/Downloads/Never Gonna Give You Up.mp4",
		OutputDir: "/Users/x/Downloads",
	}})

	if m.state != stateDone {
		t.Fatalf("state = %v, want stateDone", m.state)
	}
	if !strings.Contains(m.View(), "Never Gonna Give You Up.mp4") {
		t.Fatalf("the done screen does not name the file:\n%s", m.View())
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times on the success path, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s is still there after a successful download", infoJSON)
	}
}

func TestWorkingDirFallbackIsAnnounced(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{
		Path:           "/work/Never Gonna Give You Up.mp4",
		OutputDir:      "/work",
		UsedWorkingDir: true,
	}})

	if !strings.Contains(m.View(), "home directory could not be found") {
		t.Fatalf("the fallback directory is not explained:\n%s", m.View())
	}

	// And the ordinary case says nothing about it.
	f2 := &fakes{}
	m2 := downloadingModel(t, f2)
	m2 = send(m2, downloadDoneMsg{seq: m2.seq, res: &ytdlp.DownloadResult{Path: "/Users/x/Downloads/a.mp4"}})
	if strings.Contains(m2.View(), "home directory could not be found") {
		t.Fatalf("the fallback note showed for an ordinary download:\n%s", m2.View())
	}
}

func TestCancelledDownloadIsNotAnError(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m = send(m, downloadDoneMsg{seq: m.seq, err: cancelledErr()})

	if m.state == stateError {
		t.Fatalf("a cancelled download landed in stateError with %q", m.errMsg)
	}
	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	if m.errMsg != "" {
		t.Fatalf("errMsg = %q, want nothing: cancelling is not a failure", m.errMsg)
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times on the cancel path, want 1", n)
	}
}

func TestFailedDownloadLandsInError(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	infoJSON := m.probe.InfoJSONPath

	m = send(m, downloadDoneMsg{seq: m.seq, err: hardErr()})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if !strings.Contains(m.View(), "This video is private") {
		t.Fatalf("the failure does not say what yt-dlp said:\n%s", m.View())
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times on the error path, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived a failed download", infoJSON)
	}
}

func TestNoDestinationGetsItsOwnWording(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m = send(m, downloadDoneMsg{seq: m.seq, err: fmtErrorf("downloading: %w", ytdlp.ErrNoDestination)})

	if !strings.Contains(m.errMsg, "never said where it saved") {
		t.Fatalf("errMsg = %q, want the missing-destination wording", m.errMsg)
	}
}

// --- the stale-info retry ---------------------------------------------------

func TestStaleInfoRetriesOnceAndSucceeds(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	stalePath := m.probe.InfoJSONPath
	m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 30_100_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true,
	}})

	m, retryCmd := step(m, downloadDoneMsg{seq: m.seq, err: staleErr()})

	if m.state != stateDownloading {
		t.Fatalf("state = %v, want the download screen to stay up during the retry", m.state)
	}
	if !m.retrying {
		t.Fatal("the model is not showing that it is re-probing")
	}
	if !strings.Contains(m.View(), "expired") {
		t.Fatalf("the retry is not explained on screen:\n%s", m.View())
	}
	// The bar belonged to the attempt that just failed. Leaving it up shows a
	// percentage nothing is working towards.
	if m.hasProg {
		t.Error("the failed attempt's progress survived into the retry")
	}
	if !strings.Contains(m.View(), "--%") {
		t.Errorf("the bar still shows the failed attempt's position:\n%s", m.View())
	}
	if n := f.cleanups(); n != 0 {
		t.Fatalf("Cleanup called %d times before the fresh probe arrived, want 0", n)
	}

	fresh := newProbe(t, "Never Gonna Give You Up", "Rick Astley")
	f.mu.Lock()
	f.probes = append(f.probes, probeOutcome{probe: fresh})
	f.mu.Unlock()
	m, cmd := advance(t, m, retryCmd)

	if m.probe != fresh {
		t.Fatal("the model did not adopt the fresh probe")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times after the re-probe, want 1 for the stale result", n)
	}
	if !gone(stalePath) {
		t.Fatalf("the stale info-json %s was not removed", stalePath)
	}

	if _, ok := findMsg[downloadDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("the retry did not run Download again")
	}
	f.mu.Lock()
	rows := append([]ytdlp.Row(nil), f.downloadRows...)
	f.mu.Unlock()
	if len(rows) != 2 {
		t.Fatalf("Download called %d times, want 2", len(rows))
	}
	if rows[0].Key != rows[1].Key {
		t.Fatalf("the retry used row %q, want the same row %q", rows[1].Key, rows[0].Key)
	}

	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/Users/x/Downloads/a.mp4"}})
	if m.state != stateDone {
		t.Fatalf("state = %v, want stateDone", m.state)
	}
	// Two ProbeResults existed, so two files had to go.
	if n := f.cleanups(); n != 2 {
		t.Fatalf("Cleanup called %d times over a retried download, want 2", n)
	}
	if !gone(fresh.InfoJSONPath) {
		t.Fatalf("the fresh info-json %s survived", fresh.InfoJSONPath)
	}
}

func TestStaleInfoRetriesOnlyOnce(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	f.mu.Lock()
	f.probes = append(f.probes, probeOutcome{probe: newProbe(t, "A", "B")})
	f.mu.Unlock()

	m, retryCmd := step(m, downloadDoneMsg{seq: m.seq, err: staleErr()})
	m, launch := advance(t, m, retryCmd)
	if _, ok := findMsg[downloadDoneMsg](collect(t, launch)); !ok {
		t.Fatal("the retry did not run Download again")
	}

	// Second stale error on the same download. There is nothing left to try.
	m = send(m, downloadDoneMsg{seq: m.seq, err: staleErr()})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError after the retry also failed", m.state)
	}
	probes, downloads, cleanups := f.counts()
	if probes != 2 {
		t.Fatalf("Probe called %d times, want 2: one original and one retry", probes)
	}
	if downloads != 2 {
		t.Fatalf("Download called %d times, want 2", downloads)
	}
	if cleanups != 2 {
		t.Fatalf("Cleanup called %d times, want 2: both ProbeResults", cleanups)
	}
}

func TestStaleRetryBudgetIsPerDownload(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m = send(m, downloadDoneMsg{seq: m.seq, err: staleErr()})
	m = send(m, probeDoneMsg{seq: m.seq, probe: newProbe(t, "A", "B"), retry: true})
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})

	// A new download the user asked for gets its own retry.
	m = send(m, keyOf(tea.KeyEnter))
	m = typeURL(m, "https://example.com/v2")
	m = send(m, keyOf(tea.KeyEnter))
	m = send(m, probeDoneMsg{seq: m.seq, probe: newProbe(t, "A", "B")})
	m = send(m, keyOf(tea.KeyEnter))
	if m.retried {
		t.Fatal("the retry budget was carried over from the previous download")
	}
	m = send(m, downloadDoneMsg{seq: m.seq, err: staleErr()})
	if !m.retrying {
		t.Fatal("a fresh download did not get its own retry")
	}
}

func TestFailedReProbeReportsItAndCleansTheStaleResult(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	stalePath := m.probe.InfoJSONPath

	m = send(m, downloadDoneMsg{seq: m.seq, err: staleErr()})
	m = send(m, probeDoneMsg{seq: m.seq, err: hardErr(), retry: true})

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times, want 1 for the stale result", n)
	}
	if !gone(stalePath) {
		t.Fatalf("the stale info-json %s survived a failed re-probe", stalePath)
	}
}

// --- esc, quit and reset ----------------------------------------------------

func TestEscFromProbingReturnsToInputAndCancels(t *testing.T) {
	f := &fakes{}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))
	runCtx := m.runCtx

	m = send(m, keyOf(tea.KeyEsc))

	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	if runCtx.Err() == nil {
		t.Fatal("esc did not cancel the attempt's context")
	}
}

func TestAProbeThatArrivesAfterEscIsCleanedUp(t *testing.T) {
	// The user pressed esc while the extraction was in flight. It finishes
	// anyway, and the ProbeResult it carries owns a file on disk that nothing
	// else will ever hear about.
	f := &fakes{}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))
	stale := m.seq

	m = send(m, keyOf(tea.KeyEsc))

	late := newProbe(t, "Too Late", "Nobody")
	m = send(m, probeDoneMsg{seq: stale, probe: late})

	if m.state != stateInput {
		t.Fatalf("state = %v, want the late result ignored", m.state)
	}
	if m.probe != nil {
		t.Fatal("the model adopted a probe from an abandoned attempt")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times for the orphaned probe, want 1", n)
	}
	if !gone(late.InfoJSONPath) {
		t.Fatalf("the orphaned info-json %s leaked", late.InfoJSONPath)
	}
}

func TestEscFromPickerReturnsToInputAndCleansUp(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	infoJSON := m.probe.InfoJSONPath

	m = send(m, keyOf(tea.KeyEsc))

	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times going back to input, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived a trip back to the input screen", infoJSON)
	}
	if m.rows != nil {
		t.Fatal("the row list survived a trip back to the input screen")
	}
}

func TestEscFromDownloadingCancelsAndCleansUp(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	infoJSON := m.probe.InfoJSONPath
	runCtx := m.runCtx

	m = send(m, keyOf(tea.KeyEsc))

	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	if runCtx.Err() == nil {
		t.Fatal("esc did not cancel the download's context")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times cancelling a download, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived a cancelled download", infoJSON)
	}
	if m.errMsg != "" {
		t.Fatalf("errMsg = %q after a cancel, want nothing", m.errMsg)
	}
}

func TestEscFromErrorReturnsToInput(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyEnter} {
		f := &fakes{}
		m := downloadingModel(t, f)
		m = send(m, downloadDoneMsg{seq: m.seq, err: hardErr()})
		if m.state != stateError {
			t.Fatalf("state = %v, want stateError", m.state)
		}

		m = send(m, keyOf(key))

		if m.state != stateInput {
			t.Fatalf("%v from the error screen went to %v, want stateInput", key, m.state)
		}
		if m.errMsg != "" {
			t.Fatalf("errMsg = %q, want it cleared", m.errMsg)
		}
	}
}

func TestDoneEnterResetsEverything(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, progressMsg{seq: m.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 5, DownloadedKnown: true,
	}})
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})
	before := m

	m = send(m, keyOf(tea.KeyEnter))

	if m.state != stateInput {
		t.Fatalf("state = %v, want stateInput", m.state)
	}
	switch {
	case m.probe != nil:
		t.Error("a probe survived the reset")
	case m.rows != nil:
		t.Error("the row list survived the reset")
	case m.result != nil:
		t.Error("the download result survived the reset")
	case m.errMsg != "":
		t.Error("an error message survived the reset")
	case m.hasProg:
		t.Error("progress survived the reset")
	case m.prog != (ytdlp.Progress{}):
		t.Error("a stale progress value survived the reset")
	case m.retried || m.retrying:
		t.Error("the retry state survived the reset")
	case m.progCh != nil:
		t.Error("the progress channel survived the reset")
	case m.input.Value() != "":
		t.Error("the typed URL survived the reset")
	case m.url != "":
		t.Error("the submitted URL survived the reset")
	case m.hint != "":
		t.Error("a hint survived the reset")
	}

	// What is true across attempts is kept: the terminal size and the binary.
	if m.width != before.width || m.height != before.height {
		t.Errorf("size = %dx%d, want it kept across the reset", m.width, m.height)
	}
	if !m.hasBin || m.bin.Path != before.bin.Path {
		t.Errorf("binary = %+v, want it kept across the reset", m.bin)
	}
	// And the cleanup already done is not repeated on a probe that is gone.
	if n := f.cleanups(); n != 1 {
		t.Errorf("Cleanup called %d times over a download and a reset, want 1", n)
	}
	// Nothing from the finished attempt can address the fresh one.
	if m.seq == before.seq {
		t.Error("the reset kept the finished attempt's sequence number")
	}
}

func TestDoneQuits(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})

	_, cmd := step(m, runes("q"))

	if cmd == nil {
		t.Fatal("q on the done screen produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q on the done screen did not quit")
	}
}

func TestCtrlCQuitsFromAnywhereAndCleansUp(t *testing.T) {
	t.Run("from the picker", func(t *testing.T) {
		f := &fakes{}
		m := pickerModel(t, f, true, threeRows())
		infoJSON := m.probe.InfoJSONPath

		_, cmd := step(m, keyOf(tea.KeyCtrlC))

		if cmd == nil {
			t.Fatal("ctrl+c produced no command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("ctrl+c did not quit")
		}
		if n := f.cleanups(); n != 1 {
			t.Fatalf("Cleanup called %d times on quit, want 1", n)
		}
		if !gone(infoJSON) {
			t.Fatalf("the info-json %s leaked on quit", infoJSON)
		}
	})

	t.Run("from a download", func(t *testing.T) {
		// A download in flight is cancelled at once and then waited for, so
		// runDownload gets to remove its own partial files. See
		// TestCtrlCDuringADownloadWaitsForItToStop.
		f := &fakes{}
		m := downloadingModel(t, f)
		runCtx := m.runCtx

		m, _ = step(m, keyOf(tea.KeyCtrlC))

		if runCtx.Err() == nil {
			t.Fatal("ctrl+c left the download's context running")
		}
		_, cmd := step(m, downloadDoneMsg{seq: m.seq, err: cancelledErr()})
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("ctrl+c did not quit once the download had stopped")
		}
		if n := f.cleanups(); n != 1 {
			t.Fatalf("Cleanup called %d times on quit, want 1", n)
		}
	})

	t.Run("from the input screen", func(t *testing.T) {
		f := &fakes{}
		m := testModel(t, f, 80)
		_, cmd := step(m, keyOf(tea.KeyCtrlC))
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("ctrl+c did not quit")
		}
	})
}

// --- context ----------------------------------------------------------------

func TestCancellingTheProgramContextCancelsTheAttempt(t *testing.T) {
	f := &fakes{}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, f.deps())
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")
	m = send(m, keyOf(tea.KeyEnter))

	cancel()

	if m.runCtx.Err() == nil {
		t.Fatal("cancelling the program context left the attempt running")
	}
}

func TestUpdateDoesNotRunYtdlpInline(t *testing.T) {
	// Submitting must return a command, not do the work. Anything run inside
	// Update blocks the loop, so the screen would freeze for the length of an
	// extraction and esc would never be read.
	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, "A", "B")}}, rows: threeRows()}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, "https://example.com/v")

	m, cmd := step(m, keyOf(tea.KeyEnter))
	if probes, _, _ := f.counts(); probes != 0 {
		t.Fatalf("Probe ran inside Update (%d calls)", probes)
	}
	if cmd == nil {
		t.Fatal("submitting produced no command")
	}
	if _, ok := findMsg[probeDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("the command did not run Probe")
	}

	// Same for the download.
	m = send(m, probeDoneMsg{seq: m.seq, probe: newProbe(t, "A", "B")})
	m, cmd = step(m, keyOf(tea.KeyEnter))
	if _, downloads, _ := f.counts(); downloads != 0 {
		t.Fatalf("Download ran inside Update (%d calls)", downloads)
	}
	if _, ok := findMsg[downloadDoneMsg](collect(t, cmd)); !ok {
		t.Fatal("the command did not run Download")
	}
}
