package editor

import (
	"strings"
	"testing"

	"raj/internal/keys"
)

// cmd+u steps back through the positions the cursor has moved from. Two
// presses, two recorded positions.
func TestCursorUndoRestoresPosition(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.Handle(keys.LineDown) // 0 -> 4
	p.Handle(keys.LineDown) // 4 -> 8
	if got := p.Cursors.Primary().Head; got != 8 {
		t.Fatalf("head = %d, want 8", got)
	}
	p.Handle(keys.CursorUndo)
	if got := p.Cursors.Primary().Head; got != 4 {
		t.Fatalf("after one undo head = %d, want 4", got)
	}
	p.Handle(keys.CursorUndo)
	if got := p.Cursors.Primary().Head; got != 0 {
		t.Errorf("after two undos head = %d, want 0", got)
	}
}

// A selection is part of the position: undo restores head and anchor, not just
// where the caret sat.
func TestCursorUndoRestoresSelection(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.Handle(keys.SelCharRight) // head 1, anchor 0
	p.Handle(keys.CursorUndo)
	if c := p.Cursors.Primary(); c.Head != 0 || c.Anchor != 0 {
		t.Errorf("cursor = %+v, want head 0 anchor 0 after undo", c)
	}
}

// Every cursor in the set is recorded, so a multi-cursor selection returns as
// a set, not as one caret. The added cursor lives at the start of line 2
// (offset 4), so restoring the pre-move position brings both heads back there.
func TestCursorUndoRestoresMultiCursorSet(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.AddCursorVertical(1)
	p.Handle(keys.CharRight)
	if all := p.Cursors.All(); len(all) != 2 || all[0].Head != 1 || all[1].Head != 5 {
		t.Fatalf("after move cursors = %+v, want heads at 1 and 5", all)
	}
	p.Handle(keys.CursorUndo)
	all := p.Cursors.All()
	if len(all) != 2 || all[0].Head != 0 || all[0].Anchor != 0 ||
		all[1].Head != 4 || all[1].Anchor != 4 {
		t.Errorf("cursors = %+v, want two heads at 0 and 4", all)
	}
}

// A movement that cannot move must not fill the history with copies of where
// the cursor already is. The dedupe compares against the last recorded
// position, so a real move followed by a no-op records the no-op once and the
// next repeats nothing.
func TestCursorUndoDedupesUnchangedPositions(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.Handle(keys.LineDown) // 0 -> 4
	p.Handle(keys.LineDown) // 4 -> 8
	p.Handle(keys.LineDown) // 8 -> 8: at the last line
	p.Handle(keys.LineDown) // 8 -> 8: repeated no-op, deduped
	if got := len(p.cursorHistory); got != 3 {
		t.Fatalf("history = %d entries, want 3 (repeated no-op pushed nothing)", got)
	}
	p.Handle(keys.CursorUndo)
	p.Handle(keys.CursorUndo)
	p.Handle(keys.CursorUndo)
	if got := p.Cursors.Primary().Head; got != 0 {
		t.Errorf("head = %d, want 0 after three undos", got)
	}
}

// The ring is bounded: an hour of moving around can't grow the memory it
// takes, and the earliest positions are the ones nobody steps back to.
func TestCursorHistoryIsCapped(t *testing.T) {
	p := newTestPane(strings.Repeat("aaaa\n", 60))
	for i := 0; i < 55; i++ {
		p.Handle(keys.LineDown)
	}
	if got := len(p.cursorHistory); got != 50 {
		t.Errorf("history = %d entries, want the 50 cap", got)
	}
}

// cmd+u before anything has moved is a no-op, not a failure: the chord is
// handled, the cursor stays where it is.
func TestCursorUndoOnEmptyHistoryIsANoOp(t *testing.T) {
	p := newTestPane("x")
	p.Cursors.Set(1, 1)
	p.Handle(keys.CursorUndo)
	if c := p.Cursors.Primary(); c.Head != 1 {
		t.Errorf("head = %d, want 1 (nothing to undo)", c.Head)
	}
}
