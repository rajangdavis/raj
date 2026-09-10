package piecetable

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzSessionAgainstOracle proves the two engines agree on the text. This one
// proves they agree on RANGES carried through the same history: a range
// recorded against an old version must rebase to the same place in both
// engines, ordered and in bounds, and must either still cover the bytes it
// was recorded over or be refused as a conflict — never silently land on
// different bytes.
//
// That pair of properties is what incremental document sync and completion
// textEdit honouring will stand on: both take a range computed against a
// version that is no longer current and carry it forward, and a range that
// lands wrong is a desynchronised server or a patch applied to the wrong
// span. The op soup is the sibling harness's, extended with one verb: track.
func FuzzRangeRebaseAgainstOracle(f *testing.F) {
	// One seed per tricky shape. Each starts tracking a range and then edits
	// against it:
	//   - a caret at the origin, with an insert and a delete flush against it
	//   - an insert strictly inside a range, which must conflict, then the
	//     insert undone, which must resurrect the range
	//   - three adjacent ranges; the middle one's whole span deleted, which
	//     must break that range and carry the other two flush against each
	//     other
	//   - a tracked range deleted, then the delete rejected: the range is
	//     parked and restored through the anchor records
	//   - inserts by two authors around a range, undone and redone
	f.Add([]byte{0x05, 0x11, 0x00, 0x00, 0x01, 0x00})
	f.Add([]byte{0x05, 0x2f, 0x00, 0x0e, 0x02, 0x00})
	f.Add([]byte{0x05, 0x20, 0x05, 0x52, 0x05, 0x75, 0x01, 0x02})
	f.Add([]byte{0x05, 0x52, 0x01, 0x02, 0x04, 0x00})
	f.Add([]byte{0x05, 0x52, 0x00, 0x03, 0x00, 0x06, 0x02, 0x00, 0x02, 0x01, 0x03, 0x00})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64] // a long program is not a more interesting one
		}
		const orig = "λ日x\n"
		doc := NewSession(NewDoc(orig, 5))
		oracle := NewSession(NewNaive(orig))

		runes := []string{"a", "λ", "日", "\n", "→"}
		authors := []Author{User, Agent}
		var log []string

		// Ranges recorded against past versions, kept in a small ring so old
		// ones are eventually dropped for new ones. Six is enough for ranges
		// to end up nested, adjacent, and inside each other's deletions.
		const slots = 6
		var tracked [slots]trackedRange
		filled := 0

		for i := 0; i+1 < len(program); i += 2 {
			op, arg := program[i], program[i+1]
			text := doc.Buffer().Slice(0, doc.Buffer().Len())
			author := authors[int(arg)%len(authors)]

			switch op % 6 {
			case 0: // insert a whole rune at a rune boundary
				pos := runeStart(text, int(arg)%(len(text)+1))
				r := runes[int(arg)%len(runes)]
				both(doc, oracle, func(s *Session) { s.Insert(author, pos, r) })
				log = append(log, "ins")
			case 1: // delete exactly one rune
				if len(text) == 0 {
					continue
				}
				pos := runeStart(text, int(arg)%len(text))
				_, size := utf8.DecodeRuneInString(text[pos:])
				both(doc, oracle, func(s *Session) { s.Delete(author, pos, size) })
				log = append(log, "del")
			case 2:
				doc.Undo(author)
				oracle.Undo(author)
				log = append(log, "undo")
			case 3:
				doc.Redo(author)
				oracle.Redo(author)
				log = append(log, "redo")
			case 4: // reject a change set, which is undo addressed by group
				groups := doc.Groups()
				if len(groups) == 0 {
					continue
				}
				id := groups[int(arg)%len(groups)].ID
				if doc.RejectGroup(id) != oracle.RejectGroup(id) {
					t.Fatalf("engines disagreed about rejecting group %d\nlog=%v", id, log)
				}
				log = append(log, "reject")
			case 5: // start tracking a range against the current version
				// The low nibble picks the start, the high one the end;
				// equal nibbles give a caret.
				lo := runeStart(text, int(arg&0x0f)%(len(text)+1))
				hi := runeStart(text, int(arg>>4)%(len(text)+1))
				if lo > hi {
					lo, hi = hi, lo
				}
				tracked[filled%slots] = trackedRange{
					lo: lo, hi: hi,
					base: doc.Version(),
					text: text[lo:hi],
				}
				filled++
				log = append(log, "track")
			}

			// The engines must agree on the text before a range comparison
			// means anything.
			got := doc.Buffer().Slice(0, doc.Buffer().Len())
			want := oracle.Buffer().Slice(0, oracle.Buffer().Len())
			if got != want {
				t.Fatalf("doc %q, oracle %q\nlog=%v", got, want, strings.Join(log, ","))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("a reversal split a rune: %q\nlog=%v", got, strings.Join(log, ","))
			}
			if doc.Buffer().Len() != len(want) {
				t.Fatalf("Len()=%d but the text is %d bytes\nlog=%v",
					doc.Buffer().Len(), len(want), strings.Join(log, ","))
			}

			n := filled
			if n > slots {
				n = slots
			}
			for k := 0; k < n; k++ {
				checkRangeRebase(t, doc, oracle, tracked[k], log)
			}
		}
	})
}

