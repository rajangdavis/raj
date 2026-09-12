package piecetable

import (
	"slices"
	"testing"
	"unicode/utf8"
)

// --- unit tests -------------------------------------------------------------
//
// Each test builds a small journal through the Session's own methods, projects
// it under every policy, and checks the composition against what the contract
// says in terms plain enough to be their own oracle.

func TestProjectExcludesProposedInsertion(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	if got := s.Project(AcceptedOnly).Text(); got != "AB" {
		t.Fatalf("agreed composition %q, want %q", got, "AB")
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != "AxyB" {
		t.Fatalf("edit composition %q, want %q", got, "AxyB")
	}
	if got := s.Project(Annotated).Text(); got != "AxyB" {
		t.Fatalf("review composition %q, want %q", got, "AxyB")
	}
}

// An insert attributed to Original appends to the Original store. The base is
// the length the session was constructed with, not the store's current length,
// or the op's store bytes would land once as base bytes and once as the op
// that inserted them.
func TestProjectOriginalInsertDoesNotDuplicate(t *testing.T) {
	s := NewSession(NewNaive("original\n"))
	s.Insert(Original, 0, "mine ")

	if got := s.Buffer().Slice(0, s.Buffer().Len()); got != "mine original\n" {
		t.Fatalf("buffer = %q, want %q", got, "mine original\n")
	}
	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		if got := s.Project(p).Text(); got != "mine original\n" {
			t.Fatalf("%v: composition %q, want %q", p, got, "mine original\n")
		}
	}
}

// An excluded deletion is never applied, so its bytes stay in the composition:
// that is the whole restore, and it needs no special case.
func TestProjectRestoresProposedDeletion(t *testing.T) {
	s := NewSession(NewNaive("ABC"))
	s.Begin()
	s.Delete(User, 1, 1) // "B"
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	if got := s.Project(AcceptedOnly).Text(); got != "ABC" {
		t.Fatalf("excluded deletion was applied anyway: %q, want %q", got, "ABC")
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != "AC" {
		t.Fatalf("edit composition %q, want %q", got, "AC")
	}
}

// The three policies are three different answers on a journal with a proposed
// set: the agreed composition hides it, edit mode shows it, review mode shows
// it and says so.
func TestProjectPoliciesDifferOnProposedGroup(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Proposed)

	agreed := s.Project(AcceptedOnly)
	edit := s.Project(AcceptedAndProposed)
	review := s.Project(Annotated)

	if agreed.Text() == edit.Text() {
		t.Fatalf("agreed and edit compositions agree on a proposed set: %q", agreed.Text())
	}
	if edit.Text() != review.Text() {
		t.Fatalf("edit and review compositions disagree: %q vs %q", edit.Text(), review.Text())
	}
	if agreed.States() != nil || edit.States() != nil {
		t.Fatalf("state runs leaked out of the annotated view")
	}
	want := []StateRun{
		{Off: 0, Len: 1, Group: 0, State: Accepted},
		{Off: 1, Len: 2, Group: id, State: Proposed},
		{Off: 3, Len: 1, Group: 0, State: Accepted},
	}
	runs := review.States()
	if len(runs) != len(want) {
		t.Fatalf("review runs = %v, want %v", runs, want)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d = %+v, want %+v (all: %v)", i, runs[i], want[i], runs)
		}
	}
}

// A Rejected set that was never reversed — the decision recorded and the text
// untouched, which is what MarkGroup alone does — is absent from the agreed
// and edit compositions and present, labelled, in the review view.
func TestProjectRejectedWithoutReversal(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 2, "X")
	s.End()
	id := s.LastGroup()
	s.MarkGroup(id, Rejected)

	if got := s.Project(AcceptedOnly).Text(); got != "AB" {
		t.Fatalf("agreed composition kept rejected text: %q", got)
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != "AB" {
		t.Fatalf("edit composition kept rejected text: %q", got)
	}
	review := s.Project(Annotated)
	if got := review.Text(); got != "ABX" {
		t.Fatalf("review composition lost rejected text: %q, want %q", got, "ABX")
	}
	want := []StateRun{
		{Off: 0, Len: 2, Group: 0, State: Accepted},
		{Off: 2, Len: 1, Group: id, State: Rejected},
	}
	runs := review.States()
	if len(runs) != len(want) {
		t.Fatalf("review runs = %v, want %v", runs, want)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d = %+v, want %+v", i, runs[i], want[i])
		}
	}
}

