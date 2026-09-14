package main

import (
	"runtime/debug"
	"strings"
)

// Version is the yank version stamped at build time with:
//
//	go build -ldflags "-X main.Version=1.2.3" ./cmd/yank
//
// Left empty, --version resolves it from the build info the Go toolchain
// embeds instead; see resolveVersion. A stamped value always wins, so a release
// build can say exactly which tag it is.
//
// It has to stay a plain package-scope string variable with no initialiser
// expression, or -X cannot reach it.
var Version string

// revisionLength is how much of vcs.revision a dev build shows: enough to find
// the commit, short enough to read.
const revisionLength = 12

// currentVersion is what --version prints for this binary.
func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(Version, info, ok)
}

// resolveVersion turns what the linker and the toolchain left in the binary
// into the one string --version prints. It is pure so every case can be tested
// with a hand-built *debug.BuildInfo.
//
//   - stamped, when -X main.Version set it, is printed as given.
//   - Main.Version, the module version: "v1.2.3" for go install …@v1.2.3, a
//     pseudo-version for an untagged commit. go build in a checkout records
//     one too, with "+dirty" for uncommitted changes. Printed without the
//     leading "v", so a tag reads "1.2.3" the way a stamped build does.
//   - "(devel)", empty, or no build info at all means there is no version to
//     report, and the result is a dev string: "dev (<revision>)" with the first
//     12 characters of vcs.revision, "dev (<revision>, modified)" when
//     vcs.modified=true, and plain "dev" when the build recorded no revision.
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "" {
		return stamped
	}
	if !ok || info == nil {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}

	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > revisionLength {
		revision = revision[:revisionLength]
	}
	if modified {
		return "dev (" + revision + ", modified)"
	}
	return "dev (" + revision + ")"
}
