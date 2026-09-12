// Package ytdlp resolves the yt-dlp executable that the rest of yank shells
// out to. It prefers a copy already on PATH, falls back to a copy cached under
// the user cache directory, and downloads one from the yt-dlp GitHub releases
// as a last resort.
package ytdlp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Source records where a resolved yt-dlp executable came from.
type Source string

const (
	// SourcePATH means yt-dlp was already installed on the user's PATH.
	SourcePATH Source = "path"
	// SourceCache means yt-dlp was found in yank's own cache directory.
	SourceCache Source = "cache"
	// SourceDownload means yt-dlp was downloaded during this call.
	SourceDownload Source = "download"
)

// Result describes a working yt-dlp executable plus what yank could find out
// about the optional ffmpeg dependency.
type Result struct {
	// Path is an executable that answered --version with exit status 0,
	// either during this call or during an earlier one that recorded the
	// answer next to the file; the record is trusted only while the file's
	// size, mtime and mode are unchanged.
	Path string
	// Version is the trimmed output of that --version run.
	Version string
	// Source records where Path came from.
	Source Source
	// FFmpegPath is ffmpeg's location on PATH, or "" when it was not found.
	FFmpegPath string
	// HasFFmpeg reports whether ffmpeg was found on PATH. Its absence is not
	// an error; callers are expected to degrade the feature set instead.
	HasFFmpeg bool
}

const (
	// releaseBase is the "latest release" download prefix on GitHub. GitHub
	// redirects it to the concrete tag, so the binary and the checksum file
	// are guaranteed to come from the same release only if fetched close
	// together; that is the best the "latest" endpoint offers.
	releaseBase = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"

	// checksumsAsset is the release asset listing SHA-256 sums of every other
	// asset in the release.
	checksumsAsset = "SHA2-256SUMS"

	// maxChecksumsBytes caps the checksum file; it is a few KiB in practice.
	maxChecksumsBytes = 1 << 20 // 1 MiB
	// maxBinaryBytes caps the executable; the largest asset is ~40 MiB.
	maxBinaryBytes = 256 << 20 // 256 MiB

	// httpTimeout bounds a whole download, including the body transfer.
	httpTimeout = 10 * time.Minute
	// versionTimeout bounds a --version probe. The macOS build is a PyInstaller
	// bundle that unpacks itself on every run (~10s on darwin, measured in
	// #16), so this is generous.
	versionTimeout = 2 * time.Minute
	// waitDelay bounds how long a cancelled probe waits for the process's
	// output pipes to close after it has been killed.
	waitDelay = 2 * time.Second

	// staleTempAge is how long a download temp file must have gone untouched
	// before it counts as abandoned rather than in flight.
	staleTempAge = httpTimeout
)

// errNoChecksum reports that the checksum file did not list the wanted asset.
var errNoChecksum = errors.New("asset not listed in " + checksumsAsset)

// ResolveEvent is a milestone Resolve reports while it is still working, so a
// UI can say what the wait is for instead of guessing from how long it has
// taken.
type ResolveEvent int

const (
	// ResolveDownloading reports that neither PATH nor the cache had a working
	// copy and a download of the release has started. It starts at 1 so the
	// zero value is never mistaken for an event.
	ResolveDownloading ResolveEvent = iota + 1
)

// Resolve returns a yt-dlp executable that answered --version, downloading and
// verifying one into the cache directory if neither PATH nor the cache already
// holds a working copy. A cached copy is probed once and the answer recorded
// beside it; later calls trust the record while the file is unchanged and do
// not execute anything.
func Resolve(ctx context.Context) (Result, error) {
	return ResolveWith(ctx, nil)
}

// ResolveWith is Resolve with a channel for milestones. events may be nil.
// A send never blocks Resolve — an event nobody is ready for is dropped, so
// give it a buffer of one — and the channel is closed before ResolveWith
// returns, which means it must not be shared between calls.
func ResolveWith(ctx context.Context, events chan<- ResolveEvent) (Result, error) {
	return resolveWith(ctx, events, versionTimeout, waitDelay, releaseBase)
}

