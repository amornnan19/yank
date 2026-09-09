package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- helpers ----------------------------------------------------------------

// testWaitDelay is the WaitDelay every download test injects. Short enough that
// a hung fake does not hold the suite up, long enough that a script which is
// merely slow to exit is not cut off.
const testWaitDelay = 500 * time.Millisecond

// testJob builds a downloadJob against a fake yt-dlp, with the output directory
// pointed at a temp dir so nothing lands in the user's home.
func testJob(t *testing.T, ytdlpPath string, updates chan<- Progress) downloadJob {
	t.Helper()
	return downloadJob{
		ytdlpPath: ytdlpPath,
		infoJSON:  filepath.Join(t.TempDir(), "info.json"),
		row:       Row{Key: "video-1080", Args: []string{"-f", "137+ba/137/b[height<=1080]"}},
		outDir:    t.TempDir(),
		updates:   updates,
		waitDelay: testWaitDelay,
	}
}

// collectProgress drains ch on a goroutine of its own and returns a func that
// waits for the channel to close and hands back everything that arrived.
//
// Updates are conflated on purpose, so a test may assert on the last one, on
// whether some state was seen, or on ordering — never on a tick count.
func collectProgress(ch <-chan Progress) func() []Progress {
	var mu sync.Mutex
	var got []Progress
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range ch {
			mu.Lock()
			got = append(got, p)
			mu.Unlock()
		}
	}()
	return func() []Progress {
		<-done
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// hasPhase reports whether any update carried the given phase.
func hasPhase(got []Progress, phase Phase) bool {
	for _, p := range got {
		if p.Phase == phase {
			return true
		}
	}
	return false
}

// await polls until cond holds, reporting whether it did before the deadline.
// It takes no *testing.T because the cancelling goroutines below use it, and
// t.Fatalf may only be called from the goroutine running the test.
func await(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// awaitFile reports whether path showed up before the deadline.
func awaitFile(path string) bool {
	return await(10*time.Second, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

// waitFor polls until cond holds or the deadline passes, failing the test if it
// does not. Test goroutine only.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	if !await(within, cond) {
		t.Fatalf("timed out after %s waiting for %s", within, what)
	}
}

// readPID reads a pid a fake yt-dlp recorded for one of its children.
func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the child pid: %v", err)
	}
	pid := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("child pid = %q, want a number", raw)
	}
	return pid
}

// killPID cleans up a process a test deliberately left running. Portable so the
// file still compiles for Windows.
func killPID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// runCancelledDownload runs a fake yt-dlp that sets a scene, creates marker to
// say it is ready, and then hangs; the context is cancelled the moment marker
// appears. It returns the error the download ended with.
func runCancelledDownload(t *testing.T, outDir, script, marker string) error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	job := testJob(t, writeScript(t, script), nil)
	job.outDir = outDir

	go func() {
		// Cancel even on a timeout: the test's own assertions say what went
		// wrong far better than a Fatalf from the wrong goroutine would.
		awaitFile(marker)
		cancel()
	}()

	_, err := runDownload(ctx, job)
	return err
}

// processAlive reports whether pid still names a running process. Signal 0 asks
// the question without sending anything.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// --- argv -------------------------------------------------------------------

