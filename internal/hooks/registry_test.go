package hooks

import (
	"sync"
	"testing"
	"time"
)

// TestRegistry covers the lifecycle and the ID ordering. Precondition: a fresh
// registry. It fails if Add does not assign increasing IDs, if a caller's ID
// wins, if Remove forgets or panics on an absent ID, or if List is not ordered
// by ID and is not a snapshot.
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if got := r.Len(); got != 0 {
		t.Fatalf("Len on a fresh registry = %d; want 0", got)
	}
	if list := r.List(); list == nil || len(list) != 0 {
		t.Fatalf("List on a fresh registry = %v; want an empty non-nil slice", list)
	}

	start := time.Unix(1_700_000_000, 0)
	// The caller's ID is replaced, not trusted.
	if id := r.Add(Run{ID: 99, Hook: "a", Author: 1, PID: 10, PGID: 10, Started: start}); id == 99 {
		t.Fatalf("Add kept the caller's ID 99; want an assigned one")
	}
	id2 := r.Add(Run{Hook: "b", Author: 2, PID: 11, PGID: 11, Started: start.Add(time.Second)})
	if id2 != 2 {
		t.Fatalf("second Add ID = %d; want 2", id2)
	}
	if got := r.Len(); got != 2 {
		t.Fatalf("Len after two Adds = %d; want 2", got)
	}

	list := r.List()
	if len(list) != 2 || list[0].ID != 1 || list[1].ID != id2 {
		t.Fatalf("List = %+v; want IDs 1 then %d", list, id2)
	}
	if list[0].Hook != "a" || list[0].PID != 10 || list[0].Author != 1 || !list[0].Started.Equal(start) {
		t.Fatalf("List[0] = %+v; want the first run's payload", list[0])
	}
	// List hands back a snapshot: mutating it must not change the registry.
	list[0].Hook = "mutated"
	if again := r.List(); again[0].Hook != "a" {
		t.Fatalf("List[0].Hook = %q after mutating a previous List; want a copy", again[0].Hook)
	}

	r.Remove(1)
	if got := r.Len(); got != 1 {
		t.Fatalf("Len after Remove = %d; want 1", got)
	}
	if list := r.List(); len(list) != 1 || list[0].ID != id2 {
		t.Fatalf("List after Remove = %+v; want only ID %d", list, id2)
	}
	r.Remove(999) // an absent ID is a no-op
	if got := r.Len(); got != 1 {
		t.Fatalf("Len after Remove of an absent ID = %d; want 1", got)
	}
}

// TestRegistryConcurrent is the -race case: many goroutines add, inspect and
// remove runs at once. Precondition: a fresh registry and 32 workers each
// adding one run. It fails under -race if a verb touches the map or the next
// counter without the mutex, and the final zero Len catches bookkeeping that
// is merely race-free but wrong.
func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			id := r.Add(Run{Hook: "h", Author: uint8(w), PID: w, PGID: w, Started: time.Unix(int64(w), 0)})
			_ = r.List()
			_ = r.Len()
			r.Remove(id)
		}(w)
	}
	wg.Wait()
	if got := r.Len(); got != 0 {
		t.Fatalf("Len after concurrent add/remove = %d; want 0", got)
	}
	if list := r.List(); len(list) != 0 {
		t.Fatalf("List after concurrent add/remove = %+v; want empty", list)
	}
}
