package piecetable

import (
	"slices"
	"testing"
)

// compactSessions is the two-implementation arrangement newBuffers uses, so a
// compaction rule is checked against both the oracle-shaped Naive and the tree.
func compactSessions(orig string) map[string]*Session {
	return map[string]*Session{
		"naive": NewSession(NewNaive(orig)),
		"doc":   NewSession(NewDoc(orig, 0)),
	}
}

func sessionText(s *Session) string { return s.Buffer().Slice(0, s.Buffer().Len()) }

// fragmentedAccepted returns a session whose Agent change set is two adjacent
// pieces with non-contiguous store ranges: one group writes "XYPQ" as two
// appends, then a User deletion takes the seam "YP" out, leaving "X" and "Q"
// side by side in the document but A:0-1 and A:3-4 in the store. It returns the
// session and the Agent group id.
func fragmentedAccepted() (*Session, uint64) {
	s := NewSession(NewNaive("0123456789"))
	s.Begin()
	s.Insert(Agent, 0, "XY")
	s.Insert(Agent, 2, "PQ")
	s.End()
	g := s.LastGroup()
	s.Delete(User, 1, 2)
	return s, g
}

// TestCompactMergesAdjacentSameAuthorPieces mirrors TestSpansMergeSameAuthor:
// two adjacent runs by one author become one piece, and the composed text is
// byte-identical. Without the merge branch Compact would remove nothing and
// the piece count would stay at two.
func TestCompactMergesAdjacentSameAuthorPieces(t *testing.T) {
	for name, s := range compactSessions("") {
		s.Begin()
		s.Insert(Agent, 0, "hello ")
		s.Insert(Agent, 6, "world")
		s.End()
		before := sessionText(s)
		if got := s.Buffer().Pieces(); got != 2 {
			t.Fatalf("%s: precondition pieces = %d, want 2", name, got)
		}
		if removed := s.Compact(s.Version()); removed != 1 {
			t.Fatalf("%s: Compact removed %d, want 1", name, removed)
		}
		if got := s.Buffer().Pieces(); got != 1 {
			t.Fatalf("%s: pieces after = %d, want 1", name, got)
		}
		if got := sessionText(s); got != before {
			t.Fatalf("%s: text changed to %q, want %q", name, got, before)
		}
		// Provenance survives the merge: the one piece still resolves to the
		// group that wrote it.
		runs := s.Project(Annotated).States()
		if len(runs) != 1 || runs[0].Group == 0 || runs[0].State != Accepted {
			t.Fatalf("%s: states = %+v, want one Accepted run with a real group", name, runs)
		}
	}
}

// TestCompactFlattensSavedCommittedSpan is the flatten sibling of the merge
// test: an Accepted fragmented span is copied into one contiguous piece once it
// is saved, with a compacted origin recording its group.
func TestCompactFlattensSavedCommittedSpan(t *testing.T) {
	s, g := fragmentedAccepted()
	before := sessionText(s)
	if got := s.Buffer().Pieces(); got != 3 {
		t.Fatalf("precondition pieces = %d, want 3", got)
	}
	if removed := s.Compact(s.Version()); removed != 1 {
		t.Fatalf("Compact removed %d, want 1", removed)
	}
	if got := s.Buffer().Pieces(); got != 2 {
		t.Fatalf("pieces after = %d, want 2", got)
	}
	if got := sessionText(s); got != before {
		t.Fatalf("text changed to %q, want %q", got, before)
	}
	if len(s.compacted) != 1 || s.compacted[0].group != g {
		t.Fatalf("compacted origins = %+v, want one for group %d", s.compacted, g)
	}
	runs := s.Project(Annotated).States()
	if len(runs) != 2 || runs[0].Group != g || runs[0].State != Accepted {
		t.Fatalf("states = %+v, want an Accepted run for group %d first", runs, g)
	}
}

// TestCompactDoesNotFlattenPendingOrRejected mirrors TestDiffPending projects
// the surviving runs: a set a decision still owns keeps every piece, and its
// group and author stay listed. The same fixture flattens when Accepted, so a
// rule that ignored the decision would remove a piece here too.
func TestCompactDoesNotFlattenPendingOrRejected(t *testing.T) {
	for _, st := range []GroupState{Proposed, Rejected} {
		s, g := fragmentedAccepted()
		s.MarkGroup(g, st)
		before := s.Buffer().Pieces()
		if removed := s.Compact(s.Version()); removed != 0 {
			t.Fatalf("%v: Compact removed %d pieces, want 0", st, removed)
		}
		if got := s.Buffer().Pieces(); got != before {
			t.Fatalf("%v: pieces = %d, want %d untouched", st, got, before)
		}
		var found bool
		for _, grp := range s.Groups() {
			if grp.ID == g {
				found = true
				if grp.Author != Agent {
					t.Fatalf("%v: group author = %v, want Agent", st, grp.Author)
				}
			}
		}
		if !found {
			t.Fatalf("%v: group %d no longer listed", st, g)
		}
		var runs []StateRun
		for _, r := range s.Project(Annotated).States() {
			if r.Group == g {
				runs = append(runs, r)
			}
		}
		if len(runs) != 1 || runs[0].State != st {
			t.Fatalf("%v: states for group %d = %+v, want one %v run", st, g, runs, st)
		}
	}
}

