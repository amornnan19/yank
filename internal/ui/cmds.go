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

// updateDoneMsg reports the background yt-dlp check. It carries no seq: the
// check belongs to the session, not to an attempt, and it is started at most
// once.
type updateDoneMsg struct {
	res ytdlp.UpdateResult
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

// resolveEventMsg is one milestone from a running Resolve. closed reports that
// Resolve closed the channel, which it always does before returning; it is what
// stops the drain rescheduling itself. See Model.statusLine for what the event
// changes.
type resolveEventMsg struct {
	seq    int
	ev     ytdlp.ResolveEvent
	closed bool
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

func resolveCmd(ctx context.Context, deps Deps, seq int, events chan<- ytdlp.ResolveEvent) tea.Cmd {
	return func() tea.Msg {
		res, err := deps.Resolve(ctx, events)
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

// updateCmd runs the background check. It is not counted in pending: quit
// does not wait on it, and Run cancels it once the loop has ended.
func updateCmd(ctx context.Context, deps Deps, res ytdlp.Result) tea.Cmd {
	return func() tea.Msg {
		out, err := deps.Update(ctx, res)
		return updateDoneMsg{res: out, err: err}
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

// waitResolveEvent is waitProgress for Resolve: one event off the channel, and
// the handler reschedules it until the closed message arrives.
func waitResolveEvent(ch <-chan ytdlp.ResolveEvent, seq int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return resolveEventMsg{seq: seq, closed: true}
		}
		return resolveEventMsg{seq: seq, ev: ev}
	}
}

// quitDeadlineCmd bounds the wait in updateQuitting.
func quitDeadlineCmd() tea.Cmd {
	return tea.Tick(quitGrace, func(time.Time) tea.Msg { return quitTimeoutMsg{} })
}
