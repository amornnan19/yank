package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	// defaultWidth is the terminal width assumed until a tea.WindowSizeMsg
	// says otherwise. Bubble Tea sends one immediately on a real terminal, so
	// this only ever renders the first frame and every frame in a test that
	// did not set a size.
	defaultWidth = 80

	// minContentWidth is the narrowest the layout is allowed to get. Below it
	// truncation would leave an ellipsis and nothing else, so the content
	// overflows the terminal instead of collapsing into punctuation.
	minContentWidth = 16

	// maxContentWidth stops the text running the full width of a wide
	// terminal, where a 200-column line is unreadable.
	maxContentWidth = 96

	// ellipsis marks a truncated string. One cell wide.
	ellipsis = "…"
)

// contentWidth is how wide one line of a screen may be, docStyle's margins
// already taken off.
func (m Model) contentWidth() int {
	w := m.width
	if w <= 0 {
		w = defaultWidth
	}
	return min(max(w-4, minContentWidth), maxContentWidth)
}

// pathWidth is how wide a file path may be. Unlike prose, a path is not capped
// at maxContentWidth: the done screen exists to hand the user that path, and
// cutting it short in a terminal wide enough to hold it makes it useless for
// the one thing it is for.
func (m Model) pathWidth() int {
	w := m.width
	if w <= 0 {
		w = defaultWidth
	}
	return max(w-4, minContentWidth)
}

// layout resizes the sub-models to the current terminal. Called on every
// tea.WindowSizeMsg and once at construction.
func (m *Model) layout() {
	cw := m.contentWidth()
	// The box costs two cells of border and two of padding, and textinput
	// wants one more for the cursor sitting past the last character.
	m.input.Width = max(4, cw-5)
	m.bar.Width = cw
}

// escape is ESC, the 7-bit introducer of every sequence a terminal executes.
const escape = 0x1b

// csi8 and st8 are the 8-bit forms of CSI and String Terminator. An extractor
// that decoded a page as latin-1 can hand these over as ordinary runes.
const (
	csi8 = 0x9b
	st8  = 0x9c
)

// sanitise removes everything in s that a terminal would act on rather than
// print.
//
// Titles, uploaders, file names and yt-dlp's own error lines all come from a
// page nobody here wrote, and they reach the screen verbatim. Three things in
// them are not text:
//
//   - An escape sequence is executed. SGR repaints, OSC can retitle the window,
//     and lipgloss.Width counts none of it, so a sequence survives truncation
//     whole and arrives at the terminal intact.
//   - A newline adds a row and a carriage return overwrites one already drawn.
//     Either desynchronises Bubble Tea's renderer from what is on screen, and
//     lipgloss.Width reports the widest *line*, so a two-line title measures as
//     though it fitted and is never truncated at all.
//   - An explicit bidirectional override reorders the rest of the line, so what
//     is on screen is not what the string says.
//
// A control character becomes one space, so the words either side of it stay
// apart. Runs of spaces are deliberately left alone: a picker row's own spacing
// is what aligns the column, and collapsing it would be a second bug.
func sanitise(s string) string {
	if !needsSanitising(s) {
		return s
	}

	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == escape || r == csi8:
			i = endOfEscape(rs, i)
		case isBidiOverride(r):
			// Zero width to begin with, so it leaves no space behind.
		case isControlRune(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsSanitising is the fast path. Almost every string rendered is already
// clean — including every literal in this package — and returning it untouched
// keeps sanitise off the allocation path of a per-frame render.
func needsSanitising(s string) bool {
	for _, r := range s {
		if isControlRune(r) || isBidiOverride(r) {
			return true
		}
	}
	return false
}

// isControlRune covers C0, DEL and C1. The 8-bit introducers live in C1 and are
// handled before this is reached.
func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// isBidiOverride reports the explicit directional formatting characters — the
// only Unicode format controls that reorder a line rather than merely joining
// or breaking it. Zero-width joiners and the like are left alone: they are part
// of ordinary text, emoji sequences among it.
func isBidiOverride(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// endOfEscape returns the index of the last rune of the escape sequence
// starting at i, or the last index of rs when the sequence never terminates —
// an unterminated OSC would otherwise leave the rest of the string to be
// swallowed by the terminal.
func endOfEscape(rs []rune, i int) int {
	last := len(rs) - 1
	j := i + 1

	kind := rune('[')
	if rs[i] == escape {
		if j > last {
			return last
		}
		kind = rs[j]
		j++
	}

	switch {
	case kind == '[':
		// CSI: parameter and intermediate bytes, then one final byte.
		for ; j <= last; j++ {
			if rs[j] >= 0x40 && rs[j] <= 0x7e {
				return j
			}
		}
	case kind == ']' || kind == 'P' || kind == 'X' || kind == '^' || kind == '_':
		// OSC, DCS, SOS, PM and APC all run to a string terminator, which is
		// BEL or ST in either of its spellings.
		for ; j <= last; j++ {
			switch {
			case rs[j] == 0x07 || rs[j] == st8:
				return j
			case rs[j] == escape && j < last && rs[j+1] == '\\':
				return j + 1
			}
		}
	case kind >= 0x20 && kind <= 0x2f:
		// ESC with intermediate bytes, e.g. the charset selector ESC ( B.
		for ; j <= last; j++ {
			if rs[j] >= 0x30 && rs[j] <= 0x7e {
				return j
			}
		}
	default:
		// ESC plus a single final byte, e.g. ESC c.
		return j - 1
	}
	return last
}

// truncate sanitises s and cuts it to at most w display cells, marking the cut
// with an ellipsis.
//
// Sanitising first is not an optimisation: measuring a string that still
// carries escapes or a newline gives a width the terminal will not agree with,
// and CLAUDE.md's rule about remote text is enforced here because this is the
// one function every remote string passes through on its way to the screen.
//
// It measures with lipgloss.Width rather than counting runes: a CJK title is
// two cells per character, and a byte or rune count would let one wrap the
// layout apart — which is the whole reason this exists.
func truncate(s string, w int) string {
	s = sanitise(s)
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return ellipsis
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + ellipsis
}

// humanBytes renders a byte count the way a download size is read: decimal
// units, because that is what "MB" means and what the file manager will say.
// It matches what the picker's own labels already show, which come out of
// internal/ytdlp — two different renderings of the same number on the same
// screen would read as two different numbers.
func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10) + " B"
	}

	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	for i, unit := range units {
		v /= 1000
		digits := 0
		// Decide the decimal by what will be printed: 9.96 asked for one
		// decimal renders "10.0", which is the decimal this format drops.
		if math.Round(v*10)/10 < 10 {
			digits = 1
		}
		if math.Round(v) >= 1000 && i < len(units)-1 {
			continue
		}
		return strconv.FormatFloat(v, 'f', digits, 64) + " " + unit
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[len(units)-1]
}

// humanSpeed renders bytes per second. A negative or non-finite rate is not a
// speed — it is yt-dlp reporting something odd — and reads as unknown.
func humanSpeed(bps float64) string {
	if math.IsNaN(bps) || math.IsInf(bps, 0) || bps < 0 || bps >= float64(math.MaxInt64) {
		return "?/s"
	}
	return humanBytes(int64(bps)) + "/s"
}

// percentLabel renders the figure beside the bar, or "--%" when either end of
// the fraction is unknown. A site that states no size is common enough that
// "0%" for the whole download would be a lie told for minutes at a time.
func percentLabel(pct float64, known bool) string {
	if !known {
		return " --%"
	}
	return fmt.Sprintf("%3.0f%%", pct)
}