// resolveWith is ResolveWith with the probe durations and the release URL
// injected, so tests can drive the whole cache-then-download path against a
// fake binary and a local server without production timeouts or the network.
func resolveWith(ctx context.Context, events chan<- ResolveEvent, timeout, delay time.Duration, base string) (Result, error) {
	if events != nil {
		defer close(events)
	}

	res := Result{}
	res.FFmpegPath, res.HasFFmpeg = FindFFmpeg()

	if path, err := exec.LookPath("yt-dlp"); err == nil {
		if version, err := probeVersionWith(ctx, path, timeout, delay); err == nil {
			res.Path, res.Version, res.Source = path, version, SourcePATH
			return res, nil
		}
	}

	dir, err := BinDir()
	if err != nil {
		return Result{}, err
	}
	cached := filepath.Join(dir, binaryName(runtime.GOOS))

	// The cached copy was checksum-verified when it was installed and answered
	// --version then. Running it again costs a full unpack of the PyInstaller
	// bundle (~10s on darwin, measured in #16), so while the file is the same
	// size with the same mtime, and still executable, the recorded answer
	// stands. The stat is taken before the probe so the record cannot describe
	// a binary another process renamed into place while ours was running.
	info, statErr := os.Stat(cached)
	if statErr == nil {
		if version, ok := recordedVersion(cached, info); ok {
			res.Path, res.Version, res.Source = cached, version, SourceCache
			return res, nil
		}
	}
	version, probeErr := probeVersionWith(ctx, cached, timeout, delay)
	if probeErr == nil {
		if statErr == nil {
			recordVersion(cached, info, version)
		}
		res.Path, res.Version, res.Source = cached, version, SourceCache
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	// A failed probe of the cached copy is classified before anything is
	// fetched (#18). Only two outcomes justify a download: the stat taken
	// before the probe found nothing, or the file is positively not a working
	// yt-dlp. A timeout, a signal we did not send, pipes cut before any
	// output, or an OS refusal to start it say nothing about a file that was
	// checksum-verified when it was installed, and downloading over it would
	// be the branch the cached artifacts rule forbids.
	switch {
	case isMissing(statErr):
		// Nothing is cached, so the download is a first run, not a
		// replacement.
	case isBadBinary(probeErr):
		// The file itself is at fault. The download's rename would replace it
		// anyway; removing it now, with its sidecar, means a download that
		// fails cannot leave a record describing a file it no longer matches.
		discardBinary(cached)
	default:
		// The probe error already names the path.
		return Result{}, fmt.Errorf("could not use the cached yt-dlp (kept; retry, or delete it to force a fresh download): %w", probeErr)
	}

	if events != nil {
		select {
		case events <- ResolveDownloading:
		default:
		}
	}
	if err := downloadFrom(ctx, base, cached); err != nil {
		return Result{}, err
	}

	info, statErr = os.Stat(cached)
	version, err = probeVersionWith(ctx, cached, timeout, delay)
	if err != nil {
		if !isBadBinary(err) {
			// A cancellation, a probe timeout or an OS refusal says nothing
			// about the file, which was checksum-verified moments ago. Keep it
			// for the next run and report the real reason.
			return Result{}, fmt.Errorf("could not fetch yt-dlp: %w", err)
		}
		// The file itself is at fault. Remove it so the next run retries the
		// download instead of failing the same way forever.
		discardBinary(cached)
		return Result{}, fmt.Errorf("downloaded yt-dlp does not run: %w", err)
	}
	if statErr == nil {
		recordVersion(cached, info, version)
	}

	res.Path, res.Version, res.Source = cached, version, SourceDownload
	return res, nil
}

// versionRecord is the sidecar kept next to a cached binary: the version it
// answered --version with, and the size and mtime it had when it did.
type versionRecord struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
}

// sidecarPath is where the versionRecord for binary lives.
func sidecarPath(binary string) string {
	return binary + ".version"
}

