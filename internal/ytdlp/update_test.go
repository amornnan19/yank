package ytdlp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- versions ---------------------------------------------------------------

func TestParseVersionAcceptsOnlyYtDlpVersions(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"2026.08.19", true},
		{"2026.08.19.1", true},
		{"2024.08.06.232818", true},
		{"2026.8.19", false},
		{"26.08.19", false},
		{"2026.08", false},
		{"2026.08.19.", false},
		{"2026.08.19.1.2", false},
		{"2026.08.19.1234567890", false},
		{"v2026.08.19", false},
		{"2026.08.19 ", false},
		{"2026.08.1x", false},
		{"2026.08.19\x1b[2J", false},
		{"+026.08.19", false},
		{"", false},
		{"nightly", false},
	}
	for _, tt := range tests {
		if got := ValidVersion(tt.in); got != tt.want {
			t.Errorf("ValidVersion(%q) = %t, want %t", tt.in, got, tt.want)
		}
	}
}

func TestVersionsCompareNumericallyFieldByField(t *testing.T) {
	tests := []struct {
		candidate, current string
		newer              bool
	}{
		{"2026.08.19", "2026.07.01", true},
		{"2026.07.01", "2026.08.19", false},
		{"2026.08.19", "2026.08.19", false},
		{"2026.08.19.1", "2026.08.19", true},
		{"2026.08.19", "2026.08.19.1", false},
		// As strings "2026.08.19.10" < "2026.08.19.9"; as numbers it is newer.
		{"2026.08.19.10", "2026.08.19.9", true},
		{"2026.08.19.9", "2026.08.19.10", false},
		{"2026.08.20", "2026.08.19.99", true},
		{"2027.01.01", "2026.12.31", true},
		{"2026.08.19.0", "2026.08.19", false},
		{"garbage", "2026.08.19", false},
		{"2026.08.19", "garbage", false},
	}
	for _, tt := range tests {
		if got := NewerVersion(tt.candidate, tt.current); got != tt.newer {
			t.Errorf("NewerVersion(%q, %q) = %t, want %t", tt.candidate, tt.current, got, tt.newer)
		}
	}
}

// --- a fake GitHub ----------------------------------------------------------

// fakeGitHub serves the latest-release redirect and a per-tag release, and
// records every request so a test can see what was fetched.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []string

	// latest answers /releases/latest. The default redirects to tag.
	latest func(w http.ResponseWriter, r *http.Request)
	tag    string
	// bodies is every asset of the release by name; sums is the checksum
	// file served beside them, built from bodies unless a test overrides it.
	bodies map[string]string
	sums   string
	// asset, when set, answers asset requests instead of bodies.
	asset func(w http.ResponseWriter, r *http.Request)
}

// newFakeGitHub publishes tag, whose zipapp and bundles are all /bin/sh
// scripts with body.
func newFakeGitHub(t *testing.T, tag, body string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{t: t, tag: tag, bodies: map[string]string{}}
	for _, name := range []string{zipappAsset, "yt-dlp_macos", "yt-dlp_linux", "yt-dlp_linux_aarch64"} {
		g.bodies[name] = "#!/bin/sh\n" + body
	}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.requests = append(g.requests, r.URL.Path)
	latest, asset := g.latest, g.asset
	g.mu.Unlock()

	switch {
	case r.URL.Path == "/releases/latest":
		if latest != nil {
			latest(w, r)
			return
		}
		http.Redirect(w, r, "/yt-dlp/yt-dlp/releases/tag/"+g.tag, http.StatusFound)
		return
	case strings.HasPrefix(r.URL.Path, "/download/"+g.tag+"/"):
		name := strings.TrimPrefix(r.URL.Path, "/download/"+g.tag+"/")
		if name == checksumsAsset {
			io.WriteString(w, g.checksums())
			return
		}
		if asset != nil {
			asset(w, r)
			return
		}
		if body, ok := g.bodies[name]; ok {
			io.WriteString(w, body)
			return
		}
	}
	g.t.Errorf("fetched %s, which this release does not have", r.URL.Path)
	http.NotFound(w, r)
}

