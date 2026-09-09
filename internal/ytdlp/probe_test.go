package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures ---------------------------------------------------------------

// infoFixture is a trimmed but real-shaped `yt-dlp -J` document: the fields
// yank reads, in the order and with the null-ability yt-dlp actually emits.
const infoFixture = `{
  "id": "dQw4w9WgXcQ",
  "title": "Rick Astley - Never Gonna Give You Up (Official Video)",
  "uploader": "Rick Astley",
  "duration": 213.0,
  "webpage_url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "extractor_key": "Youtube",
  "is_live": false,
  "formats": [
    {
      "format_id": "251",
      "ext": "webm",
      "vcodec": "none",
      "acodec": "opus",
      "protocol": "https",
      "abr": 134.464,
      "tbr": 134.464,
      "filesize": 3610916
    },
    {
      "format_id": "137",
      "ext": "mp4",
      "vcodec": "avc1.640028",
      "acodec": "none",
      "protocol": "https",
      "height": 1080,
      "width": 1920,
      "fps": 25.0,
      "tbr": 2503.616,
      "filesize_approx": 66658816
    }
  ]
}
`

// nullDurationFixture is the shape a live-ish or partly extracted page returns:
// yt-dlp emits duration as JSON null rather than omitting it.
const nullDurationFixture = `{
  "title": "A stream that already ended",
  "uploader": "Someone",
  "duration": null,
  "webpage_url": "https://example.com/v/1",
  "extractor_key": "Generic",
  "is_live": false,
  "formats": [{"format_id": "0", "ext": "mp4", "vcodec": "avc1", "acodec": "mp4a"}]
}
`

const liveFixture = `{
  "title": "24/7 lofi radio",
  "uploader": "Chill",
  "duration": null,
  "webpage_url": "https://www.youtube.com/watch?v=live",
  "extractor_key": "Youtube",
  "is_live": true,
  "formats": [{"format_id": "96", "ext": "mp4", "vcodec": "avc1", "acodec": "mp4a"}]
}
`

const noFormatsFixture = `{
  "title": "Members only",
  "uploader": "A Channel",
  "duration": 61.0,
  "webpage_url": "https://www.youtube.com/watch?v=nofmt",
  "extractor_key": "Youtube",
  "is_live": false,
  "formats": []
}
`

// playlistFixture is the envelope a real playlist URL produces: _type playlist,
// an entries array, and no formats at all. Measured against yt-dlp 2026.08.19
// on https://www.youtube.com/playlist?list=PL..., which also exits 1 when one
// of its entries is unavailable while still emitting the whole document.
const playlistFixture = `{
  "id": "PLbpi6ZahtOH6Blw3RGYpWkSByi_T7Rygb",
  "_type": "playlist",
  "title": "Top Trending Videos of the Week",
  "uploader": "YouTube",
  "webpage_url": "https://www.youtube.com/playlist?list=PLbpi6ZahtOH6Blw3RGYpWkSByi_T7Rygb",
  "extractor_key": "YoutubeTab",
  "entries": [
    {"id": "a", "title": "One", "formats": [{"format_id": "18"}]},
    {"id": "b", "title": "Two", "formats": [{"format_id": "18"}]},
    {"id": "c", "title": "Three", "formats": [{"format_id": "18"}]}
  ]
}
`

// --- fake yt-dlp ------------------------------------------------------------

// fakeYTDLP writes a /bin/sh stand-in for yt-dlp that prints fixed stdout and
// stderr and exits with a chosen code. It returns the script path and the file
// its argv gets recorded in, so the exec path is exercised without the network.
func fakeYTDLP(t *testing.T, stdout, stderr string, exitCode int) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	outFile := filepath.Join(dir, "stdout")
	errFile := filepath.Join(dir, "stderr")
	argsFile = filepath.Join(dir, "argv")
	for name, body := range map[string]string{outFile: stdout, errFile: stderr} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf("printf '%%s\\n' \"$@\" > '%s'\ncat '%s'\ncat '%s' >&2\nexit %d\n",
		argsFile, outFile, errFile, exitCode)
	return writeScript(t, body), argsFile
}

// tempDirWithNoInfoJSON points os.CreateTemp at a private directory and returns
// a func reporting which info-json files are sitting in it.
func tempDirWithNoInfoJSON(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return func() []string {
		left, err := filepath.Glob(filepath.Join(dir, "yank-info-*.json"))
		if err != nil {
			t.Fatal(err)
		}
		return left
	}
}

const probeTestURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

// --- the happy path ---------------------------------------------------------

func TestProbeReturnsAPopulatedVideoInfo(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, argsFile := fakeYTDLP(t, infoFixture, "", 0)

	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("probeInfo() error = %v", err)
	}
	t.Cleanup(func() { res.Cleanup() })

	if got, want := res.Info.Title, "Rick Astley - Never Gonna Give You Up (Official Video)"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if got, want := res.Info.Uploader, "Rick Astley"; got != want {
		t.Errorf("Uploader = %q, want %q", got, want)
	}
	if res.Info.Duration == nil {
		t.Fatal("Duration = nil, want 213")
	}
	if got := *res.Info.Duration; got != 213 {
		t.Errorf("*Duration = %v, want 213", got)
	}
	if got, want := res.Info.ExtractorKey, "Youtube"; got != want {
		t.Errorf("ExtractorKey = %q, want %q", got, want)
	}
	if res.Info.IsLive {
		t.Error("IsLive = true, want false")
	}
	if got, want := len(res.Info.Formats), 2; got != want {
		t.Fatalf("len(Formats) = %d, want %d", got, want)
	}
	if got, want := res.Info.Formats[1].Height, 1080; got != want {
		t.Errorf("Formats[1].Height = %d, want %d", got, want)
	}
	if got, want := res.Info.Formats[1].FilesizeApprox, int64(66658816); got != want {
		t.Errorf("Formats[1].FilesizeApprox = %d, want %d", got, want)
	}
	// probe.go decodes straight into RawFormat, so a field added there arrives
	// with no change to the decoder. Ranking reads this one.
	if got, want := res.Info.Formats[1].Protocol, "https"; got != want {
		t.Errorf("Formats[1].Protocol = %q, want %q", got, want)
	}

	// The document on disk must be the raw stdout, byte for byte: yt-dlp reads
	// back fields VideoInfo does not model when given --load-info-json.
	saved, err := os.ReadFile(res.InfoJSONPath)
	if err != nil {
		t.Fatalf("reading InfoJSONPath: %v", err)
	}
	if string(saved) != infoFixture {
		t.Errorf("saved info-json differs from yt-dlp stdout:\n%s", saved)
	}

	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	wantArgv := strings.Join(infoArgs(probeTestURL), "\n") + "\n"
	if string(argv) != wantArgv {
		t.Errorf("argv = %q, want %q", argv, wantArgv)
	}

	if n := len(leftovers()); n != 1 {
		t.Errorf("%d info-json files in TMPDIR before cleanup, want 1", n)
	}
}

func TestProbeKeepsANullDurationNil(t *testing.T) {
	tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, nullDurationFixture, "", 0)

	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("probeInfo() error = %v", err)
	}
	t.Cleanup(func() { res.Cleanup() })

	if res.Info.Duration != nil {
		t.Errorf("Duration = %v, want nil: an unknown duration must not become 0", *res.Info.Duration)
	}
}

// --- the refusals -----------------------------------------------------------

