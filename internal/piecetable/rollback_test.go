package piecetable

import (
	"strings"
	"testing"
)

// A refused group reversal must leave the document exactly as it was before the
// attempt: no half-decided group, no half-reversed text. The interesting path
// is not the first member failing -- rollback then has nothing to undo -- but a
// later member failing after earlier ones already committed. reverseGroupBlock
// walks the group's members newest first, so the group below is built with the
// wedged member OLDER than the placeable one: the placeable member's inverse
// commits first and the wedged member then refuses, which is the one shape that
// exercises rollback's loop.
//
// The journal is append-only, so "as before" means the document, the change
// sets and the liveness of the members, not the version: the rollback records
// the re-reversal as new ops rather than rewinding.
func TestRollbackLeavesNoHalfReversedGroup(t *testing.T) {
	s := groupSession(t, "hello world\n")
	const wedgedGroup = 7

	// The wedged member, committed first (oldest) with a Pos past the
	// document. insertRecs clamps the splice to EOF, so the text lands but the
	// op's recorded Pos does not, and rebasedInverseAt refuses it -- the state
	// a hunk accepted against the wrong base leaves behind. This is the same
	// fixture TestClearRejectedRefusesAWedgedSet builds through ApplyDiff.
	wedged := s.Store().Append(Agent, []byte("W"))
	s.commitInto(Op{Author: Agent, Pos: 400, Ins: []PieceRec{{Buf: int(Agent), Start: wedged, Length: 1}}}, wedgedGroup)
	wedgedSeq := s.journal[len(s.journal)-1].Seq

	// A later, placeable member of the same group. It is the one the reversal
	// walks first, commits, and then has to roll back.
	placeable := s.Store().Append(Agent, []byte("x"))
	s.commitInto(Op{Author: Agent, Pos: 0, Ins: []PieceRec{{Buf: int(Agent), Start: placeable, Length: 1}}}, wedgedGroup)
	placeableSeq := s.journal[len(s.journal)-1].Seq

	before := text(s)
	state := s.GroupState(wedgedGroup)

	if s.reverseGroup(wedgedGroup, KindUndo, nil) {
		t.Fatal("reversing a group with an unplaceable member reported success")
	}

	if got := text(s); got != before {
		t.Fatalf("a refused reversal changed the document: %q, want %q", got, before)
	}
	if got := s.GroupState(wedgedGroup); got != state {
		t.Fatalf("a refused reversal moved the group state: %v, want %v", got, state)
	}
	// Both members must be live again: the placeable member's committed
	// reversal has to have been taken back, and the wedged one's refusal must
	// not have left a decision half-made.
	if !s.live(wedgedSeq) {
		t.Error("the wedged member was left dead by a refused reversal")
	}
	if !s.live(placeableSeq) {
		t.Error("the placeable member's committed reversal was not rolled back")
	}
	// The explicit check the text equality makes easy to miss: the placeable
	// member inserted "x" at offset 0, and if its revocation were not rolled
	// back the document would start without it.
	if !strings.HasPrefix(text(s), "x") {
		t.Fatalf("the placeable member's committed reversal was not rolled back: %q", text(s))
	}
}
