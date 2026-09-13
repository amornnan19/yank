package ui

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// --- finding 1: the model the program ended on ------------------------------

func TestReleaseFinalCleansTheInfoJSON(t *testing.T) {
	// tea.WithContext ends the program without running Update, so the key
	// handlers' cleanup ledger never gets a turn. This is the only thing
	// standing between a SIGTERM and a permanent multi-megabyte temp file.
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	infoJSON := m.probe.InfoJSONPath

	releaseFinal(m)

	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times for the final model, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived the program shutting down", infoJSON)
	}
}

func TestReleaseFinalIsHarmlessOnEveryOtherShape(t *testing.T) {
	// Bubble Tea hands back a nil interface when it fails before the loop
	// starts, and Run must not care.
	releaseFinal(nil)

	f := &fakes{}
	m := testModel(t, f, 80) // no probe yet
	releaseFinal(m)
	if n := f.cleanups(); n != 0 {
		t.Fatalf("Cleanup called %d times for a model with nothing to clean, want 0", n)
	}

	// And after an ordinary exit that already cleaned up, it does not fire a
	// second time.
	f2 := &fakes{}
	m2 := downloadingModel(t, f2)
	m2 = send(m2, downloadDoneMsg{seq: m2.seq, res: &ytdlp.DownloadResult{Path: "/x/a.mp4"}})
	releaseFinal(m2)
	if n := f2.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times across a normal exit plus the final release, want 1", n)
	}
}

// --- finding 2: remote text is not safe to print ----------------------------

func TestSanitise(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean text is untouched", "Never Gonna Give You Up", "Never Gonna Give You Up"},
		{"a row label keeps its own spacing", "1080p60  mp4  ~142 MB", "1080p60  mp4  ~142 MB"},
		{"newline", "line one\nline two", "line one line two"},
		{"carriage return", "clean title\rEVIL", "clean title EVIL"},
		{"tab", "a\tb", "a b"},
		{"DEL", "a\x7fb", "a b"},
		{"C1 control", "a\u0085b", "a b"},
		{"SGR", "Title \x1b[31mRED\x1b[0m end", "Title RED end"},
		{"OSC ending in BEL", "a\x1b]0;pwned\x07b", "ab"},
		{"OSC ending in ST", "a\x1b]0;pwned\x1b\\b", "ab"},
		{"OSC ending in 8-bit ST", "a\x1b]0;pwned\u009cb", "ab"},
		{"unterminated CSI swallows the rest", "a\x1b[31", "a"},
		{"unterminated OSC swallows the rest", "a\x1b]0;never ends", "a"},
		{"8-bit CSI", "a\u009b31mb", "ab"},
		{"charset selector", "a\x1b(Bb", "ab"},
		{"single-byte escape", "a\x1bcb", "ab"},
		{"lone escape at the end", "ab\x1b", "ab"},
		{"right-to-left override", "fpm.exe\u202egnp.", "fpm.exegnp."},
		{"isolates", "\u2066a\u2069b", "ab"},
		{"a zero-width joiner is ordinary text", "a\u200db", "a\u200db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitise(tc.in); got != tc.want {
				t.Errorf("sanitise(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitisedTextCarriesNothingTheTerminalWouldExecute(t *testing.T) {
	for _, in := range []string{
		"a\x1b[31mb", "a\x1b]0;x\x07b", "a\nb", "a\rb", "a\u009b1mb", "a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\",
	} {
		got := sanitise(in)
		if strings.ContainsRune(got, escape) || strings.ContainsRune(got, csi8) {
			t.Errorf("sanitise(%q) = %q, still carries an escape introducer", in, got)
		}
		for _, r := range got {
			if isControlRune(r) {
				t.Errorf("sanitise(%q) = %q, still carries control rune %U", in, got, r)
			}
		}
	}
}

func TestAMultilineTitleMeasuresAndTruncatesHonestly(t *testing.T) {
	// lipgloss.Width reports the widest line, so an unsanitised two-line title
	// measures small, survives truncation whole, and then takes two rows.
	hostile := "line one\nline two is here"
	if lipgloss.Width(hostile) > 20 {
		t.Fatalf("premise broken: lipgloss.Width(%q) = %d", hostile, lipgloss.Width(hostile))
	}

	got := truncate(hostile, 20)
	if strings.Contains(got, "\n") {
		t.Fatalf("truncate(%q, 20) = %q, still two lines", hostile, got)
	}
	if lipgloss.Width(got) > 20 {
		t.Fatalf("truncate(%q, 20) = %q, %d cells wide", hostile, got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("truncate(%q, 20) = %q, want it cut once measured honestly", hostile, got)
	}
}

// hostileTitle carries the three things a scraped page can put in a title that
// a terminal would act on: a newline, a carriage return and an SGR sequence.
const hostileTitle = "Rick\x1b[31mAstley\x1b[0m\nSECOND LINE\rOVERWRITE"

func TestAHostileTitleRendersSafely(t *testing.T) {
	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, hostileTitle, "up\x1b]0;pwned\x07loader")}}}
	m := pickerModel(t, f, true, threeRows())

	clean := m.View()
	assertRenderedSafely(t, "picker", clean, 80)
	if strings.Contains(clean, "SECOND LINE\n") {
		t.Errorf("the title's newline made it to the screen:\n%q", clean)
	}

	// The same text reaches the download screen and the error screen.
	m2 := downloadingModel(t, &fakes{probes: []probeOutcome{{probe: newProbe(t, hostileTitle, "")}}})
	assertRenderedSafely(t, "downloading", m2.View(), 80)

	m3 := downloadingModel(t, &fakes{})
	m3 = send(m3, downloadDoneMsg{seq: m3.seq, err: &ytdlp.ExtractError{
		Message: "Video unavailable: " + hostileTitle,
		Stderr:  "ERROR: whatever",
	}})
	assertRenderedSafely(t, "error", m3.View(), 80)

	m4 := downloadingModel(t, &fakes{})
	m4 = send(m4, downloadDoneMsg{seq: m4.seq, res: &ytdlp.DownloadResult{
		Path: "/Users/x/Downloads/" + hostileTitle + ".mp4",
	}})
	assertRenderedSafely(t, "done", m4.View(), 80)
}

