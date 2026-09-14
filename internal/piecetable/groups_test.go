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

// A reject is a pure state flip: the text stays in the document and only the
// decision changes. It can address an older change without depending on
// anything landing after it, and it cannot fail.
func TestRejectMarksWithoutTouchingText(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "AAA")
	id := s.LastGroup()
	s.Insert(User, 8, "BBB") // after the agent's text, untouched by the decision
	before := text(s)
	if before != "AAAhelloBBB\n" {
		t.Fatalf("setup produced %q", before)
	}

	if !s.RejectGroup(id) {
		t.Fatal("rejecting the older group failed")
	}
	if got := text(s); got != before {
		t.Errorf("after rejecting = %q, want the text unchanged", got)
	}
	if s.GroupState(id) != Rejected {
		t.Errorf("state = %v, want rejected", s.GroupState(id))
	}
	// The agreed composition drops the rejected set; the review view keeps it.
	if got := s.Project(AcceptedOnly).Text(); got != "helloBBB\n" {
		t.Errorf("agreed composition = %q, want the rejected text absent", got)
	}
	if got := s.Project(Annotated).Text(); got != before {
		t.Errorf("review composition = %q, want the rejected text present", got)
	}
}

// A rejected group is not rejected twice: the second call has no state change
// to report and leaves the text alone.
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

// A reject addresses a change set the journal actually holds. An id nothing
// wrote must not be marked rejected, and the call reports that no state
// changed.
func TestRejectUnknownGroupIsRefused(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	before := text(s)

	if s.RejectGroup(id + 100) {
		t.Error("rejecting an id nothing wrote reported a state change")
	}
	if got := s.GroupState(id + 100); got != Accepted {
		t.Errorf("unknown group state = %v, want accepted (nothing marked)", got)
	}
	if got := text(s); got != before {
		t.Errorf("text = %q, want it untouched", got)
	}
}

// Accepting a rejected set un-rejects it: decisions are a toggle, and the text
// was in the document the whole time.
func TestAcceptUnrejectsARejectedGroup(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	before := text(s)
	s.RejectGroup(id)
	s.AcceptGroup(id)
	if s.GroupState(id) != Accepted {
		t.Errorf("state = %v, want accepted", s.GroupState(id))
	}
	if got := text(s); got != before {
		t.Errorf("text = %q, want %q", got, before)
	}
}

// A rejected group appears, keeps its decision and keeps its live ops: the
// text is still in the document, so the listing must show it.
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
		if g.Ops != 1 {
			t.Errorf("ops = %d, want the still-live member", g.Ops)
		}
	}
	if !found {
		t.Error("a rejected group vanished from the listing")
	}
	if got := text(s); got != "Xhello\n" {
		t.Errorf("text = %q, want the rejected text present", got)
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

// A rejected group is not pending: it is a decision already made, even though
// its text is still in the document.
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

// Clearing is the operation that removes rejected text, and it backs out the
// whole change set — including the parts a second author contributed to it.
//
// This is the case the old author-selected rejection could not express: it took
// the group's author and reversed only that author's members, so a group
// written by two hands came half out.
func TestClearRejectedBacksOutTheWholeGroup(t *testing.T) {
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
	// A reject only marks; the text waits for the clear gesture.
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if got := text(s); got != "ABhello\n" {
		t.Errorf("after rejecting = %q, want the text still present", got)
	}
	if !s.ClearRejected(id) {
		t.Fatal("clear failed")
	}
	if got := text(s); got != "hello\n" {
		t.Errorf("after clearing = %q, want the whole change set gone", got)
	}
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state = %v after clearing, want the decision dropped", got)
	}
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after clearing, want none", got)
	}
}

// ClearRejected acts only on a set currently rejected: on anything else it
// reports false and touches no text.
func TestClearRejectedOnlyActsOnRejected(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	before := text(s)

	if s.ClearRejected(id) {
		t.Error("clearing an accepted set reported success")
	}
	if got := text(s); got != before {
		t.Errorf("text = %q, want it unchanged", got)
	}
	s.MarkGroup(id, Proposed)
	if s.ClearRejected(id) {
		t.Error("clearing a proposed set reported success")
	}
	if got := text(s); got != before {
		t.Errorf("text = %q, want it unchanged", got)
	}
}

