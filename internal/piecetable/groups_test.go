package piecetable

import (
	"strings"
	"testing"
)

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

// GroupDiff projects a decided set by the same walk the pending projection
// uses, so an accepted set reports the runs it contributes rather than zero:
// Hunks is what survives in the document now, Moved is what a later edit has
// overwritten. A rejected set is projected the same way — rejecting decides
// the set's standing, not whether its bytes are live.
func TestGroupDiffProjectsDecidedSets(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	id := s.LastGroup()
	s.AcceptGroup(id)

	d, ok := s.GroupDiff(id)
	if !ok {
		t.Fatalf("GroupDiff(%d) = not found", id)
	}
	if d.Group.State != Accepted {
		t.Errorf("state = %v, want accepted", d.Group.State)
	}
	if d.Moved != 0 || len(d.Hunks) != 1 {
		t.Fatalf("accepted diff = %+v, want one placed hunk", d)
	}
	if hk := d.Hunks[0]; hk.Old != "world" || hk.New != "socket" {
		t.Errorf("hunk old/new = %q/%q, want world/socket", hk.Old, hk.New)
	}

	// A rejected set keeps its live bytes, so the projection is unchanged.
	s.MarkGroup(id, Rejected)
	d, ok = s.GroupDiff(id)
	if !ok {
		t.Fatalf("GroupDiff(%d) after rejecting = not found", id)
	}
	if d.Group.State != Rejected {
		t.Errorf("state = %v, want rejected", d.Group.State)
	}
	if d.Moved != 0 || len(d.Hunks) != 1 {
		t.Errorf("rejected diff = %+v, want the same placed hunk", d)
	}
}

// A decided member a later edit overwrote whole still counts as moved, the
// same as a pending one: the counts describe the text, not the decision.
func TestGroupDiffCountsAMovedMemberOfADecidedSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.End()
	id := s.LastGroup()
	s.AcceptGroup(id)

	// Replace every byte of the accepted member's inserted text.
	s.Delete(User, 6, len("socket"))
	s.Insert(User, 6, "port")

	d, ok := s.GroupDiff(id)
	if !ok {
		t.Fatalf("GroupDiff(%d) = not found", id)
	}
	if d.Moved != 1 || len(d.Hunks) != 0 {
		t.Errorf("diff = %+v, want the member counted as moved", d)
	}
	if len(d.MovedHunks) != 1 || d.MovedHunks[0].Old != "world" || d.MovedHunks[0].New != "socket" {
		t.Errorf("moved hunks = %+v, want the member as written", d.MovedHunks)
	}

	// An id the journal never held is refused rather than reported as empty.
	if _, ok := s.GroupDiff(id + 1000); ok {
		t.Errorf("GroupDiff(%d) = found, want false for an unknown set", id+1000)
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

// An apply whose hunk lands inside a Rejected run conflicts, names the set, and
// leaves the document and the version alone. A rejection is locked, unlike an
// advisory Proposed run (TestApplyDiffAppliesOverAnotherAuthorsProposal above).
// An insertion flush with the run's first byte is outside the lease and lands.
func TestApplyDiffRefusesALease(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	v0 := s.Version()

	_, conflicts, _ := s.ApplyDiff(User, v0, []Hunk{{Start: 8, End: 10, Text: "XX"}})
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
	_, conflicts, _ = s.ApplyDiff(User, s.Version(), []Hunk{{Start: 6, End: 6, Text: ">"}})
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
	_, conflicts, _ := s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 12, Text: "port"}})
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
	_, conflicts, _ = s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 10, Text: "PORT"}})
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

	_, conflicts, _ := s.ApplyDiff(Agent, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
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
// proposed run is not a clean amendment and still refuses. The guard reads every
// intersecting Proposed run (proposedSpans), not just the first, so it sees the
// second one whichever order the projection reports; folding the hunk in would
// overwrite that writer's text.
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
	_, conflicts, _ := s.ApplyDiff(Agent, v0, []Hunk{{Start: 0, End: 7, Text: "XXX"}})
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

// A hunk that spans the editing writer's own Proposed set and another writer's
// must refuse whichever run the projection meets first. The advisory lease is
// only for a draft the hunk does not own. The own set is found among all the
// caught Proposed runs, so an own run met first refuses and one met second must
// refuse just the same rather
// than land and silently supersede the writer's own draft. Both orders name the
// writer's own set. Modelled on TestApplyDiffRefusesAHunkSpanningTwoLeases (the
// own+other refusal) and TestApplyDiffWarnsOverAnotherAuthorsProposal (the
// two-draft setup).
func TestApplyDiffRefusesOwnAndOtherProposalEitherOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		own, other Hunk
		setupText  string
	}{
		{"own first", Hunk{Start: 0, End: 3, Text: "AAA"}, Hunk{Start: 8, End: 11, Text: "BBB"}, "AAA bbb BBB\n"},
		{"other first", Hunk{Start: 8, End: 11, Text: "AAA"}, Hunk{Start: 0, End: 3, Text: "BBB"}, "BBB bbb AAA\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := groupSession(t, "aaa bbb ccc\n")
			base := s.Version()

			// The editing author's own proposal, wherever this order puts it.
			if _, c, _ := s.ApplyDiff(Agent, base, []Hunk{tc.own}); len(c) != 0 {
				t.Fatalf("own proposal conflicted: %+v", c)
			}
			ownSet := s.LastGroup()
			s.MarkGroup(ownSet, Proposed)

			// Another writer's proposal on the other run.
			if _, c, _ := s.ApplyDiff(Author(3), s.Version(), []Hunk{tc.other}); len(c) != 0 {
				t.Fatalf("other proposal conflicted: %+v", c)
			}
			otherSet := s.LastGroup()
			s.MarkGroup(otherSet, Proposed)
			if otherSet == ownSet {
				t.Fatalf("setup folded both proposals into one set %d", ownSet)
			}
			if got := text(s); got != tc.setupText {
				t.Fatalf("setup produced %q, want %q", got, tc.setupText)
			}
			v0 := s.Version()

			// The editing author's hunk spans both runs; whichever is met
			// first, it is not a clean amendment and refuses against the own set.
			_, conflicts, warnings := s.ApplyDiff(Agent, v0, []Hunk{{Start: 0, End: 11, Text: "XXX"}})
			if len(conflicts) != 1 {
				t.Fatalf("conflicts = %+v, want one refusal for the own+other span", conflicts)
			}
			if got := conflicts[0].Group; got != ownSet {
				t.Errorf("conflict group = %d, want the writer's own set %d", got, ownSet)
			}
			if got := conflicts[0].Author; got != Agent {
				t.Errorf("conflict author = %d, want the editing author %d", got, Agent)
			}
			if len(warnings) != 0 {
				t.Errorf("warnings = %+v, want none on a refusal", warnings)
			}
			if s.Version() != v0 {
				t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
			}
			if got := text(s); got != tc.setupText {
				t.Errorf("text = %q, want both proposals unchanged", got)
			}
		})
	}
}

