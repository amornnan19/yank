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
	"errors"
	"fmt"
	"io"
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
	// Path is an executable that answered --version with exit status 0.
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
	// bundle that unpacks itself on first run, so this is generous.
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

// Resolve returns a yt-dlp executable that has been proven to run, downloading
// and verifying one into the cache directory if neither PATH nor the cache
// already holds a working copy.
func Resolve(ctx context.Context) (Result, error) {
	res := Result{}
	res.FFmpegPath, res.HasFFmpeg = FindFFmpeg()

	if path, err := exec.LookPath("yt-dlp"); err == nil {
		if version, err := probeVersion(ctx, path); err == nil {
			res.Path, res.Version, res.Source = path, version, SourcePATH
			return res, nil
		}
	}

	dir, err := BinDir()
	if err != nil {
		return Result{}, err
	}
	cached := filepath.Join(dir, binaryName(runtime.GOOS))

	if version, err := probeVersion(ctx, cached); err == nil {
		res.Path, res.Version, res.Source = cached, version, SourceCache
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("could not fetch yt-dlp: %w", err)
	}

	if err := download(ctx, cached); err != nil {
		return Result{}, err
	}

	version, err := probeVersion(ctx, cached)
	if err != nil {
		if !isBadBinary(err) {
			// A cancellation, a probe timeout or an OS refusal says nothing
			// about the file, which was checksum-verified moments ago. Keep it
			// for the next run and report the real reason.
			return Result{}, fmt.Errorf("could not fetch yt-dlp: %w", err)
		}
		// The file itself is at fault. Remove it so the next run retries the
		// download instead of failing the same way forever.
		_ = os.Remove(cached)
		return Result{}, fmt.Errorf("downloaded yt-dlp does not run: %w", err)
	}

	res.Path, res.Version, res.Source = cached, version, SourceDownload
	return res, nil
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

	// ErrWaitDelay means the process exited successfully but something still
	// held its output pipes; Go only reports it when no cancellation happened.
	// Output already collected is therefore the real answer.
	if errors.Is(runErr, exec.ErrWaitDelay) && version != "" {
		return version, nil
	}

	switch {
	case callerCtxErr != nil:
		return "", &probeError{fmt.Errorf("%s --version: %w", path, callerCtxErr), false}
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

// download fetches the release asset for this platform, checks it against the
// release's SHA2-256SUMS, and atomically moves it to dest. The download lands
// in a unique temp file inside dest's directory first, so an interrupted run
// cannot leave a half-written file at dest and two yank processes racing on
// first run cannot write to the same path.
func download(ctx context.Context, dest string) error {
	client := &http.Client{Timeout: httpTimeout}
	asset := assetName(runtime.GOOS, runtime.GOARCH)

	sums, err := fetch(ctx, client, releaseBase+checksumsAsset, maxChecksumsBytes)
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

	got, err := fetchTo(ctx, client, releaseBase+asset, maxBinaryBytes, tmp)
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
