package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// progressTemplate is the one line yank asks yt-dlp to print per progress
	// tick. The point of a template is that the format is ours: the default
	// "[download]  10.0% of ~142.00MiB at 3.20MiB/s" is human-readable output
	// whose shape yt-dlp is free to change, and parsing it back out would make
	// every release a possible breakage. Four "/"-separated fields, each of
	// them either a number or the literal "NA", is a format nothing upstream
	// can move.
	progressTemplate = "download:%(progress.downloaded_bytes)s/%(progress.total_bytes)s/%(progress.total_bytes_estimate)s/%(progress.speed)s"

	// progressFields is how many "/"-separated fields progressTemplate emits.
	// A line with a different count is not one of ours — every path yt-dlp
	// prints has slashes in it too — so the count is the first thing checked.
	progressFields = 4

	// unknownField is what yt-dlp's output templates render a None as.
	unknownField = "NA"

	// outputTemplate is the -o value, joined onto the download directory.
	outputTemplate = "%(title)s.%(ext)s"

	// downloadsSubdir is the folder under the home directory that files land
	// in.
	downloadsSubdir = "Downloads"

	// maxOutputLineBytes bounds the partial line the stdout parser holds while
	// waiting for a newline. yt-dlp's longest line is a path; anything past
	// this is a program printing without ever ending a line, and buffering it
	// would grow without limit.
	maxOutputLineBytes = 1 << 16 // 64 KiB

	// progressFlushGrace bounds how long the end of a download waits for the UI
	// to take the last update. Long enough that a reader momentarily busy still
	// gets it, short enough that a reader which has gone away costs a blink.
	progressFlushGrace = 250 * time.Millisecond
)

// Phase is the coarse state of a download: which of yt-dlp's stages the bytes
// are currently in.
//
// It exists because the progress template covers the download and nothing
// else. Once the last byte is in, yt-dlp can spend a long minute in ffmpeg with
// nothing to report, and a bar frozen at 100% reads as a hang. The UI shows the
// phase instead.
type Phase string

const (
	// PhaseDownloading means bytes are arriving and the numbers on the Progress
	// mean something.
	PhaseDownloading Phase = "downloading"
	// PhaseMerging means the download is done and ffmpeg is muxing the video
	// and audio streams into one file.
	PhaseMerging Phase = "merging"
	// PhaseConverting means the download is done and ffmpeg is transcoding,
	// which is what the audio row asks for.
	PhaseConverting Phase = "converting"
)

// Progress is one update from a running download.
//
// The byte counts are paired with a "known" flag rather than using zero for
// "unknown": a download legitimately starts at zero bytes, and a site that
// states no size is a different thing from one that states a size of nothing.
type Progress struct {
	// Phase is what yt-dlp is doing. During PhaseMerging and PhaseConverting
	// the numbers below are whatever the download last reported, held so the
	// bar does not jump backwards.
	Phase Phase
	// Downloaded is the bytes fetched so far.
	Downloaded int64
	// DownloadedKnown is false when yt-dlp reported the count as NA.
	DownloadedKnown bool
	// Total is the size of what is being fetched, meaningful only when
	// TotalKnown is true.
	Total int64
	// TotalKnown is false when yt-dlp knows neither an exact size nor an
	// estimate.
	TotalKnown bool
	// TotalEstimated records that Total came from total_bytes_estimate rather
	// than total_bytes, so the UI can hedge the number it prints.
	TotalEstimated bool
	// Speed is bytes per second.
	Speed float64
	// SpeedKnown is false on the first tick and whenever yt-dlp reported NA.
	SpeedKnown bool
}

// Percent is how far along the download is, or false when either end of the
// fraction is unknown. It is clamped to 100: an estimated total can be smaller
// than what actually arrives.
func (p Progress) Percent() (float64, bool) {
	if !p.DownloadedKnown || !p.TotalKnown || p.Total <= 0 {
		return 0, false
	}
	return math.Min(100, float64(p.Downloaded)/float64(p.Total)*100), true
}

