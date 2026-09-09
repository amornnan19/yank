package ui

import "github.com/charmbracelet/lipgloss"

// The palette is deliberately empty. Nothing here sets a foreground colour, so
// every line is drawn in whatever the terminal's own foreground is and reads on
// a light background and a dark one alike. Emphasis comes from bold, faint and
// a border — attributes every terminal renders in its own colours — rather than
// from a grey that is only grey on one of them.
//
// A theme system is a later pass; these are the plain styles it would replace.
var (
	// appStyle is the name above every screen.
	appStyle = lipgloss.NewStyle().Bold(true)

	// faintStyle is for text that is there when wanted and out of the way when
	// not: the help line, hints, the uploader.
	faintStyle = lipgloss.NewStyle().Faint(true)

	// titleStyle is the video's title, the one thing on the screen a person
	// checks they got right.
	titleStyle = lipgloss.NewStyle().Bold(true)

	// boxStyle frames the URL input.
	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	// selectedStyle marks the picker row under the cursor. Bold plus the "▸"
	// marker, so the selection survives a terminal that renders no attributes
	// at all.
	selectedStyle = lipgloss.NewStyle().Bold(true)

	// docStyle is the margin every screen sits in.
	docStyle = lipgloss.NewStyle().Padding(1, 2)
)

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
