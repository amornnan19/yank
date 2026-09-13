package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// titleMarquee scrolls a title too wide for the content instead of leaving it
// cut short: it holds at the start, scrolls to the end, holds there, and
// starts over. A title that fits does not move. It rides the spinner's tick,
// at a pace one cell a tick keeps up with.
//
// The title is page text. The frame carries it already sanitised, and the
// window is cut from the sanitised title by display cell, between grapheme
// clusters, before anything styles it.
type titleMarquee struct {
	cue   cue
	title string
}

const (
	// marqueeHold is the pause at the start, and marqueeHoldEnd at the end.
	marqueeHold    = 2 * time.Second
	marqueeHoldEnd = 1500 * time.Millisecond
	// marqueeSpeed is the scroll in cells a second.
	marqueeSpeed = 8
)

func newTitleMarquee() effect { return titleMarquee{} }

func (e titleMarquee) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		e = titleMarquee{title: ev.dl.title}
		e.cue.arm()
	case evLeft:
		e = titleMarquee{}
	case evReport, evUpdate:
		// The stale-info retry brings a fresh probe; a title that changed
		// starts from its beginning.
		if ev.dl.title != e.title {
			e = titleMarquee{title: ev.dl.title}
			e.cue.arm()
		}
	case evTick:
		e.cue.settle(ev.now)
	}
	return e
}

func (e titleMarquee) busy() bool { return false }

func (e titleMarquee) wake() time.Time { return time.Time{} }

// marqueeOffset is how many cells into a title travel cells wider than its
// window the window starts, elapsed into the scroll.
func marqueeOffset(travel int, elapsed time.Duration) int {
	if travel <= 0 {
		return 0
	}
	scroll := time.Duration(float64(travel) / marqueeSpeed * float64(time.Second))
	cycle := marqueeHold + scroll + marqueeHoldEnd
	t := elapsed % cycle
	switch {
	case t < marqueeHold:
		return 0
	case t >= marqueeHold+scroll:
		return travel
	}
	return min(travel, int((t-marqueeHold).Seconds()*marqueeSpeed))
}

func (e titleMarquee) paint(f *downloadFrame) {
	travel := lipgloss.Width(f.title) - f.cw
	if travel <= 0 {
		return
	}
	f.titleText = cellWindow(f.title, marqueeOffset(travel, e.cue.elapsed(f.now)), f.cw)
	f.titleSet = true
}

// cellWindow is the w display cells of s starting at cell off, cut between
// grapheme clusters the way truncate cuts. A wide cluster cut by either edge of
// the window is replaced by the spaces of its cells that fall inside, so the
// window is never wider than w and nothing after it moves. s must already be
// sanitised.
func cellWindow(s string, off, w int) string {
	var b strings.Builder
	end := off + w
	pos := 0
	// kept is whether the last cluster with width was written whole.
	kept := false
	for c, cw := range graphemes(s) {
		if cw == 0 {
			// A cluster with no width of its own goes where the one before
			// it went.
			if kept {
				b.WriteString(c)
			}
			continue
		}
		if pos >= end {
			break
		}
		next := pos + cw
		kept = false
		switch {
		case next <= off:
			// Before the window.
		case pos < off:
			// Cut by the left edge.
			b.WriteString(strings.Repeat(" ", min(next, end)-off))
		case next <= end:
			b.WriteString(c)
			kept = true
		default:
			// Cut by the right edge.
			b.WriteString(strings.Repeat(" ", end-pos))
		}
		pos = next
	}
	return b.String()
}