// assertRenderedSafely is the whole rule in one place: nothing the terminal
// would execute, no stray row, and no line wider than the terminal.
func assertRenderedSafely(t *testing.T, name, view string, width int) {
	t.Helper()
	for _, r := range view {
		if r == '\n' {
			continue
		}
		if isControlRune(r) || isBidiOverride(r) {
			t.Errorf("%s rendered control rune %U:\n%q", name, r, view)
		}
	}
	if got := widest(view); got > width {
		t.Errorf("%s is %d cells wide at width %d:\n%s", name, got, width, view)
	}
}

// --- finding 3: truncate before styling -------------------------------------

// forceColour makes lipgloss emit escape sequences for the rest of the test.
//
// `go test` writes to a pipe, so the default profile is Ascii and every style
// renders as the identity — which is precisely the condition that hides a
// style-then-truncate. The profile is stepped down to the first one that emits
// an escape rather than set outright: these tests are about where the cut
// falls, and the nearest profile above Ascii shows that. trueColour below is
// the one that names a profile, because the palette rule needs the richest.
func forceColour(t *testing.T) {
	t.Helper()
	original := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(original) })

	for p := original - 1; p >= 0; p-- {
		lipgloss.SetColorProfile(p)
		if strings.ContainsRune(lipgloss.NewStyle().Bold(true).Render("x"), escape) {
			return
		}
	}
	t.Skip("lipgloss will not emit escape sequences here; this test cannot observe the defect")
}

func TestTheSelectedRowIsTruncatedBeforeItIsStyled(t *testing.T) {
	forceColour(t)

	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	// Narrow enough that every row has to be cut, which is the only width at
	// which the order of truncate and Render can be told apart.
	m = send(m, tea.WindowSizeMsg{Width: 28, Height: 24})

	// The rows are found on their text with the styling stripped: the number
	// cell of an unselected row is faint, so the sequence that opens it sits
	// between the marker and the digit.
	var selected, unselected string
	for _, line := range strings.Split(m.View(), "\n") {
		switch plain := sgr.ReplaceAllString(line, ""); {
		case strings.Contains(plain, selectedMarker+"1."):
			selected = line
		case strings.Contains(plain, unselectedMarker+"2."):
			unselected = line
		}
	}
	if selected == "" || unselected == "" {
		t.Fatalf("could not find both rows in:\n%s", m.View())
	}

	// Styled at all: a Render applied to an already-cut string keeps its
	// sequences, where a cut applied to an already-rendered one loses them.
	if !strings.ContainsRune(selected, escape) {
		t.Errorf("the selected row carries no styling:\n%q", selected)
	}
	// And closed: a truncated escape sequence drops the reset, and the style
	// then bleeds into every line below it.
	if !strings.HasSuffix(strings.TrimRight(selected, " "), "\x1b[0m") {
		t.Errorf("the selected row does not end with a reset, so its style bleeds:\n%q", selected)
	}
	// The margin and the full content width: a leading escape sequence eaten
	// out of the truncation budget shortens the row it is on. The row is
	// padded to the content width before it is styled.
	if got, want := lipgloss.Width(selected), m.marginLeft()+m.contentWidth(); got != want {
		t.Errorf("selected row is %d cells, want the margin and the content width, %d\n%q", got, want, selected)
	}
}

