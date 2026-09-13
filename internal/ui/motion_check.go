package ui

import "time"

// checkPop brings the done screen's title in: the glyph grows · → • → ✓, and
// then the words after it type in. It plays once for each outcome.
type checkPop struct {
	cue    cue
	seq    int
	played bool
	// chars is how many runes follow the glyph in the title.
	chars int
}

const (
	// checkStage is how long each of the glyph's stages shows.
	checkStage = 50 * time.Millisecond
	// checkType is the time between one typed rune and the next.
	checkType = 25 * time.Millisecond
)

// checkStages are the glyph's stages before the title's own glyph.
var checkStages = []string{"·", "•"}

func newCheckPop() effect { return checkPop{} }

// checkDuration is the whole entrance for a title chars runes after its glyph.
func checkDuration(chars int) time.Duration {
	return time.Duration(len(checkStages)+1)*checkStage + time.Duration(chars)*checkType
}

func (e checkPop) step(ev motionEvent) effect {
	switch ev.kind {
	case evShown:
		if e.seq == ev.done.seq && (e.played || e.cue.live()) {
			break
		}
		e = checkPop{seq: ev.done.seq, chars: len([]rune(doneTitle(ev.done.already))) - 1}
		e.cue.arm()
	case evTick:
		e.cue.settle(ev.now)
		if e.cue.running && e.cue.elapsed(ev.now) >= checkDuration(e.chars) {
			e.cue.stop()
			e.played = true
		}
	}
	return e
}

func (e checkPop) busy() bool { return e.cue.live() }

func (e checkPop) wake() time.Time { return time.Time{} }

func (e checkPop) paint(f *doneFrame) {
	if !e.cue.live() {
		return
	}
	t := e.cue.elapsed(f.now)
	if stage := int(t / checkStage); stage < len(checkStages) {
		f.glyph, f.heading = checkStages[stage], ""
		return
	}
	typed := int((t-time.Duration(len(checkStages))*checkStage)/checkType) + 1
	rs := []rune(f.heading)
	f.heading = string(rs[:min(max(typed-1, 0), len(rs))])
}
