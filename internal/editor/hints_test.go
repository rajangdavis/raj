package editor

import "testing"

// hint is a terse fixture: a hint at a line-relative offset.
func hint(off int, text string) Hint {
	return Hint{Off: off, Text: text}
}

// A hint set groups by line and keeps each line's hints ordered by offset, so
// the renderer can walk them in the order it draws them.
func TestHintSetSortsByOffset(t *testing.T) {
	var s HintSet
	s.Add(3, hint(20, "third"))
	s.Add(3, hint(4, "first"))
	s.Add(3, hint(12, "second"))

	got := s.At(3)
	want := []int{4, 12, 20}
	if len(got) != len(want) {
		t.Fatalf("got %d hints, want %d", len(got), len(want))
	}
	for i, off := range want {
		if got[i].Off != off {
			t.Fatalf("order = %v, want offsets %v", got, want)
		}
	}
}

// Lines are independent: adding to one must not disturb another.
func TestHintSetGroupsByLine(t *testing.T) {
	var s HintSet
	s.Add(1, hint(0, "a"))
	s.Add(2, hint(0, "b"))

	if got := s.At(1); len(got) != 1 || got[0].Text != "a" {
		t.Errorf("line 1 = %v, want a", got)
	}
	if got := s.At(2); len(got) != 1 || got[0].Text != "b" {
		t.Errorf("line 2 = %v, want b", got)
	}
}

// An unknown line on a populated set, an empty set and a nil set all answer
// nil, which is the renderer's common case and its fast path.
func TestHintSetAtReturnsNilForNoHints(t *testing.T) {
	var s HintSet
	if got := s.At(0); got != nil {
		t.Errorf("At on an empty set = %v, want nil", got)
	}
	s.Add(1, hint(0, "x"))
	if got := s.At(2); got != nil {
		t.Errorf("At on a line with no hints = %v, want nil", got)
	}
	var nilSet *HintSet
	if got := nilSet.At(0); got != nil {
		t.Errorf("At on a nil set = %v, want nil", got)
	}
}

func TestHintSetLenAndEmpty(t *testing.T) {
	var s HintSet
	if !s.Empty() || s.Len() != 0 {
		t.Errorf("a fresh set: Len = %d, Empty = %v, want 0 and true", s.Len(), s.Empty())
	}
	s.Add(0, hint(1, "a"))
	s.Add(0, hint(2, "b"))
	s.Add(2, hint(0, "c"))
	if s.Len() != 3 {
		t.Errorf("Len = %d, want 3", s.Len())
	}
	if s.Empty() {
		t.Error("a set with hints reported empty")
	}
}

// File.HintsAt is nil-safe before a set is installed, and SetHints/ClearHints
// are the whole install and drop path the event thread uses.
func TestFileHintsSetAndClear(t *testing.T) {
	f := NewFile("/w/a.go", "one\ntwo\n", 4)
	if got := f.HintsAt(0); got != nil {
		t.Errorf("HintsAt with no set = %v, want nil", got)
	}

	var s HintSet
	s.Add(1, Hint{Off: 0, Text: "x"})
	f.SetHints(&s)
	got := f.HintsAt(1)
	if len(got) != 1 || got[0].Text != "x" {
		t.Fatalf("HintsAt = %v, want the installed hint", got)
	}
	if f.HintsAt(0) != nil {
		t.Error("HintsAt found a hint on a line with none")
	}

	f.ClearHints()
	if f.Hints != nil {
		t.Error("ClearHints left a set behind")
	}
	if got := f.HintsAt(1); got != nil {
		t.Errorf("after ClearHints HintsAt = %v, want nil", got)
	}
}