// Another writer's Proposed run is a draft, not a wall: an apply over it lands
// in the editing author's own new group, and the overwritten set's member is
// left moved past what a rebase can carry. There is no conflict; the reply
// carries a warning naming the superseded set, which
// TestApplyDiffWarnsOverAnotherAuthorsProposal pins, and the moved count
// remains the review-surface record.
func TestApplyDiffAppliesOverAnotherAuthorsProposal(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	v0 := s.Version()

	_, conflicts, _ := s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the advisory proposal to allow the apply", conflicts)
	}
	if s.Version() <= v0 {
		t.Errorf("version = %d, want it advanced past %d", s.Version(), v0)
	}
	if got := text(s); got != "hello port\n" {
		t.Errorf("text = %q, want the user's overwrite applied", got)
	}
	// The agreed composition is deliberately not asserted here: a proposal
	// whose inserted run an accepted edit consumed is the overlap the Invalid
	// flag names (spec 12.3), so the set's exclusion and its collider are
	// pinned by TestInvalidWhollyOverwrittenProposal instead.

	// The user's op is its own change set, not folded into the agent's.
	gs := s.Groups()
	if len(gs) != 2 {
		t.Fatalf("groups = %+v, want the user's apply to open a second set", gs)
	}
	last := gs[len(gs)-1]
	if last.Author != User || last.ID == id {
		t.Errorf("last group = %+v, want a new set by %d, not the agent's %d", last, User, id)
	}

	// The agent's set survives as a superseded proposal, its member moved.
	diffs := s.DiffPending()
	if len(diffs) != 1 || diffs[0].Group.ID != id {
		t.Fatalf("diffs = %+v, want the agent's set reported as moved", diffs)
	}
	if diffs[0].Moved != 1 || len(diffs[0].Hunks) != 0 {
		t.Errorf("diff = %+v, want the overwritten member counted as moved", diffs[0])
	}
}

// A second writer apply over a first Proposed run succeeds, and the reply names
// the set it moved past: the group, its author and the span the run holds at
// the moment the hunk lands. This is the warning half of the advisory lease.
// Modelled on TestApplyDiffAppliesOverAnotherAuthorsProposal, which pins the
// apply itself.
func TestApplyDiffWarnsOverAnotherAuthorsProposal(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	v0 := s.Version()

	_, conflicts, warnings := s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the advisory proposal to allow the apply", conflicts)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want one naming the superseded set", warnings)
	}
	w := warnings[0]
	if w.Group != id || w.Author != Agent {
		t.Errorf("warning = %+v, want set %d by agent %d", w, id, Agent)
	}
	// The span is the run bounds when the hunk landed: "socket" at 6..12.
	if w.Start != 6 || w.End != 12 {
		t.Errorf("warning span = %d..%d, want the superseded run 6..12", w.Start, w.End)
	}
}

// A single hunk that lands across two different writers' Proposed sets names
// both: reporting only the first would leave the second moved past silently.
// Each set is named once. Modelled on TestApplyDiffRefusesAHunkSpanningTwoLeases
// (the same two-set setup, but with the editing writer owning neither set so
// the hunk lands) and on TestApplyDiffWarnsOverAnotherAuthorsProposal (the
// warning shape).
func TestApplyDiffWarnsOverEveryProposedSet(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc\n")
	base := s.Version()

	// Two disjoint proposals by two different writers.
	s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 3, Text: "AAA"}})
	first := s.LastGroup()
	s.MarkGroup(first, Proposed)
	s.ApplyDiff(Author(3), base, []Hunk{{Start: 4, End: 7, Text: "BBB"}})
	second := s.LastGroup()
	s.MarkGroup(second, Proposed)
	v0 := s.Version()

	// A third writer's single hunk spans both.
	_, conflicts, warnings := s.ApplyDiff(Author(4), v0, []Hunk{{Start: 0, End: 7, Text: "XXX"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want both drafts advisory", conflicts)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %+v, want both superseded sets", warnings)
	}
	byGroup := map[uint64]Block{}
	for _, w := range warnings {
		byGroup[w.Group] = w
	}
	if w, ok := byGroup[first]; !ok || w.Author != Agent {
		t.Errorf("warning for %d = %+v, want it by agent %d", first, w, Agent)
	}
	if w, ok := byGroup[second]; !ok || w.Author != Author(3) {
		t.Errorf("warning for %d = %+v, want it by author 3", second, w)
	}
	if _, ok := byGroup[second]; ok && (byGroup[second].Start != 4 || byGroup[second].End != 7) {
		t.Errorf("warning span for %d = %d..%d, want the second run 4..7",
			second, byGroup[second].Start, byGroup[second].End)
	}
}

