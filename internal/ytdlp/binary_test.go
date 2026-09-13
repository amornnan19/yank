package ytdlp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		goarch string
		want   string
	}{
		{"darwin arm64", "darwin", "arm64", "yt-dlp_macos"},
		{"darwin amd64", "darwin", "amd64", "yt-dlp_macos"},
		{"windows amd64", "windows", "amd64", "yt-dlp.exe"},
		{"windows arm64", "windows", "arm64", "yt-dlp.exe"},
		{"linux arm64", "linux", "arm64", "yt-dlp_linux_aarch64"},
		{"linux amd64", "linux", "amd64", "yt-dlp_linux"},
		{"freebsd amd64", "freebsd", "amd64", "yt-dlp_linux"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assetName(tt.goos, tt.goarch); got != tt.want {
				t.Errorf("assetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestBinaryName(t *testing.T) {
	if got, want := binaryName("windows"), "yt-dlp.exe"; got != want {
		t.Errorf("binaryName(\"windows\") = %q, want %q", got, want)
	}
	for _, goos := range []string{"darwin", "linux", "freebsd"} {
		if got, want := binaryName(goos), "yt-dlp"; got != want {
			t.Errorf("binaryName(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestCacheRootUsesXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join("/tmp", "xdg-cache"))

	got, err := CacheRoot()
	if err != nil {
		t.Fatalf("CacheRoot() error = %v", err)
	}
	if want := filepath.Join("/tmp", "xdg-cache", "yank"); got != want {
		t.Errorf("CacheRoot() = %q, want %q", got, want)
	}
}

func TestCacheRootFallsBackToHomeWhenXDGEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	// os.UserHomeDir reads HOME on unix and USERPROFILE on Windows; set both so
	// the test does not silently fall through to the real profile directory.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := CacheRoot()
	if err != nil {
		t.Fatalf("CacheRoot() error = %v", err)
	}
	if want := filepath.Join(home, ".cache", "yank"); got != want {
		t.Errorf("CacheRoot() = %q, want %q", got, want)
	}
}

func TestBinDirCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)

	dir, err := BinDir()
	if err != nil {
		t.Fatalf("BinDir() error = %v", err)
	}
	if want := filepath.Join(root, "yank", "bin"); dir != want {
		t.Fatalf("BinDir() = %q, want %q", dir, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("BinDir() did not create %q: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", dir)
	}
	// MkdirAll's 0755 is masked by the process umask, so only assert the bits
	// yank actually depends on: the owner can enter and write the directory.
	if perm := info.Mode().Perm(); perm&0o700 != 0o700 {
		t.Errorf("%q mode = %04o, want at least owner rwx", dir, perm)
	}
}

// A trimmed but otherwise verbatim excerpt of a real SHA2-256SUMS body.
const sumsFixture = `1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6  yt-dlp
66674953fe251b89f4d08c5f0e35e0728679bd67ab3d7d05c0562af101dd3e7a  yt-dlp.exe
58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a  yt-dlp_linux
b16e4dab368a816cd05d477d698a605a6ae87ccee1c8ffd38fa21d7254141fcc  yt-dlp_linux_aarch64
0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202  yt-dlp_macos
`

func TestParseChecksumsPicksTheAssetLine(t *testing.T) {
	tests := []struct {
		asset string
		want  string
	}{
		{"yt-dlp_macos", "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202"},
		{"yt-dlp.exe", "66674953fe251b89f4d08c5f0e35e0728679bd67ab3d7d05c0562af101dd3e7a"},
		{"yt-dlp_linux", "58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a"},
		{"yt-dlp_linux_aarch64", "b16e4dab368a816cd05d477d698a605a6ae87ccee1c8ffd38fa21d7254141fcc"},
	}

	for _, tt := range tests {
		t.Run(tt.asset, func(t *testing.T) {
			got, err := parseChecksums([]byte(sumsFixture), tt.asset)
			if err != nil {
				t.Fatalf("parseChecksums(_, %q) error = %v", tt.asset, err)
			}
			if got != tt.want {
				t.Errorf("parseChecksums(_, %q) = %q, want %q", tt.asset, got, tt.want)
			}
		})
	}
}

func TestParseChecksumsRejectsMissingAsset(t *testing.T) {
	// yt-dlp_musllinux is a real asset name, just not one in the fixture.
	_, err := parseChecksums([]byte(sumsFixture), "yt-dlp_musllinux")
	if err == nil {
		t.Fatal("parseChecksums() accepted an asset that is not in the body")
	}
	if !errors.Is(err, errNoChecksum) {
		t.Errorf("error = %v, want it to wrap errNoChecksum", err)
	}
	if !strings.Contains(err.Error(), "yt-dlp_musllinux") {
		t.Errorf("error = %v, want it to name the missing asset", err)
	}
}

func TestParseChecksumsRejectsMalformedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "digest without a file name",
			body: "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202\n",
		},
		{
			name: "unquoted file name with a space",
			body: "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202  yt dlp macos\n",
		},
		{
			name: "digest too short",
			body: "0f192b7ec147ab  yt-dlp_macos\n",
		},
		{
			name: "digest is not hex",
			body: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz  yt-dlp_macos\n",
		},
		{
			name: "html error page instead of a sums file",
			body: "<!DOCTYPE html>\n<html><body>Not Found</body></html>\n",
		},
		{
			name: "good line followed by a bad one",
			body: sumsFixture + "not-a-digest yt-dlp_musllinux\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChecksums([]byte(tt.body), "yt-dlp_macos")
			if err == nil {
				t.Fatalf("parseChecksums() accepted a malformed body, returned %q", got)
			}
			if errors.Is(err, errNoChecksum) {
				t.Errorf("error = %v, want a malformed-body error, not a missing-asset one", err)
			}
		})
	}
}

func TestParseChecksumsIgnoresBlankLines(t *testing.T) {
	body := "\n" + sumsFixture + "\n\n"
	got, err := parseChecksums([]byte(body), "yt-dlp_macos")
	if err != nil {
		t.Fatalf("parseChecksums() error = %v", err)
	}
	if want := "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202"; got != want {
		t.Errorf("parseChecksums() = %q, want %q", got, want)
	}
}

func TestParseChecksumsHandlesCRLF(t *testing.T) {
	body := strings.ReplaceAll(sumsFixture, "\n", "\r\n")
	got, err := parseChecksums([]byte(body), "yt-dlp_linux")
	if err != nil {
		t.Fatalf("parseChecksums() error = %v", err)
	}
	if want := "58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a"; got != want {
		t.Errorf("parseChecksums() = %q, want %q", got, want)
	}
}

func TestParseChecksumsNormalisesUppercaseDigest(t *testing.T) {
	// hex.DecodeString accepts upper case, so a release pipeline that emitted
	// it would otherwise never match hex.EncodeToString's lower-case output.
	upper := strings.ToUpper(sumsFixture[:64]) + sumsFixture[64:]
	got, err := parseChecksums([]byte(upper), "yt-dlp")
	if err != nil {
		t.Fatalf("parseChecksums() error = %v", err)
	}
	want := "1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6"
	if got != want {
		t.Errorf("parseChecksums() = %q, want the lower-cased digest %q", got, want)
	}
}

// --- probe classification (finding 1) ---------------------------------------

// exitErrorWithCode produces a real *exec.ExitError for the given exit status.
func exitErrorWithCode(t *testing.T, code int) error {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("expected a non-zero exit from /bin/sh -c 'exit %d'", code)
	}
	return err
}

// signalKillError produces a real *exec.ExitError whose ExitCode is -1, the way
// a Gatekeeper or OOM kill arrives.
func signalKillError(t *testing.T) error {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs POSIX signals")
	}
	err := exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != -1 {
		t.Fatalf("expected a signal-killed ExitError, got %v", err)
	}
	return err
}

func TestClassifyProbeSucceedsOnCleanRun(t *testing.T) {
	version, err := classifyProbe("/bin/yt-dlp", []byte("2026.08.19\n"), nil, nil, nil, time.Minute)
	if err != nil {
		t.Fatalf("classifyProbe() error = %v", err)
	}
	if version != "2026.08.19" {
		t.Errorf("version = %q, want %q", version, "2026.08.19")
	}
}

