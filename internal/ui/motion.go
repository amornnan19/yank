package ui

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// Motion is the animation on three screens — the input screen, the download
// screen and the done screen: a list of independent effects per screen, one
// clock that runs only while one of them is moving, and the off switches that
// put today's static screens back.
//
// The clock is not the same on each. The input and done screens run no
// spinner, so the motion clock is theirs: frame ticks while an effect is
// busy, a single wake for the next idle step, nothing otherwise. The download
// screen already runs the spinner's tick and, while the bar springs,
// progress.FrameMsg, so it gets no third standing chain: its effects advance
// on the spinner's tick at 10 fps, and the motion clock runs there only while
// a short burst that needs more is live. The probing, picker and error screens
// have no motion at all.

// effectRegistry is every effect, per screen, in paint order: each one draws
// over what the ones before it on its screen drew.
//
// Removing an effect is deleting its motion_*.go file (and its _test.go) and
// its one line here. No other code names an effect's type, and no effect reads
// another's state — they meet only in the frame they paint.
//
// Which screen an effect belongs to is decided by the frame its paint method
// takes, not by the list it is written in; the lists say the same thing for a
// reader, and a test holds them to it.
var effectRegistry = [screenCount][]func() effect{
	screenInput: {
		newIntroDrop,         // motion_intro.go
		newFlinch,            // motion_flinch.go
		newShimmer,           // motion_shimmer.go
		newRevealSweep,       // motion_sweep.go
		newBreathingBorder,   // motion_border.go
		newTypingPlaceholder, // motion_placeholder.go
		newSiteBadge,         // motion_badge.go
		newYankOut,           // motion_yankout.go
		newStarfield,         // motion_stars.go
	},
	screenDownloading: {
		newBarShine,       // motion_shine.go
		newLeadingEdge,    // motion_edge.go
		newSpeedSparkline, // motion_spark.go
		newPercentPulse,   // motion_pulse.go
		newTitleMarquee,   // motion_marquee.go
		newPhaseSlide,     // motion_slide.go
		newFinishFlash,    // motion_flash.go
	},
	screenDone: {
		newCheckPop,     // motion_check.go
		newBorderTrace,  // motion_trace.go
		newConfetti,     // motion_confetti.go
		newAlreadyNudge, // motion_nudge.go
	},
}

// screen is a screen that has motion.
type screen int

const (
	screenInput screen = iota
	screenDownloading
	screenDone
	screenCount
)

// ridesSpinner reports whether the screen's effects advance on the spinner's
// tick rather than on wakes of their own. On such a screen an idle effect is
// never woken by the motion clock — the spinner is already ticking — and only
// busy effects, the short bursts, run frame ticks.
func (s screen) ridesSpinner() bool { return s == screenDownloading }

// effect is one motion. It is a value: step returns the effect as it is after
// ev, and nothing an effect holds may be shared with the value it was stepped
// from, because Model is copied freely.
//
// An effect also has a paint method for its screen's frame — see inputPainter,
// downloadPainter and donePainter — and that method is what places it.
type effect interface {
	// step brings the effect up to date with one event. Anything an effect
	// starts on an event that is not a tick is armed rather than started —
	// see cue — because only a tick knows what time it is.
	step(ev motionEvent) effect
	// busy reports that the effect is mid-animation, or armed and waiting to
	// learn the time: either way it wants the next frame tick. On a screen
	// that rides the spinner it means a burst that 10 fps is too slow for.
	busy() bool
	// wake is when an effect that is not busy next needs a step, or the zero
	// time for never. Ignored on a screen that rides the spinner.
	wake() time.Time
}

// inputPainter, downloadPainter and donePainter are an effect's paint method
// for each screen. paint draws the effect's current frame onto f. It reads only
// f and the effect's own state, and it is called from View, so it must be pure.
type inputPainter interface{ paint(f *inputFrame) }

type downloadPainter interface{ paint(f *downloadFrame) }

type donePainter interface{ paint(f *doneFrame) }

// screenOf is the screen an effect paints on.
func screenOf(e effect) (screen, bool) {
	switch e.(type) {
	case inputPainter:
		return screenInput, true
	case downloadPainter:
		return screenDownloading, true
	case donePainter:
		return screenDone, true
	}
	return 0, false
}

// screenHolder is an effect that keeps its screen on show after the model has
// moved on: the input screen's exit animation over the probing screen, the
// download screen's finish flash over the done screen. It never delays the
// work or the keys; the model is already on the next screen while it plays.
type screenHolder interface {
	holds() bool
}

// motionEventKind is what happened.
type motionEventKind int

