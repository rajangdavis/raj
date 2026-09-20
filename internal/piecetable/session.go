package piecetable

// Session wraps a Buffer with the journal that makes concurrent editing safe.
//
// The model is one shared buffer, not a copy per agent. Agents hold versions
// and byte ranges, never text, so agent-side state is O(diff) rather than
// O(document) — which is the point, since cheap isolation would require a
// persistent tree and that is the structure the benchmarks rejected.
//
// Staleness is handled by rebasing rather than locking: an agent reads at
// version V, thinks, and submits offsets in V coordinates. ApplyDiff replays
// the journal from V forward to carry those offsets to the present. A hunk
// whose range someone else edited in the meantime is rejected on its own; the
// rest of the diff still lands.
type Session struct {
	buf pieceEditor
	// origLen is the base document length at construction: the bytes the
	// Original store held before any edit. Project measures the base from
	// this rather than the store's current length, because an Original-authored
	// insert appends to the same store and would otherwise land twice -- once
	// as base bytes and again as the op that inserted them.
	origLen int
	journal []Op
	// groupState records decisions about change sets. Sparse: only groups that
	// are not simply Accepted have an entry, so ordinary typing costs nothing.
	groupState map[uint64]GroupState
	// reversers maps an op to the ops that reverse it. Whether an op is in
	// effect is DERIVED from this rather than tracked as a flag: op X is live
	// exactly when no live op reverses it. A flag cannot express that, because
	// reversing a redo has to revive the original two links down the chain, and
	// a bookkeeping model that only clears its immediate target leaves a group
	// looking live after its text was already reverted — so the next undo
	// reverses it a second time and the buffer comes back scrambled.
	reversers map[Version][]Version

	// compacted records store ranges compaction created: a merged or copied
	// span whose bytes no longer sit inside one journal op's inserted range.
	// The projection reads them beside the journal's own origins so a
	// compacted piece is still attributed to the change set that wrote it.
	// They carry no ops: compaction moves bytes, never history.
	compacted []insOrigin

	group uint64
	depth int
}

// NewSession takes ownership of buf. Edits must go through the session from
// this point, or the journal and the document diverge.
func NewSession(buf pieceEditor) *Session {
	return &Session{buf: buf, reversers: map[Version][]Version{}, origLen: buf.Store().Len(Original)}
}

// NewRestoredSession rebuilds a session from a persisted journal. buf must
// already be a document over the log's base with every store blob the ops
// reference appended in order, because replaying an op reads its deleted bytes
// back out of the document and points its inserted bytes at the store; the
// caller reconstructs the store, and this is the seam that makes it live.
//
// baseLen is the length of the base document inside the Original store: the
// log's Base bytes, not the store's current length, because Original-authored
// appends sit after the base and replaying their ops must not count them a
// second time.
//
// ops must be in Seq order with Seq == index, so OpsSince keeps meaning
// "everything after this version". New commits continue numbering after the
// highest group in ops, so a restored group is never reused, and reversers is
// rebuilt from the undo and redo ops so live() answers the same before and
// after a restart. groupState seeds review decisions; nil, or an Accepted
// entry, means the group is accepted, which is the default.
func NewRestoredSession(buf pieceEditor, ops []Op, groupState map[uint64]GroupState, baseLen int) *Session {
	s := &Session{buf: buf, reversers: map[Version][]Version{}, origLen: baseLen}
	for _, o := range ops {
		apply(s.buf, o)
		if o.Kind != KindEdit {
			s.reversers[o.Undoes] = append(s.reversers[o.Undoes], o.Seq)
		}
		if o.Group > s.group {
			s.group = o.Group
		}
	}
	s.journal = append([]Op(nil), ops...)
	if len(groupState) > 0 {
		s.groupState = map[uint64]GroupState{}
		for id, st := range groupState {
			if st != Accepted {
				s.groupState[id] = st
			}
		}
	}
	return s
}

func (s *Session) Buffer() Buffer { return s.buf }

