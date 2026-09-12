// Package piecetable is the editor's document model: text lives in append-only
// stores, pieces address it, and a Session's journal of ops is the one
// timeline every view of the document is derived from.
//
// Project (this file) is the projection primitive behind layered proposals. A
// composition is a deterministic function of that one journal and of the
// change sets' states — never a second document with offsets of its own.
//
// The contract is defined for journals in which no included edit's span
// intersects the interior of an excluded edit's inserted span; the editor will
// enforce that disjointness with read-only leases in a later phase. Such
// overlapping journals must not panic here, but their composition is not
// specified.
package piecetable

import "sort"

// Policy selects which change sets a projection admits.
type Policy uint8

const (
	// AcceptedOnly is the agreed composition: live Accepted edits only. What
	// save, read and exec see.
	AcceptedOnly Policy = iota
	// AcceptedAndProposed is the edit-mode view: live Accepted and Proposed
	// edits. A Rejected set is absent.
	AcceptedAndProposed
	// Annotated is the review view: every live edit, with each run's owning
	// change set and its state reported by States.
	Annotated
)

// StateRun is a run of composition bytes that one change set put there.
type StateRun struct {
	Off, Len int
	Group    uint64
	State    GroupState
}

// DerivedProject is one composition of the session under one policy. It is a
// snapshot: deriving it never mutates the session, and a later decision
// (MarkGroup) does not reach back into an existing projection. Project always
// returns a usable value; the zero value is not one.
type DerivedProject struct {
	comp   *Naive
	states []StateRun // nil unless the policy was Annotated
}

// Project derives the composition of the session under p. It starts from the
// session's own view — every op applied, so every live edit's bytes and the
// reversals that neutralise dead edits are already in place — and un-applies
// the live edits the policy excludes, newest first. With no decisions every
// policy agrees on the view, so projectNoDecisions returns it without the walk.
//
// A live edit the policy excludes is removed by its inserted runs: stores are
// append-only, so the ranges an op wrote to still name its bytes, and the
// pieces that survive inside them are exactly what the edit still contributes
// to the view. Its deletion is put back where the inserted run leaves it, or at
// the edit's rebased position when a later edit has consumed the run entirely.
// A reversal never contributes bytes of its own, and a dead edit's effect is
// already neutralised by its reverser in the view, so neither is touched — that
// is what keeps a dropped deletion from widening a later included edit's Del.
func (s *Session) Project(p Policy) DerivedProject {
	if !s.HasDecisions() {
		return s.projectNoDecisions(p)
	}
	// The view is the true final text: pieceRange allocates fresh records over
	// the session's stores, so the composition is a snapshot later edits cannot
	// reach into.
	comp := &Naive{store: s.Store()}
	comp.pieces = s.buf.pieceRange(0, s.buf.Len())
	// origins records which change set inserted which store range, for the
	// Annotated runs. Annotated excludes nothing, so its composition is the
	// view and every live edit's ranges are candidates; stateRuns resolves each
	// surviving piece to the set that wrote it.
	var origins []insOrigin
	if p == Annotated {
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
	}
	// Newest first: each excluded edit is removed from a view that still
	// contains it, after every later decision has already been undone.
	for i := len(s.journal) - 1; i >= 0; i-- {
		o := s.journal[i]
		if o.Kind != KindEdit || !s.live(o.Seq) || s.included(o, p) {
			continue
		}
		at, removed := unapplyRemoveIns(comp, o.Ins)
		if removed {
			if o.DelLen() > 0 {
				comp.insertRecs(at, append([]PieceRec(nil), o.Del...))
			}
			continue
		}
		if o.DelLen() == 0 {
			continue
		}
		// The inserted run did not survive, so there is no run in the view to
		// anchor the deletion to; carry its old position to the present.
		mapped, _, _, ok := s.rebase(o.Pos, o.Pos, o.Seq+1)
		if !ok {
			continue
		}
		if mapped < 0 {
			mapped = 0
		} else if n := comp.Len(); mapped > n {
			mapped = n
		}
		comp.insertRecs(mapped, append([]PieceRec(nil), o.Del...))
	}
	d := DerivedProject{comp: comp}
	if p == Annotated {
		d.states = s.stateRuns(comp.pieces, origins)
	}
	return d
}

