package ui

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// state is which screen the model is showing. The ordinary path runs down the
// list; stateError is reachable from any of them.
type state int

const (
	stateInput state = iota
	stateProbing
	statePicker
	stateDownloading
	stateDone
	stateError
)

// probeStep splits stateProbing into the two things that can be happening
// behind the same spinner, because "fetching yt-dlp" and "fetching video info"
// take very different amounts of time and only one of them is worth explaining.
type probeStep int

const (
	stepResolving probeStep = iota
	stepFetchingInfo
)

// Model is the whole interface. It is a value type, as tea.Model requires, and
// holds no locks: everything that could block runs in a tea.Cmd and comes back
// as a message.
type Model struct {
	deps Deps
	// ctx is the program's lifetime. Every attempt derives a cancellable child
	// from it, held in runCtx/cancel, so esc ends one attempt and not the app.
	ctx    context.Context
	runCtx context.Context
	cancel context.CancelFunc

	state state
	// seq identifies the attempt in flight. Bumped whenever one is abandoned,
	// so a message from an abandoned attempt is recognisable — see cmds.go.
	seq int

	width, height int

	input textinput.Model
	spin  spinner.Model
	bar   progress.Model
	// frame counts the spinner ticks the spinner has accepted. It is the
	// clock the indeterminate bar sweeps on: one tick moves the spinner and
	// the block together, so there is one animation clock, not two.
	frame int

	// hint is the inline complaint under the input box, e.g. for a URL that is
	// not one. It is not an error state: nothing has been attempted yet.
	hint string

	// bin is the resolved executable, kept for the rest of the session. Its
	// HasFFmpeg is what Rank is given and what the picker's hint reads.
	bin    ytdlp.Result
	hasBin bool

	url  string
	step probeStep
	// firstRun is set only by Resolve reporting that it has started a
	// download. It is knowledge, not a guess from elapsed time: the cached
	// binary alone takes ~10s to answer on darwin (measured in #16), so any
	// timer short enough to be useful fires on every run.
	firstRun  bool
	resolveCh chan ytdlp.ResolveEvent

	// startURL is a URL that came from the command line. It is submitted once,
	// from Init, and then never read again: reset builds its fresh model with
	// an empty one, so done → enter goes back to the input screen rather than
	// downloading the same thing a second time.
	startURL string

	// probe is the extraction the picker and the download are working from.
	// While it is non-nil the model owes it exactly one Cleanup.
	probe  *ytdlp.ProbeResult
	rows   []ytdlp.Row
	cursor int

	row      ytdlp.Row
	progCh   chan ytdlp.Progress
	prog     ytdlp.Progress
	hasProg  bool
	retried  bool
	retrying bool

	result *ytdlp.DownloadResult
	errMsg string

	// pending counts the yt-dlp runs that have been started and have not yet
	// reported back, across every attempt including abandoned ones. quit waits
	// on it; see updateQuitting for why.
	pending int
	// quitting marks the window between ctrl+c and the program actually ending.
	quitting bool

	// motion is the input screen's animation; see motion.go. Its zero value
	// is switched off, which is what New builds.
	motion motion
}

// New builds the model. ctx bounds the whole program: cancelling it cancels
// whatever attempt is in flight.
//
// startURL is the URL yank was started with, or "" to start on the input
// screen. It is put in the box rather than kept beside it, and submitted from
// Init through the same path the enter key takes, so a URL that is not one
// lands where a typed one would: on the input screen, with the hint under the
// box and the text still there to be fixed.
func New(ctx context.Context, deps Deps, startURL string) Model {
	in := textinput.New()
	in.Placeholder = "https://www.youtube.com/watch?v=…"
	in.Prompt = ""
	// textinput's own placeholder default is lipgloss.Color("240"), a fixed
	// 256-cube grey, which is the one kind of colour styles.go rules out.
	in.PlaceholderStyle = styles().faint
	in.Focus()

	m := Model{
		deps:     deps,
		ctx:      ctx,
		state:    stateInput,
		width:    defaultWidth,
		input:    in,
		spin:     spinner.New(spinner.WithSpinner(spinner.Dot)),
		bar:      newBar(),
		startURL: startURL,
	}
	if startURL != "" {
		m.input.SetValue(startURL)
	}
	m.layout()
	return m
}