// Store exposes the text stores, mainly so callers can measure them: store size
// grows with edit volume, not document size, which is the memory property the
// whole design rests on.
func (s *Session) Store() *Store    { return s.buf.Store() }
func (s *Session) Version() Version { return Version(len(s.journal)) }

// LengthAt reports the document length at version v: the origin length plus
// each op's net byte delta up to that point. ok is false when v is past the
// journal, a version this session never produced.
//
// A stale base's length is not the current document's length, so a caller
// validating offsets has to measure against this rather than against the
// buffer as it stands: an offset the base never held must be refused instead
// of being rebased into the present at EOF.
func (s *Session) LengthAt(v Version) (int, bool) {
	if v > Version(len(s.journal)) {
		return 0, false
	}
	n := s.origLen
	for _, o := range s.journal[:v] {
		n += o.Delta()
	}
	return n, true
}

// Journal exposes the applied history. The slice must not be modified.
func (s *Session) Journal() []Op { return s.journal }

// Begin opens an undo transaction; every op committed until the matching End
// undoes as one step. Nesting is counted, so an action built from smaller
// actions still collapses to a single step.
func (s *Session) Begin() {
	if s.depth == 0 {
		s.group++
	}
	s.depth++
}

// End closes an undo transaction.
func (s *Session) End() {
	if s.depth > 0 {
		s.depth--
	}
}

// commit records an op in the current group: commitInto with no group to join.
func (s *Session) commit(o Op) Version { return s.commitInto(o, 0) }

// commitInto applies an op and records it. group == 0 joins the current group,
// bumping it when no Begin is open, exactly as an ordinary edit does; a
// non-zero group joins that existing change set instead, which is how an
// amendment folds into the proposal it refines rather than opening a second
// one. Seq is assigned here so it always equals the version the op produced
// minus one, making journal[v:] exactly "everything that happened since v".
func (s *Session) commitInto(o Op, group uint64) Version {
	if group == 0 {
		if s.depth == 0 {
			s.group++
		}
		group = s.group
	}
	o.Group = group
	o.Seq = Version(len(s.journal))
	apply(s.buf, o)
	s.journal = append(s.journal, o)
	if o.Kind != KindEdit {
		s.reversers[o.Undoes] = append(s.reversers[o.Undoes], o.Seq)
	}
	return s.Version()
}

// groupAuthor resolves a change set's author the way Groups does: the first
// editorial member's author, undo and redo skipped. It reports false when the
// journal holds no member for id.
func (s *Session) groupAuthor(id uint64) (Author, bool) {
	for _, o := range s.journal {
		if o.Group == id && o.Kind == KindEdit {
			return o.Author, true
		}
	}
	return 0, false
}

// Insert records author's insertion of text at pos.
func (s *Session) Insert(author Author, pos int, text string) Version {
	if text == "" {
		return s.Version()
	}
	pos, _ = clampRange(pos, 0, s.buf.Len())
	start := s.buf.Store().Append(author, []byte(text))
	return s.commit(Op{
		Author: author, Pos: pos,
		Ins: []PieceRec{{Buf: int(author), Start: start, Length: len(text)}},
	})
}

// Delete records author's deletion of length bytes at pos.
func (s *Session) Delete(author Author, pos, length int) Version {
	pos, length = clampRange(pos, length, s.buf.Len())
	if length == 0 {
		return s.Version()
	}
	return s.commit(Op{Author: author, Pos: pos, Del: s.buf.pieceRange(pos, length)})
}

// Snapshot captures the pieces covering a range without copying any text.
//
// This is what makes copy free: the pieces point into stores that are
// append-only, so the captured span stays valid even if the text is later
// deleted. A clipboard holding piece records costs a few dozen bytes whatever
// the size of the selection.
func (s *Session) Snapshot(pos, length int) []PieceRec {
	return s.buf.pieceRange(pos, length)
}

