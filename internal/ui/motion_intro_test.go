package ui

import (
	"strings"
	"testing"
	"time"
)

func TestIntroDropStartsAdvancesAndFinishes(t *testing.T) {
	const cw = 76
	r := rig(newIntroDrop)
	if !r.idle() {
		t.Fatalf("the intro wants ticks before the screen was shown")
	}

	r.send(evShown)
	if !r.effect().busy() {
		t.Fatalf("shown, the intro is not waiting for its first tick")
	}
	// Before the first tick every letter is still above the drawing.
	if got := strings.Join(plainWordmark(r.frame(cw)), ""); strings.TrimSpace(got) != "" {
		t.Errorf("before the first tick the wordmark shows %q, want nothing", got)
	}

	scrambled, dropped, bounced := false, false, false
	for at := time.Duration(0); at < introDuration(); at += motionFrame {
		r.tickAt(at)
		if !r.effect().busy() {
			t.Fatalf("the intro stopped wanting frames at %v, before its %v", at, introDuration())
		}
		f := r.frame(cw)
		rows := plainWordmark(f)
		if len(rows) != len(wordmarkRows) {
			t.Fatalf("at %v the drawing is %d rows, want %d", at, len(rows), len(wordmarkRows))
		}
		for _, row := range rows {
			if len([]rune(row)) != len([]rune(staticWordmark(cw)[0])) {
				t.Fatalf("at %v a row is %d cells, want the canvas width", at, len([]rune(row)))
			}
		}
		all := strings.Join(rows, "")
		scrambled = scrambled || strings.ContainsAny(all, "░▒▓")
		// y's bottom row drawn while its top row is empty is a letter
		// partway down; drawn a row low it is on the bounce.
		y := wordmarkLetters[0]
		top, bottom := string([]rune(rows[0])[y[0]:y[1]]), string([]rune(rows[len(rows)-1])[y[0]:y[1]])
		if strings.TrimSpace(top) == "" && strings.TrimSpace(bottom) != "" {
			dropped = true
		}
		if off := introOffset(at); off > 0 {
			bounced = true
		}
	}
	if !scrambled || !dropped || !bounced {
		t.Errorf("the intro never scrambled (%v), dropped (%v) or bounced (%v)", scrambled, dropped, bounced)
	}
	if introDuration() >= 800*time.Millisecond {
		t.Errorf("the intro takes %v, want under 0.8s", introDuration())
	}

	r.tickAt(introDuration())
	if !r.idle() {
		t.Errorf("the intro still wants ticks after it finished")
	}
	if got := plainWordmark(r.frame(cw)); !equalLines(got, staticWordmark(cw)) {
		t.Errorf("the finished intro draws\n%s\nwant the static drawing", strings.Join(got, "\n"))
	}

	// Once a session: shown again, it does not replay.
	r.send(evShown)
	if !r.idle() {
		t.Errorf("the intro replays when the screen is shown again")
	}
}

func TestIntroIsSkippedByEnter(t *testing.T) {
	const cw = 76
	r := rig(newIntroDrop)
	r.send(evShown)
	r.tickAt(0)
	r.tickAt(200 * time.Millisecond)
	r.send(evSubmit)
	if !r.idle() {
		t.Errorf("enter mid-intro left it wanting ticks")
	}
	if got := plainWordmark(r.frame(cw)); !equalLines(got, staticWordmark(cw)) {
		t.Errorf("after enter mid-intro the wordmark is\n%s", strings.Join(got, "\n"))
	}
}

func TestIntroScrambleComesFromTheSeed(t *testing.T) {
	frame := func(seed uint64) string {
		r := &effectRig{mo: newMotionOf([]effect{newIntroDrop()}, seed)}
		r.send(evShown)
		r.tickAt(0)
		r.tickAt(150 * time.Millisecond)
		return strings.Join(plainWordmark(r.frame(76)), "\n")
	}
	if frame(3) != frame(3) {
		t.Errorf("the same seed scrambled differently")
	}
	if frame(3) == frame(4) {
		t.Errorf("different seeds scrambled identically")
	}
}

func TestWordmarkLettersCoverEveryDrawnCell(t *testing.T) {
	for r, row := range wordmarkRows {
		for c, ch := range []rune(row) {
			if ch == ' ' {
				continue
			}
			n := 0
			for _, span := range wordmarkLetters {
				if c >= span[0] && c < span[1] {
					n++
				}
			}
			if n != 1 {
				t.Errorf("cell %d,%d %q lies in %d letter spans, want 1", r, c, ch, n)
			}
		}
	}
}