func TestClassifyProbeTreatsWaitDelayWithOutputAsSuccess(t *testing.T) {
	// The exact case cmd.WaitDelay creates: the process exited 0, a child still
	// held the pipe, Wait reported ErrWaitDelay, and the output is right there.
	version, err := classifyProbe("/bin/yt-dlp", []byte("2026.08.19\n"), exec.ErrWaitDelay, nil, nil, time.Minute)
	if err != nil {
		t.Fatalf("classifyProbe() error = %v, want the run treated as a success", err)
	}
	if version != "2026.08.19" {
		t.Errorf("version = %q, want %q", version, "2026.08.19")
	}
}

func TestClassifyProbeCancellationOutranksWaitDelayWithOutput(t *testing.T) {
	// Issue #11, the same shape classifyInfoRun was fixed for. os/exec can
	// report ErrWaitDelay on a cancelled run: the context fires, Cancel finds
	// the process already done and leaves err nil, and the WaitDelay timer then
	// expires on the pipe a child still holds. The version really was printed,
	// but the caller asked us to stop, and answering with a version says the
	// cancellation never happened.
	version, err := classifyProbe("/bin/yt-dlp", []byte("2026.08.19\n"), exec.ErrWaitDelay, context.Canceled, context.Canceled, time.Minute)
	if err == nil {
		t.Fatalf("classifyProbe() = %q, <nil>; want cancellation to outrank the ErrWaitDelay shortcut", version)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true, want false: a cancelled probe says nothing about the file", err)
	}
	if version != "" {
		t.Errorf("version = %q, want no version alongside the error", version)
	}
}

func TestClassifyProbeBadBinaryCases(t *testing.T) {
	tests := []struct {
		name   string
		out    []byte
		runErr error
	}{
		{"exit zero with no output", nil, nil},
		{"non-zero exit status", nil, exitErrorWithCode(t, 1)},
		{"kernel refused the image", nil, syscall.ENOEXEC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := classifyProbe("/bin/yt-dlp", tt.out, tt.runErr, nil, nil, time.Minute)
			if err == nil {
				t.Fatal("classifyProbe() returned no error")
			}
			if !isBadBinary(err) {
				t.Errorf("isBadBinary(%v) = false, want true: this file may be deleted", err)
			}
		})
	}
}

func TestClassifyProbeInconclusiveCasesMustNotDelete(t *testing.T) {
	tests := []struct {
		name         string
		out          []byte
		runErr       error
		callerCtxErr error
		probeCtxErr  error
	}{
		{"caller cancelled", nil, context.Canceled, context.Canceled, context.Canceled},
		{"probe timed out", nil, signalKillError(t), nil, context.DeadlineExceeded},
		{"killed by a signal", nil, signalKillError(t), nil, nil},
		{"wait delay with no output", nil, exec.ErrWaitDelay, nil, nil},
		{"could not start", nil, os.ErrNotExist, nil, nil},
		{"permission denied", nil, os.ErrPermission, nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := classifyProbe("/bin/yt-dlp", tt.out, tt.runErr, tt.callerCtxErr, tt.probeCtxErr, time.Minute)
			if err == nil {
				t.Fatal("classifyProbe() returned no error")
			}
			if isBadBinary(err) {
				t.Errorf("isBadBinary(%v) = true, want false: a verified file would be deleted", err)
			}
		})
	}
}

func TestClassifyProbeReportsTheRealReason(t *testing.T) {
	_, err := classifyProbe("/bin/yt-dlp", nil, signalKillError(t), nil, context.DeadlineExceeded, 90*time.Second)
	if err == nil {
		t.Fatal("classifyProbe() returned no error")
	}
	if !strings.Contains(err.Error(), "timed out after 1m30s") {
		t.Errorf("error = %q, want it to name the probe timeout", err)
	}

	_, err = classifyProbe("/bin/yt-dlp", nil, context.Canceled, context.Canceled, context.Canceled, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

// --- probeVersion over a real process ---------------------------------------

// writeScript drops an executable /bin/sh script in a temp dir.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-yt-dlp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeVersionSurvivesALingeringChild(t *testing.T) {
	// The macOS re-exec shape: print the version, leave a child holding stdout,
	// exit 0. Wait reports ErrWaitDelay; the run still succeeded.
	path := writeScript(t, "echo 2026.08.19\nsleep 3 &\nexit 0\n")

	start := time.Now()
	version, err := probeVersionWith(t.Context(), path, time.Minute, 300*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("probeVersionWith() error = %v, want the lingering child ignored", err)
	}
	if version != "2026.08.19" {
		t.Errorf("version = %q, want %q", version, "2026.08.19")
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("elapsed = %s, want at least the wait delay: the pipe was not actually held open", elapsed)
	}
}

func TestProbeVersionNonZeroExitIsBadBinary(t *testing.T) {
	path := writeScript(t, "echo boom >&2\nexit 2\n")

	_, err := probeVersionWith(t.Context(), path, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeVersionWith() returned no error")
	}
	if !isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = false, want true", err)
	}
}

func TestProbeVersionTimeoutIsNotBadBinary(t *testing.T) {
	path := writeScript(t, "sleep 30\n")

	_, err := probeVersionWith(t.Context(), path, 200*time.Millisecond, 200*time.Millisecond)
	if err == nil {
		t.Fatal("probeVersionWith() returned no error")
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true, want false: a timeout must not delete the file", err)
	}
}

func TestProbeVersionCallerCancellationIsNotBadBinary(t *testing.T) {
	path := writeScript(t, "sleep 30\n")

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := probeVersionWith(ctx, path, time.Minute, 200*time.Millisecond)
	if err == nil {
		t.Fatal("probeVersionWith() returned no error")
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true, want false", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

func TestProbeVersionUnreadableFormatIsBadBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a bad image differently")
	}
	// A truncated download looks like this: executable bit set, contents the
	// kernel cannot load.
	path := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(path, []byte("half a download"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := probeVersionWith(t.Context(), path, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeVersionWith() returned no error")
	}
	if !errors.Is(err, syscall.ENOEXEC) {
		t.Fatalf("error = %v, want it to wrap syscall.ENOEXEC", err)
	}
	if !isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = false, want true", err)
	}
}

func TestProbeVersionMissingFileIsNotBadBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-there")

	_, err := probeVersionWith(t.Context(), path, time.Minute, 300*time.Millisecond)
	if err == nil {
		t.Fatal("probeVersionWith() returned no error")
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true, want false", err)
	}
}

// --- the version sidecar (#16) ----------------------------------------------

// cachedFake installs a fake yt-dlp in a fresh cache directory that Resolve
// looks in, with PATH emptied so nothing else is found first. The script logs
// every run to a file before printing version, which is how a test proves that
// Resolve did not execute it.
func cachedFake(t *testing.T, version string) (binary, runLog string) {
	t.Helper()
	return cachedFakeScript(t, "echo "+version+"\n")
}

// cachedFakeScript is cachedFake with the script's body supplied, for fakes
// that hang or refuse instead of answering. The run log line comes first, so
// the count is right however the body ends.
func cachedFakeScript(t *testing.T, body string) (binary, runLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("PATH", t.TempDir())

	dir, err := BinDir()
	if err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(dir, binaryName(runtime.GOOS))
	runLog = filepath.Join(root, "runs.log")
	script := fmt.Sprintf("#!/bin/sh\necho run >> '%s'\n%s", runLog, body)
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, runLog
}

// runsLogged counts how many times the fake installed by cachedFake ran.
func runsLogged(t *testing.T, runLog string) int {
	t.Helper()
	body, err := os.ReadFile(runLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(body), "run\n")
}

// currentRecord is the sidecar that would describe binary as it is right now.
func currentRecord(t *testing.T, binary, version string) versionRecord {
	t.Helper()
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	return versionRecord{Version: version, Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}
}

func writeSidecar(t *testing.T, binary string, rec versionRecord) {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath(binary), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSidecar(t *testing.T, binary string) versionRecord {
	t.Helper()
	body, err := os.ReadFile(sidecarPath(binary))
	if err != nil {
		t.Fatalf("reading the sidecar: %v", err)
	}
	var rec versionRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("sidecar %q does not parse: %v", body, err)
	}
	return rec
}

// drained asserts that Resolve closed events without sending anything, which is
// what every path that does not download must do.
func drained(t *testing.T, events <-chan ResolveEvent) {
	t.Helper()
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("Resolve sent %v, want no event on a path that does not download", ev)
		}
	default:
		t.Fatal("Resolve returned without closing the event channel")
	}
}