// DownloadResult describes a finished download.
type DownloadResult struct {
	// Path is the file yt-dlp saved, as yt-dlp itself named it. It is never
	// empty on a nil error.
	Path string
	// OutputDir is the directory the download was pointed at.
	OutputDir string
	// UsedWorkingDir records that OutputDir is the working directory because
	// the home directory could not be resolved, so the UI can say where the
	// file went instead of claiming it is in Downloads.
	UsedWorkingDir bool
	// AlreadyExisted records that yt-dlp wrote nothing because Path was already
	// there, so the UI can say so instead of claiming a fresh download. It is
	// yt-dlp's decision and keyed on the output filename, not on the format:
	// a file from an earlier pick of a different format with the same name
	// counts. It is true only when Path is the file yt-dlp reported as already
	// downloaded; a run that downloaded or merged anything into Path is fresh.
	AlreadyExisted bool
	// Stderr is everything yt-dlp wrote to stderr, for the debug log.
	Stderr string
}

// ErrNoDestination reports that yt-dlp exited successfully without ever naming
// the file it wrote. Returning a wrong path, or none, after a download the user
// watched finish is a visible failure, so it is an error rather than a
// DownloadResult with an empty Path.
var ErrNoDestination = errors.New("yt-dlp did not report where it saved the file")

// Download fetches the format row names, writing it into the user's downloads
// directory, and returns where it landed.
//
// ytdlpPath is an executable Resolve has vouched for — probed this run, or
// recorded from a prior probe of the same file with size, mtime and mode
// unchanged. probe is the
// extraction the row was chosen from; Download reads its InfoJSONPath and never
// takes ownership of it — the caller still owes it exactly one Cleanup.
//
// updates receives progress as it happens and may be nil. Download closes it
// before returning, so a UI can range over it, which means the channel must not
// be shared between calls. A UI that stops draining cannot stall the download:
// updates are conflated and the newest one wins, so a slow reader loses
// intermediate ticks rather than blocking yt-dlp.
//
// Cancelling ctx kills yt-dlp and every process it started, removes the partial
// files, and returns an error for which IsCancelled reports true. That is not
// the same outcome as a failure and must not be shown as one.
func Download(ctx context.Context, ytdlpPath string, probe *ProbeResult, row Row, updates chan<- Progress) (*DownloadResult, error) {
	if probe == nil || probe.InfoJSONPath == "" {
		if updates != nil {
			close(updates)
		}
		return nil, errors.New("download needs the info-json a successful probe saved")
	}
	dir, fellBack, err := DownloadsDir()
	if err != nil {
		if updates != nil {
			close(updates)
		}
		return nil, err
	}
	return runDownload(ctx, downloadJob{
		ytdlpPath: ytdlpPath,
		infoJSON:  probe.InfoJSONPath,
		row:       row,
		outDir:    dir,
		fellBack:  fellBack,
		updates:   updates,
		waitDelay: waitDelay,
	})
}

// DownloadsDir returns the directory downloads are written to: ~/Downloads,
// created with 0755 if it does not exist.
//
// When the home directory cannot be resolved at all — no HOME, no user record —
// it falls back to the working directory and says so, because refusing to
// download is a worse answer than saving somewhere the UI can name. The
// working directory is not created: it already exists by definition.
func DownloadsDir() (dir string, fellBack bool, err error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return "", false, fmt.Errorf("could not locate a download directory: %w", errors.Join(homeErr, wdErr))
		}
		return wd, true, nil
	}
	dir = filepath.Join(home, downloadsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("could not create %s: %w", dir, err)
	}
	return dir, false, nil
}

// IsCancelled reports whether err is a download that stopped because its
// context ended, rather than one that failed.
//
// It matches on the typed sentinels only, never on text: the wrapped chain
// carries stdlib wording — exec.ErrWaitDelay stringifies to "exec: WaitDelay
// expired before I/O complete" — and any path we interpolated. Cancelled and
// failed are different outcomes for the user and for the cached info-json, so
// this has to be exact.
func IsCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// downloadJob is everything one run needs. It exists so runDownload can take
// the output directory and the wait delay as inputs, which is the seam
// probeVersionWith and probeInfo established: tests drive the real exec path
// without waiting on production timeouts or writing to the user's home.
type downloadJob struct {
	ytdlpPath string
	infoJSON  string
	row       Row
	outDir    string
	fellBack  bool
	updates   chan<- Progress
	waitDelay time.Duration
}