// newBar builds the progress bar. The fill is ANSI blue, the terminal's own
// slot 4, for the reason styles.go gives; the gradient options are not used
// because WithDefaultGradient and WithGradient are hex ramps. The empty segment
// is left in the terminal's foreground rather than given a colour: the natural
// candidate, bright black, is the background colour itself on Solarized Dark,
// and an empty bar that disappears there is the fixed-grey problem again.
//
// extra is for tests. progress.New records the colour profile of the real
// stdout when it is called, which under `go test` is Ascii whatever lipgloss
// has been told, so a test that wants to see the bar's escape sequences passes
// progress.WithColorProfile here rather than rebuilding the bar itself.
func newBar(extra ...progress.Option) progress.Model {
	opts := append([]progress.Option{progress.WithoutPercentage(), progress.WithSolidFill(ansiBlue)}, extra...)
	bar := progress.New(opts...)
	bar.EmptyColor = ""
	return bar
}

// Init starts the cursor blinking. Nothing is resolved or fetched until a URL
// is submitted: the first run's download is worth showing a status line for,
// and there is no status line before there is a screen.
//
// A URL from the command line is submitted here, as a message rather than as
// work done in New, so that the transition happens in Update where every other
// one does — with the same seq and pending accounting behind it.
func (m Model) Init() tea.Cmd {
	if m.startURL == "" {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, startURLCmd())
}

// Update is the whole state machine, with the input screen's motion kept in
// step behind it: a motion tick is the clock and goes to the motion alone, and
// every other message is handled by update and then shown to syncMotion, which
// turns what it changed into motion events and keeps one tick outstanding at
// most — none at all off the input screen.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if tick, ok := msg.(motionTickMsg); ok {
		return m.handleMotionTick(tick)
	}
	prevState, prevValue := m.state, m.input.Value()
	key, isKey := msg.(tea.KeyMsg)
	entered := isKey && key.Type == tea.KeyEnter
	next, cmd := m.update(msg)
	nm := next.(Model)
	// Two statements, for the reason startDownload gives: syncMotion mutates nm.
	motionCmd := nm.syncMotion(prevState, prevValue, entered)
	return nm, tea.Batch(cmd, motionCmd)
}