// unapplyRemoveIns drops every piece of comp that sits inside a store range the
// excluded edit inserted, and reports the document offset of the first piece it
// dropped, or -1 when none of the edit's inserted bytes survive. Filtering in
// place is safe because the range reads each element before the write cursor
// can reach it.
func unapplyRemoveIns(comp *Naive, ins []PieceRec) (at int, removed bool) {
	if len(ins) == 0 {
		return -1, false
	}
	at, removed = -1, false
	out := comp.pieces[:0]
	off := 0
	for _, piece := range comp.pieces {
		if insOwns(ins, piece) {
			if at < 0 {
				at = off
			}
			removed = true
			continue
		}
		out = append(out, piece)
		off += piece.Length
	}
	comp.pieces = out
	return at, removed
}

// projectNoDecisions is Project for a session with no decisions. Every live
// edit is Accepted then, so AcceptedOnly, AcceptedAndProposed and Annotated
// all compose to the session's own view — and the view is already the session
// buffer. The composition is therefore that buffer's pieces over the same
// shared store: O(pieces), with no forward pass over the journal and no
// offset map. pieceRange allocates a fresh slice of records, so the result is
// a snapshot that later edits cannot reach into.
//
// Annotated still has to say which change set owns each run, and a piece's
// owner lives only in the journal. Collecting the live edit ops' inserted
// ranges is one linear pass, not the per-op offset-map pass the decisions
// path pays, and the indexed stateRuns resolves each piece in log time.
func (s *Session) projectNoDecisions(p Policy) DerivedProject {
	comp := &Naive{store: s.Store()}
	comp.pieces = s.buf.pieceRange(0, s.buf.Len())
	d := DerivedProject{comp: comp}
	if p != Annotated {
		return d
	}
	// With no decisions, Annotated includes every live edit, which is exactly
	// KindEdit && live — the same filter included applies for the policy.
	var origins []insOrigin
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
	d.states = s.stateRuns(comp.pieces, origins)
	return d
}

// included reports whether o's own bytes belong in the composition: ordinary
// edits only — a reversal never contributes content, and whether the edit it
// reverses is present is decided by live — still in effect, and in a change
// set the policy admits. live is the session's final liveness, so an undone
// edit is excluded from the first frame it would have appeared in and its
// bytes never need removing.
func (s *Session) included(o Op, p Policy) bool {
	if o.Kind != KindEdit || !s.live(o.Seq) {
		return false
	}
	switch p {
	case AcceptedOnly:
		return s.GroupState(o.Group) == Accepted
	case AcceptedAndProposed:
		st := s.GroupState(o.Group)
		return st == Accepted || st == Proposed
	case Annotated:
		return true
	default:
		return false
	}
}

// insOrigin is a store range one included edit claimed, kept so the Annotated
// runs can say who owns a piece.
type insOrigin struct {
	buf, start, end int
	group           uint64
}

// originIndex answers "which change set inserted this piece" without scanning
// every origin for every piece. Origins are grouped by store buffer and sorted
// by their start offset, so labelling a piece costs a binary search plus, only
// where ranges overlap, a short walk back over the candidates that could
// contain it. Building the index sorts each buffer's origins once.
type originIndex struct {
	byBuf map[int][]insOrigin
}

func newOriginIndex(origins []insOrigin) originIndex {
	ix := originIndex{byBuf: make(map[int][]insOrigin)}
	for _, o := range origins {
		ix.byBuf[o.buf] = append(ix.byBuf[o.buf], o)
	}
	for _, os := range ix.byBuf {
		sort.SliceStable(os, func(i, j int) bool { return os[i].start < os[j].start })
	}
	return ix
}