// A dead edit — undone, so a live reverser holds it out of the document — is
// absent from every composition, and redo brings it back in all of them.
func TestProjectDeadEditAbsentEverywhere(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "X")
	s.End()
	s.Undo(Agent)

	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		if got := s.Project(p).Text(); got != "AB" {
			t.Fatalf("%v: dead edit appeared: %q, want %q", p, got, "AB")
		}
	}

	s.Redo(Agent)
	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		if got := s.Project(p).Text(); got != "AXB" {
			t.Fatalf("%v: redone edit missing: %q, want %q", p, got, "AXB")
		}
	}
}

// An excluded edit still moves later coordinates: the edit after it was
// recorded in a frame that contained it, so its Delta shifts the map even
// though its bytes never arrive. Losing the shift puts the later edit's text
// in the wrong place instead of between "y" and "B".
func TestProjectExcludedOpShiftsLaterOffsets(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)
	s.Begin()
	s.Insert(User, 3, "Z") // between "y" and "B" in the view frame
	s.End()

	if got := s.Project(AcceptedOnly).Text(); got != "AZB" {
		t.Fatalf("agreed composition %q, want %q — the shift was lost", got, "AZB")
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != "AxyZB" {
		t.Fatalf("edit composition %q, want %q", got, "AxyZB")
	}
}

func TestProjectRunsCoverTheComposition(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	s.Begin()
	s.Insert(User, 4, "Z")
	s.End()

	s.Begin()
	s.Insert(Agent, 0, "W")
	s.End()
	s.MarkGroup(s.LastGroup(), Rejected)

	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		d := s.Project(p)
		checkRuns(t, p, d.Len(), d.Spans(), d.States())
	}

	// Spelled out for the annotated view over "WAxyBZ": rejected, base,
	// proposed, base, accepted.
	want := []StateRun{
		{Off: 0, Len: 1, Group: 3, State: Rejected},
		{Off: 1, Len: 1, Group: 0, State: Accepted},
		{Off: 2, Len: 2, Group: 1, State: Proposed},
		{Off: 4, Len: 1, Group: 0, State: Accepted},
		{Off: 5, Len: 1, Group: 2, State: Accepted},
	}
	runs := s.Project(Annotated).States()
	if len(runs) != len(want) {
		t.Fatalf("annotated runs = %v, want %v", runs, want)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d = %+v, want %+v (all: %v)", i, runs[i], want[i], runs)
		}
	}
}

// checkRuns asserts the one thing every consumer of Spans and States assumes:
// contiguous, non-empty runs that start at zero and cover the composition.
func checkRuns(t *testing.T, p Policy, total int, spans []Span, runs []StateRun) {
	t.Helper()
	off := 0
	for i, r := range spans {
		if r.Len <= 0 {
			t.Fatalf("%v: span %d is empty", p, i)
		}
		if r.Off != off {
			t.Fatalf("%v: span %d starts at %d, want %d", p, i, r.Off, off)
		}
		off += r.Len
	}
	if off != total {
		t.Fatalf("%v: spans cover %d bytes, composition is %d", p, off, total)
	}
	if runs == nil {
		return // only the annotated policy carries state runs
	}
	off = 0
	for i, r := range runs {
		if r.Len <= 0 {
			t.Fatalf("%v: run %d is empty", p, i)
		}
		if r.Off != off {
			t.Fatalf("%v: run %d starts at %d, want %d", p, i, r.Off, off)
		}
		if r.State != Accepted && r.State != Proposed && r.State != Rejected {
			t.Fatalf("%v: run %d has an impossible state %d", p, i, r.State)
		}
		off += r.Len
	}
	if off != total {
		t.Fatalf("%v: runs cover %d bytes, composition is %d", p, off, total)
	}
}

