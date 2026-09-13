package ui

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestNormaliseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://www.youtube.com/watch?v=abc", "https://www.youtube.com/watch?v=abc", true},
		{"  https://youtu.be/abc  ", "https://youtu.be/abc", true},
		{"http://example.com/a.mp4", "http://example.com/a.mp4", true},
		{"HTTPS://Example.COM/a", "HTTPS://Example.COM/a", true},
		{"youtube.com/watch?v=abc", "https://youtube.com/watch?v=abc", true},
		{"vimeo.com/12345", "https://vimeo.com/12345", true},
		{"", "", false},
		{"    ", "", false},
		{"hello", "", false},
		{"rick astley never gonna", "", false},
		{"https://example.com/a b", "", false},
		{"dQw4w9WgXcQ", "", false},
		{"file:///etc/passwd", "", false},
		{"ftp://example.com/a", "", false},
		{"https://", "", false},
		{"https:///path", "", false},
	}
	for _, tc := range cases {
		got, ok := normaliseURL(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("normaliseURL(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestURLHintSaysWhichProblemItIs(t *testing.T) {
	empty := urlHint("   ")
	notURL := urlHint("rick astley")
	if empty == notURL {
		t.Fatalf("an empty box and a search phrase both read %q", empty)
	}
	if !strings.Contains(empty, "paste") {
		t.Errorf("hint for an empty box = %q", empty)
	}
	if !strings.Contains(notURL, "does not look like a URL") {
		t.Errorf("hint for a phrase = %q", notURL)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel" + ellipsis},
		{"hello", 1, ellipsis},
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"", 5, ""},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.w); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}

func TestTruncateMeasuresDisplayWidthNotRunes(t *testing.T) {
	// Each of these is two cells wide. Six of them is twelve cells, so a
	// rune-counting truncate would let this through at a width of ten and the
	// layout would come apart on the terminal it was measured for.
	wide := strings.Repeat("世", 6)
	got := truncate(wide, 10)
	if lipgloss.Width(got) > 10 {
		t.Fatalf("truncate(%q, 10) = %q, %d cells wide", wide, got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("truncate(%q, 10) = %q, want it marked as cut", wide, got)
	}
}

// clusterTitles are strings whose display width a rune-by-rune count gets
// wrong, or which a rune-by-rune cut splits in the middle of a character.
var clusterTitles = map[string]string{
	"heart":      strings.Repeat("\u2764\ufe0f", 30),
	"zwj family": strings.Repeat("\U0001f468\u200d\U0001f469\u200d\U0001f467", 20),
	"cjk":        strings.Repeat("日本語のタイトル", 5),
	"combining":  strings.Repeat("e\u0301tude ", 10),
	"flag":       strings.Repeat("\U0001f1f9\U0001f1ed", 20),
	// A consonant, a virama and a consonant are one conjunct cluster under
	// Unicode 15.1's GB9c, which lipgloss.Width's segmenter has and an older
	// one does not: split there, the halves measure a cell wider than the
	// whole.
	"devanagari": strings.Repeat("\u0928\u092e\u0938\u094d\u0924\u0947 \u0915\u094d\u0937\u092e\u093e ", 6),
	"bengali":    strings.Repeat("\u0995\u09cd\u09b7 \u09b8\u09cd\u09a4\u09cb\u09a4\u09cd\u09b0 ", 6),
	// lipgloss.Width counts the ASCII 1 as a cell before it reaches the rest
	// of the keycap, so it says one cell where x/ansi's cluster width says two.
	"keycap": strings.Repeat("1\ufe0f\u20e3 ", 10),
	"mixed":  "Rick \u2764\ufe0f \U0001f468\u200d\U0001f469\u200d\U0001f467 in 東京 \U0001f1f9\U0001f1ed, cafe\u0301 " + strings.Repeat("\U0001f44d\U0001f3fd", 8),
}

// clustersOf is s split by the segmenter lipgloss.Width measures with,
// independently of the helper under test.
func clustersOf(s string) []string {
	var out []string
	for s != "" {
		c, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		out = append(out, c)
		s = s[len(c):]
	}
	return out
}

func TestGraphemesAddUpToLipglossWidth(t *testing.T) {
	for name, s := range clusterTitles {
		sum, joined := 0, ""
		for c, w := range graphemes(s) {
			sum += w
			joined += c
		}
		if want := lipgloss.Width(s); sum != want {
			t.Errorf("%s: the clusters of %q add up to %d cells, lipgloss.Width says %d", name, s, sum, want)
		}
		if joined != s {
			t.Errorf("%s: the clusters of %q join back up to %q", name, s, joined)
		}
	}
}

func TestTruncateCutsWholeGraphemeClusters(t *testing.T) {
	for name, s := range clusterTitles {
		t.Run(name, func(t *testing.T) {
			clusters := clustersOf(s)
			full := lipgloss.Width(s)
			for w := 1; w <= full+1; w++ {
				got := truncate(s, w)
				if gw := lipgloss.Width(got); gw > w {
					t.Fatalf("truncate(_, %d) is %d cells wide: %q", w, gw, got)
				}
				if w >= full {
					if got != s {
						t.Fatalf("truncate(_, %d) cut a string %d cells wide: %q", w, full, got)
					}
					continue
				}
				kept, ok := strings.CutSuffix(got, ellipsis)
				if !ok {
					t.Fatalf("truncate(_, %d) = %q is not marked as cut", w, got)
				}
				// What is kept is the first k clusters of s exactly: a cut
				// inside a cluster leaves a piece that is not one of them.
				k := clustersOf(kept)
				if len(k) > len(clusters) || strings.Join(clusters[:len(k)], "") != kept {
					t.Fatalf("truncate(_, %d) kept %q, which is not whole clusters of %q", w, kept, s)
				}
				// And it is not cut shorter than it has to be.
				if next := lipgloss.Width(clusters[len(k)]); lipgloss.Width(kept)+next <= w-1 {
					t.Errorf("truncate(_, %d) = %q stopped before %q, which fits", w, got, clusters[len(k)])
				}
			}
		})
	}

	// The example the issue measured: thirty hearts in twenty cells.
	if got := truncate(strings.Repeat("\u2764\ufe0f", 30), 20); lipgloss.Width(got) != 19 {
		t.Errorf("thirty hearts cut to 20 cells are %d cells: %q", lipgloss.Width(got), got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 KB"},
		{9_950, "10 KB"},
		{1_500_000, "1.5 MB"},
		{30_100_000, "30 MB"},
		// PB is the last unit, matching internal/ytdlp's own renderer: a
		// number past it is not a download size.
		{999_999_999_999_999_999, "1000 PB"},
		{-5, "0 B"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanSpeedRefusesNonsense(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1e300} {
		if got := humanSpeed(v); got != "?/s" {
			t.Errorf("humanSpeed(%v) = %q, want the unknown marker", v, got)
		}
	}
	if got := humanSpeed(3_200_000); got != "3.2 MB/s" {
		t.Errorf("humanSpeed(3200000) = %q", got)
	}
}

func TestPercentLabel(t *testing.T) {
	if got := percentLabel(0, false); !strings.Contains(got, "--") {
		t.Errorf("percentLabel(_, false) = %q, want an unknown marker", got)
	}
	if got := percentLabel(47.03, true); strings.TrimSpace(got) != "47%" {
		t.Errorf("percentLabel(47.03, true) = %q", got)
	}
	// The known and unknown labels are the same width, so the bar beside them
	// does not change size as the site starts stating a total.
	if lipgloss.Width(percentLabel(0, false)) != lipgloss.Width(percentLabel(100, true)) {
		t.Error("the known and unknown percent labels are different widths")
	}
}
