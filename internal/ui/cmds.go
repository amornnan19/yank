package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// Every message an asynchronous command produces carries the seq of the attempt
// that started it. The model bumps seq whenever it abandons an attempt — esc
// out of probing, esc out of a download, a stale-info retry — so a result that
// arrives after the user has moved on is recognisable as belonging to nobody.
//
// Dropping such a message is not enough on its own: a probeDoneMsg carries a
// ProbeResult whose info-json exists on disk and is owned by whoever receives
// it. The handler cleans one up before dropping it. That is the leak this
// scheme exists to prevent, not merely a stale render.

// resolvedMsg reports what Resolve found.
type resolvedMsg struct {
	seq int
	res ytdlp.Result
	err error
}

// probeDoneMsg reports one extraction. retry marks the second probe of a
// stale-info retry, which lands in a different state and has an older
// ProbeResult to clean up behind it.
type probeDoneMsg struct {
	seq   int
	probe *ytdlp.ProbeResult
	err   error
	retry bool
}

// progressMsg is one update from a running download.
type progressMsg struct {
	seq int
	p   ytdlp.Progress
}

// progressClosedMsg reports that Download closed the progress channel, which it
// always does before returning. It is what stops the drain rescheduling itself.
type progressClosedMsg struct {
	seq int
}

// downloadDoneMsg reports the outcome of one Download call.
type downloadDoneMsg struct {
	seq int
	res *ytdlp.DownloadResult
	err error
}

// firstRunNoticeMsg fires once the binary step has taken long enough that it is
// worth saying why. See Model.statusLine for what it changes and why the delay
// is the signal.
type firstRunNoticeMsg struct {
	seq int
}

// startURLMsg carries no data: it is the command line's URL being submitted,
// once, from Init. The URL itself is already in the input box, so the handler
// reads it from there and every check the enter key performs is performed here
// too.
type startURLMsg struct{}

// startURLCmd delivers startURLMsg. It carries no seq because it starts an
// attempt rather than reporting on one.
func startURLCmd() tea.Cmd {
	return func() tea.Msg { return startURLMsg{} }
}

// quitTimeoutMsg fires when a quit has waited long enough for the runs it
// cancelled to report back.
type quitTimeoutMsg struct{}

// quitGrace bounds how long ctrl+c waits for a cancelled run to finish dying.
//
// The wait is there because runDownload removes the partial files only after
// Wait returns and the outcome has been classified, and killing the process
// tree can take up to download.go's own WaitDelay when a descendant is holding
// the pipes. Two seconds covers the ordinary kill many times over.
//
// It is a bound and not a promise: a user who pressed ctrl+c is owed an exit,
// so when it expires yank leaves anyway, and a second ctrl+c leaves at once.
// What survives then is the same .part file that quitting immediately would
// always have left, so the deadline is never worse than not waiting.
const quitGrace = 2 * time.Second

// firstRunNoticeDelay is how long resolving may take before the status line
// starts explaining itself.
//
// Resolve answers from PATH or the cache with one --version run, which is fast
// even for the self-unpacking macOS build. Past this, it is either downloading
// the release or unpacking it for the first time — both of which are "first
// run", and both of which are worth naming rather than leaving the user in
// front of a bare spinner.
const firstRunNoticeDelay = 1500 * time.Millisecond

func resolveCmd(ctx context.Context, deps Deps, seq int) tea.Cmd {
	return func() tea.Msg {
		res, err := deps.Resolve(ctx)
		return resolvedMsg{seq: seq, res: res, err: err}
	}
}

func probeCmd(ctx context.Context, deps Deps, seq int, ytdlpPath, url string, retry bool) tea.Cmd {
	return func() tea.Msg {
		probe, err := deps.Probe(ctx, ytdlpPath, url)
		return probeDoneMsg{seq: seq, probe: probe, err: err, retry: retry}
	}
}

func downloadCmd(ctx context.Context, deps Deps, seq int, ytdlpPath string, probe *ytdlp.ProbeResult, row ytdlp.Row, updates chan ytdlp.Progress) tea.Cmd {
	return func() tea.Msg {
		res, err := deps.Download(ctx, ytdlpPath, probe, row, updates)
		return downloadDoneMsg{seq: seq, res: res, err: err}
	}
}

// waitProgress takes one update off the channel and reschedules itself. The
// closed case is not an edge: Download closes the channel before it returns, so
// every download ends with one progressClosedMsg, and a drain that kept
// rescheduling past it would spin on a closed channel forever.
func waitProgress(ch <-chan ytdlp.Progress, seq int) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return progressClosedMsg{seq: seq}
		}
		return progressMsg{seq: seq, p: p}
	}
}

// quitDeadlineCmd bounds the wait in updateQuitting.
func quitDeadlineCmd() tea.Cmd {
	return tea.Tick(quitGrace, func(time.Time) tea.Msg { return quitTimeoutMsg{} })
}

func firstRunNoticeCmd(seq int) tea.Cmd {
	return tea.Tick(firstRunNoticeDelay, func(time.Time) tea.Msg {
		return firstRunNoticeMsg{seq: seq}
	})
}
