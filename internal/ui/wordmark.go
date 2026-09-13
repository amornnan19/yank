package ui

import "github.com/charmbracelet/lipgloss"

// wordmarkRows is the word "yank" drawn in block letters, one string per
// terminal row, for the input screen to open on. It is a drawing of the name,
// not a second spelling of it: appName is still the string every other screen
// prints, and the strings the screens say are not changed by this.
//
// Each cell is one of the half-block characters, so every row is the same
// width in cells and no row needs measuring at render time. Hand-drawn, so
// there is no figlet dependency and nothing here is loaded from a file.
var wordmarkRows = []string{
	"▄   ▄   ▄▄▄   ▄▄▄▄   █    ",
	"█   █   ▄▄▄█  █   █  █ ▄▀ ",
	"▀▄▄▄█  █   █  █   █  █▀▄  ",
	"▄▄▄▄▀   ▀▀▀▀  ▀   ▀  ▀  ▀ ",
}

// minWordmarkHeight is the shortest terminal that gets the wordmark. Below it
// the rows the drawing takes push the input box and the key legend off the
// bottom, so the one-line header is drawn instead, exactly as it was before
// the wordmark existed.
const minWordmarkHeight = 12

// wordmarkWidth is the widest row of the drawing in cells. The rows are all
// the same width, but the widest is what fitting has to answer for.
func wordmarkWidth() int {
	w := 0
	for _, row := range wordmarkRows {
		w = max(w, lipgloss.Width(row))
	}
	return w
}

// wordmarkFits reports whether the input screen has room for the drawing: a
// known height of at least minWordmarkHeight, and a content width at least as
// wide as the drawing. The height is unknown until the first
// tea.WindowSizeMsg, and unknown collapses, so the very first frame is the
// one-line header rather than a drawing that may not fit.
func (m Model) wordmarkFits() bool {
	return m.height >= minWordmarkHeight && m.contentWidth() >= wordmarkWidth()
}

// wordmarkView is the drawing in the app style. Each row goes through fit like
// any other line: wordmarkFits has already promised the width, and fit is the
// one spelling of "cut, then style" this package uses.
func wordmarkView(cw int) string {
	lines := make([]string, len(wordmarkRows))
	for i, row := range wordmarkRows {
		lines[i] = fit(styles().app, row, cw)
	}
	return join(lines...)
}

// wordmarkLetters is the column span [from, to) of each letter of the drawing,
// y a n k in order. Every drawn cell of wordmarkRows lies inside one of them;
// the motion that moves a letter moves exactly these columns.
var wordmarkLetters = [][2]int{{0, 5}, {7, 12}, {14, 19}, {21, 25}}
