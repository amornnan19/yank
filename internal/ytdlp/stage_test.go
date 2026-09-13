package ytdlp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
)

// withStaged is a cached copy answering 2026.07.01 and a complete staged copy
// of 2026.08.19 beside it, both bundles with sidecars describing them.
func withStaged(t *testing.T) Result {
	t.Helper()
	res := cachedCopy(t, "2026.07.01", "")
	stageFake(t, res.Path, "2026.08.19", "")
	return res
}

// stageFake writes a staged copy answering version beside binary, with a
// sidecar naming asset that describes it.
func stageFake(t *testing.T, binary, version, asset string) {
	t.Helper()
	staged := stagedPath(binary)
	if err := os.WriteFile(staged, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := currentRecord(t, staged, version)
	rec.Asset = asset
	writeSidecar(t, staged, rec)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestPromoteClassifiesTheStagedCopyBeforeTouchingAnything(t *testing.T) {
	bundle := assetName(runtime.GOOS, runtime.GOARCH)
	tests := []struct {
		name    string
		arrange func(t *testing.T, res Result, rename *func(string, string) error)
		want    promoteOutcome
	}{
		{
			name: "nothing staged",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				discardStaged(res.Path)
			},
			want: promoteNothing,
		},
		{
			name: "a staged file with no sidecar was never completed",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				os.Remove(sidecarPath(stagedPath(res.Path)))
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged sidecar that does not parse",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				os.WriteFile(sidecarPath(stagedPath(res.Path)), []byte("{nope"), 0o644)
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged sidecar that does not describe the file",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				staged := stagedPath(res.Path)
				rec := currentRecord(t, staged, "2026.08.19")
				rec.Size++
				writeSidecar(t, staged, rec)
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged sidecar naming no valid version",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				staged := stagedPath(res.Path)
				writeSidecar(t, staged, currentRecord(t, staged, "2026.08.19\x1b[2J"))
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged release no newer than the cached one",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				stageFake(t, res.Path, "2026.07.01", "")
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged release of another asset kind",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				stageFake(t, res.Path, "2026.08.19", zipappAsset)
			},
			want: promoteDiscarded,
		},
		{
			name: "a staged sidecar that cannot be read",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file whatever its mode")
				}
				side := sidecarPath(stagedPath(res.Path))
				os.Chmod(side, 0)
				t.Cleanup(func() { os.Chmod(side, 0o644) })
			},
			want: promoteKept,
		},
		{
			name: "the cached copy's own record does not describe it",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				rec := currentRecord(t, res.Path, "2026.07.01")
				rec.Size++
				writeSidecar(t, res.Path, rec)
			},
			want: promoteKept,
		},
		{
			name: "the rename is refused",
			arrange: func(t *testing.T, res Result, rename *func(string, string) error) {
				*rename = func(oldpath, newpath string) error {
					return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
				}
			},
			want: promoteKept,
		},
		{
			name: "a staged release of the same kind named explicitly",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {
				stageFake(t, res.Path, "2026.08.19", bundle)
			},
			want: promoteDone,
		},
		{
			name:    "a newer staged release",
			arrange: func(t *testing.T, res Result, _ *func(string, string) error) {},
			want:    promoteDone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := withStaged(t)
			rename := os.Rename
			tt.arrange(t, res, &rename)
			staged := stagedPath(res.Path)
			before := snap(t, res.Path)
			stagedBody, _ := os.ReadFile(staged)

			version, outcome, err := promote(res.Path, rename)
			if outcome != tt.want {
				t.Fatalf("promote() = %q, %v, %v; want outcome %v", version, outcome, err, tt.want)
			}

			switch tt.want {
			case promoteDone:
				if err != nil || version != "2026.08.19" {
					t.Errorf("promote() = %q, %v; want 2026.08.19", version, err)
				}
				body, _ := os.ReadFile(res.Path)
				if string(body) != string(stagedBody) {
					t.Errorf("cached binary = %q, want the staged file %q", body, stagedBody)
				}
				info, err := os.Stat(res.Path)
				if err != nil {
					t.Fatal(err)
				}
				if v, ok := recordedVersion(readSidecar(t, res.Path), info); !ok || v != "2026.08.19" {
					t.Errorf("cached sidecar does not describe the promoted file as 2026.08.19")
				}
				if got := entries(t, filepath.Dir(res.Path)); !slices.Equal(got, []string{"yt-dlp", "yt-dlp.version"}) {
					t.Errorf("bin dir = %q, want the staged copy and its sidecar gone", got)
				}
				return
			case promoteKept:
				if err == nil {
					t.Error("a kept staged copy came back without a reason")
				}
				if !exists(staged) {
					t.Error("the staged copy was removed on an inconclusive outcome")
				}
				if b, _ := os.ReadFile(staged); string(b) != string(stagedBody) {
					t.Error("the staged copy changed")
				}
			case promoteDiscarded:
				if !errors.Is(err, errStagedUnusable) {
					t.Errorf("error = %v, want errStagedUnusable", err)
				}
				if exists(staged) || exists(sidecarPath(staged)) {
					t.Error("a staged copy positively unusable was not removed with its sidecar")
				}
			}
			// On every outcome but a promotion the cached copy is untouched.
			after := snap(t, res.Path)
			if string(after.binary) != string(before.binary) || string(after.sidecar) != string(before.sidecar) || after.mtimeNS != before.mtimeNS {
				t.Errorf("the cached copy changed on outcome %v", outcome)
			}
		})
	}
}

