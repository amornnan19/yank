package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// motionEpoch is the time of the first motion tick in every test. The clock is
// whatever the ticks say, so any fixed instant will do.
var motionEpoch = time.Unix(1_700_000_000, 0)

// noEnv is an environment with nothing set: no off switch.
func noEnv(string) (string, bool) { return "", false }

// envWith is an environment holding exactly one variable.
func envWith(key, value string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		if k == key {
			return value, true
		}
		return "", false
	}
}

// motionModel is testModel with motion switched on — the whole registry, or
// only the effects given — after the resize that shows the screen.
func motionModel(t *testing.T, f *fakes, width, height int, effects ...func() effect) Model {
	t.Helper()
	m := testModel(t, f, width)
	if len(effects) == 0 {
		m.motion = newMotion(noEnv, 1)
	} else {
		built := make([]effect, len(effects))
		for i, build := range effects {
			built[i] = build()
		}
		m.motion = newMotionOf(built, 1)
	}
	return send(m, tea.WindowSizeMsg{Width: width, Height: height})
}

// nextTick is the tick the Bubble Tea loop would deliver next, or false when
// none is outstanding. The first tick lands at motionEpoch; every later one at
// the time the model scheduled it for.
func nextTick(m Model) (motionTickMsg, bool) {
	mo := m.motion
	if mo.pending == tickNone {
		return motionTickMsg{}, false
	}
	at := mo.due
	if mo.now.IsZero() {
		at = motionEpoch
	}
	return motionTickMsg{gen: mo.gen, at: at}, true
}

// clock is how far the model's motion clock is past motionEpoch.
func clock(m Model) time.Duration {
	if m.motion.now.IsZero() {
		return -1
	}
	return m.motion.now.Sub(motionEpoch)
}

// runMotion delivers ticks the way the loop would, one at a time, until none
// is outstanding or the next would land after until. each, if not nil, sees
// the model after every tick.
func runMotion(t *testing.T, m Model, until time.Duration, each func(Model)) Model {
	t.Helper()
	for range 100_000 {
		tick, ok := nextTick(m)
		if !ok || tick.at.Sub(motionEpoch) > until {
			return m
		}
		m = send(m, tick)
		if each != nil {
			each(m)
		}
	}
	t.Fatal("the motion clock never stopped or reached its deadline")
	return m
}

// effectRig steps one effect outside a model, for the tests that watch an
// effect's own states: what it paints, whether it is busy, when it wakes.
type effectRig struct {
	mo    motion
	value string
}

func rig(build func() effect) *effectRig {
	return &effectRig{mo: newMotionOf([]effect{build()}, 1)}
}

func (r *effectRig) send(kind motionEventKind) {
	r.mo.broadcast(motionEvent{kind: kind}, r.value)
}

func (r *effectRig) tickAt(d time.Duration) {
	r.mo.now = motionEpoch.Add(d)
	r.mo.broadcast(motionEvent{kind: evTick}, r.value)
}

func (r *effectRig) effect() effect { return r.mo.effects[0] }

func (r *effectRig) frame(cw int) inputFrame { return r.mo.frame(cw) }

// idle reports that the effect wants no tick of any kind.
func (r *effectRig) idle() bool { return !r.effect().busy() && r.effect().wake().IsZero() }

// plainWordmark is a frame's drawing as text, one string per row.
func plainWordmark(f inputFrame) []string {
	out := make([]string, len(f.wordmark))
	for i, row := range f.wordmark {
		var b strings.Builder
		for _, c := range row {
			b.WriteRune(c.r)
		}
		out[i] = b.String()
	}
	return out
}

// staticWordmark is the drawing as the static screen has it, padded to the
// canvas width.
func staticWordmark(cw int) []string {
	width := min(cw, wordmarkWidth()+wordmarkSlack)
	out := make([]string, len(wordmarkRows))
	for i, row := range wordmarkRows {
		out[i] = padRight(row, width)
	}
	return out
}

// --- off switches -----------------------------------------------------------

