package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// infoTimeout bounds one whole `yt-dlp -J` extraction. Extraction can mean
	// several round trips to the site plus a signature solve, so it is well
	// above the time a healthy probe takes.
	infoTimeout = 2 * time.Minute

	// maxInfoJSONBytes is a post-hoc reject, not a cap on what gets written.
	// stdout is the temp file's own descriptor — see probeInfo for why — so
	// yt-dlp writes as much as it likes and the size is only inspected once it
	// has exited. A runaway document therefore lands on disk in full before it
	// is refused; the deferred remove keeps the damage transient, but on a
	// tmpfs TMPDIR the peak is real. Enforcing this at write time would mean
	// putting a pipe back in front of stdout, which costs the property that a
	// re-exec'd descendant cannot block Wait on the document.
	maxInfoJSONBytes = 64 << 20 // 64 MiB

	// maxStderrBytes caps what is kept of yt-dlp's stderr for the debug log.
	// This one is enforced as it is written: stderr really is a pipe.
	maxStderrBytes = 1 << 20 // 1 MiB

	// infoJSONPattern names the temp file holding the raw -J output.
	infoJSONPattern = "yank-info-*.json"
)

// Errors that a probe can end in for reasons that are not yt-dlp failing.
var (
	// ErrLiveStream reports that the URL is a live stream. Its formats have no
	// size and no end, so the picker and the progress bar have nothing to show;
	// yank refuses the URL instead of pretending otherwise.
	ErrLiveStream = errors.New("live streams are not supported")

	// ErrPlaylist reports that the URL names a playlist, a channel or another
	// collection rather than one video. --no-playlist asks yt-dlp not to expand
	// a video that merely sits in a playlist; it does not turn a playlist URL
	// into a video, so the document comes back with entries and no formats.
	// Kept distinct from ErrNoFormatsExtracted, which would tell the user their
	// playlist was empty — untrue, and nothing they could act on.
	ErrPlaylist = errors.New("playlist and channel URLs are not supported")

	// ErrNoFormatsExtracted reports that yt-dlp extracted a single video and
	// listed no formats for it. It is kept distinct from a successful probe so
	// the UI never renders an empty picker. It is about the document yt-dlp
	// produced, not about what survives ranking.
	ErrNoFormatsExtracted = errors.New("yt-dlp listed no downloadable formats")
)

// ProbeResult is one successful `yt-dlp -J` extraction.
//
// Ownership of InfoJSONPath: the file exists only when Probe returned a nil
// error, and from that moment the caller owns it and must call Cleanup exactly
// once — after the download finishes, after it fails, after the user cancels,
// and when the user goes back to type another URL. Every path inside Probe that
// returns an error removes the file before returning, so a failed attempt never
// leaves one behind and the caller must not try to clean up after an error.
type ProbeResult struct {
	// Info is the subset of the document that yank uses.
	Info VideoInfo

	// InfoJSONPath is the raw -J stdout, saved verbatim so the download step
	// can pass `--load-info-json <path>` rather than making yt-dlp extract
	// everything a second time. It is written byte for byte rather than
	// re-serialised from Info, because yt-dlp reads back fields that VideoInfo
	// does not model.
	InfoJSONPath string

	// Stderr is everything yt-dlp wrote to stderr, for the debug log. It is
	// usually empty: --no-warnings silences the common chatter.
	Stderr string

	once sync.Once
	err  error
}

// Cleanup removes the info-json. It is safe to call more than once and from
// more than one goroutine; only the first call does anything, and later calls
// repeat its result. The error is worth logging and not worth acting on: a
// temp file that could not be removed is the operating system's problem.
func (r *ProbeResult) Cleanup() error {
	r.once.Do(func() {
		if err := os.Remove(r.InfoJSONPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			r.err = err
		}
	})
	return r.err
}

// ExtractError reports a positive failure: yt-dlp started, ran, and refused the
// URL. It is never used for an inconclusive outcome — a cancellation, our own
// timeout, a signal we did not send, or pipes cut by WaitDelay — because a
// caller is entitled to read this type as "adjudicated, the URL is at fault"
// and stop retrying.
type ExtractError struct {
	// Message is one line fit to print in the UI: yt-dlp's own complaint with
	// the "ERROR: " marker and the "[extractor] <id>: " tag taken off. It is
	// never empty; when yt-dlp said nothing usable it names the exit status.
	Message string
	// Stderr is the untouched stderr, for the debug log. It holds only yt-dlp's
	// own words, which is what makes it safe to classify on.
	Stderr string
	// ExitCode is yt-dlp's exit status, and is always >= 0. A negative code
	// means a signal killed the process, which says nothing about the URL and
	// so never reaches this type.
	ExitCode int

	err error
}

