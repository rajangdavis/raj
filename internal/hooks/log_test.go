package hooks

import "testing"

// TestLogKeepsMaxDropsOldest pins the run log ring: a Log keeps at most max
// entries, dropping the oldest as new ones arrive, and List is an oldest-first
// snapshot. Precondition: a Log sized 3. Without the ring the fourth Add would
// make the list four long; without the snapshot a caller could mutate the
// stored entries.
func TestLogKeepsMaxDropsOldest(t *testing.T) {
	l := NewLog(3)
	for i := 1; i <= 4; i++ {
		l.Add(Result{ID: uint64(i), Hook: "h"})
	}
	got := l.List()
	if len(got) != 3 {
		t.Fatalf("List after 4 adds to a size-3 log holds %d, want 3", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 3 || got[2].ID != 4 {
		t.Errorf("List ids = %d,%d,%d, want 2,3,4: the oldest was not dropped",
			got[0].ID, got[1].ID, got[2].ID)
	}
	if l.Len() != 3 {
		t.Errorf("Len = %d, want 3", l.Len())
	}
	// List is a snapshot: mutating it must not change the log.
	got[0].Hook = "mutated"
	if again := l.List(); again[0].Hook != "h" {
		t.Errorf("List[0].Hook = %q after mutating a previous List; want a copy", again[0].Hook)
	}
	// A non-positive max takes the shipped default.
	if def := NewLog(0); def.max != DefaultLogSize {
		t.Errorf("NewLog(0).max = %d, want the default %d", def.max, DefaultLogSize)
	}
}