func TestMotionOffSwitches(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) (string, bool)
		off  bool
	}{
		{"nothing set", noEnv, false},
		{"NO_COLOR set", envWith("NO_COLOR", "1"), true},
		{"NO_COLOR set but empty", envWith("NO_COLOR", ""), true},
		{"YANK_NO_MOTION set", envWith("YANK_NO_MOTION", "1"), true},
		{"YANK_NO_MOTION empty", envWith("YANK_NO_MOTION", ""), false},
	}
	for _, tc := range cases {
		if got := newMotion(tc.env, 1).enabled; got == tc.off {
			t.Errorf("%s: motion enabled = %v, want %v", tc.name, got, !tc.off)
		}
	}
}

// staticScript drives a model through the input screen — resize, type, a
// failed enter, a good enter — and returns every view on the way plus whether
// a motion tick was ever outstanding.
func staticScript(t *testing.T, m Model, width, height int) (views []string, ticked bool) {
	t.Helper()
	m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
	record := func() {
		views = append(views, m.View())
		ticked = ticked || m.motion.pending != tickNone
	}
	record()
	for _, r := range "not a url" {
		m = send(m, runes(string(r)))
		record()
	}
	m = send(m, keyOf(tea.KeyEnter))
	record()
	m.input.SetValue("")
	for _, r := range "https://www.youtube.com/watch?v=x" {
		m = send(m, runes(string(r)))
		record()
	}
	m.hasBin = true
	m = send(m, keyOf(tea.KeyEnter))
	record()
	return views, ticked
}

func TestEveryOffSwitchDrawsTodaysStaticScreen(t *testing.T) {
	type sw struct {
		name          string
		env           func(string) (string, bool)
		width, height int
	}
	switches := []sw{
		{"NO_COLOR", envWith("NO_COLOR", "1"), 80, 24},
		{"YANK_NO_MOTION", envWith("YANK_NO_MOTION", "yes"), 80, 24},
		{"too short for the wordmark", noEnv, 80, minWordmarkHeight - 1},
		{"too narrow for the wordmark", noEnv, wordmarkWidth() + 3, 24},
	}
	// Each effect alone, and the whole registry, so an effect that forgot an
	// off switch cannot hide behind the others.
	sets := map[string][]func() effect{"all effects": effectRegistry}
	for i, build := range effectRegistry {
		sets["effect "+itoa(i)] = []func() effect{build}
	}

	for _, s := range switches {
		want, _ := staticScript(t, testModel(t, &fakes{}, s.width), s.width, s.height)
		for name, set := range sets {
			m := testModel(t, &fakes{}, s.width)
			if motionSwitchedOff(s.env) {
				m.motion = newMotion(s.env, 1)
			} else {
				built := make([]effect, len(set))
				for i, build := range set {
					built[i] = build()
				}
				m.motion = newMotionOf(built, 1)
			}
			got, ticked := staticScript(t, m, s.width, s.height)
			if ticked {
				t.Errorf("%s, %s: a motion tick was scheduled", s.name, name)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s, %s: frame %d differs from the static screen:\n%s\n--- want ---\n%s", s.name, name, i, got[i], want[i])
					break
				}
			}
		}
	}
}

// --- the clock --------------------------------------------------------------

