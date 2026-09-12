package piecetable

import (
	"fmt"
	"reflect"
	"testing"
)

// Prototypes for two redesign directions of Session.Project, the primitive
// behind layered proposals. BenchmarkProjection shows the shipped forward pass
// is quadratic: it allocates a fresh offset map with
// `newMu := make([]int, len(mu)-del+ins)` on every op, whose length is the
// whole view, so an n-op journal allocates O(n^2) ints over a document of
// O(n) bytes. The two prototypes here isolate the two ways out and are
// test-only: production code is untouched.
//
//   - projectForwardInPlace keeps the same algorithm and the same map but
//     splices one mu in place (append to grow, copy to move the tail), so the
//     allocation is O(document) instead of O(n^2). The tail shift still walks
//     the rest of the map, so the worst case stays O(document) per op; on an
//     append-only journal the replaced span is empty and at the end, so the
//     tail move is one element and the pass is linear.
//
//   - projectUnapply goes the other way: it starts from the session's current
//     view and removes the excluded edits, newest first. Its cost is
//     O(excluded * view), so it is the cheap path exactly when few change sets
//     carry a decision, which is the keystroke-path case (Leased and
//     ApplyDiff's lease refusal do a full Project per call). It degrades as the
//     number of proposals a reviewer has not decided grows.
//
// The equality test below is what makes the timings meaningful: on the
// buildProjectionSession journals the prototypes must reproduce Project
// byte-for-byte and state-for-state, or they are not benchmarked.

// ---- Prototype A: forward pass over one reused offset map ----------------

// projectForwardInPlace is Project with the per-op make([]int, ...) removed.
// mu is one slice, spliced in place across the whole pass: appended to when an
// op grows the view, contracted with a reslice when it shrinks, and its tail
// values shifted by the op's composition delta, exactly as the fresh-slice
// version wrote them into newMu.
//
// The aliasing is the whole trick: the original copied mu[:pos+1] into every
// newMu, which is what made the pass quadratic in bytes; here that prefix is
// already where it belongs. copy is memmove, so it moves the tail over itself
// and no allocation depends on the document beyond the map's own growth.
func (s *Session) projectForwardInPlace(p Policy) DerivedProject {
	baseLen := s.origLen
	comp := &Naive{store: s.Store()}
	if baseLen > 0 {
		comp.pieces = []PieceRec{{Buf: int(Original), Start: 0, Length: baseLen}}
	}
	mu := make([]int, baseLen+1)
	for j := range mu {
		mu[j] = j
	}
	var origins []insOrigin
	for _, o := range s.journal {
		eff := s.included(o, p)
		pos, del, ins := o.Pos, o.DelLen(), o.InsLen()
		compStart, compEnd := mu[pos], mu[pos+del]
		if eff {
			if compEnd > compStart {
				comp.removeRange(compStart, compEnd-compStart)
			}
			if ins > 0 {
				comp.insertRecs(compStart, append([]PieceRec(nil), o.Ins...))
			}
			if p == Annotated {
				for _, r := range o.Ins {
					origins = append(origins, insOrigin{
						buf: r.Buf, start: r.Start, end: r.Start + r.Length, group: o.Group,
					})
				}
			}
		}
		// delta is how far the composition moved, same as Project: the op's
		// own Delta, except when the replaced span also covered bytes an
		// excluded deletion left restored inside it.
		delta := 0
		if eff {
			delta = ins - (compEnd - compStart)
		}
		// Grow first so the map can hold the new length, then shift the tail
		// after the replaced span by delta and slide it from pos+del to
		// pos+ins. The prefix [0, pos] is left where it is, which is the work
		// the fresh-slice version did unconditionally.
		oldLen := len(mu)
		newLen := oldLen - del + ins
		if ins > del {
			mu = append(mu, make([]int, ins-del)...)
		}
		for j := pos + del; j < oldLen; j++ {
			mu[j] += delta
		}
		copy(mu[pos+ins:newLen], mu[pos+del:oldLen])
		// The op's own view positions map to the freshly spliced composition
		// run; an excluded span collapses onto its left edge.
		for k := 0; k < ins; k++ {
			if eff {
				mu[pos+k] = compStart + k
			} else {
				mu[pos+k] = compStart
			}
		}
		mu = mu[:newLen]
	}
	d := DerivedProject{comp: comp}
	if p == Annotated {
		d.states = s.stateRuns(comp.pieces, origins)
	}
	return d
}