func (g *fakeGitHub) checksums() string {
	if g.sums != "" {
		return g.sums
	}
	var b strings.Builder
	for name, body := range g.bodies {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256([]byte(body)), name)
	}
	return b.String()
}

// fetched is every request path, in order.
func (g *fakeGitHub) fetched() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

// assetsFetched is the asset requests alone: no redirect, no checksum file.
func (g *fakeGitHub) assetsFetched() []string {
	var out []string
	for _, p := range g.fetched() {
		if strings.HasPrefix(p, "/download/") && !strings.HasSuffix(p, "/"+checksumsAsset) {
			out = append(out, filepath.Base(p))
		}
	}
	return out
}

// testNow is the fixed instant every test updater reads as now.
var testNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

// updater points an updater at g, with test-sized durations and locks of its
// own, released when the test ends.
func (g *fakeGitHub) updater() updater {
	locks := &binLocks{}
	g.t.Cleanup(locks.releaseAll)
	return updater{
		latestURL:    g.srv.URL + "/releases/latest",
		downloadBase: g.srv.URL + "/download/",
		lookup:       lookupClient(5 * time.Second),
		download:     &http.Client{Timeout: 5 * time.Second},
		now:          func() time.Time { return testNow },
		timeout:      time.Minute,
		delay:        300 * time.Millisecond,
		rename:       os.Rename,
		locks:        locks,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
	}
}

// offline is an updater whose every request fails the test: the proof that a
// call looked nothing up.
func offline(t *testing.T) updater {
	t.Helper()
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	g.latest = func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("looked up %s: this call must not touch the network", r.URL.Path)
		http.NotFound(w, r)
	}
	return g.updater()
}

// --- a cached copy ----------------------------------------------------------

// cachedCopy is a working cached yt-dlp answering version, with a sidecar
// naming asset, as Resolve would have returned it.
func cachedCopy(t *testing.T, version, asset string) Result {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release serves POSIX shell scripts")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, binaryName(runtime.GOOS))
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := currentRecord(t, binary, version)
	rec.Asset = asset
	writeSidecar(t, binary, rec)
	return Result{Path: binary, Version: version, Source: SourceCache}
}

// snapshot is everything about the cached copy an update must not change on
// a failure: both files' bytes, the binary's mode and mtime, and the directory
// listing, which is also the proof that no temp file was left behind.
type snapshot struct {
	binary, sidecar []byte
	mode            os.FileMode
	mtimeNS         int64
	entries         []string
}

func snap(t *testing.T, binary string) snapshot {
	t.Helper()
	b, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile(sidecarPath(binary))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot{b, s, info.Mode(), info.ModTime().UnixNano(), entries(t, filepath.Dir(binary))}
}

// entries lists dir without the check record, which a failed install may
// legitimately write to remember the latest tag, and without the lock file,
// which any attempt to promote creates.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".checked") || strings.HasSuffix(de.Name(), ".lock") {
			continue
		}
		names = append(names, de.Name())
	}
	return names
}

func assertUntouched(t *testing.T, binary string, before snapshot) {
	t.Helper()
	after := snap(t, binary)
	if !bytes.Equal(after.binary, before.binary) {
		t.Errorf("cached binary changed:\nbefore %q\nafter  %q", before.binary, after.binary)
	}
	if !bytes.Equal(after.sidecar, before.sidecar) {
		t.Errorf("sidecar changed:\nbefore %s\nafter  %s", before.sidecar, after.sidecar)
	}
	if after.mode != before.mode || after.mtimeNS != before.mtimeNS {
		t.Errorf("cached binary mode/mtime changed from %v/%d to %v/%d", before.mode, before.mtimeNS, after.mode, after.mtimeNS)
	}
	if !slices.Equal(after.entries, before.entries) {
		t.Errorf("bin dir is %q, want %q: a temp file was left behind", after.entries, before.entries)
	}
}