func TestTheFirstFrameTheWordmarkFitsStartsTheClock(t *testing.T) {
	m := testModel(t, &fakes{}, 80)
	m.motion = newMotion(noEnv, 1)
	m.height = 0
	m.layout()
	// Height unknown: the wordmark does not fit, so nothing moves yet.
	m = send(m, otherMsg())
	if m.motion.pending != tickNone {
		t.Fatalf("a tick was scheduled before the wordmark fitted")
	}
	m, cmd := step(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.motion.pending != tickFrame || cmd == nil {
		t.Fatalf("the resize that fits the wordmark scheduled %v, want a frame tick", m.motion.pending)
	}
	// A second resize with the tick still outstanding does not start another.
	gen := m.motion.gen
	m, cmd = step(m, tea.WindowSizeMsg{Width: 90, Height: 30})
	if m.motion.gen != gen || cmd != nil {
		t.Errorf("a resize while a frame tick was outstanding replaced it: gen %d → %d", gen, m.motion.gen)
	}
}

// otherMsg is some message that is not a key, a resize or a tick: the
// cursor blink the input screen gets all the time.
func otherMsg() tea.Msg { return struct{}{} }

func TestNoFrameTickIsPendingOnceEveryEffectIsIdle(t *testing.T) {
	m := motionModel(t, &fakes{}, 80, 24)
	frames := 0
	// Past the intro and the sweep, and short of the first shimmer: nothing
	// is mid-animation, so every tick scheduled is a single wake.
	m = runMotion(t, m, 3900*time.Millisecond, func(m Model) {
		if clock(m) > 1100*time.Millisecond && m.motion.pending == tickFrame {
			frames++
		}
	})
	if frames != 0 {
		t.Errorf("%d frame ticks were scheduled while every effect was idle", frames)
	}
	if m.motion.pending != tickWake {
		t.Errorf("idle on the input screen the outstanding tick is %v, want one wake", m.motion.pending)
	}
	for i, e := range m.motion.effects {
		if e.busy() {
			t.Errorf("effect %d (%T) is still busy at %v", i, e, clock(m))
		}
	}

}

// stubEffect is a scheduler test's effect: busy for busyFor after the screen
// is shown, then waking wakes times, wakeEvery apart, then nothing. It stands in
// for the real effects so the clock is tested without any of them, and
// deleting an effect never touches these tests.
type stubEffect struct {
	cue       cue
	last      time.Time
	busyFor   time.Duration
	wakeEvery time.Duration
	wakes     int
}

func (e stubEffect) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		e.cue.arm()
	case evTick:
		e.cue.settle(ev.now)
		e.last = ev.now
	}
	return e
}

func (e stubEffect) busy() bool {
	return e.cue.waiting || (e.cue.running && e.cue.elapsed(e.last) < e.busyFor)
}

func (e stubEffect) wake() time.Time {
	if !e.cue.running || e.wakeEvery <= 0 {
		return time.Time{}
	}
	for k := 1; k <= e.wakes; k++ {
		if at := e.cue.at.Add(e.busyFor + time.Duration(k)*e.wakeEvery); at.After(e.last) {
			return at
		}
	}
	return time.Time{}
}

func (e stubEffect) paint(*inputFrame) {}

func TestTheClockRunsFramesWhileBusyWakesWhileIdleAndThenStops(t *testing.T) {
	stub := func(busyFor, every time.Duration, wakes int) func() effect {
		return func() effect { return stubEffect{busyFor: busyFor, wakeEvery: every, wakes: wakes} }
	}
	m := motionModel(t, &fakes{}, 80, 24, stub(200*time.Millisecond, 0, 0), stub(0, time.Second, 3))

	var kinds []tickKind
	var times []time.Duration
	m = runMotion(t, m, time.Hour, func(m Model) {
		kinds = append(kinds, m.motion.pending)
		times = append(times, clock(m))
	})
	if m.motion.pending != tickNone {
		t.Fatalf("with every effect idle a %v tick is outstanding", m.motion.pending)
	}
	frames, wakes := 0, 0
	for i, k := range kinds {
		switch k {
		case tickFrame:
			frames++
			if times[i] >= 200*time.Millisecond {
				t.Errorf("a frame tick was scheduled at %v, after the busy effect finished", times[i])
			}
		case tickWake:
			wakes++
		}
	}
	// 200ms of frames at 30 fps, then the wakes at 1s, 2s (the tick at 3s
	// schedules nothing).
	if frames < 5 || frames > 7 {
		t.Errorf("%d frame ticks for 200ms of motion, want about 6", frames)
	}
	if wakes != 3 {
		t.Errorf("%d wake ticks scheduled, want 3: %v at %v", wakes, kinds, times)
	}
	if last := times[len(times)-1]; last != 3*time.Second {
		t.Errorf("the clock last ticked at %v, want the last wake at 3s", last)
	}
}

// editStub wakes lazily until the box is edited, then wants a wake almost at
// once — or, with stopOnEdit, nothing at all. It does what the effect contract
// says not to rely on, changing its wake off a tick, so the scheduler's own
// guard is what is tested.
type editStub struct {
	cue        cue
	last       time.Time
	edited     bool
	stopOnEdit bool
}

func (e editStub) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		e.cue.arm()
	case evEdit:
		e.edited = true
	case evTick:
		e.cue.settle(ev.now)
		e.last = ev.now
	}
	return e
}

