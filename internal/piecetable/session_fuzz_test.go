package piecetable

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The seeded walk in undo_utf8_test.go is a fixed budget of random programs and
// it is what found the rebase bug. This is the same idea handed to the fuzzer
// instead of to math/rand, which buys three things that budget cannot:
//
//   - It is coverage-guided, so it steers toward the branches in rebase and
//     reverseGroup rather than sampling uniformly and mostly re-walking the
//     easy paths.
//   - It shrinks. A failure arrives as the shortest program that still fails,
//     which is the difference between "seed 578 step 12" and a five-op
//     reproduction you can read.
//   - It covers ops the seeded walk does not: two authors interleaved, and
//     rejection, which is undo addressed by group and therefore the operation
//     most likely to reverse something a later op depends on.
//
// The oracle is the same one the rest of the package uses: Naive applies the
// identical journal to a flat string, so a disagreement is a bug in the tree or
// in the offsets the session computed, and never in both at once.
func FuzzSessionAgainstOracle(f *testing.F) {
	// Programs that exercise each op at least once, so the corpus starts
	// somewhere useful rather than at the empty document.
	f.Add([]byte{0, 3, 1, 0, 2, 0, 3, 0})
	f.Add([]byte{0, 0, 0, 1, 4, 2, 3, 4})
	f.Add([]byte{1, 9, 4, 4, 0, 2, 5, 1})

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

		for i := 0; i+1 < len(program); i += 2 {
			op, arg := program[i], program[i+1]
			text := doc.Buffer().Slice(0, doc.Buffer().Len())
			author := authors[int(arg)%len(authors)]

			switch op % 5 {
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
				// Both sessions have applied the same journal in the same
				// order, so the group counter agrees and the same id addresses
				// the same change set in each. They must also agree about
				// whether it could be backed out: a reversal that succeeds on
				// one engine and fails on the other is a divergence even when
				// the text still matches afterwards.
				if doc.RejectGroup(id) != oracle.RejectGroup(id) {
					t.Fatalf("engines disagreed about rejecting group %d\nlog=%v", id, log)
				}
				log = append(log, "reject")
			}

			got := doc.Buffer().Slice(0, doc.Buffer().Len())
			want := oracle.Buffer().Slice(0, oracle.Buffer().Len())
			if got != want {
				t.Fatalf("doc %q, oracle %q\nlog=%v", got, want, strings.Join(log, ","))
			}
			// Every edit above is rune-aligned, so a partial rune can only come
			// from a reversal applied at an offset that was carried forward
			// wrongly. Worth asserting separately from the oracle comparison,
			// because both engines apply the session's offsets faithfully and
			// will therefore agree on a document neither the user nor either
			// engine ever created.
			if !utf8.ValidString(got) {
				t.Fatalf("a reversal split a rune: %q\nlog=%v", got, strings.Join(log, ","))
			}
			if doc.Buffer().Len() != len(want) {
				t.Fatalf("Len()=%d but the text is %d bytes\nlog=%v",
					doc.Buffer().Len(), len(want), strings.Join(log, ","))
			}
		}
	})
}

// both applies one edit to each engine inside a transaction, so the two
// journals stay in step and one user action is one group.
func both(doc, oracle *Session, fn func(*Session)) {
	for _, s := range []*Session{doc, oracle} {
		s.Begin()
		fn(s)
		s.End()
	}
}
