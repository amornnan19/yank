package ui

import (
	"strings"
	"testing"
	"time"
)

func TestSiteBadgeNamesTheHostAndFadesIn(t *testing.T) {
	cases := []struct {
		value, badge, colour string
	}{
		{"https://www.youtube.com/watch?v=x", "▶ youtube.com", ansiRed},
		{"youtu.be/abc", "▶ youtu.be", ansiRed},
		{"https://music.youtube.com/x", "▶ music.youtube.com", ansiRed},
		{"soundcloud.com/a/b", "♪ soundcloud.com", ansiYellow},
		{"https://vimeo.com/1", "↗ vimeo.com", ansiBlue},
		{"http://EXAMPLE.org:8080/v", "↗ example.org", ansiBlue},
	}
	for _, tc := range cases {
		r := rig(newSiteBadge)
		r.value = tc.value
		r.send(evEdit)
		f := r.frame(76)
		if f.badge != tc.badge || f.badgeColour != tc.colour {
			t.Errorf("%q: badge %q in %q, want %q in %q", tc.value, f.badge, f.badgeColour, tc.badge, tc.colour)
		}
		if !f.badgeFaint || !r.effect().busy() {
			t.Errorf("%q: a new badge is not fading in", tc.value)
		}
		r.tickAt(0)
		r.tickAt(badgeFade / 2)
		if !r.frame(76).badgeFaint {
			t.Errorf("%q: the badge is not faint mid-fade", tc.value)
		}
		r.tickAt(badgeFade)
		if r.frame(76).badgeFaint || !r.idle() {
			t.Errorf("%q: the badge is still fading after %v", tc.value, badgeFade)
		}
		if f.border != ansiCyan {
			t.Errorf("%q: a URL turned the border %q", tc.value, f.border)
		}
		// The same host again does not fade in again.
		r.value = tc.value + "y"
		r.send(evEdit)
		if !r.idle() {
			t.Errorf("%q: editing within the same host restarted the fade", tc.value)
		}
	}

	for _, bad := range []string{"not a url", "file:///etc/passwd", "youtube", "ftp://x.y/z"} {
		r := rig(newSiteBadge)
		r.value = bad
		r.send(evEdit)
		f := r.frame(76)
		if f.badge != "" || f.border != ansiYellow {
			t.Errorf("%q: badge %q, border %q; want no badge and a yellow border", bad, f.badge, f.border)
		}
	}
	empty := rig(newSiteBadge)
	empty.send(evShown)
	if f := empty.frame(76); f.badge != "" || f.border != ansiCyan {
		t.Errorf("an empty box got badge %q and border %q", f.badge, f.border)
	}
}

func TestSiteBadgeIsCutAndSanitisedBeforeItIsStyled(t *testing.T) {
	forceColour(t)
	long := "https://" + strings.Repeat("a", 120) + ".example.com/v"
	m := motionModel(t, &fakes{}, 40, 24, newSiteBadge)
	m.input.SetValue(long)
	m = send(m, runes("x"))
	view := m.View()
	line := ""
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(sgr.ReplaceAllString(l, ""), "↗") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no badge on screen:\n%s", view)
	}
	if !strings.Contains(line, ellipsis) {
		t.Errorf("a host wider than the terminal was not cut: %q", line)
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "\x1b[0m") {
		t.Errorf("the badge line does not end in a reset, so it was cut after styling: %q", line)
	}
	if widest(view) > 40 {
		t.Errorf("the screen is %d cells wide at 40:\n%s", widest(view), view)
	}

	// A host carrying an escape reaches the badge only through fit.
	hostile := siteBadge{host: "evil\x1b[2Jhost.com"}
	f := inputFrame{border: ansiCyan}
	hostile.paint(&f)
	if got := fit(styles().faint, f.badge, 40); strings.Contains(sgr.ReplaceAllString(got, ""), "\x1b") {
		t.Errorf("the badge let an escape through: %q", got)
	}
}

func TestTheBadgeShowsOnTheShortestTerminalItFits(t *testing.T) {
	const url = "https://www.youtube.com/watch?v=x"
	const badge = "▶ youtube.com"
	screen := func(height int) (views []string) {
		m := motionModel(t, &fakes{}, 80, height)
		m.input.SetValue(url)
		m = send(m, runes("y"))
		views = append(views, m.View())
		runMotion(t, m, 3*time.Second, func(m Model) { views = append(views, m.View()) })
		return views
	}

	// The rows the screen with the badge takes, measured on a terminal with
	// room to spare: everything down to the legend, and the padding under it.
	tall := screen(40)
	last := strings.Split(tall[len(tall)-1], "\n")
	if !strings.Contains(strings.Join(last, "\n"), badge) {
		t.Fatalf("no badge on a 40-row terminal:\n%s", strings.Join(last, "\n"))
	}
	need := lineContaining(strings.Join(last, "\n"), "ctrl+c") + 2
	if need-1 < minWordmarkHeight {
		t.Fatalf("the screen with the badge needs %d rows, too few to test the row below it", need)
	}

	for height := need - 1; height <= need+3; height++ {
		shown := false
		for _, view := range screen(height) {
			if n := strings.Count(view, "\n") + 1; n > height {
				t.Fatalf("at height %d a frame is %d rows:\n%s", height, n, view)
			}
			shown = shown || strings.Contains(view, badge)
		}
		if want := height >= need; shown != want {
			t.Errorf("at height %d the badge showed = %v, want %v (the screen with it is %d rows)", height, shown, want, need)
		}
	}
}
