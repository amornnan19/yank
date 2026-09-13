package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestCellWindowCutsByDisplayCell(t *testing.T) {
	cases := []struct {
		s         string
		off, w    int
		want      string
		wantWidth int
	}{
		{"abcdef", 0, 3, "abc", 3},
		{"abcdef", 2, 3, "cde", 3},
		{"abcdef", 4, 3, "ef", 2},
		{"日本語", 0, 4, "日本", 4},
		{"日本語", 1, 4, " 本 ", 4},
		{"日本語", 1, 3, " 本", 3},
		{"a日b", 2, 2, " b", 2},
		{"étude", 0, 2, "ét", 2},
		{"étude", 1, 2, "tu", 2},
		// Clusters, not runes: a heart with its variation selector, a ZWJ
		// family and a flag are each one character.
		{"\u2764\ufe0f\u2764\ufe0f\u2764\ufe0f", 0, 4, "\u2764\ufe0f\u2764\ufe0f", 4},
		{"\u2764\ufe0f\u2764\ufe0f\u2764\ufe0f", 1, 4, " \u2764\ufe0f ", 4},
		{"\u2764\ufe0f\u2764\ufe0f\u2764\ufe0f", 4, 4, "\u2764\ufe0f", 2},
		{"a\U0001f468\u200d\U0001f469\u200d\U0001f467b", 1, 3, "\U0001f468\u200d\U0001f469\u200d\U0001f467b", 3},
		{"a\U0001f468\u200d\U0001f469\u200d\U0001f467b", 2, 2, " b", 2},
		{"\U0001f1f9\U0001f1ed\U0001f1ef\U0001f1f5", 2, 2, "\U0001f1ef\U0001f1f5", 2},
	}
	for _, tc := range cases {
		got := cellWindow(tc.s, tc.off, tc.w)
		if got != tc.want || lipgloss.Width(got) != tc.wantWidth {
			t.Errorf("cellWindow(%q, %d, %d) = %q (%d cells), want %q (%d)", tc.s, tc.off, tc.w, got, lipgloss.Width(got), tc.want, tc.wantWidth)
		}
	}
}

func TestTitleMarqueeScrollsATitleThatDoesNotFit(t *testing.T) {
	const cw = 40
	title := sanitise(longTitle)
	travel := lipgloss.Width(title) - cw
	r := rig(newTitleMarquee)
	r.dl.title = longTitle
	r.send(evShown)
	if !r.idle() {
		t.Fatalf("the marquee asks for ticks of its own; it rides the spinner")
	}
	window := func() string {
		f := r.mo.paintDownload(downloadFrame{cw: cw, title: title})
		if !f.titleSet {
			t.Fatalf("a title %d cells wide in %d is not scrolled", lipgloss.Width(title), cw)
		}
		if w := lipgloss.Width(f.titleText); w > cw {
			t.Fatalf("the window is %d cells, over %d: %q", w, cw, f.titleText)
		}
		return f.titleText
	}
	if got := window(); got != cellWindow(title, 0, cw) {
		t.Errorf("before the first tick the window is %q, want the start", got)
	}
	r.tickAt(0)
	last, reachedEnd, restarted := 0, false, false
	for at := time.Duration(0); at < 3*(marqueeHold+marqueeHoldEnd+time.Duration(travel)*time.Second/marqueeSpeed); at += spinPeriod {
		r.tickAt(at)
		got := window()
		off := strings.Index(title, got)
		if off < 0 {
			t.Fatalf("at %v the window %q is not a piece of the title", at, got)
		}
		switch {
		case at < marqueeHold && off != 0:
			t.Errorf("at %v, inside the hold, the window has moved to %d", at, off)
		case off < last:
			if off != 0 || last != travel {
				t.Errorf("at %v the window went back from %d to %d without finishing", at, last, off)
			}
			restarted = true
		case off-last > 1:
			t.Errorf("at %v the window jumped %d cells in one spinner tick", at, off-last)
		}
		reachedEnd = reachedEnd || off == travel
		last = off
	}
	if !reachedEnd || !restarted {
		t.Errorf("the marquee reached the end %v and started over %v", reachedEnd, restarted)
	}

	// A title that fits does not move.
	short := rig(newTitleMarquee)
	short.dl.title = "Me at the zoo"
	short.send(evShown)
	for at := time.Duration(0); at < 10*time.Second; at += time.Second {
		short.tickAt(at)
		if f := short.mo.paintDownload(downloadFrame{cw: cw, title: "Me at the zoo"}); f.titleSet {
			t.Errorf("at %v a title that fits was scrolled to %q", at, f.titleText)
		}
	}

	// A new title starts from its beginning.
	r.tickAt(time.Minute + marqueeHold + time.Second)
	r.dl.title = longTitle + " (2)"
	r.send(evUpdate)
	r.tickAt(time.Minute + marqueeHold + 2*time.Second)
	if got := r.mo.paintDownload(downloadFrame{cw: cw, title: sanitise(r.dl.title)}).titleText; !strings.HasPrefix(sanitise(r.dl.title), got) {
		t.Errorf("a changed title did not start from its beginning: %q", got)
	}
}