// --- looking up the latest release ------------------------------------------

func TestLatestTagFollowsNoRedirectAndReadsTheTag(t *testing.T) {
	tests := []struct {
		name    string
		latest  func(w http.ResponseWriter, r *http.Request)
		timeout time.Duration
		want    string
	}{
		{
			name: "a good tag",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://github.com/yt-dlp/yt-dlp/releases/tag/2026.08.19")
				w.WriteHeader(http.StatusFound)
			},
			want: "2026.08.19",
		},
		{
			name: "a good tag with a revision, relative location",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/yt-dlp/yt-dlp/releases/tag/2026.08.19.1")
				w.WriteHeader(http.StatusMovedPermanently)
			},
			want: "2026.08.19.1",
		},
		{
			name: "a malformed tag",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://github.com/yt-dlp/yt-dlp/releases/tag/nightly")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "a tag with escape codes in it",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://github.com/yt-dlp/yt-dlp/releases/tag/2026.08.19%1B%5B2J")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "a redirect somewhere other than a tag",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://github.com/login?return_to=2026.08.19")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "no redirect",
			latest: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "<html>releases/tag/2026.08.19</html>")
			},
		},
		{
			name: "a redirect without a Location",
			latest: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name:   "404",
			latest: http.NotFound,
		},
		{
			name: "timeout",
			latest: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			},
			timeout: 200 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
			g.latest = tt.latest
			u := g.updater()
			if tt.timeout > 0 {
				u.lookup = lookupClient(tt.timeout)
			}

			tag, err := u.latestTag(t.Context())
			if tt.want != "" {
				if err != nil || tag != tt.want {
					t.Fatalf("latestTag() = %q, %v; want %q", tag, err, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("latestTag() = %q, want an error", tag)
			}
			if tag != "" {
				t.Errorf("latestTag() returned %q beside its error", tag)
			}
		})
	}
}

func TestAnInconclusiveLookupChangesNothingAndDoesNotResetTheClock(t *testing.T) {
	for name, latest := range map[string]func(w http.ResponseWriter, r *http.Request){
		"malformed tag": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/yt-dlp/yt-dlp/releases/tag/latest-and-greatest", http.StatusFound)
		},
		"no redirect": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") },
		"404":         http.NotFound,
		"timeout": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := cachedCopy(t, "2026.07.01", "")
			old := testNow.Add(-48 * time.Hour).UnixNano()
			writeCheck(res.Path, checkRecord{CheckedNS: old})
			before := snap(t, res.Path)
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
			g.latest = latest
			u := g.updater()
			u.lookup = lookupClient(200 * time.Millisecond)

			out, err := u.background(t.Context(), res, noEnv)
			if err == nil {
				t.Fatalf("background() = %+v, want an error", out)
			}
			if IsCancelled(err) {
				t.Errorf("IsCancelled(%v) = true; nobody cancelled this, and a timeout is not a ctrl+c", err)
			}
			if out.Status != UpdateNotChecked || out.Latest != "" {
				t.Errorf("result = %+v, want nothing checked and no latest", out)
			}
			assertUntouched(t, res.Path, before)
			if got := readCheck(res.Path); got.CheckedNS != old {
				t.Errorf("check time = %d, want the old %d: an inconclusive check must not reset the clock", got.CheckedNS, old)
			}
			if assets := g.assetsFetched(); len(assets) != 0 {
				t.Errorf("fetched %q after an inconclusive lookup", assets)
			}
		})
	}
}

// noEnv is an environment with nothing set.
func noEnv(string) string { return "" }

// --- up to date, and installing ---------------------------------------------

