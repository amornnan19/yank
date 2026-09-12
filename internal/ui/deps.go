// Package ui is the Bubble Tea interface for yank: paste a URL, pick a format,
// watch a progress bar, get the file path.
//
// The model does no work of its own. Everything that touches the network, the
// filesystem or a child process is reached through Deps and runs inside a
// tea.Cmd, so a test can drive every state of the interface without yt-dlp
// being installed and without a single byte leaving the machine.
package ui

import (
	"context"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// Deps is every call the model makes into internal/ytdlp, as function values so
// tests can substitute fakes.
//
// The zero Deps is not usable; Production fills it with the real functions.
// Each field has the signature of the ytdlp function it stands for, so wiring
// one to the wrong package function does not compile.
type Deps struct {
	// Resolve produces a yt-dlp executable, downloading one on first run. It
	// reports the start of that download on events, which may be nil, and
	// closes the channel before returning.
	Resolve func(ctx context.Context, events chan<- ytdlp.ResolveEvent) (ytdlp.Result, error)

	// Probe extracts one URL into a ProbeResult the caller then owns.
	Probe func(ctx context.Context, ytdlpPath, url string) (*ytdlp.ProbeResult, error)

	// Rank reduces a format list to the lines the picker shows.
	Rank func(info *ytdlp.VideoInfo, hasFFmpeg bool) []ytdlp.Row

	// Download fetches one row, streaming progress into updates and closing it
	// before returning.
	Download func(ctx context.Context, ytdlpPath string, probe *ytdlp.ProbeResult, row ytdlp.Row, updates chan<- ytdlp.Progress) (*ytdlp.DownloadResult, error)

	// Cleanup removes the info-json a ProbeResult owns. It is a field rather
	// than a direct method call so a test can count the calls: the model owes
	// exactly one per ProbeResult it accepted, on every path out.
	Cleanup func(probe *ytdlp.ProbeResult) error
}

// Production is Deps wired to the real internal/ytdlp functions.
func Production() Deps {
	return Deps{
		Resolve:  ytdlp.ResolveWith,
		Probe:    ytdlp.Probe,
		Rank:     ytdlp.Rank,
		Download: ytdlp.Download,
		Cleanup:  (*ytdlp.ProbeResult).Cleanup,
	}
}
