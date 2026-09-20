package piecetable

import "testing"

// A restored journal is data on disk, so a cycle in its reversers is a
// malformed log rather than an impossibility, and live() must not follow it
// forever. The cycle is two ops that each claim to reverse the other -- a
// shape no well-formed journal can produce, because a reverser is always a
// later op, but one a truncated or hand-edited log can encode. Without the
// visited set live() recurses between them until the stack overflows and the
// editor dies on startup.
func TestRestoredLiveTerminatesOnACyclicJournal(t *testing.T) {
	buf := NewDoc("hello", 4)
	s := NewRestoredSession(buf, nil, nil, 5)

	// seq 0 is an ordinary-looking edit, seq 1 a reversal of it, and then the
	// two are made to reverse each other. Reflexivity alone (an op listed as
	// its own reverser) is the smallest form of the same cycle.
	s.journal = []Op{
		{Seq: 0, Kind: KindEdit, Pos: 0, Ins: []PieceRec{{Buf: 0, Start: 0, Length: 5}}},
		{Seq: 1, Kind: KindUndo, Undoes: 0},
	}
	s.reversers = map[Version][]Version{
		0: {1},
		1: {0}, // the malformed back-edge: 0 claims to reverse 1
	}

	// The contract is only that this returns; what it answers for a journal
	// nobody could have written is not interesting. If it hangs, the test
	// times out rather than failing an assertion.
	_ = s.live(0)
	_ = s.live(1)
}

// The reflexive form: an op that lists itself as its own reverser. It is the
// same defect with one node, and it is worth pinning separately because a
// visited set that is seeded before the loop trips this one too, while a
// visited set that only records the recursed node would not.
func TestRestoredLiveTerminatesOnASelfReversingOp(t *testing.T) {
	buf := NewDoc("hello", 4)
	s := NewRestoredSession(buf, nil, nil, 5)
	s.journal = []Op{{Seq: 0, Kind: KindEdit, Pos: 0, Ins: []PieceRec{{Buf: 0, Start: 0, Length: 5}}}}
	s.reversers = map[Version][]Version{0: {0}}

	_ = s.live(0)
}
