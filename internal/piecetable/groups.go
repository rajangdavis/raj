package piecetable

import (
	"sort"
	"strings"
)

// Change sets, and whether they are in.
//
// Begin/End ties ops into a group so that one action is one undo step, and
// reverseGroup backs a whole group out all-or-nothing, rebasing each member and
// rolling back if any cannot be placed. That mechanism still backs undo, redo
// and the clear gesture. A rejection is not that: a decision about a change set
// is state, not an edit. Rejecting marks the set without touching the text, and
// only the clear gesture reverses it.
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
	// Rejected is in the document but excluded from the agreed composition.
	// The text stays live — rejecting is a decision, not an edit — and only
	// the clear gesture reverses it. The ops stay in the journal, which is
	// append-only, so offsets recorded against past versions remain
	// rebaseable through them.
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

// hasGroup reports whether any op in the journal belongs to id. RejectGroup
// addresses a real change set: marking an id nothing wrote would record a
// decision about nothing and report a state flip that never happened.
func (s *Session) hasGroup(id uint64) bool {
	for _, o := range s.journal {
		if o.Group == id {
			return true
		}
	}
	return false
}

// GroupState returns a group's state, Accepted if nothing said otherwise.
func (s *Session) GroupState(id uint64) GroupState {
	if s.groupState == nil {
		return Accepted
	}
	return s.groupState[id]
}

// HasDecisions reports whether any change set carries a decision other than
// the default Accepted. groupState holds only the exceptions, so this is a
// length check rather than a scan.
func (s *Session) HasDecisions() bool { return len(s.groupState) > 0 }