// downloadArgs is the argv one download runs, minus the executable.
//
// The row's own Args go in verbatim. D1 says the row you see is the format you
// get, and the row already named a concrete format id; a second selector added
// here would be free to disagree with the label the user picked, which is the
// one guarantee this package exists to keep. Nothing here contains a quote
// character — every element is one argv slot (D4).
func downloadArgs(infoJSON string, row Row, outDir string) []string {
	args := make([]string, 0, 9+len(row.Args))
	args = append(args,
		"--load-info-json", infoJSON,
		"--no-playlist",
		"-o", filepath.Join(outDir, outputTemplate),
	)
	args = append(args, row.Args...)
	return append(args, "--newline", "--progress-template", progressTemplate)
}

// runDownload is Download with the directory and the wait delay injected.
func runDownload(ctx context.Context, job downloadJob) (*DownloadResult, error) {
	pump := newProgressPump(job.updates)
	defer pump.close()

	out := &outputScanner{pump: pump}
	errBuf := &cappedBuffer{limit: maxStderrBytes}

	cmd := exec.CommandContext(ctx, job.ytdlpPath, downloadArgs(job.infoJSON, job.row, job.outDir)...)
	cmd.Stdout = out
	cmd.Stderr = errBuf
	// yt-dlp hands its own stdout and stderr to ffmpeg, so a merge that is
	// still running holds both pipes open after yt-dlp itself is gone. Without
	// a WaitDelay, Wait blocks on that and the context stops meaning anything.
	// The price is that Wait can report ErrWaitDelay for a run that actually
	// succeeded; classifyDownloadRun sorts that out rather than blaming yt-dlp,
	// exactly as classifyProbe and classifyInfoRun do.
	cmd.WaitDelay = job.waitDelay
	// Cancel replaces the default, which is Process.Kill: killing yt-dlp alone
	// leaves the ffmpeg it spawned running, burning a core on a merge nobody
	// wants any more, long after the user believes they stopped.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killTree(cmd) }

	runErr := cmd.Run()
	out.flush()
	stderr := strings.TrimRight(errBuf.String(), "\n")

	if err := classifyDownloadRun(runErr, ctx.Err(), stderr); err != nil {
		if IsCancelled(err) {
			// Only after the process group is gone: a live yt-dlp would write
			// the .part file straight back.
			removeLeftovers(out.leftovers())
		}
		return nil, err
	}

	path, already := out.destination()
	if path == "" {
		return nil, ErrNoDestination
	}
	return &DownloadResult{
		Path:           path,
		OutputDir:      job.outDir,
		UsedWorkingDir: job.fellBack,
		AlreadyExisted: already,
		Stderr:         stderr,
	}, nil
}

// classifyDownloadRun turns the outcome of a download run into an error, or nil
// when the file landed. It is pure so the classification can be tested without
// spawning anything, and it separates a positive failure — yt-dlp ran and
// refused — from an inconclusive one, the way classifyProbe does in binary.go
// and classifyInfoRun does in probe.go.
func classifyDownloadRun(runErr, callerCtxErr error, stderr string) error {
	if runErr == nil {
		return nil
	}

	// Cancellation is checked before anything else, and it is not an
	// afterthought here: killing the process group makes Wait return an
	// ExitError whose ExitCode is -1, and os/exec prefers that over the
	// context error it was about to report. Read in the other order, a user who
	// pressed q would be told their download was killed by something.
	if callerCtxErr != nil {
		return fmt.Errorf("downloading: %w", callerCtxErr)
	}

	// ErrWaitDelay with nobody cancelling is only ever reported when the
	// process itself exited successfully — os/exec prefers a real exit error
	// over it — so the file is on disk and something merely held the pipes.
	// The tail of the output may have been cut, which is why a missing
	// destination is its own error rather than a silent empty path.
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ExitCode() < 0 {
			// Killed by a signal we did not send: the OOM killer, an operator.
			// It says nothing about the URL or the file, so it must not become
			// an ExtractError, which callers read as a settled verdict.
			return fmt.Errorf("downloading: yt-dlp was killed: %w", runErr)
		}
		// It started and refused. That is a verdict, and it is the only shape
		// IsStaleInfo can classify: the stale-info retry works by re-probing
		// when yt-dlp rejects an info-json whose media URLs have expired, and
		// a plain fmt.Errorf of the same stderr would never be recognised.
		return &ExtractError{
			Message:  friendlyError(stderr, exitErr.ExitCode()),
			Stderr:   stderr,
			ExitCode: exitErr.ExitCode(),
			err:      runErr,
		}
	}

	// Could not start it at all: missing, not permitted, busy.
	return fmt.Errorf("could not run yt-dlp: %w", runErr)
}

