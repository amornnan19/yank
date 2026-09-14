package main

import (
	"runtime/debug"
	"testing"
)

// vcs builds the build settings go build records from a git checkout.
func vcs(revision, modified string) []debug.BuildSetting {
	return []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"},
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: "2026-09-13T20:09:15Z"},
		{Key: "vcs.modified", Value: modified},
	}
}

func TestResolveVersion(t *testing.T) {
	const revision = "51c041b1ae8104a2e56de5dfed65cc13d1307709"

	for _, tc := range []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{
			name: "go install at a tag",
			info: &debug.BuildInfo{Main: debug.Module{Path: "github.com/amornnan19/yank", Version: "v0.2.0"}},
			ok:   true,
			want: "0.2.0",
		},
		{
			name: "go install at an untagged commit",
			info: &debug.BuildInfo{Main: debug.Module{Path: "github.com/amornnan19/yank", Version: "v0.2.1-0.20260913200915-51c041b1ae81"}},
			ok:   true,
			want: "0.2.1-0.20260913200915-51c041b1ae81",
		},
		{
			// A tagged version wins over the revision it was built from.
			name: "a module version with vcs settings",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: vcs(revision, "false")},
			ok:   true,
			want: "0.1.0",
		},
		{
			name: "a clone build with a revision",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs(revision, "false")},
			ok:   true,
			want: "dev (51c041b1ae81)",
		},
		{
			name: "a clone build with uncommitted changes",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs(revision, "true")},
			ok:   true,
			want: "dev (51c041b1ae81, modified)",
		},
		{
			name: "a revision shorter than the cut",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs("abc123", "false")},
			ok:   true,
			want: "dev (abc123)",
		},
		{
			// -buildvcs=false, or a source tree that is not a checkout.
			name: "a clone build without vcs settings",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "-buildmode", Value: "exe"}}},
			ok:   true,
			want: "dev",
		},
		{
			name: "an empty module version",
			info: &debug.BuildInfo{Settings: vcs(revision, "true")},
			ok:   true,
			want: "dev (51c041b1ae81, modified)",
		},
		{
			name: "no build info",
			info: nil,
			ok:   false,
			want: "dev",
		},
		{
			name: "build info reported but nil",
			info: nil,
			ok:   true,
			want: "dev",
		},
		{
			name:    "ldflags win over a module version",
			stamped: "9.9.9",
			info:    &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}, Settings: vcs(revision, "true")},
			ok:      true,
			want:    "9.9.9",
		},
		{
			// A stamped value is printed as given, "v" included.
			name:    "ldflags win without build info",
			stamped: "v1.2.3",
			ok:      false,
			want:    "v1.2.3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.info, tc.ok); got != tc.want {
				t.Errorf("resolveVersion(%q, …, %v) = %q, want %q", tc.stamped, tc.ok, got, tc.want)
			}
		})
	}
}