// InsertPieces splices captured pieces at pos as a single op, appending nothing.
//
// The pieces keep their original author, so pasting text an agent wrote leaves
// it attributed to that agent — the tint follows the text rather than the
// gesture that moved it, which is what makes attribution mean anything.
func (s *Session) InsertPieces(author Author, pos int, recs []PieceRec) Version {
	if len(recs) == 0 {
		return s.Version()
	}
	pos, _ = clampRange(pos, 0, s.buf.Len())
	return s.commit(Op{Author: author, Pos: pos, Ins: append([]PieceRec(nil), recs...)})
}

// Hunk is one edit in a diff: replace the bytes in [Start, End) with Text.
// Offsets are in the coordinates of the version the agent read, not the
// present. A pure insertion has Start == End; a pure deletion has empty Text.
type Hunk struct {
	Start, End int
	Text       string
}

// Conflict reports a hunk that could not be applied. Index is the hunk's
// position in the submitted slice, so an agent can retry precisely that one
// after re-reading rather than resubmitting the whole diff.
type Conflict struct {
	Index int
	Hunk  Hunk
	// At is the op that invalidated the range, for the stale-offset case. A
	// lease refusal invalidates nothing — the range is intact and simply
	// someone else's — so At is zero there and the owner below is what the
	// caller acts on instead.
	At Version
	// Group names the change set that owns the range as a read-only lease when
	// the conflict is a lease refusal; zero otherwise. It tells a caller which
	// decision has to be made before the hunk can land.
	Group uint64
	// Author is the owning set's writer and Start/End the current rebased span
	// of the run that refused the hunk, when Group is non-zero. They are zero
	// for a stale-offset conflict. Reporting them here saves the caller a
	// second call to learn who holds the text and where it is.
	Author     Author
	Start, End int
}

// ApplyDiff applies hunks written against version base.
//
// Each hunk is rebased independently and rejected independently: an agent
// fixing five call sites does not lose four because one moved. Hunks are
// applied in order and each is rebased from base against everything already in
// the journal, including earlier hunks from this same diff — so overlapping
// hunks within one diff conflict with each other, which is what you want.
//
// A hunk that replaces nothing with nothing — Start == End and empty text — is
// skipped before it can commit. It is not a change, so it must not open a
// change set or join one; a batch of only no-ops leaves the journal untouched
// and a mixed batch applies only the real hunks.
//
// Warnings are the third channel. When a hunk lands over another writer's
// Proposed run -- the advisory lease -- every distinct set it moved past is
// named there by group, author and the span it held when the hunk landed, once
// per set however many hunks in the batch catch it. A conflict is a refusal; a
// warning is a success the caller has to know about; a clean apply has neither.
// The advisory applies only when every Proposed run a hunk catches belongs to
// another writer: a hunk that also catches the writer's own Proposed set is not
// a clean amendment and refuses, whatever the run order.
func (s *Session) ApplyDiff(author Author, base Version, hunks []Hunk) (Version, []Conflict, []Block) {
	var conflicts []Conflict
	var warnings []Block
	warned := map[uint64]bool{}
	for i, h := range hunks {
		if h.Start == h.End && h.Text == "" {
			continue
		}
		start, end, at, ok := s.rebase(h.Start, h.End, base)
		if !ok {
			conflicts = append(conflicts, Conflict{Index: i, Hunk: h, At: at})
			continue
		}
		// A pending or rejected span is a read-only lease, but the two states
		// are different walls. A Rejected span is the human's decision, not a
		// draft, so a hunk that catches one is refused and the rejection wins
		// over any Proposed draft the hunk also catches. A Proposed run is
		// advisory: the editing author's op applies in its own new group, the
		// original set's overlapping members are left moved past what a rebase
		// can carry, and the returned warnings name every set it moved past.
		//
		// A writer amending its own Proposed set is the one hunk that joins:
		// the refinement belongs to the same reviewable unit, so the op folds
		// into that set rather than opening a second one. That exception is
		// order-independent: it holds only when every intersecting Proposed
		// run is the writer's own. A hunk that also catches another writer's
		// draft is not a clean amendment and refuses against the own set,
		// whichever run is met first -- otherwise run order would sometimes
		// land the hunk and silently supersede the writer's own draft.
		// proposedSpans reports every distinct intersecting Proposed set, so
		// one pass is the evidence for both the amendment and the refusal.
		var join uint64
		if rg, rs, re, rejected := s.rejectedLease(start, end-start); rejected {
			ra, _ := s.groupAuthor(rg)
			conflicts = append(conflicts, Conflict{Index: i, Hunk: h, Group: rg,
				Author: ra, Start: rs, End: re})
			continue
		}
		spans := s.proposedSpans(start, end-start)
		var own Block
		for _, w := range spans {
			if w.Author == author {
				own = w
				break
			}
		}
		switch {
		case own.Group != 0 && len(spans) > 1:
			conflicts = append(conflicts, Conflict{Index: i, Hunk: h, Group: own.Group,
				Author: own.Author, Start: own.Start, End: own.End})
			continue
		case own.Group != 0:
			join = own.Group
		default:
			// Every caught Proposed run is another writer's draft, not a
			// wall: the hunk commits below in the editing author's own new
			// group, and every distinct set it moved past is named once, so a
			// caller told about one overlap is not left to find the others in
			// groups/diff later. The dedupe is per set across the whole batch:
			// two hunks that catch one set name it once.
			for _, w := range spans {
				if warned[w.Group] {
					continue
				}
				warned[w.Group] = true
				warnings = append(warnings, w)
			}
		}
		op := Op{Author: author, Pos: start}
		if end > start {
			op.Del = s.buf.pieceRange(start, end-start)
		}
		if h.Text != "" {
			off := s.buf.Store().Append(author, []byte(h.Text))
			op.Ins = []PieceRec{{Buf: int(author), Start: off, Length: len(h.Text)}}
		}
		s.commitInto(op, join)
	}
	return s.Version(), conflicts, warnings
}

