package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Placement puts a screen in the middle of the terminal. Every screen is built
// as a block — its lines, left-aligned to one another, no margin round them —
// and View hands that block to place, which is the one spot that decides where
// it goes. The text inside is never centred line by line: the whole block
// moves, and nothing in it moves relative to anything else.

const (
	// marginRows and marginCols are the doc style's padding: the margin a
	// screen drawn top-left sits in, and the gap the starfield and the
	// confetti keep round a centred block.
	marginRows = 1
	marginCols = 2
)

// placement is where a block goes.
type placement struct {
	// centred is false when the terminal's size is unknown, too narrow for
	// the content with its margins, or no taller than the lines the block
	// draws: the block is then drawn top-left in the doc style, exactly as
	// every screen was drawn before centring existed.
	centred bool
	// top and left are the block's first row and column.
	top, left int
	// rows is the height the block is centred on: its own lines, or the rows
	// its screen reserves for lines that come and go, whichever is more — but
	// only while the reserve is shorter than the terminal; see placement.
	// width is the widest the block is: the content width, or a done-screen
	// path that is wider.
	rows, width int
}

// placement is where block goes in the terminal. reserve is the rows its
// screen keeps for lines that come and go — see inputRows and doneRows — so
// that one appearing does not move the block.
//
// Whether the block is centred at all depends on the lines it draws, never on
// its reserve: a screen that fits is centred even when the tallest screen it
// could have been would not. The reserve counts only while it is shorter than
// the terminal, which is when its guarantee holds — the block does not move as
// a reserved line comes or goes. On a terminal as short as the reserve or
// shorter, the block is centred on its own lines instead: a line appearing
// then moves it, which on a terminal that short is better than drawing it
// top-left with no room round it. The input screen's reserve is always kept
// while it shows the wordmark, which needs a taller terminal than that.
//
// The left edge comes from contentWidth, never from the block's widest line,
// so the block does not step sideways as a marquee, a badge or a path changes
// what its widest line is. At 100 columns or fewer that edge is the doc
// style's own margin: the content already fills the terminal, and centring
// changes nothing sideways.
func (m Model) placement(block string, reserve int) placement {
	cw := m.contentWidth()
	lines := lineCount(block)
	p := placement{
		rows:  lines,
		width: max(cw, blockWidth(block)),
	}
	if m.width <= 0 || m.height <= 0 || lines >= m.height || cw+2*marginCols > m.width {
		return p
	}
	if reserve < m.height {
		p.rows = max(lines, reserve)
	}
	p.centred = true
	p.top = (m.height - p.rows) / 2
	p.left = m.marginLeft()
	return p
}

// place draws block where placement puts it, with marks — the starfield's
// or the confetti's, when the screen has either — in the free space around it.
// Centring pads with plain spaces only: nothing styled is added round the
// block, and none of its lines is cut or restyled on the way.
//
// It never adds a row past the terminal's height: the block ends inside it by
// placement's own rule, and freeArea offers no cell outside it.
func (m Model) place(block string, reserve int, marks func(freeArea) []mark) string {
	p := m.placement(block, reserve)
	if !p.centred {
		return styles().doc.Render(block)
	}
	var ms []mark
	if marks != nil {
		ms = marks(m.freeArea(p))
	}
	lines := strings.Split(block, "\n")
	rows := p.top + len(lines)
	for _, mk := range ms {
		rows = max(rows, mk.row+1)
	}
	byRow := make([][]mark, rows)
	for _, mk := range ms {
		byRow[mk.row] = append(byRow[mk.row], mk)
	}
	out := make([]string, rows)
	for r := range out {
		line := ""
		if i := r - p.top; i >= 0 && i < len(lines) {
			line = lines[i]
		}
		out[r] = composeRow(line, p.left, byRow[r])
	}
	return join(out...)
}

// mark is one cell of decoration in the free space round a block: a glyph one
// cell wide, already styled, at a terminal row and column.
type mark struct {
	row, col int
	glyph    string
}

// composeRow is one terminal row: line at column left, and marks either side
// of it. freeArea never offers a cell the line holds, so every mark lies wholly
// before left or wholly after the line; a mark that does not — two marks on
// one cell — is dropped rather than drawn over what is already there.
func composeRow(line string, left int, marks []mark) string {
	marks = slices.Clone(marks)
	slices.SortStableFunc(marks, func(a, b mark) int { return cmp.Compare(a.col, b.col) })
	var b strings.Builder
	col := 0
	put := func(at int, s string, w int) {
		b.WriteString(strings.Repeat(" ", at-col))
		b.WriteString(s)
		col = at + w
	}
	i := 0
	if line != "" {
		for ; i < len(marks) && marks[i].col < left; i++ {
			if marks[i].col >= col {
				put(marks[i].col, marks[i].glyph, 1)
			}
		}
		put(max(left, col), line, lipgloss.Width(line))
	}
	for ; i < len(marks); i++ {
		if marks[i].col >= col {
			put(marks[i].col, marks[i].glyph, 1)
		}
	}
	return b.String()
}

// freeArea is the part of the terminal a centred block leaves empty: every cell
// except a rectangle round the block, which covers the block's rows and
// columns and a margin of marginRows and marginCols on every side. The
// rectangle takes the rows the screen reserves as well as the ones it draws,
// so a line appearing never lands on a mark. The zero value has no cells: a
// block drawn top-left leaves nothing for decoration.
type freeArea struct {
	height, width int
	// top, bottom, left and right bound the rectangle kept clear:
	// rows [top, bottom) and columns [left, right), inside the terminal.
	top, bottom, left, right int
}

// freeArea is the free space round a block placed at p.
func (m Model) freeArea(p placement) freeArea {
	if !p.centred {
		return freeArea{}
	}
	return freeArea{
		height: m.height,
		width:  m.width,
		top:    max(p.top-marginRows, 0),
		bottom: min(p.top+p.rows+marginRows, m.height),
		left:   max(p.left-marginCols, 0),
		right:  min(p.left+p.width+marginCols, m.width),
	}
}

// free reports whether the cell at row r, column c is in the terminal and
// outside the rectangle kept clear.
func (a freeArea) free(r, c int) bool {
	if r < 0 || r >= a.height || c < 0 || c >= a.width {
		return false
	}
	return r < a.top || r >= a.bottom || c < a.left || c >= a.right
}

// cells is how many free cells there are.
func (a freeArea) cells() int {
	return a.height*a.width - (a.bottom-a.top)*(a.right-a.left)
}

// cell is the i-th free cell, counted along each row from the top-left, for i
// in [0, cells()): the rows above the rectangle, then the cells either side of
// it on its own rows, then the rows below.
func (a freeArea) cell(i int) (row, col int) {
	above := a.top * a.width
	if i < above {
		return i / a.width, i % a.width
	}
	i -= above
	gap := a.right - a.left
	side := a.width - gap
	beside := (a.bottom - a.top) * side
	if i < beside {
		j := i % side
		if j >= a.left {
			j += gap
		}
		return a.top + i/side, j
	}
	i -= beside
	return a.bottom + i/a.width, i % a.width
}

// lineCount is how many rows a block takes.
func lineCount(block string) int { return strings.Count(block, "\n") + 1 }

// blockWidth is the display width of a block's widest line.
func blockWidth(block string) int {
	w := 0
	for _, line := range strings.Split(block, "\n") {
		w = max(w, lipgloss.Width(line))
	}
	return w
}
