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

// ProjSeg is one run of the alignment between the session's own text and a
// projection of it. Concatenating Text()[Disp:Disp+DLen] over the segments
// reproduces Text(), and for every segment that is neither Hide nor Restore the
// composition bytes equal the session bytes at Doc.
//
// A run is exactly one of three things:
//
//   - kept: Len == DLen > 0, the bytes are in both the session and the
//     composition and sit at Doc and Disp respectively;
//   - Hide: an excluded insertion that survived in the session, Len > 0 and
//     DLen == 0 — the session bytes at Doc are not in the composition, and the
//     display renders them as one fold row;
//   - Restore: an excluded deletion put back, Len == 0 and DLen > 0 — the
//     composition holds bytes at Disp the session no longer holds.
//
// The slice is the projection's own and a snapshot: it is built once, when the
// projection is, and a later decision does not reach back into it.
type ProjSeg struct {
	Doc, Disp int        // session-byte and composition-byte starts
	Len, DLen int        // session bytes consumed, composition bytes produced
	Group     uint64     // change set that owns the run, 0 for base bytes
	State     GroupState // that set's state at projection time
	Hide      bool       // excluded insertion: Len > 0, DLen == 0
	Restore   bool       // excluded deletion put back: Len == 0, DLen > 0
	// HiddenLines is the number of '\n' bytes in the hidden run's session bytes
	// [Doc, Doc+Len). It is nonzero only for a Hide segment, and lets a display
	// projection number the session lines that follow a fold without carrying
	// the hidden text itself. Zero for every other segment.
	HiddenLines int
}

// projOrigin says where one composition piece came from while Project folds the
// session into a composition: the session byte it starts at, or -1 once bytes a
// deletion removed are put back. The group and state are the owning set's, so a
// run can be labelled without walking the journal again.
type projOrigin struct {
	doc   int
	group uint64
	state GroupState
}

// hiddenRun is a session range an excluded insertion contributed and the
// composition drops: the input to one Hide segment. lines is the number of
// newlines among those session bytes, read back from the session store.
type hiddenRun struct {
	doc, length int
	group       uint64
	state       GroupState
	lines       int
}

