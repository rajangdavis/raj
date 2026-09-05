package piecetable

import "sort"

// Change sets, and whether they are in.
//
// Begin/End already ties ops into a group so that one action is one undo step,
// and reverseGroup already backs a whole group out all-or-nothing, rebasing each
// member and rolling back if any cannot be placed. So "reject this change" is
// not a new mechanism — it is undo, addressed by group instead of by recency.
//
// What was missing is the two things that make a group reviewable: a way to
// enumerate groups, and somewhere to record whether one has been agreed to.
//
// State is deliberately NOT inferred from the author. The piece table does not
// know which authors are agents — that is the participant registry's business,
// one layer up — and baking the assumption in here is what would have to be
// unpicked the first time a second person edits the same workspace.

// GroupState is whether a change set is in the document by agreement.
type GroupState uint8

const (
	// Accepted is the default, and what an ordinary edit is. Someone typed it;
	// there is nothing to agree to.
	Accepted GroupState = iota
	// Proposed is in the document and visible, but not yet agreed. Marked by
	// whoever knows the writer is proposing rather than editing.
	Proposed
	// Rejected has been reversed. The ops stay in the journal — it is
	// append-only, and offsets recorded against past versions have to remain
	// rebaseable through them — so this records the decision, not a deletion.
	Rejected
)

func (g GroupState) String() string {
	switch g {
	case Proposed:
		return "proposed"
	case Rejected:
		return "rejected"
	}
	return "accepted"
}

// Group is one change set.
type Group struct {
	ID     uint64
	Author Author
	State  GroupState
	// Ops is how many live edits it still contains. A group whose members have
	// all been reversed reads as zero.
	Ops int
	// First and Last bound it in the journal, which is what a caller uses to
	// ask what changed and to order groups against each other.
	First, Last Version
	// Bytes is the net change in document length, so a listing can say "+40"
	// without the caller replaying anything.
	Bytes int
}

// LastGroup is the group the most recent commit joined, so a caller that just
// applied a diff can address what it wrote.
func (s *Session) LastGroup() uint64 { return s.group }

// MarkGroup records a decision about a change set.
func (s *Session) MarkGroup(id uint64, st GroupState) {
	if s.groupState == nil {
		s.groupState = map[uint64]GroupState{}
	}
	if st == Accepted {
		// Accepted is the default, so recording it is the same as forgetting
		// the group ever needed a decision. Keeps the map to the interesting
		// entries rather than one per keystroke.
		delete(s.groupState, id)
		return
	}
	s.groupState[id] = st
}

// GroupState returns a group's state, Accepted if nothing said otherwise.
func (s *Session) GroupState(id uint64) GroupState {
	if s.groupState == nil {
		return Accepted
	}
	return s.groupState[id]
}

// Groups lists every change set in the journal, oldest first.
//
// Undo and redo ops are skipped as members: they are the mechanism by which a
// group is reversed, not changes anyone proposed, and counting them would make
// a rejected group look like two.
func (s *Session) Groups() []Group {
	byID := map[uint64]*Group{}
	for _, o := range s.journal {
		if o.Kind != KindEdit {
			continue
		}
		g, ok := byID[o.Group]
		if !ok {
			g = &Group{ID: o.Group, Author: o.Author, State: s.GroupState(o.Group),
				First: o.Seq, Last: o.Seq}
			byID[o.Group] = g
		}
		if o.Seq < g.First {
			g.First = o.Seq
		}
		if o.Seq > g.Last {
			g.Last = o.Seq
		}
		if !s.live(o.Seq) {
			continue // reversed since: it is in the journal but not in the text
		}
		g.Ops++
		for _, r := range o.Ins {
			g.Bytes += r.Length
		}
		for _, r := range o.Del {
			g.Bytes -= r.Length
		}
	}
	out := make([]Group, 0, len(byID))
	for _, g := range byID {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out
}

// Pending lists the change sets still awaiting a decision, oldest first.
//
// A proposed group whose members have all been reversed is not pending: there
// is nothing left in the text to agree to, and reporting it would block a save
// on a change that is not there.
func (s *Session) Pending() []Group {
	var out []Group
	for _, g := range s.Groups() {
		if g.State == Proposed && g.Ops > 0 {
			out = append(out, g)
		}
	}
	return out
}

// AcceptPending agrees to every change set awaiting a decision and reports how
// many there were.
//
// This is the bulk form, for the one gesture that means "all of it": the user
// saving the file. Accepting is not an edit — the text is already in the
// document — so this cannot fail and cannot conflict.
func (s *Session) AcceptPending() int {
	pending := s.Pending()
	for _, g := range pending {
		s.AcceptGroup(g.ID)
	}
	return len(pending)
}

// RejectGroup backs a change set out and records the decision.
//
// This is undo addressed by group rather than by recency, which is what lets a
// caller reject an older change while newer ones stay: each member is rebased
// through everything that landed after it, and if any cannot be placed the
// whole group rolls back rather than leaving the document in a state nobody
// created.
//
// It returns false when the group is not reversible — already rejected, or
// wedged behind a later change it overlaps. False is not an error to retry: the
// caller has to look at what happened since.
func (s *Session) RejectGroup(id uint64, author Author) bool {
	if s.GroupState(id) == Rejected {
		return false
	}
	if !s.reverseGroup(id, author, KindUndo) {
		return false
	}
	s.MarkGroup(id, Rejected)
	return true
}

// AcceptGroup agrees to a proposed change set. The text is already in the
// document — accepting is a decision, not an edit — so this only clears the
// pending mark.
func (s *Session) AcceptGroup(id uint64) {
	if s.GroupState(id) == Rejected {
		return // reversed already; accepting would claim text that is not there
	}
	s.MarkGroup(id, Accepted)
}