// Text and Len agree with the composition buffer's own Slice — the assumption
// every consumer makes without saying it.
func TestProjectTextAndLenAgreeWithSlice(t *testing.T) {
	s := NewSession(NewNaive("λ日x\n"))
	s.Begin()
	s.Insert(Agent, 0, "→")
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	s.Begin()
	s.Delete(User, 8, 2) // "x\n"
	s.End()

	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		d := s.Project(p)
		if got := d.Buffer().Slice(0, d.Buffer().Len()); d.Text() != got {
			t.Fatalf("%v: Text %q != Slice %q", p, d.Text(), got)
		}
		if d.Len() != len(d.Text()) {
			t.Fatalf("%v: Len %d != len(Text) %d", p, d.Len(), len(d.Text()))
		}
	}
	if got := s.Project(AcceptedOnly).Text(); got != "λ日" {
		t.Fatalf("agreed composition %q, want %q", got, "λ日")
	}
	if got := s.Project(AcceptedAndProposed).Text(); got != "→λ日" {
		t.Fatalf("edit composition %q, want %q", got, "→λ日")
	}
}

// Project is a pure derivation: the session it reads is the session after.
func TestProjectDoesNotMutateSession(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)
	s.Begin()
	s.Delete(User, 3, 1)
	s.End()

	v0 := s.Version()
	text0 := s.Buffer().Slice(0, s.Buffer().Len())
	groups0 := len(s.Groups())

	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		s.Project(p)
	}

	if s.Version() != v0 {
		t.Fatalf("Project bumped the version: %d -> %d", v0, s.Version())
	}
	if got := s.Buffer().Slice(0, s.Buffer().Len()); got != text0 {
		t.Fatalf("Project changed the buffer: %q -> %q", text0, got)
	}
	if n := len(s.Groups()); n != groups0 {
		t.Fatalf("Project changed the group list: %d -> %d", groups0, n)
	}
}

func TestProjectBaseOnlyAndEmpty(t *testing.T) {
	s := NewSession(NewNaive("base"))
	d := s.Project(AcceptedOnly)
	if d.Len() != 4 || d.Text() != "base" {
		t.Fatalf("base-only composition %q, len %d", d.Text(), d.Len())
	}
	runs := s.Project(Annotated).States()
	if len(runs) != 1 || runs[0] != (StateRun{Off: 0, Len: 4, Group: 0, State: Accepted}) {
		t.Fatalf("base-only runs = %v, want one accepted base run", runs)
	}

	empty := NewSession(NewNaive(""))
	de := empty.Project(Annotated)
	if de.Len() != 0 || de.Text() != "" || de.States() != nil {
		t.Fatalf("empty composition: %q len %d runs %v", de.Text(), de.Len(), de.States())
	}
}

// With no decisions, Project short-circuits to the session view. This checks
// that shortcut against the full forward pass on the very same text: a
// decision attached to a group whose ops have all been undone changes no
// composition and no run, so HasDecisions alone decides which path runs.
// Marking it takes the long path; withdrawing it returns to the shortcut.
func TestProjectNoDecisionsMatchesFullPass(t *testing.T) {
	s := NewSession(NewNaive("base\n"))
	s.Begin()
	s.Insert(Agent, 0, "AA")
	s.Insert(Agent, 2, "BB")
	s.End()
	s.Insert(User, 2, "mid")

	s.Begin()
	s.Insert(Agent, 0, "XX")
	s.End()
	dead := s.LastGroup()
	if !s.Undo(Agent) {
		t.Fatal("undo failed")
	}

	for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
		fast := s.Project(p)
		s.MarkGroup(dead, Proposed) // HasDecisions is now true: the full pass
		if !s.HasDecisions() {
			t.Fatal("marking a group did not register a decision")
		}
		full := s.Project(p)
		s.AcceptGroup(dead) // withdrawn: the shortcut again

		if fast.Len() != full.Len() {
			t.Fatalf("%v: Len fast %d, full %d", p, fast.Len(), full.Len())
		}
		if fast.Text() != full.Text() {
			t.Fatalf("%v: text fast %q, full %q", p, fast.Text(), full.Text())
		}
		if !slices.Equal(fast.Spans(), full.Spans()) {
			t.Fatalf("%v: spans fast %+v, full %+v", p, fast.Spans(), full.Spans())
		}
		if !slices.Equal(fast.States(), full.States()) {
			t.Fatalf("%v: states fast %+v, full %+v", p, fast.States(), full.States())
		}
	}
}

// --- fuzz -------------------------------------------------------------------