const (
	// evTick is the clock: ev.now has advanced and ev.rng is set. On the
	// download screen the spinner's accepted tick is one too.
	evTick motionEventKind = iota
	// evShown is the screen coming into view with motion on: the first frame
	// it fits, a return from another screen, or a resize that brought it back.
	evShown
	// evLeft is the screen going out of view: another screen, an off switch,
	// or the end of the hold that kept it on show.
	evLeft
	// evEdit is the box's content changing.
	evEdit
	// evSubmit is enter accepting a URL.
	evSubmit
	// evLayout is the free space round the screen's block changing: a resize,
	// or a screen whose block is a different size. ev.freeCells is the new
	// count.
	evLayout
	// evReport is a progress report reaching the download screen.
	evReport
	// evUpdate is any other message the download screen saw: the retry
	// starting or ending, a key, a resize.
	evUpdate
	// evFinish is the download finishing successfully. The model is already
	// on the done screen.
	evFinish
)

// motionEvent is one thing an effect may react to.
type motionEvent struct {
	kind motionEventKind
	// now is the time of the latest tick, which is the only clock motion
	// has. It is zero before the first tick, and stale on anything but a
	// tick.
	now time.Time
	// value is the box's content.
	value string
	// rng is the model's seeded source. Set on ticks only: randomness is
	// drawn when a frame advances, never while one is painted.
	rng *rand.Rand
	// freeCells is how many cells round the screen's block the terminal
	// leaves empty for decoration, as the last layout measured it — see
	// freeArea; zero is none.
	freeCells int
	// dl is what the download screen is drawn from, and done what the done
	// screen is. Both are set on every event, whatever the screen.
	dl   downloadFacts
	done doneFacts
}

// downloadFacts is the part of the model the download screen's effects react
// to.
type downloadFacts struct {
	// title is the video's title as it came from the page: not sanitised.
	title   string
	prog    ytdlp.Progress
	hasProg bool
	line    underLine
}

// doneFacts is the part of the model the done screen's effects react to.
type doneFacts struct {
	// seq identifies the outcome on show, so an effect that has played for
	// it does not play again when a resize brings the screen back.
	seq     int
	already bool
}

// cue is the start of a timed animation. An effect triggered by a key press or
// a screen change cannot know what time it is — messages other than the tick
// carry no clock, and reading the wall clock in Update would make the frames
// untestable — so it arms the cue, reports busy, and the next tick fixes the
// start to its own time.
type cue struct {
	at      time.Time
	waiting bool
	running bool
}

// arm starts the cue waiting for the next tick.
func (c *cue) arm() { *c = cue{waiting: true} }

// stop clears the cue.
func (c *cue) stop() { *c = cue{} }

// settle fixes a waiting cue to the tick's time.
func (c *cue) settle(now time.Time) {
	if c.waiting {
		*c = cue{at: now, running: true}
	}
}

// live reports that the cue is armed or running.
func (c cue) live() bool { return c.waiting || c.running }

// elapsed is how long the cue has been running at now; zero while it waits.
func (c cue) elapsed(now time.Time) time.Duration {
	if !c.running || now.Before(c.at) {
		return 0
	}
	return now.Sub(c.at)
}

// motionFrame is the clock's period while something is moving: about 30 fps.
const motionFrame = time.Second / 30

// motionTickMsg is one tick of the motion clock. gen ties it to the chain that
// scheduled it; a tick whose gen is not the model's current one is from a chain
// that was replaced or stopped, and is dropped without rescheduling.
type motionTickMsg struct {
	gen int
	at  time.Time
}

// tickKind is what kind of tick is outstanding.
type tickKind int

const (
	tickNone tickKind = iota
	// tickFrame is the ~30 fps tick, only while an effect is busy.
	tickFrame
	// tickWake is a single tea.Tick for the next idle step that is due.
	tickWake
)

// motion is the model's animation state.
type motion struct {
	// enabled is false under an off switch, and in a model that was never
	// given motion — which is every model New builds; Run switches it on.
	enabled bool
	// halted is set once the program is ending, so nothing is scheduled
	// alongside tea.Quit.
	halted bool
	// active is whether the last Update left motion showing, and screen which
	// screen it was. A change in either is what turns into evShown and
	// evLeft, and what leaving stops.
	active bool
	screen screen

	effects [screenCount][]effect
	pcg     rand.PCG

	// gen is the generation of the one tick allowed to be outstanding;
	// pending is its kind, and due when it is expected to fire.
	gen     int
	pending tickKind
	due     time.Time
	// now is the time of the latest tick accepted.
	now time.Time
	// freeCells is what every event carries as ev.freeCells: the free cells
	// round the rendered screen's block when it was last measured.
	freeCells int
	// heldTitle is the title the download screen last showed. The finish
	// flash holds that screen over the done screen, by which time the probe
	// the title came from has been released.
	heldTitle string
}

