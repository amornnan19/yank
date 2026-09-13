package ui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// finishFlash fills the bar and flashes it green when the download finishes,
// before the done screen shows. Like the input screen's exit it holds only
// what View draws: the model is already on the done screen with the result
// set, and enter and q act on it on every frame of the flash.
type finishFlash struct {
	cue cue
}

const (
	// flashDuration is the whole flash, and flashBright the first part of
	// it, drawn in bright green before it settles to green.
	flashDuration = 250 * time.Millisecond
	flashBright   = 120 * time.Millisecond
)

func newFinishFlash() effect { return finishFlash{} }

func (e finishFlash) step(ev motionEvent) effect {
	switch ev.kind {
	case evFinish:
		e.cue.arm()
	case evShown, evLeft:
		e = finishFlash{}
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= flashDuration {
			e = finishFlash{}
		}
	}
	return e
}

func (e finishFlash) busy() bool { return e.cue.live() }

func (e finishFlash) wake() time.Time { return time.Time{} }

func (e finishFlash) holds() bool { return e.cue.live() }

func (e finishFlash) paint(f *downloadFrame) {
	if !e.cue.live() {
		return
	}
	k := inkFlashDim
	if e.cue.elapsed(f.now) < flashBright {
		k = inkFlash
	}
	f.label = percentLabel(100, true)
	width := max(1, f.cw-lipgloss.Width(f.label)-2)
	f.sweep = false
	f.bar = make([]cell, width)
	for i := range f.bar {
		f.bar[i] = cell{r: '█', ink: k}
	}
	f.filled = width
}