// projOracle is what FuzzProjectAgainstOracle checks Project against: an
// independent fold of the same journal over plain []byte, with its own view
// document, its own per-byte group labels and its own offset map. It reads the
// projection's inputs — journal, stores, group states — and nothing else; it
// never calls Project.
type projOracle struct {
	view    string   // the oracle's own replay of the journal
	text    string   // the composition the oracle folds
	groups  []uint64 // per composition byte: the change set that put it there
	overlap bool     // an included edit met an excluded edit's inserted span
}

// foldProjectOracle replays the journal into its own view frame — every op,
// live or not, because that is what the frame is — then folds the composition
// the way the contract describes: a live included edit is already in the view,
// and a live edit the policy excludes is un-applied from it, newest first, by
// removing the store ranges it inserted and putting its deleted bytes back.
// Dead edits and reversals contribute no bytes of their own and are not
// touched: the reversals have already neutralised the dead edits in the view.
// Where Project walks pieces, this walks bytes.
func foldProjectOracle(orig string, s *Session, p Policy) projOracle {
	journal := s.Journal()

	// Liveness, derived from the journal alone: an op is live unless a live
	// reverser reverses it.
	reversers := map[Version][]Version{}
	for _, o := range journal {
		if o.Kind != KindEdit {
			reversers[o.Undoes] = append(reversers[o.Undoes], o.Seq)
		}
	}
	liveMemo := map[Version]bool{}
	var isLive func(Version) bool
	isLive = func(seq Version) bool {
		if v, ok := liveMemo[seq]; ok {
			return v
		}
		v := !slices.ContainsFunc(reversers[seq], isLive)
		liveMemo[seq] = v
		return v
	}

	included := func(o Op) bool {
		if o.Kind != KindEdit || !isLive(o.Seq) {
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

	splice := func(src []byte, pos, del int, ins []byte) []byte {
		out := make([]byte, 0, len(src)-del+len(ins))
		out = append(out, src[:pos]...)
		out = append(out, ins...)
		out = append(out, src[pos+del:]...)
		return out
	}

	// Replay every op into the view. vsrc records, per view byte, the store
	// byte it came from, so an edit's inserted runs can still be found after
	// later ops have moved them around.
	view := []byte(orig)
	vsrc := make([]srcRef, len(view))
	for i := range vsrc {
		vsrc[i] = srcRef{buf: int(Original), start: i}
	}
	vexcl := make([]bool, len(view)) // per view byte: inside an excluded insertion
	var overlap bool

	for _, o := range journal {
		eff := included(o)
		pos, del, ins := o.Pos, o.DelLen(), o.InsLen()

		// The undefined case, detected in the oracle's own terms: the span
		// this op replaces contains bytes an excluded edit inserted.
		if eff {
			for k := pos; k < pos+del; k++ {
				if vexcl[k] {
					overlap = true
				}
			}
		}

		insText := make([]byte, 0, ins)
		insSrc := make([]srcRef, 0, ins)
		for _, r := range o.Ins {
			insText = append(insText, s.Store().Slice(Author(r.Buf), r.Start, r.Length)...)
			for j := 0; j < r.Length; j++ {
				insSrc = append(insSrc, srcRef{buf: r.Buf, start: r.Start + j})
			}
		}

		// Replay into the view frame, whatever the op is and whatever was
		// decided about it.
		view = splice(view, pos, del, insText)
		nvs := make([]srcRef, 0, len(vsrc)-del+ins)
		nvs = append(nvs, vsrc[:pos]...)
		nvs = append(nvs, insSrc...)
		nvs = append(nvs, vsrc[pos+del:]...)
		nvexcl := make([]bool, 0, len(vexcl)-del+ins)
		nvexcl = append(nvexcl, vexcl[:pos]...)
		for range ins {
			nvexcl = append(nvexcl, o.Kind == KindEdit && !eff)
		}
		nvexcl = append(nvexcl, vexcl[pos+del:]...)
		vsrc, vexcl = nvs, nvexcl
	}

	// The composition is the view with each live excluded edit un-applied,
	// newest first. comp and csrc are byte slices, so removal is a filter and a
	// splice — the byte-level form of the piece operations Project performs.
	comp := append([]byte(nil), view...)
	csrc := append([]srcRef(nil), vsrc...)
	for i := len(journal) - 1; i >= 0; i-- {
		o := journal[i]
		if o.Kind != KindEdit || !isLive(o.Seq) || included(o) {
			continue
		}
		at := -1
		out := comp[:0]
		outs := csrc[:0]
		for k := range comp {
			if srcIn(o.Ins, csrc[k]) {
				if at < 0 {
					at = len(out)
				}
				continue
			}
			out = append(out, comp[k])
			outs = append(outs, csrc[k])
		}
		comp, csrc = out, outs
		if at >= 0 {
			delText, delSrc := recsBytes(s, o.Del)
			comp = splice(comp, at, 0, delText)
			csrc = spliceSrc(csrc, at, 0, delSrc)
			continue
		}
		if o.DelLen() == 0 {
			continue
		}
		// The inserted run did not survive; carry the old position forward,
		// clamped, because the view it is measured against is not the one the
		// edit was recorded in.
		mapped, _, _, ok := s.rebase(o.Pos, o.Pos, o.Seq+1)
		if !ok {
			continue
		}
		if mapped < 0 {
			mapped = 0
		} else if mapped > len(comp) {
			mapped = len(comp)
		}
		delText, delSrc := recsBytes(s, o.Del)
		comp = splice(comp, mapped, 0, delText)
		csrc = spliceSrc(csrc, mapped, 0, delSrc)
	}

	var groups []uint64
	if p == Annotated {
		groups = make([]uint64, len(comp))
		for i, r := range csrc {
			for _, o := range journal {
				if o.Kind != KindEdit || !isLive(o.Seq) {
					continue
				}
				if srcIn(o.Ins, r) {
					groups[i] = o.Group
				}
			}
		}
	}
	return projOracle{view: string(view), text: string(comp), groups: groups, overlap: overlap}
}

// srcRef names the store byte a view or composition byte came from.
type srcRef struct {
	buf, start int
}

// srcIn reports whether r names a byte inside one of the store ranges recs
// covers: the byte-level form of insOwns.
func srcIn(recs []PieceRec, r srcRef) bool {
	for _, p := range recs {
		if p.Buf == r.buf && r.start >= p.Start && r.start < p.Start+p.Length {
			return true
		}
	}
	return false
}

// recsBytes reads a piece list out of the stores as text plus one srcRef per
// byte, so an un-applied edit's deleted bytes can be spliced back with their
// provenance intact.
func recsBytes(s *Session, recs []PieceRec) ([]byte, []srcRef) {
	var text []byte
	src := make([]srcRef, 0, recsLen(recs))
	for _, r := range recs {
		text = append(text, s.Store().Slice(Author(r.Buf), r.Start, r.Length)...)
		for j := 0; j < r.Length; j++ {
			src = append(src, srcRef{buf: r.Buf, start: r.Start + j})
		}
	}
	return text, src
}

// spliceSrc is splice for provenance: insert ins at pos, dropping del bytes.
func spliceSrc(src []srcRef, pos, del int, ins []srcRef) []srcRef {
	out := make([]srcRef, 0, len(src)-del+len(ins))
	out = append(out, src[:pos]...)
	out = append(out, ins...)
	out = append(out, src[pos+del:]...)
	return out
}

// oracleRuns turns the oracle's per-byte groups into runs the way States does:
// base bytes are group 0 and always Accepted; adjacent runs merge when group
// and state both match.
func oracleRuns(groups []uint64, s *Session) []StateRun {
	runs := make([]StateRun, 0, len(groups))
	for i, g := range groups {
		st := Accepted
		if g != 0 {
			st = s.GroupState(g)
		}
		if n := len(runs); n > 0 && runs[n-1].Group == g && runs[n-1].State == st {
			runs[n-1].Len++
			continue
		}
		runs = append(runs, StateRun{Off: i, Len: 1, Group: g, State: st})
	}
	return runs
}

// FuzzProjectAgainstOracle builds a random journal — edits, undos, redos,
// rejections and bare decisions — and checks, after every step and under every
// policy, that Project agrees with an independent byte-level fold of the same
// journal.
//
// Where the journal contains an included edit whose span intersects the
// interior of an excluded edit's inserted span, the contract does not define
// the composition; the oracle detects that in its own terms and the test then
// asserts only that nothing panicked and the lengths still agree.
func FuzzProjectAgainstOracle(f *testing.F) {
	// One program per op kind, plus ones that mix decisions with reversals.
	f.Add([]byte{0, 1, 5, 0, 0, 2, 2, 0, 1, 1, 4, 0, 3, 0, 5, 1})
	f.Add([]byte{0, 0, 1, 4, 5, 1, 0, 3, 2, 1, 4, 1, 1, 2, 3, 0})
	f.Add([]byte{1, 9, 4, 0, 0, 2, 5, 0, 2, 0, 3, 1, 0, 4, 5, 1})
	f.Add([]byte{5, 1, 5, 0, 4, 1, 2, 0, 3, 0, 0, 1, 1, 3, 2, 1})
	f.Add([]byte("1B11200"))

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 96 {
			program = program[:96] // a long program is not a more interesting one
		}
		const orig = "λ日x\n"
		s := NewSession(NewNaive(orig))
		runes := []string{"a", "λ", "日", "\n", "→"}
		authors := []Author{User, Agent}
		var log []string

		for i := 0; i+1 < len(program); i += 2 {
			op, arg := program[i], program[i+1]
			author := authors[int(arg)%len(authors)]

			switch op % 6 {
			case 0: // insert a whole rune at a rune boundary
				text := s.Buffer().Slice(0, s.Buffer().Len())
				pos := runeStart(text, int(arg)%(len(text)+1))
				s.Insert(author, pos, runes[int(arg)%len(runes)])
				log = append(log, "ins")
			case 1: // delete exactly one rune
				text := s.Buffer().Slice(0, s.Buffer().Len())
				if len(text) == 0 {
					continue
				}
				pos := runeStart(text, int(arg)%len(text))
				_, size := utf8.DecodeRuneInString(text[pos:])
				s.Delete(author, pos, size)
				log = append(log, "del")
			case 2:
				s.Undo(author)
				log = append(log, "undo")
			case 3:
				s.Redo(author)
				log = append(log, "redo")
			case 4: // reject a change set: marked Rejected, text left in place
				groups := s.Groups()
				if len(groups) == 0 {
					continue
				}
				s.RejectGroup(groups[int(arg)%len(groups)].ID)
				log = append(log, "reject")
			case 5: // a bare decision: marked, never reversed
				groups := s.Groups()
				if len(groups) == 0 {
					continue
				}
				id := groups[int(arg)%len(groups)].ID
				if int(arg)%2 == 0 {
					s.MarkGroup(id, Proposed)
					log = append(log, "propose")
				} else {
					s.MarkGroup(id, Rejected)
					log = append(log, "rejectmark")
				}
			}

			now := s.Buffer().Slice(0, s.Buffer().Len())
			for _, p := range []Policy{AcceptedOnly, AcceptedAndProposed, Annotated} {
				proj := s.Project(p) // no panic is part of the contract, always
				orc := foldProjectOracle(orig, s, p)

				// The oracle's replay of the journal must reproduce the very
				// buffer the session holds, or every comparison below is
				// comparing against the wrong document.
				if orc.view != now {
					t.Fatalf("oracle replay %q diverged from the buffer %q\nlog=%v",
						orc.view, now, log)
				}
				if proj.Len() != len(orc.text) {
					t.Fatalf("%v: Len %d, oracle %d\nlog=%v", p, proj.Len(), len(orc.text), log)
				}
				if orc.overlap {
					continue // the contract does not define the composition here
				}
				if got := proj.Text(); got != orc.text {
					t.Fatalf("%v: composition %q, oracle %q\nlog=%v", p, got, orc.text, log)
				}
				if p == Annotated {
					want := oracleRuns(orc.groups, s)
					got := proj.States()
					if len(got) != len(want) {
						t.Fatalf("annotated runs = %v, oracle %v\nlog=%v", got, want, log)
					}
					for i := range want {
						if got[i] != want[i] {
							t.Fatalf("run %d = %+v, oracle %+v\nlog=%v", i, got[i], want[i], log)
						}
					}
				}
			}
		}
	})
}