// motionSwitchedOff reports whether the environment turns motion off.
// NO_COLOR counts when it is present at all; YANK_NO_MOTION when it is
// non-empty.
func motionSwitchedOff(lookupEnv func(string) (string, bool)) bool {
	if _, ok := lookupEnv("NO_COLOR"); ok {
		return true
	}
	v, _ := lookupEnv("YANK_NO_MOTION")
	return v != ""
}

// newMotion builds the animation state from the registry, or a disabled one
// under an off switch. seed is the only source of randomness the effects get.
func newMotion(lookupEnv func(string) (string, bool), seed uint64) motion {
	if motionSwitchedOff(lookupEnv) {
		return motion{}
	}
	var effects []effect
	for _, builds := range effectRegistry {
		for _, build := range builds {
			effects = append(effects, build())
		}
	}
	return newMotionOf(effects, seed)
}

// newMotionOf is newMotion with the effects given, for tests that watch a few
// effects on their own. Each goes to the screen its paint method is for, in
// the order given.
func newMotionOf(effects []effect, seed uint64) motion {
	mo := motion{
		enabled: true,
		pcg:     *rand.NewPCG(seed, seed^0x9e3779b97f4a7c15),
	}
	for _, e := range effects {
		s, ok := screenOf(e)
		if !ok {
			panic(fmt.Sprintf("ui: effect %T paints no screen's frame", e))
		}
		mo.effects[s] = append(mo.effects[s], e)
	}
	return mo
}

// broadcast steps every effect on screen s through ev. The slice is rebuilt
// rather than written in place: the Model this came from shares its backing
// array.
func (mo *motion) broadcast(s screen, ev motionEvent) {
	ev.now, ev.freeCells = mo.now, mo.freeCells
	if ev.kind == evTick {
		ev.rng = rand.New(&mo.pcg)
	}
	next := make([]effect, len(mo.effects[s]))
	for i, e := range mo.effects[s] {
		next[i] = e.step(ev)
	}
	mo.effects[s] = next
}

// holding reports whether an effect is keeping screen s on show.
func (mo motion) holding(s screen) bool {
	for _, e := range mo.effects[s] {
		if h, ok := e.(screenHolder); ok && h.holds() {
			return true
		}
	}
	return false
}

// stop invalidates the outstanding tick, if any. The timer still fires; its
// gen no longer matches and the message is dropped.
func (mo *motion) stop() {
	if mo.pending != tickNone {
		mo.gen++
		mo.pending = tickNone
	}
}

// motionScreen is the animated screen View draws, if any. Every off switch
// lands here: the environment (enabled), the program ending, a screen the
// terminal is too small for, and every screen without motion — except the
// probing screen while the input screen's exit still plays over it, and the
// done screen while the download screen's finish flash does.
func (m Model) motionScreen() (screen, bool) {
	mo := m.motion
	if !mo.enabled || mo.halted || m.quitting {
		return 0, false
	}
	switch m.state {
	case stateInput:
		return screenInput, m.wordmarkFits()
	case stateProbing:
		return screenInput, mo.holding(screenInput) && m.wordmarkFits()
	case stateDownloading:
		return screenDownloading, m.downloadFits()
	case stateDone:
		if mo.holding(screenDownloading) && m.downloadFits() {
			return screenDownloading, true
		}
		return screenDone, m.doneFits()
	}
	return 0, false
}

// motionShowing reports whether View is drawing an animated screen.
func (m Model) motionShowing() bool {
	_, ok := m.motionScreen()
	return ok
}

// screenFits reports whether the static screen, header included, fits the
// terminal: a known height with room for every row, and a width that is not
// already overflowing. Motion is decoration, and a terminal too small for
// today's screen gets today's screen exactly.
func (m Model) screenFits(static string) bool {
	return m.height > 0 && m.width-4 >= minContentWidth && m.spareRows(static) >= 0
}

// downloadFits and doneFits are screenFits for the two screens.
func (m Model) downloadFits() bool { return m.screenFits(m.header() + m.downloadingView()) }

func (m Model) doneFits() bool { return m.screenFits(m.header() + m.doneView()) }

// motionEvent is an event of kind carrying the model's facts.
func (m Model) motionEvent(kind motionEventKind) motionEvent {
	return motionEvent{kind: kind, value: m.input.Value(), dl: m.downloadFacts(), done: m.doneFacts()}
}

// downloadFacts is what the download screen is drawn from. Past the end of the
// download — the finish flash holding the screen — the title is the one it
// last showed.
func (m Model) downloadFacts() downloadFacts {
	title := m.motion.heldTitle
	if m.state == stateDownloading {
		title = m.videoTitle()
	}
	return downloadFacts{title: title, prog: m.prog, hasProg: m.hasProg, line: m.underLine()}
}

func (m Model) doneFacts() doneFacts {
	return doneFacts{seq: m.seq, already: m.result != nil && m.result.AlreadyExisted}
}