func TestPickerLinesArePlainAndNoWiderThanTheColumn(t *testing.T) {
	// Colour is forced because the assertion below is that pickerLines does
	// not style: at the Ascii profile a Render inside it would be the identity
	// and there would be nothing to see.
	forceColour(t)

	rows := threeRows()
	for _, w := range []int{20, 76} {
		// 20 is narrow enough that every row is cut; 76 is the aligned layout.
		// A row that fits is not evidence about how a cut one is built, and
		// the other way round.
		lines := pickerLines(rows, 0, w)
		if len(lines) != len(rows) {
			t.Fatalf("pickerLines returned %d lines for %d rows", len(lines), len(rows))
		}
		for i, line := range lines {
			if strings.ContainsRune(line, escape) {
				t.Errorf("pickerLines(%d)[%d] = %q is already styled; styling is fit's job, after the cut", w, i, line)
			}
			if lipgloss.Width(line) > w {
				t.Errorf("pickerLines(%d)[%d] = %q is %d cells wide", w, i, line, lipgloss.Width(line))
			}
		}
		if lipgloss.Width(lines[0]) != w {
			t.Errorf("at width %d the selected row is %d cells, want the full %d so its reverse block spans the row: %q", w, lipgloss.Width(lines[0]), w, lines[0])
		}
	}
}

func TestFitCutsBeforeItStyles(t *testing.T) {
	forceColour(t)

	long := strings.Repeat("a", 40)
	got := fit(styles().selected, long, 20)

	if lipgloss.Width(got) != 20 {
		t.Errorf("fit produced %d visible cells, want 20: %q", lipgloss.Width(got), got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("fit dropped the reset sequence: %q", got)
	}
}

// --- finding 4: ctrl+c waits for the run it cancelled -----------------------

// quitsNow reports whether cmd is tea.Quit itself rather than a batch that
// keeps the model alive.
func quitsNow(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCtrlCDuringADownloadWaitsForItToStop(t *testing.T) {
	// runDownload removes the partial files after Wait returns. Quitting in the
	// same Update would race the process exit against that, and lose.
	f := &fakes{}
	m := downloadingModel(t, f)
	runCtx := m.runCtx

	m, cmd := step(m, keyOf(tea.KeyCtrlC))

	if quitsNow(cmd) {
		t.Fatal("ctrl+c quit before the download it cancelled had reported back")
	}
	if !m.quitting {
		t.Fatal("ctrl+c did not put the model into its shutdown wait")
	}
	if runCtx.Err() == nil {
		t.Fatal("ctrl+c did not cancel the download")
	}
	if n := f.cleanups(); n != 0 {
		t.Fatalf("Cleanup called %d times while yt-dlp is still reading the info-json, want 0", n)
	}
	if !strings.Contains(m.View(), "stopping") {
		t.Fatalf("the shutdown screen does not say what it is waiting for:\n%s", m.View())
	}

	m, cmd = step(m, downloadDoneMsg{seq: m.seq, err: cancelledErr()})

	if !quitsNow(cmd) {
		t.Fatal("the download reported back and the program still did not quit")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times over the shutdown, want 1", n)
	}
}

func TestTheShutdownWaitIsBounded(t *testing.T) {
	// A kill that never lands must not hold the terminal.
	f := &fakes{}
	m := downloadingModel(t, f)
	infoJSON := m.probe.InfoJSONPath

	m, _ = step(m, keyOf(tea.KeyCtrlC))
	_, cmd := step(m, quitTimeoutMsg{})

	if !quitsNow(cmd) {
		t.Fatal("the shutdown deadline expired and the program did not quit")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times when the deadline expired, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived a deadline shutdown", infoJSON)
	}
}

func TestASecondCtrlCLeavesAtOnce(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)

	m, _ = step(m, keyOf(tea.KeyCtrlC))
	_, cmd := step(m, keyOf(tea.KeyCtrlC))

	if !quitsNow(cmd) {
		t.Fatal("a second ctrl+c did not leave immediately")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times, want 1", n)
	}
}

func TestOtherKeysDoNotEndTheShutdownWait(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m, _ = step(m, keyOf(tea.KeyCtrlC))

	for _, key := range []tea.KeyMsg{keyOf(tea.KeyEsc), keyOf(tea.KeyEnter), runes("q")} {
		next, cmd := step(m, key)
		if quitsNow(cmd) {
			t.Fatalf("%v cut the shutdown wait short", key)
		}
		if !next.quitting {
			t.Fatalf("%v took the model out of its shutdown wait", key)
		}
	}
}

func TestALateProbeArrivingDuringAShutdownIsCleanedUp(t *testing.T) {
	// The stale-info retry has a probe in flight and no download. Its result
	// still owns a file, and this is the last code that will ever see it.
	f := &fakes{}
	m := downloadingModel(t, f)
	m = send(m, downloadDoneMsg{seq: m.seq, err: staleErr()})
	staleProbe := m.probe.InfoJSONPath

	m, cmd := step(m, keyOf(tea.KeyCtrlC))
	if quitsNow(cmd) {
		t.Fatal("ctrl+c quit while a re-probe was still running")
	}

	late := newProbe(t, "Too Late", "Nobody")
	_, cmd = step(m, probeDoneMsg{seq: m.seq, probe: late, retry: true})

	if !quitsNow(cmd) {
		t.Fatal("the re-probe reported back and the program did not quit")
	}
	if n := f.cleanups(); n != 2 {
		t.Fatalf("Cleanup called %d times, want 2: the stale result and the late one", n)
	}
	if !gone(late.InfoJSONPath) {
		t.Fatalf("the late info-json %s leaked on shutdown", late.InfoJSONPath)
	}
	if !gone(staleProbe) {
		t.Fatalf("the stale info-json %s leaked on shutdown", staleProbe)
	}
}

func TestCtrlCWithNothingRunningQuitsImmediately(t *testing.T) {
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())

	_, cmd := step(m, keyOf(tea.KeyCtrlC))

	if !quitsNow(cmd) {
		t.Fatal("ctrl+c waited although no yt-dlp run was outstanding")
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times, want 1", n)
	}
}