func TestResolveTrustsAMatchingSidecarWithoutRunningTheBinary(t *testing.T) {
	// The script answers with one version and the sidecar records another, so
	// the version in the result says which of the two Resolve consulted, and
	// the run log says whether the binary was executed at all.
	binary, runLog := cachedFake(t, "2026.09.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.08.19"))

	events := make(chan ResolveEvent, 1)
	res, err := ResolveWith(t.Context(), events)
	if err != nil {
		t.Fatalf("ResolveWith() error = %v", err)
	}
	if res.Path != binary || res.Source != SourceCache {
		t.Fatalf("result = %+v, want the cached binary from SourceCache", res)
	}
	if res.Version != "2026.08.19" {
		t.Errorf("Version = %q, want the recorded %q, not the script's answer", res.Version, "2026.08.19")
	}
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("the cached binary ran %d times, want 0: the sidecar matched", n)
	}
	drained(t, events)
}

func TestRecordedVersionRequiresAnExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no execute bit in the file mode")
	}
	binary, _ := cachedFake(t, "2026.09.01")
	rec := currentRecord(t, binary, "2026.08.19")
	writeSidecar(t, binary, rec)

	// chmod -x keeps size and mtime; the record still matches on both.
	if err := os.Chmod(binary, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentRecord(t, binary, "2026.08.19"); got != rec {
		t.Fatalf("chmod changed the record from %+v to %+v; the test no longer isolates the mode", rec, got)
	}

	if version, ok := recordedVersion(readRecord(binary), info); ok {
		t.Fatalf("recordedVersion() = %q, true; want the record distrusted once the file is not executable", version)
	}
}

func TestResolveDoesNotTrustTheSidecarOfANonExecutableFile(t *testing.T) {
	// The same shape through Resolve. Once the sidecar is distrusted the path
	// is the pre-existing one — the probe runs and fails with permission
	// denied, which is not the file's fault. The context is cancelled up
	// front so the probe reports the cancellation and Resolve stops at the
	// ctx.Err() check; what an uncancelled run does with the refusal is
	// TestResolveKeepsTheCachedBinaryWhenTheOSRefusesToStartIt. A Resolve
	// that trusted the sidecar would never look at the context and would
	// answer from the record.
	binary, _ := cachedFake(t, "2026.09.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.08.19"))
	if err := os.Chmod(binary, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := Resolve(ctx)
	if err == nil {
		t.Fatalf("Resolve() = %+v, nil; want the sidecar distrusted and the cancelled fall-through reported", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	// Distrusting the record is not a verdict on the file: nothing is removed.
	for _, path := range []string{binary, sidecarPath(binary)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(path), err)
		}
	}
}

func TestResolveReprobesWhenTheSidecarDoesNotMatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(rec versionRecord) versionRecord
	}{
		{"size differs", func(rec versionRecord) versionRecord { rec.Size++; return rec }},
		{"mtime differs", func(rec versionRecord) versionRecord { rec.MTimeNS++; return rec }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, runLog := cachedFake(t, "2026.09.01")
			writeSidecar(t, binary, tt.mutate(currentRecord(t, binary, "2026.08.19")))

			res, err := Resolve(t.Context())
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if res.Version != "2026.09.01" || res.Source != SourceCache {
				t.Fatalf("result = %+v, want the script's own answer from SourceCache", res)
			}
			if n := runsLogged(t, runLog); n != 1 {
				t.Errorf("the cached binary ran %d times, want 1: the sidecar did not match", n)
			}
			if got, want := readSidecar(t, binary), currentRecord(t, binary, "2026.09.01"); got != want {
				t.Errorf("sidecar = %+v, want it rewritten to %+v", got, want)
			}
		})
	}
}

func TestResolveWritesTheSidecarWhenItIsMissing(t *testing.T) {
	binary, runLog := cachedFake(t, "2026.09.01")

	res, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.Version != "2026.09.01" || res.Source != SourceCache {
		t.Fatalf("result = %+v, want the script's answer from SourceCache", res)
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Fatalf("the cached binary ran %d times, want 1: there was no sidecar", n)
	}
	if got, want := readSidecar(t, binary), currentRecord(t, binary, "2026.09.01"); got != want {
		t.Fatalf("sidecar = %+v, want %+v", got, want)
	}

	// The record the probe produced is what the next run answers from.
	res, err = Resolve(t.Context())
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if res.Version != "2026.09.01" {
		t.Errorf("second Version = %q, want %q", res.Version, "2026.09.01")
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("the cached binary ran %d times across two resolves, want 1", n)
	}
}

func TestResolveReprobesOnAGarbageSidecar(t *testing.T) {
	binary, runLog := cachedFake(t, "2026.09.01")
	if err := os.WriteFile(sidecarPath(binary), []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v: a bad sidecar is not a bad binary", err)
	}
	if res.Version != "2026.09.01" {
		t.Errorf("Version = %q, want the script's answer %q", res.Version, "2026.09.01")
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("the cached binary ran %d times, want 1", n)
	}
	if got, want := readSidecar(t, binary), currentRecord(t, binary, "2026.09.01"); got != want {
		t.Errorf("sidecar = %+v, want the garbage replaced with %+v", got, want)
	}
}

func TestDiscardBinaryRemovesTheSidecarToo(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "yt-dlp")
	for _, path := range []string{binary, sidecarPath(binary)} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	discardBinary(binary)

	for _, path := range []string{binary, sidecarPath(binary)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still present after discardBinary: %v", filepath.Base(path), err)
		}
	}
}

// --- classifying a failed cache probe (#18) ---------------------------------

// noFetch is a release base that fails the test the moment anything is fetched
// from it. It is the proof that Resolve did not start a download.
func noFetch(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Resolve fetched %s: a download was started", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// refusingRelease is a release base that answers every request with 404 and
// counts them, so a test can prove a download was attempted without serving
// a binary.
func refusingRelease(t *testing.T) (base string, hits *int) {
	t.Helper()
	hits = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/", hits
}

// fileState is the size and mtime of a file, the two things the sidecar
// records and the two things a re-download would change.
type fileState struct {
	size    int64
	mtimeNS int64
}

func stateOf(t *testing.T, path string) fileState {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	return fileState{info.Size(), info.ModTime().UnixNano()}
}

// assertKept checks that an inconclusive probe left the cached binary exactly
// as it was and told the user where the file is and what they can do.
func assertKept(t *testing.T, err error, binary string, before fileState) {
	t.Helper()
	if err == nil {
		t.Fatal("Resolve returned no error")
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true, want false: this outcome is inconclusive", err)
	}
	if !strings.Contains(err.Error(), "(kept; retry, or delete it to force a fresh download)") {
		t.Errorf("error = %q, want it to say the file was kept and how to force a download", err)
	}
	if !strings.Contains(err.Error(), binary) {
		t.Errorf("error = %q, want it to name %s", err, binary)
	}
	if after := stateOf(t, binary); after != before {
		t.Errorf("cached binary changed from %+v to %+v; it must be untouched", before, after)
	}
}

// assertSidecarKept checks that the pre-existing sidecar is still in place.
func assertSidecarKept(t *testing.T, binary string, sidecar versionRecord) {
	t.Helper()
	if got := readSidecar(t, binary); got != sidecar {
		t.Errorf("sidecar = %+v, want the pre-existing %+v left in place", got, sidecar)
	}
}

func TestResolveKeepsTheCachedBinaryWhenTheProbeTimesOut(t *testing.T) {
	// The slow-machine case from #18: the PyInstaller unpack outlives
	// versionTimeout. The sidecar's size is off by one so the record is
	// distrusted and the probe actually runs. sleep is spelled with its path
	// because cachedFake empties PATH, and a "sleep: not found" exit 127 would
	// be a positive failure, not a timeout. The timeout leaves room for
	// macOS's first exec of a freshly written file, which alone takes a few
	// hundred milliseconds before the shell reaches its first line.
	binary, runLog := cachedFakeScript(t, "/bin/sleep 30\n")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Size++
	writeSidecar(t, binary, sidecar)
	before := stateOf(t, binary)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, 2*time.Second, 200*time.Millisecond, noFetch(t), noPython)

	assertKept(t, err, binary, before)
	assertSidecarKept(t, binary, sidecar)
	if !strings.Contains(err.Error(), "timed out after 2s") {
		t.Errorf("error = %q, want the probe timeout as the reason", err)
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("the cached binary ran %d times, want 1", n)
	}
	drained(t, events)
}

