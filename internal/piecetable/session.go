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
		start, end, at, ok := s.rebase(h.Start, h.End, base, true)
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

// rebase carries [start, end) from version base to the present. It fails when
// any intervening op touched the range's interior: a deletion overlapping it,
// or an insertion strictly inside it.
//
// slide decides what an insertion landing exactly on start does. The two
// callers genuinely want different answers:
//
//   - A diff hunk slides (true): text typed at a hunk's first byte belongs
//     before the hunk, so the hunk moves past it. Without this, adjacent hunks
//     reject each other constantly.
//   - An undo does not (false): it has to compose exactly with the deletion it
//     reverses. A deletion starting at a point leaves the point alone, so the
//     matching re-insertion must leave it alone too. Sliding there makes undo
//     drift by the deleted length every time it crosses a later edit — which
//     is precisely how undo used to mangle a buffer rather than restore it.
func (s *Session) rebase(start, end int, base Version, slide bool) (int, int, Version, bool) {
	if int(base) > len(s.journal) {
		return 0, 0, 0, false
	}
	for _, o := range s.journal[base:] {
		// Skip ops that are no longer in effect, and the inverses that
		// cancelled them. An op and its inverse compose to nothing, but walking
		// both counts each separately: the pair reports a conflict against a
		// region neither of them still touches, so undo refuses for no reason.
		// Skip anything not currently in effect, and skip a live reversal whose
		// target is already dead: the pair composes to nothing, but counting
		// only one of them makes the rebase drift by the other's length.
		if !s.live(o.Seq) || (o.Kind != KindEdit && !s.live(o.Undoes)) {
			continue
		}
		d := o.DelLen()
		switch {
		case d == 0 && o.Pos == start && !slide:
			// A pure insertion exactly at the point: leave the point put, so
			// this composes with the deletion whose inverse it is.
		case o.Pos+d <= start:
			start += o.Delta()
			end += o.Delta()
		case o.Pos >= end:
			// entirely after the range: no effect
		default:
			return 0, 0, o.Seq, false
		}
	}
	return start, end, 0, true
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
	start, end, _, ok := s.rebase(o.Pos, o.Pos+o.InsLen(), o.Seq+1, false)
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