func TestTheShutdownScreenStaysWithinTheTerminal(t *testing.T) {
	f := &fakes{}
	m := downloadingModel(t, f)
	m, _ = step(m, keyOf(tea.KeyCtrlC))

	for _, width := range []int{80, 40, 8} {
		m = send(m, tea.WindowSizeMsg{Width: width, Height: 24})
		view := m.View()
		if got := widest(view); got > max(width, minContentWidth+4) {
			t.Errorf("the shutdown screen is %d cells wide at width %d:\n%s", got, width, view)
		}
	}
}

// --- finding 5: a scheme is a prefix, not a substring -----------------------

func TestLeadingScheme(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.com", "https"},
		{"HTTP://example.com", "HTTP"},
		{"file:///etc/passwd", "file"},
		{"ftp://example.com", "ftp"},
		{"svn+ssh://example.com", "svn+ssh"},
		{"example.com", ""},
		{"://example.com", ""},
		{"example.com:8080/x", ""},
		{"consent.youtube.com/m?continue=https://www.youtube.com/watch?v=X", ""},
		{"1http://example.com", ""},
		{"a b://example.com", ""},
	}
	for _, tc := range cases {
		if got := leadingScheme(tc.in); got != tc.want {
			t.Errorf("leadingScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestASchemeInAQueryParameterIsNotAScheme(t *testing.T) {
	// A consent or redirect link pasted without its own scheme. Reading the
	// "://" in the query as a scheme sent this down the http-or-https branch,
	// found neither, and told the user their URL was not one.
	const pasted = "consent.youtube.com/m?continue=https://www.youtube.com/watch?v=dQw4w9WgXcQ"

	got, ok := normaliseURL(pasted)
	if !ok {
		t.Fatalf("normaliseURL(%q) refused a URL the https:// prefix fixes", pasted)
	}
	if got != "https://"+pasted {
		t.Fatalf("normaliseURL(%q) = %q, want it prefixed", pasted, got)
	}

	f := &fakes{probes: []probeOutcome{{probe: newProbe(t, "A", "B")}}}
	m := testModel(t, f, 80)
	m.hasBin = true
	m = typeURL(m, pasted)
	m = send(m, keyOf(tea.KeyEnter))
	if m.state != stateProbing {
		t.Fatalf("state = %v, want the consent link to be accepted; hint was %q", m.state, m.hint)
	}
}

func TestRunProgramReleasesWhateverTheLoopEndedOn(t *testing.T) {
	// The shutdown that skips Update entirely: the context ends, Bubble Tea
	// returns the live model beside ErrProgramKilled, and the info-json is
	// nobody's but this function's.
	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	infoJSON := m.probe.InfoJSONPath

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutSignals(),
		tea.WithoutRenderer(),
	)

	err := runProgram(p)

	if !errors.Is(err, tea.ErrProgramKilled) {
		t.Fatalf("runProgram err = %v, want it killed by the cancelled context", err)
	}
	if n := f.cleanups(); n != 1 {
		t.Fatalf("Cleanup called %d times after the program was killed, want 1", n)
	}
	if !gone(infoJSON) {
		t.Fatalf("the info-json %s survived the program being killed", infoJSON)
	}
}

// --- finding 6: package init must not touch the terminal --------------------

// styleBuildsAtInit is styleBuilds as it stood when this test package finished
// initialising — which is after internal/ui's own initialisation and before any
// test has run. Reading it later is the only way to ask "did package init build
// a style?", because by then the first View has built one and the counter has
// moved.
var styleBuildsAtInit = styleBuilds.Load()

// TestPackageInitBuildsNoStyle is the assertion behind styles being a function.
//
// Package initialisation runs before main, and therefore before main installs a
// signal handler: a signal arriving while an init is talking to the terminal
// kills the process by its default disposition. Nothing in this package may do
// that work at init, and a style is the piece of it this file owns.
//
// The counter is the evidence rather than the rendered frames, because the
// frames are required not to change at all — laziness that showed up on screen
// would be a different bug.
func TestPackageInitBuildsNoStyle(t *testing.T) {
	if styleBuildsAtInit != 0 {
		t.Errorf("package initialisation built the style set %d times; it must be built on first use, not before main", styleBuildsAtInit)
	}
}

// TestStylesAreBuiltOnceOnFirstUse checks the other half: first use builds the
// set and nothing builds it again. A set rebuilt per frame would satisfy the
// test above and put style construction on the render path.
//
// The counter is the whole of the evidence, and that is a limit worth writing
// down rather than papering over: whether two callers get the *same* set cannot
// be observed from here. styles() returns a styleSet by value, so the sets two
// calls hand back are copies even when the implementation is right, and under
// lipgloss v1.1.0 a Style is a plain value struct — a props bitset, ints, nil
// colours, and a pointer to the one package-level renderer. Two independently
// constructed styles are therefore reflect.DeepEqual and render byte-identical,
// while == does not compile at all (the struct holds a func field). Measured
// against a styles() deliberately rebuilt on every call: the counter below
// caught it, and DeepEqual on the whole set, on faint, and on box all still
// reported equal. A comparison of the returned values would pass whether or not
// the set is shared, so this test does not make one.
func TestStylesAreBuiltOnceOnFirstUse(t *testing.T) {
	styles()
	before := styleBuilds.Load()
	if before == 0 {
		t.Fatalf("styles() did not build the set")
	}

	styles()
	if after := styleBuilds.Load(); after != before {
		t.Errorf("styles() built the set again: %d builds, want %d", after, before)
	}
}

// --- finding 7: colour, on a real profile -----------------------------------

// sgr matches one Select Graphic Rendition sequence and captures its
// parameters.
var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// allowedSGR is every SGR parameter a style built from the terminal's own
// sixteen colours can produce: reset, bold, faint, underline, reverse and
// their offs, and the 30-37/90-97 foregrounds and 40-47/100-107 backgrounds
// with their defaults. 38 and 48 — the introducers for a 256-cube or a
// truecolor value — are the ones the palette rule forbids, and they are absent
// from this set on purpose.
var allowedSGR = func() map[int]bool {
	ok := map[int]bool{0: true, 1: true, 2: true, 4: true, 7: true, 22: true, 24: true, 27: true, 39: true, 49: true}
	for _, base := range []int{30, 40, 90, 100} {
		for i := range 8 {
			ok[base+i] = true
		}
	}
	return ok
}()

// trueColour makes lipgloss render at the richest profile there is, which is
// where a hex or 256-cube colour would show up as one. forceColour steps down
// to whatever first emits an escape, and that is ANSI under `go test`, where a
// Color("240") is quietly degraded to a 16-colour approximation and passes.
func trueColour(t *testing.T) {
	t.Helper()
	original := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(original) })
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// colourScreens is screens plus the states that only differ by a style or by
// motion: the first-run wording, the converting phase, the done screen without
// its note, a picker with the cursor moved, the input screen at a height that
// shows the wordmark and one that collapses it, the bar one spring frame into
// a report, and the merging sweep at two consecutive ticks. Every model gets a
// bar that renders at TrueColor, because progress.New reads the real stdout's
// profile and would otherwise draw the bar plain whatever lipgloss was told.
// The bar is swapped in before the messages that move it, so the animated
// entries are animating the bar the test looks at.
func colourScreens(t *testing.T) map[string]Model {
	t.Helper()
	out := screens(t)
	for name, m := range out {
		out[name] = colourBar(m)
	}

	tall := testModel(t, &fakes{}, 80)
	tall.height = 30
	out["input tall"] = tall

	short := testModel(t, &fakes{}, 80)
	short.height = 10
	out["input short"] = short

	sprung := colourBar(downloadingModel(t, &fakes{}))
	sprung, cmd := step(sprung, progressMsg{seq: sprung.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseDownloading, Downloaded: 30_100_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true,
	}})
	sprung, _ = advance(t, sprung, cmd)
	if !sprung.bar.IsAnimating() {
		t.Fatal("one frame into a report the bar has already stopped; the spring is not running")
	}
	out["downloading after a frame"] = sprung

	sweep := colourBar(downloadingModel(t, &fakes{}))
	sweep = send(sweep, progressMsg{seq: sweep.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseMerging, Downloaded: 64_000_000, DownloadedKnown: true,
		Total: 64_000_000, TotalKnown: true,
	}})
	sweep = send(sweep, sweep.spin.Tick())
	out["merging sweep frame 1"] = sweep
	sweep = send(sweep, sweep.spin.Tick())
	out["merging sweep frame 2"] = sweep

	firstRun := testModel(t, &fakes{}, 80)
	firstRun.hasBin = true
	firstRun = typeURL(firstRun, "https://example.com/v")
	firstRun = send(firstRun, keyOf(tea.KeyEnter))
	firstRun.firstRun = true
	out["probing first run"] = firstRun

	moved := pickerModel(t, &fakes{}, true, threeRows())
	moved = send(moved, runes("j"))
	out["picker cursor moved"] = moved

	converting := downloadingModel(t, &fakes{})
	converting = send(converting, progressMsg{seq: converting.seq, p: ytdlp.Progress{
		Phase: ytdlp.PhaseConverting, Downloaded: 4_000_000, DownloadedKnown: true,
		Total: 4_000_000, TotalKnown: true,
	}})
	out["converting"] = converting

	done := downloadingModel(t, &fakes{})
	done = send(done, downloadDoneMsg{seq: done.seq, res: &ytdlp.DownloadResult{Path: "/Users/x/Downloads/a b.mp4"}})
	out["done without note"] = done

	// The input screen with motion, at three moments: mid-intro, with the
	// site badge fading in and the shimmer crossing, and mid-exit. Every
	// frame of every effect is measured against the same rule in
	// TestEveryMotionFrameFitsTheTerminalAndThePalette; these keep the
	// animated screen in the one table every screen is held to.
	intro := runMotion(t, motionModel(t, &fakes{}, 80, 24), 300*time.Millisecond, nil)
	out["input motion mid-intro"] = intro
	// The times are literals, not the effects' constants, so deleting an
	// effect never breaks this file.
	badge := runMotion(t, motionModel(t, &fakes{}, 80, 24), 4*time.Second, nil)
	badge.input.SetValue("https://www.youtube.com/watch?v=x")
	badge = send(badge, runes("y"))
	badge = runMotion(t, badge, 4300*time.Millisecond, nil)
	out["input motion badge and shimmer"] = badge
	exit := badge
	exit.hasBin = true
	exit = send(exit, keyOf(tea.KeyEnter))
	exit = runMotion(t, exit, clock(exit)+100*time.Millisecond, nil)
	out["input motion mid-exit"] = exit

	for _, name := range []string{"probing first run", "picker cursor moved", "converting", "done without note"} {
		out[name] = colourBar(out[name])
	}
	return out
}

