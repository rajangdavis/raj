package review

import (
	"strings"
	"testing"

	"raj/internal/control"
)

// TestItemStateSplitsInvalidAndSuperseded pins the mapping from the wire's one
// Invalid bool to the two queue states: a named collider is superseded, an
// unnamed one is invalid. Without this mapping every invalid set would render
// as proposed and be offered for an accept that cannot work.
func TestItemStateSplitsInvalidAndSuperseded(t *testing.T) {
	named := control.Group{State: "proposed", Invalid: true,
		InvalidBy: &control.GroupOverlap{Group: 9}}
	if got := itemState(named); got != StateSuperseded {
		t.Fatalf("named collider: got %q, want %q", got, StateSuperseded)
	}
	unnamed := control.Group{State: "proposed", Invalid: true}
	if got := itemState(unnamed); got != StateInvalid {
		t.Fatalf("unnamed collider: got %q, want %q", got, StateInvalid)
	}
	if got := itemState(control.Group{State: "proposed"}); got != StateProposed {
		t.Fatalf("live set: got %q, want %q", got, StateProposed)
	}
	if got := itemState(control.Group{State: "rejected"}); got != StateRejected {
		t.Fatalf("rejected set: got %q, want %q", got, StateRejected)
	}
}

// TestQueueSortsProposedThenRejectedThenInvalid builds the states out of order
// and asserts the review order. Without Sort the queue would render in whatever
// order the transport returned, putting rejected and invalid sets ahead of the
// work that is actually decidable.
func TestQueueSortsProposedThenRejectedThenInvalid(t *testing.T) {
	q := Queue{Items: []Item{
		{File: "c.go", Group: 3, State: StateInvalid},
		{File: "b.go", Group: 2, State: StateRejected},
		{File: "a.go", Group: 4, State: StateProposed},
		{File: "a.go", Group: 1, State: StateProposed},
	}}
	q.Sort()
	want := []State{StateProposed, StateProposed, StateRejected, StateInvalid}
	for i, w := range want {
		if q.Items[i].State != w {
			t.Fatalf("item %d: got %q, want %q", i, q.Items[i].State, w)
		}
	}
	if q.Items[0].Group != 1 || q.Items[1].Group != 4 {
		t.Fatalf("within a band, group order: got %d,%d want 1,4",
			q.Items[0].Group, q.Items[1].Group)
	}
}

// TestHeaderCounts pins total, per-author and invalid/superseded counts, which
// the header renders. Without Header the console could not tell a clean queue
// from one holding sets no decision can clear.
func TestHeaderCounts(t *testing.T) {
	q := Queue{Items: []Item{
		{File: "a.go", Author: 3, State: StateProposed},
		{File: "b.go", Author: 3, State: StateRejected},
		{File: "c.go", Author: 5, State: StateInvalid},
		{File: "d.go", Author: 5, State: StateSuperseded},
	}}
	h := q.Header()
	if h.Total != 4 || h.Invalid != 1 || h.Superseded != 1 {
		t.Fatalf("header counts: %+v", h)
	}
	if len(h.Authors) != 2 || h.Authors[0].Author != 3 || h.Authors[0].Count != 2 ||
		h.Authors[1].Author != 5 || h.Authors[1].Count != 2 {
		t.Fatalf("per-author: %+v", h.Authors)
	}
	line := h.Line()
	for _, want := range []string{"4 item(s)", "author 3: 2", "author 5: 2",
		"invalid: 1", "superseded: 1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("header line %q missing %q", line, want)
		}
	}
}

// TestItemFromSumsHunksAndExcerptsTheChange builds a diff whose new side is
// larger than the old and asserts the size, excerpt and body. Without itemFrom
// the queue would carry no added/removed split and no one-line excerpt, which
// is exactly the shape the spec's item requires.
func TestItemFromSumsHunksAndExcerptsTheChange(t *testing.T) {
	g := control.Group{ID: 12, Author: 3, State: "proposed", Bytes: 12}
	d := &control.DiffGroup{
		Group: g,
		Hunks: []control.DiffHunk{{
			Start: 10, End: 12, Line: 3, EndLine: 3,
			Old: "old\n", New: "new line\nsecond\n",
		}},
	}
	it := itemFrom("a.go", g, d)
	if it.Size != (Size{Added: 16, Removed: 4}) {
		t.Fatalf("size: %+v", it.Size)
	}
	if it.Size.String() != "+16 -4" {
		t.Fatalf("size string: %q", it.Size.String())
	}
	if it.Excerpt != "new line" {
		t.Fatalf("excerpt: %q", it.Excerpt)
	}
	if it.State != StateProposed || it.Group != 12 || it.Kind != KindEdit {
		t.Fatalf("identity: %+v", it)
	}
	body := strings.Join(it.Diff, "\n")
	if !strings.Contains(body, "-old") || !strings.Contains(body, "+new line") ||
		!strings.Contains(body, "@@ L3..L3") {
		t.Fatalf("diff body: %q", body)
	}
}

// TestItemFromRejectedFallsBackToNetBytes proves a set the server did not render
// (a rejected one) still carries a size and an excerpt, so it is not silently
// blank in the queue.
func TestItemFromRejectedFallsBackToNetBytes(t *testing.T) {
	g := control.Group{ID: 4, Author: 2, State: "rejected", Bytes: -7}
	it := itemFrom("b.go", g, nil)
	if it.State != StateRejected {
		t.Fatalf("state: %q", it.State)
	}
	if it.Size != (Size{Removed: 7}) {
		t.Fatalf("size: %+v", it.Size)
	}
	if it.Excerpt != "(rejected)" {
		t.Fatalf("excerpt: %q", it.Excerpt)
	}
}