func (e editStub) busy() bool { return e.cue.waiting }

func (e editStub) wake() time.Time {
	switch {
	case !e.cue.running, e.edited && e.stopOnEdit:
		return time.Time{}
	case e.edited:
		return e.last.Add(10 * time.Millisecond)
	}
	return e.last.Add(5 * time.Second)
}

func (e editStub) paint(*inputFrame) {}

func TestAWakeNeededOffATickIsTimedByAFrameTick(t *testing.T) {
	m := motionModel(t, &fakes{}, 80, 24, func() effect { return editStub{} })
	m = runMotion(t, m, 0, nil)
	if m.motion.pending != tickWake {
		t.Fatalf("idle, the outstanding tick is %v, want a wake", m.motion.pending)
	}
	gen := m.motion.gen
	// The edit wants a wake before the one outstanding, and off a tick the
	// clock is stale: a frame tick learns the time instead.
	m = send(m, runes("a"))
	if m.motion.pending != tickFrame || m.motion.gen == gen {
		t.Errorf("an earlier wake wanted off a tick scheduled %v (gen %d → %d), want a new frame tick", m.motion.pending, gen, m.motion.gen)
	}
	m = runMotion(t, m, clock(m)+motionFrame, nil)
	if m.motion.pending != tickWake {
		t.Errorf("after the frame tick the outstanding tick is %v, want the wake", m.motion.pending)
	}

	// An edit that leaves nothing wanted stops the outstanding wake at once.
	m = motionModel(t, &fakes{}, 80, 24, func() effect { return editStub{stopOnEdit: true} })
	m = runMotion(t, m, 0, nil)
	m = send(m, runes("a"))
	if m.motion.pending != tickNone {
		t.Errorf("with nothing wanted after an edit a %v tick is still outstanding", m.motion.pending)
	}
}

func TestDoneEnterKeepsMotionOnTheFreshInputScreen(t *testing.T) {
	m := downloadingModel(t, &fakes{})
	m.motion = newMotion(noEnv, 1)
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(m, downloadDoneMsg{seq: m.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})
	if m.motion.pending != tickNone {
		t.Fatalf("the done screen has a %v motion tick outstanding", m.motion.pending)
	}
	gen := m.motion.gen
	m = send(m, keyOf(tea.KeyEnter))
	if m.state != stateInput {
		t.Fatalf("enter on the done screen went to %v", m.state)
	}
	if !m.motion.enabled || m.motion.pending != tickFrame {
		t.Errorf("the fresh input screen has motion %v with a %v tick, want it on and ticking", m.motion.enabled, m.motion.pending)
	}
	if m.motion.gen < gen {
		t.Errorf("the fresh screen's generation went back from %d to %d; a stale tick could match it", gen, m.motion.gen)
	}
}

func TestAStaleTickIsDroppedAndNeverStartsASecondChain(t *testing.T) {
	m := motionModel(t, &fakes{}, 80, 24)
	m = runMotion(t, m, 100*time.Millisecond, nil)
	stale, _ := nextTick(m)

	// A key press while a frame tick is outstanding keeps that one tick.
	gen := m.motion.gen
	for _, r := range "abc" {
		var cmd tea.Cmd
		m, cmd = step(m, runes(string(r)))
		if m.motion.gen != gen {
			t.Fatalf("typing replaced the outstanding tick: gen %d → %d", gen, m.motion.gen)
		}
		if cmd != nil {
			for _, msg := range collect(t, cmd) {
				if _, ok := msg.(motionTickMsg); ok {
					t.Fatalf("typing scheduled a second motion tick")
				}
			}
		}
	}

	live, _ := nextTick(m)
	m = send(m, live)
	// The tick just delivered is now stale; delivering it again changes nothing.
	before := m.motion
	again, cmd := step(m, live)
	if cmd != nil {
		t.Errorf("a duplicate tick scheduled another")
	}
	if again.motion.gen != before.gen || again.motion.now != before.now || again.motion.pending != before.pending {
		t.Errorf("a duplicate tick moved the clock or the schedule")
	}
	if _, cmd := step(m, stale); cmd != nil {
		t.Errorf("a tick from an earlier generation scheduled another")
	}
}