func (e *ExtractError) Error() string { return e.Message }
func (e *ExtractError) Unwrap() error { return e.err }

// Probe runs `yt-dlp -J` against url and returns what it said.
//
// ytdlpPath is an executable that Resolve has already proven to run; Probe
// never resolves one itself. The context bounds the whole call: cancelling it
// kills yt-dlp, removes the temp file and returns an error wrapping
// context.Canceled.
func Probe(ctx context.Context, ytdlpPath, url string) (*ProbeResult, error) {
	return probeInfo(ctx, ytdlpPath, url, infoTimeout, waitDelay)
}

// infoArgs is the argv Probe runs, minus the executable. The trailing "--"
// keeps a URL that begins with a dash from being read as an option.
func infoArgs(url string) []string {
	return []string{"-J", "--no-playlist", "--no-warnings", "--", url}
}

// probeInfo is Probe with the two durations injected, so tests can exercise the
// real exec path without waiting on production timeouts. It mirrors the seam
// probeVersionWith established in binary.go.
func probeInfo(ctx context.Context, ytdlpPath, url string, timeout, delay time.Duration) (*ProbeResult, error) {
	f, err := os.CreateTemp("", infoJSONPattern)
	if err != nil {
		return nil, fmt.Errorf("could not create info-json temp file: %w", err)
	}
	path := f.Name()
	// The file belongs to probeInfo until it hands it over on the success path.
	// Until keep is set, every return below removes it, so no URL attempt can
	// leak one — success, failure, timeout and cancellation alike.
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(path)
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ytdlpPath, infoArgs(url)...)
	// stdout is the temp file itself, not a pipe: the child gets the descriptor
	// directly, so a descendant of the macOS re-exec cannot leave Wait blocked
	// on reading the document, and it never sits in memory.
	cmd.Stdout = f
	errBuf := &cappedBuffer{limit: maxStderrBytes}
	cmd.Stderr = errBuf
	// stderr is still a pipe, and a lingering descendant holds it open past the
	// parent's exit. Without a WaitDelay, Wait blocks on that pipe and the
	// context stops meaning anything. The price is that Wait can report
	// ErrWaitDelay for a run that actually succeeded; classifyInfoRun sorts
	// that out rather than blaming yt-dlp, exactly as classifyProbe does.
	cmd.WaitDelay = delay

	runErr := cmd.Run()
	stderr := strings.TrimRight(errBuf.String(), "\n")

	raw, decodeErr := decodeInfoJSON(f, maxInfoJSONBytes)

	// The order of the three checks below is the whole point.
	//
	// A cancellation outranks everything: the caller asked us to stop and must
	// never be told the URL was at fault. classifyInfoRun re-checks this for
	// its own callers, and the context can be cancelled between here and there,
	// so both checks earn their place.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("probing %s: %w", url, err)
	}
	// Then the document, when one decoded. "_type: playlist" is a positive
	// statement about the URL the user typed, and a better answer than
	// whichever single entry made yt-dlp exit non-zero — real playlists do
	// exit 1 with a complete document when one of their videos is unavailable.
	if decodeErr == nil {
		if entries, isCollection := raw.collection(); isCollection {
			return nil, fmt.Errorf("%w: %s (%d entries)", ErrPlaylist, describeVideo(raw.VideoInfo, url), entries)
		}
	}
	// Only then the run's own outcome.
	if err := classifyInfoRun(url, decodeErr == nil, runErr, ctx.Err(), runCtx.Err(), stderr, timeout); err != nil {
		return nil, err
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("could not read yt-dlp output for %s: %w", url, decodeErr)
	}

	if raw.IsLive {
		return nil, fmt.Errorf("%w: %s", ErrLiveStream, describeVideo(raw.VideoInfo, url))
	}
	if len(raw.Formats) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoFormatsExtracted, describeVideo(raw.VideoInfo, url))
	}

	keep = true
	return &ProbeResult{Info: raw.VideoInfo, InfoJSONPath: path, Stderr: stderr}, nil
}

