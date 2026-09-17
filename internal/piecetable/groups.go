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
	// Invalid marks a still-Proposed set whose recorded edit no longer fits
	// the current composition: every live member has been moved past what a
	// rebase can carry, so no hunk of it survives. It is orthogonal to State
	// and recomputed, never stored -- clear the colliding edit (reject, undo,
	// un-reject) and it goes away. InvalidBy names the live set whose edit now
	// occupies the range and where that set sits, or nil when no single
	// collider can be named. An invalid set is already dropped from Pending,
	// and so from the agreed composition; it is reported, never cascaded or
	// clamped.
	Invalid   bool
	InvalidBy *Overlap
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
	s.markInvalid(out)
	return out
}

// markInvalid recomputes the Invalid flag of every proposed set in gs from the
// current journal.
//
// Invalid is the same per-member evidence Pending already uses to drop a set:
// a proposed group with live members but not one surviving hunk, i.e. every
// member moved past what a rebase can carry. One predicate, not a second rule
// beside it, so the two listings cannot disagree about whether work survives.
// Deriving it from the journal and the decisions rather than storing it in the
// journal is what lets it clear when the colliding edit goes away.
//
// A set overwritten by several edits names the first live edit the rebase walk
// stops at as its collider rather than listing every one: the point is to name
// what to clear, not to cascade.
func (s *Session) markInvalid(gs []Group) {
	at := make(map[uint64]int, len(gs))
	for i := range gs {
		at[gs[i].ID] = i
	}
	survives := map[uint64]bool{}
	collider := map[uint64]Overlap{}
	for _, o := range s.journal {
		if o.Kind != KindEdit || !s.live(o.Seq) {
			continue
		}
		i, ok := at[o.Group]
		if !ok || gs[i].State != Proposed || gs[i].Ops == 0 {
			continue
		}
		if len(s.projectMember(o)) > 0 {
			survives[o.Group] = true
			continue
		}
		if _, seen := collider[o.Group]; !seen {
			if ov, ok := s.invalidBy(o); ok {
				collider[o.Group] = ov
			}
		}
	}
	for i := range gs {
		if gs[i].State != Proposed || gs[i].Ops == 0 {
			continue
		}
		gs[i].Invalid = !survives[gs[i].ID]
		// Only an invalid set carries a collider: a set with a surviving hunk
		// may still have lost a member, but it is not superseded and naming a
		// collider would misreport it as one.
		if gs[i].Invalid {
			if ov, ok := collider[gs[i].ID]; ok {
				gs[i].InvalidBy = &ov
			}
		}
	}
}