// recordedVersion returns the version the sidecar next to binary recorded,
// provided the record describes the file exactly as info reports it now. A
// missing, unparsable or mismatched sidecar is a reason to probe, never a
// verdict on the binary.
//
// The file must also still be executable. A chmod -x, or a restore that keeps
// timestamps but drops modes, leaves size and mtime matching, and trusting the
// record then would hand Probe a path that fails with permission denied on
// every launch with nothing to repair it; falling through to the probe is what
// lets the download and rename put a runnable file back. Windows keeps no
// execute bit in the mode, so the check is skipped there.
func recordedVersion(binary string, info os.FileInfo) (string, bool) {
	if !info.Mode().IsRegular() {
		return "", false
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return "", false
	}
	body, err := os.ReadFile(sidecarPath(binary))
	if err != nil {
		return "", false
	}
	var rec versionRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return "", false
	}
	if rec.Version == "" || rec.Size != info.Size() || rec.MTimeNS != info.ModTime().UnixNano() {
		return "", false
	}
	return rec.Version, true
}

// recordVersion writes the sidecar for binary, describing it as info saw it
// before the probe that produced version. It is best effort: a sidecar that
// could not be written costs the next run a probe, nothing more. The write
// goes through a temp file in the same directory and a rename, so a reader
// never sees a half-written record.
func recordVersion(binary string, info os.FileInfo, version string) {
	body, err := json.Marshal(versionRecord{
		Version: version,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
	})
	if err != nil {
		return
	}
	dest := sidecarPath(binary)
	// The prefix matches sweepStaleTemps's glob, so a temp file abandoned here
	// is cleaned up by the same sweep as an abandoned download.
	tmp, err := os.CreateTemp(filepath.Dir(dest), "yt-dlp-version-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
	}
}

// discardBinary removes a cached binary that a probe has positively classified
// as unusable, and the sidecar that would otherwise describe a file that is no
// longer there. It is the only place the cached binary is removed.
func discardBinary(binary string) {
	_ = os.Remove(binary)
	_ = os.Remove(sidecarPath(binary))
}

// FindFFmpeg looks ffmpeg up on PATH. yank neither bundles nor downloads it, so
// a false result is a reduced feature set rather than a failure.
func FindFFmpeg() (path string, found bool) {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", false
	}
	return p, true
}

// CacheRoot returns yank's cache directory: $XDG_CACHE_HOME/yank when
// XDG_CACHE_HOME is set and non-empty, otherwise ~/.cache/yank. It does not
// create the directory.
func CacheRoot() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not locate home directory: %w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "yank"), nil
}

// BinDir returns <cache>/yank/bin, creating it (0755) if it does not exist.
func BinDir() (string, error) {
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create %s: %w", dir, err)
	}
	return dir, nil
}

// binaryName is the file name yt-dlp is stored under locally.
func binaryName(goos string) string {
	if goos == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// assetName maps a platform onto the matching yt-dlp release asset.
func assetName(goos, goarch string) string {
	switch {
	case goos == "darwin":
		return "yt-dlp_macos"
	case goos == "windows":
		return "yt-dlp.exe"
	case goos == "linux" && goarch == "arm64":
		return "yt-dlp_linux_aarch64"
	default:
		return "yt-dlp_linux"
	}
}

// probeError reports why a --version probe produced no version.
type probeError struct {
	err error
	// badBinary is set only when the failure is positive evidence that the file
	// is not a working yt-dlp: it started and exited non-zero, it exited zero
	// with nothing to say, or the kernel rejected the image. A cancellation, a
	// timeout or a signal kill proves nothing about the file, so it stays
	// false and the caller must not delete anything.
	badBinary bool
}

func (e *probeError) Error() string { return e.err.Error() }
func (e *probeError) Unwrap() error { return e.err }

// isBadBinary reports whether err positively classifies the probed file as
// unusable. A cached artifact may only be removed when this is true.
func isBadBinary(err error) bool {
	var pe *probeError
	return errors.As(err, &pe) && pe.badBinary
}

// isMissing reports whether the stat taken before the probe found no file at
// all, the one outcome that makes a download a first run rather than a
// replacement. It reads the stat error and not the exec error on purpose:
// execve answers ENOENT for a present file whose #! interpreter or ELF loader
// is absent (a glibc build on a musl host, say), and errors.Is(fs.ErrNotExist)
// is true for both. Deciding from the exec error would download over a
// present, verified file and then report it as not there. Not a verdict on
// the file either way, so deliberately not part of isBadBinary.
func isMissing(statErr error) bool {
	return errors.Is(statErr, fs.ErrNotExist)
}

// probeVersion runs path with --version and returns what it printed.
func probeVersion(ctx context.Context, path string) (string, error) {
	return probeVersionWith(ctx, path, versionTimeout, waitDelay)
}

// probeVersionWith is probeVersion with the two durations injected, so tests
// can exercise the real exec path without waiting on production timeouts.
func probeVersionWith(ctx context.Context, path string, timeout, delay time.Duration) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, path, "--version")
	// The macOS build re-execs itself, and the child inherits the stdout pipe.
	// Without a WaitDelay, killing the parent on cancellation still leaves Wait
	// blocked on that pipe until the orphan finishes unpacking, which defeats
	// the point of taking a context. The price is that Wait can report
	// ErrWaitDelay for a run that actually succeeded; classifyProbe sorts that
	// out rather than blaming the file.
	cmd.WaitDelay = delay

	out, err := cmd.Output()
	return classifyProbe(path, out, err, ctx.Err(), probeCtx.Err(), timeout)
}

