//go:build unix && !aix && !solaris

package ytdlp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdLockEnv names the lock file TestHelperProcessHoldsTheSharedLock locks.
const holdLockEnv = "YANK_TEST_HOLD_LOCK"

// TestHelperProcessHoldsTheSharedLock is not a test. Run as a child of the
// test binary with holdLockEnv set, it is another yank: it takes the shared
// lock the way Resolve does, says so, and holds it until its stdin closes.
func TestHelperProcessHoldsTheSharedLock(t *testing.T) {
	path := os.Getenv(holdLockEnv)
	if path == "" {
		t.Skip("helper process for the lock tests")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		// A lock file this process may not write, as Resolve falls back to.
		f, err = os.Open(path)
	}
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		fmt.Println("flock:", err)
		os.Exit(1)
	}
	fmt.Println("held")
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// anotherYank starts a process holding the shared lock on path, and returns
// once it holds it. release ends it and waits until it has gone, which is
// when the kernel has released its lock.
func anotherYank(t *testing.T, path string) (release func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessHoldsTheSharedLock$")
	cmd.Env = append(os.Environ(), holdLockEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("the other yank did not take the lock: %q, %v", line, err)
	}
	done := false
	release = func() {
		if done {
			return
		}
		done = true
		stdin.Close()
		cmd.Wait()
	}
	t.Cleanup(release)
	return release
}

func TestAnotherRunningYankPreventsPromotionAndTheCachedCopyStaysByteIdentical(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", zipappAsset)
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	u := g.updater()
	staged, err := u.update(t.Context(), res)
	if err != nil || staged.Status != UpdateStaged {
		t.Fatalf("update() = %+v, %v; want the release staged", staged, err)
	}
	before := snap(t, res.Path)
	release := anotherYank(t, lockPath(res.Path))

	out, err := u.promote(t.Context(), res, staged)
	if !errors.Is(out.Kept, ErrInUse) {
		t.Errorf("Kept = %v, want ErrInUse: another yank holds the lock", out.Kept)
	}
	out.Kept = nil
	if err != nil || out != staged {
		t.Fatalf("promote() with another yank running = %+v, %v; want %+v and no error", out, err, staged)
	}
	assertUntouched(t, res.Path, before)
	if stagedVersion(res.Path) != "2026.08.19" {
		t.Fatal("the staged copy did not survive a refused promotion")
	}

	// The other yank exits. This process still holds the shared lock after
	// the refusal — flock may have released it in the attempt to convert, and
	// it is taken again — so another open of the lock file is still refused.
	release()
	probe, err := os.OpenFile(lockPath(res.Path), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if granted, err := tryLock(probe, true); err != nil || granted {
		t.Errorf("an exclusive lock was granted (%t, %v) while this process should hold the shared one", granted, err)
	}
	probe.Close()

	// This process is now the only holder; converting its own lock succeeds.
	out, err = u.promote(t.Context(), res, staged)
	if err != nil || out.Status != UpdateInstalled || out.Version != "2026.08.19" || out.Staged != "" {
		t.Fatalf("promote() with the lock free = %+v, %v; want 2026.08.19 installed", out, err)
	}
	body, _ := os.ReadFile(res.Path)
	if string(body) != g.bodies[zipappAsset] {
		t.Errorf("cached binary = %q, want the new release", body)
	}
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	rec := readSidecar(t, res.Path)
	if v, ok := recordedVersion(rec, info); !ok || v != "2026.08.19" || rec.Asset != zipappAsset {
		t.Errorf("cached sidecar = %+v, want it to describe the promoted zipapp", rec)
	}
	if got := entries(t, filepath.Dir(res.Path)); !slices.Equal(got, []string{"yt-dlp", "yt-dlp.version"}) {
		t.Errorf("bin dir = %q, want the staged copy gone", got)
	}
}

// Resolve is where a staged release is normally put in place: the first launch
// that finds no other yank running.
func TestResolvePromotesOnlyWhenNoOtherYankIsRunning(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, runLog := cachedFake(t, "2026.07.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.07.01"))
	stageFake(t, binary, "2026.08.19", "")
	stagedBody, _ := os.ReadFile(stagedPath(binary))
	before := stateOf(t, binary)
	beforeSidecar := readSidecar(t, binary)
	release := anotherYank(t, lockPath(binary))

	res, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.Version != "2026.07.01" || res.Updated {
		t.Errorf("Resolve() = %+v, want the old copy, not updated, while another yank runs", res)
	}
	if after := stateOf(t, binary); after != before {
		t.Errorf("the cached binary changed while another yank was running: %+v -> %+v", before, after)
	}
	assertSidecarKept(t, binary, beforeSidecar)
	if stagedVersion(binary) != "2026.08.19" {
		t.Error("the staged copy did not survive")
	}

	// The other yank exits, and so does this one.
	release()
	processLocks.releaseAll()

	res, err = resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.Version != "2026.08.19" || !res.Updated || res.Source != SourceCache {
		t.Errorf("Resolve() = %+v, want the staged release promoted and trusted from its record", res)
	}
	if body, _ := os.ReadFile(binary); string(body) != string(stagedBody) {
		t.Errorf("cached binary = %q, want the staged file", body)
	}
	if exists(stagedPath(binary)) {
		t.Error("the staged copy is still there after promotion")
	}
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("a binary ran %d times; promotion and the trusted record need no exec", n)
	}

	// This Resolve now holds the shared lock, as a running yank must.
	probe, err := os.OpenFile(lockPath(binary), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if granted, err := tryLock(probe, true); err != nil || granted {
		t.Errorf("an exclusive lock was granted (%t, %v) after Resolve returned", granted, err)
	}
}