func TestDownloadArgsHaveTheAgreedShape(t *testing.T) {
	row := Row{Args: []string{"-f", "137+ba/137/b[height<=1080]"}}
	got := downloadArgs("/tmp/info.json", row, "/home/u/Downloads")

	want := []string{
		"--load-info-json", "/tmp/info.json",
		"--no-playlist",
		"-o", filepath.Join("/home/u/Downloads", "%(title)s.%(ext)s"),
		"-f", "137+ba/137/b[height<=1080]",
		"--newline",
		"--progress-template", "download:%(progress.downloaded_bytes)s/%(progress.total_bytes)s/%(progress.total_bytes_estimate)s/%(progress.speed)s",
	}
	if len(got) != len(want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// D1: the row picked the format. Adding a selector of our own here would let the
// file disagree with the label the user read.
func TestDownloadArgsAddNoSecondFormatSelector(t *testing.T) {
	row := Row{Args: []string{"-x", "--audio-format", "mp3"}}
	got := downloadArgs("/tmp/info.json", row, "/out")

	for i, a := range got {
		if a == "-f" || a == "--format" || strings.HasPrefix(a, "--format-sort") {
			t.Errorf("args[%d] = %q: the row asked for %q and yank added a format selector", i, a, row.Args)
		}
		if strings.ContainsAny(a, `"'`) {
			t.Errorf("args[%d] = %q contains a quote character; every argument is one argv slot (D4)", i, a)
		}
	}
	// The row's own fragment survives, in order and unaltered.
	joined := strings.Join(got, "\x00")
	if !strings.Contains(joined, strings.Join(row.Args, "\x00")) {
		t.Errorf("args = %q, want the row's %q spliced in verbatim", got, row.Args)
	}
}

func TestDownloadArgsRecordedByTheFakeMatchDownloadArgs(t *testing.T) {
	path, argsFile := fakeYTDLP(t, "[download] Destination: /out/Video.mp4\n", "", 0)
	job := testJob(t, path, nil)

	if _, err := runDownload(t.Context(), job); err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}

	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	want := strings.Join(downloadArgs(job.infoJSON, job.row, job.outDir), "\n") + "\n"
	if string(argv) != want {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

// --- progress parsing -------------------------------------------------------

func TestParseProgressLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Progress
		ok   bool
	}{
		{
			name: "every field present",
			line: "1048576/66658816/66658816/3145728.5",
			want: Progress{
				Phase:      PhaseDownloading,
				Downloaded: 1048576, DownloadedKnown: true,
				Total: 66658816, TotalKnown: true,
				Speed: 3145728.5, SpeedKnown: true,
			},
			ok: true,
		},
		{
			name: "every field NA",
			line: "NA/NA/NA/NA",
			want: Progress{Phase: PhaseDownloading},
			ok:   true,
		},
		{
			name: "no total, only an estimate",
			line: "512/NA/1000000/NA",
			want: Progress{
				Phase:      PhaseDownloading,
				Downloaded: 512, DownloadedKnown: true,
				Total: 1000000, TotalKnown: true, TotalEstimated: true,
			},
			ok: true,
		},
		{
			name: "an exact total outranks the estimate beside it",
			line: "512/900000/1000000/NA",
			want: Progress{
				Phase:      PhaseDownloading,
				Downloaded: 512, DownloadedKnown: true,
				Total: 900000, TotalKnown: true,
			},
			ok: true,
		},
		{
			name: "first tick: zero bytes and no speed yet",
			line: "0/1000/NA/NA",
			want: Progress{
				Phase: PhaseDownloading,
				// Zero downloaded is a real answer, not an unknown.
				Downloaded: 0, DownloadedKnown: true,
				Total: 1000, TotalKnown: true,
			},
			ok: true,
		},
		{
			name: "yt-dlp renders a size as a float",
			line: "1024.0/2048.0/NA/512.25",
			want: Progress{
				Phase:      PhaseDownloading,
				Downloaded: 1024, DownloadedKnown: true,
				Total: 2048, TotalKnown: true,
				Speed: 512.25, SpeedKnown: true,
			},
			ok: true,
		},
		{
			name: "downloaded NA, total known",
			line: "NA/2048/NA/NA",
			want: Progress{
				Phase: PhaseDownloading,
				Total: 2048, TotalKnown: true,
			},
			ok: true,
		},
		{name: "not a number", line: "1024/oops/NA/NA"},
		{name: "too few fields", line: "1024/2048/NA"},
		{name: "too many fields", line: "1024/2048/NA/NA/NA"},
		{name: "no fields at all", line: "downloading"},
		{name: "a destination line is not progress", line: "[download] Destination: /a/b/c.mp4"},
		{name: "empty", line: ""},
		{name: "negative", line: "-1/2048/NA/NA"},
		{name: "not a number, infinite", line: "1024/Inf/NA/NA"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProgressLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("parseProgressLine(%q) ok = %v, want %v (got %+v)", tc.line, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("parseProgressLine(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

// A field past int64's range is unknown, not a negative size: the conversion
// would be implementation-defined and the number would vouch for itself.
func TestParseProgressLineRejectsAnOutOfRangeByteCount(t *testing.T) {
	got, ok := parseProgressLine("99999999999999999999999999/NA/NA/NA")
	if !ok {
		t.Fatal("parseProgressLine() ok = false, want the line accepted with an unknown count")
	}
	if got.DownloadedKnown {
		t.Errorf("DownloadedKnown = true with Downloaded = %d, want the out-of-range count reported as unknown", got.Downloaded)
	}
}

func TestProgressPercent(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
		ok   bool
	}{
		{name: "half", p: Progress{Downloaded: 50, DownloadedKnown: true, Total: 100, TotalKnown: true}, want: 50, ok: true},
		{name: "unknown total", p: Progress{Downloaded: 50, DownloadedKnown: true}},
		{name: "unknown downloaded", p: Progress{Total: 100, TotalKnown: true}},
		{name: "zero total", p: Progress{Downloaded: 0, DownloadedKnown: true, Total: 0, TotalKnown: true}},
		{
			name: "an estimate that came in low is clamped",
			p:    Progress{Downloaded: 120, DownloadedKnown: true, Total: 100, TotalKnown: true, TotalEstimated: true},
			want: 100, ok: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.p.Percent()
			if ok != tc.ok {
				t.Fatalf("Percent() ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("Percent() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- progress over a real run -----------------------------------------------

func TestDownloadStreamsProgressToTheUI(t *testing.T) {
	stdout := "[download] Destination: /out/Video.mp4\n" +
		"0/1000/NA/NA\n" +
		"500/1000/NA/1234.5\n" +
		"1000/1000/NA/2000\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	res, err := runDownload(t.Context(), testJob(t, path, updates))
	if err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}
	if res.Path != "/out/Video.mp4" {
		t.Errorf("Path = %q, want %q", res.Path, "/out/Video.mp4")
	}

	all := got()
	if len(all) == 0 {
		t.Fatal("no progress updates reached the UI")
	}
	last := all[len(all)-1]
	if last.Phase != PhaseDownloading {
		t.Errorf("last phase = %q, want %q", last.Phase, PhaseDownloading)
	}
	if !last.DownloadedKnown || last.Downloaded != 1000 {
		t.Errorf("last downloaded = %d (known %v), want 1000", last.Downloaded, last.DownloadedKnown)
	}
	if !last.TotalKnown || last.Total != 1000 {
		t.Errorf("last total = %d (known %v), want 1000", last.Total, last.TotalKnown)
	}
	if !last.SpeedKnown || last.Speed != 2000 {
		t.Errorf("last speed = %v (known %v), want 2000", last.Speed, last.SpeedKnown)
	}
	if pct, ok := last.Percent(); !ok || pct != 100 {
		t.Errorf("last Percent() = %v, %v, want 100, true", pct, ok)
	}
}

// A tick yt-dlp could not fill in is not a broken download.
func TestDownloadHandlesNAInEveryProgressField(t *testing.T) {
	stdout := "[download] Destination: /out/Live.mp4\n" +
		"NA/NA/NA/NA\n" +
		"1024/NA/NA/NA\n" +
		"2048/NA/9000/512.0\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	if _, err := runDownload(t.Context(), testJob(t, path, updates)); err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}

	all := got()
	if len(all) == 0 {
		t.Fatal("no progress updates reached the UI")
	}
	last := all[len(all)-1]
	if !last.DownloadedKnown || last.Downloaded != 2048 {
		t.Errorf("last downloaded = %d (known %v), want 2048", last.Downloaded, last.DownloadedKnown)
	}
	if !last.TotalKnown || last.Total != 9000 || !last.TotalEstimated {
		t.Errorf("last total = %d (known %v, estimated %v), want 9000 from the estimate", last.Total, last.TotalKnown, last.TotalEstimated)
	}
}

// A line yt-dlp printed that is not a progress tick — a warning, a plugin
// banner, a template yank did not ask for — is skipped, not fatal.
func TestDownloadIgnoresAMalformedProgressLine(t *testing.T) {
	stdout := "[download] Destination: /out/Video.mp4\n" +
		"500/1000/NA/NA\n" +
		"garbage/not/a/tick\n" +
		"//x//\n" +
		"1000/1000/NA/NA\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	res, err := runDownload(t.Context(), testJob(t, path, updates))
	if err != nil {
		t.Fatalf("runDownload() error = %v, want the malformed line ignored", err)
	}
	if res.Path != "/out/Video.mp4" {
		t.Errorf("Path = %q, want %q", res.Path, "/out/Video.mp4")
	}

	all := got()
	if len(all) == 0 {
		t.Fatal("no progress updates reached the UI")
	}
	last := all[len(all)-1]
	if last.Downloaded != 1000 || !last.DownloadedKnown {
		t.Errorf("last downloaded = %d (known %v), want 1000: the good tick after the junk was lost",
			last.Downloaded, last.DownloadedKnown)
	}
}

// --- post-processing --------------------------------------------------------

func TestDownloadReportsMergingAfterTheDownloadFinishes(t *testing.T) {
	stdout := "[download] Destination: /out/Video.f137.mp4\n" +
		"1000/1000/NA/NA\n" +
		"[download] Destination: /out/Video.f251.webm\n" +
		"200/200/NA/NA\n" +
		`[Merger] Merging formats into "/out/Video.mp4"` + "\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	res, err := runDownload(t.Context(), testJob(t, path, updates))
	if err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}

	all := got()
	if !hasPhase(all, PhaseMerging) {
		t.Errorf("phases seen = %v, want one of them to be %q: a bar frozen at 100%% reads as a hang",
			phases(all), PhaseMerging)
	}
	if hasPhase(all, PhaseConverting) {
		t.Errorf("phases seen = %v, want merging kept distinct from converting", phases(all))
	}
	if res.Path != "/out/Video.mp4" {
		t.Errorf("Path = %q, want the merged file", res.Path)
	}
}

func TestDownloadReportsConvertingForAnAudioExtraction(t *testing.T) {
	stdout := "[download] Destination: /out/Song.webm\n" +
		"1000/1000/NA/NA\n" +
		"[ExtractAudio] Destination: /out/Song.mp3\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	res, err := runDownload(t.Context(), testJob(t, path, updates))
	if err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}

	all := got()
	if !hasPhase(all, PhaseConverting) {
		t.Errorf("phases seen = %v, want one of them to be %q", phases(all), PhaseConverting)
	}
	if hasPhase(all, PhaseMerging) {
		t.Errorf("phases seen = %v, want converting kept distinct from merging", phases(all))
	}
	// yt-dlp deletes the .webm it extracted from, so returning it would name a
	// file that is no longer there.
	if res.Path != "/out/Song.mp3" {
		t.Errorf("Path = %q, want the transcoded file", res.Path)
	}
}

// The post-processing update carries the numbers the download ended on, so a
// full bar stays full instead of jumping back to zero.
func TestMergingKeepsTheLastDownloadNumbers(t *testing.T) {
	stdout := "[download] Destination: /out/Video.f137.mp4\n" +
		"1000/1000/NA/NA\n" +
		`[Merger] Merging formats into "/out/Video.mp4"` + "\n"
	path, _ := fakeYTDLP(t, stdout, "", 0)

	updates := make(chan Progress)
	got := collectProgress(updates)

	if _, err := runDownload(t.Context(), testJob(t, path, updates)); err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}

	all := got()
	last := all[len(all)-1]
	if last.Phase != PhaseMerging {
		t.Fatalf("last phase = %q, want %q", last.Phase, PhaseMerging)
	}
	if pct, ok := last.Percent(); !ok || pct != 100 {
		t.Errorf("Percent() during merging = %v, %v, want 100, true", pct, ok)
	}
}

// --- the saved path ---------------------------------------------------------

func TestDownloadCapturesTheDestination(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "a plain download",
			stdout: "[download] Destination: /out/Video.mp4\n1000/1000/NA/NA\n",
			want:   "/out/Video.mp4",
		},
		{
			name: "the merger path wins over the streams it consumed",
			stdout: "[download] Destination: /out/Video.f137.mp4\n" +
				"[download] Destination: /out/Video.f251.webm\n" +
				`[Merger] Merging formats into "/out/Video.mp4"` + "\n",
			want: "/out/Video.mp4",
		},
		{
			name:   "a path with spaces and punctuation survives",
			stdout: "[download] Destination: /out/A Video: part 2 [HD].mp4\n",
			want:   "/out/A Video: part 2 [HD].mp4",
		},
		{
			name:   "a merged path with spaces survives",
			stdout: `[Merger] Merging formats into "/out/A Video: part 2 [HD].mkv"` + "\n",
			want:   "/out/A Video: part 2 [HD].mkv",
		},
		{
			name:   "nothing new to download",
			stdout: "[download] /out/Video.mp4 has already been downloaded\n",
			want:   "/out/Video.mp4",
		},
		{
			// yt-dlp deletes the stream it extracted from, so returning the
			// .webm would name a file that is no longer there.
			name: "the extracted path wins over the stream it was read from",
			stdout: "[download] Destination: /out/Song.webm\n" +
				"[ExtractAudio] Destination: /out/Song.mp3\n",
			want: "/out/Song.mp3",
		},
		{
			// Post-processors run in order and each deletes what it read.
			// Merger runs before ExtractAudio, so when both appear the merged
			// file has been consumed and the extracted one is what is on disk.
			name: "the extracted path wins over the merged one it was read from",
			stdout: "[download] Destination: /out/V.f137.mp4\n" +
				"[download] Destination: /out/V.f251.webm\n" +
				`[Merger] Merging formats into "/out/V.mkv"` + "\n" +
				"[ExtractAudio] Destination: /out/V.mp3\n",
			want: "/out/V.mp3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := fakeYTDLP(t, tc.stdout, "", 0)
			res, err := runDownload(t.Context(), testJob(t, path, nil))
			if err != nil {
				t.Fatalf("runDownload() error = %v", err)
			}
			if res.Path != tc.want {
				t.Errorf("Path = %q, want %q", res.Path, tc.want)
			}
		})
	}
}

// A successful run that never said where the file went is a failure the user
// can see, not a DownloadResult with an empty Path.
func TestDownloadWithoutADestinationIsAnError(t *testing.T) {
	path, _ := fakeYTDLP(t, "1000/1000/NA/NA\n", "", 0)

	res, err := runDownload(t.Context(), testJob(t, path, nil))
	if err == nil {
		t.Fatalf("runDownload() returned no error, result = %+v", res)
	}
	if !errors.Is(err, ErrNoDestination) {
		t.Errorf("error = %v, want it to wrap ErrNoDestination", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil alongside an error", res)
	}
}

// --- cancellation -----------------------------------------------------------

// slowDownloader writes a fake yt-dlp that announces a destination, creates the
// partial files a real one would, and then sits there until it is killed.
func slowDownloader(t *testing.T, dest string) string {
	t.Helper()
	body := fmt.Sprintf(
		"echo '[download] Destination: %s'\n"+
			": > '%s.part'\n"+
			": > '%s.ytdl'\n"+
			"echo '0/1000/NA/NA'\n"+
			"sleep 30\n",
		dest, dest, dest)
	return writeScript(t, body)
}

func TestDownloadCancellationIsCancelledNotFailed(t *testing.T) {
	outDir := t.TempDir()
	dest := filepath.Join(outDir, "Video.mp4")
	path := slowDownloader(t, dest)

	ctx, cancel := context.WithCancel(t.Context())
	job := testJob(t, path, nil)
	job.outDir = outDir

	go func() {
		awaitFile(dest + ".part")
		cancel()
	}()

	res, err := runDownload(ctx, job)
	if err == nil {
		t.Fatalf("runDownload() returned no error, result = %+v", res)
	}
	if !IsCancelled(err) {
		t.Errorf("IsCancelled(%v) = false, want true", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	// The user pressed stop. Nothing about the URL or the file was adjudicated,
	// so the verdict types must stay out of it.
	var ee *ExtractError
	if errors.As(err, &ee) {
		t.Errorf("error = %v, want a cancellation not to be an *ExtractError blaming the URL", err)
	}
	if IsStaleInfo(err) {
		t.Errorf("IsStaleInfo(%v) = true, want false: a cancellation is not a stale info-json", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil alongside an error", res)
	}
}

func TestDownloadCancellationRemovesThePartialFiles(t *testing.T) {
	outDir := t.TempDir()
	dest := filepath.Join(outDir, "Video.mp4")
	path := slowDownloader(t, dest)

	ctx, cancel := context.WithCancel(t.Context())
	job := testJob(t, path, nil)
	job.outDir = outDir

	go func() {
		awaitFile(dest + ".part")
		awaitFile(dest + ".ytdl")
		cancel()
	}()

	if _, err := runDownload(ctx, job); !IsCancelled(err) {
		t.Fatalf("runDownload() error = %v, want a cancellation", err)
	}

	for _, leftover := range []string{dest + ".part", dest + ".ytdl"} {
		if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after cancellation (stat err = %v)", leftover, err)
		}
	}
}

// Process.Kill on yt-dlp alone leaves the ffmpeg it spawned running, burning a
// core on a merge nobody wants. The whole group has to go.
func TestDownloadCancellationKillsTheWholeProcessGroup(t *testing.T) {
	outDir := t.TempDir()
	dest := filepath.Join(outDir, "Video.mp4")
	pidFile := filepath.Join(outDir, "child.pid")

	// The backgrounded sleep stands in for ffmpeg: a child of yt-dlp, in the
	// same process group, which survives a signal aimed at yt-dlp alone.
	body := fmt.Sprintf(
		"echo '[download] Destination: %s'\n"+
			"sleep 30 &\n"+
			"echo $! > '%s'\n"+
			"sleep 30\n",
		dest, pidFile)
	path := writeScript(t, body)

	ctx, cancel := context.WithCancel(t.Context())
	job := testJob(t, path, nil)
	job.outDir = outDir

	go func() {
		awaitFile(pidFile)
		cancel()
	}()

	if _, err := runDownload(ctx, job); !IsCancelled(err) {
		t.Fatalf("runDownload() error = %v, want a cancellation", err)
	}

	pid := readPID(t, pidFile)

	waitFor(t, 5*time.Second, fmt.Sprintf("the child process %d to be gone", pid), func() bool {
		return !processAlive(pid)
	})
}

// Cmd.Wait reaps the child and only then rendezvouses with the cancel watcher,
// so Cancel can fire on a pid that is already gone — and reaping leaves Pid
// numeric, so nothing about the pid itself says so. Signalling the negated pid
// at that point goes to whatever the kernel has since recycled it for.
//
// The survivor here is a detector, not something worth protecting: it keeps the
// process group alive after its leader is reaped, so a group signal that should
// not have been sent has something to land on. In production that group belongs
// to an unrelated process.
func TestKillTreeDoesNotSignalTheGroupOfAReapedProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(writeScript(t, fmt.Sprintf("sleep 30 &\necho $! > '%s'\nexit 0\n", pidFile)))
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	waitForFile(t, pidFile)
	pid := readPID(t, pidFile)
	t.Cleanup(func() { killPID(pid) })
	if !processAlive(pid) {
		t.Fatal("the survivor is already gone; this test would prove nothing")
	}

	if err := killTree(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("killTree() = %v, want os.ErrProcessDone for an already-reaped process", err)
	}
	// Long enough that a signal that was sent would have landed.
	if await(500*time.Millisecond, func() bool { return !processAlive(pid) }) {
		t.Error("killTree signalled the process group of a reaped pid; that group can belong to an unrelated process")
	}
}

// Only os.ErrProcessDone means the process is gone. Unix answers signal 0 on a
// live process with nil, so a plain "err != nil" reads correctly here and is
// wrong on Windows, where a live process answers with EWINDOWS — killTree would
// then decline to kill anything at all. No unix test can see that, so the rule
// gets a predicate of its own and this table.
func TestProcessIsDone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unix, alive: signal 0 succeeded", err: nil, want: false},
		{name: "reaped", err: os.ErrProcessDone, want: true},
		{name: "reaped, wrapped", err: fmt.Errorf("probing pid: %w", os.ErrProcessDone), want: true},
		{
			// What os.Process.Signal returns on Windows for anything but Kill,
			// including for a process that is very much alive.
			name: "windows, alive: the signal itself is unsupported",
			err:  errors.New("not supported by windows"),
			want: false,
		},
		{name: "alive but not ours to signal", err: syscall.EPERM, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := processIsDone(tc.err); got != tc.want {
				t.Errorf("processIsDone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// waitForFile blocks until path exists. Test goroutine only.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	if !awaitFile(path) {
		t.Fatalf("timed out waiting for %s to appear", path)
	}
}

// Cancelling during a merge: the streams the merger was consuming are garbage
// and go, but the path the run would have handed back stays. A file already
// sitting at the merged path is a previous complete download — yt-dlp merges
// into "<name>.temp.<ext>" and renames — so deleting it would be data loss.
func TestDownloadCancellationSweepsTheConsumedIntermediates(t *testing.T) {
	outDir := t.TempDir()
	stream1 := filepath.Join(outDir, "V.f137.mp4")
	stream2 := filepath.Join(outDir, "V.f251.webm")
	merged := filepath.Join(outDir, "V.mkv")
	marker := filepath.Join(outDir, "merging.marker")

	script := fmt.Sprintf(
		"echo '[download] Destination: %s'\n"+
			": > '%s'\n"+
			"echo '[download] Destination: %s'\n"+
			": > '%s'\n"+
			"echo '[Merger] Merging formats into \"%s\"'\n"+
			": > '%s'\n"+
			": > '%s'\n"+
			"sleep 30\n",
		stream1, stream1, stream2, stream2, merged, merged, marker)

	if err := runCancelledDownload(t, outDir, script, marker); !IsCancelled(err) {
		t.Fatalf("runDownload() error = %v, want a cancellation", err)
	}

	for _, gone := range []string{stream1, stream2} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived: a stream the merger consumed is garbage (stat err = %v)", gone, err)
		}
	}
	if _, err := os.Stat(merged); err != nil {
		t.Errorf("%s was removed: never delete the path the run would have handed back (stat err = %v)", merged, err)
	}
}

// The same sweep must not touch a row with no post-processing, where the one
// Destination is the deliverable itself. The .ytdl beside it still goes.
func TestDownloadCancellationKeepsTheDeliverableWhenNothingPostProcessed(t *testing.T) {
	outDir := t.TempDir()
	dest := filepath.Join(outDir, "V.mp4")
	marker := filepath.Join(outDir, "ready.marker")

	script := fmt.Sprintf(
		"echo '[download] Destination: %s'\n"+
			": > '%s'\n"+
			": > '%s.ytdl'\n"+
			": > '%s'\n"+
			"sleep 30\n",
		dest, dest, dest, marker)

	if err := runCancelledDownload(t, outDir, script, marker); !IsCancelled(err) {
		t.Fatalf("runDownload() error = %v, want a cancellation", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Errorf("%s was deleted: with no post-processor the Destination is the file the user asked for (stat err = %v)", dest, err)
	}
	if _, err := os.Stat(dest + ".ytdl"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s.ytdl survived; the partial-file sweep still runs (stat err = %v)", dest, err)
	}
}

// --- refusals ---------------------------------------------------------------

func TestDownloadRefusalIsAnExtractError(t *testing.T) {
	tests := []struct {
		name      string
		stderr    string
		wantMsg   string
		wantStale bool
	}{
		{
			// The reason the type matters: this is the one the UI answers by
			// probing again for a fresh info-json rather than showing a wall.
			name:      "expired media URLs",
			stderr:    "ERROR: unable to download video data: HTTP Error 403: Forbidden\n",
			wantMsg:   "unable to download video data: HTTP Error 403: Forbidden",
			wantStale: true,
		},
		{
			name:      "the video is simply gone",
			stderr:    "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable\n",
			wantMsg:   "Video unavailable",
			wantStale: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := fakeYTDLP(t, "[download] Destination: /out/Video.mp4\n", tc.stderr, 1)

			_, err := runDownload(t.Context(), testJob(t, path, nil))
			if err == nil {
				t.Fatal("runDownload() returned no error")
			}

			var ee *ExtractError
			if !errors.As(err, &ee) {
				t.Fatalf("error = %v (%T), want an *ExtractError: IsStaleInfo matches on nothing else", err, err)
			}
			if ee.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", ee.Message, tc.wantMsg)
			}
			if ee.ExitCode != 1 {
				t.Errorf("ExitCode = %d, want 1", ee.ExitCode)
			}
			if !strings.Contains(ee.Stderr, strings.TrimSpace(tc.stderr)) {
				t.Errorf("Stderr = %q, want it to hold yt-dlp's own words", ee.Stderr)
			}
			if got := IsStaleInfo(err); got != tc.wantStale {
				t.Errorf("IsStaleInfo(%v) = %v, want %v", err, got, tc.wantStale)
			}
			if IsCancelled(err) {
				t.Errorf("IsCancelled(%v) = true, want false: yt-dlp ran and refused", err)
			}
		})
	}
}

func TestClassifyDownloadRun(t *testing.T) {
	killed := runScript(t, "kill -TERM $$\n")
	refused := runScript(t, "exit 1\n")

	tests := []struct {
		name         string
		runErr       error
		callerCtxErr error
		stderr       string
		check        func(*testing.T, error)
	}{
		{
			name:  "a clean exit",
			check: func(t *testing.T, err error) { requireNil(t, err) },
		},
		{
			name: "wait delay with nobody cancelling means it exited fine",
			// os/exec only reports ErrWaitDelay when the process itself exited
			// successfully; something merely held the pipes open.
			runErr: exec.ErrWaitDelay,
			check:  func(t *testing.T, err error) { requireNil(t, err) },
		},
		{
			name:         "a cancellation",
			runErr:       killed,
			callerCtxErr: context.Canceled,
			check: func(t *testing.T, err error) {
				if !IsCancelled(err) {
					t.Errorf("IsCancelled(%v) = false, want true", err)
				}
				var ee *ExtractError
				if errors.As(err, &ee) {
					t.Errorf("error = %v, want a cancellation not to be an *ExtractError", err)
				}
			},
		},
		{
			// Finding 5's shape, carried over: os/exec can report ErrWaitDelay
			// on a cancelled run. Taking the shortcut above first would tell a
			// caller who pressed stop that the download succeeded.
			name:         "wait delay that arrived alongside a cancellation",
			runErr:       exec.ErrWaitDelay,
			callerCtxErr: context.Canceled,
			check: func(t *testing.T, err error) {
				if !IsCancelled(err) {
					t.Errorf("IsCancelled(%v) = false, want cancellation to outrank the ErrWaitDelay shortcut", err)
				}
			},
		},
		{
			name:   "killed by a signal we did not send",
			runErr: killed,
			stderr: "ERROR: HTTP Error 403: Forbidden\n",
			check: func(t *testing.T, err error) {
				var ee *ExtractError
				if errors.As(err, &ee) {
					t.Errorf("error = %v, want a signal kill not to be adjudicated as the URL's fault", err)
				}
				// And it must not be retried as a stale info-json either: the
				// stderr looks stale, but nothing here was decided.
				if IsStaleInfo(err) {
					t.Errorf("IsStaleInfo(%v) = true, want false", err)
				}
				if IsCancelled(err) {
					t.Errorf("IsCancelled(%v) = true, want false: nobody cancelled", err)
				}
			},
		},
		{
			name:   "it ran and refused",
			runErr: refused,
			stderr: "ERROR: [youtube] x: Video unavailable\n",
			check: func(t *testing.T, err error) {
				var ee *ExtractError
				if !errors.As(err, &ee) {
					t.Fatalf("error = %v (%T), want an *ExtractError", err, err)
				}
				if ee.Message != "Video unavailable" {
					t.Errorf("Message = %q, want %q", ee.Message, "Video unavailable")
				}
				if !errors.Is(err, refused) {
					t.Errorf("error = %v, want it to unwrap to the exec error", err)
				}
			},
		},
		{
			name:   "could not start it at all",
			runErr: exec.ErrNotFound,
			check: func(t *testing.T, err error) {
				var ee *ExtractError
				if errors.As(err, &ee) {
					t.Errorf("error = %v, want a start failure not to blame the URL", err)
				}
				if !errors.Is(err, exec.ErrNotFound) {
					t.Errorf("error = %v, want it to wrap the start failure", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, classifyDownloadRun(tc.runErr, tc.callerCtxErr, tc.stderr))
		})
	}
}

func requireNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("error = %v, want nil", err)
	}
}

// runScript runs a shell script to completion and returns the error Wait gave,
// so a table can hold a genuine *exec.ExitError rather than a hand-rolled one.
func runScript(t *testing.T, body string) error {
	t.Helper()
	err := exec.Command(writeScript(t, body)).Run()
	if err == nil {
		t.Fatalf("script %q exited 0, want a failure to classify", body)
	}
	return err
}

// --- the output directory ---------------------------------------------------

func TestDownloadsDirCreatesItWhenMissing(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads HOME on unix and USERPROFILE on Windows; set both so
	// the test does not write into the real profile directory.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	want := filepath.Join(home, "Downloads")
	if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s already exists; the test proves nothing", want)
	}

	dir, fellBack, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir() error = %v", err)
	}
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if fellBack {
		t.Error("fellBack = true, want false: the home directory resolved")
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("%s was not created: %v", want, err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", want)
	}
}

func TestDownloadsDirIsReusedWhenItAlreadyExists(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads HOME on unix and USERPROFILE on Windows; set both so
	// the test does not write into the real profile directory.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}

	dir, _, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir() error = %v", err)
	}
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
}

