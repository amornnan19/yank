package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	if version, ok := recordedVersion(binary, info); ok {
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
	_, err := resolveWith(t.Context(), events, 2*time.Second, 200*time.Millisecond, noFetch(t))

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
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t))

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
	// downloaded over on every launch and reported as not there.
	binary, _ := cachedFake(t, "2026.09.01")
	if err := os.WriteFile(binary, []byte("#!/nonexistent/sh\necho 2026.09.01\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := stateOf(t, binary)

	events := make(chan ResolveEvent, 1)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t))

	assertKept(t, err, binary, before)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want the ENOENT from execve in the chain: the test no longer exercises the trap", err)
	}
	if _, serr := os.Stat(sidecarPath(binary)); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("sidecar: %v, want none written", serr)
	}
	drained(t, events)
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
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base)
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
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, base)
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
