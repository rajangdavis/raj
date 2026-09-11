package piecetable

import "testing"

func groupSession(t *testing.T, text string) *Session {
	t.Helper()
	return NewSession(NewDoc(text, 8))
}

// Groups enumerate what Begin/End already tied together, so a change set is
// addressable rather than only reachable by being the most recent thing.
func TestGroupsEnumerateChangeSets(t *testing.T) {
	s := groupSession(t, "hello world\n")
	s.Begin()
	s.Insert(Agent, 0, "// ")
	s.Insert(Agent, 3, "x")
	s.End()
	s.Insert(User, 0, "A")

	gs := s.Groups()
	if len(gs) != 2 {
		t.Fatalf("groups = %+v, want two", gs)
	}
	if gs[0].Author != Agent || gs[0].Ops != 2 {
		t.Errorf("first group = %+v, want the agent's two ops", gs[0])
	}
	if gs[1].Author != User || gs[1].Ops != 1 {
		t.Errorf("second group = %+v", gs[1])
	}
	if gs[0].First >= gs[1].First {
		t.Error("groups are not in journal order")
	}
	if gs[0].Bytes != 4 {
		t.Errorf("bytes = %d, want +4", gs[0].Bytes)
	}
}

// The default is Accepted: someone typed it, there is nothing to agree to.
// State is not inferred from the author, because this layer does not know which
// authors are agents.
func TestGroupStateDefaultsToAccepted(t *testing.T) {
	s := groupSession(t, "x")
	s.Insert(Agent, 0, "a")
	id := s.LastGroup()
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state = %v, want accepted", got)
	}
	s.MarkGroup(id, Proposed)
	if got := s.GroupState(id); got != Proposed {
		t.Errorf("state = %v after marking", got)
	}
	s.AcceptGroup(id)
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state = %v after accepting", got)
	}
}

// Rejection is undo addressed by group rather than by recency: an older change
// can be backed out while newer ones stay.
func TestRejectAnOlderGroup(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "AAA")
	first := s.LastGroup()
	s.Insert(User, 8, "BBB") // after the agent's text, untouched by removing it
	before := text(s)
	if before != "AAAhelloBBB\n" {
		t.Fatalf("setup produced %q", before)
	}

	if !s.RejectGroup(first) {
		t.Fatal("rejecting the older group failed")
	}
	if got := text(s); got != "helloBBB\n" {
		t.Errorf("after rejecting = %q, want the later change kept", got)
	}
	if s.GroupState(first) != Rejected {
		t.Errorf("state = %v", s.GroupState(first))
	}
}

// A rejected group is not rejected twice. The journal is append-only, so a
// second reversal would re-apply the text rather than removing it again.
func TestRejectIsNotRepeatable(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	if !s.RejectGroup(id) {
		t.Fatal("first reject failed")
	}
	after := text(s)
	if s.RejectGroup(id) {
		t.Error("rejecting twice was allowed")
	}
	if got := text(s); got != after {
		t.Errorf("text changed on the second reject: %q", got)
	}
}

// Accepting something already reversed would claim text that is not there.
func TestAcceptDoesNotResurrectARejectedGroup(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	s.RejectGroup(id)
	s.AcceptGroup(id)
	if s.GroupState(id) != Rejected {
		t.Errorf("state = %v, want it to stay rejected", s.GroupState(id))
	}
	if got := text(s); got != "hello\n" {
		t.Errorf("text = %q", got)
	}
}

// A reversed group still appears — the journal is append-only and the decision
// is part of the record — but contributes no live ops.
func TestRejectedGroupsStayListed(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	s.RejectGroup(id)

	var found bool
	for _, g := range s.Groups() {
		if g.ID != id {
			continue
		}
		found = true
		if g.State != Rejected {
			t.Errorf("state = %v", g.State)
		}
		if g.Ops != 0 {
			t.Errorf("ops = %d, want none live", g.Ops)
		}
	}
	if !found {
		t.Error("a rejected group vanished from the listing")
	}
}

// Undo and redo are how a group is reversed, not change sets anyone proposed.
// Counting them would make one rejected group look like two.
func TestUndoOpsAreNotGroups(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(User, 0, "X")
	before := len(s.Groups())
	s.Undo(User)
	if got := len(s.Groups()); got != before {
		t.Errorf("groups = %d after undo, want %d", got, before)
	}
}

