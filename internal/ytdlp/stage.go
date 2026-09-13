package ytdlp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The staged copy (#26). An update installs a newer release beside the cached
// binary, never over it, because a yank may be running the cached path (see
// lock.go). The staged file has a sidecar of its own, written by the update
// that probed it, and it is renamed over the cached binary only by promote,
// under the exclusive lock.

// stagedPath is where a newer release waits to replace binary. It keeps the
// binary's extension, so on Windows it is still an image the OS will start.
func stagedPath(binary string) string {
	ext := filepath.Ext(binaryName(runtime.GOOS))
	if ext != "" && strings.HasSuffix(binary, ext) {
		return strings.TrimSuffix(binary, ext) + ".staged" + ext
	}
	return binary + ".staged"
}

// promoteOutcome is what promote did with the staged copy.
type promoteOutcome int

const (
	// promoteNothing: nothing was staged.
	promoteNothing promoteOutcome = iota
	// promoteKept: the staged copy was left for a later launch, because
	// something could not be determined or done this time.
	promoteKept
	// promoteDiscarded: the staged copy was positively not usable and was
	// removed. The cached binary was not touched.
	promoteDiscarded
	// promoteDone: the staged copy is now the cached binary.
	promoteDone
)

// errStagedUnusable reports a staged copy promote removed.
var errStagedUnusable = errors.New("the staged yt-dlp could not be used")

// promote puts the staged copy in place of binary. The caller must hold the
// exclusive lock for binary: nothing else may be running it. It returns the
// version now at binary when it promoted, and an error saying why when it kept
// or discarded the staged copy.
//
// The staged copy is not the working copy, but it is a checksum-verified
// download, so it is classified like one: it is removed only on positive
// evidence that it cannot be used, and anything that could not be read or
// done keeps it for a later launch.
//
//  1. The staged file is statted. Absent: nothing to do. Any other stat
//     error: kept.
//  2. Its sidecar is read. Absent or unparsable: discarded, because staging
//     writes the sidecar before it renames the file into place, so a staged
//     file without one was never completed. Unreadable for any other reason:
//     kept.
//  3. The sidecar must describe the file exactly as it is (size, mtime, still
//     executable) and name a valid version. Otherwise: discarded.
//  4. The cached binary is statted. Absent: the staged copy is promoted, since
//     it is a verified yt-dlp and nothing is in its way. Any other stat error:
//     kept.
//  5. The cached binary's own record must describe it, so there is a version
//     to compare against. Otherwise: kept; Resolve re-probes the cached copy
//     and records it, and a later launch compares.
//  6. The staged copy must be the same asset kind as the cached one and a
//     newer version. Otherwise: discarded — the cached copy has moved on
//     since it was staged (a fresh download, a downgrade from the zipapp to
//     the bundle), and promoting it would undo that.
//  7. The staged file is renamed over the binary. Refused (Windows, while an
//     image is mapped): kept.
//  8. The staged sidecar becomes the binary's. A rename keeps size and mtime,
//     so it describes the promoted file. Best effort, as every sidecar write
//     is: one that fails costs the next Resolve a probe.
func promote(binary string, rename func(oldpath, newpath string) error) (string, promoteOutcome, error) {
	staged := stagedPath(binary)
	info, err := os.Stat(staged)
	if errors.Is(err, fs.ErrNotExist) {
		return "", promoteNothing, nil
	}
	if err != nil {
		return "", promoteKept, err
	}

	body, err := os.ReadFile(sidecarPath(staged))
	if errors.Is(err, fs.ErrNotExist) {
		discardStaged(binary)
		return "", promoteDiscarded, fmt.Errorf("%w: %s has no record", errStagedUnusable, staged)
	}
	if err != nil {
		return "", promoteKept, err
	}
	var rec versionRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		discardStaged(binary)
		return "", promoteDiscarded, fmt.Errorf("%w: its record does not parse: %v", errStagedUnusable, err)
	}
	if _, ok := recordedVersion(rec, info); !ok || !ValidVersion(rec.Version) {
		discardStaged(binary)
		return "", promoteDiscarded, fmt.Errorf("%w: its record does not describe %s", errStagedUnusable, staged)
	}

	cinfo, err := os.Stat(binary)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", promoteKept, err
	default:
		crec := readRecord(binary)
		current, ok := recordedVersion(crec, cinfo)
		if !ok || !ValidVersion(current) {
			return "", promoteKept, fmt.Errorf("the record beside %s does not describe it", binary)
		}
		if assetKind(crec.Asset) != assetKind(rec.Asset) {
			discardStaged(binary)
			return "", promoteDiscarded, fmt.Errorf("%w: it is %s and the cached copy is now %s", errStagedUnusable, assetKind(rec.Asset), assetKind(crec.Asset))
		}
		if !NewerVersion(rec.Version, current) {
			discardStaged(binary)
			return "", promoteDiscarded, fmt.Errorf("%w: it is %s and the cached copy is already %s", errStagedUnusable, rec.Version, current)
		}
	}

	if err := rename(staged, binary); err != nil {
		return "", promoteKept, fmt.Errorf("could not replace %s: %w", binary, err)
	}
	if body, err := json.Marshal(rec); err == nil {
		writeAtomic(sidecarPath(binary), body, "yt-dlp-version-*")
	}
	_ = os.Remove(sidecarPath(staged))
	return rec.Version, promoteDone, nil
}

// stagedVersion is the version of a complete staged copy beside binary, or ""
// when there is none: the file and a sidecar describing it exactly. It reads,
// and never removes anything.
func stagedVersion(binary string) string {
	staged := stagedPath(binary)
	info, err := os.Stat(staged)
	if err != nil {
		return ""
	}
	rec := readRecord(staged)
	if v, ok := recordedVersion(rec, info); ok && ValidVersion(v) {
		return v
	}
	return ""
}

// assetKind is a sidecar's asset with the empty one read as the bundle, the
// way every other reader of the sidecar reads it.
func assetKind(asset string) string {
	if asset == "" {
		return assetName(runtime.GOOS, runtime.GOARCH)
	}
	return asset
}

// discardStaged removes the staged copy beside binary and its sidecar. It
// never touches binary itself.
func discardStaged(binary string) {
	staged := stagedPath(binary)
	_ = os.Remove(staged)
	_ = os.Remove(sidecarPath(staged))
}