// One Proposed set with two surviving runs, caught by two hunks in one batch,
// is named once: the set is superseded once, not once per hunk. Modelled on
// TestApplyDiffWarnsOverAnotherAuthorsProposal, with the two-run set built as
// one Begin/End change set.
func TestApplyDiffWarnsOnceForASetCaughtTwice(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc\n")
	base := s.Version()

	// One proposal, two disjoint runs, one change set.
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{
		{Start: 0, End: 3, Text: "AAA"},
		{Start: 4, End: 7, Text: "BBB"},
	})
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	v0 := s.Version()

	// A second writer's batch touches each run with its own hunk.
	_, conflicts, warnings := s.ApplyDiff(Author(3), v0, []Hunk{
		{Start: 0, End: 3, Text: "XXX"},
		{Start: 4, End: 7, Text: "YYY"},
	})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the draft advisory", conflicts)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one set named once", warnings)
	}
	if warnings[0].Group != id || warnings[0].Author != Agent {
		t.Errorf("warning = %+v, want set %d by agent %d", warnings[0], id, Agent)
	}
}

// Amending a writer own Proposed set is the one advisory overlap that is not a
// warning: the refinement folds into the same set, so no second set is
// superseded. Modelled on TestApplyDiffAmendsOwnProposal.
func TestApplyDiffAmendDoesNotWarn(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	_, conflicts, warnings := s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the amendment to fold in", conflicts)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none for an own-set amendment", warnings)
	}
}

// A clean apply reports no warnings: the advisory channel must stay silent
// unless a hunk really landed over another Proposed span. Modelled on
// TestApplyDiffClean.
func TestApplyDiffCleanHasNoWarnings(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc")
	base := s.Version()
	_, conflicts, warnings := s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 3, Text: "XXX"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want a clean apply", conflicts)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none for a clean apply", warnings)
	}
}

// A Rejected span still refuses, so the overlap that warns for a Proposed draft
// reports a conflict for a decision and no warning at all. The two cases must
// not collapse into one. Modelled on TestApplyDiffRefusesALease.
func TestApplyDiffRefusalIsNotAWarning(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	v0 := s.Version()

	_, conflicts, warnings := s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 1 || conflicts[0].Group != id {
		t.Fatalf("conflicts = %+v, want the rejected set to refuse", conflicts)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v, want none for a refusal", warnings)
	}
	if s.Version() != v0 {
		t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
	}
}

// A Proposed set every member of which a later accepted edit has overwritten is
// invalid: recomputed from the journal, never stored, so a listing names it and
// the live set whose edit now occupies its range. Pending already drops it; the
// flag names the same fact and keeps its inserted text out of both
// compositions. Modelled on TestApplyDiffAppliesOverAnotherAuthorsProposal
// (which pins the superseded member as moved) and on
// TestProjectPoliciesDifferOnProposedGroup (the per-policy split).
func TestInvalidWhollyOverwrittenProposal(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	v0 := s.Version()
	s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	collider := s.LastGroup()

	g, ok := findGroup(s, superseded)
	if !ok {
		t.Fatalf("group %d not listed", superseded)
	}
	if !g.Invalid {
		t.Fatalf("superseded set = %+v, want invalid", g)
	}
	if g.InvalidBy == nil || g.InvalidBy.Group != collider {
		t.Fatalf("invalid by = %+v, want the colliding set %d", g.InvalidBy, collider)
	}
	if g.InvalidBy.Start != 6 || g.InvalidBy.End != 10 {
		t.Errorf("colliding span = %d..%d, want the live run 6..10",
			g.InvalidBy.Start, g.InvalidBy.End)
	}
	for _, p := range s.Pending() {
		if p.ID == superseded {
			t.Errorf("invalid set %d is still pending", superseded)
		}
	}
	if got := s.Project(AcceptedAndProposed).Text(); strings.Contains(got, "socket") {
		t.Errorf("edit composition %q still holds the invalid set's text", got)
	}
	if got := s.Project(AcceptedOnly).Text(); strings.Contains(got, "socket") {
		t.Errorf("agreed composition %q still holds the invalid set's text", got)
	}
}

// A Proposed set only partly overwritten is not invalid: its surviving member
// is still there to accept, and the set must not be labelled with a collider
// just because a sibling member was consumed. Modelled on
// TestApplyDiffAppliesOverAnotherAuthorsProposal, with a two-member set so the
// surviving half of the predicate is exercised rather than a lone member.
func TestPartlyOverwrittenProposalSurvives(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.Begin()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 0, End: 0, Text: "// "}})
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	v0 := s.Version()
	// The user overwrites the first member's inserted run; the second survives
	// ahead of it.
	s.ApplyDiff(User, v0, []Hunk{{Start: 9, End: 15, Text: "port"}})

	if got := text(s); got != "// hello port\n" {
		t.Fatalf("setup produced %q", got)
	}
	g, ok := findGroup(s, id)
	if !ok {
		t.Fatalf("group %d not listed", id)
	}
	if g.Ops != 2 {
		t.Fatalf("group = %+v, want both members live", g)
	}
	if g.Invalid {
		t.Errorf("partly overwritten set = %+v, want not invalid", g)
	}
	if g.InvalidBy != nil {
		t.Errorf("invalid by = %+v, want nil for a surviving set", g.InvalidBy)
	}
	var pending bool
	for _, p := range s.Pending() {
		if p.ID == id {
			pending = true
		}
	}
	if !pending {
		t.Errorf("set %d dropped from Pending though a member survives", id)
	}
}