// Pending is what a save has to consult: the change sets still awaiting a
// decision, and only those.
func TestPendingListsOnlyUndecidedGroups(t *testing.T) {
	s := groupSession(t, "hello\n")

	s.Insert(User, 0, "A") // the user typing: accepted by default
	s.Insert(Agent, 0, "B")
	proposed := s.LastGroup()
	s.MarkGroup(proposed, Proposed)

	pending := s.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want just the proposed group", pending)
	}
	if pending[0].ID != proposed {
		t.Errorf("pending group = %d, want %d", pending[0].ID, proposed)
	}

	s.AcceptGroup(proposed)
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after accepting, want none", got)
	}
}

// A proposed group that has been rejected is no longer pending. Its ops are in
// the journal — that is append-only — but they are not in the text, so blocking
// a save on it would block on a change nobody can see.
func TestPendingIgnoresRejectedGroups(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "B")
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after rejecting, want none", got)
	}
}

// AcceptPending is the bulk form the user's save uses. It clears everything and
// says how much it cleared, and it leaves the text alone.
func TestAcceptPendingClearsEverything(t *testing.T) {
	s := groupSession(t, "hello\n")
	for i := 0; i < 3; i++ {
		s.Insert(Agent, 0, "x")
		s.MarkGroup(s.LastGroup(), Proposed)
	}
	before := text(s)

	if n := s.AcceptPending(); n != 3 {
		t.Errorf("accepted %d, want 3", n)
	}
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v, want none", got)
	}
	if got := text(s); got != before {
		t.Errorf("accepting changed the text: %q, want %q", got, before)
	}
	if n := s.AcceptPending(); n != 0 {
		t.Errorf("second call accepted %d, want 0", n)
	}
}

// Rejecting is a decision about a proposal, not about a person, so it backs out
// the whole change set — including the parts a second author contributed to it.
//
// This is the case the old author-selected form could not express: it took the
// group's author and reversed only that author's members, so a group written by
// two hands came half out.
func TestRejectBacksOutTheWholeGroup(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Begin()
	s.Insert(Agent, 0, "A")
	s.Insert(User, 1, "B")
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	if got := text(s); got != "ABhello\n" {
		t.Fatalf("setup produced %q", got)
	}
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if got := text(s); got != "hello\n" {
		t.Errorf("after rejecting = %q, want the whole change set gone", got)
	}
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after rejecting, want none", got)
	}
}

// Undo is the other half of that split, and it stays personal: pressing undo
// must not back out a collaborator's op because it happens to share a group.
func TestUndoInASharedGroupTouchesOnlyYourOwnOps(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Begin()
	s.Insert(Agent, 0, "A")
	s.Insert(User, 1, "B")
	s.End()

	if !s.Undo(User) {
		t.Fatal("undo failed")
	}
	if got := text(s); got != "Ahello\n" {
		t.Errorf("undo = %q, want only the user's insertion removed", got)
	}
}

// DiffPending renders a proposed change set as old→new text: the span each
// member occupies now, the bytes it removed, and the bytes it wrote.
func TestDiffPendingRendersOldAndNew(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	diffs := s.DiffPending()
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the one proposed group", diffs)
	}
	d := diffs[0]
	if d.Moved != 0 || len(d.Hunks) != 1 {
		t.Fatalf("diff = %+v, want one placed hunk", d)
	}
	hk := d.Hunks[0]
	if hk.Old != "world" || hk.New != "socket" {
		t.Errorf("hunk old/new = %q/%q, want world/socket", hk.Old, hk.New)
	}
	if hk.Start != 6 || hk.End != 6+len("socket") {
		t.Errorf("hunk span = %d..%d, want 6..12", hk.Start, hk.End)
	}

	// Accepted, the set is no longer pending and the diff is clean.
	s.AcceptGroup(d.Group.ID)
	if got := s.DiffPending(); len(got) != 0 {
		t.Errorf("diffs after accepting = %+v, want none", got)
	}
}

// A pure deletion reports its removed text as old with an empty new, and its
// span collapses to the point where the text was.
func TestDiffPendingRendersADeletion(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 5, End: 11, Text: ""}})
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	diffs := s.DiffPending()
	if len(diffs) != 1 || len(diffs[0].Hunks) != 1 {
		t.Fatalf("diffs = %+v, want one group with one hunk", diffs)
	}
	hk := diffs[0].Hunks[0]
	if hk.Old != " world" || hk.New != "" {
		t.Errorf("hunk old/new = %q/%q, want %q/empty", hk.Old, hk.New, " world")
	}
	if hk.Start != 5 || hk.End != 5 {
		t.Errorf("deletion span = %d..%d, want the point 5..5", hk.Start, hk.End)
	}
}

