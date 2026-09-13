package ui

import "time"

// typingPlaceholder types and erases a short rotation of examples in the empty
// box. The moment the box holds anything it stops for good — for the rest of
// the session, not just until the box is emptied again — and the box's own
// placeholder is left alone from then on.
type typingPlaceholder struct {
	cue     cue
	stopped bool
	last    time.Time
}

// placeholderExamples is the rotation.
var placeholderExamples = []string{
	"https://www.youtube.com/watch?v=…",
	"a Vimeo or SoundCloud link…",
	"or any of yt-dlp's 1000+ sites",
}

const (
	// placeholderType is the time between typed characters.
	placeholderType = 55 * time.Millisecond
	// placeholderHold is how long a finished example stays up.
	placeholderHold = 1600 * time.Millisecond
	// placeholderErase is the time between erased characters.
	placeholderErase = 22 * time.Millisecond
	// placeholderGap is the empty pause before the next example.
	placeholderGap = 400 * time.Millisecond
)

func newTypingPlaceholder() effect { return typingPlaceholder{} }

func (e typingPlaceholder) step(ev motionEvent) effect {
	if e.stopped {
		return e
	}
	switch ev.kind {
	case evShown, evEdit, evSubmit:
		switch {
		case ev.value != "":
			// A stopped placeholder holds no cue, so it wants no tick
			// and paints nothing.
			e = typingPlaceholder{stopped: true}
		case !e.cue.live():
			e.cue.arm()
		}
	case evTick:
		e.cue.settle(ev.now)
		e.last = ev.now
	}
	return e
}

func (e typingPlaceholder) busy() bool { return e.cue.waiting }

// wake is the next character typed or erased.
func (e typingPlaceholder) wake() time.Time {
	if !e.cue.running {
		return time.Time{}
	}
	elapsed := e.cue.elapsed(e.last)
	_, next := placeholderAt(elapsed)
	return e.cue.at.Add(elapsed + next)
}

func (e typingPlaceholder) paint(f *inputFrame) {
	if !e.cue.live() {
		return
	}
	f.placeholder, _ = placeholderAt(e.cue.elapsed(f.now))
	f.placeholderSet = true
}

// placeholderAt is the placeholder t into the rotation, and how long until it
// next changes. The rotation is a fixed timeline, so any t lands on the same
// text however it was reached.
func placeholderAt(t time.Duration) (string, time.Duration) {
	var cycle time.Duration
	for _, ex := range placeholderExamples {
		cycle += placeholderSpan(ex)
	}
	if cycle <= 0 {
		return "", 0
	}
	t %= cycle
	for _, ex := range placeholderExamples {
		rs := []rune(ex)
		n := time.Duration(len(rs))
		typing := n * placeholderType
		erasing := n * placeholderErase
		switch {
		case t < typing:
			k := t / placeholderType
			return string(rs[:k]), (k+1)*placeholderType - t
		case t < typing+placeholderHold:
			return ex, typing + placeholderHold - t
		case t < typing+placeholderHold+erasing:
			k := (t - typing - placeholderHold) / placeholderErase
			return string(rs[:n-k]), typing + placeholderHold + (k+1)*placeholderErase - t
		case t < placeholderSpan(ex):
			return "", placeholderSpan(ex) - t
		}
		t -= placeholderSpan(ex)
	}
	return "", placeholderType
}

// placeholderSpan is one example's whole turn: typed, held, erased, and the
// gap after it.
func placeholderSpan(ex string) time.Duration {
	n := time.Duration(len([]rune(ex)))
	return n*placeholderType + placeholderHold + n*placeholderErase + placeholderGap
}