func TestDownloadsDirFallsBackToTheWorkingDirectory(t *testing.T) {
	// Both, or os.UserHomeDir succeeds from USERPROFILE on Windows and the
	// fallback branch is never reached on the one platform that can reach it.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	// Stated as a precondition rather than inferred from the assertions below.
	// If a platform still resolves a home from somewhere else, the fallback
	// branch is unreachable and this test proves nothing — and worse, the rest
	// of it would create and write into that real home.
	if home, err := os.UserHomeDir(); err == nil {
		t.Fatalf("os.UserHomeDir() = %q with the home environment cleared; the fallback branch is unreachable here", home)
	}

	dir, fellBack, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir() error = %v", err)
	}
	if !fellBack {
		t.Error("fellBack = false, want true so the UI can say where the file went")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if dir != wd {
		t.Errorf("dir = %q, want the working directory %q", dir, wd)
	}
}

// Download itself, not runDownload: this is what wires the output directory and
// the info-json together.
func TestDownloadWritesIntoTheHomeDownloadsDirectory(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads HOME on unix and USERPROFILE on Windows; set both so
	// the test does not write into the real profile directory.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	wantDir := filepath.Join(home, "Downloads")

	path, argsFile := fakeYTDLP(t, "[download] Destination: /out/Video.mp4\n", "", 0)
	infoJSON := filepath.Join(t.TempDir(), "info.json")
	probe := &ProbeResult{InfoJSONPath: infoJSON}
	row := Row{Args: []string{"-f", "137"}}

	updates := make(chan Progress)
	got := collectProgress(updates)

	res, err := Download(t.Context(), path, probe, row, updates)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	got() // Download closes updates; a UI ranging over it must terminate.

	if res.OutputDir != wantDir {
		t.Errorf("OutputDir = %q, want %q", res.OutputDir, wantDir)
	}
	if res.UsedWorkingDir {
		t.Error("UsedWorkingDir = true, want false")
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("%s was not created: %v", wantDir, err)
	}

	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	wantTemplate := filepath.Join(wantDir, "%(title)s.%(ext)s")
	if !strings.Contains(string(argv), wantTemplate) {
		t.Errorf("argv = %q, want -o %q", argv, wantTemplate)
	}
	if !strings.Contains(string(argv), infoJSON) {
		t.Errorf("argv = %q, want --load-info-json %q", argv, infoJSON)
	}
}