func TestUpToDateFetchesNothingAndResetsTheClock(t *testing.T) {
	for _, installed := range []string{"2026.08.19", "2026.08.19.1", "2026.09.01"} {
		t.Run(installed, func(t *testing.T) {
			res := cachedCopy(t, installed, "")
			before := snap(t, res.Path)
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")

			out, err := g.updater().update(t.Context(), res)
			if err != nil {
				t.Fatalf("update() error = %v", err)
			}
			want := UpdateResult{Status: UpdateUpToDate, Previous: installed, Version: installed, Latest: "2026.08.19"}
			if out != want {
				t.Errorf("update() = %+v, want %+v", out, want)
			}
			assertUntouched(t, res.Path, before)
			if assets := g.assetsFetched(); len(assets) != 0 {
				t.Errorf("fetched %q for an up-to-date copy", assets)
			}
			if got := readCheck(res.Path); got.CheckedNS != testNow.UnixNano() || got.Latest != "2026.08.19" {
				t.Errorf("check record = %+v, want the check time reset to now with the latest tag", got)
			}
		})
	}
}

func TestANewerReleaseIsStagedBesideTheCachedCopyNeverOverIt(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	before := snap(t, res.Path)
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	u := g.updater()
	var renames [][2]string
	u.rename = func(oldpath, newpath string) error {
		renames = append(renames, [2]string{oldpath, newpath})
		// The staged sidecar is already written when the file is renamed
		// into place, so a staged file never exists without one.
		if _, err := os.Stat(sidecarPath(newpath)); err != nil {
			t.Errorf("rename(%s, %s) before the staged sidecar was written: %v", oldpath, newpath, err)
		}
		return os.Rename(oldpath, newpath)
	}

	out, err := u.update(t.Context(), res)
	if err != nil {
		t.Fatalf("update() error = %v", err)
	}
	want := UpdateResult{Status: UpdateStaged, Previous: "2026.07.01", Version: "2026.07.01", Staged: "2026.08.19", Latest: "2026.08.19"}
	if out != want {
		t.Errorf("update() = %+v, want %+v", out, want)
	}
	staged := stagedPath(res.Path)
	if len(renames) != 1 || renames[0][1] != staged || filepath.Dir(renames[0][0]) != filepath.Dir(res.Path) {
		t.Errorf("renames = %q, want one temp file in the bin dir renamed to %s", renames, staged)
	}

	// The cached binary and its sidecar are exactly as they were.
	after := snap(t, res.Path)
	if !bytes.Equal(after.binary, before.binary) || !bytes.Equal(after.sidecar, before.sidecar) || after.mtimeNS != before.mtimeNS {
		t.Errorf("the cached copy changed: a staged update must never touch the cached path")
	}

	body, err := os.ReadFile(staged)
	if err != nil || string(body) != g.bodies[assetName(runtime.GOOS, runtime.GOARCH)] {
		t.Errorf("staged file = %q, %v; want the new release", body, err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("staged file mode %v is not executable", info.Mode())
	}
	rec := readSidecar(t, staged)
	if v, ok := recordedVersion(rec, info); !ok || v != "2026.08.19" {
		t.Errorf("staged sidecar = %+v does not describe the staged file as 2026.08.19", rec)
	}
	if got := readCheck(res.Path); got.CheckedNS != testNow.UnixNano() || got.Latest != "2026.08.19" {
		t.Errorf("check record = %+v, want the check time reset with the latest tag", got)
	}
	if got := entries(t, filepath.Dir(res.Path)); !slices.Equal(got, []string{"yt-dlp", "yt-dlp.staged", "yt-dlp.staged.version", "yt-dlp.version"}) {
		t.Errorf("bin dir = %q, want the binary, the staged copy and their sidecars", got)
	}
	for _, p := range g.fetched()[1:] {
		if !strings.HasPrefix(p, "/download/2026.08.19/") {
			t.Errorf("fetched %s, want everything from the tag's own download path", p)
		}
	}

	// A second check finds the release already staged and fetches nothing.
	g2 := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	out, err = g2.updater().update(t.Context(), res)
	if err != nil || out.Status != UpdateStaged || out.Staged != "2026.08.19" {
		t.Errorf("second update() = %+v, %v; want the staged release reported", out, err)
	}
	if assets := g2.assetsFetched(); len(assets) != 0 {
		t.Errorf("fetched %q for a release already staged", assets)
	}
}

func TestTheAssetKindTheSidecarRecordsIsKept(t *testing.T) {
	bundle := assetName(runtime.GOOS, runtime.GOARCH)
	tests := []struct {
		name, recorded, fetched, written string
	}{
		{"zipapp", zipappAsset, zipappAsset, zipappAsset},
		{"bundle", bundle, bundle, bundle},
		{"a sidecar from before the asset field is the bundle", "", bundle, bundle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := cachedCopy(t, "2026.07.01", tt.recorded)
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")

			if _, err := g.updater().update(t.Context(), res); err != nil {
				t.Fatalf("update() error = %v", err)
			}
			if got := g.assetsFetched(); !slices.Equal(got, []string{tt.fetched}) {
				t.Errorf("assets fetched = %q, want %q", got, tt.fetched)
			}
			if rec := readSidecar(t, stagedPath(res.Path)); rec.Asset != tt.written {
				t.Errorf("staged sidecar asset = %q, want %q", rec.Asset, tt.written)
			}
		})
	}
}

