package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Keeping the cached yt-dlp up to date (#26). yt-dlp's extractors break
// whenever a site changes and the fix is always a newer yt-dlp, so a cached
// copy trusted forever eventually stops working with nothing on screen saying
// why. An update stages a release that was checksum-verified and positively
// answered --version with its own tag beside the cached copy, never over it,
// and the staged copy is promoted only under the exclusive bin-dir lock (see
// lock.go and stage.go). Every other outcome leaves the working copy and its
// sidecar exactly as they were.

const (
	// latestReleaseURL is GitHub's "latest release" page. It answers with a
	// redirect whose Location ends in /tag/<version>, which is the whole
	// lookup: no API token, no JSON, no rate-limited API endpoint.
	latestReleaseURL = "https://github.com/yt-dlp/yt-dlp/releases/latest"
	// releaseDownloadBase is the per-tag download prefix. An update fetches
	// the asset and SHA2-256SUMS from <base><tag>/, never from latest/download/,
	// so a release published mid-update cannot pair a new checksum file with
	// an old asset.
	releaseDownloadBase = "https://github.com/yt-dlp/yt-dlp/releases/download/"

	// NoUpdateEnv names the environment variable that, set to anything
	// non-empty, switches the background check off.
	NoUpdateEnv = "YANK_NO_UPDATE"

	// updateInterval is how long a completed check stands before the
	// background check looks again.
	updateInterval = 24 * time.Hour
	// lookupTimeout bounds the latest-release request. It is one redirect with
	// no body worth reading.
	lookupTimeout = 30 * time.Second
)

// UpdateStatus is what an update call did.
type UpdateStatus int

const (
	// UpdateNotChecked means nothing was looked up this call: the copy is not
	// the cached one, the check is switched off, or a completed check is still
	// recent. It is the zero value.
	UpdateNotChecked UpdateStatus = iota
	// UpdateUpToDate means the latest release is the version in use, or older.
	UpdateUpToDate
	// UpdateStaged means a newer release is verified and staged beside the
	// cached copy, and the cached path is unchanged. The next Resolve that
	// finds no other yank running puts it in place.
	UpdateStaged
	// UpdateInstalled means a newer release now sits at the cached path: the
	// call staged it and could promote it at once, because no other yank was
	// running.
	UpdateInstalled
)

// UpdateResult describes an update call. It is filled as far as the call got,
// including when it also returns an error: a lookup that completed before the
// install failed still knows the latest release.
type UpdateResult struct {
	Status UpdateStatus
	// Previous is the version that was in use when the call started.
	Previous string
	// Version is the version at the cached path when the call returned: the
	// installed release for UpdateInstalled, Previous otherwise.
	Version string
	// Staged is the release waiting beside the cached copy for UpdateStaged,
	// and "" otherwise. It is always a valid yt-dlp version.
	Staged string
	// Latest is the newest release a completed lookup knows of, from this
	// call or recorded by an earlier one, or "" when none does. It is always
	// a valid yt-dlp version, so it is safe to print.
	Latest string
	// Failed is a release whose install failed positively — its checksum did
	// not match, or it refused --version or answered with another version —
	// as this call or an earlier one recorded it, or "". Nothing recommends
	// it. It is always a valid yt-dlp version.
	Failed string
	// Kept says why a call that tried to put the staged release in place left
	// it staged instead, for UpdateStaged: an error satisfying
	// errors.Is(ErrInUse) when another yank holds the lock, which is the
	// ordinary case of two yank windows, and the reason otherwise. It is nil
	// when nothing tried, as for the background check, which never promotes.
	Kept error
}

// BackgroundUpdate is the check the interface starts after Resolve. It acts
// only on a copy from SourceCache: one found on PATH belongs to the user's
// package manager, and a fresh download is already the latest. It does
// nothing but read the record beside the binary when NoUpdateEnv is set or a
// check completed within the last 24 hours; otherwise it looks up the latest
// release and stages it. It never promotes: the interface that started it is
// itself running the cached copy.
func BackgroundUpdate(ctx context.Context, res Result) (UpdateResult, error) {
	return productionUpdater().background(ctx, res, os.Getenv)
}

// Update looks up the latest yt-dlp release and, when it is newer than the
// cached copy res describes, stages it and then promotes it if no other yank
// is running, ignoring the 24-hour check throttle. It refuses a copy found on
// PATH. A cancelled update is reported with the context's error in its chain,
// so IsCancelled recognises it.
func Update(ctx context.Context, res Result) (UpdateResult, error) {
	u := productionUpdater()
	out, err := u.update(ctx, res)
	if err != nil || out.Status != UpdateStaged {
		return out, err
	}
	return u.promote(ctx, res, out)
}

// errNotCached is Update refusing a yt-dlp it did not install.
var errNotCached = errors.New("yt-dlp is not yank's cached copy")

// updater is the update with everything a test needs to substitute: the two
// URLs, the lookup client, the clock, the probe durations, the rename, the
// locks, and the platform the bundle's name is chosen for.
type updater struct {
	latestURL    string
	downloadBase string
	// lookup is the client for the latest-release request. It must not
	// follow redirects: the redirect is the answer.
	lookup *http.Client
	// download is the client for the checksum file and the asset.
	download *http.Client
	now      func() time.Time
	timeout  time.Duration
	delay    time.Duration
	rename   func(oldpath, newpath string) error
	locks    *binLocks
	goos     string
	goarch   string
}

func productionUpdater() updater {
	return updater{
		latestURL:    latestReleaseURL,
		downloadBase: releaseDownloadBase,
		lookup:       lookupClient(lookupTimeout),
		download:     &http.Client{Timeout: httpTimeout},
		now:          time.Now,
		timeout:      versionTimeout,
		delay:        waitDelay,
		rename:       os.Rename,
		locks:        &processLocks,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
	}
}

// lookupClient is an HTTP client that hands back a redirect instead of
// following it.
func lookupClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// background is BackgroundUpdate with the environment injected.
func (u updater) background(ctx context.Context, res Result, getenv func(string) string) (UpdateResult, error) {
	if res.Source != SourceCache {
		return UpdateResult{}, nil
	}
	rec := readCheck(res.Path)
	skipped := UpdateResult{Previous: res.Version, Version: res.Version, Latest: rec.Latest, Failed: rec.FailedTag}
	if getenv(NoUpdateEnv) != "" || rec.recent(u.now()) {
		return skipped, nil
	}
	return u.update(ctx, res)
}

// errFailedRecently is update declining to fetch again a release whose install
// failed positively less than updateInterval ago.
var errFailedRecently = errors.New("that release failed to install less than 24 hours ago; yank will try it again after that")

// errWrongVersion is a downloaded release answering --version with a version
// other than its own tag.
var errWrongVersion = errors.New("the downloaded release answered --version with the wrong version")

// update stages the latest release when it is newer. It never touches the
// cached path; see promote for what puts the staged copy there. The steps, and
// what each failure means:
//
//  1. The version in use must parse; if it does not, nothing can be compared
//     and nothing is done.
//  2. The latest tag is looked up. Anything but a redirect to a tag that
//     looks like a yt-dlp version is inconclusive: no update, clock not reset.
//  3. A latest release no newer than the version in use is a completed check:
//     the clock is reset.
//  4. A complete staged copy of that release and of the cached copy's asset
//     kind is already waiting: a completed check, nothing fetched.
//  5. A release whose install failed positively within updateInterval is not
//     fetched again: errFailedRecently, clock not reset.
//  6. The same asset kind the sidecar records is fetched from that tag and
//     checksum-verified into a temp file beside the binary.
//  7. The temp file is probed with --version and must positively answer the
//     tag's version.
//  8. Only then is its sidecar written as the staged sidecar and the file
//     renamed to the staged path. The sidecar goes first, so a staged file
//     never exists without a record describing it — which is what lets
//     promote read a staged file with none as never completed.
//
// A failure at any of 6-8 removes the temp file and leaves the cached binary,
// its sidecar and any staged copy as they were: an update that could not be
// completed is not evidence that anything on disk is bad, so nothing here ever
// calls discardBinary. A positive failure at 6 or 7 — a checksum mismatch, a
// probe that ran and refused, a wrong version — records the tag as failed; a
// cancellation, a timeout, a network error or a refused rename does not. The
// latest tag is recorded as soon as step 2 has it, so the error screen can say
// a newer release exists; the check time is recorded only at 3, 4 and 8.
func (u updater) update(ctx context.Context, res Result) (UpdateResult, error) {
	out := UpdateResult{Previous: res.Version, Version: res.Version}
	switch res.Source {
	case SourceCache, SourceDownload:
	default:
		return out, fmt.Errorf("%w: %s", errNotCached, res.Path)
	}
	rec := readCheck(res.Path)
	out.Latest, out.Failed = rec.Latest, rec.FailedTag

	current, ok := parseVersion(res.Version)
	if !ok {
		return out, fmt.Errorf("could not update yt-dlp: the version in use, %q, is not one yank can compare", res.Version)
	}

	tag, err := u.latestTag(ctx)
	if err != nil {
		return out, u.failure(ctx, "could not look up the latest yt-dlp release", err)
	}
	latest, _ := parseVersion(tag) // latestTag has validated it
	out.Latest = tag
	// Known from here on, whatever the install does; the clock stays where
	// the last completed check left it until a step below completes one.
	rec.Latest = tag

	if compareVersions(latest, current) <= 0 {
		rec.CheckedNS = u.now().UnixNano()
		writeCheck(res.Path, rec)
		out.Status = UpdateUpToDate
		return out, nil
	}

	asset := assetKind(readRecord(res.Path).Asset)
	if stagedVersion(res.Path) == tag && assetKind(readRecord(stagedPath(res.Path)).Asset) == asset {
		rec.CheckedNS = u.now().UnixNano()
		writeCheck(res.Path, rec)
		out.Status, out.Staged = UpdateStaged, tag
		return out, nil
	}

	writeCheck(res.Path, rec)
	if rec.failedRecently(tag, u.now()) {
		return out, fmt.Errorf("could not update yt-dlp to %s: %w", tag, errFailedRecently)
	}

	version, err := u.install(ctx, res.Path, tag, asset)
	if err != nil {
		if ctx.Err() == nil && positiveInstallFailure(err) {
			rec.FailedTag, rec.FailedNS = tag, u.now().UnixNano()
			writeCheck(res.Path, rec)
			out.Failed = tag
		}
		return out, u.failure(ctx, "could not update yt-dlp to "+tag, err)
	}
	rec.CheckedNS = u.now().UnixNano()
	if rec.FailedTag == tag {
		rec.FailedTag, rec.FailedNS = "", 0
		out.Failed = ""
	}
	writeCheck(res.Path, rec)
	out.Status, out.Staged = UpdateStaged, version
	return out, nil
}

// positiveInstallFailure reports whether err is an install that positively
// failed because of the release itself: the checksum did not match, the
// probe ran and refused, or it answered with another version. Only these
// record a tag as failed.
func positiveInstallFailure(err error) bool {
	return errors.Is(err, errChecksumMismatch) || errors.Is(err, errWrongVersion) || isBadBinary(err)
}

// install fetches tag's release of asset, probes it, and stages it beside
// cached. See update for the rules.
func (u updater) install(ctx context.Context, cached, tag, asset string) (string, error) {
	// The temp name keeps the binary's extension: Windows will not start an
	// image whose name has none.
	pattern := "yt-dlp-*" + filepath.Ext(binaryName(u.goos))
	tmp, err := fetchVerified(ctx, u.download, u.downloadBase+tag+"/", asset, filepath.Dir(cached), pattern)
	if err != nil {
		return "", err
	}
	staged := false
	defer func() {
		if !staged {
			os.Remove(tmp)
		}
	}()

	// Stat before the probe, as Resolve does, so the sidecar cannot describe
	// a file that changed while it ran. Rename keeps size and mtime.
	info, err := os.Stat(tmp)
	if err != nil {
		return "", err
	}
	version, err := probeVersionWith(ctx, tmp, u.timeout, u.delay)
	if err != nil {
		return "", fmt.Errorf("the downloaded release did not answer --version: %w", err)
	}
	if version != tag {
		return "", fmt.Errorf("%w: %q, want %q", errWrongVersion, version, tag)
	}

	dest := stagedPath(cached)
	recordVersion(dest, info, version, asset)
	if err := u.rename(tmp, dest); err != nil {
		// The sidecar describes the temp file, which is about to be removed,
		// so it describes nothing; a staged copy it replaced no longer matches
		// it either way.
		_ = os.Remove(sidecarPath(dest))
		return "", fmt.Errorf("could not stage %s: %w", dest, err)
	}
	staged = true
	return version, nil
}

// promote tries to put the copy out.Staged describes in place of the cached
// copy res names, now, for yank --update. It needs the exclusive lock, which
// it has only when no other yank is running; otherwise the staged copy waits
// for a later Resolve and out is returned UpdateStaged, with Kept saying why:
// ErrInUse when another yank holds the lock, and the lock or promotion failure
// that kept it otherwise.
func (u updater) promote(ctx context.Context, res Result, out UpdateResult) (UpdateResult, error) {
	var version string
	var outcome promoteOutcome
	var perr error
	refused, err := u.locks.withExclusive(ctx, res.Path, func() {
		version, outcome, perr = promote(res.Path, u.rename)
	})
	switch outcome {
	case promoteDone:
		out.Status, out.Version, out.Staged = UpdateInstalled, version, ""
		return out, nil
	case promoteDiscarded:
		return out, fmt.Errorf("could not update yt-dlp to %s: %w", out.Staged, perr)
	}
	if err != nil {
		return out, u.failure(ctx, "could not update yt-dlp to "+out.Staged, err)
	}
	switch {
	case refused != nil:
		out.Kept = refused
	case outcome == promoteKept:
		out.Kept = perr
	default:
		// promoteNothing: the staged file went between staging and the lock,
		// promoted or discarded by another yank that has since exited.
		out.Kept = errStagedGone
	}
	return out, nil
}

// errStagedGone is promote finding no staged copy to put in place after the
// update had staged one.
var errStagedGone = errors.New("the staged copy was no longer there")

// latestTag asks GitHub where the latest release is and returns its tag.
func (u updater) latestTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.latestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.lookup.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("GET %s: unexpected status %s, want a redirect to the release", u.latestURL, resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("GET %s: redirect without a usable Location: %w", u.latestURL, err)
	}
	prefix, tag, found := cutLast(loc.Path, "/tag/")
	if !found || prefix == "" || !ValidVersion(tag) {
		return "", fmt.Errorf("GET %s: redirect to %q does not name a yt-dlp release tag", u.latestURL, loc.Path)
	}
	return tag, nil
}