// update is every message but the motion tick.
func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case resolvedMsg, probeDoneMsg, downloadDoneMsg:
		// One started run has reported back, whichever attempt it belonged to
		// and whether or not anybody still wants its answer. Counted here, in
		// one place, so no handler can forget.
		if m.pending > 0 {
			m.pending--
		}
	}

	if m.quitting {
		return m.updateQuitting(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		// Only the two waiting screens animate. Anywhere else the tick is
		// dropped and the chain stops rescheduling itself.
		if m.state != stateProbing && m.state != stateDownloading {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		// The spinner answers a tick it accepted with the next one and a
		// stale tick — from a chain an earlier attempt started — with
		// nothing, so the command is the evidence that the frame moved.
		if cmd != nil {
			m.frame++
		}
		return m, cmd

	case progress.FrameMsg:
		// One step of the bar's spring. bubbles schedules these itself, only
		// while the bar is still moving, and stops once it has arrived; off
		// the download screen the step is dropped and the chain ends here.
		if m.state != stateDownloading {
			return m, nil
		}
		bar, cmd := m.bar.Update(msg)
		m.bar = bar.(progress.Model)
		// Re-targeting happens here, at frame cadence, not on the report:
		// SetPercent drops the frame already scheduled (see the progressMsg
		// case), and this is the one place a frame is known to have landed.
		// The new frame it schedules replaces the one Update just made, so
		// exactly one chain is alive after this either way.
		if pct, known := m.prog.Percent(); known && pct/100 != m.bar.Percent() {
			cmd = m.bar.SetPercent(pct / 100)
		}
		return m, cmd

	case startURLMsg:
		// The URL yank was started with, arriving as though it had been typed
		// and submitted. Ignored anywhere but the input screen: by the time it
		// lands the user may already have gone somewhere else.
		if m.state != stateInput {
			return m, nil
		}
		return m.submit()

	case resolveEventMsg:
		// Resolve has closed the channel, or this event belongs to an attempt
		// that is over: nothing to change and nothing to reschedule.
		if msg.closed || msg.seq != m.seq || m.state != stateProbing || m.step != stepResolving {
			return m, nil
		}
		if msg.ev == ytdlp.ResolveDownloading {
			m.firstRun = true
		}
		return m, waitResolveEvent(m.resolveCh, msg.seq)

	case resolvedMsg:
		return m.handleResolved(msg)

	case probeDoneMsg:
		return m.handleProbed(msg)

	case progressMsg:
		if msg.seq != m.seq || m.state != stateDownloading {
			return m, nil
		}
		m.prog, m.hasProg = msg.p, true
		// A known percentage is handed to the bar's spring rather than drawn
		// at once, so the bar slides between yt-dlp's reports instead of
		// jumping. The label beside it still reads the report itself.
		//
		// The spring is only started here, never re-aimed: SetPercent bumps
		// the bar's tag and schedules one 16 ms frame, and Update drops any
		// frame carrying a stale tag. yt-dlp fires its hook per block and the
		// pump delivers as fast as the loop drains, so reports often land
		// closer together than a frame, and a SetPercent per report would
		// invalidate every frame before it fired — the bar would stand still
		// until the reports paused. The invariant kept instead is that
		// IsAnimating() ⇒ a frame chain is alive: a stopped bar is kicked
		// once, and a moving one is re-aimed by the FrameMsg handler.
		var animate tea.Cmd
		if pct, known := m.prog.Percent(); known && !m.bar.IsAnimating() {
			animate = m.bar.SetPercent(pct / 100)
		}
		return m, tea.Batch(waitProgress(m.progCh, msg.seq), animate)

	case progressClosedMsg:
		// Download has closed the channel; the outcome arrives separately as a
		// downloadDoneMsg. Nothing to reschedule.
		return m, nil

	case downloadDoneMsg:
		return m.handleDownloaded(msg)
	}

	if m.state == stateInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// --- keys -------------------------------------------------------------------

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quit()
	}

	switch m.state {
	case stateInput:
		switch msg.Type {
		case tea.KeyEnter:
			return m.submit()
		case tea.KeyEsc:
			return m, nil
		}
		m.hint = ""
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stateProbing:
		if msg.Type == tea.KeyEsc {
			return m.backToInput(), nil
		}

	case statePicker:
		return m.pickerKey(msg)

	case stateDownloading:
		if msg.Type == tea.KeyEsc {
			return m.backToInput(), nil
		}

	case stateDone:
		switch {
		case msg.Type == tea.KeyEnter:
			return m.reset(), textinput.Blink
		case msg.String() == "q":
			return m.quit()
		}

	case stateError:
		if msg.Type == tea.KeyEsc || msg.Type == tea.KeyEnter {
			return m.backToInput(), nil
		}
	}
	return m, nil
}

func (m Model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		return m.backToInput(), nil
	case tea.KeyUp:
		m.cursor = moveCursor(m.cursor, -1, len(m.rows))
		return m, nil
	case tea.KeyDown:
		m.cursor = moveCursor(m.cursor, 1, len(m.rows))
		return m, nil
	case tea.KeyEnter:
		if len(m.rows) == 0 {
			return m, nil
		}
		return m.startDownload(m.rows[m.cursor])
	}

	switch s := msg.String(); {
	case s == "k":
		m.cursor = moveCursor(m.cursor, -1, len(m.rows))
	case s == "j":
		m.cursor = moveCursor(m.cursor, 1, len(m.rows))
	case len(s) == 1 && s[0] >= '1' && s[0] <= '9':
		// The digit only moves the cursor. enter is the one key that commits,
		// from every row and by every route: a digit is the cheapest key to
		// press by accident and the one pressed to *look* at a row, so it must
		// not spawn yt-dlp. A digit past the last row does nothing rather than
		// clamping to the end — it named a row that is not on screen.
		if n := int(s[0] - '1'); n < len(m.rows) {
			m.cursor = n
		}
	}
	return m, nil
}

