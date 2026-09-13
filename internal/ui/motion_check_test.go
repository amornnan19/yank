package ui

import (
	"testing"
	"time"
)

func TestCheckPopGrowsTheGlyphThenTypesTheTitle(t *testing.T) {
	for _, already := range []bool{false, true} {
		title := []rune(doneTitle(already))
		base := doneFrame{glyph: string(title[0]), heading: string(title[1:]), trace: 1, noteFaint: true}
		r := rig(newCheckPop)
		r.done = doneFacts{seq: 7, already: already}
		r.send(evShown)
		if !r.effect().busy() {
			t.Fatalf("already %v: shown, the pop is not waiting for its first tick", already)
		}
		if f := r.mo.paintDone(base); f.glyph != "·" || f.heading != "" {
			t.Errorf("already %v: before the first tick the title is %q%q", already, f.glyph, f.heading)
		}
		var glyphs []string
		typed := -1
		for at := time.Duration(0); at < checkDuration(len(title)-1); at += motionFrame / 3 {
			r.tickAt(at)
			f := r.mo.paintDone(base)
			if len(glyphs) == 0 || glyphs[len(glyphs)-1] != f.glyph {
				glyphs = append(glyphs, f.glyph)
			}
			n := len([]rune(f.heading))
			if f.heading != string(title[1:1+n]) {
				t.Fatalf("already %v: at %v the title reads %q, not a start of %q", already, at, f.heading, string(title[1:]))
			}
			if f.glyph != string(title[0]) && n > 0 {
				t.Errorf("already %v: at %v words typed before the glyph arrived", already, at)
			}
			if n < typed {
				t.Errorf("already %v: at %v the title went back from %d runes to %d", already, at, typed, n)
			}
			typed = n
		}
		if len(glyphs) != 3 || glyphs[0] != "·" || glyphs[1] != "•" || glyphs[2] != "✓" {
			t.Errorf("already %v: the glyph went %v, want · • ✓", already, glyphs)
		}
		r.tickAt(checkDuration(len(title) - 1))
		if f := r.mo.paintDone(base); f.glyph != base.glyph || f.heading != base.heading || !r.idle() {
			t.Errorf("already %v: finished, the title is %q%q and the pop idle %v", already, f.glyph, f.heading, r.idle())
		}
		// Once for each outcome: shown again for the same one, it stays put.
		r.send(evShown)
		if !r.idle() {
			t.Errorf("already %v: the pop played again for the same outcome", already)
		}
		r.done.seq++
		r.send(evShown)
		if !r.effect().busy() {
			t.Errorf("already %v: a new outcome did not play the pop", already)
		}
	}
	if checkStage*3 > 200*time.Millisecond {
		t.Errorf("the glyph takes %v to arrive, want about 150ms", checkStage*3)
	}
}