func TestTheMarqueeReachesTheLastClusterOfAnEmojiTitle(t *testing.T) {
	for name, tc := range map[string]struct {
		title string
		cw    int
		// end is what the last full window must end with, when it is more
		// than the title's last cluster.
		end string
	}{
		// Counted per rune a heart is one cell and draws as two, so a rune
		// count understates the travel and the end never shows.
		"hearts": {title: strings.Repeat("\u2764\ufe0f", 50), cw: 40},
		// Twenty-five hearts count as 25 cells rune by rune and draw as 50: a
		// rune count says the title fits and it never scrolls.
		"hearts that look like they fit": {title: strings.Repeat("\u2764\ufe0f", 25), cw: 40},
		// A family counts as six cells rune by rune and draws as two, so a rune
		// count scrolls the window off the end of the title.
		"zwj families":      {title: "The family " + strings.Repeat("\U0001f468\u200d\U0001f469\u200d\U0001f467", 20) + " end", cw: 40},
		"flags and accents": {title: strings.Repeat("\U0001f1f9\U0001f1ed cafe\u0301 ", 8) + "fin", cw: 40},
		// Split the way an older segmenter splits them, the conjuncts measure a
		// cell wider apiece than lipgloss.Width says of the title, so the
		// window runs out of cells before the travel does and END never shows.
		"indic conjuncts": {title: "\u0928\u092e\u0938\u094d\u0924\u0947 \u0926\u0941\u0928\u093f\u092f\u093e \u0915\u094d\u0937\u092e\u093e END", end: "END", cw: 10},
	} {
		t.Run(name, func(t *testing.T) {
			title, cw := tc.title, tc.cw
			clusters := clustersOf(title)
			last := clusters[len(clusters)-1]
			if tc.end != "" {
				last = tc.end
			}
			travel := lipgloss.Width(title) - cw
			r := rig(newTitleMarquee)
			r.dl.title = title
			r.send(evShown)
			r.tickAt(0)
			reachedEnd := false
			for at := time.Duration(0); at < 2*(marqueeHold+marqueeHoldEnd+time.Duration(travel)*time.Second/marqueeSpeed); at += spinPeriod {
				r.tickAt(at)
				f := r.mo.paintDownload(downloadFrame{cw: cw, title: title})
				if !f.titleSet {
					t.Fatalf("a title %d cells wide in %d is not scrolled", lipgloss.Width(title), cw)
				}
				w := lipgloss.Width(f.titleText)
				if w > cw {
					t.Fatalf("at %v the window is %d cells, over %d: %q", at, w, cw, f.titleText)
				}
				if strings.TrimSpace(f.titleText) == "" {
					t.Fatalf("at %v the window has scrolled past the end of the title", at)
				}
				// The window is whole clusters of the title, bar the spaces
				// standing in for one cut by an edge.
				for _, c := range clustersOf(f.titleText) {
					if c != " " && !slices.Contains(clusters, c) {
						t.Fatalf("at %v the window %q carries %q, a piece of a cluster", at, f.titleText, c)
					}
				}
				if strings.HasSuffix(f.titleText, last) && w == cw {
					reachedEnd = true
				}
			}
			if !reachedEnd {
				t.Errorf("the window never showed the title's last cluster %q in a full %d cells", last, cw)
			}
		})
	}
}

func TestTheMarqueeIsSanitisedCutAndFitsEveryFrame(t *testing.T) {
	trueColour(t)
	hostile := "evil\x1b]0;pwned\x07 title\nwith a newline, \x1b[31mred\x1b[0m and 日本語のタイトル " + longTitle
	for _, width := range []int{80, 40, 24} {
		m, _ := downloadMotion(t, hostile, width, 24, newMotionOf([]effect{newTitleMarquee()}, 1))
		scrolled := false
		for at := time.Duration(0); at < 30*time.Second; at += spinPeriod {
			m = spinAt(m, at)
			view := m.View()
			lines := strings.Split(view, "\n")
			for n, line := range lines {
				assertLineIsPaletteSafe(t, "marquee at "+at.String(), width, n, line)
			}
			if len(lines) != strings.Count(staticView(m), "\n")+1 {
				t.Fatalf("at %v the screen is %d rows, not the static screen's", at, len(lines))
			}
			titleRow := sgr.ReplaceAllString(lines[4], "")
			if strings.ContainsAny(titleRow, "\x1b\x07\n") {
				t.Fatalf("at %v the title row carries a control character: %q", at, titleRow)
			}
			// The window is cut from the sanitised title, so what shows is a
			// piece of it; a window cut from the raw title and cleaned after
			// shows pieces of the escape sequences instead.
			if piece := strings.Trim(strings.TrimSpace(titleRow), ellipsis); !strings.Contains(sanitise(hostile), piece) {
				t.Fatalf("at %v the title row %q is not a piece of the sanitised title", at, piece)
			}
			if at > marqueeHold+time.Second && !strings.HasPrefix(strings.TrimSpace(titleRow), "evil") {
				scrolled = true
			}
		}
		if !scrolled {
			t.Errorf("at width %d the long title never scrolled mid-way", width)
		}
	}
}