func TestResolveKeepsTheCachedBinaryWhenTheOSRefusesToStartIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no execute bit in the file mode")
	}
	// chmod -x with a sidecar that still matches: #16 distrusts the record
	// because the file is not executable, the probe runs, and execve answers
	// EACCES. That is a refusal to start, not a verdict on the file.
	binary, runLog := cachedFake(t, "2026.09.01")
	sidecar := currentRecord(t, binary, "2026.08.19")
	writeSidecar(t, binary, sidecar)
	if err := os.Chmod(binary, 0o644); err != nil {
		t.Fatal(err)
	}
	before := stateOf(t, binary)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), noPython)

	assertKept(t, err, binary, before)
	assertSidecarKept(t, binary, sidecar)
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("error = %v, want it to wrap the permission error", err)
	}
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("the cached binary ran %d times, want 0: it is not executable", n)
	}
	drained(t, events)
}

func TestResolveKeepsACachedFileWhoseInterpreterIsMissing(t *testing.T) {
	// execve answers ENOENT for a present file whose #! interpreter (or ELF
	// loader: a glibc build on a musl host) is absent, and the exec error then
	// satisfies errors.Is(fs.ErrNotExist) exactly as a missing file would.
	// "Nothing is cached" has to come from the stat, or this verified file is
	// downloaded over on every launch and reported as not there. The zipapp
	// is the one exception (#19), and only when the sidecar says so; a file
	// with no sidecar, with one from before the asset field existed, or with
	// one naming the bundle is kept. The sidecars are stale on size so the
	// record is distrusted and the probe actually runs.
	tests := []struct {
		name        string
		withSidecar bool
		asset       string
	}{
		{"no sidecar", false, ""},
		{"sidecar names the bundle", true, assetName(runtime.GOOS, runtime.GOARCH)},
		{"sidecar predates the asset field", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, _ := cachedFake(t, "2026.09.01")
			if err := os.WriteFile(binary, []byte("#!/nonexistent/sh\necho 2026.09.01\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			var sidecar versionRecord
			if tt.withSidecar {
				sidecar = currentRecord(t, binary, "2026.08.19")
				sidecar.Size++
				sidecar.Asset = tt.asset
				writeSidecar(t, binary, sidecar)
			}
			before := stateOf(t, binary)

			events := make(chan ResolveEvent, 1)
			_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), noPython)

			assertKept(t, err, binary, before)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("error = %v, want the ENOENT from execve in the chain: the test no longer exercises the trap", err)
			}
			if tt.withSidecar {
				assertSidecarKept(t, binary, sidecar)
			} else if _, serr := os.Stat(sidecarPath(binary)); !errors.Is(serr, fs.ErrNotExist) {
				t.Errorf("sidecar: %v, want none written", serr)
			}
			drained(t, events)
		})
	}
}

func TestResolveReplacesACachedBinaryThatRefusesToRun(t *testing.T) {
	// A non-zero exit is positive evidence. Resolve may download over it, and
	// the mismatched sidecar goes first so a failed download cannot leave a
	// record for a file it no longer describes.
	binary, runLog := cachedFakeScript(t, "echo boom >&2\nexit 2\n")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Size++
	writeSidecar(t, binary, sidecar)
	base, hits := refusingRelease(t)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, noPython)
	if err == nil {
		t.Fatal("Resolve returned no error, want the download failure")
	}
	var pe *probeError
	if errors.As(err, &pe) {
		t.Errorf("error = %v, want the download error, not the probe's", err)
	}
	if *hits == 0 {
		t.Error("no request reached the release server: the download never started")
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("the cached binary ran %d times, want 1", n)
	}
	select {
	case ev, ok := <-events:
		if !ok || ev != ResolveDownloading {
			t.Errorf("event = %v, %t; want ResolveDownloading", ev, ok)
		}
	default:
		t.Error("no ResolveDownloading event was sent")
	}
	for _, path := range []string{binary, sidecarPath(binary)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still present after a positive probe failure: %v", filepath.Base(path), err)
		}
	}
}

func TestResolveDownloadsWhenNothingIsCached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the download stub needs the POSIX temp-dir layout the other tests use")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	base, hits := refusingRelease(t)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, noPython)
	if err == nil {
		t.Fatal("Resolve returned no error, want the download failure")
	}
	var pe *probeError
	if errors.As(err, &pe) {
		t.Errorf("error = %v, want the download error, not the probe's", err)
	}
	if *hits == 0 {
		t.Error("no request reached the release server: the download never started")
	}
	select {
	case ev, ok := <-events:
		if !ok || ev != ResolveDownloading {
			t.Errorf("event = %v, %t; want ResolveDownloading", ev, ok)
		}
	default:
		t.Error("no ResolveDownloading event was sent")
	}
}

// --- the zipapp release asset (#19) -----------------------------------------

// noPython is a host with no python3 anywhere: the bundle's case. Its goos is
// the runtime's so the bundle it leads to is the one fakeRelease serves for
// this platform.
var noPython = pythonEnv{
	goos:        runtime.GOOS,
	lookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
	systemDir:   "/usr/bin",
	xcodeSelect: "/usr/bin/xcode-select",
	timeout:     time.Minute,
	delay:       300 * time.Millisecond,
}

// pythonHost is a host whose python3 is a fake /bin/sh script with body,
// handed out by lookPath directly so PATH does not matter, and logging every
// run so a test can prove it was not executed. The script's directory comes
// back resolved, so it can stand in for env.systemDir, and it is where
// env.xcodeSelect points, so a test that drops a fake xcode-select there with
// fakeTool has it consulted instead of the real one.
func pythonHost(t *testing.T, goos, body string) (env pythonEnv, dir, runLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runLog = fakeTool(t, dir, "python3", body)
	path := filepath.Join(dir, "python3")
	env = pythonEnv{
		goos:        goos,
		lookPath:    func(string) (string, error) { return path, nil },
		systemDir:   "/usr/bin",
		xcodeSelect: filepath.Join(dir, "xcode-select"),
		timeout:     time.Minute,
		delay:       300 * time.Millisecond,
	}
	return env, dir, runLog
}

// hasPython is a host whose python3 answers 3.12: the zipapp's case.
func hasPython(t *testing.T) pythonEnv {
	t.Helper()
	env, _, _ := pythonHost(t, runtime.GOOS, "echo 'Python 3.12.1'\n")
	return env
}

// toolDir makes a directory for fake python3 and xcode-select scripts, puts it
// alone on PATH, and returns it with symlinks resolved, so it compares equal
// to what usablePython resolves a path inside it to (/var is a symlink to
// /private/var on darwin).
func toolDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

// fakeTool drops an executable /bin/sh script called name in dir that logs
// every run before doing body, and returns the log's path.
func fakeTool(t *testing.T, dir, name, body string) (runLog string) {
	t.Helper()
	runLog = filepath.Join(dir, name+".log")
	script := fmt.Sprintf("#!/bin/sh\necho run >> '%s'\n%s", runLog, body)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return runLog
}

// testPythonEnv is a pythonEnv that looks python3 up on the PATH toolDir set,
// runs the xcode-select in dir, and treats systemDir as the Command Line
// Tools directory.
func testPythonEnv(goos, dir, systemDir string) pythonEnv {
	return pythonEnv{
		goos:        goos,
		lookPath:    exec.LookPath,
		systemDir:   systemDir,
		xcodeSelect: filepath.Join(dir, "xcode-select"),
		timeout:     time.Minute,
		delay:       300 * time.Millisecond,
	}
}

func TestUsablePythonIsFalseWithoutPython3(t *testing.T) {
	dir := toolDir(t)

	if usablePython(t.Context(), testPythonEnv("linux", dir, "/usr/bin")) {
		t.Fatal("usablePython() = true with nothing on PATH")
	}
}

func TestUsablePythonReadsTheVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"3.8 is too old", "echo 'Python 3.8.10'\n", false},
		{"3.9 is too old", "echo 'Python 3.9.6'\n", false},
		{"3.10 is the floor", "echo 'Python 3.10.0'\n", true},
		{"3.12", "echo 'Python 3.12.1'\n", true},
		{"3.13", "echo 'Python 3.13.0'\n", true},
		{"version printed on stderr", "echo 'Python 3.12.1' >&2\n", true},
		{"python 2 answers on stderr", "echo 'Python 2.7.18' >&2\n", false},
		{"not python at all", "echo 'Perl 5'\n", false},
		{"refuses to run", "echo 'Python 3.12.1'\nexit 1\n", false},
		{"prints nothing", "exit 0\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := toolDir(t)
			runLog := fakeTool(t, dir, "python3", tt.body)

			if got := usablePython(t.Context(), testPythonEnv("linux", dir, "/usr/bin")); got != tt.want {
				t.Errorf("usablePython() = %t, want %t", got, tt.want)
			}
			if n := runsLogged(t, runLog); n != 1 {
				t.Errorf("python3 ran %d times, want 1", n)
			}
		})
	}
}