// rebase carries [start, end) from version base to the present, and reports the
// op that made the range unrecoverable if one did.
//
// The walk answers two questions that used to be one, which is what made it
// wrong whenever undo and redo were in the window:
//
//   - WHERE the range is now is a question about the journal's order. Every op
//     in the window counts, live or not, because each one's Pos is recorded in
//     the frame its predecessors produced — skipping a dead op shifts every
//     later op into a frame it was never written in.
//   - WHETHER the range survived is a question about the document's contents.
//     Only an ordinary edit still in effect can destroy it; a reversal composes
//     with its target to nothing, and a dead op's bytes have already come back.
//
// The caller used to pass a `slide` flag to say what an insertion landing
// exactly on start meant, because a diff hunk and an undo wanted opposite
// answers. They no longer disagree: each end now records the deletions that
// were flush against it and which side their bytes were on, so the reversal of
// each is placed by what actually happened rather than by who is asking.
func (s *Session) rebase(start, end int, base Version) (int, int, Version, bool) {
	if int(base) > len(s.journal) {
		return 0, 0, 0, false
	}
	lo, hi := point{at: start}, point{at: end}
	for _, o := range s.journal[base:] {
		// Damage is asked about first, because the question is about the range
		// as it stood in the frame this op was recorded in.
		if at, bad := s.damages(o, lo, hi); bad {
			return 0, 0, at, false
		}
		// The two ends take opposite gravity where a range has an inside, which
		// is what the interval form used to encode implicitly by testing start
		// before end: text typed at the range's first byte belongs before it,
		// so the start moves past; text typed at its last belongs after it, so
		// the end stays and the range does not swallow it. An empty range has
		// no inside to distinguish, so both ends follow the start.
		empty := !lo.held && !hi.held && lo.at == hi.at
		s.carry(&lo, o, true)
		s.carry(&hi, o, empty)
	}
	if lo.held || hi.held {
		// The reversal that would bring the bytes back is not in this window,
		// so as far as this walk can see they are simply gone.
		return 0, 0, lo.by, false
	}
	return lo.at, hi.at, 0, true
}