// ---- Prototype B: unapply the excluded edits from the present -----------

// projectUnapply derives the composition from the present instead of the base.
// The current view already holds every live edit, so this removes the ones the
// policy EXCLUDES, newest first, and puts back what they deleted.
//
// Newest-first is what makes the cost track the number of decisions rather
// than the size of the journal. In the common case a reviewer has one pending
// or rejected set, so the pass walks the journal once and touches one op; when
// many sets are Rejected it approaches the forward pass's cost, which is the
// crossover the benchmark measures.
//
// An excluded op's inserted bytes are found by the store ranges it wrote to,
// the same containment test insOwns uses for review hunks: a position in the
// op's own frame has long been overwritten by everything after it, but the
// (Buf, Start, Length) records do not move, because stores are append-only.
// The bytes it deleted are spliced back at the hole its inserted run leaves; a
// pure deletion has no inserted run to locate and falls back to rebase
// carrying the point to the present.
//
// LIMITATION: the pure-deletion fallback rebases against the whole journal, so
// it is exact only when inverting a journal that does not mix several excluded
// deletions. The equality tests drive append-only journals, where an excluded
// op always has DelLen()==0, so the fallback is never reached there; journals
// that mix deletions across several decisions are not validated.
func (s *Session) projectUnapply(p Policy) DerivedProject {
	comp := &Naive{store: s.Store()}
	comp.pieces = append([]PieceRec(nil), s.buf.pieceRange(0, s.buf.Len())...)

	var origins []insOrigin
	if p == Annotated {
		// Every non-base piece left after the removals descends from an
		// included op, so collecting those ops' ranges is enough for
		// stateRuns to label what survives.
		for _, o := range s.journal {
			if !s.included(o, p) {
				continue
			}
			for _, r := range o.Ins {
				origins = append(origins, insOrigin{
					buf: r.Buf, start: r.Start, end: r.Start + r.Length, group: o.Group,
				})
			}
		}
	}

	for i := len(s.journal) - 1; i >= 0; i-- {
		o := s.journal[i]
		if o.Kind != KindEdit || !s.live(o.Seq) || s.included(o, p) {
			continue
		}
		at, _ := unapplyRemoveOwned(comp, o.Ins)
		if o.DelLen() == 0 {
			continue
		}
		if at < 0 {
			mapped, _, _, ok := s.rebase(o.Pos, o.Pos, o.Seq+1)
			if !ok {
				// The deletion cannot be placed against the present; leaving
				// it out keeps the composition well formed rather than
				// splicing at a wrong offset.
				continue
			}
			at = mapped
		}
		comp.insertRecs(at, o.Del)
	}
	d := DerivedProject{comp: comp}
	if p == Annotated {
		d.states = s.stateRuns(comp.pieces, origins)
	}
	return d
}