// --- failures leave the working copy alone ----------------------------------

func TestAFailedInstallLeavesTheWorkingCopyByteIdentical(t *testing.T) {
	tests := []struct {
		name string
		// arrange changes the release or the updater to fail the way named.
		arrange func(t *testing.T, g *fakeGitHub, u *updater)
		// positive is set when the failure is the release's own fault, which
		// records the tag as failed.
		positive bool
	}{
		{
			name:     "checksum mismatch",
			positive: true,
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				g.sums = strings.Repeat("0", 64) + "  " + assetName(runtime.GOOS, runtime.GOARCH) + "\n"
			},
		},
		{
			name:     "probe refuses (positive failure)",
			positive: true,
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				for name := range g.bodies {
					g.bodies[name] = "#!/bin/sh\necho boom >&2\nexit 2\n"
				}
			},
		},
		{
			name:     "probe answers another version",
			positive: true,
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				for name := range g.bodies {
					g.bodies[name] = "#!/bin/sh\necho 2026.08.18\n"
				}
			},
		},
		{
			name: "probe times out (inconclusive)",
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				for name := range g.bodies {
					g.bodies[name] = "#!/bin/sh\n/bin/sleep 30\n"
				}
				u.timeout, u.delay = 2*time.Second, 200*time.Millisecond
			},
		},
		{
			name: "rename refused",
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				u.rename = func(oldpath, newpath string) error {
					// The shape Windows returns while a running yank holds the image.
					return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
				}
			},
		},
		{
			name: "download fails",
			arrange: func(t *testing.T, g *fakeGitHub, u *updater) {
				g.asset = func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusBadGateway) }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := cachedCopy(t, "2026.07.01", "")
			old := testNow.Add(-48 * time.Hour).UnixNano()
			writeCheck(res.Path, checkRecord{CheckedNS: old})
			before := snap(t, res.Path)
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
			u := g.updater()
			tt.arrange(t, g, &u)

			out, err := u.background(t.Context(), res, noEnv)
			if err == nil {
				t.Fatalf("background() = %+v, want an error", out)
			}
			if IsCancelled(err) {
				t.Errorf("IsCancelled(%v) = true, want false", err)
			}
			if out.Status != UpdateNotChecked || out.Version != "2026.07.01" {
				t.Errorf("result = %+v, want nothing installed and the old version in use", out)
			}
			if out.Latest != "2026.08.19" {
				t.Errorf("Latest = %q, want the tag the lookup found", out.Latest)
			}
			assertUntouched(t, res.Path, before)
			rec := readCheck(res.Path)
			if rec.CheckedNS != old {
				t.Errorf("check time = %d, want the old %d: the install did not complete", rec.CheckedNS, old)
			}
			if rec.Latest != "2026.08.19" {
				t.Errorf("recorded latest = %q, want the tag the lookup found", rec.Latest)
			}
			wantFailed := ""
			if tt.positive {
				wantFailed = "2026.08.19"
			}
			if rec.FailedTag != wantFailed || out.Failed != wantFailed {
				t.Errorf("failed tag recorded %q, reported %q; want %q", rec.FailedTag, out.Failed, wantFailed)
			}
			if tt.positive && rec.FailedNS != testNow.UnixNano() {
				t.Errorf("failure time = %d, want now", rec.FailedNS)
			}
			if _, err := os.Stat(stagedPath(res.Path)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a staged file exists after a failed install: %v", err)
			}
		})
	}
}

