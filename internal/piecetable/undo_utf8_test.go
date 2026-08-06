package piecetable

import (
	"math/rand"
	"testing"
	"unicode/utf8"
)

// undoRedoSeeds is how many random undo/redo sequences this walks.
//
// It sat at 500 while the rebase walk still counted a resurrected op in the
// wrong coordinate frame; 3000 reproduced that at seed 578 step 12, which is
// how it was found and fixed. 5000 is the standing budget now — it runs in
// about a second, and 40000 has been walked by hand without a failure.
const undoRedoSeeds = 5000

// Every edit here is rune-aligned, so the document can only end up holding a
// partial rune if a reversal was applied at the wrong offset. That is worth
// asserting separately from "the two engines agree", because they can agree on
// a document neither the user nor either engine ever created: the offsets come
// from the session, and both buffers apply them faithfully.
//
// This is what caught the rebase bug that made undo delete the wrong bytes when
// a later insertion landed exactly at the start of the range being reversed.
func TestUndoRedoKeepsRunesIntact(t *testing.T) {
	runes := []string{"a", "λ", "日", "\n", "→"}
	for seed := int64(0); seed < undoRedoSeeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		doc := NewSession(NewDoc("λ日x\n", 5))
		oracle := NewSession(NewNaive("λ日x\n"))
		var log []string

		for step := 0; step < 16; step++ {
			text := doc.Buffer().Slice(0, doc.Buffer().Len())
			switch rng.Intn(4) {
			case 0:
				pos := runeStart(text, rng.Intn(len(text)+1))
				r := runes[rng.Intn(len(runes))]
				edit(doc, oracle, func(s *Session) { s.Insert(1, pos, r) })
				log = append(log, "ins")
			case 1:
				if len(text) == 0 {
					continue
				}
				pos := runeStart(text, rng.Intn(len(text)))
				_, size := utf8.DecodeRuneInString(text[pos:])
				edit(doc, oracle, func(s *Session) { s.Delete(1, pos, size) })
				log = append(log, "del")
			case 2:
				doc.Undo(1)
				oracle.Undo(1)
				log = append(log, "undo")
			case 3:
				doc.Redo(1)
				oracle.Redo(1)
				log = append(log, "redo")
			}

			got := doc.Buffer().Slice(0, doc.Buffer().Len())
			if want := oracle.Buffer().Slice(0, oracle.Buffer().Len()); got != want {
				t.Fatalf("seed %d step %d: doc %q, oracle %q\nlog=%v", seed, step, got, want, log)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("seed %d step %d: a reversal split a rune: %q\nlog=%v", seed, step, got, log)
			}
		}
	}
}

func edit(doc, oracle *Session, fn func(*Session)) {
	for _, s := range []*Session{doc, oracle} {
		s.Begin()
		fn(s)
		s.End()
	}
}

// runeStart rounds an index down to a rune boundary.
func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