func TestResolveKeepsTheStagedCopyWhenTheLockCannotBeTaken(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, _ := cachedFake(t, "2026.07.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.07.01"))
	stageFake(t, binary, "2026.08.19", "")
	unopenableLock(t, binary)
	before := stateOf(t, binary)

	res, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)
	if err != nil || res.Version != "2026.07.01" || res.Updated {
		t.Errorf("Resolve() = %+v, %v; want the old copy in use and nothing promoted", res, err)
	}
	if after := stateOf(t, binary); after != before {
		t.Errorf("the cached binary changed: %+v -> %+v", before, after)
	}
	if stagedVersion(binary) != "2026.08.19" {
		t.Error("the staged copy did not survive an inconclusive lock")
	}
}

func TestResolveWaitingForTheSharedLockHonoursCancel(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, _ := cachedFake(t, "2026.07.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.07.01"))
	// A promotion in progress elsewhere holds the lock exclusively.
	other, err := os.OpenFile(lockPath(binary), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err = resolveWith(ctx, nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)
	if !IsCancelled(err) {
		t.Errorf("Resolve() error = %v, want the cancelled wait reported as cancelled", err)
	}
}

// assertRepairRefused checks that Resolve left a cached copy it would have
// repaired exactly as it was, and said why in a way no caller mistakes for a
// cancel or for a verdict on the file.
func assertRepairRefused(t *testing.T, err error, binary string, before fileState, sidecar versionRecord, why error) {
	t.Helper()
	if err == nil {
		t.Fatal("Resolve returned no error, want the repair refused")
	}
	if !errors.Is(err, why) {
		t.Errorf("error = %v, want %v in the chain", err, why)
	}
	if IsCancelled(err) {
		t.Errorf("IsCancelled(%v) = true; nobody cancelled", err)
	}
	if isBadBinary(err) {
		t.Errorf("isBadBinary(%v) = true; a lock says nothing about the file", err)
	}
	if !strings.Contains(err.Error(), binary) {
		t.Errorf("error = %q, want it to name %s", err, binary)
	}
	if !exists(binary) {
		t.Fatal("the cached binary was removed")
	}
	if after := stateOf(t, binary); after != before {
		t.Errorf("the cached binary changed from %+v to %+v", before, after)
	}
	assertSidecarKept(t, binary, sidecar)
}

// A launch whose PATH has no python3 — cron running yank --update, say — finds
// the zipapp stranded and would replace it with the bundle. Another yank may be
// running that zipapp, so the replacement waits for the exclusive lock.
func TestAnotherRunningYankStopsResolveReplacingAStrandedZipapp(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, runLog := cachedFake(t, "2026.09.01")
	sidecar := currentRecord(t, binary, "2026.09.01")
	sidecar.Asset = zipappAsset
	writeSidecar(t, binary, sidecar)
	env := noPython
	env.goos = "linux"
	before := stateOf(t, binary)
	release := anotherYank(t, lockPath(binary))

	events := make(chan ResolveEvent, 2)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), env)

	assertRepairRefused(t, err, binary, before, sidecar, ErrInUse)
	if n := runsLogged(t, runLog); n != 0 {
		t.Errorf("the cached zipapp ran %d times, want 0", n)
	}
	drained(t, events)

	// The other yank exits; this process is the only holder, and the repair
	// goes ahead.
	release()
	base, requested := fakeRelease(t, "echo 2026.09.01\n", "echo 2026.08.19\n")
	bundle := assetName(env.goos, runtime.GOARCH)
	res, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, base, env)
	if err != nil || res.Source != SourceDownload || res.Version != "2026.08.19" {
		t.Fatalf("resolveWith() = %+v, %v; want the bundle installed once the lock is free", res, err)
	}
	if got := strings.Join(requested(), ","); got != bundle {
		t.Errorf("assets requested = %q, want only %q", got, bundle)
	}
}