func TestLeavingTheInputScreenDropsTheChain(t *testing.T) {
	f := &fakes{}
	m := motionModel(t, f, 80, 24)
	m.hasBin = true
	m = runMotion(t, m, 200*time.Millisecond, nil)
	m.input.SetValue("https://example.com/v")
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = runMotion(t, m, 300*time.Millisecond, nil)
	before, _ := nextTick(m)

	m = send(m, keyOf(tea.KeyEnter))
	if m.state != stateProbing {
		t.Fatalf("enter did not start the probe: state %v", m.state)
	}
	m = runMotion(t, m, time.Hour, nil)
	if m.motion.pending != tickNone {
		t.Fatalf("on the probing screen a %v tick is still outstanding", m.motion.pending)
	}
	if clock(m) > 300*time.Millisecond+yankDurationBound {
		t.Errorf("the clock ran until %v, past the exit", clock(m))
	}
	if _, cmd := step(m, before); cmd != nil {
		t.Errorf("a tick from the input screen rescheduled itself on the probing screen")
	}
	// Spinner ticks on the probing screen schedule no motion.
	for range 3 {
		var cmd tea.Cmd
		m, cmd = step(m, m.spin.Tick())
		for _, msg := range collect(t, cmd) {
			if _, ok := msg.(motionTickMsg); ok {
				t.Fatalf("the probing screen scheduled a motion tick")
			}
		}
	}

	// esc goes back, and a fresh chain starts under a new generation.
	m = send(m, keyOf(tea.KeyEsc))
	if m.motion.pending != tickFrame {
		t.Fatalf("back on the input screen the outstanding tick is %v, want a frame tick", m.motion.pending)
	}
	if m.motion.gen <= before.gen {
		t.Errorf("the new chain reused generation %d", m.motion.gen)
	}
	if _, cmd := step(m, before); cmd != nil {
		t.Errorf("the old chain's tick was accepted by the new one")
	}

	// Every other way off the screen ends it too.
	for name, leave := range map[string]func(Model) Model{
		"resize below the wordmark": func(m Model) Model { return send(m, tea.WindowSizeMsg{Width: 80, Height: 8}) },
		"ctrl+c":                    func(m Model) Model { return send(m, keyOf(tea.KeyCtrlC)) },
	} {
		m := motionModel(t, &fakes{}, 80, 24)
		m = runMotion(t, m, 100*time.Millisecond, nil)
		m = leave(m)
		if m.motion.pending != tickNone {
			t.Errorf("%s left a %v tick outstanding", name, m.motion.pending)
		}
	}
}

// yankDurationBound is the most any exit may take, from the spec.
const yankDurationBound = 300 * time.Millisecond

// --- never in the way -------------------------------------------------------

func TestKeysWorkOnEveryFrameOfTheIntro(t *testing.T) {
	for at := time.Duration(0); at < 800*time.Millisecond; at += 70 * time.Millisecond {
		m := motionModel(t, &fakes{}, 80, 24)
		m = runMotion(t, m, at, nil)

		typed := send(m, runes("h"))
		if typed.input.Value() != "h" {
			t.Errorf("at %v typing did not reach the box: %q", at, typed.input.Value())
		}
		pasted := send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://a.b/c"), Paste: true})
		if pasted.input.Value() != "https://a.b/c" {
			t.Errorf("at %v a paste did not reach the box: %q", at, pasted.input.Value())
		}
		if _, cmd := step(m, keyOf(tea.KeyCtrlC)); !quitsNow(cmd) {
			t.Errorf("at %v ctrl+c did not quit", at)
		}

		f := &fakes{}
		entered := m
		entered.deps = f.deps()
		entered.hasBin = true
		entered.input.SetValue("https://example.com/v")
		entered, cmd := step(entered, keyOf(tea.KeyEnter))
		if entered.state != stateProbing {
			t.Fatalf("at %v enter did not start the probe", at)
		}
		// The probe is in the command enter returned, not behind the exit.
		if _, ok := findMsg[probeDoneMsg](collect(t, cmd)); !ok {
			t.Errorf("at %v enter's command did not run the probe", at)
		}
		// Enter mid-intro skips it: the next frame's wordmark is the drawing.
		entered = runMotion(t, entered, clock(entered)+motionFrame, nil)
		if entered.motionShowing() {
			got := plainWordmark(entered.motion.frame(entered.contentWidth()))
			if strings.ContainsAny(strings.Join(got, ""), "░▒▓") {
				t.Errorf("at %v the intro was still scrambling after enter:\n%s", at, strings.Join(got, "\n"))
			}
		}
	}
}