// classifyProbe turns the outcome of a --version run into either a version or
// a probeError that says whether the file itself is to blame. It is pure so
// that the classification can be tested without spawning anything.
func classifyProbe(path string, out []byte, runErr, callerCtxErr, probeCtxErr error, timeout time.Duration) (string, error) {
	version := strings.TrimSpace(string(out))

	if runErr == nil {
		if version == "" {
			// It ran, it exited 0, and it is not yt-dlp.
			return "", &probeError{fmt.Errorf("%s --version printed nothing", path), true}
		}
		return version, nil
	}

	// Cancellation is checked before anything else. os/exec can report
	// ErrWaitDelay on a cancelled run: when the context fires and Cancel finds
	// the process already done, watchCtx keeps err nil, the WaitDelay timer
	// then fires on the still-held stdout pipe, and Wait returns ErrWaitDelay
	// with ctx.Err() == context.Canceled. Taking the shortcut below first would
	// answer a caller who asked us to stop with a version, as though nothing
	// had been cancelled.
	if callerCtxErr != nil {
		return "", &probeError{fmt.Errorf("%s --version: %w", path, callerCtxErr), false}
	}

	// ErrWaitDelay with nobody cancelling means the process exited successfully
	// but something still held its output pipes. Output already collected is
	// therefore the real answer.
	if errors.Is(runErr, exec.ErrWaitDelay) && version != "" {
		return version, nil
	}

	switch {
	case probeCtxErr != nil:
		return "", &probeError{fmt.Errorf("%s --version timed out after %s", path, timeout), false}
	case errors.Is(runErr, exec.ErrWaitDelay):
		// Pipes were cut before anything was read. Inconclusive.
		return "", &probeError{fmt.Errorf("%s --version produced no output: %w", path, runErr), false}
	case errors.Is(runErr, syscall.ENOEXEC):
		// The kernel refused the image: truncated, or built for another
		// platform. That is the file's fault.
		return "", &probeError{fmt.Errorf("%s is not executable on this platform: %w", path, runErr), true}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ExitCode() >= 0 {
			// It started and refused.
			return "", &probeError{fmt.Errorf("%s --version failed: %w", path, runErr), true}
		}
		// Killed by a signal with no cancellation of ours behind it: Gatekeeper,
		// the OOM killer, an operator. Says nothing about the file.
		return "", &probeError{fmt.Errorf("%s --version was killed: %w", path, runErr), false}
	}

	// Could not start it at all: missing, not permitted, busy. Not corruption.
	return "", &probeError{fmt.Errorf("%s --version failed: %w", path, runErr), false}
}