// moveCursor steps the picker without wrapping. Wrapping in a list this short
// costs more than it saves: it turns an over-press into a jump to the other
// end, which is never what the press meant.
func moveCursor(cur, delta, n int) int {
	if n == 0 {
		return 0
	}
	return min(max(cur+delta, 0), n-1)
}

// --- transitions ------------------------------------------------------------

// submit validates the typed URL and starts an attempt.
func (m Model) submit() (tea.Model, tea.Cmd) {
	target, ok := normaliseURL(m.input.Value())
	if !ok {
		m.hint = urlHint(m.input.Value())
		return m, nil
	}

	m.hint = ""
	m.url = target
	m.state = stateProbing
	m.firstRun = false
	m.retried = false
	m.startAttempt()

	if m.hasBin {
		m.step = stepFetchingInfo
		return m, tea.Batch(m.spin.Tick, probeCmd(m.runCtx, m.deps, m.seq, m.bin.Path, m.url, false))
	}
	m.step = stepResolving
	// Buffered by one so Resolve's send never waits on the drain; the drain
	// still sees the event because the channel is closed, not dropped, after it.
	m.resolveCh = make(chan ytdlp.ResolveEvent, 1)
	return m, tea.Batch(
		m.spin.Tick,
		resolveCmd(m.runCtx, m.deps, m.seq, m.resolveCh),
		waitResolveEvent(m.resolveCh, m.seq),
	)
}

// startAttempt abandons whatever was in flight and opens a new generation.
//
// The cancel is what makes esc mean something: Resolve, Probe and Download all
// take the context, and all three stop when it ends.
func (m *Model) startAttempt() {
	m.abandon()
	m.seq++
	m.pending++
	m.runCtx, m.cancel = context.WithCancel(m.ctx)
}

// abandon cancels the attempt in flight, if any. It does not touch the probe:
// who owns the info-json is a separate question, answered by releaseProbe.
func (m *Model) abandon() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.runCtx = nil
}

// releaseProbe hands the info-json back. Every path out of the picker and the
// download goes through here, and Cleanup is idempotent, so the file is removed
// once and the model never points at a path that is gone.
func (m *Model) releaseProbe() {
	if m.probe == nil {
		return
	}
	p := m.probe
	m.probe = nil
	_ = m.deps.Cleanup(p)
}

// backToInput is what esc means everywhere: this attempt is over, nothing
// failed, and the cursor goes back to the URL box.
//
// A download in flight is cancelled rather than left running — the file it
// would produce is one nobody asked for any more, and Download removes its own
// partials on a cancel.
func (m Model) backToInput() Model {
	m.abandon()
	m.releaseProbe()
	m.seq++
	m.state = stateInput
	m.rows, m.cursor = nil, 0
	m.prog, m.hasProg = ytdlp.Progress{}, false
	m.progCh = nil
	m.resolveCh = nil
	m.retrying = false
	m.errMsg = ""
	m.result = nil
	m.input.Focus()
	return m
}

// reset is done → enter: a completely fresh model, keeping only what is true
// across attempts — the deps, the terminal size, and the executable already
// resolved. Everything an attempt produced is dropped, including the typed URL.
func (m Model) reset() Model {
	// Defensive: at stateDone the download has already released both. Doing it
	// again costs nothing — releaseProbe is a no-op on a nil probe — and means
	// reset cannot become the path that leaks one.
	m.abandon()
	m.releaseProbe()

	// The fresh model gets no start URL: enter on the done screen means
	// "another one", not "that one again".
	fresh := New(m.ctx, m.deps, "")
	fresh.width, fresh.height = m.width, m.height
	fresh.bin, fresh.hasBin = m.bin, m.hasBin
	fresh.seq = m.seq + 1
	// The runs still outstanding are carried over, symmetric with seq: a
	// yt-dlp abandoned by an earlier attempt can still be dying here, and
	// pending is the only thing that makes a later ctrl+c wait for it to
	// remove its .part rather than leaving one behind. Update decrements on
	// the message whatever attempt it belonged to, so copying the count keeps
	// it honest rather than double-counting.
	fresh.pending = m.pending
	// Motion is a property of the session, not of an attempt: whether it is
	// switched on, the seeded source, the intro already played, and the tick
	// generation a stale tick has to be recognised against.
	fresh.motion = m.motion
	fresh.layout()
	return fresh
}