// A rejected set with nothing live left to reverse — its edits were undone —
// has no reversal to wedge, so the clear gesture succeeds by dropping the
// decision rather than leaving it stuck.
func TestClearRejectedWithNoLiveMembersSucceeds(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	id := s.LastGroup()
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	s.Undo(Agent) // the edit is gone; only the decision remains

	if got := text(s); got != "hello\n" {
		t.Fatalf("setup produced %q", got)
	}
	if !s.ClearRejected(id) {
		t.Fatal("clearing a rejected set with nothing left to reverse failed")
	}
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state = %v after clearing, want the decision dropped", got)
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

// An apply whose hunk lands inside a leased run conflicts, names the set, and
// leaves the document and the version alone. An insertion flush with the run's
// first byte is outside the lease and still lands.
func TestApplyDiffRefusesALease(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	v0 := s.Version()

	_, conflicts := s.ApplyDiff(User, v0, []Hunk{{Start: 8, End: 10, Text: "XX"}})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one lease refusal", conflicts)
	}
	if conflicts[0].Group != id {
		t.Errorf("conflict group = %d, want %d", conflicts[0].Group, id)
	}
	if s.Version() != v0 {
		t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
	}
	if got := text(s); got != "hello socket\n" {
		t.Fatalf("text = %q, want the leased text unchanged", got)
	}

	// The run starts at 6; an insertion there sits before it, not in it.
	_, conflicts = s.ApplyDiff(User, s.Version(), []Hunk{{Start: 6, End: 6, Text: ">"}})
	if len(conflicts) != 0 {
		t.Fatalf("boundary insert conflicted: %+v", conflicts)
	}
	if got := text(s); got != "hello >socket\n" {
		t.Errorf("text = %q, want the boundary insert to land", got)
	}
}

// A writer amending its own pending proposal is not refused by the lease: the
// refinement folds into the same change set, so refining a just-applied edit
// stays one reviewable unit instead of forcing reject -> clear -> re-apply. A
// second amendment to the same set folds too, and Last keeps advancing.
func TestApplyDiffAmendsOwnProposal(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	// Refine the set's own text: "socket" becomes "port".
	_, conflicts := s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("amending own proposal conflicted: %+v", conflicts)
	}
	if got := text(s); got != "hello port\n" {
		t.Errorf("text = %q, want the amendment applied", got)
	}
	gs := s.Groups()
	if len(gs) != 1 {
		t.Fatalf("groups = %+v, want the amendment folded into the one set", gs)
	}
	if gs[0].ID != id || gs[0].Author != Agent {
		t.Errorf("group = %+v, want the original proposal %d", gs[0], id)
	}
	if gs[0].Ops != 2 {
		t.Errorf("ops = %d, want the amendment counted as a member", gs[0].Ops)
	}
	if gs[0].Bytes != -1 {
		t.Errorf("bytes = %d, want the net change -1", gs[0].Bytes)
	}
	if gs[0].Last <= gs[0].First {
		t.Errorf("last = %d, want it advanced past first %d", gs[0].Last, gs[0].First)
	}

	// A later amendment of the same set folds too, and Last keeps advancing.
	_, conflicts = s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 10, Text: "PORT"}})
	if len(conflicts) != 0 {
		t.Fatalf("second amendment conflicted: %+v", conflicts)
	}
	if got := text(s); got != "hello PORT\n" {
		t.Errorf("text = %q after the second amendment", got)
	}
	gs = s.Groups()
	if len(gs) != 1 || gs[0].ID != id || gs[0].Ops != 3 {
		t.Fatalf("groups = %+v, want the three-member set %d", gs, id)
	}
	if gs[0].Last != 2 {
		t.Errorf("last = %d after the second amendment, want 2", gs[0].Last)
	}
}

// The amend exception is narrow: a set the same writer already rejected is not
// resurrected by an edit. Un-rejecting is a decision and removing the text is
// clear, so a new apply into the rejected run still refuses.
func TestApplyDiffRefusesOwnRejectedSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	v0 := s.Version()

	_, conflicts := s.ApplyDiff(Agent, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 1 || conflicts[0].Group != id {
		t.Fatalf("conflicts = %+v, want the rejected set to refuse", conflicts)
	}
	if s.Version() != v0 {
		t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
	}
	if got := text(s); got != "hello socket\n" {
		t.Errorf("text = %q, want the rejected text unchanged", got)
	}
}

// The amend exception is per-set: a hunk that also catches another writer's
// proposed run is not a clean amendment and still refuses. Leased reports only
// the first intersecting run, so the guard has to look for a second one
// (leasedElsewhere); folding the hunk in would overwrite that writer's text.
func TestApplyDiffRefusesAHunkSpanningTwoLeases(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc\n")
	base := s.Version()

	// Two disjoint proposals: the agent's "AAA" and the user's "BBB".
	s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 3, Text: "AAA"}})
	agentSet := s.LastGroup()
	s.MarkGroup(agentSet, Proposed)
	s.ApplyDiff(User, base, []Hunk{{Start: 4, End: 7, Text: "BBB"}})
	userSet := s.LastGroup()
	s.MarkGroup(userSet, Proposed)
	if got := text(s); got != "AAA BBB ccc\n" {
		t.Fatalf("setup produced %q", got)
	}
	v0 := s.Version()

	// The agent's hunk starts in its own set but spans the user's too, so it is
	// refused: the user's run is a lease it does not own.
	_, conflicts := s.ApplyDiff(Agent, v0, []Hunk{{Start: 0, End: 7, Text: "XXX"}})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one refusal for the second lease", conflicts)
	}
	if conflicts[0].Group == 0 {
		t.Errorf("conflict = %+v, want it to name a lease", conflicts[0])
	}
	if s.Version() != v0 {
		t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
	}
	if got := text(s); got != "AAA BBB ccc\n" {
		t.Errorf("text = %q, want both proposals unchanged", got)
	}
}
