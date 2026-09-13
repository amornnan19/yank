package ui

import (
	"time"

	"github.com/amornnan19/yank/internal/ytdlp"
)

// leadingEdge draws the cell at the head of the bar's fill one step brighter
// while bytes are arriving: from a report that counted more bytes than the one
// before it until edgeHold has passed without another. It rides the spinner's
// tick.
type leadingEdge struct {
	// grew is armed by a report that counted more bytes, and runs from the
	// tick that learns the time.
	grew cue
	last int64
	now  time.Time
}

// edgeHold is how long the edge stays lit after the last report that grew.
const edgeHold = time.Second

func newLeadingEdge() effect { return leadingEdge{} }

func (e leadingEdge) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown, evLeft:
		e = leadingEdge{}
	case evReport:
		p := ev.dl.prog
		if p.Phase != ytdlp.PhaseDownloading || !p.DownloadedKnown {
			e.grew.stop()
			break
		}
		if p.Downloaded > e.last {
			e.grew.arm()
		}
		e.last = p.Downloaded
	case evTick:
		e.grew.settle(ev.now)
		e.now = ev.now
		if e.grew.running && e.grew.elapsed(ev.now) >= edgeHold {
			e.grew.stop()
		}
	}
	return e
}

func (e leadingEdge) busy() bool { return false }

func (e leadingEdge) wake() time.Time { return time.Time{} }

func (e leadingEdge) paint(f *downloadFrame) {
	lit := e.grew.waiting || (e.grew.running && e.grew.elapsed(f.now) < edgeHold)
	if !lit || f.filled < 1 || f.filled > len(f.bar) {
		return
	}
	f.bar[f.filled-1].ink = inkGlint
}