// An additive edit inside a proposed span does not erase the member: the runs
// the agent wrote on either side of the user's text survive, in the present,
// and the set stays reachable.
func TestDiffPendingProjectsSurvivingRuns(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	// The user types inside the proposed text. The bytes on either side are
	// still the agent's; the typed bytes are the user's.
	s.Insert(User, 8, "XYZ")

	diffs := s.DiffPending()
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the one proposed group", diffs)
	}
	d := diffs[0]
	if d.Moved != 0 {
		t.Errorf("moved = %d, want 0: the surrounding runs survive", d.Moved)
	}
	if len(d.Hunks) != 2 {
		t.Fatalf("hunks = %+v, want the two surviving runs", d.Hunks)
	}
	if got := d.Hunks[0]; got.Start != 6 || got.End != 8 || got.New != "so" || got.Old != "world" {
		t.Errorf("first run = %+v, want 6..8 %q with old %q", got, "so", "world")
	}
	if got := d.Hunks[1]; got.Start != 11 || got.End != 15 || got.New != "cket" || got.Old != "" {
		t.Errorf("second run = %+v, want 11..15 %q with no old side", got, "cket")
	}
	if pending := s.Pending(); len(pending) != 1 || pending[0].ID != id {
		t.Errorf("pending = %+v, want the set still reachable", pending)
	}
}

// A proposal a later edit has entirely overwritten is auto-rejected: every
// agent piece is gone, so Pending drops it and it cannot block a save. Its
// moved member is still reported by the projection, and the state stays
// Proposed as a record.
func TestDiffPendingAutoRejectsOverwrittenSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	// Replace every byte of the inserted text with the user's own.
	s.Delete(User, 6, len("socket"))
	s.Insert(User, 6, "port")

	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v, want the overwritten set auto-rejected", got)
	}
	// The projection still reports it as moved, so a reviewer can see what the
	// buffer moved past even though it no longer blocks a save.
	diffs := s.DiffPending()
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the moved set still reported", diffs)
	}
	if diffs[0].Moved != 1 || len(diffs[0].Hunks) != 0 {
		t.Errorf("diff = %+v, want the overwritten member counted as moved", diffs[0])
	}
	if s.GroupState(id) != Proposed {
		t.Errorf("state = %v, want the record to stay proposed", s.GroupState(id))
	}
	if got := text(s); got != "hello port\n" {
		t.Errorf("text = %q, want the user's replacement", got)
	}
}

// A member whose inserted text is entirely gone is counted as moved, and a set
// with another surviving member keeps that count rather than hiding it.
func TestDiffPendingCountsAMovedMemberAmongSurvivors(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{
		{Start: 0, End: 3, Text: "AAA"},
		{Start: 8, End: 11, Text: "CCC"},
	})
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	// Overwrite the second member's text; the first survives untouched.
	s.Delete(User, 8, 3)
	s.Insert(User, 8, "zzz")

	diffs := s.DiffPending()
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the set kept by its surviving member", diffs)
	}
	d := diffs[0]
	if d.Moved != 1 {
		t.Errorf("moved = %d, want the overwritten member counted", d.Moved)
	}
	if len(d.Hunks) != 1 {
		t.Fatalf("hunks = %+v, want the one surviving member", d.Hunks)
	}
	if got := d.Hunks[0]; got.Start != 0 || got.End != 3 || got.New != "AAA" {
		t.Errorf("surviving hunk = %+v, want 0..3 %q", got, "AAA")
	}
	if pending := s.Pending(); len(pending) != 1 || pending[0].ID != id {
		t.Errorf("pending = %+v, want the set still pending for its survivor", pending)
	}
}

// A partial deletion trims the member instead of dropping it: what is left of
// the inserted text is still the agent's, contiguous and unbroken.
func TestDiffPendingProjectsATrimmedRun(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	// Remove the middle of the inserted text: "so|cke|t" loses "cke".
	s.Delete(User, 8, 3)

	diffs := s.DiffPending()
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the one group", diffs)
	}
	d := diffs[0]
	if d.Moved != 0 || len(d.Hunks) != 1 {
		t.Fatalf("diff = %+v, want one surviving run", d)
	}
	if got := d.Hunks[0]; got.Start != 6 || got.End != 9 || got.New != "sot" || got.Old != "world" {
		t.Errorf("trimmed run = %+v, want 6..9 %q", got, "sot")
	}
}