// Invalid is derived, so clearing the colliding edit clears the flag and the
// superseded set returns as work to decide. Modelled on
// TestClearRejectedBacksOutTheWholeGroup for the reject-then-clear gesture and
// on TestInvalidWhollyOverwrittenProposal for the setup.
func TestInvalidClearsWhenColliderClears(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	v0 := s.Version()
	s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})
	collider := s.LastGroup()

	if g, _ := findGroup(s, superseded); !g.Invalid {
		t.Fatalf("setup: set %d is not invalid", superseded)
	}
	if !s.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}
	if !s.ClearRejected(collider) {
		t.Fatal("clear of the collider failed")
	}
	if got := text(s); got != "hello socket\n" {
		t.Fatalf("after clearing the collider text = %q, want the proposal back", got)
	}
	g, _ := findGroup(s, superseded)
	if g.Invalid {
		t.Errorf("set %d stayed invalid after the colliding edit was cleared", superseded)
	}
	if g.InvalidBy != nil {
		t.Errorf("invalid by = %+v, want nil after the collider cleared", g.InvalidBy)
	}
	var pending bool
	for _, p := range s.Pending() {
		if p.ID == superseded {
			pending = true
		}
	}
	if !pending {
		t.Errorf("set %d did not return to Pending after the collider cleared", superseded)
	}
}

// Naming a set invalid is derived from the same per-member projection the
// composition uses, so it must not perturb the composition: under every policy
// the projection still agrees with the fold oracle where the contract defines
// it, and the run lengths still agree where it does not. Modelled on the
// per-step checks in FuzzProjectAgainstOracle.
func TestInvalidKeepsProjectionInvariants(t *testing.T) {
	const orig = "hello world\n"
	s := groupSession(t, orig)
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	v0 := s.Version()
	s.ApplyDiff(User, v0, []Hunk{{Start: 6, End: 12, Text: "port"}})

	if g, ok := findGroup(s, superseded); !ok || !g.Invalid {
		t.Fatalf("set %d = %+v (listed %v), want invalid", superseded, g, ok)
	}
	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		proj := s.Project(p)
		orc := foldProjectOracle(orig, s, p)
		if got, want := s.Buffer().Slice(0, s.Buffer().Len()), orc.view; got != want {
			t.Fatalf("%v: oracle replay %q diverged from the buffer %q", p, want, got)
		}
		if proj.Len() != len(orc.text) {
			t.Fatalf("%v: Len %d, oracle %d", p, proj.Len(), len(orc.text))
		}
		if orc.overlap {
			continue // the contract does not define the composition here
		}
		checkSegments(t, s, p)
		if got := proj.Text(); got != orc.text {
			t.Fatalf("%v: composition %q, oracle %q", p, got, orc.text)
		}
	}
}

// findGroup returns the listed set with id, or false when the journal holds
// none. Test-local, so the invalid tests read the listing the way a caller
// does.
func findGroup(s *Session, id uint64) (Group, bool) {
	for _, g := range s.Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// A Rejected span is the human's decision, not a draft, so it still refuses an
// apply even though an intersecting Proposed run would be advisory. When one
// hunk catches both, the rejection wins and the conflict names the rejecting
// set, rather than sending the caller to accept a draft that is not the block.
func TestApplyDiffRefusesARejectedLeaseBesideAProposal(t *testing.T) {
	s := groupSession(t, "aaa bbb ccc\n")
	base := s.Version()

	// The agent proposes "AAA"; the user rejects "BBB".
	s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 3, Text: "AAA"}})
	agentSet := s.LastGroup()
	s.MarkGroup(agentSet, Proposed)
	s.ApplyDiff(User, base, []Hunk{{Start: 4, End: 7, Text: "BBB"}})
	userSet := s.LastGroup()
	if !s.RejectGroup(userSet) {
		t.Fatal("reject failed")
	}
	if got := text(s); got != "AAA BBB ccc\n" {
		t.Fatalf("setup produced %q", got)
	}
	v0 := s.Version()

	// The agent's hunk starts in its own Proposed run and spans the rejected
	// one too: the rejection wins.
	_, conflicts, _ := s.ApplyDiff(Agent, v0, []Hunk{{Start: 0, End: 7, Text: "XXX"}})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want the rejection to refuse the hunk", conflicts)
	}
	if conflicts[0].Group != userSet {
		t.Errorf("conflict group = %d, want the rejected set %d, not the proposal %d",
			conflicts[0].Group, userSet, agentSet)
	}
	if s.Version() != v0 {
		t.Errorf("version moved on a refused hunk: %d -> %d", v0, s.Version())
	}
	if got := text(s); got != "AAA BBB ccc\n" {
		t.Errorf("text = %q, want both sets unchanged", got)
	}
}

// The advisory change does not touch the same-author join: a writer amending
// its own Proposed set still folds the op into that set rather than opening a
// second one.
func TestApplyDiffAmendsOwnProposalWithoutANewGroup(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	_, conflicts, _ := s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("amending own proposal conflicted: %+v", conflicts)
	}
	gs := s.Groups()
	if len(gs) != 1 {
		t.Fatalf("groups = %+v, want the amendment to join the one set", gs)
	}
	if gs[0].ID != id || gs[0].Ops != 2 {
		t.Errorf("group = %+v, want set %d with both members", gs[0], id)
	}
	if got := text(s); got != "hello port\n" {
		t.Errorf("text = %q, want the amendment applied", got)
	}
}