func TestUsablePythonTimeoutIsFalseNotAnError(t *testing.T) {
	// sleep is spelled with its path because toolDir leaves only itself on
	// PATH, and "sleep: not found" would be an exit 127, not a hang. The
	// timeout leaves room for macOS's first exec of a freshly written file,
	// which alone takes a few hundred milliseconds before the shell reaches
	// its first line.
	dir := toolDir(t)
	runLog := fakeTool(t, dir, "python3", "/bin/sleep 30\n")
	env := testPythonEnv("linux", dir, "/usr/bin")
	env.timeout = 2 * time.Second

	start := time.Now()
	got := usablePython(t.Context(), env)
	elapsed := time.Since(start)

	if got {
		t.Error("usablePython() = true for a python3 that never answered")
	}
	if elapsed > 10*time.Second {
		t.Errorf("usablePython() took %s, want the timeout to have cut the run", elapsed)
	}
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("python3 ran %d times, want 1", n)
	}
}

func TestUsablePythonChecksCommandLineToolsBeforeRunningTheSystemPython(t *testing.T) {
	// On darwin, /usr/bin/python3 without the Command Line Tools is a stub
	// that opens the install dialog instead of running. The fake tool dir
	// stands in for /usr/bin, so the same script is the "system" python3 in
	// the darwin cases and an ordinary one in the linux case.
	tests := []struct {
		name          string
		goos          string
		xcodeSelect   string
		want          bool
		wantXcodeRuns int
		wantPyRuns    int
	}{
		{"darwin, tools missing", "darwin", "exit 1\n", false, 1, 0},
		{"darwin, tools installed", "darwin", "echo /Library/Developer/CommandLineTools\nexit 0\n", true, 1, 1},
		{"darwin, xcode-select hangs", "darwin", "/bin/sleep 30\n", false, 1, 0},
		{"linux never asks", "linux", "exit 1\n", true, 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := toolDir(t)
			pyLog := fakeTool(t, dir, "python3", "echo 'Python 3.12.1'\n")
			xcodeLog := fakeTool(t, dir, "xcode-select", tt.xcodeSelect)
			env := testPythonEnv(tt.goos, dir, dir)
			env.timeout = 2 * time.Second

			if got := usablePython(t.Context(), env); got != tt.want {
				t.Errorf("usablePython() = %t, want %t", got, tt.want)
			}
			if n := runsLogged(t, xcodeLog); n != tt.wantXcodeRuns {
				t.Errorf("xcode-select ran %d times, want %d", n, tt.wantXcodeRuns)
			}
			if n := runsLogged(t, pyLog); n != tt.wantPyRuns {
				t.Errorf("python3 ran %d times, want %d", n, tt.wantPyRuns)
			}
		})
	}
}

func TestUsablePythonOutsideTheSystemDirSkipsTheToolsCheckOnDarwin(t *testing.T) {
	// A Homebrew or pyenv python3 is not the stub; xcode-select must not be
	// consulted for it, since a failing one would wrongly rule it out.
	dir := toolDir(t)
	pyLog := fakeTool(t, dir, "python3", "echo 'Python 3.12.1'\n")
	xcodeLog := fakeTool(t, dir, "xcode-select", "exit 1\n")

	if !usablePython(t.Context(), testPythonEnv("darwin", dir, "/usr/bin")) {
		t.Error("usablePython() = false for a python3 outside the system dir")
	}
	if n := runsLogged(t, xcodeLog); n != 0 {
		t.Errorf("xcode-select ran %d times, want 0", n)
	}
	if n := runsLogged(t, pyLog); n != 1 {
		t.Errorf("python3 ran %d times, want 1", n)
	}
}

func TestZipappRunnableClassifiesACutShortToolsCheck(t *testing.T) {
	// Run under exec.CommandContext reports a context that fired mid-run as
	// the kill it sent, an *exec.ExitError saying "signal: killed", never as
	// context.Canceled or DeadlineExceeded. The check has to look at the
	// contexts itself, as classifyProbe does, or a caller's cancel is not
	// wrapped and our own timeout reads as an outside kill. This is tested
	// at the check's own seam because resolveWith wraps ctx.Err() on its own
	// before the error reaches the user, which would hide a regression here.
	tests := []struct {
		name string
		// arrange gets the fake xcode-select's run log so a cancel can wait
		// for the run to have started, and returns the context to use.
		arrange func(t *testing.T, env *pythonEnv, xcodeLog string) context.Context
		check   func(t *testing.T, err error)
	}{
		{"cancelled during the check", func(t *testing.T, _ *pythonEnv, xcodeLog string) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			go func() {
				for t.Context().Err() == nil {
					if _, err := os.Stat(xcodeLog); err == nil {
						cancel()
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			return ctx
		}, func(t *testing.T, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want it to wrap context.Canceled", err)
			}
		}},
		{"timed out", func(t *testing.T, env *pythonEnv, _ string) context.Context {
			env.timeout = 2 * time.Second
			return t.Context()
		}, func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), "xcode-select -p timed out after 2s") {
				t.Errorf("error = %q, want it to say xcode-select -p timed out", err)
			}
			if IsCancelled(err) {
				t.Errorf("IsCancelled(%v) = true, want false: our own timeout is not the caller's cancellation", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := toolDir(t)
			pyLog := fakeTool(t, dir, "python3", "echo 'Python 3.12.1'\n")
			xcodeLog := fakeTool(t, dir, "xcode-select", "/bin/sleep 30\n")
			env := testPythonEnv("darwin", dir, dir)
			ctx := tt.arrange(t, &env, xcodeLog)

			runnable, err := zipappRunnable(ctx, env)
			if err == nil {
				t.Fatalf("zipappRunnable() = %t, <nil>, want an error: nothing was learned", runnable)
			}
			if runnable {
				t.Errorf("zipappRunnable() = true alongside %v, want false", err)
			}
			tt.check(t, err)
			if n := runsLogged(t, xcodeLog); n != 1 {
				t.Errorf("xcode-select ran %d times, want 1", n)
			}
			if n := runsLogged(t, pyLog); n != 0 {
				t.Errorf("python3 ran %d times, want 0", n)
			}
		})
	}
}

func TestPythonIsRecent(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"Python 3.9.6", false},
		{"Python 3.9.25", false},
		{"Python 3.10.0", true},
		{"Python 3.13.2", true},
		{"Python 3.14.7\n", true},
		{"Python 3.8.10", false},
		{"Python 3.1", false},
		{"Python 3.", false},
		{"Python 3.x", false},
		{"Python 3.-10", false},
		{"Python 3 .10", false},
		{"Python 310", false},
		{"Python 2.7.18", false},
		{"Python 4.0.0", false},
		{"", false},
		{"python 3.12.1", false},
	}

	for _, tt := range tests {
		if got := pythonIsRecent(tt.version); got != tt.want {
			t.Errorf("pythonIsRecent(%q) = %t, want %t", tt.version, got, tt.want)
		}
	}
}

func TestChooseAsset(t *testing.T) {
	darwinPython, _, _ := pythonHost(t, "darwin", "echo 'Python 3.12.1'\n")
	linuxPython, _, _ := pythonHost(t, "linux", "echo 'Python 3.12.1'\n")
	freebsdPython, _, _ := pythonHost(t, "freebsd", "echo 'Python 3.12.1'\n")
	darwinNoPython, linuxNoPython := noPython, noPython
	darwinNoPython.goos, linuxNoPython.goos = "darwin", "linux"

	tests := []struct {
		name       string
		goarch     string
		env        pythonEnv
		wantAsset  string
		wantZipapp bool
	}{
		{"darwin with python", "arm64", darwinPython, zipappAsset, true},
		{"darwin without python", "arm64", darwinNoPython, "yt-dlp_macos", false},
		{"linux with python", "amd64", linuxPython, zipappAsset, true},
		{"linux arm64 without python", "arm64", linuxNoPython, "yt-dlp_linux_aarch64", false},
		{"freebsd stays on the bundle", "amd64", freebsdPython, "yt-dlp_linux", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, zipapp := chooseAsset(t.Context(), tt.goarch, tt.env)
			if asset != tt.wantAsset || zipapp != tt.wantZipapp {
				t.Errorf("chooseAsset(%q, %q) = %q, %t; want %q, %t", tt.env.goos, tt.goarch, asset, zipapp, tt.wantAsset, tt.wantZipapp)
			}
		})
	}
}