// colourBar gives m a bar that renders at TrueColor; see colourScreens.
func colourBar(m Model) Model {
	m.bar = newBar(progress.WithColorProfile(termenv.TrueColor))
	return m
}

func TestEveryScreenUsesOnlyTheTerminalsOwnPalette(t *testing.T) {
	trueColour(t)

	for _, width := range []int{80, 120, 24, 20} {
		for name, m := range colourScreens(t) {
			// The height is the model's own: the two input entries differ
			// only by it, and a resize that reset it would render the same
			// screen twice.
			m = send(m, tea.WindowSizeMsg{Width: width, Height: m.height})
			view := m.View()
			if !strings.ContainsRune(view, escape) {
				t.Fatalf("%s at width %d rendered no escape sequence at TrueColor; the profile is not in effect and this test can see nothing", name, width)
			}
			for n, line := range strings.Split(view, "\n") {
				assertLineIsPaletteSafe(t, name, width, n, line)
			}
		}
	}
}

// assertLineIsPaletteSafe is the palette rule and the fit rule for one rendered
// line: only the allowed SGR parameters, no escape sequence that is not an SGR,
// no wider than the terminal once the sequences are discounted, and a reset
// closing whatever was opened so nothing bleeds into the line below.
func assertLineIsPaletteSafe(t *testing.T, name string, width, n int, line string) {
	t.Helper()
	where := name + " at width " + strconv.Itoa(width) + " line " + strconv.Itoa(n)

	matches := sgr.FindAllStringSubmatch(line, -1)
	for _, m := range matches {
		for _, param := range strings.Split(m[1], ";") {
			if param == "" {
				continue // ESC[m is a reset
			}
			v, err := strconv.Atoi(param)
			if err != nil || !allowedSGR[v] {
				t.Errorf("%s carries SGR parameter %q in %q, outside the sixteen-colour palette:\n%q", where, param, m[0], line)
			}
		}
	}
	if stripped := sgr.ReplaceAllString(line, ""); strings.ContainsRune(stripped, escape) {
		t.Errorf("%s carries an escape sequence that is not an SGR:\n%q", where, line)
	}
	if got := lipgloss.Width(line); got > width {
		t.Errorf("%s is %d cells wide:\n%q", where, got, line)
	}
	if len(matches) > 0 {
		last := matches[len(matches)-1][1]
		if last != "" && last != "0" {
			t.Errorf("%s opens a style it does not close; the last SGR is %q:\n%q", where, matches[len(matches)-1][0], line)
		}
	}
}