// A rejected set whose only member was recorded with an offset past the
// document cannot be reversed: its rebased span lies beyond the text, so the
// inverse's removeRange clamps to a no-op and marking the set Accepted would
// leave the text orphaned under a tombstone no one can remove. ClearRejected
// has to report the wedge instead, and keep the decision.
//
// This is the session-level state the host now refuses to create: a hunk
// submitted against a base it does not fit (offset 5 against the empty base 0)
// is carried to EOF rather than refused, so the op's Pos (16) outlives the
// document it was written in.
func TestClearRejectedRefusesAWedgedSet(t *testing.T) {
	s := groupSession(t, "")
	if _, c, _ := s.ApplyDiff(Agent, 0, []Hunk{{Start: 0, End: 0, Text: "hello world"}}); len(c) != 0 {
		t.Fatalf("setup insert conflicts: %+v", c)
	}
	if _, c, _ := s.ApplyDiff(Agent, 0, []Hunk{{Start: 5, End: 5, Text: "X"}}); len(c) != 0 {
		t.Fatalf("setup offset conflicts: %+v", c)
	}
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)
	if !s.RejectGroup(id) {
		t.Fatal("reject failed")
	}

	if got := text(s); got != "hello worldX" {
		t.Fatalf("setup produced %q, want the X carried to EOF", got)
	}
	// The set still holds a live member; it is only the projection that cannot
	// place it. That is what the old live check mistook for "nothing left".
	if gs := s.Groups(); len(gs) != 2 || gs[1].Ops != 1 {
		t.Fatalf("groups = %+v, want the second set still holding a live member", gs)
	}
	if s.ClearRejected(id) {
		t.Error("clearing a set that cannot be reversed reported success")
	}
	if got := text(s); got != "hello worldX" {
		t.Errorf("text = %q after a refused clear, want it unchanged", got)
	}
	if got := s.GroupState(id); got != Rejected {
		t.Errorf("state = %v after a refused clear, want it to stay rejected", got)
	}
}

// A rejected set can also be wedged by a later live edit that overlaps it, and
// ClearRejectedBlock names that edit's set, author and span so the caller knows
// what to clear first. A pure deletion leases nothing, so a later deletion of
// overlapping original text is the reachable shape of the block.
func TestClearRejectedBlockNamesTheBlocker(t *testing.T) {
	s := groupSession(t, "hello world\n")
	s.Begin()
	s.Delete(Agent, 6, 5) // remove "world"
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Rejected)

	s.Delete(User, 0, 11) // a later edit removes the whole line

	ok, block := s.ClearRejectedBlock(id)
	if ok {
		t.Fatal("clearing a wedged set reported success")
	}
	if block.Group == 0 {
		t.Fatalf("block = %+v, want the blocking set named", block)
	}
	if block.Author != User {
		t.Errorf("block author = %d, want the user's %d", block.Author, User)
	}
	if got := s.GroupState(id); got != Rejected {
		t.Errorf("state = %v, want it kept Rejected", got)
	}
}

// Two pending sets whose rebased member ranges intersect are reported on both,
// with the other's author, and nothing is clamped or merged: the editor does
// not arbitrate.
func TestPendingOverlapsNamesBothSets(t *testing.T) {
	s := groupSession(t, "hello world\n")
	s.Begin()
	s.Insert(Agent, 0, "AA")
	s.End()
	a := s.LastGroup()
	s.MarkGroup(a, Proposed)

	// B inserts inside the span A's member now covers. A direct Insert is used
	// because ApplyDiff would refuse it as a lease; the overlap is the fact
	// under test, not the route to it.
	s.Begin()
	s.Insert(Agent+1, 1, "BB")
	s.End()
	b := s.LastGroup()
	s.MarkGroup(b, Proposed)

	ovs := s.PendingOverlaps()
	if len(ovs[a]) != 1 || ovs[a][0].Group != b || ovs[a][0].Author != Agent+1 {
		t.Errorf("overlaps[%d] = %+v, want set %d by %d", a, ovs[a], b, Agent+1)
	}
	if len(ovs[b]) != 1 || ovs[b][0].Group != a || ovs[b][0].Author != Agent {
		t.Errorf("overlaps[%d] = %+v, want set %d by %d", b, ovs[b], a, Agent)
	}
	if ovs[a][0].Start == ovs[a][0].End {
		t.Errorf("overlap span = %d..%d, want the shared range", ovs[a][0].Start, ovs[a][0].End)
	}
}

// Two sets that abut without sharing a byte are not an overlap. The earlier
// set's range is the text it still owns, not the neighbour's insertion flush
// against its end -- the rebased bounding range would have swallowed it.
func TestPendingOverlapsIgnoresAdjacentSets(t *testing.T) {
	s := groupSession(t, "hello world\n")
	s.Begin()
	s.Insert(Agent, 0, "AA")
	s.End()
	a := s.LastGroup()
	s.MarkGroup(a, Proposed)

	s.Begin()
	s.Insert(Agent+1, 2, "BB")
	s.End()
	b := s.LastGroup()
	s.MarkGroup(b, Proposed)

	if ovs := s.PendingOverlaps(); len(ovs[a]) != 0 || len(ovs[b]) != 0 {
		t.Errorf("adjacent sets reported as overlapping: %+v", ovs)
	}
}

