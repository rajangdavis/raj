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

	if !s.RejectGroup(first, Agent) {
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
	if !s.RejectGroup(id, Agent) {
		t.Fatal("first reject failed")
	}
	after := text(s)
	if s.RejectGroup(id, Agent) {
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
	s.RejectGroup(id, Agent)
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
	s.RejectGroup(id, Agent)

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