func TestProbeRejectsLiveStreams(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, liveFixture, "", 0)

	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err == nil {
		res.Cleanup()
		t.Fatal("probeInfo() returned no error for a live stream")
	}
	if !errors.Is(err, ErrLiveStream) {
		t.Errorf("error = %v, want it to wrap ErrLiveStream", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil alongside an error", res)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

func TestProbeRejectsAnEmptyFormatList(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, noFormatsFixture, "", 0)

	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err == nil {
		res.Cleanup()
		t.Fatal("probeInfo() returned no error for an empty formats array")
	}
	if !errors.Is(err, ErrNoFormatsExtracted) {
		t.Errorf("error = %v, want it to wrap ErrNoFormatsExtracted", err)
	}
	if errors.Is(err, ErrLiveStream) || errors.Is(err, ErrPlaylist) {
		t.Errorf("error = %v, want it distinct from the other refusals", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// --- finding 4: a playlist URL is not an empty video ------------------------

func TestProbeRejectsAPlaylistURLAsAPlaylist(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	playlistURL := "https://www.youtube.com/playlist?list=PLbpi6ZahtOH6Blw3RGYpWkSByi_T7Rygb"
	path, _ := fakeYTDLP(t, playlistFixture, "", 0)

	res, err := probeInfo(t.Context(), path, playlistURL, time.Minute, 300*time.Millisecond)
	if err == nil {
		res.Cleanup()
		t.Fatal("probeInfo() returned no error for a playlist URL")
	}
	if !errors.Is(err, ErrPlaylist) {
		t.Fatalf("error = %v, want it to wrap ErrPlaylist", err)
	}
	// Reverting the fix lands this on ErrNoFormatsExtracted, which tells the
	// user their playlist is empty. It is not; it has entries.
	if errors.Is(err, ErrNoFormatsExtracted) {
		t.Errorf("error = %v, want it not to claim the playlist has no formats", err)
	}
	if !strings.Contains(err.Error(), "Top Trending Videos of the Week") {
		t.Errorf("error = %v, want it to name the playlist", err)
	}
	if !strings.Contains(err.Error(), "3 entries") {
		t.Errorf("error = %v, want it to say how many entries were listed", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// A real playlist exits 1 when one of its entries is unavailable, while still
// emitting the whole document. The per-entry complaint must not become the
// user-facing answer: what they typed was a playlist.
func TestProbeReportsAPlaylistEvenWhenYTDLPExitsNonZero(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	stderr := "ERROR: [youtube] InDJvcWtTCw: This video is not available\n"
	path, _ := fakeYTDLP(t, playlistFixture, stderr, 1)

	_, err := probeInfo(t.Context(), path, "https://www.youtube.com/playlist?list=PL", time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error")
	}
	if !errors.Is(err, ErrPlaylist) {
		t.Errorf("error = %v, want it to wrap ErrPlaylist rather than the entry's complaint", err)
	}
	var ee *ExtractError
	if errors.As(err, &ee) {
		t.Errorf("error = %v, want it not to be an *ExtractError blaming one entry", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

func TestRawInfoCollection(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantEntries int
		wantOK      bool
	}{
		{name: "a plain video has no _type", body: infoFixture, wantOK: false},
		{name: "playlist", body: playlistFixture, wantEntries: 3, wantOK: true},
		{
			name:        "multi_video",
			body:        `{"_type": "multi_video", "title": "A show", "entries": [{"id":"a"},{"id":"b"}]}`,
			wantEntries: 2, wantOK: true,
		},
		{
			name:   "an explicit video _type is still a video",
			body:   `{"_type": "video", "title": "x", "formats": [{"format_id":"18"}]}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := decodeString(t, tt.body)
			entries, ok := raw.collection()
			if ok != tt.wantOK {
				t.Fatalf("collection() ok = %v, want %v", ok, tt.wantOK)
			}
			if entries != tt.wantEntries {
				t.Errorf("collection() entries = %d, want %d", entries, tt.wantEntries)
			}
		})
	}
}

// decodeString runs body through decodeInfoJSON the same way probeInfo does.
func decodeString(t *testing.T, body string) rawInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := decodeInfoJSON(f, maxInfoJSONBytes)
	if err != nil {
		t.Fatalf("decodeInfoJSON() error = %v", err)
	}
	return raw
}

func TestProbeSurfacesStderrOnNonZeroExit(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	stderr := "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable. This video is private\n"
	path, _ := fakeYTDLP(t, "", stderr, 1)

	_, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error")
	}
	var ee *ExtractError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v (%T), want an *ExtractError", err, err)
	}
	if got, want := ee.Message, "Video unavailable. This video is private"; got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
	if got, want := ee.Stderr, strings.TrimRight(stderr, "\n"); got != want {
		t.Errorf("Stderr = %q, want the full text %q", got, want)
	}
	if ee.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", ee.ExitCode)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// --- temp file ownership ----------------------------------------------------

func TestProbeCleanupRemovesTheInfoJSONOnSuccess(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, infoFixture, "", 0)

	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("probeInfo() error = %v", err)
	}
	if _, err := os.Stat(res.InfoJSONPath); err != nil {
		t.Fatalf("info-json missing on the success path: %v", err)
	}

	if err := res.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(res.InfoJSONPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat after Cleanup() = %v, want ErrNotExist", err)
	}
	// Idempotent: the UI calls it again when the user goes back for a new URL.
	if err := res.Cleanup(); err != nil {
		t.Errorf("second Cleanup() error = %v, want nil", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

func TestProbeRemovesTheInfoJSONOnCancellation(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path := writeScript(t, "sleep 30\n")

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := probeInfo(ctx, path, probeTestURL, time.Minute, 200*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if IsStaleInfo(err) {
		t.Errorf("IsStaleInfo(%v) = true, want false: a cancellation is not a stale URL", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind after cancellation: %v", left)
	}
}

func TestProbeRemovesTheInfoJSONOnTimeout(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path := writeScript(t, "sleep 30\n")

	_, err := probeInfo(t.Context(), path, probeTestURL, 200*time.Millisecond, 200*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to say it timed out", err)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind after a timeout: %v", left)
	}
}

func TestProbeRemovesTheInfoJSONWhenYTDLPCannotStart(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	missing := filepath.Join(t.TempDir(), "not-there")

	if _, err := probeInfo(t.Context(), missing, probeTestURL, time.Minute, 200*time.Millisecond); err == nil {
		t.Fatal("probeInfo() returned no error for a missing executable")
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// --- the WaitDelay trap -----------------------------------------------------

func TestProbeSurvivesALingeringChild(t *testing.T) {
	tempDirWithNoInfoJSON(t)
	// The yt-dlp_macos re-exec shape: print the document, leave a child holding
	// the stderr pipe, exit 0. Wait reports ErrWaitDelay; the run succeeded.
	dir := t.TempDir()
	outFile := filepath.Join(dir, "stdout")
	if err := os.WriteFile(outFile, []byte(infoFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeScript(t, fmt.Sprintf("cat '%s'\nsleep 3 &\nexit 0\n", outFile))

	start := time.Now()
	res, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("probeInfo() error = %v, want the lingering child ignored", err)
	}
	t.Cleanup(func() { res.Cleanup() })

	if got, want := res.Info.Uploader, "Rick Astley"; got != want {
		t.Errorf("Uploader = %q, want %q", got, want)
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("elapsed = %s, want at least the wait delay: the pipe was not actually held open", elapsed)
	}
}

// --- classifyInfoRun --------------------------------------------------------

func TestClassifyInfoRun(t *testing.T) {
	waitDelayErr := fmt.Errorf("wrapped: %w", exec.ErrWaitDelay)

	tests := []struct {
		name         string
		haveJSON     bool
		runErr       error
		callerCtxErr error
		runCtxErr    error
		wantErr      bool
		check        func(t *testing.T, err error)
	}{
		{name: "clean exit", haveJSON: true, runErr: nil},
		{name: "wait delay with a complete document", haveJSON: true, runErr: waitDelayErr},
		{
			name: "wait delay with nothing collected", runErr: waitDelayErr, wantErr: true,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, exec.ErrWaitDelay) {
					t.Errorf("error = %v, want it to wrap exec.ErrWaitDelay", err)
				}
			},
		},
		{
			name: "caller cancelled", runErr: errors.New("signal: killed"),
			callerCtxErr: context.Canceled, runCtxErr: context.Canceled, wantErr: true,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want it to wrap context.Canceled", err)
				}
			},
		},
		{
			// Finding 5. os/exec can return ErrWaitDelay on a cancelled run
			// when Cancel finds the process already done. Checking the
			// ErrWaitDelay shortcut first would report success here, and a
			// caller told "cancelled" would drop the ProbeResult without
			// calling Cleanup, leaking the info-json.
			name:     "wait delay that arrived alongside a cancellation",
			haveJSON: true, runErr: waitDelayErr,
			callerCtxErr: context.Canceled, runCtxErr: context.Canceled, wantErr: true,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want cancellation to outrank the ErrWaitDelay shortcut", err)
				}
			},
		},
		{
			name: "our own timeout", runErr: errors.New("signal: killed"),
			runCtxErr: context.DeadlineExceeded, wantErr: true,
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "timed out") {
					t.Errorf("error = %v, want it to say it timed out", err)
				}
			},
		},
		{
			name: "could not start", runErr: errors.New("fork/exec: permission denied"), wantErr: true,
			check: func(t *testing.T, err error) {
				var ee *ExtractError
				if errors.As(err, &ee) {
					t.Errorf("error = %v, want it not to be an *ExtractError: yt-dlp never ran", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyInfoRun(probeTestURL, tt.haveJSON, tt.runErr, tt.callerCtxErr, tt.runCtxErr, "", time.Minute)
			if tt.wantErr != (err != nil) {
				t.Fatalf("classifyInfoRun() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, err)
			}
		})
	}
}

// Finding 3. A signal we did not send — Gatekeeper, the OOM killer, an operator
// — says nothing about the URL. Reporting it as *ExtractError tells a caller
// the URL has been adjudicated and makes it reject a perfectly good one
// forever. classifyProbe in binary.go draws exactly this line.
func TestClassifyInfoRunTreatsASignalKillAsInconclusive(t *testing.T) {
	killed := signalKillError(t)

	err := classifyInfoRun(probeTestURL, false, killed, nil, nil, "", time.Minute)
	if err == nil {
		t.Fatal("classifyInfoRun() returned no error")
	}
	var ee *ExtractError
	if errors.As(err, &ee) {
		t.Errorf("error = %v is an *ExtractError with ExitCode %d, want an inconclusive error: no signal kill blames the URL",
			err, ee.ExitCode)
	}
	if !strings.Contains(err.Error(), "killed") {
		t.Errorf("error = %v, want it to say yt-dlp was killed", err)
	}
	if !errors.Is(err, killed) {
		t.Errorf("error = %v, want it to wrap the underlying exec error", err)
	}
}

// The other half of the same line: a real non-zero exit status is a verdict.
func TestClassifyInfoRunTreatsANonZeroExitAsAVerdict(t *testing.T) {
	err := exec.Command("/bin/sh", "-c", "exit 2").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected a status-2 ExitError, got %v", err)
	}

	got := classifyInfoRun(probeTestURL, false, err, nil, nil, "ERROR: Unsupported URL: x", time.Minute)
	var ee *ExtractError
	if !errors.As(got, &ee) {
		t.Fatalf("error = %v (%T), want an *ExtractError", got, got)
	}
	if ee.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", ee.ExitCode)
	}
	if ee.ExitCode < 0 {
		t.Error("an ExtractError must never carry a negative exit code")
	}
}

// --- finding 2: an ExtractError must never stringify to nothing -------------

func TestFriendlyErrorNeverReturnsEmpty(t *testing.T) {
	// Every one of these survives primaryErrorLine and is eaten by stripNoise.
	// yt-dlp formats unconditionally as "ERROR: {msg}" and can raise with an
	// empty message; the maxStderrBytes cap landing mid-line does it too.
	for _, stderr := range []string{
		"ERROR:",
		"ERROR: ",
		"ERROR: [youtube] abc123:",
		"ERROR: [youtube] abc123: ",
		"ERROR: [youtube]",
		"ERROR: [youtube] ",
	} {
		t.Run(fmt.Sprintf("%q", stderr), func(t *testing.T) {
			got := friendlyError(stderr, 1)
			if got == "" {
				t.Fatal("friendlyError() = \"\", want the exit status named instead")
			}
			if want := "yt-dlp exited with status 1"; got != want {
				t.Errorf("friendlyError() = %q, want %q", got, want)
			}
		})
	}
}

func TestExtractErrorAlwaysStringifiesToSomething(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, "", "ERROR: [youtube] abc123: \n", 1)

	_, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error")
	}
	var ee *ExtractError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v (%T), want an *ExtractError", err, err)
	}
	if ee.Message == "" {
		t.Error("ExtractError.Message = \"\", want it never empty")
	}
	// This is what the UI actually prints.
	if wrapped := fmt.Errorf("probe failed: %w", ee).Error(); wrapped == "probe failed: " {
		t.Errorf("wrapped error = %q, want a reason after the colon", wrapped)
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// --- stderr prefix stripping ------------------------------------------------

func TestFriendlyErrorStripsNoisePrefixes(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "youtube error with a video id",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable",
			want:   "Video unavailable",
		},
		{
			name:   "extractor name carrying a colon",
			stderr: "ERROR: [youtube:tab] @someone: This channel does not have a videos tab",
			want:   "This channel does not have a videos tab",
		},
		{
			name:   "generic extractor with a None id",
			stderr: "ERROR: [generic] None: Unable to download webpage: HTTP Error 404: Not Found",
			want:   "Unable to download webpage: HTTP Error 404: Not Found",
		},
		{
			name:   "error with no extractor tag",
			stderr: "ERROR: Unsupported URL: https://example.com/not-a-video",
			want:   "Unsupported URL: https://example.com/not-a-video",
		},
		{
			name:   "extractor tag with no id",
			stderr: "ERROR: [youtube] Unable to download API page",
			want:   "Unable to download API page",
		},
		{
			name:   "the ERROR line is picked out of the surrounding chatter",
			stderr: "[youtube] Extracting URL: https://www.youtube.com/watch?v=x\n[youtube] x: Downloading webpage\nERROR: [youtube] x: Private video. Sign in if you've been granted access to this video\n",
			want:   "Private video. Sign in if you've been granted access to this video",
		},
		{
			name:   "no recognised prefix passes through unchanged",
			stderr: "yt-dlp: error: You must provide at least one URL",
			want:   "yt-dlp: error: You must provide at least one URL",
		},
		{
			name:   "an optparse usage error falls back to the last line",
			stderr: "Usage: yt-dlp [OPTIONS] URL [URL...]\n\nyt-dlp: error: no such option: --nope",
			want:   "yt-dlp: error: no such option: --nope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := friendlyError(tt.stderr, 1); got != tt.want {
				t.Errorf("friendlyError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFriendlyErrorWithNoStderrNamesTheExitStatus(t *testing.T) {
	if got, want := friendlyError("", 2), "yt-dlp exited with status 2"; got != want {
		t.Errorf("friendlyError(\"\", 2) = %q, want %q", got, want)
	}
	if got, want := friendlyError("", -1), "yt-dlp was killed"; got != want {
		t.Errorf("friendlyError(\"\", -1) = %q, want %q", got, want)
	}
}

// --- finding 1: stale classification must not read the wrapped chain --------

func TestIsStaleInfo(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "expired googlevideo url",
			err:  &ExtractError{Message: "unable to download video data: HTTP Error 403: Forbidden", Stderr: "ERROR: unable to download video data: HTTP Error 403: Forbidden", ExitCode: 1},
			want: true,
		},
		{
			name: "gone",
			err:  &ExtractError{Message: "unable to download video data: HTTP Error 410: Gone", Stderr: "ERROR: unable to download video data: HTTP Error 410: Gone", ExitCode: 1},
			want: true,
		},
		{
			name: "extractor says the url expired",
			err:  &ExtractError{Message: "The download URL has expired, please refresh the page", Stderr: "ERROR: [somesite] 42: The download URL has expired, please refresh the page", ExitCode: 1},
			want: true,
		},
		{
			name: "fragment gone mid-download",
			err:  &ExtractError{Message: "fragment 1 not found, unable to continue", Stderr: "ERROR: fragment 1 not found, unable to continue", ExitCode: 1},
			want: true,
		},
		{
			name: "the signature is only in the untouched stderr",
			err:  &ExtractError{Message: "yt-dlp exited with status 1", Stderr: "WARNING: something\nHTTP Error 403: Forbidden", ExitCode: 1},
			want: true,
		},
		{
			name: "wrapped extract error",
			err:  fmt.Errorf("downloading: %w", &ExtractError{Message: "HTTP Error 403: Forbidden", Stderr: "HTTP Error 403: Forbidden", ExitCode: 1}),
			want: true,
		},
		{
			name: "a video that is genuinely gone is not stale",
			err:  &ExtractError{Message: "Video unavailable. This video is private", Stderr: "ERROR: [youtube] x: Video unavailable. This video is private", ExitCode: 1},
			want: false,
		},
		{
			name: "an unsupported url is not stale",
			err:  &ExtractError{Message: "Unsupported URL: https://example.com/x", Stderr: "ERROR: Unsupported URL: https://example.com/x", ExitCode: 1},
			want: false,
		},
		{name: "a cancellation is not stale", err: context.Canceled, want: false},
		{name: "an unrelated error is not stale", err: errors.New("no such file or directory"), want: false},
		{
			// Finding 1, first trigger. exec.ErrWaitDelay stringifies to
			// "exec: WaitDelay expired before I/O complete", which contains a
			// stale signature. classifyInfoRun wraps it verbatim on the
			// no-usable-output branch. That failure is permanent and perfectly
			// reproducible — retrying it burns the whole budget.
			name: "an ErrWaitDelay with no output is not stale",
			err:  classifyInfoRun(probeTestURL, false, fmt.Errorf("x: %w", exec.ErrWaitDelay), nil, nil, "", time.Minute),
			want: false,
		},
		{
			name: "bare exec.ErrWaitDelay is not stale",
			err:  exec.ErrWaitDelay,
			want: false,
		},
		{
			// Finding 1, second trigger. Anything we interpolate into a message
			// is ours, not yt-dlp's, and a user can paste whatever they like.
			name: "a URL containing a signature does not make an error stale",
			err:  fmt.Errorf("could not read yt-dlp output for %s: malformed", "https://site.com/expired/http-error-403/1"),
			want: false,
		},
		{
			name: "a plain error carrying a signature is not stale either",
			err:  errors.New("probing x: HTTP Error 403: Forbidden"),
			want: false,
		},
		{
			// A signal kill is no longer an *ExtractError (finding 3), so even
			// if its text mentioned a signature it could not read as stale.
			name: "an inconclusive kill is not stale",
			err:  fmt.Errorf("probing x: yt-dlp was killed: %w", errors.New("signal: killed")),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStaleInfo(tt.err); got != tt.want {
				t.Errorf("IsStaleInfo(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// The end-to-end shape of finding 1's second trigger: a URL the user pasted,
// carrying a signature word, on a failure that is not an ExtractError at all.
func TestProbeFailureOnASignatureBearingURLIsNotStale(t *testing.T) {
	tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, "not json at all\n", "", 0)
	url := "https://site.example/videos/expired/http-error-403"

	_, err := probeInfo(t.Context(), path, url, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeInfo() returned no error for malformed output")
	}
	if !strings.Contains(err.Error(), url) {
		t.Fatalf("error = %v, want it to name the URL (that is what makes the trap real)", err)
	}
	if IsStaleInfo(err) {
		t.Errorf("IsStaleInfo(%v) = true, want false: the signature came from the user's URL", err)
	}
}

// --- decoding ---------------------------------------------------------------

func TestProbeRejectsMalformedOutput(t *testing.T) {
	leftovers := tempDirWithNoInfoJSON(t)
	path, _ := fakeYTDLP(t, "not json at all\n", "", 0)

	if _, err := probeInfo(t.Context(), path, probeTestURL, time.Minute, 300*time.Millisecond); err == nil {
		t.Fatal("probeInfo() returned no error for malformed output")
	}
	if left := leftovers(); len(left) != 0 {
		t.Errorf("info-json left behind: %v", left)
	}
}

// Finding 6. The size limit is a post-hoc reject: nothing bounds the write, so
// the oversized document is on disk in full by the time it is refused.
func TestDecodeInfoJSONRejectsAnOversizedDocumentAfterItIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.json")
	body := []byte(infoFixture)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	limit := int64(len(body) - 1)
	if _, err := decodeInfoJSON(f, limit); err == nil {
		t.Fatal("decodeInfoJSON() accepted a document over the limit")
	} else if !strings.Contains(err.Error(), "over the") {
		t.Errorf("error = %v, want it to name the limit", err)
	}
	// The whole document reached the disk before the limit was consulted.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != int64(len(body)) {
		t.Errorf("file size = %d, want the full %d: nothing bounds the write", st.Size(), len(body))
	}

	if _, err := decodeInfoJSON(f, int64(len(body))); err != nil {
		t.Errorf("decodeInfoJSON() at exactly the limit error = %v, want it accepted", err)
	}
}

func TestDecodeInfoJSONRejectsAnEmptyDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := decodeInfoJSON(f, maxInfoJSONBytes); err == nil {
		t.Fatal("decodeInfoJSON() accepted an empty document")
	}
}

func TestCappedBufferStopsAtTheLimit(t *testing.T) {
	b := &cappedBuffer{limit: 8}
	n, err := b.Write([]byte("12345"))
	if n != 5 || err != nil {
		t.Fatalf("Write() = %d, %v, want 5, nil", n, err)
	}
	// A short write must still be reported as fully accepted, or io.Copy fails.
	n, err = b.Write([]byte("67890"))
	if n != 5 || err != nil {
		t.Fatalf("Write() = %d, %v, want 5, nil", n, err)
	}
	if got, want := b.String(), "12345678"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
