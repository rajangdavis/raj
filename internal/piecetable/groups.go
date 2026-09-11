package piecetable

import (
	"sort"
	"strings"
)

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

// Pending lists the change sets that still need a decision: proposed, with a
// surviving projection in the document, oldest first.
//
// Auto-rejection lives here. A proposed group whose members have all been
// reversed is not pending, and neither is one a later edit has entirely
// overwritten — every agent piece gone. There is nothing left in the text to
// agree to in either case, and reporting it would block a save on a change
// that is not there. The state stays Proposed as a record; only the claim on
// the user's decision is dropped, and it comes back if that edit is undone.
func (s *Session) Pending() []Group {
	var out []Group
	for _, d := range s.DiffPending() {
		if len(d.Hunks) == 0 {
			continue // no agent piece survives: nothing left to decide
		}
		out = append(out, d.Group)
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
//
// It takes no author. It used to, and the parameter selected which members to
// back out, which meant a caller had to already know who wrote the group and a
// caller that passed the deciding author instead — the natural reading of
// "reject" — silently selected nothing and got a bare false. Rejecting a change
// set means the whole change set; who decided that is a fact about the
// conversation, not about which ops come out of the document.
func (s *Session) RejectGroup(id uint64) bool {
	if s.GroupState(id) == Rejected {
		return false
	}
	if !s.reverseGroup(id, KindUndo, nil) {
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

// DiffHunk is one surviving run of a change set as old→new text, in the
// coordinates of the present: Start and End locate that run of inserted text
// now, Old is the text the member removed and New the text it added. A pure
// insertion has Old empty; a pure deletion has New empty and Start == End.
type DiffHunk struct {
	Start, End int
	Old, New   string
}

// GroupDiff is a change set rendered for review: the group as Groups lists
// it, one hunk per surviving run of its member ops, and a count of members the
// buffer has moved past entirely.
type GroupDiff struct {
	Group Group
	Hunks []DiffHunk
	Moved int
}

// projectGroup renders one proposed group: each live member's surviving runs
// as hunks, and a count of members whose inserted text a later edit has
// completely overwritten.
func (s *Session) projectGroup(g Group) GroupDiff {
	d := GroupDiff{Group: g}
	for _, o := range s.journal {
		if o.Group != g.ID || o.Kind != KindEdit || !s.live(o.Seq) {
			continue
		}
		hunks := s.projectMember(o)
		if len(hunks) == 0 {
			d.Moved++
			continue
		}
		d.Hunks = append(d.Hunks, hunks...)
	}
	return d
}

// projectMember carries one live member op into the present. A pure deletion
// projects to a single zero-width hunk at its rebased point. An insertion
// projects to one hunk per run of its text that survives, so a later edit
// inside the span fragments the member rather than erasing it: the runs the
// agent wrote stay theirs, and the bytes the user typed between them are
// theirs. It returns nil when a member's inserted text is entirely gone, which
// is the one honest reason to count the member in Moved.
func (s *Session) projectMember(o Op) []DiffHunk {
	if o.InsLen() == 0 {
		at, _, _, ok := s.rebase(o.Pos, o.Pos, o.Seq+1)
		if !ok {
			return nil
		}
		return []DiffHunk{{Start: at, End: at, Old: s.recsText(o.Del)}}
	}
	// Empty probes give a bounding span even when a later edit deleted part
	// of it: a probe is never damaged, it clamps to the deletion, so the two
	// ends still bracket every surviving byte. rebase cannot do this for the
	// whole span, because one deletion inside it is a conflict by its rules.
	lo, _, _, okLo := s.rebase(o.Pos, o.Pos, o.Seq+1)
	_, hi, _, okHi := s.rebase(o.Pos+o.InsLen(), o.Pos+o.InsLen(), o.Seq+1)
	if !okLo || !okHi || hi <= lo {
		return nil
	}
	var hunks []DiffHunk
	var run []PieceRec
	pos := lo
	runStart := lo
	flush := func() {
		if len(run) == 0 {
			return
		}
		hunks = append(hunks, DiffHunk{Start: runStart, End: pos, New: s.recsText(run)})
		run = nil
	}
	for _, p := range s.buf.pieceRange(lo, hi-lo) {
		if insOwns(o.Ins, p) {
			if len(run) == 0 {
				runStart = pos
			}
			run = append(run, p)
		} else {
			flush()
		}
		pos += p.Length
	}
	flush()
	if len(hunks) > 0 && o.DelLen() > 0 {
		// The removed bytes have no surviving position of their own, so the
		// old side rides on the member's first surviving run.
		hunks[0].Old = s.recsText(o.Del)
	}
	return hunks
}

// insOwns reports whether a current piece is part of what a member inserted.
// Stores are append-only, so (Buf, Start) names the exact bytes written; a
// later edit that split or trimmed the piece leaves a sub-range of the same
// record, while another author's text points into a different store range.
func insOwns(ins []PieceRec, p PieceRec) bool {
	for _, r := range ins {
		if r.Buf == p.Buf && p.Start >= r.Start && p.Start+p.Length <= r.Start+r.Length {
			return true
		}
	}
	return false
}

// DiffPending renders the proposed change sets as old→new text, oldest first,
// including any the buffer has moved entirely past. It is the review surface
// Groups cannot be: a listing says a change set exists, this says what it says.
//
// Each member's span is carried from the version it was recorded at to the
// present by the same rebase walk that applies and reverses edits, then
// projected onto the pieces that survive now. A later edit inside a member
// leaves the runs on either side of it; they come back as separate hunks so
// the tint, the caret and the jump all stop at the bytes the agent still owns.
// A member whose inserted text is entirely gone is counted in Moved rather
// than dropped; the group is still rendered so a reviewer can see what the
// buffer moved past. Pending is the stricter list — it leaves out a set with
// no surviving member, because a later edit has overwritten it and there is
// nothing left to decide or to block a save on.
func (s *Session) DiffPending() []GroupDiff {
	var out []GroupDiff
	for _, g := range s.Groups() {
		if g.State != Proposed || g.Ops == 0 {
			continue
		}
		out = append(out, s.projectGroup(g))
	}
	return out
}

// recsText reads the bytes a piece list spans out of the stores. Nothing is
// ever erased, so the pieces an op deleted still read back exactly.
func (s *Session) recsText(recs []PieceRec) string {
	var b strings.Builder
	for _, r := range recs {
		b.Write(s.buf.Store().Slice(Author(r.Buf), r.Start, r.Length))
	}
	return b.String()
}