// removeLeftovers deletes the partial files a cancelled download left behind.
//
// Best effort on purpose: a file that will not go away is the operating
// system's problem, and turning it into an error would report a clean
// cancellation as a failure.
func removeLeftovers(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// killTree ends yt-dlp and everything it started.
//
// Process.Kill on its own is not enough: yt-dlp spawns ffmpeg to merge and to
// transcode, and an orphaned ffmpeg keeps a core busy on work the user has
// already abandoned. setProcessGroup and killProcessTree are the two halves of
// reaching that ffmpeg, and each platform spells them its own way.
//
// The liveness probe is not defensive padding, and it is the one thing the
// stock os/exec Cancel gave us that replacing it took away. Cmd.Wait reaps the
// child before it rendezvouses with the cancel watcher, and reaping marks the
// Process done while leaving Pid numeric — only Release zeroes that. A context
// that becomes done inside that gap, which every normal completion passes
// through, would otherwise send a group signal to a pid the kernel is free to
// have recycled as some unrelated group's leader. Signalling proc itself is
// safe where signalling -proc.Pid is not: os.Process checks its own done state,
// a freshly built one for the negated pid carries none. A TOCTOU sliver is left
// and cannot be closed while signalling by pgid, but it is now a few
// instructions rather than the whole reap-to-rendezvous window.
//
// It returns nil once something was killed, and os.ErrProcessDone when there
// was nothing left to kill — os/exec reads that as "it finished on its own"
// and reports the context error rather than a cancellation failure.
func killTree(cmd *exec.Cmd) error {
	proc := cmd.Process
	if proc == nil || proc.Pid <= 0 {
		return os.ErrProcessDone
	}
	// Signal 0 asks whether the process is still there without sending
	// anything.
	if processIsDone(proc.Signal(syscall.Signal(0))) {
		return os.ErrProcessDone
	}
	if err := killProcessTree(proc); err == nil {
		return nil
	}
	// Last resort: yt-dlp alone. Better than leaving it running.
	return proc.Kill()
}

// processIsDone reports whether a liveness probe means the process is gone.
//
// Only os.ErrProcessDone does, and the distinction is not pedantry. Unix
// answers signal 0 on a live process with nil, so "err != nil" would look
// right here — but Windows answers it with EWINDOWS, because os.Process.Signal
// there implements nothing except Kill, and an EPERM says only that this
// process may not signal that one. Reading any error as "gone" would turn
// killTree into a no-op on Windows for every download that is still running,
// which is the exact failure this probe was added to avoid on unix.
func processIsDone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}

// --- progress delivery ------------------------------------------------------

// progressPump carries updates to the UI without ever letting the UI slow the
// download down.
//
// A plain send on the caller's channel would block the goroutine copying
// yt-dlp's stdout the moment a UI stopped draining — a Bubble Tea program
// blocked in its own update, a picker the user walked away from — and a blocked
// copier eventually blocks yt-dlp on a full pipe. A plain non-blocking send
// would instead drop updates, and the one update that must not be dropped is
// the phase change into merging, which is the only thing that ever arrives
// while the bar sits at 100%.
//
// So updates are conflated: the newest one is held and delivered when the
// reader is ready, and an update that arrives while an older one is still in
// flight replaces it. Nothing blocks the download, and whatever the reader sees
// is the current state rather than a stale one.
type progressPump struct {
	out chan<- Progress

	mu      sync.Mutex
	pending Progress
	has     bool

	wake     chan struct{}
	draining chan struct{}
	done     chan struct{}
	stopped  chan struct{}
}