// --- every frame, measured --------------------------------------------------

// motionScript runs the whole registry through everything it does — the intro,
// the sweep, typing into the box, an invalid URL, the badge, the shimmer, the
// stars, and the exit — calling each for every frame that shows motion.
func motionScript(t *testing.T, width, height int, seed uint64, each func(name string, m Model)) {
	t.Helper()
	m := testModel(t, &fakes{}, width)
	m.motion = newMotion(noEnv, seed)
	m.hasBin = true
	m = send(m, tea.WindowSizeMsg{Width: width, Height: height})
	see := func(name string) func(Model) {
		return func(m Model) {
			if m.motionShowing() {
				each(name+" at "+clock(m).String(), m)
			}
		}
	}
	see("first frame")(m)
	m = runMotion(t, m, 1200*time.Millisecond, see("intro"))
	for _, r := range "not a url" {
		m = send(m, runes(string(r)))
		see("typing an invalid URL")(m)
	}
	m = send(m, keyOf(tea.KeyEnter))
	see("the hint")(m)
	m.input.SetValue("")
	m = send(m, runes("h"))
	m = send(m, keyOf(tea.KeyBackspace))
	m = runMotion(t, m, 2500*time.Millisecond, see("empty again"))
	for _, r := range "https://www.youtube.com/watch?v=x" {
		m = send(m, runes(string(r)))
		see("typing a URL")(m)
	}
	m = runMotion(t, m, 5*time.Second, see("badge and shimmer"))
	m = send(m, keyOf(tea.KeyEnter))
	see("enter")(m)
	runMotion(t, m, 6*time.Second, see("exit"))
}

func TestEveryMotionFrameFitsTheTerminalAndThePalette(t *testing.T) {
	trueColour(t)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {40, 24}, {wordmarkWidth() + 4, minWordmarkHeight}, {wordmarkWidth() + 6, 16}} {
		width, height := size[0], size[1]
		frames := 0
		boxRow := -1
		motionScript(t, width, height, 1, func(name string, m Model) {
			frames++
			view := m.View()
			where := name + " at " + itoa(width) + "x" + itoa(height)
			if !strings.ContainsRune(view, escape) {
				t.Fatalf("%s rendered no escape sequence at TrueColor", where)
			}
			lines := strings.Split(view, "\n")
			// The hint is today's line, and on the shortest terminal the
			// static screen with the hint is already a row too tall; motion
			// may not add to that.
			limit := height
			if m.hint != "" {
				static := m
				static.motion.enabled = false
				limit = max(limit, strings.Count(static.View(), "\n")+1)
			}
			if len(lines) > limit {
				t.Errorf("%s is %d rows tall in a %d-row terminal:\n%s", where, len(lines), height, view)
			}
			for n, line := range lines {
				assertLineIsPaletteSafe(t, where, width, n, line)
			}
			// No frame adds a row above the box: a dropping letter is clipped
			// to the wordmark's own rows.
			row := lineContaining(view, "╭")
			if boxRow < 0 {
				boxRow = row
			}
			if row != boxRow {
				t.Errorf("%s has the box on row %d, want row %d as on every other frame:\n%s", where, row, boxRow, view)
			}
		})
		if frames < 50 {
			t.Errorf("at %dx%d only %d frames showed motion; the script is not exercising the effects", width, height, frames)
		}
	}
}

func TestMotionIsDeterministicForASeed(t *testing.T) {
	record := func(seed uint64) []string {
		var views []string
		motionScript(t, 80, 24, seed, func(_ string, m Model) { views = append(views, m.View()) })
		return views
	}
	a, b, c := record(7), record(7), record(8)
	if !equalLines(a, b) {
		t.Errorf("two runs with the same seed drew different frames")
	}
	if equalLines(a, c) {
		t.Errorf("two different seeds drew identical frames; the randomness is not coming from the seed")
	}
}