func TestTheSelectedRowsReverseBlockSpansTheContentWidth(t *testing.T) {
	trueColour(t)

	f := &fakes{}
	m := pickerModel(t, f, true, threeRows())
	for _, width := range []int{80, 40} {
		m = send(m, tea.WindowSizeMsg{Width: width, Height: 24})
		cw := m.contentWidth()

		// The unstyled text first: it is what the style is applied to.
		lines := pickerLines(m.rows, m.cursor, cw)
		if got := lipgloss.Width(lines[m.cursor]); got != cw {
			t.Errorf("at width %d the selected row's text is %d cells, want %d: %q", width, got, cw, lines[m.cursor])
		}
		for i, line := range lines {
			if i != m.cursor && lipgloss.Width(line) > cw {
				t.Errorf("at width %d row %d is %d cells, wider than %d: %q", width, i, lipgloss.Width(line), cw, line)
			}
		}

		// Then the rendered frame: the cells between the reverse being switched
		// on and the reset must be the same count, or the block stops short.
		reversed := regexp.MustCompile(`\x1b\[[0-9;]*7[0-9;]*m(.*?)\x1b\[0?m`).FindStringSubmatch(m.View())
		if reversed == nil {
			t.Fatalf("at width %d no reverse-video block in:\n%q", width, m.View())
		}
		if got := lipgloss.Width(reversed[1]); got != cw {
			t.Errorf("at width %d the reverse block covers %d cells, want %d: %q", width, got, cw, reversed[1])
		}
	}
}