func TestAnotherRunningYankStopsResolveReplacingABadBinary(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, runLog := cachedFakeScript(t, "echo boom >&2\nexit 2\n")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Size++
	writeSidecar(t, binary, sidecar)
	before := stateOf(t, binary)
	anotherYank(t, lockPath(binary))

	events := make(chan ResolveEvent, 2)
	_, err := resolveWith(t.Context(), events, time.Minute, 300*time.Millisecond, noFetch(t), noPython)

	assertRepairRefused(t, err, binary, before, sidecar, ErrInUse)
	if n := runsLogged(t, runLog); n != 1 {
		t.Errorf("the cached binary ran %d times, want 1", n)
	}
	drained(t, events)
}

// A process that cannot hold the lock cannot know who else runs the file, so
// it never repairs it either.
func TestResolveWithoutTheLockNeverReplacesABadBinary(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, _ := cachedFakeScript(t, "exit 2\n")
	sidecar := currentRecord(t, binary, "2026.08.19")
	sidecar.Size++
	writeSidecar(t, binary, sidecar)
	unopenableLock(t, binary)
	before := stateOf(t, binary)

	_, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)

	assertRepairRefused(t, err, binary, before, sidecar, errUnlocked)
	if errors.Is(err, ErrInUse) {
		t.Errorf("error = %v; nothing showed another yank holds the lock", err)
	}
}

// A lock file this process may not write — left root-owned by a sudo yank —
// is still locked through a read-only open. Running unlocked would let another
// yank take the lock exclusively and promote under this one.
func TestALockFileThatCannotBeWrittenIsStillHeldShared(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0444 file")
	}
	t.Cleanup(processLocks.releaseAll)
	binary, _ := cachedFake(t, "2026.07.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.07.01"))
	if err := os.WriteFile(lockPath(binary), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := os.OpenFile(lockPath(binary), os.O_RDWR, 0); err == nil {
		t.Fatal("the lock file opened for writing: the test no longer exercises the fallback")
	}

	if _, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	probe, err := os.Open(lockPath(binary))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if granted, err := tryLock(probe, true); err != nil || granted {
		t.Errorf("an exclusive lock was granted (%t, %v) after Resolve returned: this process runs unlocked", granted, err)
	}
}

// A process that ran unlocked stays that way: a lock file that becomes
// openable later proves nothing about the yanks that started meanwhile, or
// about this process's own use of the cached copy.
func TestAProcessThatRanUnlockedNeverPromotes(t *testing.T) {
	t.Cleanup(processLocks.releaseAll)
	binary, _ := cachedFake(t, "2026.07.01")
	writeSidecar(t, binary, currentRecord(t, binary, "2026.07.01"))
	stageFake(t, binary, "2026.08.19", "")
	unopenableLock(t, binary)

	res, err := resolveWith(t.Context(), nil, time.Minute, 300*time.Millisecond, noFetch(t), noPython)
	if err != nil || res.Version != "2026.07.01" || res.Updated {
		t.Fatalf("Resolve() = %+v, %v; want the old copy and nothing promoted", res, err)
	}
	before := stateOf(t, binary)

	if err := os.Remove(lockPath(binary)); err != nil {
		t.Fatal(err)
	}
	u := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n").updater()
	u.locks = &processLocks
	in := UpdateResult{Status: UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"}

	out, err := u.promote(t.Context(), res, in)
	if err != nil || out.Status != UpdateStaged || out.Staged != "2026.08.19" {
		t.Fatalf("promote() = %+v, %v; want the release still staged", out, err)
	}
	if !errors.Is(out.Kept, errUnlocked) || errors.Is(out.Kept, ErrInUse) {
		t.Errorf("Kept = %v, want the unlocked process named as the reason", out.Kept)
	}
	if after := stateOf(t, binary); after != before {
		t.Errorf("the cached binary changed from %+v to %+v", before, after)
	}
	if stagedVersion(binary) != "2026.08.19" {
		t.Error("the staged copy did not survive")
	}
}
