package ui

import (
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
)

// styleSet is every lipgloss style the views draw with.
//
// The palette is deliberately empty. Nothing here sets a foreground colour, so
// every line is drawn in whatever the terminal's own foreground is and reads on
// a light background and a dark one alike. Emphasis comes from bold, faint and
// a border — attributes every terminal renders in its own colours — rather than
// from a grey that is only grey on one of them.
//
// A theme system is a later pass; these are the plain styles it would replace.
type styleSet struct {
	// app is the name above every screen.
	app lipgloss.Style

	// faint is for text that is there when wanted and out of the way when
	// not: the help line, hints, the uploader.
	faint lipgloss.Style

	// title is the video's title, the one thing on the screen a person
	// checks they got right.
	title lipgloss.Style

	// box frames the URL input.
	box lipgloss.Style

	// selected marks the picker row under the cursor. Bold plus the "▸"
	// marker, so the selection survives a terminal that renders no attributes
	// at all.
	selected lipgloss.Style

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
// default renderer, but a style that carried a colour — an AdaptiveColor, say —
// would query the terminal for its background, and by then the shape here is
// the thing that decides when.
//
// It is not what closes that window today: bubbletea's own init calls
// lipgloss.HasDarkBackground() on purpose (tea_init.go), so importing this
// package still queries the terminal before main whatever these styles do.
// Closing it is a change to how the binary starts, not to how it draws.
var styles = sync.OnceValue(func() styleSet {
	styleBuilds.Add(1)
	return styleSet{
		app:      lipgloss.NewStyle().Bold(true),
		faint:    lipgloss.NewStyle().Faint(true),
		title:    lipgloss.NewStyle().Bold(true),
		box:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		selected: lipgloss.NewStyle().Bold(true),
		doc:      lipgloss.NewStyle().Padding(1, 2),
	}
})

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