// quit begins shutting the program down: the runs in flight are cancelled, and
// if any of them was still going the model stays alive until it has reported
// back. See updateQuitting for why the wait is there and what bounds it.
func (m Model) quit() (tea.Model, tea.Cmd) {
	m.abandon()
	m.seq++
	if m.pending == 0 {
		return m.finishQuit()
	}
	m.quitting = true
	return m, tea.Batch(m.spin.Tick, quitDeadlineCmd())
}

// updateQuitting is the model between ctrl+c and the program ending.
//
// It exists because tea.Quit returns from Update and the process is on its way
// out before the download goroutine has finished dying. runDownload removes the
// partial files only after Wait has returned and the outcome has been
// classified, so quitting the instant the context is cancelled leaves a .part
// behind where pressing esc — which keeps the model alive — does not. The same
// window can cut short the kill of the process tree that download.go goes out
// of its way to perform. Two spellings of "stop this" must not leave different
// debris.
//
// The wait is bounded by quitGrace, and a second ctrl+c leaves immediately: a
// user who asked to quit is owed an exit, not a frozen screen.
func (m Model) updateQuitting(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m.finishQuit()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case quitTimeoutMsg:
		return m.finishQuit()

	case probeDoneMsg:
		// Whichever attempt this belonged to, nobody is going to use it and
		// this is the last code that will ever know the path.
		if msg.probe != nil {
			_ = m.deps.Cleanup(msg.probe)
		}
		return m.settle()

	case resolvedMsg, downloadDoneMsg:
		return m.settle()
	}
	return m, nil
}

// settle quits once the last outstanding run has reported back. Update has
// already decremented the count for the message that got us here.
func (m Model) settle() (tea.Model, tea.Cmd) {
	if m.pending > 0 {
		return m, nil
	}
	return m.finishQuit()
}

// finishQuit releases the info-json and ends the program. It is the only place
// tea.Quit is produced, so the cleanup cannot be skipped by a route that grew
// its own exit.
func (m Model) finishQuit() (tea.Model, tea.Cmd) {
	m.releaseProbe()
	m.motion.halted = true
	return m, tea.Quit
}

func (m Model) handleResolved(msg resolvedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.seq || m.state != stateProbing {
		return m, nil
	}
	if msg.err != nil {
		if ytdlp.IsCancelled(msg.err) {
			return m.backToInput(), nil
		}
		return m.fail(msg.err.Error()), nil
	}

	m.bin, m.hasBin = msg.res, true
	m.step = stepFetchingInfo
	m.firstRun = false
	m.resolveCh = nil
	// A second run on the same attempt, so it is counted here rather than in
	// startAttempt: resolving and probing are two runs behind one spinner.
	m.pending++
	return m, probeCmd(m.runCtx, m.deps, m.seq, m.bin.Path, m.url, false)
}

func (m Model) handleProbed(msg probeDoneMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.seq {
		// Nobody owns this one. It still has a file on disk, and this is the
		// only place left that knows about it.
		if msg.probe != nil {
			_ = m.deps.Cleanup(msg.probe)
		}
		return m, nil
	}
	if msg.retry {
		return m.handleRetryProbed(msg)
	}
	if m.state != stateProbing {
		if msg.probe != nil {
			_ = m.deps.Cleanup(msg.probe)
		}
		return m, nil
	}

	if msg.err != nil {
		if ytdlp.IsCancelled(msg.err) {
			return m.backToInput(), nil
		}
		return m.fail(describeProbeError(msg.err)), nil
	}

	m.abandon()
	m.probe = msg.probe
	m.rows = m.deps.Rank(&msg.probe.Info, m.bin.HasFFmpeg)
	m.cursor = 0
	m.state = statePicker
	return m, nil
}

