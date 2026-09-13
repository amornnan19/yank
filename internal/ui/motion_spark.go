package ui

import (
	"math"
	"time"
)

// speedSparkline is a faint graph of the last few speed reports after the
// stats line. It is driven by the reports alone — a new bar per report, not
// per tick — so it never asks for a frame, and it is hidden while the latest
// report's speed is unknown. Whether the line has room for it is View's call.
type speedSparkline struct {
	speeds [sparkLength]float64
	n      int
	known  bool
}

// sparkLength is how many reports the graph shows.
const sparkLength = 12

// sparkLevels are the graph's bars, lowest first.
var sparkLevels = []rune("▁▂▃▄▅▆▇█")

func newSpeedSparkline() effect { return speedSparkline{} }

func (e speedSparkline) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown, evLeft:
		e = speedSparkline{}
	case evReport:
		p := ev.dl.prog
		e.known = p.SpeedKnown && p.Speed >= 0 && !math.IsInf(p.Speed, 0) && !math.IsNaN(p.Speed)
		if !e.known {
			break
		}
		// An array, shifted rather than ringed: a copy of the effect is a
		// copy of the history, and twelve floats is nothing to move.
		if e.n == sparkLength {
			copy(e.speeds[:], e.speeds[1:])
			e.n--
		}
		e.speeds[e.n] = p.Speed
		e.n++
	}
	return e
}

func (e speedSparkline) busy() bool { return false }

func (e speedSparkline) wake() time.Time { return time.Time{} }

func (e speedSparkline) paint(f *downloadFrame) {
	if !e.known || e.n == 0 {
		return
	}
	top := 0.0
	for _, s := range e.speeds[:e.n] {
		top = max(top, s)
	}
	graph := make([]rune, e.n)
	for i, s := range e.speeds[:e.n] {
		level := 0
		if top > 0 {
			level = int(math.Round(s / top * float64(len(sparkLevels)-1)))
		}
		graph[i] = sparkLevels[min(max(level, 0), len(sparkLevels)-1)]
	}
	f.spark = string(graph)
}