func TestDownloadRefusesAProbeWithNoInfoJSON(t *testing.T) {
	updates := make(chan Progress)
	got := collectProgress(updates)

	if _, err := Download(t.Context(), "/nonexistent", nil, Row{}, updates); err == nil {
		t.Fatal("Download() with a nil probe returned no error")
	}
	got() // The channel is closed even on the refusal paths.
}

// --- a UI that stops listening ----------------------------------------------

// The download must finish whether or not anyone is watching it. A blocked send
// would stall the goroutine draining yt-dlp's stdout, which stalls yt-dlp on a
// full pipe, which is a hang with no error to report.
func TestDownloadSurvivesAUIThatStoppedReading(t *testing.T) {
	var b strings.Builder
	b.WriteString("[download] Destination: /out/Video.mp4\n")
	for i := range 5000 {
		fmt.Fprintf(&b, "%d/5000/NA/1000.0\n", i)
	}
	b.WriteString(`[Merger] Merging formats into "/out/Video.mkv"` + "\n")
	path, _ := fakeYTDLP(t, b.String(), "", 0)

	// Unbuffered, and nobody will ever receive from it.
	updates := make(chan Progress)

	done := make(chan struct{})
	var res *DownloadResult
	var err error
	go func() {
		defer close(done)
		res, err = runDownload(t.Context(), testJob(t, path, updates))
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runDownload() never returned: a UI that stopped draining deadlocked the download")
	}

	if err != nil {
		t.Fatalf("runDownload() error = %v", err)
	}
	if res.Path != "/out/Video.mkv" {
		t.Errorf("Path = %q, want %q", res.Path, "/out/Video.mkv")
	}
}