// TestCompactIsIdempotent: a second pass with the same save line finds nothing
// to fold and leaves the document exactly as the first pass did.
func TestCompactIsIdempotent(t *testing.T) {
	s, _ := fragmentedAccepted()
	if removed := s.Compact(s.Version()); removed == 0 {
		t.Fatalf("first Compact removed 0, want a change")
	}
	pieces, text := s.Buffer().Pieces(), sessionText(s)
	if removed := s.Compact(s.Version()); removed != 0 {
		t.Fatalf("second Compact removed %d, want 0", removed)
	}
	if s.Buffer().Pieces() != pieces || sessionText(s) != text {
		t.Fatalf("second Compact changed the document: pieces %d->%d, text %q->%q",
			pieces, s.Buffer().Pieces(), text, sessionText(s))
	}
}

// TestCompactKeepsProjectionOracle is the FuzzProjectAgainstOracle/checkSegments
// sibling: a journaled session is compacted and the projection still agrees with
// an independent byte-level fold under every policy, with the composition text
// and the Annotated state runs byte-for-byte what they were before. A merge rule
// that dropped Accepted provenance would show up as a state run with group 0.
func TestCompactKeepsProjectionOracle(t *testing.T) {
	const orig = "0123456789"
	for name, s := range compactSessions(orig) {
		// g1: Accepted and fragmented by a later deletion.
		s.Begin()
		s.Insert(Agent, 0, "XY")
		s.Insert(Agent, 2, "PQ")
		s.End()
		s.Delete(User, 1, 2)
		// g2 Proposed and g3 Rejected, so Project takes the decisions path and
		// the compacted origin has to coexist with pending provenance.
		s.Insert(Agent, 0, "zz")
		s.MarkGroup(s.LastGroup(), Proposed)
		s.Insert(Agent, s.Buffer().Len(), "!!")
		s.MarkGroup(s.LastGroup(), Rejected)

		policies := []Policy{AcceptedOnly, AcceptedAndProposed, Annotated}
		beforeText := map[Policy]string{}
		for _, p := range policies {
			beforeText[p] = s.Project(p).Text()
		}
		beforeRuns := append([]StateRun(nil), s.Project(Annotated).States()...)

		if removed := s.Compact(s.Version()); removed == 0 {
			t.Fatalf("%s: Compact removed 0, want a change", name)
		}
		for _, p := range policies {
			checkSegments(t, s, p)
			proj := s.Project(p)
			if got := proj.Text(); got != beforeText[p] {
				t.Fatalf("%s %v: composition changed to %q, want %q", name, p, got, beforeText[p])
			}
			orc := foldProjectOracle(orig, s, p)
			if got := proj.Text(); got != orc.text {
				t.Fatalf("%s %v: composition %q, oracle %q", name, p, got, orc.text)
			}
			if p != Annotated {
				continue
			}
			if got := proj.States(); !slices.Equal(got, beforeRuns) {
				t.Fatalf("%s: states changed to %+v, want %+v", name, got, beforeRuns)
			}
			if got, want := proj.States(), oracleRuns(orc.groups, s); !slices.Equal(got, want) {
				t.Fatalf("%s: states = %+v, oracle %+v", name, got, want)
			}
		}
	}
}

// TestCompactDoesNotFlattenUnsavedSpan: the same fragmented Accepted span that
// flattens once saved is left alone when the save line is before it. The
// decision gate is not enough on its own; an unsaved accepted set must keep its
// pieces because a restart replays the journal and never sees the copy.
func TestCompactDoesNotFlattenUnsavedSpan(t *testing.T) {
	s, g := fragmentedAccepted()
	before := s.Buffer().Pieces()
	if removed := s.Compact(0); removed != 0 {
		t.Fatalf("Compact(0) removed %d pieces, want 0", removed)
	}
	if got := s.Buffer().Pieces(); got != before {
		t.Fatalf("pieces = %d, want %d untouched", got, before)
	}
	if len(s.compacted) != 0 {
		t.Fatalf("compacted origins = %+v, want none before a save", s.compacted)
	}
	if runs := s.Project(Annotated).States(); len(runs) != 2 || runs[0].Group != g {
		t.Fatalf("states = %+v, want the two-piece group %d intact", runs, g)
	}
}