// point is one end of a rebased range.
//
// Ends are carried independently rather than as an interval, because an op can
// remove the bytes under one of them and leave the other alone: a range
// straddling a deletion has one end in the document and one end waiting to come
// back, and an interval has nowhere to record that.
type point struct {
	at   int
	held bool    // its bytes are out of the document
	by   Version // the reversal that will restore them
	off  int     // where inside that reversal's re-inserted span it lands

	// anchored records the deletions that ended flush against this point, and
	// which side of it their bytes were on.
	//
	// An offset cannot say whether it sits before or after text that is
	// missing, and both neighbours look identical once the bytes are gone: a
	// deletion beginning here left the point alone, one ending here dragged it
	// left, and the reversal of either is an insertion at exactly this offset.
	// Only the record says which way to move. Deriving it from one flag on the
	// whole walk is what made undo compose correctly with a deletion at its own
	// position and incorrectly with one just before it.
	anchored []anchor
}

// anchor is a deletion flush against a point, and where its bytes go when it
// comes back: to the point's right (the point stays) or its left (the point
// moves past them).
type anchor struct {
	seq   Version
	right bool
}

// damages reports whether an op destroyed bytes the range owns.
//
// This is the distinction the whole walk turns on. Whether an op's COORDINATES
// belong to the frame being walked is a question about the journal's order, and
// every op answers yes — which is why carry runs for all of them, live or not.
// Whether an op DAMAGED the range is a different question, and only an ordinary
// edit that is still in effect can. Conflating the two is what let a
// resurrected op shift later ops that were recorded without it.
//
// A reversal is never damage. It only removes bytes its target added or
// restores bytes its target removed, so it composes with that target to nothing
// as far as any third party's range is concerned — including the case where the
// target's insertion had split this range in two.
func (s *Session) damages(o Op, lo, hi point) (Version, bool) {
	if o.Kind != KindEdit || !s.live(o.Seq) {
		return 0, false
	}
	if lo.held || hi.held {
		return 0, false // the bytes are not in the document; nothing can reach them
	}
	if d := o.DelLen(); o.Pos+d > lo.at && o.Pos < hi.at {
		return o.Seq, true
	}
	return 0, false
}

// carry moves one endpoint through one op. rightward says which way the point
// leans when an insertion lands exactly on it, subject to its own anchors.
func (s *Session) carry(p *point, o Op, rightward bool) {
	if p.held {
		if o.Seq == p.by {
			p.at = o.Pos + p.off
			p.held = false
		}
		return
	}
	d := o.DelLen()
	switch {
	case d == 0 && o.Pos == p.at:
		// An insertion landing exactly here. If it is restoring bytes this
		// point recorded, the record decides; otherwise gravity does.
		if right, known := p.releases(o.Undoes); known {
			if right {
				return
			}
		} else if !rightward {
			return
		}
		p.at += o.Delta()
	case o.Pos+d <= p.at:
		if d > 0 && o.Pos+d == p.at {
			// A deletion ending flush against the point dragged it left, so
			// the reversal that puts those bytes back must push it right again.
			p.anchored = append(p.anchored, anchor{o.Seq, false})
		}
		p.at += o.Delta()
	case o.Pos >= p.at:
		if d > 0 && o.Pos == p.at {
			// A deletion beginning exactly here left the point alone, so the
			// reversal must leave it alone too.
			p.anchored = append(p.anchored, anchor{o.Seq, true})
		}
	default:
		// The point is inside bytes this op removed. If the op is still in
		// effect the bytes are gone for good and damages has already refused;
		// clamping is only so the walk ends with a defined value. If it was
		// undone, park the point until the reversal brings them back.
		if r, ok := s.liveReverser(o.Seq); ok {
			p.held, p.by, p.off = true, r, p.at-o.Pos
			return
		}
		p.at = o.Pos
	}
}

// releases looks up an anchor for the op being undone, consuming it. right
// reports that the restored bytes belong on the point's right, so it stays.
func (p *point) releases(target Version) (right, known bool) {
	for i, a := range p.anchored {
		if a.seq == target {
			p.anchored = append(p.anchored[:i], p.anchored[i+1:]...)
			return a.right, true
		}
	}
	return false, false
}