func TestPromoteWithNothingCachedPutsTheStagedCopyInPlace(t *testing.T) {
	res := withStaged(t)
	discardBinary(res.Path)

	version, outcome, err := promote(res.Path, os.Rename)
	if outcome != promoteDone || version != "2026.08.19" || err != nil {
		t.Fatalf("promote() = %q, %v, %v; want the staged copy promoted", version, outcome, err)
	}
	if exists(stagedPath(res.Path)) || !exists(res.Path) {
		t.Error("the staged copy is not at the cached path")
	}
}

// A lock file that cannot be opened is a lock that cannot be taken: nothing
// proves no other yank is running, so the staged copy waits.
func TestAStagedCopySurvivesALockThatCannotBeTaken(t *testing.T) {
	res := withStaged(t)
	unopenableLock(t, res.Path)
	before := snap(t, res.Path)
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	u := g.updater()
	in := UpdateResult{Status: UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"}

	out, err := u.promote(t.Context(), res, in)
	if !errors.Is(out.Kept, errUnlocked) || errors.Is(out.Kept, ErrInUse) {
		t.Errorf("Kept = %v, want errUnlocked and not ErrInUse: nothing showed another yank", out.Kept)
	}
	out.Kept = nil
	if err != nil || out != in {
		t.Errorf("promote() = %+v, %v; want the staged result unchanged and no error", out, err)
	}
	assertUntouched(t, res.Path, before)
	if !exists(stagedPath(res.Path)) || !exists(sidecarPath(stagedPath(res.Path))) {
		t.Error("the staged copy or its sidecar was removed on an inconclusive lock")
	}
}

// A rename that is refused keeps the staged copy for a reason that is not
// another yank, and Kept says which.
func TestARefusedPromotionSaysWhyAndItIsNotAnotherYank(t *testing.T) {
	res := withStaged(t)
	before := snap(t, res.Path)
	u := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n").updater()
	u.rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
	}
	in := UpdateResult{Status: UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"}

	out, err := u.promote(t.Context(), res, in)
	if err != nil || out.Status != UpdateStaged {
		t.Fatalf("promote() = %+v, %v; want the release still staged and no error", out, err)
	}
	if !errors.Is(out.Kept, syscall.EACCES) || errors.Is(out.Kept, ErrInUse) {
		t.Errorf("Kept = %v, want the refused rename and not ErrInUse", out.Kept)
	}
	assertUntouched(t, res.Path, before)
}

// unopenableLock makes the lock file for binary one that cannot be opened at
// all, for reading or for writing: a symlink into a directory that does not
// exist. A directory would not do, since a directory opens read-only and flock
// locks it.
func unopenableLock(t *testing.T, binary string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "yt-dlp.lock"), lockPath(binary)); err != nil {
		t.Fatal(err)
	}
}