func newProgressPump(out chan<- Progress) *progressPump {
	p := &progressPump{
		out:      out,
		wake:     make(chan struct{}, 1),
		draining: make(chan struct{}),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if out == nil {
		// Nobody is listening. send still records the value and throws it away,
		// which is cheaper than a nil check at every call site.
		close(p.stopped)
		return p
	}
	go p.run()
	return p
}

// send hands an update to the pump. It never blocks, whatever the reader is
// doing.
func (p *progressPump) send(v Progress) {
	p.mu.Lock()
	p.pending, p.has = v, true
	p.mu.Unlock()
	// One token is enough: run re-reads the slot after every delivery, so a
	// second token would only make it spin.
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *progressPump) run() {
	defer close(p.stopped)
	for {
		p.mu.Lock()
		v, ok := p.pending, p.has
		p.has = false
		p.mu.Unlock()

		if !ok {
			select {
			case <-p.wake:
			case <-p.draining:
				// The queue is empty and no more updates are coming.
				return
			case <-p.done:
				return
			}
			continue
		}
		select {
		case p.out <- v:
		case <-p.done:
			return
		}
	}
}

// close stops the pump and closes the caller's channel, so a UI ranging over it
// terminates.
//
// It flushes first. The last update of a download is very often the phase
// change into merging, and dropping that is the one loss the conflation is
// there to prevent: the UI would sit at 100% for as long as ffmpeg takes and
// never be told why. So a reader that is still there gets what is pending. A
// reader that is gone gets progressFlushGrace and no longer — waiting on a UI
// that stopped listening is the deadlock this whole type exists to avoid.
//
// Only then is the pump stopped and waited for, so nothing can be blocked
// mid-send on a channel that is about to close.
func (p *progressPump) close() {
	close(p.draining)
	if p.out != nil {
		select {
		case <-p.stopped:
		case <-time.After(progressFlushGrace):
		}
	}
	close(p.done)
	<-p.stopped
	if p.out != nil {
		close(p.out)
	}
}

// --- reading yt-dlp's stdout ------------------------------------------------

// mergerDestination matches the line ffmpeg's muxing step prints. The path is
// the merged file — the one the user actually ends up with — so it outranks
// the per-stream destinations that came before it.
var mergerDestination = regexp.MustCompile(`^\[Merger\] Merging formats into "(.*)"$`)

// alreadyDownloaded matches what yt-dlp says instead of a Destination line when
// the file is already there. The run succeeds and the path is still the answer.
var alreadyDownloaded = regexp.MustCompile(`^\[download\] (.+) has already been downloaded$`)

const (
	downloadDestPrefix = "[download] Destination: "
	audioDestPrefix    = "[ExtractAudio] Destination: "
	mergerPrefix       = "[Merger]"
	extractAudioPrefix = "[ExtractAudio]"
)

// outputScanner is the io.Writer yt-dlp's stdout is pointed at. It splits the
// stream into lines, turns the ones it recognises into progress updates, and
// remembers the facts the caller needs once the run is over.
//
// Every field is guarded by mu, because Write runs on the goroutine os/exec
// copies stdout with while flush and the accessors run on the goroutine that
// called Run.
//
// Those two are in fact already ordered by os/exec, on every path including the
// WaitDelay one: awaitGoroutines closes the pipes when the delay fires and then
// still blocks on the copier's result before Wait returns. So the mutex is not
// what makes reading after Run safe — Wait is. It is here so that this type is
// safe on its own terms rather than by appeal to a caller's internals. Do not
// read it as evidence that a plain buffer would be a race: cappedBuffer is used
// unsynchronised for stderr here and in probe.go, and that is correct.
type outputScanner struct {
	pump *progressPump

	mu      sync.Mutex
	partial []byte
	last    Progress
	// dests are the "[download] Destination:" paths in the order they appeared.
	// A merged download has one per stream, and each is what the matching .part
	// file is named after.
	dests   []string
	merged  string
	audio   string
	already string
}

func (s *outputScanner) Write(p []byte) (int, error) {
	n := len(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		s.line(string(append(s.partial, p[:i]...)))
		s.partial = s.partial[:0]
		p = p[i+1:]
	}
	s.partial = append(s.partial, p...)
	if len(s.partial) > maxOutputLineBytes {
		// Something is printing without ever ending a line. Drop what is held
		// rather than grow without bound; the remainder will be read as if it
		// were a line of its own, which is no worse than the junk it is.
		s.partial = s.partial[:0]
	}
	// The whole slice is always reported as written: a short write without an
	// error is a contract violation, and failing here would abort a download
	// over unparseable chatter.
	return n, nil
}

// flush handles a last line that arrived without a trailing newline, which is
// what a run cut short by WaitDelay leaves behind. Call it after Wait.
func (s *outputScanner) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.line(string(s.partial))
		s.partial = s.partial[:0]
	}
}