func TestUnselectedRowNumbersAreFaintAndNothingElseMoves(t *testing.T) {
	trueColour(t)

	m := pickerModel(t, &fakes{}, true, threeRows())
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	cw := m.contentWidth()
	plainRows := pickerLines(m.rows, m.cursor, cw)

	faint := regexp.MustCompile(`^  \x1b\[2m([1-9]\. )\x1b\[0?m`)
	seen := 0
	for _, line := range strings.Split(m.View(), "\n") {
		line = strings.TrimPrefix(line, "  ") // the doc margin
		plain := sgr.ReplaceAllString(line, "")
		for i, row := range plainRows {
			if i == m.cursor || strings.TrimRight(plain, " ") != strings.TrimRight(row, " ") {
				continue
			}
			seen++
			// The number cell is faint and closed again before the row's
			// text begins, so the text itself stays in the terminal's own
			// foreground and the row is exactly as wide as pickerLines made it.
			sub := faint.FindStringSubmatch(line)
			if sub == nil || sub[1] != rowNumber(i) {
				t.Errorf("row %d does not open with its number cell in faint: %q", i, line)
			}
			if rest := line[len(sub[0]):]; strings.ContainsRune(rest, escape) {
				t.Errorf("row %d carries styling past its number cell: %q", i, line)
			}
			// Trimmed, because the doc style pads every line of the frame
			// to the block's width; the text before the padding is the row.
			if got := lipgloss.Width(strings.TrimRight(line, " ")); got != lipgloss.Width(strings.TrimRight(row, " ")) {
				t.Errorf("row %d is %d cells styled and %d plain: %q", i, got, lipgloss.Width(row), line)
			}
		}
	}
	if want := len(plainRows) - 1; seen != want {
		t.Fatalf("found %d unselected rows in the frame, want %d:\n%q", seen, want, m.View())
	}
}