// liveReverser is the op currently holding o out of the document, if any.
func (s *Session) liveReverser(seq Version) (Version, bool) {
	for _, r := range s.reversers[seq] {
		if s.live(r) {
			return r, true
		}
	}
	return 0, false
}

// Undo reverses author's most recent surviving action, and Redo reverses their
// most recent undo. Both are recorded as new ops rather than by rewinding, so
// the journal stays append-only and offsets recorded against any past version
// remain rebaseable through it.
//
// The journal is one ordered timeline for every author, not a stack per actor.
// Separate stacks would let a user and an agent undo into a state neither ever
// saw, which is the hardest class of corruption to reason about.
func (s *Session) Undo(author Author) bool { return s.reverseLatest(author, KindUndo) }
func (s *Session) Redo(author Author) bool { return s.reverseLatest(author, KindRedo) }

// reverseLatest finds the newest live op this reversal applies to and reverses
// its whole group. want is the kind the reversal will produce: an undo reverses
// edits and redos, a redo reverses undos.
func (s *Session) reverseLatest(author Author, want OpKind) bool {
	for i := len(s.journal) - 1; i >= 0; i-- {
		o := s.journal[i]
		if o.Author != author || !s.live(o.Seq) {
			continue
		}
		if (o.Kind == KindUndo) != (want == KindRedo) {
			continue
		}
		// Only this author's members: undo is personal, and backing out a
		// collaborator's op because it shares a group is not what the key
		// means.
		return s.reverseGroup(o.Group, want, byAuthor(author))
	}
	return false
}

// byAuthor is the member filter undo and redo use.
func byAuthor(a Author) func(Author) bool {
	return func(got Author) bool { return got == a }
}

// reverseMembers collects the live ops reverseGroup would reverse: the group's
// members of the kind want reverses, narrowed by keep when it is not nil.
func (s *Session) reverseMembers(group uint64, want OpKind, keep func(Author) bool) []Op {
	var members []Op
	for _, o := range s.journal {
		if o.Group != group || !s.live(o.Seq) {
			continue
		}
		if keep != nil && !keep(o.Author) {
			continue
		}
		if (o.Kind == KindUndo) != (want == KindRedo) {
			continue
		}
		members = append(members, o)
	}
	return members
}

// reverseGroup reverses every live member of a group, latest first so each
// inverse lands on the document the next one expects. The inverses share one
// group of their own, which is what lets redo reverse them as a unit.
//
// keep selects which members to reverse; nil means all of them. The two callers
// want different things and the difference is not cosmetic. Undo is personal —
// it must not back out an op that merely shares a group with yours — while
// rejecting a change set means the whole set, which is the only reading under
// which "reject" is a decision about a proposal rather than about a person.
func (s *Session) reverseGroup(group uint64, want OpKind, keep func(Author) bool) bool {
	ok, _ := s.reverseGroupBlock(group, want, keep)
	return ok
}

// reverseGroupBlock is reverseGroup plus, when a member cannot be placed, the
// live change set whose span overlaps it. The block is the report a caller
// needs to say what has to move first; reverseGroup throws it away, and the
// undo and redo callers have no use for it.
func (s *Session) reverseGroupBlock(group uint64, want OpKind, keep func(Author) bool) (bool, Block) {
	members := s.reverseMembers(group, want, keep)
	s.Begin()
	defer s.End()

	// All or nothing. A half-reversed group leaves the document in a state
	// nobody created — the user presses undo and their text comes back
	// scrambled rather than restored — so a member that cannot be rebased
	// rolls the whole group back instead.
	var applied []Version
	for i := len(members) - 1; i >= 0; i-- {
		inv, at, ok := s.rebasedInverseAt(members[i])
		if !ok {
			s.rollback(applied)
			return false, s.blockFor(at)
		}
		applied = append(applied, s.Version())
		s.commit(inv)
	}
	return len(applied) > 0, Block{}
}