// invalidBy names the live change set that consumed a moved member's recorded
// edit, and where that set now sits.
//
// It reuses the rebase walk that decides the member is moved: carrying the
// member's inserted span forward from the version just after it reports the
// first live edit that deleted into it, and blockFor turns that op into its
// set, author and projected span. A member with no insertion, or a wedge the
// walk attributes to a reversal rather than a live edit, has no set to name;
// it reports false and the set stays invalid without a collider.
func (s *Session) invalidBy(o Op) (Overlap, bool) {
	if o.InsLen() == 0 {
		return Overlap{}, false
	}
	_, _, damage, ok := s.rebase(o.Pos, o.Pos+o.InsLen(), o.Seq+1)
	if ok || damage == 0 || int(damage) >= len(s.journal) {
		return Overlap{}, false
	}
	bad := s.journal[damage]
	if bad.Kind != KindEdit || !s.live(bad.Seq) {
		return Overlap{}, false
	}
	b := s.blockFor(damage)
	if b.Group == 0 {
		return Overlap{}, false
	}
	return Overlap{Group: b.Group, Author: b.Author, Start: b.Start, End: b.End}, true
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
	// Recompute the set-level Invalid flag the same way Groups does, so a
	// caller reaching this set directly sees the same listing as one reaching
	// it through DiffPending.
	one := []Group{*g}
	s.markInvalid(one)
	return s.projectGroup(one[0]), true
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
// The second case is the one Group.Invalid names: the hunk test below and the
// flag are the same per-member projection, so Pending cannot disagree with the
// `groups` listing about which proposed sets have work left.
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

// UnsavedProposed reports the change sets a save would leave out even though
// the edit composition still shows their text. It is meant to be consulted
// after AcceptPending, when every Proposed set with a surviving hunk has been
// agreed to: what remains Proposed then is an Invalid set, whose members a
// later edit has moved past.
//
// Invalid alone is not enough to refuse a save, and the classification is per
// set. A wholly consumed insertion is absent from the edit view as well, so
// there is nothing on screen to lose; but a collider that is itself excluded --
// a rejected edit that deleted the run -- restores the superseded bytes to
// AcceptedAndProposed while AcceptedOnly still omits them. Asking each set
// whether any of its inserted bytes is present in AcceptedAndProposed tells the
// two apart: the restored run points back into the inserted store range of the
// superseded member, so visible names exactly the sets a save would drop. A
// whole-document comparison cannot, because a memberless set can still differ
// between the two compositions by its *deletion* coming back in AcceptedOnly,
// which adds text rather than dropping it.
func (s *Session) UnsavedProposed() []Group {
	d := s.Project(AcceptedAndProposed)
	var out []Group
	for _, g := range s.Groups() {
		if g.State == Proposed && g.Invalid && s.visible(d, g) {
			out = append(out, g)
		}
	}
	return out
}

// InvalidWithoutMembers lists the still-Proposed Invalid sets a save can retire
// without changing a byte of either composition: every live member is a pure
// insertion whose bytes are already gone. It is the bookkeeping half of the
// Invalid sets, the complement of UnsavedProposed: a set whose inserted bytes
// are still in AcceptedAndProposed would be dropped by a save and is refused;
// a set with no such byte and nothing removed contributes nothing to either
// view, and retiring it (a state-only reject) changes no text.
//
// visible answers the inserted-bytes question from the AcceptedAndProposed
// composition with the same store-range ownership projectMember uses, so a
// rejected collider restored run counts and the two listings cannot overlap.
// deletes excludes a member that removed text: an Invalid set has no surviving
// inserted run, so un-applying such a member to retire the record would restore
// the bytes it removed and change the view -- an edit, not bookkeeping.
func (s *Session) InvalidWithoutMembers() []Group {
	d := s.Project(AcceptedAndProposed)
	var out []Group
	for _, g := range s.Groups() {
		if g.State == Proposed && g.Invalid && !s.visible(d, g) && !s.deletes(g) {
			out = append(out, g)
		}
	}
	return out
}

// deletes reports whether any live member of the set removed text. It is the
// deletion-side companion of visible: an Invalid set has no surviving inserted
// run, so the removals of its members are the only thing excluding it would put
// back, and putting them back is a real edit rather than the state-only
// disposal retirement is.
func (s *Session) deletes(g Group) bool {
	for _, o := range s.journal {
		if o.Group == g.ID && o.Kind == KindEdit && s.live(o.Seq) && o.DelLen() > 0 {
			return true
		}
	}
	return false
}

// visible reports whether any inserted byte of the set is present in the
// composition d. It is the composition-side form of the ownership test that
// projectMember makes against the buffer the session holds: a piece that points
// into a store range the set inserted -- or a compacted origin of one --
// belongs to the set, wherever the projection placed it. Reading the
// composition rather than the session buffer is what lets a restored run from a
// rejected collider count: its bytes are gone from the view and only excluding
// the collider puts them back.
func (s *Session) visible(d DerivedProject, g Group) bool {
	for _, o := range s.journal {
		if o.Group != g.ID || o.Kind != KindEdit || !s.live(o.Seq) {
			continue
		}
		for _, p := range d.comp.pieces {
			if insOwns(o.Ins, p) || s.compactedOwns(g.ID, p) {
				return true
			}
		}
	}
	return false
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

// ClearGroup is the one-gesture disposal the clear verb exposes. A Rejected set
// takes the existing ClearRejectedBlock path: reverse its live members and
// forget the decision. A Proposed set is ordinarily not clear's business --
// accept or reject decides it -- with one exception: an Invalid set. No live
// member of an invalid set has a surviving projection, so there is no text left
// in the session to reverse; marking it Rejected is the whole disposal, and it
// is safe because Rejected is excluded from the edit and agreed compositions
// alike. Forgetting the decision instead, as ClearRejectedBlock does for a set a
// reversal already emptied, would make the restored run reappear as Accepted --
// the opposite of a purge.
//
// It returns false for an id the journal does not hold, for a Proposed set that
// is not invalid, and for an Accepted set: none of those is a clear.
func (s *Session) ClearGroup(id uint64) (bool, Block) {
	switch s.GroupState(id) {
	case Rejected:
		return s.ClearRejectedBlock(id)
	case Proposed:
		g, ok := s.groupByID(id)
		if !ok || !g.Invalid {
			return false, Block{}
		}
		s.MarkGroup(id, Rejected)
		return true, Block{}
	default:
		return false, Block{}
	}
}

// groupByID returns the listed change set with id, or false when the journal
// holds none. It narrows Groups' listing to one set, so the Invalid flag a
// caller reads here is the same one `groups` reports.
func (s *Session) groupByID(id uint64) (Group, bool) {
	for _, g := range s.Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
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
	ok, _ := s.ClearRejectedBlock(id)
	return ok
}

// ClearRejectedBlock is ClearRejected plus, when the reversal wedges, the live
// change set whose span overlaps a member that could not be placed. The block
// is zero when the set was not rejected to begin with, or when no live member
// is left to reverse; a non-zero Group is the report a caller re-proposes
// against.
func (s *Session) ClearRejectedBlock(id uint64) (bool, Block) {
	if s.GroupState(id) != Rejected {
		return false, Block{}
	}
	// The "already gone" case is no live member ops, the same notion Groups
	// counts as Ops: an op in the journal, of kind KindEdit, not reversed by a
	// live undo or redo. reverseMembers is not that test -- its kind filter can
	// come back empty for a set whose members still exist -- so asking it was
	// how a live set used to be marked Accepted and its text orphaned under a
	// tombstone no one can remove.
	if !s.hasLiveMembers(id) {
		s.MarkGroup(id, Accepted)
		return true, Block{}
	}
	// Live members remain, so the reversal really has to happen. reverseGroup
	// fails when one cannot be placed; that failure has to surface rather than
	// be swallowed by dropping the decision, or the purge is in name only.
	ok, block := s.reverseGroupBlock(id, KindUndo, nil)
	if !ok {
		return false, block
	}
	s.MarkGroup(id, Accepted)
	return true, Block{}
}

// RevertAuthor is the inverse of attribution: it discards every live piece
// author wrote, reversing their whole contribution out of the document and
// recording the reversal in the journal. It is the reversal machinery clear
// uses — reverseGroup and rebasedInverse — applied to each change set the
// author still holds, newest first, so a later set that overlaps an earlier one
// comes out before the earlier one can move.
//
// It returns whether anything was reversed and, when a member could not be
// placed, the live change set whose span overlaps it: the same report
// ClearRejectedBlock gives, rather than clamping a wedge. Each change set is
// reversed all or nothing; the sets are independent, so a wedge stops the walk
// and names the blocker, leaving any sets already dropped dropped. The caller
// clears the blocker and reverts again for the rest.
//
// A set the revert emptied — the author's own proposed or rejected set — has
// its decision dropped, because a set with no live member has nothing left to
// decide. A set that still holds another author's live members keeps its
// decision.
func (s *Session) RevertAuthor(author Author) (bool, Block) {
	// The author's live editorial change sets, in journal order. A member, not
	// the set's nominal author, decides membership: a group two writers
	// contributed to is still partly this author's.
	var ids []uint64
	seen := map[uint64]bool{}
	for _, o := range s.journal {
		if o.Kind != KindEdit || o.Author != author || !s.live(o.Seq) {
			continue
		}
		if !seen[o.Group] {
			seen[o.Group] = true
			ids = append(ids, o.Group)
		}
	}
	// Newest first: a later set can overlap an earlier one and has to come out
	// before the earlier can move.
	for i := len(ids) - 1; i >= 0; i-- {
		ok, block := s.reverseGroupBlock(ids[i], KindUndo, byAuthor(author))
		if !ok {
			return false, block
		}
		if !s.hasLiveMembers(ids[i]) {
			s.MarkGroup(ids[i], Accepted)
		}
	}
	return len(ids) > 0, Block{}
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
		if insOwns(o.Ins, p) || s.compactedOwns(o.Group, p) {
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

// Block names a live change set that overlaps work a verb was trying to do.
// Group is the blocking set, Author its writer, and Start/End the current span
// of its surviving projection. Zero Group means the blocker could not be named
// as a set -- a reversal that failed for a reason other than an overlap.
type Block struct {
	Group      uint64
	Author     Author
	Start, End int
}

// blockFor turns the journal seq rebase reported as damage into the live change
// set that owns that op: its group, its author, and the current span of its
// surviving projection, so a caller learns what has to move before the blocked
// work can go in. Zero reports no block.
func (s *Session) blockFor(at Version) Block {
	if at == 0 || int(at) >= len(s.journal) {
		return Block{}
	}
	o := s.journal[at]
	b := Block{Group: o.Group, Author: o.Author}
	hunks := s.projectMember(o)
	if len(hunks) == 0 {
		return b
	}
	lo, hi := hunks[0].Start, hunks[0].End
	for _, h := range hunks[1:] {
		if h.Start < lo {
			lo = h.Start
		}
		if h.End > hi {
			hi = h.End
		}
	}
	b.Start, b.End = lo, hi
	return b
}

// Overlap names another live change set whose projected range intersects one
// set's, and where. Group and Author are the other set; Start and End bound the
// shared span in the buffer's current coordinates.
type Overlap struct {
	Group      uint64
	Author     Author
	Start, End int
}

// PendingOverlaps maps each pending change set to the other pending sets whose
// projected ranges intersect it.
//
// The editor reports an overlap rather than resolving it: nothing here clamps,
// merges, reorders or refuses anything, and the read-only lease in ApplyDiff
// still decides what may be written. Two sets still awaiting a decision that
// claim the same bytes is a fact a person has to settle, and it is carried on
// both sets so the earlier writer sees it on its next query rather than only
// the later one. A set that is not pending is not paired: only a pending set
// still has the option to move.
//
// The range of a member is the bounding of the runs it still owns (the same
// runs DiffPending reports): a member a later insertion split still covers the
// gap between its own runs, where two sets can meet, but a neighbour's text
// flush against the member's end is not swallowed into its range.
func (s *Session) PendingOverlaps() map[uint64][]Overlap {
	type memberSpan struct {
		group      uint64
		author     Author
		start, end int
	}
	var spans []memberSpan
	for _, g := range s.Groups() {
		if g.State != Proposed || g.Ops == 0 {
			continue
		}
		for _, o := range s.journal {
			if o.Group != g.ID || o.Kind != KindEdit || !s.live(o.Seq) {
				continue
			}
			// The span is the bounding of the member's surviving runs -- the
			// same runs DiffPending reports -- not the raw rebased [lo,hi).
			// The raw range absorbs a later insertion flush against the
			// member's end, which would report a neighbour's adjacent text as
			// an overlap; bounding the owned runs keeps a split member's gap
			// without swallowing the set next to it.
			hunks := s.projectMember(o)
			if len(hunks) == 0 {
				continue
			}
			lo, hi := hunks[0].Start, hunks[0].End
			for _, h := range hunks[1:] {
				if h.Start < lo {
					lo = h.Start
				}
				if h.End > hi {
					hi = h.End
				}
			}
			spans = append(spans, memberSpan{group: g.ID, author: o.Author, start: lo, end: hi})
		}
	}
	out := map[uint64][]Overlap{}
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			a, b := spans[i], spans[j]
			if a.group == b.group {
				continue
			}
			lo, hi, ok := hunkOverlap(DiffHunk{Start: a.start, End: a.end},
				DiffHunk{Start: b.start, End: b.end})
			if !ok {
				continue
			}
			out[a.group] = addOverlap(out[a.group], Overlap{Group: b.group, Author: b.author, Start: lo, End: hi})
			out[b.group] = addOverlap(out[b.group], Overlap{Group: a.group, Author: a.author, Start: lo, End: hi})
		}
	}
	for _, ovs := range out {
		sort.Slice(ovs, func(i, j int) bool { return ovs[i].Group < ovs[j].Group })
	}
	return out
}

// addOverlap merges one intersection into a set's list, keeping a single entry
// per other set and widening its span to cover every place the two meet.
func addOverlap(list []Overlap, o Overlap) []Overlap {
	for i := range list {
		if list[i].Group != o.Group {
			continue
		}
		if o.Start < list[i].Start {
			list[i].Start = o.Start
		}
		if o.End > list[i].End {
			list[i].End = o.End
		}
		return list
	}
	return append(list, o)
}

// hunkOverlap reports the span two hunks share, if any. The rule is the
// half-open one Leased uses: [s,e) shares with [s2,e2) exactly where
// max(s,s2) < min(e,e2). A zero-width hunk -- a pure deletion, or a probe -- is
// a point, and shares only when it lies strictly inside the other range, which
// is the same boundary an insertion flush against a run's edge clears.
func hunkOverlap(a, b DiffHunk) (int, int, bool) {
	lo, hi := a.Start, a.End
	if b.Start > lo {
		lo = b.Start
	}
	if b.End < hi {
		hi = b.End
	}
	if lo < hi {
		return lo, hi, true
	}
	if a.Start == a.End && b.Start < a.Start && a.Start < b.End {
		return a.Start, a.Start, true
	}
	if b.Start == b.End && a.Start < b.Start && b.Start < a.End {
		return b.Start, b.Start, true
	}
	return 0, 0, false
}

// proposedSpans reports every distinct Proposed change set whose run intersects
// [pos, pos+length), each once, with its owning author and the run's bounds.
// A hunk that lands needs every draft it moved past, so this reports them all.
// Runs of one set collapse to a single entry: a set is
// superseded once however many of its places the hunk covers, and the span is
// the first run the hunk catches, which is where the set sat when it landed.
func (s *Session) proposedSpans(pos, length int) []Block {
	if !s.HasDecisions() {
		return nil
	}
	if length < 0 {
		length = 0
	}
	var out []Block
	seen := map[uint64]bool{}
	for _, r := range s.Project(Annotated).States() {
		if r.State != Proposed || r.Len <= 0 || seen[r.Group] {
			continue
		}
		if pos < r.Off+r.Len && r.Off < pos+length {
			seen[r.Group] = true
			owner, _ := s.groupAuthor(r.Group)
			out = append(out, Block{Group: r.Group, Author: owner, Start: r.Off, End: r.Off + r.Len})
		}
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