func TestDoneAndErrorScreensFrameTheirText(t *testing.T) {
	trueColour(t)

	done := downloadingModel(t, &fakes{})
	done = send(done, downloadDoneMsg{seq: done.seq, res: &ytdlp.DownloadResult{Path: "/Users/x/Downloads/a b.mp4"}})
	failed := downloadingModel(t, &fakes{})
	failed = send(failed, downloadDoneMsg{seq: failed.seq, err: hardErr()})

	// framed reports whether text sits on a line between two box sides, with
	// the box's top edge somewhere above it and its bottom edge somewhere
	// below, every row between being a row of the same box. The text may be
	// on any row of the box: the error wraps and the path is cut.
	framed := func(view, text string) bool {
		var plain []string
		for _, line := range strings.Split(view, "\n") {
			plain = append(plain, strings.TrimSpace(sgr.ReplaceAllString(line, "")))
		}
		side := func(n int) bool {
			return n >= 0 && n < len(plain) && strings.HasPrefix(plain[n], "│") && strings.HasSuffix(plain[n], "│")
		}
		for n, line := range plain {
			if !strings.Contains(line, text) {
				continue
			}
			if !side(n) {
				return false
			}
			top := n - 1
			for side(top) {
				top--
			}
			bottom := n + 1
			for side(bottom) {
				bottom++
			}
			return top >= 0 && strings.HasPrefix(plain[top], "╭") &&
				bottom < len(plain) && strings.HasPrefix(plain[bottom], "╰")
		}
		return false
	}
	// borderSGR is the colour the box's corner is drawn in.
	borderSGR := func(view string) string {
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "╭") {
				m := sgr.FindStringSubmatch(line)
				if m != nil {
					return m[1]
				}
			}
		}
		return ""
	}

	for _, width := range []int{80, 120, 24} {
		d := send(done, tea.WindowSizeMsg{Width: width, Height: 24})
		// The head of the path: at 24 columns the tail is cut.
		if !framed(d.View(), "/Users/x/D") {
			t.Errorf("at width %d the saved path is not framed:\n%s", width, d.View())
		}
		if got := borderSGR(d.View()); got != "32" {
			t.Errorf("at width %d the done box border is SGR %q, want 32 (ANSI green):\n%q", width, got, d.View())
		}
		e := send(failed, tea.WindowSizeMsg{Width: width, Height: 24})
		// The last word: at 24 columns the sentence wraps inside the box.
		if !framed(e.View(), "private.") {
			t.Errorf("at width %d the error text is not framed:\n%s", width, e.View())
		}
		if got := borderSGR(e.View()); got != "31" {
			t.Errorf("at width %d the error box border is SGR %q, want 31 (ANSI red):\n%q", width, got, e.View())
		}
	}

	// One column narrower than a frame is allowed at, both draw bare.
	for name, m := range map[string]Model{"done": done, "error": failed} {
		m = send(m, tea.WindowSizeMsg{Width: minBoxedWidth + 3, Height: 24})
		if strings.Contains(m.View(), "╭") {
			t.Errorf("the %s screen is framed at width %d, below the floor:\n%s", name, minBoxedWidth+3, m.View())
		}
		if got := widest(m.View()); got > minBoxedWidth+3 {
			t.Errorf("the bare %s screen is %d cells wide at width %d", name, got, minBoxedWidth+3)
		}
	}
}

func TestPickerColumnsFallBackToTheLabel(t *testing.T) {
	// A size that was never pinned down, spelled the way the Label spells it,
	// and right-aligned under the one that was.
	rows := []ytdlp.Row{
		{Label: "1080p60  mp4  ~142 MB", Height: 1080, FPS: 60, Ext: "mp4", Bytes: 142_000_000, SizeKnown: true},
		{Label: "720p  webm  ~?", Height: 720, FPS: 24, Ext: "webm"},
	}
	want := []string{
		padRight("▸ 1. 1080p60  mp4   ~142 MB", 60),
		"  2. 720p     webm       ~?",
	}
	if got := pickerLines(rows, 0, 60); !equalLines(got, want) {
		t.Errorf("pickerLines with an unknown size:\n got %q\nwant %q", got, want)
	}

	// A Label the columns cannot fit under a narrow width: every row falls
	// back to its Label, cut to the width, and the list is no wider than it
	// was before the columns existed.
	long := ytdlp.Row{
		Label:  "2160p60  " + strings.Repeat("x", 40) + "  ~9.8 GB",
		Height: 2160, FPS: 60, Ext: strings.Repeat("x", 40), Bytes: 9_800_000_000, SizeKnown: true,
	}
	rows = []ytdlp.Row{rows[0], long, rows[1]}
	const w = 20
	want = []string{
		"  1. 1080p60  mp4  " + ellipsis,
		"▸ 2. 2160p60  xxxxx" + ellipsis,
		"  3. 720p  webm  ~?",
	}
	got := pickerLines(rows, 1, w)
	if !equalLines(got, want) {
		t.Errorf("pickerLines under a width the columns cannot fit:\n got %q\nwant %q", got, want)
	}
	for i, line := range got {
		if lipgloss.Width(line) > w {
			t.Errorf("row %d is %d cells at width %d: %q", i, lipgloss.Width(line), w, line)
		}
	}
}

func TestPickerColumnsSanitiseTheContainer(t *testing.T) {
	// Ext is remote text like everything else on the row, and it is measured
	// to size its column before truncate ever sees it.
	rows := []ytdlp.Row{
		{Label: "720p  mp4  ~64 MB", Height: 720, Ext: "mp\x1b[31m4", Bytes: 64_000_000, SizeKnown: true},
		{Label: "audio only  mp3", AudioOnly: true},
	}
	want := []string{
		padRight("▸ 1. 720p        mp4  ~64 MB", 40),
		"  2. audio only  mp3",
	}
	if got := pickerLines(rows, 0, 40); !equalLines(got, want) {
		t.Errorf("pickerLines with a hostile container:\n got %q\nwant %q", got, want)
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
