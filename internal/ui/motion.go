package ui

import (
	"math/rand/v2"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Motion is the input screen's animation: a list of independent effects, one
// clock that runs only while one of them is moving, and the off switches that
// put today's static screen back.
//
// The input screen is the only one with no spinner, so it is the only one that
// may run a clock of its own. Everywhere else the spinner's tick is the clock,
// and nothing here schedules anything.

// effectRegistry is every effect on the input screen, in paint order: each one
// draws over what the ones before it drew.
//
// Removing an effect is deleting its motion_*.go file (and its _test.go) and
// its one line here. No other code names an effect's type, and no effect reads
// another's state — they meet only in the inputFrame they paint.
var effectRegistry = []func() effect{
	newIntroDrop,         // motion_intro.go
	newFlinch,            // motion_flinch.go
	newShimmer,           // motion_shimmer.go
	newRevealSweep,       // motion_sweep.go
	newBreathingBorder,   // motion_border.go
	newTypingPlaceholder, // motion_placeholder.go
	newSiteBadge,         // motion_badge.go
	newYankOut,           // motion_yankout.go
	newStarfield,         // motion_stars.go
}

// effect is one motion on the input screen. It is a value: step returns the
// effect as it is after ev, and nothing an effect holds may be shared with the
// value it was stepped from, because Model is copied freely.
type effect interface {
	// step brings the effect up to date with one event. Anything an effect
	// starts on an event that is not a tick is armed rather than started —
	// see cue — because only a tick knows what time it is.
	step(ev motionEvent) effect
	// busy reports that the effect is mid-animation, or armed and waiting to
	// learn the time: either way it wants the next frame tick.
	busy() bool
	// wake is when an effect that is not busy next needs a step, or the zero
	// time for never.
	wake() time.Time
	// paint draws the effect's current frame onto f. It reads only f and the
	// effect's own state, and it is called from View, so it must be pure.
	paint(f *inputFrame)
}

// screenHolder is an effect that keeps the input screen on show after enter
// has moved the model to the probing screen: the exit animation. It never
// delays the work; the probe is already running while it plays.
type screenHolder interface {
	holds() bool
}

// motionEventKind is what happened.
type motionEventKind int

const (
	// evTick is the clock: ev.now has advanced and ev.rng is set.
	evTick motionEventKind = iota
	// evShown is the input screen coming into view with motion on: the
	// first frame the wordmark fits, a return from another screen, or a
	// resize that brought the wordmark back.
	evShown
	// evEdit is the box's content changing.
	evEdit
	// evSubmit is enter accepting a URL.
	evSubmit
	// evLayout is the rows left for the starfield changing: a resize, or a
	// hint or badge line coming or going. ev.starRows is the new count.
	evLayout
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
	// starRows is how many rows below the legend the screen leaves for the
	// starfield, as the last layout measured it; zero or less is none.
	starRows int
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
	// active is whether the last Update left motion showing. It is what turns
	// a change into evShown, and what leaving the screen stops.
	active bool

	effects []effect
	pcg     rand.PCG

	// gen is the generation of the one tick allowed to be outstanding;
	// pending is its kind, and due when it is expected to fire.
	gen     int
	pending tickKind
	due     time.Time
	// now is the time of the latest tick accepted.
	now time.Time
	// starRows is what every event carries as ev.starRows: the rows the
	// rendered screen left for the starfield when it was last measured.
	starRows int
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
	effects := make([]effect, len(effectRegistry))
	for i, build := range effectRegistry {
		effects[i] = build()
	}
	return newMotionOf(effects, seed)
}

// newMotionOf is newMotion with the effects given, for tests that watch one
// effect on its own.
func newMotionOf(effects []effect, seed uint64) motion {
	return motion{
		enabled: true,
		effects: effects,
		pcg:     *rand.NewPCG(seed, seed^0x9e3779b97f4a7c15),
	}
}

// broadcast steps every effect through ev. The slice is rebuilt rather than
// written in place: the Model this came from shares its backing array.
func (mo *motion) broadcast(ev motionEvent, value string) {
	ev.now, ev.value, ev.starRows = mo.now, value, mo.starRows
	if ev.kind == evTick {
		ev.rng = rand.New(&mo.pcg)
	}
	next := make([]effect, len(mo.effects))
	for i, e := range mo.effects {
		next[i] = e.step(ev)
	}
	mo.effects = next
}

// holding reports whether an effect is keeping the input screen on show.
func (mo motion) holding() bool {
	for _, e := range mo.effects {
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

// motionShowing reports whether the animated input screen is what View draws.
// Every off switch lands here: the environment (enabled), the wordmark not
// fitting, and every screen but the input one — except the probing screen
// while the exit animation is still playing over it.
func (m Model) motionShowing() bool {
	mo := m.motion
	if !mo.enabled || mo.halted || m.quitting || !m.wordmarkFits() {
		return false
	}
	switch m.state {
	case stateInput:
		return true
	case stateProbing:
		return mo.holding()
	}
	return false
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
	if !m.motionShowing() {
		mo.active = false
		return m, nil
	}
	mo.broadcast(motionEvent{kind: evTick}, m.input.Value())
	if !m.motionShowing() {
		// This frame ended the exit animation, and with it the only thing
		// keeping motion on the probing screen.
		mo.active = false
		return m, nil
	}
	cmd := m.scheduleMotion(true)
	return m, cmd
}

// syncMotion runs after every message but a tick. It turns what the message
// changed into events — enter accepted, the box edited, the screen shown, the
// rows under the legend changed — and then makes the outstanding tick match
// what the effects now want, or stops it when motion is no longer showing.
//
// entered is whether the message was the enter key. Only that plays the exit:
// the command line's URL takes the same input → probing transition through
// submit, and launching with one goes straight to the probing screen.
func (m *Model) syncMotion(prevState state, prevValue string, entered bool) tea.Cmd {
	mo := &m.motion
	if !mo.enabled {
		return nil
	}
	value := m.input.Value()

	if entered && mo.active && prevState == stateInput && m.state == stateProbing {
		mo.broadcast(motionEvent{kind: evSubmit}, value)
	}

	if !m.motionShowing() {
		mo.active = false
		mo.stop()
		return nil
	}

	returned := prevState != stateInput && m.state == stateInput
	switch {
	case !mo.active || returned:
		mo.active = true
		mo.broadcast(motionEvent{kind: evShown}, value)
	case value != prevValue:
		mo.broadcast(motionEvent{kind: evEdit}, value)
	}
	// Measured after the events above, which are what bring the badge line.
	// Nothing a tick does changes the row count, so this is the only place
	// it can go stale.
	if rows := m.starRowCount(m.animatedScreen(mo.frame(m.contentWidth()))); rows != mo.starRows {
		mo.starRows = rows
		mo.broadcast(motionEvent{kind: evLayout}, value)
	}
	return m.scheduleMotion(false)
}

// scheduleMotion keeps exactly one tick outstanding for what the effects want:
// a frame tick while any is busy, one tea.Tick for the earliest wake while none
// is, and nothing at all when none wants either.
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
	for _, e := range mo.effects {
		if e.busy() {
			want = tickFrame
			break
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

// ink is how one wordmark cell is styled. Cells are grouped into runs of the
// same ink when rendered, so a frame costs a few escape sequences a row rather
// than one per cell.
type ink int

const (
	// inkApp is the wordmark's own style.
	inkApp ink = iota
	// inkBright is the highlight: bold bright white.
	inkBright
)

// cell is one wordmark cell.
type cell struct {
	r   rune
	ink ink
}

// starMark is one star, in raw coordinates: View maps row and col onto the
// empty rows the frame actually has, which only it knows.
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
	for _, e := range mo.effects {
		e.paint(&f)
	}
	return f
}

// inkStyle is the lipgloss style for an ink.
func inkStyle(k ink) lipgloss.Style {
	if k == inkBright {
		return styles().bright
	}
	return styles().app
}

// wordmarkView renders the painted drawing. Every rune on the canvas is one
// cell wide, so the rows are exactly as wide as the canvas, and each run is
// styled whole after it is built: nothing styled is cut.
func (f inputFrame) wordmarkView() string {
	lines := make([]string, len(f.wordmark))
	for i, row := range f.wordmark {
		var b strings.Builder
		for start := 0; start < len(row); {
			end := start
			var run strings.Builder
			for end < len(row) && row[end].ink == row[start].ink {
				run.WriteRune(row[end].r)
				end++
			}
			b.WriteString(inkStyle(row[start].ink).Render(run.String()))
			start = end
		}
		lines[i] = b.String()
	}
	return join(lines...)
}

// animatedInputView is the input screen with motion: the painted wordmark, the
// box in the frame's border colour with the frame's placeholder or text, the
// hint or the site badge under it, the legend, and the starfield in whatever
// rows are left below. Under any off switch inputView draws the screen instead,
// exactly as it was before motion existed.
func (m Model) animatedInputView() string {
	f := m.motion.frame(m.contentWidth())
	screen := m.animatedScreen(f)
	return screen + m.starRows(f.stars, screen)
}

// animatedScreen is the animated input screen down to the legend, without the
// starfield. The badge is decoration: it is drawn only when the screen with it
// still fits the terminal, so on one exactly as tall as the screen without it
// the legend is not pushed off the bottom.
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
		if m.spareRows(under()) < 0 {
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

// starRowCount is how many rows below screen the starfield may draw in: the
// spare rows less the blank one kept under the legend. Zero or less is none.
func (m Model) starRowCount(screen string) int {
	return m.spareRows(screen) - 1
}

// starRows is the starfield: the rows below the legend that the terminal has
// and the screen does not use, one blank row left under the legend, the stars
// mapped onto the rest. It never adds a row the terminal does not have, and
// never touches a row that holds content.
func (m Model) starRows(stars []starMark, screen string) string {
	cw := m.contentWidth()
	rows := m.starRowCount(screen)
	if rows < 1 || len(stars) == 0 {
		return ""
	}
	grid := make([][]rune, rows)
	for _, s := range stars {
		r, c := mod(s.row, rows), mod(s.col, cw)
		if grid[r] == nil {
			grid[r] = []rune(strings.Repeat(" ", cw))
		}
		grid[r][c] = s.glyph
	}
	out := make([]string, rows)
	for i, row := range grid {
		if row == nil {
			continue
		}
		var b strings.Builder
		last := len(strings.TrimRight(string(row), " "))
		for _, r := range string(row)[:last] {
			if r == ' ' {
				b.WriteRune(' ')
				continue
			}
			b.WriteString(styles().faint.Render(string(r)))
		}
		out[i] = b.String()
	}
	return "\n\n" + join(out...)
}

// mod is n mod d, never negative.
func mod(n, d int) int {
	if d <= 0 {
		return 0
	}
	return ((n % d) + d) % d
}