// The last update of a download is very often the change into merging, and it
// arrives when the run is already ending. A UI that was busy for a moment when
// it landed must still be told, or it sits at 100% for as long as ffmpeg takes
// and never learns why.
func TestProgressPumpFlushesTheLastUpdateToAMomentarilyBusyReader(t *testing.T) {
	out := make(chan Progress)
	pump := newProgressPump(out)

	pump.send(Progress{Phase: PhaseDownloading, Downloaded: 1, DownloadedKnown: true})
	pump.send(Progress{Phase: PhaseMerging, Downloaded: 2, DownloadedKnown: true})

	// close starts while nobody is receiving yet: the reader shows up well
	// inside progressFlushGrace, the way a UI between two frames does.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		pump.close()
	}()
	time.Sleep(progressFlushGrace / 5)

	got := collectProgress(out)
	all := got()
	<-closed

	if len(all) == 0 {
		t.Fatal("no updates delivered: the pending update was dropped at close")
	}
	if last := all[len(all)-1]; last.Phase != PhaseMerging {
		t.Errorf("last phase = %q, want %q: the newest update must survive conflation", last.Phase, PhaseMerging)
	}
}

// A reader that never comes back must not hold the end of a download open for
// longer than the grace period.
func TestProgressPumpCloseGivesUpOnAReaderThatIsGone(t *testing.T) {
	pump := newProgressPump(make(chan Progress))
	pump.send(Progress{Phase: PhaseMerging})

	start := time.Now()
	pump.close()
	if elapsed := time.Since(start); elapsed > 5*progressFlushGrace {
		t.Errorf("close() took %s, want it to give up after about %s", elapsed, progressFlushGrace)
	}
}