// classifyInfoRun turns the outcome of a `yt-dlp -J` run into an error, or nil
// when the document that landed on disk can be trusted. haveJSON says whether
// that document decoded. It is pure so the classification can be tested without
// spawning anything, and it separates a positive failure — yt-dlp ran and
// refused — from an inconclusive one, the way classifyProbe does in binary.go.
func classifyInfoRun(url string, haveJSON bool, runErr, callerCtxErr, runCtxErr error, stderr string, timeout time.Duration) error {
	if runErr == nil {
		return nil
	}

	// Cancellation is checked before anything else. os/exec can report
	// ErrWaitDelay on a cancelled run: when the context fires and Cancel finds
	// the process already done, watchCtx keeps err nil, the WaitDelay timer
	// then fires on the still-held stderr pipe, and Wait returns ErrWaitDelay
	// with ctx.Err() == context.Canceled. Taking the shortcut below first would
	// hand a caller who was told "cancelled" a ProbeResult it is unlikely to
	// Cleanup, leaking the info-json.
	if callerCtxErr != nil {
		return fmt.Errorf("probing %s: %w", url, callerCtxErr)
	}

	// ErrWaitDelay with nobody cancelling means the process exited successfully
	// but something still held its stderr pipe. The document is complete, so it
	// is the real answer.
	if errors.Is(runErr, exec.ErrWaitDelay) && haveJSON {
		return nil
	}

	switch {
	case runCtxErr != nil:
		return fmt.Errorf("probing %s: yt-dlp timed out after %s", url, timeout)
	case errors.Is(runErr, exec.ErrWaitDelay):
		// Pipes were cut before yt-dlp had said anything usable. Inconclusive:
		// nothing here blames the URL, and nothing here is worth retrying.
		return fmt.Errorf("probing %s: yt-dlp produced no usable output: %w", url, runErr)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ExitCode() < 0 {
			// Killed by a signal we did not send: Gatekeeper, the OOM killer,
			// an operator. It says nothing about the URL, so it must not become
			// an ExtractError, which callers read as a settled verdict.
			return fmt.Errorf("probing %s: yt-dlp was killed: %w", url, runErr)
		}
		// It started and refused. That is a verdict on the URL.
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

// rawInfo is the -J document as probeInfo reads it: everything VideoInfo
// models, plus the envelope fields that decide whether the document describes a
// single video at all. The embedded VideoInfo is flattened by encoding/json, so
// both decode in one pass and types.go needs no new field — formats.go compiles
// against that file.
type rawInfo struct {
	VideoInfo
	// Type is yt-dlp's "_type". A single video leaves it out or sets it to
	// "video"; a collection sets "playlist" or "multi_video".
	Type string `json:"_type"`
	// Entries is decoded for its length alone. []struct{} parses the array and
	// keeps none of it, so counting a thousand-video playlist costs nothing.
	Entries []struct{} `json:"entries"`
}

// collection reports whether the document describes a collection rather than
// one video, and how many items it lists.
func (r rawInfo) collection() (entries int, ok bool) {
	switch r.Type {
	case "playlist", "multi_video":
		return len(r.Entries), true
	}
	return 0, false
}

// decodeInfoJSON rewinds f and decodes the document yt-dlp wrote into it,
// refusing one larger than limit. The limit is a parameter so a test can
// exercise the rejection without writing 64 MiB, and it is checked after the
// fact: see maxInfoJSONBytes for why nothing bounds the write itself.
func decodeInfoJSON(f *os.File, limit int64) (rawInfo, error) {
	st, err := f.Stat()
	if err != nil {
		return rawInfo{}, err
	}
	switch {
	case st.Size() == 0:
		return rawInfo{}, errors.New("yt-dlp printed nothing")
	case st.Size() > limit:
		return rawInfo{}, fmt.Errorf("info-json is %d bytes, over the %d byte limit", st.Size(), limit)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return rawInfo{}, err
	}
	var raw rawInfo
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return rawInfo{}, fmt.Errorf("malformed -J output: %w", err)
	}
	return raw, nil
}

// describeVideo names a document in an error message, falling back to the URL
// for the extractors that return an empty title.
func describeVideo(info VideoInfo, url string) string {
	if strings.TrimSpace(info.Title) != "" {
		return info.Title
	}
	return url
}

// extractorPrefix matches the "[youtube] dQw4w9WgXcQ: " tag yt-dlp puts in
// front of an extractor's complaint. The bracketed name covers every extractor,
// including the colon-bearing ones like "[youtube:tab]". The id after it is
// optional and must be a single token with no spaces, so that a message such as
// "[generic] Unable to download webpage: HTTP Error 404" keeps its text instead
// of losing everything before the first colon.
var extractorPrefix = regexp.MustCompile(`^\[[^\]]+\]\s*(?:[^\s:]+:\s*)?`)

// friendlyError reduces yt-dlp's stderr to the one line a caller can print.
// Nothing is thrown away: ExtractError.Stderr keeps the whole thing.
//
// It never returns "". yt-dlp formats unconditionally as "ERROR: {msg}" and can
// raise with an empty message, and the maxStderrBytes cap can land mid-line, so
// a line that survives primaryErrorLine can still be nothing at all once the
// prefixes come off. The fallback is therefore applied to the stripped result,
// not to the raw line: an error that stringifies to "" would reach the user as
// a blank failure, with the exit status that would have helped skipped over.
func friendlyError(stderr string, exitCode int) string {
	if msg := stripNoise(primaryErrorLine(stderr)); msg != "" {
		return msg
	}
	if exitCode >= 0 {
		return fmt.Sprintf("yt-dlp exited with status %d", exitCode)
	}
	return "yt-dlp was killed"
}

// primaryErrorLine picks the line worth showing: the first one yt-dlp marked
// ERROR, or failing that the last thing it said, which is where optparse usage
// errors end up.
func primaryErrorLine(stderr string) string {
	last := ""
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ERROR:") {
			return line
		}
		last = line
	}
	return last
}