func TestChooseAssetNeverPicksTheZipappOnWindows(t *testing.T) {
	// "python3" on Windows is the Store stub that opens a window, so the
	// question is not even asked there.
	env := noPython
	env.goos = "windows"
	env.lookPath = func(string) (string, error) {
		t.Error("python3 was looked up on windows")
		return "", exec.ErrNotFound
	}

	asset, zipapp := chooseAsset(t.Context(), "amd64", env)
	if asset != "yt-dlp.exe" || zipapp {
		t.Errorf("chooseAsset(windows) = %q, %t; want %q, false", asset, zipapp, "yt-dlp.exe")
	}
}

func TestSidecarRecordsTheAssetAndReadsAnOldOneAsTheBundle(t *testing.T) {
	binary, _ := cachedFake(t, "2026.09.01")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}

	recordVersion(binary, info, "2026.09.01", zipappAsset)
	body, err := os.ReadFile(sidecarPath(binary))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"asset":"yt-dlp"`) {
		t.Errorf("sidecar = %s, want it to carry the asset", body)
	}
	if rec := readRecord(binary); !rec.isZipapp() {
		t.Errorf("readRecord() = %+v, want it to read back as the zipapp", rec)
	}

	// A sidecar from before #19, verbatim: no asset field at all.
	old := fmt.Sprintf(`{"version":"2026.08.19","size":%d,"mtime_ns":%d}`, info.Size(), info.ModTime().UnixNano())
	if err := os.WriteFile(sidecarPath(binary), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := readRecord(binary)
	if rec.isZipapp() || rec.Asset != "" {
		t.Errorf("readRecord() = %+v, want an old record to read as the bundle", rec)
	}
	if version, ok := recordedVersion(rec, info); !ok || version != "2026.08.19" {
		t.Errorf("recordedVersion() = %q, %t; want the old record still trusted", version, ok)
	}
	// And the trust holds through Resolve without a run.
	res, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.Version != "2026.08.19" || res.Source != SourceCache {
		t.Errorf("result = %+v, want the old record's version from SourceCache", res)
	}
}

// emptyCache points Resolve at a fresh cache with nothing in it, empties PATH,
// and returns the path a download would install to.
func emptyCache(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release serves POSIX shell scripts")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	dir, err := BinDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, binaryName(runtime.GOOS))
}

// fakeRelease serves a release whose SHA2-256SUMS lists the zipapp and every
// unix bundle, each a /bin/sh script with the given body, and records which
// assets were fetched, in order, so a test can see which one Resolve chose.
// Every bundle is served so a test can pin env.goos without caring what the
// test machine is. Anything else requested fails the test.
func fakeRelease(t *testing.T, zipapp, bundle string) (base string, requested func() []string) {
	t.Helper()
	bodies := map[string]string{zipappAsset: "#!/bin/sh\n" + zipapp}
	for _, name := range []string{"yt-dlp_macos", "yt-dlp_linux", "yt-dlp_linux_aarch64"} {
		bodies[name] = "#!/bin/sh\n" + bundle
	}
	var sums strings.Builder
	for name, body := range bodies {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256([]byte(body)), name)
	}

	var mu sync.Mutex
	var assets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == checksumsAsset {
			io.WriteString(w, sums.String())
			return
		}
		body, ok := bodies[name]
		if !ok {
			t.Errorf("Resolve fetched %s, which is not a release asset", name)
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		assets = append(assets, name)
		mu.Unlock()
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/", func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), assets...)
	}
}

// downloadedOnce asserts that Resolve sent ResolveDownloading exactly once and
// then closed events. The channel must have room for two, or a second send
// would be dropped rather than seen.
func downloadedOnce(t *testing.T, events <-chan ResolveEvent) {
	t.Helper()
	if cap(events) < 2 {
		t.Fatal("events needs a buffer of two to see a second send")
	}
	ev, ok := <-events
	if !ok || ev != ResolveDownloading {
		t.Fatalf("event = %v, %t; want ResolveDownloading", ev, ok)
	}
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("Resolve sent a second event %v, want exactly one", ev)
		}
	default:
		t.Fatal("Resolve returned without closing the event channel")
	}
}

func TestResolveInstallsTheZipappWhenPythonIsUsable(t *testing.T) {
	// The two scripts answer different versions, so Version says which one
	// was installed independently of the request log.
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "echo 2026.09.01\n", "echo 2026.08.19\n")

	events := make(chan ResolveEvent, 2)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, hasPython(t))
	if err != nil {
		t.Fatalf("resolveWith() error = %v", err)
	}
	if res.Path != binary || res.Source != SourceDownload || res.Version != "2026.09.01" {
		t.Errorf("result = %+v, want the zipapp's answer from SourceDownload at %s", res, binary)
	}
	if got := strings.Join(requested(), ","); got != zipappAsset {
		t.Errorf("assets requested = %q, want only %q", got, zipappAsset)
	}
	if rec := readSidecar(t, binary); rec.Asset != zipappAsset {
		t.Errorf("sidecar = %+v, want asset %q", rec, zipappAsset)
	}
	downloadedOnce(t, events)

	// The next run answers from the record: nothing is fetched and no version
	// is asked for again.
	res, err = resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), hasPython(t))
	if err != nil {
		t.Fatalf("second resolveWith() error = %v", err)
	}
	if res.Source != SourceCache || res.Version != "2026.09.01" {
		t.Errorf("second result = %+v, want the recorded zipapp from SourceCache", res)
	}
}

func TestResolveInstallsTheBundleWithoutUsablePython(t *testing.T) {
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "echo 2026.09.01\n", "echo 2026.08.19\n")
	bundle := assetName(runtime.GOOS, runtime.GOARCH)

	events := make(chan ResolveEvent, 2)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, noPython)
	if err != nil {
		t.Fatalf("resolveWith() error = %v", err)
	}
	if res.Source != SourceDownload || res.Version != "2026.08.19" {
		t.Errorf("result = %+v, want the bundle's answer from SourceDownload", res)
	}
	if got := strings.Join(requested(), ","); got != bundle {
		t.Errorf("assets requested = %q, want only %q", got, bundle)
	}
	if rec := readSidecar(t, binary); rec.Asset != bundle {
		t.Errorf("sidecar = %+v, want asset %q", rec, bundle)
	}
	downloadedOnce(t, events)
}

func TestResolveFallsBackToTheBundleWhenTheFreshZipappRefusesToRun(t *testing.T) {
	// The zipapp downloads and verifies, then exits non-zero on its probe: a
	// python3 that passed --version but cannot run yt-dlp. That is positive,
	// so it is discarded and the bundle is fetched in the same Resolve, with
	// one ResolveDownloading for the whole thing.
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "echo boom >&2\nexit 3\n", "echo 2026.08.19\n")
	bundle := assetName(runtime.GOOS, runtime.GOARCH)

	events := make(chan ResolveEvent, 2)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, hasPython(t))
	if err != nil {
		t.Fatalf("resolveWith() error = %v, want the bundle to have taken over", err)
	}
	if res.Path != binary || res.Source != SourceDownload || res.Version != "2026.08.19" {
		t.Errorf("result = %+v, want the bundle's answer from SourceDownload", res)
	}
	if got, want := strings.Join(requested(), ","), zipappAsset+","+bundle; got != want {
		t.Errorf("assets requested = %q, want %q", got, want)
	}
	if got, want := readSidecar(t, binary), currentRecord(t, binary, "2026.08.19"); got.Asset != bundle || got.Version != want.Version || got.Size != want.Size || got.MTimeNS != want.MTimeNS {
		t.Errorf("sidecar = %+v, want %+v naming %q", got, want, bundle)
	}
	downloadedOnce(t, events)
}

func TestResolveReportsABundleThatRefusesToRunAfterTheZipappDid(t *testing.T) {
	// Both assets fail positively: the second failure is reported the way a
	// failing download always was, and nothing is left in the cache.
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "exit 3\n", "exit 4\n")

	events := make(chan ResolveEvent, 2)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, hasPython(t))
	if err == nil {
		t.Fatal("resolveWith() returned no error")
	}
	if !isBadBinary(err) || !strings.Contains(err.Error(), "downloaded yt-dlp does not run") {
		t.Errorf("error = %v, want the bundle's positive failure", err)
	}
	if got, want := strings.Join(requested(), ","), zipappAsset+","+assetName(runtime.GOOS, runtime.GOARCH); got != want {
		t.Errorf("assets requested = %q, want %q", got, want)
	}
	for _, path := range []string{binary, sidecarPath(binary)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", filepath.Base(path), err)
		}
	}
	downloadedOnce(t, events)
}

func TestResolveDoesNotRetryABundleThatRefusesToRun(t *testing.T) {
	// The retry is the zipapp's alone. A bundle that fails positively is
	// reported at once, as it always was: fetching the same asset again
	// would only fail the same way.
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "echo 2026.09.01\n", "exit 4\n")
	bundle := assetName(runtime.GOOS, runtime.GOARCH)

	events := make(chan ResolveEvent, 2)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, noPython)
	if err == nil {
		t.Fatal("resolveWith() returned no error")
	}
	if !isBadBinary(err) || !strings.Contains(err.Error(), "downloaded yt-dlp does not run") {
		t.Errorf("error = %v, want the bundle's positive failure", err)
	}
	if got := strings.Join(requested(), ","); got != bundle {
		t.Errorf("assets requested = %q, want %q exactly once", got, bundle)
	}
	for _, path := range []string{binary, sidecarPath(binary)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", filepath.Base(path), err)
		}
	}
	downloadedOnce(t, events)
}

func TestResolveKeepsAFreshZipappWhoseProbeIsInconclusive(t *testing.T) {
	// A timeout on the zipapp's probe says nothing about which asset is
	// right, so there is no second download: the file stays and the timeout
	// is reported, as it always was.
	binary := emptyCache(t)
	base, requested := fakeRelease(t, "/bin/sleep 30\n", "echo 2026.08.19\n")

	events := make(chan ResolveEvent, 2)
	_, err := resolveWith(t.Context(), events, 2*time.Second, 200*time.Millisecond, base, hasPython(t))
	if err == nil {
		t.Fatal("resolveWith() returned no error")
	}
	if isBadBinary(err) || !strings.Contains(err.Error(), "timed out after 2s") {
		t.Errorf("error = %v, want the probe timeout, inconclusive", err)
	}
	if got := strings.Join(requested(), ","); got != zipappAsset {
		t.Errorf("assets requested = %q, want only %q: an inconclusive probe must not fetch the bundle", got, zipappAsset)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Errorf("the zipapp was removed after an inconclusive probe: %v", err)
	}
	downloadedOnce(t, events)
}

func TestResolveReplacesACachedZipappWhosePythonIsGoneFromPath(t *testing.T) {
	// The zipapp's shebang is #!/usr/bin/env python3, and env is always
	// there: a python3 gone from PATH is exit 127 from env, a positive failure
	// isBadBinary already covers, never ENOENT from execve. The record is
	// stale on size so the probe runs; lookPath still finds a python3 (too
	// old to be chosen again) so the no-exec check lets the probe happen; the
	// asset choice made afresh then picks the bundle.
	binary, _ := cachedFake(t, "2026.09.01")
	if err := os.WriteFile(binary, []byte("#!/usr/bin/env nonexistent_python3_xyz\necho 2026.09.01\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := currentRecord(t, binary, "2026.09.01")
	sidecar.Size++
	sidecar.Asset = zipappAsset
	writeSidecar(t, binary, sidecar)
	env, _, _ := pythonHost(t, runtime.GOOS, "echo 'Python 3.8.10'\n")
	base, requested := fakeRelease(t, "echo 2026.09.01\n", "echo 2026.08.19\n")
	bundle := assetName(runtime.GOOS, runtime.GOARCH)

	events := make(chan ResolveEvent, 2)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, env)
	if err != nil {
		t.Fatalf("resolveWith() error = %v, want the zipapp replaced by the bundle", err)
	}
	if res.Path != binary || res.Source != SourceDownload || res.Version != "2026.08.19" {
		t.Errorf("result = %+v, want the bundle's answer from SourceDownload", res)
	}
	if got := strings.Join(requested(), ","); got != bundle {
		t.Errorf("assets requested = %q, want only %q", got, bundle)
	}
	if rec := readSidecar(t, binary); rec.Asset != bundle || rec.Version != "2026.08.19" {
		t.Errorf("sidecar = %+v, want the bundle recorded", rec)
	}
	downloadedOnce(t, events)
}

func TestResolveDiscardsATrustedZipappTheHostCanNoLongerRun(t *testing.T) {
	// A matching record would be trusted and the zipapp handed to Probe
	// blind. On darwin, with the Command Line Tools gone since the record was
	// written, `env python3` reaches the /usr/bin stub and opens the install
	// dialog on every launch, and nothing would repair it because Resolve
	// never probes. So a record naming the zipapp is checked first, without
	// executing the zipapp: xcode-select -p on darwin when python3 is the
	// stub, a bare lookup on linux. When that says no, the zipapp is not run
	// at all; it is discarded and the bundle takes its place.
	tests := []struct {
		name string
		host func(t *testing.T) (env pythonEnv, pythonLog string)
	}{
		{"darwin, system python, tools gone", func(t *testing.T) (pythonEnv, string) {
			env, dir, pyLog := pythonHost(t, "darwin", "echo 'Python 3.12.1'\n")
			env.systemDir = dir
			fakeTool(t, dir, "xcode-select", "exit 1\n")
			return env, pyLog
		}},
		{"linux, no python3 on PATH", func(t *testing.T) (pythonEnv, string) {
			env := noPython
			env.goos = "linux"
			return env, ""
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, runLog := cachedFake(t, "2026.09.01")
			sidecar := currentRecord(t, binary, "2026.09.01")
			sidecar.Asset = zipappAsset
			writeSidecar(t, binary, sidecar)
			env, pyLog := tt.host(t)
			base, requested := fakeRelease(t, "echo 2026.09.01\n", "echo 2026.08.19\n")
			bundle := assetName(env.goos, runtime.GOARCH)

			events := make(chan ResolveEvent, 2)
			res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base, env)
			if err != nil {
				t.Fatalf("resolveWith() error = %v, want the zipapp replaced by the bundle", err)
			}
			if res.Path != binary || res.Source != SourceDownload || res.Version != "2026.08.19" {
				t.Errorf("result = %+v, want the bundle's answer from SourceDownload", res)
			}
			if n := runsLogged(t, runLog); n != 0 {
				t.Errorf("the cached zipapp ran %d times, want 0: executing it is what opens the dialog", n)
			}
			if pyLog != "" {
				if n := runsLogged(t, pyLog); n != 0 {
					t.Errorf("python3 ran %d times, want 0", n)
				}
			}
			if got := strings.Join(requested(), ","); got != bundle {
				t.Errorf("assets requested = %q, want only %q", got, bundle)
			}
			if got, want := readSidecar(t, binary), currentRecord(t, binary, "2026.08.19"); got.Asset != bundle || got.Version != want.Version || got.Size != want.Size {
				t.Errorf("sidecar = %+v, want the bundle recorded as %+v", got, want)
			}
			downloadedOnce(t, events)
		})
	}
}

func TestResolveDoesNotConsultPythonForABundleRecord(t *testing.T) {
	// The no-exec check is for the zipapp alone. A record naming the bundle
	// is trusted as before, with no lookup and no xcode-select.
	binary, runLog := cachedFake(t, "2026.09.01")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Asset = assetName(runtime.GOOS, runtime.GOARCH)
	writeSidecar(t, binary, sidecar)
	dir := toolDir(t)
	xcodeLog := fakeTool(t, dir, "xcode-select", "exit 1\n")
	env := noPython
	env.goos, env.systemDir = "darwin", dir
	env.xcodeSelect = filepath.Join(dir, "xcode-select")
	env.lookPath = func(string) (string, error) {
		t.Error("python3 was looked up for a bundle record")
		return "", exec.ErrNotFound
	}

	events := make(chan ResolveEvent, 1)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), env)
	if err != nil {
		t.Fatalf("resolveWith() error = %v", err)
	}
	if res.Source != SourceCache || res.Version != "2026.08.19" {
		t.Errorf("result = %+v, want the recorded bundle from SourceCache", res)
	}
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("the cached binary ran %d times, want 0", n)
	}
	if n := runsLogged(t, xcodeLog); n != 0 {
		t.Errorf("xcode-select ran %d times, want 0", n)
	}
	drained(t, events)
}

func TestResolveTrustsAZipappRecordWhenTheHostCanRunIt(t *testing.T) {
	// The check that lets a zipapp record be trusted executes neither the
	// zipapp nor python3: lookup only, plus xcode-select on darwin for the
	// system python, which this fake is not, so a failing xcode-select must
	// not be consulted and cannot rule the record out.
	binary, runLog := cachedFake(t, "2026.09.01")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Asset = zipappAsset
	writeSidecar(t, binary, sidecar)
	env, dir, pyLog := pythonHost(t, "darwin", "echo 'Python 3.12.1'\n")
	xcodeLog := fakeTool(t, dir, "xcode-select", "exit 1\n")

	events := make(chan ResolveEvent, 1)
	res, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), env)
	if err != nil {
		t.Fatalf("resolveWith() error = %v", err)
	}
	if res.Source != SourceCache || res.Version != "2026.08.19" {
		t.Errorf("result = %+v, want the recorded zipapp from SourceCache", res)
	}
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("the cached zipapp ran %d times, want 0", n)
	}
	if n := runsLogged(t, pyLog); n != 0 {
		t.Errorf("python3 ran %d times, want 0: the record is trusted on a lookup alone", n)
	}
	if n := runsLogged(t, xcodeLog); n != 0 {
		t.Errorf("xcode-select ran %d times, want 0: this python3 is not the system stub", n)
	}
	drained(t, events)
}

func TestResolveKeepsAZipappWhenTheToolsCheckIsInconclusive(t *testing.T) {
	// xcode-select that hangs says nothing either way. The zipapp must not be
	// probed (that is the exec that opens the dialog) and must not be removed
	// (nothing positive was learned), so the run stops with the file kept.
	binary, runLog := cachedFake(t, "2026.09.01")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Asset = zipappAsset
	writeSidecar(t, binary, sidecar)
	env, dir, pyLog := pythonHost(t, "darwin", "echo 'Python 3.12.1'\n")
	env.systemDir = dir
	env.timeout = 2 * time.Second
	fakeTool(t, dir, "xcode-select", "/bin/sleep 30\n")
	before := stateOf(t, binary)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), env)

	assertKept(t, err, binary, before)
	assertSidecarKept(t, binary, sidecar)
	// The timeout is ours, so it is named as one. Run itself reports the kill
	// as "signal: killed", which would read as an outside kill.
	if !strings.Contains(err.Error(), "xcode-select -p timed out after 2s") {
		t.Errorf("error = %q, want it to say xcode-select -p timed out", err)
	}
	if IsCancelled(err) {
		t.Errorf("IsCancelled(%v) = true, want false: our own timeout is not the caller's cancellation", err)
	}
	for name, log := range map[string]string{"the cached zipapp": runLog, "python3": pyLog} {
		if n := runsLogged(t, log); n != 0 {
			t.Errorf("%s ran %d times, want 0", name, n)
		}
	}
	drained(t, events)
}

func TestResolveReportsACancelledToolsCheckAsCancelled(t *testing.T) {
	// A caller who cancels while xcode-select -p is the thing running gets
	// the same answer as one who cancels during a probe: context.Canceled,
	// wrapped, so the UI's IsCancelled route sees it. Nothing is learned
	// about the zipapp either way, so it and its record stay. Cancelling
	// before the check is the easy case (Start returns ctx.Err() without an
	// exec); cancelling during it is the one that matters, because Run then
	// reports the kill as an *exec.ExitError and only the wrap in
	// commandLineToolsInstalled says it was a cancellation.
	tests := []struct {
		name string
		// cancelled arranges for cancel to be called, given the fake
		// xcode-select's run log so it can wait for the run to start.
		cancelled     func(t *testing.T, cancel context.CancelFunc, xcodeLog string)
		wantXcodeRuns int
	}{
		{"before the check", func(_ *testing.T, cancel context.CancelFunc, _ string) {
			cancel()
		}, 0},
		{"during the check", func(t *testing.T, cancel context.CancelFunc, xcodeLog string) {
			go func() {
				for t.Context().Err() == nil {
					if _, err := os.Stat(xcodeLog); err == nil {
						cancel()
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, runLog := cachedFake(t, "2026.09.01")
			sidecar := currentRecord(t, binary, "2026.08.19")
			sidecar.Asset = zipappAsset
			writeSidecar(t, binary, sidecar)
			env, dir, pyLog := pythonHost(t, "darwin", "echo 'Python 3.12.1'\n")
			env.systemDir = dir
			xcodeLog := fakeTool(t, dir, "xcode-select", "/bin/sleep 30\n")
			before := stateOf(t, binary)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tt.cancelled(t, cancel, xcodeLog)

			events := make(chan ResolveEvent, 1)
			_, err := resolveWith(ctx, events, time.Minute, 300*time.Millisecond, noFetch(t), env)

			if err == nil {
				t.Fatal("resolveWith() returned no error")
			}
			if !IsCancelled(err) {
				t.Errorf("IsCancelled(%v) = false, want true", err)
			}
			if isBadBinary(err) {
				t.Errorf("isBadBinary(%v) = true, want false: cancelled is not failed", err)
			}
			if !strings.Contains(err.Error(), "could not fetch yt-dlp") {
				t.Errorf("error = %q, want the cancellation wording the probe path uses", err)
			}
			if after := stateOf(t, binary); after != before {
				t.Errorf("cached binary changed from %+v to %+v; it must be untouched", before, after)
			}
			assertSidecarKept(t, binary, sidecar)
			for name, log := range map[string]string{"the cached zipapp": runLog, "python3": pyLog} {
				if n := runsLogged(t, log); n != 0 {
					t.Errorf("%s ran %d times, want 0", name, n)
				}
			}
			if n := runsLogged(t, xcodeLog); n != tt.wantXcodeRuns {
				t.Errorf("xcode-select ran %d times, want %d", n, tt.wantXcodeRuns)
			}
			drained(t, events)
		})
	}
}

// --- stale temp file sweep (finding 3) --------------------------------------

func TestSweepStaleTemps(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)

	write := func(name string, modTime time.Time) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
		return path
	}

	abandoned := write("yt-dlp-123456", old)
	abandonedSidecar := write("yt-dlp-version-123456", old)
	inFlight := write("yt-dlp-987654", time.Now())
	installed := write("yt-dlp", old)
	sidecar := write("yt-dlp.version", old)
	unrelated := write("other-file", old)

	sweepStaleTemps(dir, time.Hour)

	for _, swept := range []string{abandoned, abandonedSidecar} {
		if _, err := os.Stat(swept); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("abandoned temp file %s still present: %v", filepath.Base(swept), err)
		}
	}
	for _, keep := range []string{inFlight, installed, sidecar, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed but should have been kept: %v", filepath.Base(keep), err)
		}
	}
}

func TestSweepStaleTempsIgnoresDirectoriesAndMissingDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "yt-dlp-subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(sub, old, old); err != nil {
		t.Fatal(err)
	}

	sweepStaleTemps(dir, time.Hour)
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("a directory was swept: %v", err)
	}

	// Must not panic or block when the directory does not exist yet.
	sweepStaleTemps(filepath.Join(dir, "nope"), time.Hour)
}

// --- losing the install race (finding 4) ------------------------------------

// denyWrites makes dir unwritable so os.Rename into it fails, standing in for
// Windows refusing to replace an executing image.
func denyWrites(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not gate renames on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

func TestInstallVerifiedAcceptsAWorkingBinaryAtDest(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(dest, []byte("#!/bin/sh\necho 2026.08.19\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpName := filepath.Join(dir, "yt-dlp-123456")
	if err := os.WriteFile(tmpName, []byte("#!/bin/sh\necho 2026.08.19\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	denyWrites(t, dir)

	if err := installVerified(t.Context(), tmpName, dest); err != nil {
		t.Fatalf("installVerified() = %v, want nil: a working binary is already at dest", err)
	}
	if _, err := os.Stat(tmpName); err != nil {
		t.Fatalf("the rename should have failed, leaving the temp file: %v", err)
	}
}

func TestInstallVerifiedReportsFailureWhenDestIsNotUsable(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp")
	tmpName := filepath.Join(dir, "yt-dlp-123456")
	if err := os.WriteFile(tmpName, []byte("#!/bin/sh\necho 2026.08.19\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	denyWrites(t, dir)

	if err := installVerified(t.Context(), tmpName, dest); err == nil {
		t.Fatal("installVerified() = nil, want the rename error: nothing usable is at dest")
	}
}

func TestInstallVerifiedRenamesWhenItCan(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp")
	tmpName := filepath.Join(dir, "yt-dlp-123456")
	if err := os.WriteFile(tmpName, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := installVerified(t.Context(), tmpName, dest); err != nil {
		t.Fatalf("installVerified() = %v, want nil", err)
	}
	if _, err := os.Stat(tmpName); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file survived the rename: %v", err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "payload" {
		t.Errorf("dest = %q, %v; want the temp file's contents", body, err)
	}
}