func TestProgressPumpTolerAtesANilChannel(t *testing.T) {
	pump := newProgressPump(nil)
	pump.send(Progress{Phase: PhaseDownloading})
	pump.close()
}

// --- the stdout parser ------------------------------------------------------

func TestOutputScannerJoinsALineSplitAcrossWrites(t *testing.T) {
	s := &outputScanner{pump: newProgressPump(nil)}
	defer s.pump.close()

	for _, chunk := range []string{"[download] Des", "tination: /out/Vid", "eo.mp4\n50", "0/1000/NA/NA\n"} {
		if n, err := s.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v, want %d, nil", chunk, n, err, len(chunk))
		}
	}
	if got := s.destination(); got != "/out/Video.mp4" {
		t.Errorf("destination() = %q, want %q", got, "/out/Video.mp4")
	}
}

func TestOutputScannerFlushesALineWithNoTrailingNewline(t *testing.T) {
	s := &outputScanner{pump: newProgressPump(nil)}
	defer s.pump.close()

	if _, err := s.Write([]byte("[download] Destination: /out/Video.mp4")); err != nil {
		t.Fatal(err)
	}
	if got := s.destination(); got != "" {
		t.Fatalf("destination() = %q before flush, want it still buffered", got)
	}
	s.flush()
	if got := s.destination(); got != "/out/Video.mp4" {
		t.Errorf("destination() after flush = %q, want %q", got, "/out/Video.mp4")
	}
}