// RevertAuthor is the inverse of attribution: it reverses every live piece the
// named author wrote, keeps everyone else's, and records the reversal in the
// journal rather than holding a second forward edit. A set the revert emptied
// drops its decision, so the writer's own proposal stops being pending.
func TestRevertAuthorDropsItsPiecesKeepsOthers(t *testing.T) {
	s := groupSession(t, "")
	s.Insert(User, 0, "user")    // the user's, first
	s.Insert(Agent, 4, "agent")  // the pieces to drop
	s.Insert(Agent+1, 9, "peer") // a peer's, at the end
	if got := text(s); got != "useragentpeer" {
		t.Fatalf("setup produced %q", got)
	}
	// The agent's set is a proposal; reverting it drops the decision too. It is
	// the second group, after the user's.
	s.MarkGroup(s.Groups()[1].ID, Proposed)

	before := s.Version()
	if ok, _ := s.RevertAuthor(Agent); !ok {
		t.Fatal("revert of a live author reported nothing to do")
	}
	if got := text(s); got != "userpeer" {
		t.Errorf("after revert = %q, want only the agent's pieces gone", got)
	}
	if s.Version() <= before {
		t.Error("revert did not advance the journal")
	}
	// The reversal is recorded: the last op undoes the agent's member rather
	// than being a second insertion beside it.
	last := s.Journal()[s.Version()-1]
	if last.Kind != KindUndo || last.Undoes == 0 {
		t.Errorf("last op = %+v, want a recorded reversal", last)
	}
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after revert, want none", got)
	}
}

// Reverting an author with nothing live to reverse reports false and changes
// nothing, the same shape as a clear of an already-gone set.
func TestRevertAuthorWithNothingLiveReportsFalse(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Insert(Agent, 0, "X")
	if !s.Undo(Agent) {
		t.Fatal("undo failed")
	}
	before := text(s)
	if ok, _ := s.RevertAuthor(Agent); ok {
		t.Error("revert of an author with no live pieces reported success")
	}
	if got := text(s); got != before {
		t.Errorf("text = %q, want it unchanged", got)
	}
}

// A later edit that removes the bytes a member needs wedges the revert. The
// block names the live set whose span overlaps it, so the caller learns what to
// clear first; the reversal is reported rather than clamped, and the text is
// left exactly as it was.
func TestRevertAuthorReportsAWedgedReversal(t *testing.T) {
	s := groupSession(t, "hello world\n")
	s.Begin()
	s.Delete(Agent, 6, 5) // the agent removes "world"
	s.End()
	id := s.LastGroup()

	s.Delete(User, 0, 11) // a later edit removes the whole line

	before := text(s)
	ok, block := s.RevertAuthor(Agent)
	if ok {
		t.Fatal("reverting a wedged author reported success")
	}
	if block.Group == 0 {
		t.Fatalf("block = %+v, want the blocking set named", block)
	}
	if block.Author != User {
		t.Errorf("block author = %d, want the user's %d", block.Author, User)
	}
	if got := text(s); got != before {
		t.Errorf("text = %q after a refused revert, want it unchanged", got)
	}
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state = %v, want the agent's decision untouched", got)
	}
}

// A wedge part-way through the newest-first walk is still a partial reversal:
// the newer set is reversed and dropped before the walk stops on the older one.
// The journal has moved even though ok is false, so the caller cannot read
// false as "nothing happened". This wedge is named (block.Group != 0), which
// File.RevertAuthor syncs for on its own; the version comparison there covers
// only the unnamed case, which the public API cannot build.
func TestRevertAuthorPartialWedgeLeavesNewerSetReversed(t *testing.T) {
	s := groupSession(t, "hello world\n")

	s.Begin()
	s.Delete(Agent, 6, 5) // the older set: removes "world"
	s.End()
	older := s.LastGroup()

	s.Begin()
	s.Insert(Agent, 0, "Z") // the newer set: reversed first, and it lands
	s.End()
	newer := s.LastGroup()
	s.MarkGroup(newer, Proposed)

	s.Delete(User, 6, 2) // the wedge: removes " \n" from "Zhello \n"
	wedge := s.LastGroup()

	if got := text(s); got != "Zhello" {
		t.Fatalf("setup produced %q", got)
	}
	before := s.Version()
	ok, block := s.RevertAuthor(Agent)
	if ok {
		t.Fatal("reverting an author whose older set is wedged reported success")
	}
	// The newer set came out before the walk reached the wedge and stays out;
	// the older set's reversal never ran, so its removal of "world" stands.
	if got := text(s); got != "hello" {
		t.Errorf("after revert = %q, want only the newer set's piece gone", got)
	}
	if s.Version() <= before {
		t.Error("the journal did not move before the wedge; that partial reversal is what the File-level guard has to sync for")
	}
	if !s.hasLiveMembers(older) {
		t.Error("the wedged older set was dropped even though its reversal never ran")
	}
	if got := s.GroupState(newer); got != Accepted {
		t.Errorf("newer set state = %v, want the emptied set's decision dropped", got)
	}
	// The block names the live set the older reversal could not be placed
	// through: the user's group, its author, and the point the wedge sits at
	// now (the newer set's reversal shifted it).
	if block.Group != wedge {
		t.Errorf("block group = %d, want the wedging set %d", block.Group, wedge)
	}
	if block.Author != User {
		t.Errorf("block author = %d, want the user's %d", block.Author, User)
	}
	if block.Start != 5 || block.End != 5 {
		t.Errorf("block span = %d..%d, want the wedging deletion's point 5..5", block.Start, block.End)
	}
}