func TestACancelledUpdateIsReportedAsCancelledAndLeavesNoTempFile(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(g *fakeGitHub, cancel context.CancelFunc)
	}{
		{
			name: "mid-download",
			arrange: func(g *fakeGitHub, cancel context.CancelFunc) {
				g.asset = func(w http.ResponseWriter, r *http.Request) {
					// Some bytes reach the temp file first, so there is a
					// partial file to clean up.
					w.Header().Set("Content-Length", "1000000")
					io.WriteString(w, strings.Repeat("x", 4096))
					w.(http.Flusher).Flush()
					cancel()
					<-r.Context().Done()
				}
			},
		},
		{
			name: "mid-probe",
			arrange: func(g *fakeGitHub, cancel context.CancelFunc) {
				for name := range g.bodies {
					g.bodies[name] = "#!/bin/sh\n/bin/sleep 30\n"
				}
				go func() {
					time.Sleep(time.Second)
					cancel()
				}()
			},
		},
		{
			name: "mid-lookup",
			arrange: func(g *fakeGitHub, cancel context.CancelFunc) {
				g.latest = func(w http.ResponseWriter, r *http.Request) {
					cancel()
					<-r.Context().Done()
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := cachedCopy(t, "2026.07.01", "")
			before := snap(t, res.Path)
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tt.arrange(g, cancel)
			u := g.updater()
			u.delay = 200 * time.Millisecond

			start := time.Now()
			_, err := u.update(ctx, res)
			if err == nil {
				t.Fatal("update() returned no error after a cancel")
			}
			if !IsCancelled(err) || !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want it to wrap context.Canceled", err)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("a cancelled update took %s to return", elapsed)
			}
			assertUntouched(t, res.Path, before)
		})
	}
}

// --- a release that failed ---------------------------------------------------