// startDownload runs the chosen row. retried is cleared here rather than on the
// stale-info branch so the budget is one retry per download the user asked for,
// not one per session.
func (m Model) startDownload(row ytdlp.Row) (tea.Model, tea.Cmd) {
	m.row = row
	m.retried = false
	m.retrying = false
	m.prog, m.hasProg = ytdlp.Progress{Phase: ytdlp.PhaseDownloading}, false
	m.state = stateDownloading
	// Two statements, not one: launchDownload has a pointer receiver and
	// mutates seq, runCtx and progCh, and the order in which Go evaluates the
	// plain operand m against the call is not specified. Written as
	// "return m, m.launchDownload()" a toolchain would be free to return the
	// model from before the launch, whose progCh the drain would never fill.
	cmd := m.launchDownload()
	return m, cmd
}

// launchDownload starts one Download call and the drain that feeds the bar.
// Both are new every time, including on the stale-info retry: Download closes
// the channel it was given, so it cannot be reused.
func (m *Model) launchDownload() tea.Cmd {
	m.startAttempt()
	m.progCh = make(chan ytdlp.Progress)
	// A fresh bar, not SetPercent(0): the spring would animate down from
	// wherever the last download left it, and a bar seen sliding back from
	// full at the start of a download is a lie about this one.
	m.bar = newBar()
	return tea.Batch(
		m.spin.Tick,
		downloadCmd(m.runCtx, m.deps, m.seq, m.bin.Path, m.probe, m.row, m.progCh),
		waitProgress(m.progCh, m.seq),
	)
}

func (m Model) handleDownloaded(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.seq || m.state != stateDownloading {
		return m, nil
	}

	switch {
	case msg.err == nil:
		m.abandon()
		m.releaseProbe()
		m.result = msg.res
		m.retrying = false
		m.state = stateDone
		return m, nil

	case ytdlp.IsCancelled(msg.err):
		// The user pressed esc. That is an outcome, not a failure, and it must
		// never be phrased as one.
		return m.backToInput(), nil

	case ytdlp.IsStaleInfo(msg.err) && !m.retried:
		// The media URLs in the info-json expired while the picker was on
		// screen. Probe again for a fresh one and run the same row against it.
		// Once: a second stale error is a real one.
		m.retried = true
		m.retrying = true
		// The bar belongs to the attempt that just failed. Leaving it up under
		// the "link had expired" line shows a percentage nothing is working on.
		m.prog, m.hasProg = ytdlp.Progress{}, false
		m.startAttempt()
		return m, tea.Batch(m.spin.Tick, probeCmd(m.runCtx, m.deps, m.seq, m.bin.Path, m.url, true))
	}

	m.abandon()
	m.releaseProbe()
	return m.fail(describeDownloadError(msg.err)), nil
}

// handleRetryProbed takes the second probe of a stale-info retry. The fresh
// result has an info-json of its own, so the stale one is released here — one
// leaked file per retry would be invisible and permanent.
func (m Model) handleRetryProbed(msg probeDoneMsg) (tea.Model, tea.Cmd) {
	if m.state != stateDownloading {
		if msg.probe != nil {
			_ = m.deps.Cleanup(msg.probe)
		}
		return m, nil
	}

	m.releaseProbe()

	if msg.err != nil {
		if ytdlp.IsCancelled(msg.err) {
			return m.backToInput(), nil
		}
		return m.fail(describeProbeError(msg.err)), nil
	}

	m.probe = msg.probe
	m.retrying = false
	m.prog, m.hasProg = ytdlp.Progress{Phase: ytdlp.PhaseDownloading}, false
	// See startDownload for why this is not one expression.
	cmd := m.launchDownload()
	return m, cmd
}

// fail moves to the error screen. It is never reached for a cancellation:
// every caller checks IsCancelled first.
func (m Model) fail(text string) Model {
	m.abandon()
	m.seq++
	m.state = stateError
	m.errMsg = text
	m.retrying = false
	m.rows, m.cursor = nil, 0
	m.progCh = nil
	return m
}