// A set shared by two writers keeps its decision through a revert: only the
// reverted author's members come out, and because the set still holds the other
// author's live member, hasLiveMembers keeps the decision instead of marking
// the whole set Accepted.
func TestRevertAuthorKeepsASharedGroupsDecision(t *testing.T) {
	s := groupSession(t, "hello\n")
	s.Begin()
	s.Insert(Agent, 0, "A")
	s.Insert(User, 1, "B") // one set, two authors
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	if got := text(s); got != "ABhello\n" {
		t.Fatalf("setup produced %q", got)
	}
	if ok, _ := s.RevertAuthor(Agent); !ok {
		t.Fatal("revert of the shared set reported nothing to do")
	}
	// Only the agent's member is reversed; the user's survives.
	if got := text(s); got != "Bhello\n" {
		t.Errorf("after revert = %q, want only the agent's piece gone", got)
	}
	if !s.hasLiveMembers(id) {
		t.Error("the set reports no live member, but the user's op survives")
	}
	if got := s.GroupState(id); got != Proposed {
		t.Errorf("state = %v, want the shared set's decision kept", got)
	}
	// Still addressable as a pending set: the user's surviving member is what
	// there is left to decide on.
	if got := s.Pending(); len(got) != 1 || got[0].ID != id {
		t.Errorf("pending = %+v, want the shared set %d still pending", got, id)
	}
}

// A superseded proposal whose text a rejected collider restores to the edit
// view is what a save would silently drop: Pending has already let it go, and
// AcceptedOnly never held it. UnsavedProposed names it. Without the comparison
// against AcceptedAndProposed the function would either name every invalid set
// -- including the wholly-consumed one that is honestly absent from both views
// -- or none. Modelled on TestInvalidWhollyOverwrittenProposal for the setup
// and on TestProjectPoliciesDifferOnRejected and friends for the policy split.
func TestUnsavedProposedNamesTheSetASaveWouldDrop(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)

	// The user deletes the whole line; the set's insertion is consumed. The
	// deletion is rejected, so its bytes come back to the session and the
	// projection restores them, which is what separates the two compositions.
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 0, End: 12, Text: ""}})
	collider := s.LastGroup()
	if !s.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}

	if len(s.Pending()) != 0 {
		t.Fatalf("Pending = %+v, want none; the refusal is for a set Pending drops", s.Pending())
	}
	if g, _ := findGroup(s, superseded); !g.Invalid {
		t.Fatalf("set %d = %+v, want invalid", superseded, g)
	}
	got := s.UnsavedProposed()
	if len(got) != 1 || got[0].ID != superseded {
		t.Fatalf("UnsavedProposed = %+v, want just set %d", got, superseded)
	}
	if got[0].State != Proposed {
		t.Errorf("named set state = %v, want Proposed", got[0].State)
	}
}

// UnsavedProposed is silent when the invalid set is absent from the edit view
// too: a wholly consumed insertion is already gone from AcceptedAndProposed, so
// nothing on screen is lost and a save must not be refused. Modelled on
// TestInvalidWhollyOverwrittenProposal, where the collider is accepted and stays.
func TestUnsavedProposedIsSilentWhenNothingIsShown(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	// An accepted edit overwrites the insertion; nothing restores it.
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 6, End: 12, Text: "port"}})

	if g, _ := findGroup(s, superseded); !g.Invalid {
		t.Fatalf("setup: set %d is not invalid", superseded)
	}
	if got := s.UnsavedProposed(); len(got) != 0 {
		t.Errorf("UnsavedProposed = %+v, want none when the text is in neither view", got)
	}
}

// A set that still has a surviving hunk is Pending, so it is not what
// UnsavedProposed names: AcceptPending agrees to it and the text is written. It
// guards against the function reporting the ordinary proposed set a save
// handles. Sibling: TestPendingListsOnlyUndecidedGroups.
func TestUnsavedProposedIgnoresAPendingSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	s.MarkGroup(s.LastGroup(), Proposed)

	if len(s.Pending()) != 1 {
		t.Fatalf("Pending = %+v, want the set", s.Pending())
	}
	if got := s.UnsavedProposed(); len(got) != 0 {
		t.Errorf("UnsavedProposed = %+v, want none for a set a save accepts", got)
	}
}

// InvalidWithoutMembers names the memberless invalid sets a save retires: a
// Proposed set every live member of which is a pure insertion a later edit
// removed, with no rejected collider restoring a run and no removal of its own
// to put back. The set is in neither composition, so retiring it is pure
// bookkeeping. Without the predicate the save has nothing to select and the set
// lingers Proposed forever.
func TestInvalidWithoutMembersNamesTheDeadSets(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 0, Text: "AB"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	// An accepted edit removes the insertion; nothing restores it, and the
	// member removed nothing itself, so the set is in neither composition.
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 0, End: 2, Text: ""}})

	if got := s.UnsavedProposed(); len(got) != 0 {
		t.Fatalf("setup: UnsavedProposed = %+v, want none; the two must not overlap", got)
	}
	if len(s.Pending()) != 0 {
		t.Fatalf("setup: Pending = %+v, want none", s.Pending())
	}
	got := s.InvalidWithoutMembers()
	if len(got) != 1 || got[0].ID != superseded {
		t.Fatalf("InvalidWithoutMembers = %+v, want just set %d", got, superseded)
	}
	if !got[0].Invalid {
		t.Errorf("set %+v is not marked invalid", got[0])
	}
}