func TestAPositivelyFailedReleaseIsNotFetchedAgainForADay(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	bad := newFakeGitHub(t, "2026.08.19", "echo boom >&2\nexit 2\n")
	if _, err := bad.updater().update(t.Context(), res); err == nil {
		t.Fatal("update() of a release that refuses --version succeeded")
	}
	if rec := readCheck(res.Path); rec.FailedTag != "2026.08.19" {
		t.Fatalf("check record = %+v, want 2026.08.19 recorded as failed", rec)
	}

	for _, tt := range []struct {
		name    string
		after   time.Duration
		fetches bool
	}{
		{"an hour later", time.Hour, false},
		{"just under a day later", updateInterval - time.Second, false},
		{"a clock set back before the failure", -time.Hour, true},
		{"a day later", updateInterval, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The same tag, fixed: whether it is fetched is the throttle alone.
			good := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
			u := good.updater()
			now := testNow.Add(tt.after)
			u.now = func() time.Time { return now }
			// Each case starts from the recorded failure.
			writeCheck(res.Path, checkRecord{Latest: "2026.08.19", FailedTag: "2026.08.19", FailedNS: testNow.UnixNano()})
			discardStaged(res.Path)

			out, err := u.background(t.Context(), res, noEnv)
			fetched := len(good.assetsFetched()) > 0
			if fetched != tt.fetches {
				t.Fatalf("fetched the failed release again = %t, want %t (err %v)", fetched, tt.fetches, err)
			}
			if !tt.fetches {
				if !errors.Is(err, errFailedRecently) {
					t.Errorf("error = %v, want errFailedRecently", err)
				}
				if IsCancelled(err) {
					t.Errorf("IsCancelled(%v) = true", err)
				}
				if out.Status != UpdateNotChecked || out.Failed != "2026.08.19" || out.Latest != "2026.08.19" {
					t.Errorf("result = %+v, want nothing checked, the failed tag reported", out)
				}
				if rec := readCheck(res.Path); rec.CheckedNS != 0 || rec.FailedTag != "2026.08.19" {
					t.Errorf("check record = %+v, want the clock not reset and the failure kept", rec)
				}
				return
			}
			if err != nil || out.Status != UpdateStaged || out.Failed != "" {
				t.Errorf("update() = %+v, %v; want the release staged and the failure cleared", out, err)
			}
			if rec := readCheck(res.Path); rec.FailedTag != "" || rec.FailedNS != 0 {
				t.Errorf("check record = %+v, want the failure cleared by the install that worked", rec)
			}
		})
	}
}

func TestAFailureOfAnotherTagDoesNotHoldBackANewerOne(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	writeCheck(res.Path, checkRecord{Latest: "2026.08.19", FailedTag: "2026.08.19", FailedNS: testNow.UnixNano()})
	g := newFakeGitHub(t, "2026.08.20", "echo 2026.08.20\n")

	out, err := g.updater().update(t.Context(), res)
	if err != nil || out.Status != UpdateStaged || out.Staged != "2026.08.20" {
		t.Fatalf("update() = %+v, %v; want the newer tag staged", out, err)
	}
}

func TestTheThrottledCheckStillReportsTheFailedTag(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	writeCheck(res.Path, checkRecord{CheckedNS: testNow.Add(-time.Hour).UnixNano(), Latest: "2026.08.19", FailedTag: "2026.08.19", FailedNS: testNow.UnixNano()})

	out, err := offline(t).background(t.Context(), res, noEnv)
	if err != nil || out.Failed != "2026.08.19" || out.Latest != "2026.08.19" {
		t.Errorf("background() = %+v, %v; want the recorded latest and failed tag", out, err)
	}
}

// --- when it runs -----------------------------------------------------------

func TestAYtDlpOnPathIsNeverUpdated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 2020.01.01\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := Result{Path: path, Version: "2020.01.01", Source: SourcePATH}

	out, err := offline(t).background(t.Context(), res, noEnv)
	if err != nil || out != (UpdateResult{}) {
		t.Errorf("background() = %+v, %v; want nothing at all for a PATH copy", out, err)
	}
	if _, err := offline(t).update(t.Context(), res); !errors.Is(err, errNotCached) {
		t.Errorf("update() error = %v, want errNotCached", err)
	}
	if body, _ := os.ReadFile(path); string(body) != "#!/bin/sh\necho 2020.01.01\n" {
		t.Errorf("the PATH copy changed: %q", body)
	}
	if _, err := os.Stat(checkPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a check record was written beside the PATH copy: %v", err)
	}
}

func TestTheBackgroundCheckRunsOnlyForTheCachedCopy(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	for _, src := range []Source{SourcePATH, SourceDownload} {
		res := res
		res.Source = src
		if out, err := offline(t).background(t.Context(), res, noEnv); err != nil || out != (UpdateResult{}) {
			t.Errorf("background(%s) = %+v, %v; want nothing checked", src, out, err)
		}
	}
}