// A pending or rejected span is a lease. Leased names its owning set strictly
// inside the run and reports nothing at its boundaries: the half-open rule
// makes an insertion flush with either edge fall outside, matching where an
// insert at that offset lands relative to the run.
func TestLeasedNamesTheOwningRun(t *testing.T) {
	s := NewSession(NewNaive("AB"))
	s.Begin()
	s.Insert(Agent, 1, "xy")
	s.End()
	id := s.LastGroup()

	if _, ok := s.Leased(2, 0); ok {
		t.Fatal("nothing is leased before a decision")
	}
	s.MarkGroup(id, Proposed)

	if g, ok := s.Leased(1, 0); ok || g != 0 {
		t.Errorf("Leased at the run start = (%d,%v), want not leased", g, ok)
	}
	if g, ok := s.Leased(3, 0); ok || g != 0 {
		t.Errorf("Leased at the run end = (%d,%v), want not leased", g, ok)
	}
	if g, ok := s.Leased(2, 0); !ok || g != id {
		t.Errorf("Leased strictly inside = (%d,%v), want (%d,true)", g, ok, id)
	}
	if g, ok := s.Leased(1, 2); !ok || g != id {
		t.Errorf("Leased over the run = (%d,%v), want (%d,true)", g, ok, id)
	}
	if g, ok := s.Leased(1, 1); !ok || g != id {
		t.Errorf("Leased over the first byte = (%d,%v), want (%d,true)", g, ok, id)
	}
	if g, ok := s.Leased(0, 1); ok || g != 0 {
		t.Errorf("Leased on base text = (%d,%v), want not leased", g, ok)
	}
	if g, ok := s.Leased(3, 1); ok || g != 0 {
		t.Errorf("Leased just past the run = (%d,%v), want not leased", g, ok)
	}

	// Accepting ends the lease; rejecting re-establishes it, since the text
	// stays and only the decision changed.
	s.AcceptGroup(id)
	if _, ok := s.Leased(2, 0); ok {
		t.Error("accepted text must not be leased")
	}
	s.RejectGroup(id)
	if g, ok := s.Leased(2, 0); !ok || g != id {
		t.Errorf("rejected text = (%d,%v), want leased to %d", g, ok, id)
	}
}