// InvalidWithoutMembers is silent when a rejected collider restores the
// superseded run to the edit view: that set is UnsavedProposed's, and a save
// must refuse over it rather than retire it. It pins the distinction the two
// predicates share; without the projection guard the function would hand a set
// holding visible text to the retirement path. Sibling:
// TestUnsavedProposedNamesTheSetASaveWouldDrop.
func TestInvalidWithoutMembersIsSilentWhenTextIsShown(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	// The user deletes the whole line and the deletion is rejected, so its
	// bytes come back to the session and the projection restores them.
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 0, End: 12, Text: ""}})
	collider := s.LastGroup()
	if !s.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}
	if got := s.UnsavedProposed(); len(got) != 1 || got[0].ID != superseded {
		t.Fatalf("setup: UnsavedProposed = %+v, want just set %d", got, superseded)
	}
	if got := s.InvalidWithoutMembers(); len(got) != 0 {
		t.Errorf("InvalidWithoutMembers = %+v, want none while the run is restored", got)
	}
}

// Retiring a memberless invalid set is bookkeeping: it changes no composition,
// so the session text, the edit view and the agreed composition all stay
// byte-for-byte what they were. Without that property the save's retirement
// would silently rewrite the file. Modelled on
// TestClearGroupDisposesASupersededSet for the fixture.
func TestRetiringInvalidWithoutMembersChangesNoText(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 0, End: 0, Text: "AB"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 0, End: 2, Text: ""}})

	if len(s.InvalidWithoutMembers()) != 1 {
		t.Fatalf("setup: InvalidWithoutMembers = %+v, want the dead set", s.InvalidWithoutMembers())
	}
	sessionBefore, editBefore, agreedBefore := text(s),
		s.Project(AcceptedAndProposed).Text(), s.Project(AcceptedOnly).Text()

	for _, g := range s.InvalidWithoutMembers() {
		s.MarkGroup(g.ID, Rejected)
	}

	if got := text(s); got != sessionBefore {
		t.Errorf("session text = %q after retirement, want %q", got, sessionBefore)
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != editBefore {
		t.Errorf("edit composition = %q after retirement, want %q", got, editBefore)
	}
	if got := s.Project(AcceptedOnly).Text(); got != agreedBefore {
		t.Errorf("agreed composition = %q after retirement, want %q", got, agreedBefore)
	}
	if got := s.InvalidWithoutMembers(); len(got) != 0 {
		t.Errorf("InvalidWithoutMembers = %+v, want none after retirement", got)
	}
}

// ClearGroup disposes of a superseded proposal in one gesture. The set is
// Proposed and Pending drops it, so no accept or reject step can reach it, and
// no live member can be reversed; marking it Rejected is the whole disposal and
// takes its restored run out of the edit view. Without the Proposed case the
// set can be neither decided nor dropped -- the clear verb refuses it as "not a
// rejected set" -- which is the wedge this closes. Modelled on
// TestInvalidClearsWhenColliderClears for the setup and on
// TestClearRejectedBacksOutTheWholeGroup for the rejected path.
func TestClearGroupDisposesASupersededSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := s.LastGroup()
	s.MarkGroup(superseded, Proposed)
	s.ApplyDiff(User, s.Version(), []Hunk{{Start: 0, End: 12, Text: ""}})
	collider := s.LastGroup()
	if !s.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}
	if g, _ := findGroup(s, superseded); !g.Invalid {
		t.Fatalf("setup: set %d is not invalid", superseded)
	}
	before := text(s)

	ok, block := s.ClearGroup(superseded)
	if !ok {
		t.Fatalf("ClearGroup of a superseded set = false (block %+v), want one-gesture disposal", block)
	}
	if got := s.GroupState(superseded); got != Rejected {
		t.Errorf("state after clear = %v, want Rejected", got)
	}
	if got := text(s); got != before {
		t.Errorf("session text = %q after a state-only clear, want %q", got, before)
	}
	if edit := s.Project(AcceptedAndProposed).Text(); strings.Contains(edit, "socket") {
		t.Errorf("edit composition = %q, want the restored superseded run gone", edit)
	}
	if g, _ := findGroup(s, superseded); g.Invalid {
		t.Errorf("set %d is still reported invalid after disposal", superseded)
	}
}

// ClearGroup still disposes a genuinely rejected set by reversing it, so the
// new Proposed case did not close the old path. Modelled on
// TestClearRejectedBacksOutTheWholeGroup.
func TestClearGroupStillReversesARejectedSet(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := s.LastGroup()
	s.MarkGroup(id, Rejected)

	ok, _ := s.ClearGroup(id)
	if !ok {
		t.Fatal("ClearGroup of a rejected set failed")
	}
	if got := text(s); got != "hello world\n" {
		t.Errorf("text after clearing the rejected set = %q, want the reversal", got)
	}
	if got := s.GroupState(id); got != Accepted {
		t.Errorf("state after clear = %v, want the decision dropped", got)
	}
}

// ClearGroup refuses a Proposed set that is not invalid, and an Accepted one:
// neither is a clear, and silently marking an ordinary proposal rejected would
// drop a decision the user has not made. Without the Invalid guard the Proposed
// case would swallow every proposal. Sibling: TestClearRejectedRefusesAProposedSet
// and the address check in TestClearRejectedRefusesUnknownGroup.
func TestClearGroupRefusesWhatItCannotDispose(t *testing.T) {
	s := groupSession(t, "hello world\n")
	base := s.Version()
	s.ApplyDiff(Agent, base, []Hunk{{Start: 6, End: 11, Text: "socket"}})
	proposed := s.LastGroup()
	s.MarkGroup(proposed, Proposed)

	if ok, _ := s.ClearGroup(proposed); ok {
		t.Error("ClearGroup disposed a proposed set that is not invalid")
	}
	if got := s.GroupState(proposed); got != Proposed {
		t.Errorf("state after the refused clear = %v, want Proposed", got)
	}
	if ok, _ := s.ClearGroup(9999); ok {
		t.Error("ClearGroup disposed an id the journal does not hold")
	}
}