func TestTheBackgroundCheckIsThrottledToOnceADay(t *testing.T) {
	tests := []struct {
		name    string
		checked time.Time
		looks   bool
	}{
		{"never checked", time.Time{}, true},
		{"an hour ago", testNow.Add(-time.Hour), false},
		{"just under a day ago", testNow.Add(-updateInterval + time.Second), false},
		{"a day ago", testNow.Add(-updateInterval), true},
		{"a week ago", testNow.Add(-7 * updateInterval), true},
		{"in the future", testNow.Add(time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := cachedCopy(t, "2026.08.19", "")
			if !tt.checked.IsZero() {
				writeCheck(res.Path, checkRecord{CheckedNS: tt.checked.UnixNano(), Latest: "2026.08.19"})
			}
			g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")

			out, err := g.updater().background(t.Context(), res, noEnv)
			if err != nil {
				t.Fatalf("background() error = %v", err)
			}
			looked := len(g.fetched()) > 0
			if looked != tt.looks {
				t.Errorf("looked up the latest release = %t, want %t", looked, tt.looks)
			}
			wantStatus := UpdateNotChecked
			if tt.looks {
				wantStatus = UpdateUpToDate
			}
			if out.Status != wantStatus {
				t.Errorf("status = %v, want %v", out.Status, wantStatus)
			}
		})
	}
}

func TestYankNoUpdateSwitchesTheBackgroundCheckOff(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	writeCheck(res.Path, checkRecord{CheckedNS: testNow.Add(-48 * time.Hour).UnixNano(), Latest: "2026.08.19"})
	env := func(name string) string {
		if name == NoUpdateEnv {
			return "1"
		}
		return ""
	}

	out, err := offline(t).background(t.Context(), res, env)
	if err != nil {
		t.Fatalf("background() error = %v", err)
	}
	// Nothing looked up, but the recorded latest is still reported, so the
	// error screen can say a newer release exists.
	want := UpdateResult{Previous: "2026.07.01", Version: "2026.07.01", Latest: "2026.08.19"}
	if out != want {
		t.Errorf("background() = %+v, want %+v", out, want)
	}

	// Empty is not set.
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")
	empty := func(string) string { return "" }
	if _, err := g.updater().background(t.Context(), res, empty); err != nil {
		t.Fatalf("background() error = %v", err)
	}
	if len(g.fetched()) == 0 {
		t.Error("an empty YANK_NO_UPDATE switched the check off")
	}
}

func TestUpdateIgnoresTheThrottle(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	writeCheck(res.Path, checkRecord{CheckedNS: testNow.Add(-time.Minute).UnixNano(), Latest: "2026.07.01"})
	g := newFakeGitHub(t, "2026.08.19", "echo 2026.08.19\n")

	out, err := g.updater().update(t.Context(), res)
	if err != nil || out.Status != UpdateStaged {
		t.Fatalf("update() = %+v, %v; want the newer release staged despite a check a minute ago", out, err)
	}
}

func TestARecordedLatestThatIsNotAVersionIsNeverReported(t *testing.T) {
	res := cachedCopy(t, "2026.07.01", "")
	// Written as JSON by hand, the escape spelled \u001b, so the record parses
	// and only its latest is wrong.
	body := fmt.Sprintf(`{"checked_ns":%d,"latest":"2026.08.19\u001b[2J"}`, testNow.UnixNano())
	if err := os.WriteFile(checkPath(res.Path), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := readCheck(res.Path); rec.CheckedNS != testNow.UnixNano() {
		t.Fatalf("the record did not parse (%+v); the test no longer isolates the latest field", rec)
	}

	out, err := offline(t).background(t.Context(), res, noEnv)
	if err != nil {
		t.Fatalf("background() error = %v", err)
	}
	if out.Latest != "" {
		t.Errorf("Latest = %q, want a malformed record dropped", out.Latest)
	}
}