// A change set that only deletes has no inserted run, so Leased cannot name it
// and the deletion is not protected. This is the known limitation of a lease
// built from inserted runs; it is reported, not hidden.
func TestLeasedIgnoresPureDeletionSet(t *testing.T) {
	s := NewSession(NewNaive("ABC"))
	s.Begin()
	s.Delete(Agent, 1, 1) // "B", inserting nothing
	s.End()
	s.MarkGroup(s.LastGroup(), Proposed)

	if _, ok := s.Leased(0, 3); ok {
		t.Error("a pure-deletion set has no inserted run and cannot be leased")
	}
}

// stateRuns labels each composition piece from the origins, and the origins do
// not arrive in start order: pieces follow the composition while origins
// follow the journal, and a change set can splice into a store more than once.
// This hands stateRuns origins deliberately out of order, and two claims on
// one store range, and checks the ownership, the states and the run merge.
func TestStateRunsIndexesSeveralOrigins(t *testing.T) {
	s := NewSession(NewNaive("base"))
	s.Insert(Agent, 0, "AA")
	accepted := s.LastGroup()
	s.Insert(Agent, 0, "BB")
	rejected := s.LastGroup()
	s.MarkGroup(rejected, Rejected)
	s.Insert(Agent, 0, "CC")
	proposed := s.LastGroup()
	s.MarkGroup(proposed, Proposed)

	// The second claim on [100,104) is newer, so it must win; the origins
	// arrive in neither start nor ownership order.
	origins := []insOrigin{
		{buf: int(Agent), start: 100, end: 104, group: proposed},
		{buf: int(Agent), start: 40, end: 44, group: accepted},
		{buf: int(Agent), start: 100, end: 104, group: rejected},
	}
	pieces := []PieceRec{
		{Buf: int(Original), Start: 0, Length: 4}, // base
		{Buf: int(Agent), Start: 40, Length: 4},   // accepted
		{Buf: int(Agent), Start: 100, Length: 4},  // doubly claimed: rejected wins
		{Buf: int(Agent), Start: 900, Length: 4},  // no claim
	}
	want := []StateRun{
		{Off: 0, Len: 4, Group: 0, State: Accepted},
		{Off: 4, Len: 4, Group: accepted, State: Accepted},
		{Off: 8, Len: 4, Group: rejected, State: Rejected},
		{Off: 12, Len: 4, Group: 0, State: Accepted},
	}
	if got := s.stateRuns(pieces, origins); !slices.Equal(got, want) {
		t.Fatalf("stateRuns = %+v, want %+v", got, want)
	}
}
