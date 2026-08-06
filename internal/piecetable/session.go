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
	buf     pieceEditor
	journal []Op
	// reversers maps an op to the ops that reverse it. Whether an op is in
	// effect is DERIVED from this rather than tracked as a flag: op X is live
	// exactly when no live op reverses it. A flag cannot express that, because
	// reversing a redo has to revive the original two links down the chain, and
	// a bookkeeping model that only clears its immediate target leaves a group
	// looking live after its text was already reverted — so the next undo
	// reverses it a second time and the buffer comes back scrambled.
	reversers map[Version][]Version

	group uint64
	depth int
}

// NewSession takes ownership of buf. Edits must go through the session from
// this point, or the journal and the document diverge.
func NewSession(buf pieceEditor) *Session {
	return &Session{buf: buf, reversers: map[Version][]Version{}}
}

func (s *Session) Buffer() Buffer { return s.buf }

// Store exposes the text stores, mainly so callers can measure them: store size
// grows with edit volume, not document size, which is the memory property the
// whole design rests on.
func (s *Session) Store() *Store    { return s.buf.Store() }
func (s *Session) Version() Version { return Version(len(s.journal)) }

// Journal exposes the applied history. The slice must not be modified.
func (s *Session) Journal() []Op { return s.journal }

// commit applies an op and records it. Seq is assigned here so it always equals
// the version the op produced minus one, making journal[v:] exactly "everything
// that happened since v".
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

func (s *Session) commit(o Op) Version {
	if s.depth == 0 {
		s.group++
	}
	o.Group = s.group
	o.Seq = Version(len(s.journal))
	apply(s.buf, o)
	s.journal = append(s.journal, o)
	if o.Kind != KindEdit {
		s.reversers[o.Undoes] = append(s.reversers[o.Undoes], o.Seq)
	}
	return s.Version()
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
	At    Version // the op that invalidated the range
}

// ApplyDiff applies hunks written against version base.
//
// Each hunk is rebased independently and rejected independently: an agent
// fixing five call sites does not lose four because one moved. Hunks are
// applied in order and each is rebased from base against everything already in
// the journal, including earlier hunks from this same diff — so overlapping
// hunks within one diff conflict with each other, which is what you want.
func (s *Session) ApplyDiff(author Author, base Version, hunks []Hunk) (Version, []Conflict) {
	var conflicts []Conflict
	for i, h := range hunks {
		start, end, at, ok := s.rebase(h.Start, h.End, base)
		if !ok {
			conflicts = append(conflicts, Conflict{Index: i, Hunk: h, At: at})
			continue
		}
		op := Op{Author: author, Pos: start}
		if end > start {
			op.Del = s.buf.pieceRange(start, end-start)
		}
		if h.Text != "" {
			off := s.buf.Store().Append(author, []byte(h.Text))
			op.Ins = []PieceRec{{Buf: int(author), Start: off, Length: len(h.Text)}}
		}
		s.commit(op)
	}
	return s.Version(), conflicts
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
		return s.reverseGroup(o.Group, author, want)
	}
	return false
}

// reverseGroup reverses every live member of a group, latest first so each
// inverse lands on the document the next one expects. The inverses share one
// group of their own, which is what lets redo reverse them as a unit.
func (s *Session) reverseGroup(group uint64, author Author, want OpKind) bool {
	var members []Op
	for _, o := range s.journal {
		if o.Group != group || o.Author != author || !s.live(o.Seq) {
			continue
		}
		if (o.Kind == KindUndo) != (want == KindRedo) {
			continue
		}
		members = append(members, o)
	}
	s.Begin()
	defer s.End()

	// All or nothing. A half-reversed group leaves the document in a state
	// nobody created — the user presses undo and their text comes back
	// scrambled rather than restored — so a member that cannot be rebased
	// rolls the whole group back instead.
	var applied []Version
	for i := len(members) - 1; i >= 0; i-- {
		inv, ok := s.rebasedInverse(members[i])
		if !ok {
			s.rollback(applied, members)
			return false
		}
		applied = append(applied, s.Version())
		s.commit(inv)
	}
	return len(applied) > 0
}

// rollback undoes a partial group reversal, newest first, restoring the flags
// each step set.
func (s *Session) rollback(applied []Version, members []Op) {
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
// some op that reverses it is itself live. The chain is short — an op, its
// undo, its redo — so the recursion is cheap and always terminates, because a
// reverser is always a later op than what it reverses.
func (s *Session) live(seq Version) bool {
	for _, r := range s.reversers[seq] {
		if s.live(r) {
			return false
		}
	}
	return true
}

// rebasedInverse carries an op's inverse forward to the present. The op's Pos
// is in the coordinates of the version it was applied at, so everything since
// has to be replayed over it.
func (s *Session) rebasedInverse(o Op) (Op, bool) {
	inv := o.Inverse()
	start, end, _, ok := s.rebase(o.Pos, o.Pos+o.InsLen(), o.Seq+1)
	if !ok || end-start != o.InsLen() {
		return Op{}, false
	}
	inv.Pos = start
	return inv, true
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