// handleMotionTick is one tick of the clock.
func (m Model) handleMotionTick(msg motionTickMsg) (tea.Model, tea.Cmd) {
	mo := &m.motion
	if msg.gen != mo.gen || mo.pending == tickNone {
		// A chain that was replaced or stopped. Nothing is rescheduled, so
		// it ends here.
		return m, nil
	}
	mo.pending = tickNone
	if msg.at.After(mo.now) {
		mo.now = msg.at
	}
	if !m.follow(false) {
		return m, nil
	}
	was := mo.screen
	mo.broadcast(was, m.motionEvent(evTick))
	// This frame may have ended a hold: the exit over the probing screen, or
	// the flash over the done screen.
	if !m.follow(false) {
		return m, nil
	}
	if mo.screen != was {
		m.measureFreeCells()
	}
	cmd := m.scheduleMotion(true)
	return m, cmd
}

// follow brings motion's idea of the screen on show in line with the model's:
// evLeft to the screen that went, evShown to the one that came — or to the
// same one again when reshow is set. It reports whether any animated screen is
// on show; when none is, motion is inactive, and the caller stops the clock.
func (m *Model) follow(reshow bool) bool {
	mo := &m.motion
	s, ok := m.motionScreen()
	if mo.active && (!ok || s != mo.screen) {
		mo.broadcast(mo.screen, m.motionEvent(evLeft))
		mo.active = false
	}
	if !ok {
		return false
	}
	if !mo.active || reshow {
		mo.active, mo.screen = true, s
		mo.broadcast(s, m.motionEvent(evShown))
	}
	return true
}

// measureFreeCells re-measures the free cells round the block of the screen on
// show, and tells the screen's effects when that changed. It is measured after
// the events just sent, from the block View would draw now; the rows a screen
// reserves keep a hint or a badge line from changing it, and nothing a tick
// does on any screen changes it.
func (m *Model) measureFreeCells() {
	mo := &m.motion
	cells := m.freeArea(m.placement(m.screenBlockSize())).cells()
	if cells != mo.freeCells {
		mo.freeCells = cells
		mo.broadcast(mo.screen, m.motionEvent(evLayout))
	}
}

// screenBlockSize is screenBlock without the marks, for measuring.
func (m Model) screenBlockSize() (string, int) {
	block, reserve, _ := m.screenBlock()
	return block, reserve
}

// syncMotion runs after every message but a motion tick. It turns what the
// message changed into events — enter accepted, the download finished, the
// box edited, a report or a spinner tick on the download screen, a screen
// shown or left, the rows under the legend changed — and then makes the
// outstanding tick match what the effects now want, or stops it when motion
// is no longer showing.
//
// prev is the model before the message. Only the enter key plays the exit:
// the command line's URL takes the same input → probing transition through
// submit, and launching with one goes straight to the probing screen.
func (m *Model) syncMotion(prev Model, msg tea.Msg) tea.Cmd {
	mo := &m.motion
	if !mo.enabled {
		return nil
	}
	if prev.state == stateDownloading {
		mo.heldTitle = prev.videoTitle()
	}

	key, isKey := msg.(tea.KeyMsg)
	entered := isKey && key.Type == tea.KeyEnter
	if mo.active {
		switch {
		case mo.screen == screenInput && entered && prev.state == stateInput && m.state == stateProbing:
			mo.broadcast(screenInput, m.motionEvent(evSubmit))
		case mo.screen == screenDownloading && prev.state == stateDownloading && m.state == stateDone:
			mo.broadcast(screenDownloading, m.motionEvent(evFinish))
		}
	}

	wasActive, was := mo.active, mo.screen
	returned := prev.state != stateInput && m.state == stateInput
	if !m.follow(returned) {
		mo.stop()
		return nil
	}
	if wasActive && was == mo.screen && !returned {
		switch mo.screen {
		case screenInput:
			if m.input.Value() != prev.input.Value() {
				mo.broadcast(screenInput, m.motionEvent(evEdit))
			}
		case screenDownloading:
			tick, isTick := msg.(spinner.TickMsg)
			report, isReport := msg.(progressMsg)
			switch {
			case isTick && m.frame != prev.frame:
				// The spinner's accepted tick is this screen's clock.
				if tick.Time.After(mo.now) {
					mo.now = tick.Time
				}
				mo.broadcast(screenDownloading, m.motionEvent(evTick))
			case isReport && report.seq == prev.seq && prev.state == stateDownloading && m.state == stateDownloading:
				mo.broadcast(screenDownloading, m.motionEvent(evReport))
			default:
				mo.broadcast(screenDownloading, m.motionEvent(evUpdate))
			}
		}
	}
	m.measureFreeCells()
	return m.scheduleMotion(false)
}