// line interprets one line of yt-dlp's stdout. Called with mu held.
func (s *outputScanner) line(raw string) {
	line := strings.TrimRight(raw, "\r")
	if line == "" {
		return
	}

	// Progress first, and only on a line that is nothing but the template:
	// every path yt-dlp prints has slashes in it, and it is the field count
	// plus the shape of each field that tells the two apart.
	if prog, ok := parseProgressLine(line); ok {
		s.last = prog
		s.pump.send(prog)
		return
	}

	switch {
	case strings.HasPrefix(line, downloadDestPrefix):
		s.dests = append(s.dests, strings.TrimSpace(strings.TrimPrefix(line, downloadDestPrefix)))
	case strings.HasPrefix(line, audioDestPrefix):
		s.audio = strings.TrimSpace(strings.TrimPrefix(line, audioDestPrefix))
		s.setPhase(PhaseConverting)
	case strings.HasPrefix(line, extractAudioPrefix):
		s.setPhase(PhaseConverting)
	case strings.HasPrefix(line, mergerPrefix):
		if m := mergerDestination.FindStringSubmatch(line); m != nil {
			s.merged = strings.TrimSpace(m[1])
		}
		s.setPhase(PhaseMerging)
	default:
		if m := alreadyDownloaded.FindStringSubmatch(line); m != nil {
			s.already = strings.TrimSpace(m[1])
		}
	}
}

// setPhase records a stage change and tells the UI about it. The byte counts
// ride along unchanged so a bar that reached 100% stays there while ffmpeg
// works. Called with mu held.
func (s *outputScanner) setPhase(phase Phase) {
	if s.last.Phase == phase {
		return
	}
	s.last.Phase = phase
	s.pump.send(s.last)
}

// destination reports the file the user ended up with.
//
// The rule is that the last post-processor to run wins, because each one
// deletes what it read. ExtractAudio runs after Merger, so where both appear
// the merged file has been consumed and naming it would name a file that is no
// longer there; the same reasoning that puts either of them above the
// per-stream downloads puts the later of the two above the earlier. Failing
// both, the last Destination line is the download, and failing that the file
// yt-dlp found already there.
//
// alreadyExisted is true only on that last branch. It is decided here, from the
// same switch that picks the path, so the two cannot disagree: a run that
// printed "has already been downloaded" for one stream and then merged has a
// merged path, and that file is fresh.
//
// A run that both merges and extracts is not reachable from today's rows —
// audioRow passes no -f, and yt-dlp's default for --extract-audio is a single
// bestaudio stream that never merges — but it becomes reachable the moment a
// row pairs a merging selector with -x, and ordering it correctly costs
// nothing now.
func (s *outputScanner) destination() (path string, alreadyExisted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destinationLocked()
}

// destinationLocked is destination without taking mu, for callers that already
// hold it and must see the same answer this one gives.
func (s *outputScanner) destinationLocked() (path string, alreadyExisted bool) {
	switch {
	case s.audio != "":
		return s.audio, false
	case s.merged != "":
		return s.merged, false
	case len(s.dests) > 0:
		return s.dests[len(s.dests)-1], false
	}
	return s.already, s.already != ""
}