// cutLast is strings.Cut around the last instance of sep.
func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// failure turns a failed step into the error update returns. The caller's
// cancellation is checked first and put in the chain, so IsCancelled is true
// for it. A context error that is not the caller's — an HTTP client timeout
// satisfies errors.Is(context.DeadlineExceeded) — is flattened into the text
// instead, or IsCancelled would report our own timeout as the user's ctrl+c.
func (u updater) failure(ctx context.Context, what string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("yt-dlp update cancelled: %w", cerr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %v", what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// --- versions ---------------------------------------------------------------

// ValidVersion reports whether s is a yt-dlp version: YYYY.MM.DD, with an
// optional .N. Anything else, including text a page or a binary made up, is
// not, and is never printed as one.
func ValidVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

// NewerVersion reports whether candidate is a later yt-dlp version than
// current. It is false when either is not a valid version.
func NewerVersion(candidate, current string) bool {
	a, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	b, ok := parseVersion(current)
	if !ok {
		return false
	}
	return compareVersions(a, b) > 0
}

// maxRevisionDigits bounds the optional .N so it always fits an int.
const maxRevisionDigits = 9

// parseVersion splits a yt-dlp version into year, month, day and revision,
// the revision 0 when absent. Every field must be all ASCII digits: four for
// the year, two for the month and the day, and up to maxRevisionDigits for the
// revision.
func parseVersion(s string) ([4]int, bool) {
	var out [4]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 && len(parts) != 4 {
		return out, false
	}
	widths := [3]int{4, 2, 2}
	for i, p := range parts {
		switch {
		case i < 3 && len(p) != widths[i]:
			return out, false
		case i == 3 && (len(p) == 0 || len(p) > maxRevisionDigits):
			return out, false
		}
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				return out, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// compareVersions compares two parsed versions field by field, numerically.
func compareVersions(a, b [4]int) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// --- the check record -------------------------------------------------------

// checkRecord is kept beside the binary: when the last completed check
// finished, the newest release a lookup has found, and the last release whose
// install failed positively, with when it did.
type checkRecord struct {
	CheckedNS int64  `json:"checked_ns"`
	Latest    string `json:"latest,omitempty"`
	FailedTag string `json:"failed_tag,omitempty"`
	FailedNS  int64  `json:"failed_ns,omitempty"`
}

// checkPath is where the checkRecord for binary lives.
func checkPath(binary string) string {
	return binary + ".checked"
}

// readCheck parses the record beside binary. A missing or unparsable record
// reads as the zero one, which is never recent; a Latest that is not a valid
// version is dropped, so what comes back is safe to print.
func readCheck(binary string) checkRecord {
	body, err := os.ReadFile(checkPath(binary))
	if err != nil {
		return checkRecord{}
	}
	var rec checkRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return checkRecord{}
	}
	if !ValidVersion(rec.Latest) {
		rec.Latest = ""
	}
	if !ValidVersion(rec.FailedTag) {
		rec.FailedTag, rec.FailedNS = "", 0
	}
	return rec
}

// writeCheck writes the record beside binary. Best effort: a record that
// could not be written costs the next launch a lookup.
func writeCheck(binary string, rec checkRecord) {
	body, err := json.Marshal(rec)
	if err != nil {
		return
	}
	writeAtomic(checkPath(binary), body, "yt-dlp-checked-*")
}

// recent reports whether a completed check stands at now. A check time in
// the future — a clock set back — does not: trusting it would stop checks
// until the clock caught up.
func (rec checkRecord) recent(now time.Time) bool {
	if rec.CheckedNS <= 0 {
		return false
	}
	at := time.Unix(0, rec.CheckedNS)
	return !at.After(now) && now.Sub(at) < updateInterval
}

// failedRecently reports whether tag's install failed positively less than
// updateInterval before now. A failure time in the future does not count, for
// the reason recent gives.
func (rec checkRecord) failedRecently(tag string, now time.Time) bool {
	if rec.FailedTag != tag || rec.FailedNS <= 0 {
		return false
	}
	at := time.Unix(0, rec.FailedNS)
	return !at.After(now) && now.Sub(at) < updateInterval
}
