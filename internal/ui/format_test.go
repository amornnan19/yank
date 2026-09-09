package ui

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