// DerivedProject is one composition of the session under one policy. It is a
// snapshot: deriving it never mutates the session, and a later decision
// (MarkGroup) does not reach back into an existing projection. Project always
// returns a usable value; the zero value is not one.
type DerivedProject struct {
	comp     *Naive
	states   []StateRun // nil unless the policy was Annotated
	segments []ProjSeg  // nil on the no-decisions path, like the short-circuit
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
	// origins records which change set inserted which store range. Every policy
	// needs them now: a kept run reports its owning set and state in the
	// segments, not only in the annotated view.
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
	index := newOriginIndex(origins)
	// prov runs parallel to comp.pieces and says where each piece came from: the
	// session byte it starts at, or -1 once a restored deletion puts bytes back
	// the session no longer holds. It is what lets Segments align the two texts
	// without copying either.
	prov := make([]projOrigin, len(comp.pieces))
	doc := 0
	for i, piece := range comp.pieces {
		group, state := uint64(0), Accepted
		if g, ok := index.ownerOf(piece); ok {
			group, state = g, s.GroupState(g)
		}
		prov[i] = projOrigin{doc: doc, group: group, state: state}
		doc += piece.Length
	}
	// Newest first: each excluded edit is removed from a view that still
	// contains it, after every later decision has already been undone.
	var hidden []hiddenRun
	for i := len(s.journal) - 1; i >= 0; i-- {
		o := s.journal[i]
		if o.Kind != KindEdit || !s.live(o.Seq) || s.included(o, p) {
			continue
		}
		at, removed, kept, dropped := unapplyRemoveIns(comp, prov, o.Ins)
		prov = kept
		hidden = append(hidden, dropped...)
		if removed {
			if o.DelLen() > 0 {
				prov = insertRecsProv(comp, prov, at, append([]PieceRec(nil), o.Del...),
					projOrigin{doc: -1, group: o.Group, state: s.GroupState(o.Group)})
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
		prov = insertRecsProv(comp, prov, mapped, append([]PieceRec(nil), o.Del...),
			projOrigin{doc: -1, group: o.Group, state: s.GroupState(o.Group)})
	}
	d := DerivedProject{comp: comp}
	d.segments = buildSegments(comp.pieces, prov, hidden)
	if p == Annotated {
		d.states = s.stateRuns(comp.pieces, origins)
	}
	return d
}

// unapplyRemoveIns drops every piece of comp that sits inside a store range the
// excluded edit inserted, and reports the document offset of the first piece it
// dropped, or -1 when none of the edit's inserted bytes survive. Filtering in
// place is safe because the range reads each element before the write cursor
// can reach it. kept mirrors the surviving comp.pieces and dropped records the
// session bytes the removal hides, one entry per piece removed, including the
// number of newlines among them.
func unapplyRemoveIns(comp *Naive, prov []projOrigin, ins []PieceRec) (at int, removed bool, kept []projOrigin, dropped []hiddenRun) {
	if len(ins) == 0 {
		return -1, false, prov, nil
	}
	at, removed = -1, false
	out := comp.pieces[:0]
	kept = prov[:0]
	off := 0
	for k, piece := range comp.pieces {
		if insOwns(ins, piece) {
			if at < 0 {
				at = off
			}
			removed = true
			if pr := prov[k]; pr.doc >= 0 && piece.Length > 0 {
				dropped = append(dropped, hiddenRun{
					doc: pr.doc, length: piece.Length, group: pr.group, state: pr.state,
					lines: countNewlines(comp.store.Slice(Author(piece.Buf), piece.Start, piece.Length)),
				})
			}
			continue
		}
		out = append(out, piece)
		kept = append(kept, prov[k])
		off += piece.Length
	}
	comp.pieces = out
	return at, removed, kept, dropped
}

// countNewlines counts the '\n' bytes in b. A hidden run's bytes are live in the
// session store — the same bytes a Restore segment reads — so the count spans
// the session lines the fold hides.
func countNewlines(b []byte) (n int) {
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

// insertRecsProv is comp.insertRecs plus the provenance bookkeeping that keeps
// prov the same length as comp.pieces and aligned with it. A split at pos
// splits the provenance record the same way, with the right half starting off
// bytes into the session range; the inserted records carry meta, which for a
// restored deletion marks them session-less. An insertion at or past the end has
// no piece to split, so the appended records simply follow the run.
func insertRecsProv(comp *Naive, prov []projOrigin, pos int, recs []PieceRec, meta projOrigin) []projOrigin {
	if len(recs) == 0 {
		return prov
	}
	i, off := comp.locate(pos)
	comp.insertRecs(pos, recs)
	out := make([]projOrigin, 0, len(prov)+len(recs)+1)
	out = append(out, prov[:i]...)
	if off > 0 {
		left := prov[i]
		right := left
		if left.doc >= 0 {
			right.doc = left.doc + off
		}
		out = append(out, left, right)
	}
	for range recs {
		out = append(out, meta)
	}
	if i < len(prov) {
		if off <= 0 {
			out = append(out, prov[i])
		}
		out = append(out, prov[i+1:]...)
	}
	return out
}

// buildSegments turns the folded composition back into the run alignment
// Segments reports. It walks the kept and restored pieces in composition order,
// filling each hidden run in at the session gap where its bytes were removed;
// adjacent pieces merge when their owning set and state agree and their session
// and composition offsets stay contiguous, so a run is one segment rather than
// one per piece. A merged hidden run adds its newline counts.
func buildSegments(pieces []PieceRec, prov []projOrigin, hidden []hiddenRun) []ProjSeg {
	if len(hidden) > 1 {
		sort.Slice(hidden, func(i, j int) bool { return hidden[i].doc < hidden[j].doc })
		merged := make([]hiddenRun, 0, len(hidden))
		for _, h := range hidden {
			if n := len(merged); n > 0 && merged[n-1].doc+merged[n-1].length == h.doc &&
				merged[n-1].group == h.group && merged[n-1].state == h.state {
				merged[n-1].length += h.length
				merged[n-1].lines += h.lines
				continue
			}
			merged = append(merged, h)
		}
		hidden = merged
	}
	var segs []ProjSeg
	disp, doc, hi := 0, 0, 0
	flush := func(before int) {
		for hi < len(hidden) && hidden[hi].doc < before {
			h := hidden[hi]
			segs = append(segs, ProjSeg{
				Doc: h.doc, Disp: disp, Len: h.length, DLen: 0,
				Group: h.group, State: h.state, Hide: true, HiddenLines: h.lines,
			})
			doc = h.doc + h.length
			hi++
		}
	}
	n := min(len(prov), len(pieces))
	for i := range n {
		piece := pieces[i]
		pr := prov[i]
		if pr.doc < 0 {
			segs = append(segs, ProjSeg{
				Doc: doc, Disp: disp, Len: 0, DLen: piece.Length,
				Group: pr.group, State: pr.state, Restore: true,
			})
			disp += piece.Length
			continue
		}
		flush(pr.doc)
		if len(segs) > 0 {
			last := &segs[len(segs)-1]
			if !last.Hide && !last.Restore && last.Group == pr.group && last.State == pr.state &&
				last.Doc+last.Len == pr.doc {
				last.Len += piece.Length
				last.DLen += piece.Length
				disp += piece.Length
				doc = pr.doc + piece.Length
				continue
			}
		}
		segs = append(segs, ProjSeg{
			Doc: pr.doc, Disp: disp, Len: piece.Length, DLen: piece.Length,
			Group: pr.group, State: pr.state,
		})
		disp += piece.Length
		doc = pr.doc + piece.Length
	}
	for ; hi < len(hidden); hi++ {
		h := hidden[hi]
		segs = append(segs, ProjSeg{
			Doc: h.doc, Disp: disp, Len: h.length, DLen: 0,
			Group: h.group, State: h.state, Hide: true, HiddenLines: h.lines,
		})
	}
	return segs
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

// Segments reports the projection as runs aligned with the session's own text:
// where each kept run sits in both, where an excluded insertion hides session
// bytes, and where an excluded deletion puts composition-only bytes back. nil
// on the no-decisions path, where the projection is the session's own view and
// there is nothing to align. The slice is the projection's own; treat it as
// read-only.
func (d DerivedProject) Segments() []ProjSeg { return d.segments }

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
