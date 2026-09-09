package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
// style-then-truncate. The profile value is stepped down from the one lipgloss
// already hands out rather than named, because naming it would mean importing
// termenv, and CLAUDE.md allows this module three third-party dependencies.
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

	var selected, unselected string
	for _, line := range strings.Split(m.View(), "\n") {
		switch {
		case strings.Contains(line, selectedMarker+"1."):
			selected = line
		case strings.Contains(line, unselectedMarker+"2."):
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
	// Same visible width as its neighbour: a leading escape sequence eaten out
	// of the truncation budget shortens the row it is on.
	if lipgloss.Width(selected) != lipgloss.Width(unselected) {
		t.Errorf("selected row is %d cells and its neighbour %d; the markers are the same width so the rows must be too\n%q\n%q",
			lipgloss.Width(selected), lipgloss.Width(unselected), selected, unselected)
	}
}

func TestPickerRowTextIsPlainAndTheSameWidthEitherWay(t *testing.T) {
	// Colour is forced because the assertion below is that pickerRowText does
	// not style: at the Ascii profile a Render inside it would be the identity
	// and there would be nothing to see.
	forceColour(t)

	rows := threeRows()
	// Narrow enough that both rows are cut; a row that fits is not evidence
	// about how a cut one is built.
	const w = 20
	// Asserted on the raw value, before truncate: truncate sanitises, so it
	// would strip a stray escape sequence and hide the very thing under test.
	for i, raw := range []string{pickerRowText(0, true, rows[0].Label), pickerRowText(1, false, rows[1].Label)} {
		if strings.ContainsRune(raw, escape) {
			t.Errorf("pickerRowText(%d) = %q is already styled; styling is fit's job, after the cut", i, raw)
		}
	}

	sel := truncate(pickerRowText(0, true, rows[0].Label), w)
	unsel := truncate(pickerRowText(1, false, rows[1].Label), w)
	if lipgloss.Width(sel) != w {
		t.Errorf("the selected row is %d cells, want the full %d", lipgloss.Width(sel), w)
	}
	if lipgloss.Width(sel) != lipgloss.Width(unsel) {
		t.Errorf("cut rows differ in width: %d vs %d", lipgloss.Width(sel), lipgloss.Width(unsel))
	}
}

func TestFitCutsBeforeItStyles(t *testing.T) {
	forceColour(t)

	long := strings.Repeat("a", 40)
	got := fit(selectedStyle, long, 20)

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