// ownerOf names the change set a composition piece came from, if any. The
// candidate is the last origin starting at or before the piece; the walk back
// finds a claim whose range contains the whole piece. Sorting is stable, so
// when a pasted capture makes two ops claim one range the later op sits later
// in the slice and its splice wins, as before.
func (ix originIndex) ownerOf(p PieceRec) (uint64, bool) {
	os := ix.byBuf[p.Buf]
	end := p.Start + p.Length
	i := sort.Search(len(os), func(k int) bool { return os[k].start > p.Start })
	for j := i - 1; j >= 0; j-- {
		o := os[j]
		if p.Start >= o.start && end <= o.end {
			return o.group, true
		}
	}
	return 0, false
}

// stateRuns labels the composition's pieces once: base bytes belong to no
// change set (group 0, and always Accepted — the base is what everything was
// agreed onto), each spliced piece to the change set that inserted it, with
// that set's current state. Adjacent runs merge when group AND state both
// match; the run's Group has to stay truthful, so two neighbouring proposals
// stay two runs even though they would tint the same.
func (s *Session) stateRuns(pieces []PieceRec, origins []insOrigin) []StateRun {
	index := newOriginIndex(origins)
	var runs []StateRun
	off := 0
	for _, p := range pieces {
		if p.Length <= 0 {
			continue
		}
		group, state := uint64(0), Accepted
		if g, ok := index.ownerOf(p); ok {
			group, state = g, s.GroupState(g)
		}
		if n := len(runs); n > 0 && runs[n-1].Group == group && runs[n-1].State == state {
			runs[n-1].Len += p.Length
		} else {
			runs = append(runs, StateRun{Off: off, Len: p.Length, Group: group, State: state})
		}
		off += p.Length
	}
	return runs
}

// Buffer is the composition itself: a buffer over the session's own stores.
// Nothing is copied — every piece points into text the session already holds.
func (d DerivedProject) Buffer() Buffer { return d.comp }

// Len is the composition's size in bytes.
func (d DerivedProject) Len() int { return d.comp.Len() }

// Text is the whole composition.
func (d DerivedProject) Text() string { return d.comp.Slice(0, d.comp.Len()) }

// Spans reports authorship across the whole composition.
func (d DerivedProject) Spans() []Span { return d.comp.Spans(0, d.comp.Len()) }

// States reports which change set owns each run of the composition and what
// was decided about it. nil unless the policy was Annotated. The slice is the
// projection's own; treat it as read-only.
func (d DerivedProject) States() []StateRun { return d.states }

// Leased reports which change set, if any, owns the bytes in [pos, pos+length)
// as a read-only run — a lease. A pending or rejected span is atomic: an edit
// or an agent diff that intersects it is refused, which is what keeps the
// composition overlap-free by construction rather than by resolving overlaps
// after the fact.
//
// Coordinates are the live document's, because the runs come from the
// Annotated projection and its composition is the view frame. Only inserted
// runs carry an owner, so a change set that only deletes has no run and cannot
// be addressed this way; that is a known limitation of every caret- and
// range-addressed review surface, and the deletion is simply not leased.
//
// A zero length probes one insertion point, and the half-open intersection
// rule makes it leased exactly when the point lies strictly inside a run
// (start < pos < end): an insertion flush against a run's first byte lands
// before the run and one flush against its last byte lands after it, so
// neither touches the leased bytes. That is the same boundary the insert
// gravity in rebase uses.
func (s *Session) Leased(pos, length int) (group uint64, ok bool) {
	if !s.HasDecisions() {
		return 0, false
	}
	if length < 0 {
		length = 0
	}
	for _, r := range s.Project(Annotated).States() {
		if r.State == Accepted || r.Len <= 0 {
			continue
		}
		if pos < r.Off+r.Len && r.Off < pos+length {
			return r.Group, true
		}
	}
	return 0, false
}
