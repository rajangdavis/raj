package piecetable

import (
	"math/rand"
	"testing"
)

// Undoing every action must return the document exactly to its starting text.
func TestFuzzUndoRestoresOriginal(t *testing.T) {
	for seed := 0; seed < 3000; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		const orig = "package main\n\nfunc main() {\n\tprintln(1)\n}\n"
		s := NewSession(NewDoc(orig, 5))

		actions := rng.Intn(8) + 1
		for a := 0; a < actions; a++ {
			s.Begin()
			edits := rng.Intn(3) + 1
			for e := 0; e < edits; e++ {
				n := s.Buffer().Len()
				if rng.Intn(2) == 0 && n > 1 {
					pos := rng.Intn(n - 1)
					s.Delete(User, pos, rng.Intn(4)+1)
				} else {
					pos := 0
					if n > 0 {
						pos = rng.Intn(n + 1)
					}
					s.Insert(User, pos, "XY")
				}
			}
			s.End()
		}

		for i := 0; i < actions; i++ {
			if !s.Undo(User) {
				t.Fatalf("seed %d: undo %d refused with %d actions", seed, i, actions)
			}
		}
		if got := s.Buffer().Slice(0, s.Buffer().Len()); got != orig {
			t.Fatalf("seed %d: after undoing %d actions\n got %q\nwant %q", seed, actions, got, orig)
		}
	}
}

// Redoing everything must return to the final text.
func TestFuzzRedoRestoresFinal(t *testing.T) {
	for seed := 0; seed < 3000; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		s := NewSession(NewDoc("abcdefghij\n", 5))
		actions := rng.Intn(6) + 1
		for a := 0; a < actions; a++ {
			s.Begin()
			for e := rng.Intn(3) + 1; e > 0; e-- {
				n := s.Buffer().Len()
				if rng.Intn(2) == 0 && n > 1 {
					s.Delete(User, rng.Intn(n-1), 1)
				} else {
					s.Insert(User, rng.Intn(n+1), "Z")
				}
			}
			s.End()
		}
		want := s.Buffer().Slice(0, s.Buffer().Len())
		for i := 0; i < actions; i++ {
			s.Undo(User)
		}
		for i := 0; i < actions; i++ {
			s.Redo(User)
		}
		if got := s.Buffer().Slice(0, s.Buffer().Len()); got != want {
			t.Fatalf("seed %d:\n got %q\nwant %q", seed, got, want)
		}
	}
}

// Undo and redo must interleave: undo, redo, undo again all reach the states a
// user would expect, in any order.
func TestFuzzUndoRedoInterleaved(t *testing.T) {
	for seed := 0; seed < 3000; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		s := NewSession(NewDoc("hello world\n", 5))

		var states []string // states[i] is the text after i actions
		states = append(states, s.Buffer().Slice(0, s.Buffer().Len()))
		actions := rng.Intn(5) + 2
		for a := 0; a < actions; a++ {
			s.Begin()
			for e := rng.Intn(3) + 1; e > 0; e-- {
				n := s.Buffer().Len()
				if rng.Intn(2) == 0 && n > 1 {
					s.Delete(User, rng.Intn(n-1), 1)
				} else {
					s.Insert(User, rng.Intn(n+1), "Q")
				}
			}
			s.End()
			states = append(states, s.Buffer().Slice(0, s.Buffer().Len()))
		}

		at := actions
		for step := 0; step < 40; step++ {
			if rng.Intn(2) == 0 {
				if s.Undo(User) {
					at--
				}
			} else if s.Redo(User) {
				at++
			}
			if at < 0 || at > actions {
				t.Fatalf("seed %d: position %d out of range", seed, at)
			}
			if got := s.Buffer().Slice(0, s.Buffer().Len()); got != states[at] {
				t.Fatalf("seed %d step %d: at action %d\n got %q\nwant %q",
					seed, step, at, got, states[at])
			}
		}
	}
}
