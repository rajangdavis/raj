package piecetable

import "testing"

// snapshotFixture builds the state the round trip must preserve: base text, a
// user insert, an agent edit that is Proposed across an insert and a delete, a
// later agent insert that is Rejected, and an undo of that rejected insert.
func snapshotFixture(t *testing.T) (*Session, uint64, uint64) {
	t.Helper()
	s := NewSession(NewDoc("hello world\nsecond line\n", 0))
	s.Insert(User, 0, "A")
	s.Begin()
	s.Insert(Agent, 6, "XX")
	s.Delete(Agent, 13, 3)
	s.End()
	prop := s.LastGroup()
	s.MarkGroup(prop, Proposed)
	s.Begin()
	s.Insert(Agent, s.Buffer().Len(), "tail")
	s.End()
	rej := s.LastGroup()
	s.MarkGroup(rej, Rejected)
	if !s.Undo(Agent) {
		t.Fatal("undo of the newest agent edit failed")
	}
	return s, prop, rej
}

// Snapshot then Restore must reproduce the version, both projections and every
// group decision exactly. This is the whole point of the snapshot: a second
// process renders the same document state without the original session.
func TestSnapshotRoundTripsExactly(t *testing.T) {
	s, prop, rej := snapshotFixture(t)
	wantVersion := s.Version()
	wantEdits := s.Project(AcceptedAndProposed).Text()
	wantAnnotated := s.Project(Annotated).Text()
	if s.GroupState(prop) != Proposed || s.GroupState(rej) != Rejected {
		t.Fatalf("fixture states = %v/%v, want proposed/rejected", s.GroupState(prop), s.GroupState(rej))
	}

	snap, err := s.SnapshotState()
	if err != nil {
		t.Fatal(err)
	}
	data, err := snap.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}

	if got.Version() != wantVersion {
		t.Errorf("version = %d, want %d", got.Version(), wantVersion)
	}
	if txt := got.Project(AcceptedAndProposed).Text(); txt != wantEdits {
		t.Errorf("edit projection = %q, want %q", txt, wantEdits)
	}
	if txt := got.Project(Annotated).Text(); txt != wantAnnotated {
		t.Errorf("annotated projection = %q, want %q", txt, wantAnnotated)
	}
	if got.GroupState(prop) != Proposed {
		t.Errorf("proposed set state = %v, want Proposed", got.GroupState(prop))
	}
	if got.GroupState(rej) != Rejected {
		t.Errorf("rejected set survived as %v, want Rejected", got.GroupState(rej))
	}
	if len(got.Groups()) != len(s.Groups()) {
		t.Errorf("groups = %d, want %d", len(got.Groups()), len(s.Groups()))
	}
}

// A snapshot that is truncated, not JSON, the wrong version, or whose journal
// points outside the store is refused with an error rather than panicking in
// the piece tree.
func TestSnapshotRefusesGarbage(t *testing.T) {
	if _, err := DecodeSnapshot([]byte(`{"version":1,"base":"!!"`)); err == nil {
		t.Error("truncated JSON accepted")
	}
	if _, err := DecodeSnapshot([]byte("not a snapshot")); err == nil {
		t.Error("non-JSON accepted")
	}

	s := NewSession(NewDoc("x", 0))
	snap, err := s.SnapshotState()
	if err != nil {
		t.Fatal(err)
	}
	wrong := snap
	wrong.Version = 999
	if _, err := Restore(wrong); err == nil {
		t.Error("unknown snapshot version accepted")
	}
	bad := snap
	bad.Journal = []Op{{Seq: 0, Del: []PieceRec{{Buf: 99, Start: 0, Length: 1}}}}
	if _, err := Restore(bad); err == nil {
		t.Error("out-of-store piece accepted")
	}
	mismatch := snap
	mismatch.Base = []byte("y")
	if _, err := Restore(mismatch); err == nil {
		t.Error("base/store mismatch accepted")
	}
}