// --- error wording ----------------------------------------------------------

// describeProbeError turns a failed extraction into a line the user can act on.
//
// The three sentinels are kept apart on purpose. A playlist, a live stream and
// a page with no formats are three different situations with three different
// next moves, and collapsing them into "could not fetch video" throws away the
// only part that was useful.
func describeProbeError(err error) string {
	switch {
	case errors.Is(err, ytdlp.ErrPlaylist):
		return "That link is a playlist or a channel, not a single video. Open the video you want and paste its own URL."
	case errors.Is(err, ytdlp.ErrLiveStream):
		return "That is a live stream. It has no end and no size, so there is nothing to download yet — try again once it has finished."
	case errors.Is(err, ytdlp.ErrNoFormatsExtracted):
		return "yt-dlp reached that page but found no video on it. It may be private, region-locked, or not a video page at all."
	}
	var ee *ytdlp.ExtractError
	if errors.As(err, &ee) {
		return ee.Message
	}
	return err.Error()
}

// describeDownloadError turns a failed download into a line, preferring
// yt-dlp's own words to the wrapped chain, which carries stdlib wording and the
// paths we interpolated.
func describeDownloadError(err error) string {
	if errors.Is(err, ytdlp.ErrNoDestination) {
		return "yt-dlp finished but never said where it saved the file, so yank cannot tell you where it went."
	}
	var ee *ytdlp.ExtractError
	if errors.As(err, &ee) {
		return ee.Message
	}
	return err.Error()
}

// --- the URL box ------------------------------------------------------------

// normaliseURL reports whether what was typed is plausibly a URL yt-dlp could
// take, and returns the form to hand it.
//
// The bar is deliberately low — "obviously not a URL" — because yt-dlp knows
// about a thousand sites and this function knows about none of them. It is here
// to catch an empty box and a search phrase, not to adjudicate hosts.
//
// A scheme-less "youtube.com/watch?v=…" is accepted and https:// is put in
// front of it, because that is what a person pasting from a URL bar ends up
// with and the alternative is a hint that reads as a bug.
func normaliseURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	// Anything with whitespace inside it is a phrase, not a URL. A control
	// character is a paste that went wrong.
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", false
	}

	if scheme := leadingScheme(s); scheme != "" {
		switch strings.ToLower(scheme) {
		case "http", "https":
		default:
			// file:// and friends are not something to hand an extractor.
			return "", false
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", false
		}
		return s, true
	}

	u, err := url.Parse("https://" + s)
	if err != nil || u.Host == "" {
		return "", false
	}
	// A bare word is a search, not a host. A dot is the cheapest evidence that
	// something was meant as one.
	if !strings.Contains(u.Hostname(), ".") {
		return "", false
	}
	return "https://" + s, true
}

// leadingScheme returns the URL scheme s begins with, or "" when it has none.
//
// It anchors on the front rather than searching for "://" anywhere, because a
// scheme-less URL can carry one in a query parameter — a consent or redirect
// link such as "consent.youtube.com/m?continue=https://www.youtube.com/watch"
// is exactly the sort of thing that gets pasted without its scheme. Reading the
// "://" in the middle as evidence of a scheme sent that input down the branch
// that demands http or https, found none, and told the user their URL was not
// one.
//
// url.Parse cannot answer this on its own either way round: it reports no
// scheme for the consent link above, and it reports "example.com" as the scheme
// of "example.com:8080/x", which is a host and a port.
func leadingScheme(s string) string {
	i := strings.Index(s, "://")
	if i <= 0 {
		return ""
	}
	for j := 0; j < i; j++ {
		c := s[j]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') && j > 0:
			// A scheme starts with a letter; these may only follow one.
		default:
			return ""
		}
	}
	return s[:i]
}

// urlHint is the inline complaint under the box: it says which of the two
// things went wrong, because "invalid URL" for an empty box is a lie.
func urlHint(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "paste a video URL first"
	}
	return "that does not look like a URL — paste the link to a video page"
}