// scheduleMotion keeps exactly one tick outstanding for what the effects on the
// screen want: a frame tick while any is busy, one tea.Tick for the earliest
// wake while none is, and nothing at all when none wants either. A screen that
// rides the spinner has no wakes: its idle clock is the spinner.
//
// atTick is whether this runs on a tick, the only moment the clock is current.
// Off a tick, an idle wake cannot be timed — the clock is as old as the last
// tick — so a wake that has to be scheduled then is replaced by one frame tick,
// which learns the time and schedules the wake from it.
//
// Every new tick bumps gen, so whatever was outstanding before is dead: there
// is never a second chain, only a timer that fires into a mismatch.
func (m *Model) scheduleMotion(atTick bool) tea.Cmd {
	mo := &m.motion
	want := tickNone
	var due time.Time
	for _, e := range mo.effects[mo.screen] {
		if e.busy() {
			want = tickFrame
			break
		}
		if mo.screen.ridesSpinner() {
			continue
		}
		if w := e.wake(); !w.IsZero() && (due.IsZero() || w.Before(due)) {
			due = w
		}
	}
	if want == tickNone && !due.IsZero() {
		want = tickWake
	}

	if want == tickNone {
		mo.stop()
		return nil
	}
	if !atTick {
		switch {
		case mo.pending == tickFrame:
			// Lands within a frame and reschedules from a real clock.
			return nil
		case mo.pending == tickWake && want == tickWake && !due.Before(mo.due):
			// Fires no later than it is needed.
			return nil
		}
		want = tickFrame
	}

	mo.gen++
	mo.pending = want
	gen := mo.gen
	wait := motionFrame
	if want == tickWake {
		wait = max(due.Sub(mo.now), motionFrame)
	}
	mo.due = mo.now.Add(wait)
	return tea.Tick(wait, func(at time.Time) tea.Msg { return motionTickMsg{gen: gen, at: at} })
}

// --- painting ---------------------------------------------------------------

// ink is how one cell of a wordmark or a bar is styled. Cells are grouped into
// runs of the same ink when rendered, so a frame costs a few escape sequences a
// row rather than one per cell.
type ink int

const (
	// inkApp is the wordmark's own style.
	inkApp ink = iota
	// inkBright is the highlight: bold bright white. The bar's shine uses it
	// too.
	inkBright
	// inkPlain is no style at all: the bar's empty cells, which are left in
	// the terminal's foreground for the reason newBar gives.
	inkPlain
	// inkFill is the bar's filled cells, in the bar's own blue.
	inkFill
	// inkGlint is one step brighter than the fill: bright blue.
	inkGlint
	// inkFlash and inkFlashDim are the finish flash: bright green, then the
	// success green.
	inkFlash
	inkFlashDim
)

// cell is one wordmark or bar cell.
type cell struct {
	r   rune
	ink ink
}

// starMark is one star, in raw coordinates: View maps row and col onto the
// free cells round the block, which only it knows.
type starMark struct {
	row, col int
	glyph    rune
}

// inputFrame is one frame of the animated input screen, before it is drawn.
// It starts as the static screen and each effect paints over it.
type inputFrame struct {
	// now is the motion clock.
	now time.Time
	// wordmark is the drawing, one row of cells per row, all the same width
	// and never wider than the content.
	wordmark [][]cell
	// border is the ANSI index the URL box's border is drawn in.
	border string
	// placeholder replaces the box's placeholder when placeholderSet.
	placeholder    string
	placeholderSet bool
	// boxText replaces the box's contents when boxTextSet. It must already be
	// plain and cut to boxWidth.
	boxText    string
	boxTextSet bool
	boxWidth   int
	// badge is the plain line under the box, in ANSI badgeColour, faint while
	// badgeFaint.
	badge       string
	badgeColour string
	badgeFaint  bool
	// stars are the starfield's marks.
	stars []starMark
}

// wordmarkSlack is the room the wordmark canvas keeps beside the drawing for a
// letter moved sideways, where the content width allows it.
const wordmarkSlack = 2

// frame paints every effect onto the static input screen at content width cw.
func (mo motion) frame(cw int) inputFrame {
	width := min(cw, wordmarkWidth()+wordmarkSlack)
	canvas := make([][]cell, len(wordmarkRows))
	for i, row := range wordmarkRows {
		canvas[i] = make([]cell, width)
		rs := []rune(row)
		for col := range width {
			canvas[i][col] = cell{r: ' '}
			if col < len(rs) {
				canvas[i][col].r = rs[col]
			}
		}
	}
	f := inputFrame{
		now:      mo.now,
		wordmark: canvas,
		border:   ansiCyan,
		boxWidth: max(1, cw-boxOverhead),
	}
	for _, e := range mo.effects[screenInput] {
		e.(inputPainter).paint(&f)
	}
	return f
}

