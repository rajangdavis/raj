package widget

import (
	"testing"

	"raj/internal/keys"
)

func field(text string, cursor int) *Input {
	return &Input{Text: text, Cursor: cursor, Anchor: cursor, Focused: true}
}

// alt+left/right were aliased to prevBoundary/nextBoundary, which walk one
// UTF-8 rune. Word motion and character motion were the same keystroke under
// two names, which reads as "bound" rather than "missing".
func TestWordMotionMovesByWord(t *testing.T) {
	in := field("alpha beta gamma", 16)
	in.Handle(keys.WordLeft, "")
	if in.Cursor != 11 {
		t.Errorf("word left = %d, want 11 (start of gamma)", in.Cursor)
	}
	in.Handle(keys.WordLeft, "")
	if in.Cursor != 6 {
		t.Errorf("second word left = %d, want 6 (start of beta)", in.Cursor)
	}
	in.Handle(keys.WordRight, "")
	if in.Cursor != 10 {
		t.Errorf("word right = %d, want 10 (end of beta)", in.Cursor)
	}
}

// Queries hold globs and paths. Punctuation has to break words or alt+left is
// useless for editing "internal/keys" or "*.go".
func TestWordMotionBreaksOnPunctuation(t *testing.T) {
	in := field("internal/keys/table.go", 22)
	in.Handle(keys.WordLeft, "")
	if got := in.Text[in.Cursor:]; got != "go" {
		t.Errorf("landed before %q, want %q", got, "go")
	}
}

func TestShiftAltSelectsByWord(t *testing.T) {
	in := field("alpha beta gamma", 16)
	in.Handle(keys.SelWordLeft, "")
	lo, hi := in.Selection()
	if in.Text[lo:hi] != "gamma" {
		t.Errorf("selected %q, want %q", in.Text[lo:hi], "gamma")
	}
	in.Handle(keys.SelWordLeft, "")
	lo, hi = in.Selection()
	if in.Text[lo:hi] != "beta gamma" {
		t.Errorf("extended to %q, want %q", in.Text[lo:hi], "beta gamma")
	}
}

func TestCmdShiftSelectsToEdge(t *testing.T) {
	in := field("alpha beta", 5)
	in.Handle(keys.SelLineStart, "")
	if lo, hi := in.Selection(); in.Text[lo:hi] != "alpha" {
		t.Errorf("selected %q, want %q", in.Text[lo:hi], "alpha")
	}
	in = field("alpha beta", 5)
	in.Handle(keys.SelLineEnd, "")
	if lo, hi := in.Selection(); in.Text[lo:hi] != " beta" {
		t.Errorf("selected %q, want %q", in.Text[lo:hi], " beta")
	}
}

// cmd+a used to empty the field: with no selection to represent, "select all"
// had nowhere to put its result and did the destructive thing that looked like it.
func TestSelectAllSelectsRatherThanClears(t *testing.T) {
	in := field("keep me", 0)
	in.Handle(keys.SelectAll, "")
	if in.Text != "keep me" {
		t.Fatalf("text = %q; select-all destroyed the contents", in.Text)
	}
	if lo, hi := in.Selection(); lo != 0 || hi != len(in.Text) {
		t.Errorf("selection = %d..%d, want the whole field", lo, hi)
	}
}

func TestTypingReplacesSelection(t *testing.T) {
	in := field("alpha beta", 10)
	in.Handle(keys.SelWordLeft, "")
	in.Handle(keys.None, "x")
	if in.Text != "alpha x" {
		t.Errorf("text = %q, want %q", in.Text, "alpha x")
	}
	if in.HasSelection() {
		t.Error("selection survived the replacement")
	}
}

func TestBackspaceDeletesSelection(t *testing.T) {
	in := field("alpha beta", 10)
	in.Handle(keys.SelWordLeft, "")
	in.Handle(keys.Backspace, "")
	if in.Text != "alpha " {
		t.Errorf("text = %q, want %q", in.Text, "alpha ")
	}
}

// An unshifted arrow beside a selection jumps to that edge rather than moving
// one place from the caret, which is what every macOS field does.
func TestPlainArrowCollapsesToEdge(t *testing.T) {
	in := field("alpha beta", 10)
	in.Handle(keys.SelWordLeft, "")
	in.Handle(keys.CharLeft, "")
	if in.Cursor != 6 || in.HasSelection() {
		t.Errorf("cursor = %d selected = %v, want 6 and collapsed", in.Cursor, in.HasSelection())
	}
}

// Replacing the contents must not leave the anchor indexing past the new end;
// that panicked the find bar on its first keystroke.
func TestSetTextClearsStaleAnchor(t *testing.T) {
	in := field("something", 9)
	in.Handle(keys.SelectAll, "")
	in.SetText("")
	if in.Cursor != 0 || in.Anchor != 0 {
		t.Fatalf("cursor = %d anchor = %d, want 0/0", in.Cursor, in.Anchor)
	}
	in.Handle(keys.None, "a") // would panic on a stale anchor
	if in.Text != "a" {
		t.Errorf("text = %q, want %q", in.Text, "a")
	}
}

func TestTrimClampsOffsets(t *testing.T) {
	in := field("  hi  ", 6)
	f := Fields{Inputs: []*Input{in}}
	f.Trim()
	if in.Cursor > len(in.Text) || in.Anchor > len(in.Text) {
		t.Errorf("cursor = %d anchor = %d, past len %d", in.Cursor, in.Anchor, len(in.Text))
	}
}
