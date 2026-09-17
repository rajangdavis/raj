package piecetable

// Piece compaction.
//
// A long editing session fragments the document into many adjacent pieces by
// one author: every insert appends its own run to that author's store, and a
// deletion or replacement can leave two same-author runs adjacent in the
// document while their bytes sit far apart in the store. That grows both the
// piece records and the projection's segment count, so compaction folds the
// runs back together.
//
// There are two operations, with different safety rules:
//
//   - A MERGE combines adjacent same-author pieces whose store ranges are
//     contiguous. No bytes move, no store range changes, and the union shares
//     the pieces' change set, so the composition, the projection and the
//     journal are untouched. It is always safe and needs no decision about
//     what is saved.
//
//   - A FLATTEN combines adjacent same-author pieces whose store ranges are
//     NOT contiguous by copying the span into one fresh contiguous run. The
//     copy has no journal op of its own, so it is allowed only for a span that
//     is both saved and committed: an Accepted change set whose every
//     contributing op sits at or before the save line. A span with a pending
//     (Proposed) or Rejected proposal keeps its pieces, because the journal
//     needs its provenance to exclude it from the agreed composition and to
//     render its review hunks.
//
// Provenance is not lost either way. Whenever compaction folds a span that
// belonged to a real change set, it records the resulting store range as a
// compacted origin for that set. The projection consults these beside the
// journal's own inserted ranges, so a compacted piece is still attributed to
// the group that wrote it and an excluded set can still be un-applied. That is
// what keeps compaction behaviour-neutral: it moves bytes, never history.

// Compact folds adjacent same-author pieces together and returns how many
// pieces the document lost.
//
// saved is the version of the last write to disk: a flatten is allowed only
// for spans whose contributing ops all sit before it. Pass Version(0) for a
// buffer that has never been saved, which merges what is safe and flattens
// nothing.
//
// Compact never touches the journal or the group decisions, and it never
// changes the composed text. It is idempotent: a second call with the same
// saved line finds nothing left to do and returns zero.
func (s *Session) Compact(saved Version) int {
	n := s.buf.Len()
	pieces := s.buf.pieceRange(0, n)
	if len(pieces) < 2 {
		return 0
	}
	idx := newOriginIndex(s.allOrigins())
	out, changed := s.compactPieces(pieces, idx, saved)
	if !changed {
		return 0
	}
	s.buf.removeRange(0, n)
	s.buf.insertRecs(0, out)
	return len(pieces) - len(out)
}

// compactPieces is Compact's single left-to-right pass. It never runs far
// enough to need a second pass: a run is extended to its maximal length
// before it is emitted, so the output is already compact and a repeat call
// finds adjacent pieces that are neither contiguous nor flattenable.
func (s *Session) compactPieces(pieces []PieceRec, idx originIndex, saved Version) ([]PieceRec, bool) {
	out := make([]PieceRec, 0, len(pieces))
	changed := false
	i := 0
	for i < len(pieces) {
		cur := pieces[i]
		g, hasGroup := idx.ownerOf(cur)
		stable := s.pieceStable(cur, saved)
		copied := false
		combined := false
		i++
		for i < len(pieces) {
			next := pieces[i]
			if next.Length == 0 || next.Buf != cur.Buf {
				break
			}
			ng, nh := idx.ownerOf(next)
			if nh != hasGroup || ng != g {
				break
			}
			// A span a decision still owns is never touched: a pending or
			// rejected set's pieces and hunks are what review renders, and
			// merging them would fuse one member's run into another's.
			accepted := !hasGroup || (g != 0 && s.GroupState(g) == Accepted)
			if accepted && cur.Start+cur.Length == next.Start {
				// Contiguous in the store: a descriptor merge, no copy.
				cur.Length += next.Length
				stable = stable && s.pieceStable(next, saved)
				combined = true
				changed = true
				i++
				continue
			}
			if accepted && hasGroup && g != 0 &&
				stable && s.pieceStable(next, saved) {
				// Fragmented but saved and committed: copy the pair into one
				// contiguous run. The first copy pulls cur in with it; later
				// pieces append after it, so the run stays one record.
				if !copied {
					off := s.buf.Store().Append(Author(cur.Buf), s.recBytes(cur))
					cur = PieceRec{Buf: cur.Buf, Start: off, Length: cur.Length}
					copied = true
				}
				off := s.buf.Store().Append(Author(next.Buf), s.recBytes(next))
				cur.Length = off - cur.Start + next.Length
				combined = true
				changed = true
				i++
				continue
			}
			break
		}
		if combined && hasGroup && g != 0 {
			s.compacted = append(s.compacted, insOrigin{
				buf: cur.Buf, start: cur.Start, end: cur.Start + cur.Length, group: g,
			})
		}
		out = append(out, cur)
	}
	return out, changed
}

// pieceStable reports whether a piece is safe to copy: every live edit that
// claims its bytes is Accepted and was written at or before saved. A compacted
// piece has no op of its own, so a claim is also a compacted origin of the
// op's group. An undecided or unsaved claimant refuses the copy rather than
// entangle the flattened bytes with a set the journal still has to move.
func (s *Session) pieceStable(p PieceRec, saved Version) bool {
	for _, o := range s.journal {
		if o.Kind != KindEdit || !s.live(o.Seq) {
			continue
		}
		if !insOwns(o.Ins, p) && !s.compactedOwns(o.Group, p) {
			continue
		}
		if s.GroupState(o.Group) != Accepted || o.Seq >= saved {
			return false
		}
	}
	return true
}

// compactedOwns reports whether a piece's bytes lie inside a compacted origin
// of group, the store-range form of insOwns for a span compaction copied. It
// is what lets an excluded set that was Accepted when it was flattened still
// be un-applied after a later decision, and what lets the review hunks of a
// set that gains a decision after compaction be projected.
func (s *Session) compactedOwns(group uint64, p PieceRec) bool {
	for _, c := range s.compacted {
		if c.group == group && c.buf == p.Buf &&
			p.Start >= c.start && p.Start+p.Length <= c.end {
			return true
		}
	}
	return false
}

// allOrigins is every store range a live edit or an earlier compaction claimed.
// The journal's own ranges are what the projection must agree with, so a
// compacted origin is added only while its group still has a live member and
// only ahead of the journal's: when a paste reuses a compacted range, the
// pasting op's own origin has the same start and wins the ownerOf walk-back,
// and a group whose ops are all reversed contributes nothing, exactly as it
// did before compaction.
func (s *Session) allOrigins() []insOrigin {
	live := map[uint64]bool{}
	for _, o := range s.journal {
		if o.Kind == KindEdit && s.live(o.Seq) {
			live[o.Group] = true
		}
	}
	var origins []insOrigin
	for _, c := range s.compacted {
		if live[c.group] {
			origins = append(origins, c)
		}
	}
	for _, o := range s.journal {
		if o.Kind != KindEdit || !s.live(o.Seq) {
			continue
		}
		for _, r := range o.Ins {
			origins = append(origins, insOrigin{
				buf: r.Buf, start: r.Start, end: r.Start + r.Length, group: o.Group,
			})
		}
	}
	return origins
}

// recBytes reads one piece's bytes out of the stores. Nothing is ever erased,
// so a piece a previous compaction pointed at still reads back exactly.
func (s *Session) recBytes(p PieceRec) []byte {
	return s.buf.Store().Slice(Author(p.Buf), p.Start, p.Length)
}