// inkStyle is the lipgloss style for an ink.
func inkStyle(k ink) lipgloss.Style {
	switch k {
	case inkBright:
		return styles().bright
	case inkPlain:
		return lipgloss.NewStyle()
	case inkFill:
		return styles().barFill
	case inkGlint:
		return styles().barGlint
	case inkFlash:
		return styles().barFlash
	case inkFlashDim:
		return styles().barFlashDim
	}
	return styles().app
}

// cellsView renders one row of cells. Every rune in a row is one cell wide, so
// the row is exactly as wide as the slice, and each run is styled whole after
// it is built: nothing styled is cut. Plain runs are written bare.
func cellsView(row []cell) string {
	var b strings.Builder
	for start := 0; start < len(row); {
		end := start
		var run strings.Builder
		for end < len(row) && row[end].ink == row[start].ink {
			run.WriteRune(row[end].r)
			end++
		}
		if row[start].ink == inkPlain {
			b.WriteString(run.String())
		} else {
			b.WriteString(inkStyle(row[start].ink).Render(run.String()))
		}
		start = end
	}
	return b.String()
}

// wordmarkView renders the painted drawing.
func (f inputFrame) wordmarkView() string {
	lines := make([]string, len(f.wordmark))
	for i, row := range f.wordmark {
		lines[i] = cellsView(row)
	}
	return join(lines...)
}

// animatedScreen is the animated input screen down to the legend, without the
// starfield. The badge is decoration: it is drawn only when the block with it
// is centred by placement's own rule — the row under the box is part of the
// reserve inputRows centres on, so the badge takes a row that is already kept
// for it and pushes nothing off the bottom. The wordmark needs a terminal
// taller than that block, so while motion shows this screen the badge always
// has its row, and its fade never ticks with nothing on screen to fade.
func (m Model) animatedScreen(f inputFrame) string {
	cw := m.contentWidth()
	in := m.input
	if f.placeholderSet {
		in.Placeholder = f.placeholder
	}
	text := in.View()
	if f.boxTextSet {
		text = f.boxText
	}
	box := styles().box.BorderForeground(lipgloss.Color(f.border)).Width(cw - 2).Render(text)

	lines := []string{box}
	under := func() string { return f.wordmarkView() + "\n\n" + join(lines...) + "\n\n" + m.inputLegend() }
	switch {
	case m.hint != "":
		lines = append(lines, fit(styles().faint, m.hint, cw))
	case f.badge != "":
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(f.badgeColour)).Faint(f.badgeFaint)
		lines = append(lines, fit(style, f.badge, cw))
		if !m.placement(under(), m.inputRows()).centred {
			lines = lines[:len(lines)-1]
		}
	}
	return under()
}

// spareRows is how many rows the terminal has below screen: its height less
// the screen's rows and the doc style's padding row above and below. It is
// negative when screen does not fit.
func (m Model) spareRows(screen string) int {
	return m.height - (strings.Count(screen, "\n") + 1) - 2
}

// starMarks is the starfield drawn into the free space round the block: each
// star's raw row and column picks one free cell, so every star is on a cell no
// content holds and the stars spread over all the room there is — above the
// block, below it, and beside it on a terminal wider than the content. With no
// free cell there is no star.
func starMarks(stars []starMark, a freeArea) []mark {
	n := a.cells()
	if n < 1 {
		return nil
	}
	out := make([]mark, 0, len(stars))
	for _, s := range stars {
		r, c := a.cell(mod(s.row<<16|s.col&0xffff, n))
		out = append(out, mark{row: r, col: c, glyph: styles().faint.Render(string(s.glyph))})
	}
	return out
}

// mod is n mod d, never negative.
func mod(n, d int) int {
	if d <= 0 {
		return 0
	}
	return ((n % d) + d) % d
}

// --- the download screen ----------------------------------------------------

// downloadFrame is one frame of the animated download screen, before it is
// drawn. It starts as the static screen and each effect paints over it.
type downloadFrame struct {
	// now is the motion clock; cw the content width.
	now time.Time
	cw  int
	// title is the video's title, sanitised. titleText replaces its line
	// when titleSet; it must already be plain and at most cw cells.
	title     string
	titleText string
	titleSet  bool
	// bar is the progress bar's cells while it has a percentage to show, and
	// filled how many of them, from the left, are the fill. It is nil while
	// sweep is set: a stretch with nothing to count, where the indeterminate
	// sweep is drawn instead.
	bar    []cell
	filled int
	sweep  bool
	// label is the text beside the bar, bold bright while labelBright.
	label       string
	labelBright bool
	// line is which line is drawn under the bar, and lineShift how many cells
	// right of its place it is drawn: the rest of it is cut off at the edge.
	line      underLine
	lineShift int
	// stats is the plain stats line, and spark a plain graph to follow it
	// when the line has room.
	stats string
	spark string
}

