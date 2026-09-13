package ui

import (
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
)

// The palette is the terminal's own sixteen ANSI colours and nothing else.
//
// lipgloss.Color("4") does not mean one particular blue: it names slot 4 of
// the palette the user chose, which their theme has already tuned to read
// against their own background. That is what lets these styles carry colour
// and still read on a light terminal and a dark one alike. A hex colour or a
// 256-cube index is a fixed value that is only right on one of the two, and an
// AdaptiveColor queries the terminal for its background, which is startup I/O
// this package keeps out of the way on purpose. So: indices 0-15 only, and
// emphasis beyond that from bold, faint, reverse and underline, which every
// terminal renders in its own colours.
const (
	ansiRed    = "1"
	ansiGreen  = "2"
	ansiYellow = "3"
	ansiBlue   = "4"
	ansiCyan   = "6"

	// The bright half of the palette, slots 8-15, for motion: the same
	// sixteen colours the user's theme already tunes.
	ansiBrightGreen = "10"
	ansiBrightBlue  = "12"
	ansiBrightCyan  = "14"
	ansiBrightWhite = "15"
)

// styleSet is every lipgloss style the views draw with.
//
// A theme system is a later pass; these are the styles it would replace.
type styleSet struct {
	// app is the name above every screen.
	app lipgloss.Style

	// bright is the highlight the input screen's motion passes over the
	// wordmark — the shimmer band and the reveal sweep — and the core of the
	// shine that crosses the progress bar.
	bright lipgloss.Style

	// barFill, barGlint, barFlash and barFlashDim draw the progress bar's
	// cells on the animated download screen: the fill in the bar's own blue,
	// one step brighter for the leading edge and the shine's flanks, and the
	// finish flash in bright green settling to green.
	barFill     lipgloss.Style
	barGlint    lipgloss.Style
	barFlash    lipgloss.Style
	barFlashDim lipgloss.Style

	// savedBorder is the done screen's box border on its own, for the frames in
	// which it is still being traced.
	savedBorder lipgloss.Style

	// faint is for text that is there when wanted and out of the way when
	// not: the help line, hints, the uploader.
	faint lipgloss.Style

	// title is the video's title, the one thing on the screen a person
	// checks they got right.
	title lipgloss.Style

	// box frames the URL input. savedBox and failedBox frame the saved path
	// and the error text on the two ending screens, in the colour of the
	// outcome, so the frame carries the same signal as the heading above it.
	box       lipgloss.Style
	savedBox  lipgloss.Style
	failedBox lipgloss.Style

	// spinner colours the spinner on the probing screen. Like phase below it
	// is applied at render time to the one spinner model, not kept as a
	// second spinner, so there is one tick to keep in step.
	spinner lipgloss.Style

	// selected marks the picker row under the cursor. Reverse video across
	// the whole row plus bold and the "▸" marker: reverse works on a terminal
	// with no colour at all, and the marker survives one that renders no
	// attributes either.
	selected lipgloss.Style

	// phase colours the spinner on the lines where yank is doing something
	// other than downloading — merging, converting, re-fetching expired info —
	// so the transition is visible without reading the wording. The block
	// that sweeps the indeterminate bar on those lines is drawn in it too.
	phase lipgloss.Style

	// success and failure head the done and error screens. Two outcomes that
	// used to look identical at a glance.
	success lipgloss.Style
	failure lipgloss.Style

	// path is the saved file's path, the one thing the user came for.
	path lipgloss.Style

	// doc is the margin every screen sits in.
	doc lipgloss.Style
}

// styles is the one set, built on first use and reused for every frame after.
//
// It is a function rather than a package-level var because package
// initialisation runs before main, and therefore before main has installed a
// signal handler: a signal arriving while an init is querying the terminal
// kills the process by its default disposition, with none of the cleanup the
// program does on its way out. Anything that could reach the terminal is
// therefore kept out of init, and style construction is the part of that this
// package owns. Under lipgloss v1.1.0 NewStyle only records a pointer to the
// default renderer, and an ANSI index needs no query either, but a style that
// carried an AdaptiveColor would query the terminal for its background, and by
// then the shape here is the thing that decides when.
//
// It is not what closes that window today: bubbletea's own init calls
// lipgloss.HasDarkBackground() on purpose (tea_init.go), so importing this
// package still queries the terminal before main whatever these styles do.
// Closing it is a change to how the binary starts, not to how it draws.
var styles = sync.OnceValue(func() styleSet {
	styleBuilds.Add(1)
	return styleSet{
		app:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiCyan)),
		bright:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiBrightWhite)),
		faint:     lipgloss.NewStyle().Faint(true),
		title:     lipgloss.NewStyle().Bold(true),
		box:       frame(ansiCyan),
		savedBox:  frame(ansiGreen),
		failedBox: frame(ansiRed),
		selected:  lipgloss.NewStyle().Bold(true).Reverse(true),
		spinner:   lipgloss.NewStyle().Foreground(lipgloss.Color(ansiCyan)),
		phase:     lipgloss.NewStyle().Foreground(lipgloss.Color(ansiYellow)),
		success:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiGreen)),
		failure:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiRed)),
		// UnderlineSpaces keeps the line continuous through a path with a
		// space in it, which on macOS is most of them.
		path: lipgloss.NewStyle().Underline(true).UnderlineSpaces(true),
		doc:  lipgloss.NewStyle().Padding(1, 2),
		// The animated download and done screens.
		barFill:     lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBlue)),
		barGlint:    lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlue)),
		barFlash:    lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightGreen)),
		barFlashDim: lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)),
		savedBorder: lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)),
	}
})

// frame is a rounded box with one cell of padding inside it, its border in
// the given ANSI colour. The three boxes the screens draw differ only in that
// colour, so the geometry is written once and boxOverhead stays true of all
// of them.
func frame(colour string) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colour)).
		Padding(0, 1)
}

// boxOverhead is what a frame costs a line: one cell of border and one of
// padding on each side. Text going into a box is cut or wrapped to the width
// left after it, before the box is drawn, so the box wraps nothing itself.
const boxOverhead = 4

// styleBuilds counts how many times the set has been constructed. It is here
// for the test that asserts package initialisation builds none: laziness is
// invisible in the rendered output — that is the point, the frames must not
// change — so the count is the only thing an assertion can hold on to.
var styleBuilds atomic.Int64

// fit cuts s to w cells and only then applies style.
//
// The order is load-bearing, and it is the rule CLAUDE.md states: Style.Render
// wraps its argument in escape sequences, so cutting the rendered string to a
// display width cuts the trailing reset off with it — the style then bleeds
// down every line below — while the leading sequence eats cells out of the
// budget, leaving the line shorter than its unstyled neighbours. lipgloss.Width
// counts neither, so truncate cannot defend against it.
//
// Neither symptom appears under `go test`, where the colour profile is Ascii
// and Render is the identity. That is exactly why this is one named function
// with the reason written on it rather than two calls nested at each site.
func fit(style lipgloss.Style, s string, w int) string {
	return style.Render(truncate(s, w))
}

const (
	// selectedMarker and unselectedMarker are the same width on purpose: the
	// rows must not shift sideways as the cursor moves.
	selectedMarker   = "▸ "
	unselectedMarker = "  "
)