// A program printing without ever ending a line must not grow the buffer
// without bound, and must not stop the next real line being read.
func TestOutputScannerDropsAnUnendingLine(t *testing.T) {
	s := &outputScanner{pump: newProgressPump(nil)}
	defer s.pump.close()

	if _, err := s.Write([]byte(strings.Repeat("x", maxOutputLineBytes+1024))); err != nil {
		t.Fatal(err)
	}

	// The whole point is the bound, so the bound is what is asserted. Checking
	// only that the next line still parses would pass just as well with no
	// guard at all, which is a test of nothing.
	s.mu.Lock()
	held := len(s.partial)
	s.mu.Unlock()
	if held > maxOutputLineBytes {
		t.Errorf("buffered %d bytes of an unfinished line, want at most %d", held, maxOutputLineBytes)
	}

	if _, err := s.Write([]byte("\n[download] Destination: /out/Video.mp4\n")); err != nil {
		t.Fatal(err)
	}
	if got := s.destination(); got != "/out/Video.mp4" {
		t.Errorf("destination() = %q, want %q", got, "/out/Video.mp4")
	}
}

func TestOutputScannerLeftovers(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   []string
	}{
		{
			// The single Destination of a row with no post-processing IS the
			// deliverable: yt-dlp renames .part onto it as soon as the bytes
			// are in and only then finishes tearing down. Sweeping it would
			// turn a cancel landing in that gap into silent data loss.
			name:   "no post-processing: the deliverable is never swept",
			stdout: "[download] Destination: /out/V.mp4\n",
			want:   []string{"/out/V.mp4.part", "/out/V.mp4.ytdl"},
		},
		{
			name: "a merge consumed its streams, so the finished ones are garbage",
			stdout: "[download] Destination: /out/V.f137.mp4\n" +
				"[download] Destination: /out/V.f251.webm\n" +
				`[Merger] Merging formats into "/out/V.mkv"` + "\n",
			want: []string{
				"/out/V.f137.mp4.part", "/out/V.f137.mp4.ytdl", "/out/V.f137.mp4",
				"/out/V.f251.webm.part", "/out/V.f251.webm.ytdl", "/out/V.f251.webm",
			},
		},
		{
			// The first condition on its own. Two streams are down and one of
			// them finished, but no post-processor has started, so nothing has
			// declared a completed download garbage yet. This is the case the
			// deliverable check cannot cover, because neither path is the
			// deliverable.
			name: "two streams and no post-processor yet: nothing completed is swept",
			stdout: "[download] Destination: /out/V.f137.mp4\n" +
				"[download] Destination: /out/V.f251.webm\n",
			want: []string{
				"/out/V.f137.mp4.part", "/out/V.f137.mp4.ytdl",
				"/out/V.f251.webm.part", "/out/V.f251.webm.ytdl",
			},
		},
		{
			name: "an extraction consumed the stream it read",
			stdout: "[download] Destination: /out/Song.webm\n" +
				"[ExtractAudio] Destination: /out/Song.mp3\n",
			want: []string{"/out/Song.webm.part", "/out/Song.webm.ytdl", "/out/Song.webm"},
		},
		{
			// The second condition on its own: a post-processor did run, but it
			// named the file it was given, so that path is the deliverable and
			// must survive even though a post-processor is what produced it.
			name: "a post-processor that wrote back over its input leaves it alone",
			stdout: "[download] Destination: /out/Song.mp3\n" +
				"[ExtractAudio] Destination: /out/Song.mp3\n",
			want: []string{"/out/Song.mp3.part", "/out/Song.mp3.ytdl"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &outputScanner{pump: newProgressPump(nil)}
			defer s.pump.close()

			if _, err := s.Write([]byte(tc.stdout)); err != nil {
				t.Fatal(err)
			}

			got := s.leftovers()
			if len(got) != len(tc.want) {
				t.Fatalf("leftovers() = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("leftovers()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// phases renders the phases of a run for a failure message.
func phases(got []Progress) []Phase {
	out := make([]Phase, 0, len(got))
	for _, p := range got {
		out = append(out, p.Phase)
	}
	return out
}