// barCells is the static bar as cells: filled cells of the fill, then empty
// ones, width in all.
func barCells(width, filled int) []cell {
	cells := make([]cell, max(width, 0))
	for i := range cells {
		cells[i] = cell{r: '░', ink: inkPlain}
		if i < filled {
			cells[i] = cell{r: '█', ink: inkFill}
		}
	}
	return cells
}

// downloadFrame is the static download screen as a frame, painted by every
// effect on the screen.
func (m Model) downloadFrame() downloadFrame {
	facts := m.downloadFacts()
	cw := m.contentWidth()
	pct, known := m.prog.Percent()
	f := downloadFrame{
		cw:    cw,
		title: sanitise(facts.title),
		label: percentLabel(pct, known),
		line:  facts.line,
		stats: m.statsLine(),
	}
	// The same arithmetic as barLine: the label and its separator come off
	// the bar's width.
	width := max(1, cw-lipgloss.Width(f.label)-2)
	switch {
	case m.indeterminate():
		f.sweep = true
	case known && pct >= 100:
		f.filled = width
	case known:
		// Where the spring has got to. progress does not export it, so it
		// is read off the bar's own rendering: every filled cell is a Full
		// rune, and no escape sequence contains one.
		bar := m.bar
		bar.Width = width
		f.filled = min(width, strings.Count(bar.View(), string(bar.Full)))
	}
	if !f.sweep {
		f.bar = barCells(width, f.filled)
	}
	return m.motion.paintDownload(f)
}

// paintDownload paints every effect on the download screen onto f.
func (mo motion) paintDownload(f downloadFrame) downloadFrame {
	f.now = mo.now
	// The effects write the cells in place; the caller's slice is not theirs.
	if f.bar != nil {
		f.bar = append([]cell(nil), f.bar...)
	}
	for _, e := range mo.effects[screenDownloading] {
		e.(downloadPainter).paint(&f)
	}
	return f
}

// animatedDownloadingView is the download screen with motion: the title or
// its marquee, the row's label, the painted bar and label, and the line under
// the bar in its place or sliding into it. Under any off switch
// downloadingView draws the screen instead, exactly as it was before motion
// existed.
func (m Model) animatedDownloadingView() string {
	f := m.downloadFrame()
	cw := m.contentWidth()
	title := fit(styles().title, f.title, cw)
	if f.titleSet {
		title = styles().title.Render(truncate(f.titleText, cw))
	}
	lines := []string{
		title,
		fit(styles().faint, m.row.Label, cw),
		"",
		m.animatedBarLine(f),
		m.animatedUnderLine(f),
	}
	return join(lines...) + "\n\n" + m.help("esc  cancel", "ctrl+c  quit")
}

// animatedBarLine is barLine drawn from the frame.
func (m Model) animatedBarLine(f downloadFrame) string {
	width := max(1, f.cw-lipgloss.Width(f.label)-2)
	drawn := cellsView(f.bar)
	if f.sweep {
		drawn = sweepBar(width, m.frame)
	}
	label := f.label
	if f.labelBright {
		label = styles().bright.Render(label)
	}
	return drawn + "  " + label
}

// animatedUnderLine is the line under the bar drawn from the frame, pushed
// lineShift cells right and cut at the content's edge. The cut is made on the
// plain text, before any of it is styled.
func (m Model) animatedUnderLine(f downloadFrame) string {
	shift := min(max(f.lineShift, 0), f.cw)
	w := f.cw - shift
	pad := strings.Repeat(" ", shift)
	if text := f.line.text(); text != "" {
		if w <= lipgloss.Width(m.spin.Spinner.Frames[0]) {
			return ""
		}
		return pad + m.spinLineAt(styles().phase, text, w)
	}
	if w <= 0 {
		return ""
	}
	stats := sanitise(f.stats)
	if f.spark != "" && lipgloss.Width(stats)+2+lipgloss.Width(f.spark) <= w {
		return pad + styles().faint.Render(stats+"  "+f.spark)
	}
	return pad + fit(styles().faint, stats, w)
}

// --- the done screen --------------------------------------------------------

// confettiMark is one piece of confetti: y is how far down the terminal it is,
// as a fraction of its height, and col a raw column. View maps both onto the
// cells the screen actually leaves free, which only it knows, and draws nothing
// for a piece off the terminal or behind the block.
type confettiMark struct {
	y      float64
	col    int
	glyph  rune
	colour string
}