// downloadFrom fetches the release asset for this platform from base, checks
// it against the release's SHA2-256SUMS, and atomically moves it to dest. The
// download lands in a unique temp file inside dest's directory first, so an
// interrupted run cannot leave a half-written file at dest and two yank
// processes racing on first run cannot write to the same path. base is
// releaseBase in production; tests point it at a local server.
func downloadFrom(ctx context.Context, base, dest string) error {
	client := &http.Client{Timeout: httpTimeout}
	asset := assetName(runtime.GOOS, runtime.GOARCH)

	sums, err := fetch(ctx, client, base+checksumsAsset, maxChecksumsBytes)
	if err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	want, err := parseChecksums(sums, asset)
	if err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}

	dir := filepath.Dir(dest)
	sweepStaleTemps(dir, staleTempAge)

	tmp, err := os.CreateTemp(dir, "yt-dlp-*")
	if err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	tmpName := tmp.Name()
	// Closed and removed unless the rename below succeeds first.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	got, err := fetchTo(ctx, client, base+asset, maxBinaryBytes, tmp)
	if err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	if got != want {
		return fmt.Errorf("could not fetch yt-dlp: checksum mismatch for %s: got %s, want %s", asset, got, want)
	}

	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	if err := installVerified(ctx, tmpName, dest); err != nil {
		return fmt.Errorf("could not fetch yt-dlp: %w", err)
	}
	return nil
}

// installVerified moves the verified temp file onto dest. It tolerates the one
// way that legitimately fails: another yank process finished its own download
// first and is executing dest, which Windows refuses to replace while the image
// is mapped. A working binary at dest is the outcome we wanted either way.
func installVerified(ctx context.Context, tmpName, dest string) error {
	err := os.Rename(tmpName, dest)
	if err == nil {
		return nil
	}
	if _, probeErr := probeVersion(ctx, dest); probeErr == nil {
		return nil
	}
	return err
}

// sweepStaleTemps removes abandoned download temp files from dir. Only entries
// untouched for longer than maxAge are removed: the HTTP client timeout bounds
// how long a live download can go without writing, so anything older than that
// belongs to a run that was interrupted and will never come back. It is best
// effort; a failure here must not stop a download.
func sweepStaleTemps(dir string, maxAge time.Duration) {
	matches, err := filepath.Glob(filepath.Join(dir, "yt-dlp-*"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, name := range matches {
		info, err := os.Stat(name)
		if err != nil || info.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(name)
	}
}

// fetch reads at most limit bytes of a GET response body into memory.
func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := fetchTo(ctx, client, url, limit, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetchTo streams a GET response body into w, refusing to read more than limit
// bytes, and returns the hex-encoded SHA-256 of what it wrote.
func fetchTo(ctx context.Context, client *http.Client, url string, limit int64, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}

	sum := sha256.New()
	// limit+1 so a body exactly at the cap is distinguishable from one over it.
	n, err := io.Copy(io.MultiWriter(w, sum), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	if n > limit {
		return "", fmt.Errorf("GET %s: body exceeds %d bytes", url, limit)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// parseChecksums pulls the SHA-256 of asset out of a SHA2-256SUMS body, whose
// lines are "<64 hex digits>  <asset name>". A line that does not have that
// shape makes the whole body untrustworthy, so it is rejected rather than
// skipped.
func parseChecksums(body []byte, asset string) (string, error) {
	found := ""
	for i, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed %s line %d: %q", checksumsAsset, i+1, line)
		}
		digest := fields[0]
		if len(digest) != sha256.Size*2 {
			return "", fmt.Errorf("malformed %s line %d: %q is not a sha-256 digest", checksumsAsset, i+1, digest)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return "", fmt.Errorf("malformed %s line %d: %q is not a sha-256 digest", checksumsAsset, i+1, digest)
		}
		// GNU sha256sum marks binary mode with a leading "*" on the name.
		if strings.TrimPrefix(fields[1], "*") == asset && found == "" {
			// Lower-cased so it can be compared against hex.EncodeToString,
			// which always emits lower case; upper-case hex is valid here.
			found = strings.ToLower(digest)
		}
	}
	if found == "" {
		return "", fmt.Errorf("%w: %s", errNoChecksum, asset)
	}
	return found, nil
}
