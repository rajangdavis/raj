package piecetable

import "testing"

// The bug these pin was found by a fuzz seed, which is a poor thing to leave as
// the only guard: a seed asserts that one arbitrary walk happens to come out
// right, and says nothing about which mechanism made it so. Each test below
// builds the situation directly.

// A redone op is live again, but an op recorded while it was absent carries a
// position from a document that did not contain it. Counting the resurrection
// before that op puts the range out by its length.
//
// The third author matters: its insertion has to stay live, so it cannot be
// half of a cancelling pair the walk could skip. Its position — flush against
// the end of the range being rebased — is chosen to sit between the answers the
// two models give, which is the only place they disagree.
func TestResurrectedOpDoesNotShiftLaterOps(t *testing.T) {
	const other = Author(3)
	for name, s := range sessions("abcdef") {
		s.Insert(Agent, 1, "PQ") // the range a later undo has to find
		s.Insert(User, 0, "XY")  // present, then not, then present again
		s.Undo(User)             // XY out
		s.Insert(other, 3, "Z")  // recorded against a document with no XY
		s.Redo(User)             // XY back

		if got := text(s); got != "XYaPQZbcdef" {
			t.Fatalf("%s: setup produced %q", name, got)
		}
		// PQ is at [3,5). Skipping the undo that removed XY, and the redo that
		// brought it back, leaves the walk holding [3,5) when it meets the Z at
		// 3 — so the Z pushes the range to [4,6) and the undo deletes "QZ".
		if !s.Undo(Agent) {
			t.Fatalf("%s: undo refused", name)
		}
		if got := text(s); got != "XYaZbcdef" {
			t.Errorf("%s: after undo = %q, want XYaZbcdef", name, got)
		}
	}
}

// An offset cannot say which side of a gap it is on. These two cases put a
// deletion flush against the same point from opposite sides; the reversal of
// each is an insertion at the identical offset, and only a record of what
// happened distinguishes them.
func TestReversalPlacementDependsOnWhichSideWasDeleted(t *testing.T) {
	t.Run("deletion begins at the point", func(t *testing.T) {
		for name, s := range sessions("abcdef") {
			s.Delete(User, 2, 2) // op0: removes "cd", the undo target
			s.Delete(User, 2, 2) // op1: removes "ef", beginning where op0 did
			s.Undo(User)         // op1 back: "abef"
			if got := text(s); got != "abef" {
				t.Fatalf("%s: setup produced %q", name, got)
			}
			// op0's bytes belong before the ones op1 just put back.
			if !s.Undo(User) {
				t.Fatalf("%s: undo refused", name)
			}
			if got := text(s); got != "abcdef" {
				t.Errorf("%s: got %q, want abcdef", name, got)
			}
		}
	})

	t.Run("deletion ends at the point", func(t *testing.T) {
		for name, s := range sessions("abcdef") {
			s.Delete(User, 4, 2) // op0: removes "ef", the undo target
			s.Delete(User, 2, 2) // op1: removes "cd", ending where op0 began
			s.Undo(User)         // op1 back: "abcd"
			if got := text(s); got != "abcd" {
				t.Fatalf("%s: setup produced %q", name, got)
			}
			// op0's bytes belong after the ones op1 just put back — the
			// opposite answer from the case above, at the same offset.
			if !s.Undo(User) {
				t.Fatalf("%s: undo refused", name)
			}
			if got := text(s); got != "abcdef" {
				t.Errorf("%s: got %q, want abcdef", name, got)
			}
		}
	})
}

// A deletion can take the bytes under one end of a range and leave the other
// alone. The range has to survive that as two ends with different fates, and
// come back whole when the deletion is undone.
func TestRangeStraddlingAnUndoneDeletionSurvives(t *testing.T) {
	for name, s := range sessions("abcdefgh") {
		s.Insert(User, 4, "MN") // op0: the range a later undo must find
		s.Delete(User, 3, 3)    // op1: takes "dM", straddling op0's start
		s.Undo(User)            // op1 back
		if got := text(s); got != "abcdMNefgh" {
			t.Fatalf("%s: setup produced %q", name, got)
		}
		if !s.Undo(User) {
			t.Fatalf("%s: undo of the straddled insertion refused", name)
		}
		if got := text(s); got != "abcdefgh" {
			t.Errorf("%s: got %q, want abcdefgh", name, got)
		}
	}
}

// A reversal is never damage to a third party. Its target's insertion split
// this range in two; taking it back out makes the range whole rather than
// breaking it, so the undo must not be refused.
func TestReversalOfAnInsertionInsideARangeIsNotAConflict(t *testing.T) {
	for name, s := range sessions("abcdef") {
		s.Insert(User, 2, "WXYZ") // op0: the range to undo later
		s.Insert(User, 4, "..")   // op1: lands strictly inside it
		s.Undo(User)              // op1 back out
		if got := text(s); got != "abWXYZcdef" {
			t.Fatalf("%s: setup produced %q", name, got)
		}
		if !s.Undo(User) {
			t.Fatalf("%s: undo refused after an insertion inside it was undone", name)
		}
		if got := text(s); got != "abcdef" {
			t.Errorf("%s: got %q, want abcdef", name, got)
		}
	}
}

// The same op left in place must still be refused: an edit that is genuinely in
// effect inside the range is real damage, and the relaxation above must not
// have swallowed that case too.
func TestLiveInsertionInsideARangeStillConflicts(t *testing.T) {
	for name, s := range sessions("abcdef") {
		s.Insert(User, 2, "WXYZ")
		s.Insert(Agent, 4, "..") // another author, so User's undo cannot take it
		if s.Undo(User) {
			t.Errorf("%s: undo accepted over a live edit inside its range", name)
		}
		if got := text(s); got != "abWX..YZcdef" {
			t.Errorf("%s: document changed on a refused undo: %q", name, got)
		}
	}
}