// doneFrame is one frame of the animated done screen, before it is drawn.
type doneFrame struct {
	now time.Time
	// glyph and heading make up the title: the glyph, and the text after it
	// as far as it has been typed.
	glyph   string
	heading string
	// trace is how much of the box around the path is drawn, from 0 to 1,
	// clockwise from its top-left corner.
	trace float64
	// noteFaint is whether the already-there note is faint, as it is when
	// nothing is moving it.
	noteFaint bool
	confetti  []confettiMark
}

// doneFrame is the static done screen as a frame, painted by every effect on
// the screen.
func (m Model) doneFrame() doneFrame {
	title := []rune(doneTitle(m.doneFacts().already))
	return m.motion.paintDone(doneFrame{
		glyph:     string(title[0]),
		heading:   string(title[1:]),
		trace:     1,
		noteFaint: true,
	})
}

// paintDone paints every effect on the done screen onto f.
func (mo motion) paintDone(f doneFrame) doneFrame {
	f.now = mo.now
	f.confetti = append([]confettiMark(nil), f.confetti...)
	for _, e := range mo.effects[screenDone] {
		e.(donePainter).paint(&f)
	}
	return f
}

// animatedDoneScreen is doneView drawn from the frame, down to the legend.
func (m Model) animatedDoneScreen(f doneFrame) string {
	cw := m.contentWidth()
	lines := []string{fit(styles().success, f.glyph+f.heading, cw), ""}
	if m.result != nil {
		lines = append(lines, m.tracedPathView(m.result.Path, f.trace))
		if m.result.AlreadyExisted {
			note := wrap(alreadyNote, cw)
			if f.noteFaint {
				note = styles().faint.Render(note)
			}
			lines = append(lines, "", note)
		}
		if m.result.UsedWorkingDir {
			note := workingDirNote
			if m.result.AlreadyExisted {
				note = alreadyWorkingDirNote
			}
			lines = append(lines, "", styles().faint.Render(wrap(note, cw)))
		}
	}
	return join(lines...) + "\n\n" + m.help("enter  another", "q  quit")
}

// tracedPathView is pathView with only trace of its border drawn. Whole, or on
// a terminal too narrow for a frame, it is pathView itself.
func (m Model) tracedPathView(path string, trace float64) string {
	cw := m.contentWidth()
	if cw < minBoxedWidth || trace >= 1 {
		return m.pathView(path)
	}
	cut := truncate(path, m.pathWidth()-boxOverhead)
	inner := max(cw-boxOverhead, lipgloss.Width(cut))
	return traceBox(cut, inner, int(max(trace, 0)*float64(boxPerimeter(inner))))
}

// boxPerimeter is how many border cells a box with inner cells of text has:
// the top and bottom rows, and one side cell each for its one text row.
func boxPerimeter(inner int) int { return 2*(inner+boxOverhead) + 2 }

// traceBox draws the saved path's box as pathView does — the same corners,
// the same padding, the path cut before it arrives — with only the first cells
// of its border, counted clockwise from the top-left corner, drawn. The rest
// of the border is blank, so the box is as wide and as tall on every frame.
func traceBox(cut string, inner, cells int) string {
	w := inner + boxOverhead
	b := lipgloss.RoundedBorder()
	drawn := func(i int) bool { return i < cells }
	edge := func(n int, from func(int) int, left, mid, right string) string {
		var s strings.Builder
		for col := range n {
			r := mid
			switch col {
			case 0:
				r = left
			case n - 1:
				r = right
			}
			if drawn(from(col)) {
				s.WriteString(styles().savedBorder.Render(r))
			} else {
				s.WriteString(" ")
			}
		}
		return s.String()
	}
	side := func(i int) string {
		if drawn(i) {
			return styles().savedBorder.Render(b.Left)
		}
		return " "
	}
	top := edge(w, func(col int) int { return col }, b.TopLeft, b.Top, b.TopRight)
	middle := side(2*w+1) + " " + styles().path.Render(cut) +
		strings.Repeat(" ", inner-lipgloss.Width(cut)) + " " + side(w)
	bottom := edge(w, func(col int) int { return w + 1 + (w - 1 - col) }, b.BottomLeft, b.Bottom, b.BottomRight)
	return join(top, middle, bottom)
}

// confettiMarks is the confetti drawn into the free space round the block. A
// piece's y is a fraction of the terminal's height and its column is taken
// modulo the width, so the burst falls down the whole terminal; a piece above
// or below it, or on a cell the rectangle round the block keeps clear, is not
// drawn — it passes behind the screen rather than over it.
func confettiMarks(pieces []confettiMark, a freeArea) []mark {
	var out []mark
	for _, p := range pieces {
		r := int(math.Floor(p.y * float64(a.height)))
		c := mod(p.col, a.width)
		if !a.free(r, c) {
			continue
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(p.colour))
		out = append(out, mark{row: r, col: c, glyph: style.Render(string(p.glyph))})
	}
	return out
}