// stripNoise removes the two prefixes yt-dlp adds to an extractor's own words:
// the "ERROR: " severity marker and the "[extractor] <id>: " tag. A line
// carrying neither comes back unchanged apart from surrounding space.
func stripNoise(line string) string {
	s := strings.TrimSpace(line)
	// Matched case-sensitively: yt-dlp always shouts it, and a message that
	// genuinely starts "error:" is the extractor's wording, not a prefix.
	if rest, ok := strings.CutPrefix(s, "ERROR:"); ok {
		s = strings.TrimSpace(rest)
	}
	if loc := extractorPrefix.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	}
	return strings.TrimSpace(s)
}

// staleSignatures are the lower-cased substrings that mark a saved info-json
// whose media URLs have expired, as opposed to a video that is genuinely gone.
//
// Signed CDN URLs — googlevideo's above all — carry an expiry a few minutes
// out, so an info-json can go stale between the picker appearing and the user
// pressing enter. When it does, yt-dlp refuses the fetch with a 403 (or a 410
// from CDNs that prefer it), wraps it as "unable to download video data", and
// for fragmented formats says the fragment is not found. Extractors that mint
// their own tokens say "expired" outright.
var staleSignatures = []string{
	"http error 403",
	"http error 410",
	"expired",
	"not found, unable to continue",
}

// IsStaleInfo reports whether err is yt-dlp refusing a saved info-json whose
// media URLs have expired, which a caller answers by probing again for a fresh
// one rather than by showing the user a failure.
//
// It matches only on *ExtractError, and inside one only on Stderr and Message —
// fields that hold yt-dlp's own words. It never looks at err.Error(). The
// wrapped chain carries stdlib text and any URL we interpolated, and both of
// those produce false positives on this signature list: exec.ErrWaitDelay
// stringifies to "exec: WaitDelay expired before I/O complete", and a user can
// paste a URL with "expired" or "403" in the path. Both would mark a permanent,
// perfectly reproducible failure as retryable and burn a retry budget on it.
// That an outcome is an *ExtractError at all is already the guarantee that
// yt-dlp ran and adjudicated; everything inconclusive is a different type.
//
// Within that, it is deliberately biased towards saying yes: a false positive
// costs one extra extraction, which is idempotent, while a false negative shows
// the user a 403 they cannot act on. "expired" will match any of yt-dlp's own
// messages containing the word. The retry loop itself belongs to the download
// step; this only classifies.
func IsStaleInfo(err error) bool {
	var ee *ExtractError
	if !errors.As(err, &ee) {
		return false
	}
	text := strings.ToLower(ee.Stderr + "\n" + ee.Message)
	for _, sig := range staleSignatures {
		if strings.Contains(text, sig) {
			return true
		}
	}
	return false
}

// cappedBuffer collects at most limit bytes and silently drops the rest, so a
// yt-dlp stuck printing the same complaint cannot exhaust memory. Writes always
// report success: dropping debug output must not fail the run.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		keep := p
		if len(keep) > room {
			keep = keep[:room]
		}
		b.buf.Write(keep)
	}
	// The whole slice is reported as accepted even when part of it was
	// dropped; a short write without an error is a contract violation that
	// io.Copy turns into io.ErrShortWrite.
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