// rollback undoes a partial reversal, newest first, restoring the flags each
// step set.
func (s *Session) rollback(applied []Version) {
	for j := len(applied) - 1; j >= 0; j-- {
		op := s.journal[applied[j]]
		back, ok := s.rebasedInverse(op)
		if !ok {
			return // nothing safe left to do; the caller reported failure
		}
		s.commit(back)
	}
}

// live reports whether an op currently affects the document: it does unless
// some op that reverses it is itself live.
//
// A well-formed journal's chain is short — an op, its undo, its redo — and
// always terminates, because a reverser is a later op than what it reverses.
// A restored journal is data on disk, though, and nothing stops a truncated or
// hand-edited log from claiming a cycle: op A reverses B while B reverses A,
// or an op lists itself. Following that cycle forever is a stack overflow that
// takes the editor down, so the walk below carries a visited set: each op is
// entered at most once per call, which bounds the recursion depth by the
// journal's length rather than by however long a malformed chain happens to be.
func (s *Session) live(seq Version) bool {
	// An op with no reverser is live, and that is the common case: an ordinary
	// edit is never reversed. Answering it before the map is built keeps the
	// guard off the hot path, where live() is asked once per op per rebase.
	if len(s.reversers[seq]) == 0 {
		return true
	}
	return s.liveFrom(seq, map[Version]bool{seq: true})
}

// liveFrom is live() carrying the set of ops this call has already asked
// about. An edge back into that set is the malformed cycle, and dropping it is
// what makes live() terminate on a journal no honest writer produced: the node
// is already being answered, so re-entering it can only repeat the same
// question. The set lives for the whole call and is shared by every branch,
// because liveness is a reachability question — once an op is being resolved,
// reaching it again from a different branch adds nothing.
func (s *Session) liveFrom(seq Version, seen map[Version]bool) bool {
	for _, r := range s.reversers[seq] {
		if seen[r] {
			continue
		}
		seen[r] = true
		if s.liveFrom(r, seen) {
			return false
		}
	}
	return true
}

// rebasedInverse carries an op's inverse forward to the present. The op's Pos
// is in the coordinates of the version it was applied at, so everything since
// has to be replayed over it.
func (s *Session) rebasedInverse(o Op) (Op, bool) {
	inv, _, ok := s.rebasedInverseAt(o)
	return inv, ok
}

// rebasedInverseAt is rebasedInverse plus the seq of the op rebase reported as
// having damaged the range when it cannot be placed, so a caller can name what
// blocked it (see blockFor). A failure with no damaging op -- a Pos past the
// document -- reports zero.
func (s *Session) rebasedInverseAt(o Op) (Op, Version, bool) {
	inv := o.Inverse()
	start, end, at, ok := s.rebase(o.Pos, o.Pos+o.InsLen(), o.Seq+1)
	// The rebased span has to be inside the document for the inverse to be
	// placeable. It does not always end up there: an op recorded with a Pos
	// past the document (a bad offset accepted against the wrong base) maps
	// to a span beyond the end, and removeRange clamps it to a no-op.
	// Committing that inverse would report success while the text stayed and
	// the op went dead, so refuse rather than lie.
	if !ok || end-start != o.InsLen() || end > s.buf.Len() {
		return Op{}, at, false
	}
	inv.Pos = start
	return inv, 0, true
}

// LastOp is the most recently applied op. Callers that maintain derived state —
// a line index, a syntax cache — use it to update incrementally instead of
// rebuilding after undo, redo, or a diff.
func (s *Session) LastOp() (Op, bool) {
	if len(s.journal) == 0 {
		return Op{}, false
	}
	return s.journal[len(s.journal)-1], true
}

// OpsSince returns everything applied after version v, for callers catching up
// derived state across a multi-hunk diff.
func (s *Session) OpsSince(v Version) []Op {
	if int(v) >= len(s.journal) {
		return nil
	}
	return s.journal[v:]
}