// trackedRange is a range recorded against version base, and the bytes it
// covered then.
type trackedRange struct {
	lo, hi int
	base   Version
	text   string
}

// checkRangeRebase carries one tracked range from its version to the present
// in both engines and asserts the invariants everything downstream of rebase
// relies on.
func checkRangeRebase(t *testing.T, doc, oracle *Session, tr trackedRange, log []string) {
	t.Helper()
	dLo, dHi, dAt, dOk := doc.rebase(tr.lo, tr.hi, tr.base)
	oLo, oHi, oAt, oOk := oracle.rebase(tr.lo, tr.hi, tr.base)

	// The engines are two structures over one journal, and a range carried
	// through the journal must land in the same place in each — or in
	// neither, with the same op to blame. A disagreement is a bug in the
	// tree or in the walk, and never in both at once.
	if dOk != oOk || (dOk && (dLo != oLo || dHi != oHi)) || (!dOk && dAt != oAt) {
		t.Fatalf("engines disagree on range [%d,%d) recorded at %d: doc (%d,%d,%v), oracle (%d,%d,%v)\nlog=%v",
			tr.lo, tr.hi, tr.base, dLo, dHi, dOk, oLo, oHi, oOk, strings.Join(log, ","))
	}
	if !dOk {
		// Refusal is the defined answer when a live edit damaged the range's
		// bytes — never a clamp onto text the range did not name.
		return
	}

	// Ordered and inside the document. ApplyDiff splices at the rebased
	// offsets, so an inverted or out-of-bounds pair here is a backwards
	// delete there.
	if dLo < 0 || dLo > dHi || dHi > doc.Buffer().Len() {
		t.Fatalf("range [%d,%d) recorded at %d rebased to (%d,%d), document is %d bytes\nlog=%v",
			tr.lo, tr.hi, tr.base, dLo, dHi, doc.Buffer().Len(), strings.Join(log, ","))
	}

	// A range that is carried at all must still name the same bytes. Only a
	// live edit strictly inside the range can change them, and that is a
	// conflict — so a successful rebase holding different bytes is silent
	// corruption.
	if got := doc.Buffer().Slice(dLo, dHi-dLo); got != tr.text {
		t.Fatalf("range [%d,%d) recorded at %d held %q, rebased to (%d,%d) holding %q\nlog=%v",
			tr.lo, tr.hi, tr.base, tr.text, dLo, dHi, got, strings.Join(log, ","))
	}

	// Rebasing against the present is the identity: nothing in the window,
	// nothing moved.
	lo2, hi2, _, ok2 := doc.rebase(dLo, dHi, doc.Version())
	if !ok2 || lo2 != dLo || hi2 != dHi {
		t.Fatalf("range (%d,%d) at the present version remapped to (%d,%d,%v)\nlog=%v",
			dLo, dHi, lo2, hi2, ok2, strings.Join(log, ","))
	}
}