// unapplyRemoveOwned drops every piece of n that sits inside a store range the
// excluded op inserted and reports the document position of the first piece it
// dropped, or -1 when the op owns nothing that survives. Filtering in place is
// safe because the range reads each element before the write cursor can reach
// it.
func unapplyRemoveOwned(n *Naive, ins []PieceRec) (at int, removed bool) {
	if len(ins) == 0 {
		return -1, false
	}
	at, removed = -1, false
	out := n.pieces[:0]
	off := 0
	for _, piece := range n.pieces {
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
	n.pieces = out
	return at, removed
}

// ---- Equality test -------------------------------------------------------

// TestProjectionAltMatchesProject checks each prototype against Project on the
// exact sessions the benchmarks time. Numbers from a prototype that composes
// differently would be worse than useless, so this is the gate: if either
// prototype drifts, the benchmark must not be trusted.
func TestProjectionAltMatchesProject(t *testing.T) {
	shapes := []struct {
		name  string
		swarm bool
	}{
		{"single-user", false},
		{"swarm", true},
	}
	policies := []struct {
		name string
		p    Policy
	}{
		{"AcceptedOnly", AcceptedOnly},
		{"Annotated", Annotated},
	}
	for _, sh := range shapes {
		for n := 1; n <= 200; n++ {
			s, _, _ := buildProjectionSession(sh.swarm, n)
			for _, pol := range policies {
				want := s.Project(pol.p)
				cases := []struct {
					name string
					got  DerivedProject
				}{
					{"forward-in-place", s.projectForwardInPlace(pol.p)},
					{"unapply", s.projectUnapply(pol.p)},
				}
				for _, c := range cases {
					if got, wantLen := c.got.Len(), want.Len(); got != wantLen {
						t.Fatalf("%s/ops=%d/%s/%s: Len=%d, want %d",
							sh.name, n, pol.name, c.name, got, wantLen)
					}
					if got, wantText := c.got.Text(), want.Text(); got != wantText {
						t.Fatalf("%s/ops=%d/%s/%s: text differs:\n got %q\nwant %q",
							sh.name, n, pol.name, c.name, got, wantText)
					}
					if got, wantSpans := c.got.Spans(), want.Spans(); !reflect.DeepEqual(got, wantSpans) {
						t.Fatalf("%s/ops=%d/%s/%s: spans differ:\n got %+v\nwant %+v",
							sh.name, n, pol.name, c.name, got, wantSpans)
					}
					if got, wantStates := c.got.States(), want.States(); !reflect.DeepEqual(got, wantStates) {
						t.Fatalf("%s/ops=%d/%s/%s: states differ:\n got %+v\nwant %+v",
							sh.name, n, pol.name, c.name, got, wantStates)
					}
				}
			}
		}
	}
}

// ---- Benchmarks ----------------------------------------------------------

// Sinks keep the timed calls from being optimised away.
var (
	benchAltProject DerivedProject
	benchAltGroup   uint64
	benchAltOK      bool
)

// leasedProject is Session.Leased with the projection under test substituted.
// It mirrors Leased exactly: the HasDecisions short-circuit, the zero-width
// probe, and the half-open intersection over the Annotated runs.
func leasedProject(s *Session, pos, length int, project func(Policy) DerivedProject) (group uint64, ok bool) {
	if !s.HasDecisions() {
		return 0, false
	}
	if length < 0 {
		length = 0
	}
	for _, r := range project(Annotated).States() {
		if r.State == Accepted || r.Len <= 0 {
			continue
		}
		if pos < r.Off+r.Len && r.Off < pos+length {
			return r.Group, true
		}
	}
	return 0, false
}

// BenchmarkProjectionAlt times the two prototypes against the shipped Project
// on BenchmarkProjection's own shape: {single-user, swarm} x
// ops={1000,10000,100000}, for AcceptedOnly and Annotated, plus a
// Leased-equivalent comparison.
//
// Expected hot cells: with current, swarm/ops=100000 spends O(n^2) bytes in
// make([]int, ...) and takes tens of seconds per iteration (Project/Annotated
// is the worst, since every live edit is included); that whole 100k group is
// minutes, not seconds. forward-in-place removes those allocations, and on
// these append-only journals its tail move is one element, so it should be
// near-linear; unapply is trivial for Annotated (nothing is excluded) and pays
// for AcceptedOnly, where swarm excludes two thirds of the sets.
func BenchmarkProjectionAlt(b *testing.B) {
	sizes := []int{1000, 10000, 100000}
	shapes := []struct {
		name  string
		swarm bool
	}{
		{"single-user", false},
		{"swarm", true},
	}
	policies := []struct {
		name string
		p    Policy
	}{
		{"AcceptedOnly", AcceptedOnly},
		{"Annotated", Annotated},
	}
	for _, sh := range shapes {
		for _, n := range sizes {
			s, inside, outside := buildProjectionSession(sh.swarm, n)
			b.Run(fmt.Sprintf("%s/ops=%d", sh.name, n), func(b *testing.B) {
				for _, pol := range policies {
					p := pol.p
					b.Run("Project/"+pol.name+"/current", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							benchAltProject = s.Project(p)
						}
					})
					b.Run("Project/"+pol.name+"/forward-in-place", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							benchAltProject = s.projectForwardInPlace(p)
						}
					})
					b.Run("Project/"+pol.name+"/unapply", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							benchAltProject = s.projectUnapply(p)
						}
					})
				}
				variants := []struct {
					name    string
					project func(Policy) DerivedProject
				}{
					{"current", s.Project},
					{"forward-in-place", s.projectForwardInPlace},
					{"unapply", s.projectUnapply},
				}
				for _, v := range variants {
					project := v.project
					b.Run("Leased/inside/"+v.name, func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							benchAltGroup, benchAltOK = leasedProject(s, inside, 0, project)
						}
					})
					b.Run("Leased/outside/"+v.name, func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							benchAltGroup, benchAltOK = leasedProject(s, outside, 0, project)
						}
					})
				}
			})
		}
	}
}