// leftovers lists what a cancelled run should remove.
//
// Always the partial files: yt-dlp writes the bytes to "<destination>.part"
// and, for a fragmented format, keeps its resume state in "<destination>.ytdl".
// Both are named after a Destination line, and a merged download has one of
// each per stream.
//
// A completed intermediate — a stream that finished, so yt-dlp renamed its
// .part onto the final name — is swept too, but only under both of these:
//
//   - a post-processor that consumes its inputs actually started. That is the
//     only state in which a completed download is garbage rather than the
//     thing the user asked for. For a row with no post-processing there is one
//     Destination and it *is* the deliverable: yt-dlp renames .part onto it as
//     soon as the bytes are in and then still runs its post-processing loop and
//     tears down, so a cancel landing in that gap would delete the finished
//     file. Sweeping on dests alone turns "a file was left behind" into silent
//     data loss, which is strictly worse.
//   - the path is not the one destination reports. Redundant with the above
//     wherever the rows can reach today, and kept because it is the condition
//     that actually states the invariant: never delete what we are about to
//     hand back.
//
// One case is knowingly accepted. A Destination line is printed for a file this
// run resumed as well as for one it created, so a swept intermediate can carry
// bytes from an earlier session. Statting each path as its line is read would
// narrow that, and was rejected: it is a TOCTOU of its own, and the case it
// would catch is nearly empty — a *complete* file from an earlier session is
// announced as "has already been downloaded" and never reaches dests at all, so
// what is left is a resumed partial that the post-processor which just ran was
// about to consume regardless.
func (s *outputScanner) leftovers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	postProcessed := s.merged != "" || s.audio != ""
	deliverable, _ := s.destinationLocked()

	paths := make([]string, 0, 3*len(s.dests))
	for _, d := range s.dests {
		paths = append(paths, d+".part", d+".ytdl")
		if postProcessed && d != deliverable {
			paths = append(paths, d)
		}
	}
	return paths
}

// parseProgressLine reads one rendering of progressTemplate. It reports false
// for anything that is not one, which is how yt-dlp's ordinary chatter is
// skipped: a malformed tick is a line to ignore, never a reason to fail a
// download that is otherwise going fine.
func parseProgressLine(line string) (Progress, bool) {
	fields := strings.Split(strings.TrimSpace(line), "/")
	if len(fields) != progressFields {
		return Progress{}, false
	}

	downloaded, downloadedKnown, ok := parseProgressField(fields[0])
	if !ok {
		return Progress{}, false
	}
	total, totalKnown, ok := parseProgressField(fields[1])
	if !ok {
		return Progress{}, false
	}
	estimate, estimateKnown, ok := parseProgressField(fields[2])
	if !ok {
		return Progress{}, false
	}
	speed, speedKnown, ok := parseProgressField(fields[3])
	if !ok {
		return Progress{}, false
	}

	p := Progress{Phase: PhaseDownloading, Speed: speed, SpeedKnown: speedKnown}
	p.Downloaded, p.DownloadedKnown = progressBytes(downloaded, downloadedKnown)
	// total_bytes is the exact size when the server stated one. Only when it is
	// NA does the estimate get a say, and then the UI is told it is an estimate.
	if p.Total, p.TotalKnown = progressBytes(total, totalKnown); !p.TotalKnown {
		p.Total, p.TotalKnown = progressBytes(estimate, estimateKnown)
		p.TotalEstimated = p.TotalKnown
	}
	return p, true
}

// parseProgressField reads one field of a progress line. Any of them can be the
// literal "NA" — a stream with no stated size, the very first tick before there
// is a speed to report — which is unknown, not a broken line. valid is false
// only for a field that is neither NA nor a number, which does mean the line is
// not a progress line at all.
func parseProgressField(s string) (v float64, known, valid bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == unknownField {
		return 0, false, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false, false
	}
	return v, true, true
}

// progressBytes narrows a parsed field to a byte count.
//
// This is not formats.go's safeBytes and cannot be: that one refuses zero,
// because a computed size of zero there means the estimate failed. Here zero is
// the honest answer for the first tick of every download, and refusing it would
// make a bar that has not started look like a bar with no size. Out of int64's
// range is still unknown — the conversion would be implementation-defined and
// could land negative.
func progressBytes(v float64, known bool) (int64, bool) {
	if !known || v >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(v), true
}