// Groups lists every change set in the journal, oldest first.
//
// Undo and redo ops are skipped as members: they are the mechanism by which a
// group is reversed, not changes anyone proposed, and counting them would make
// a rejected group look like two.
func (s *Session) Groups() []Group {
	byID := map[uint64]*Group{}
	for _, o := range s.journal {
		s.addMember(byID, o)
	}
	out := make([]Group, 0, len(byID))
	for _, g := range byID {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out
}

// GroupDiff projects one named change set by the same rebase walk DiffPending
// uses, whatever its state. The Group field is the set as Groups lists it, and
// Hunks and Moved mean for a decided set exactly what they mean for a pending
// one: Hunks counts the runs of its live members' inserted bytes that survive
// in the document now, Moved counts the members a later edit overwrote whole.
//
// A rejected set is not composed into the agreed text, but rejecting is a
// decision, not an edit: its bytes are still live in the buffer, so the walk
// still describes them. "Surviving" here is about the text and not the
// decision, so a rejected set reports what it would contribute if it were in;
// that is the honest reading, and it keeps a decided set's listing as
// informative as a pending set's. An accepted set is the same walk with its
// bytes already in the agreed text. It reports false when the journal holds no
// member for id.
func (s *Session) GroupDiff(id uint64) (GroupDiff, bool) {
	byID := map[uint64]*Group{}
	for _, o := range s.journal {
		if o.Group == id {
			s.addMember(byID, o)
		}
	}
	g, ok := byID[id]
	if !ok {
		return GroupDiff{}, false
	}
	return s.projectGroup(*g), true
}

// addMember folds one journal op into its change set's running listing, or
// creates the listing on first sight. It is the one place the fold lives, so
// Groups and GroupDiff cannot disagree about which ops count, how many bytes
// they net, or which are live.
func (s *Session) addMember(byID map[uint64]*Group, o Op) {
	if o.Kind != KindEdit {
		return
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
		return // reversed since: it is in the journal but not in the text
	}
	g.Ops++
	for _, r := range o.Ins {
		g.Bytes += r.Length
	}
	for _, r := range o.Del {
		g.Bytes -= r.Length
	}
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

// RejectGroup records that a change set is not agreed.
//
// A reject is a pure state flip: the text stays in the document and only the
// decision changes, so it cannot fail and can never be wedged by a later edit.
// The set drops out of the agreed composition and takes on the rejected tint;
// AcceptGroup un-rejects it. The only operation that removes rejected text is
// ClearRejected, the clear gesture.
//
// It returns whether the state actually changed: false if id names no change
// set the journal holds or the set was already Rejected, true otherwise.
func (s *Session) RejectGroup(id uint64) bool {
	if !s.hasGroup(id) {
		return false
	}
	if s.GroupState(id) == Rejected {
		return false
	}
	s.MarkGroup(id, Rejected)
	return true
}

// AcceptGroup agrees to a change set. The text is already in the document —
// accepting is a decision, not an edit — so this only clears the mark: it
// un-rejects a set RejectGroup marked, and clears a Proposed mark. Both are
// the same forgetting, because Accepted is the default.
func (s *Session) AcceptGroup(id uint64) {
	s.MarkGroup(id, Accepted)
}

// ClearRejected is the hard purge behind the clear gesture: it reverses a
// rejected change set out of the document and drops the decision, so the text
// and the set both leave the view.
//
// Unlike RejectGroup this really edits, because removing the bytes is the
// point: the reversal can wedge behind a later change and return false. It
// acts only on a set currently Rejected, so a false means either that the set
// was not rejected to begin with or that a reversal genuinely wedged -- a live
// member moved past what a rebase can carry, including one whose only
// surviving record is a bad offset. A set with no live member ops left -- its
// edits were undone -- has nothing to wedge, so clearing it drops the decision
// and succeeds. False is not retryable: the caller has to look at what
// happened since.
func (s *Session) ClearRejected(id uint64) bool {
	if s.GroupState(id) != Rejected {
		return false
	}
	// The "already gone" case is no live member ops, the same notion Groups
	// counts as Ops: an op in the journal, of kind KindEdit, not reversed by a
	// live undo or redo. reverseMembers is not that test -- its kind filter can
	// come back empty for a set whose members still exist -- so asking it was
	// how a live set used to be marked Accepted and its text orphaned under a
	// tombstone no one can remove.
	if !s.hasLiveMembers(id) {
		s.MarkGroup(id, Accepted)
		return true
	}
	// Live members remain, so the reversal really has to happen. reverseGroup
	// fails when one cannot be placed; that failure has to surface rather than
	// be swallowed by dropping the decision, or the purge is in name only.
	if !s.reverseGroup(id, KindUndo, nil) {
		return false
	}
	s.MarkGroup(id, Accepted)
	return true
}

// hasLiveMembers reports whether a change set still holds a live editorial
// member, the same notion Groups counts as Ops: a journal op of kind KindEdit
// that no live undo or redo has reversed. Undo and redo ops are the mechanism,
// not members, so they never keep a set alive.
func (s *Session) hasLiveMembers(id uint64) bool {
	for _, o := range s.journal {
		if o.Group == id && o.Kind == KindEdit && s.live(o.Seq) {
			return true
		}
	}
	return false
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
// buffer has moved past entirely. MovedHunks carries the recorded old→new text
// of those moved members, as written, so a reviewer sees the proposal even
// though no current span can locate it.
type GroupDiff struct {
	Group Group
	Hunks []DiffHunk
	Moved int
	// MovedHunks is the as-written text of the members counted in Moved.
	// Start and End are -1: a later edit overwrote where they sat, so there is
	// no honest place to point at.
	MovedHunks []DiffHunk
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
			d.MovedHunks = append(d.MovedHunks, s.writtenHunk(o))
			continue
		}
		d.Hunks = append(d.Hunks, hunks...)
	}
	return d
}

// writtenHunk is a moved member as it was written: the text the op removed and
// the text it added, with no current span, because a later edit overwrote
// where it sat. projectMember returns nil exactly when nothing of the inserted
// text survives, so this is the one place the recorded text is still worth
// handing a reviewer.
func (s *Session) writtenHunk(o Op) DiffHunk {
	return DiffHunk{Start: -1, End: -1, Old: s.recsText(o.Del), New: s.recsText(o.Ins)}
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
//
// The scan is over one member's inserted pieces, not the whole journal: this
// runs on the DiffPending review path, not inside Project's per-piece origin
// lookup, so stateRuns' originIndex does not apply here.
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
