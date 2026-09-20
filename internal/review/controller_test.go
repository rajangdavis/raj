package review

import (
	"errors"
	"fmt"
	"testing"

	"raj/internal/control"
)

// fakeTransport serves a scripted workspace and records the decisions sent to
// it. Groups returns a copy, so a test can mutate the backing slice between
// Fetch and Decide to model another writer moving the set.
type fakeTransport struct {
	buffers  []control.Buffer
	props    []control.Proposal
	groups   map[string][]control.Group
	diffs    map[string][]control.DiffGroup
	accepts  []string
	rejects  []string
	clears   []string
	groupErr error
}

func (f *fakeTransport) Proposals() ([]control.Proposal, error) { return f.props, nil }

func (f *fakeTransport) Buffers() ([]control.Buffer, error) { return f.buffers, nil }

func (f *fakeTransport) Groups(path string) ([]control.Group, error) {
	if f.groupErr != nil {
		return nil, f.groupErr
	}
	return append([]control.Group(nil), f.groups[path]...), nil
}

func (f *fakeTransport) Diff(path string) ([]control.DiffGroup, error) {
	return f.diffs[path], nil
}

func (f *fakeTransport) Accept(path string, group uint64) error {
	f.accepts = append(f.accepts, fmt.Sprintf("%s#%d", path, group))
	return nil
}

func (f *fakeTransport) Reject(path string, group uint64) error {
	f.rejects = append(f.rejects, fmt.Sprintf("%s#%d", path, group))
	return nil
}

func (f *fakeTransport) Clear(path string, group uint64) error {
	f.clears = append(f.clears, fmt.Sprintf("%s#%d", path, group))
	return nil
}

func (f *fakeTransport) Author() uint8 { return 7 }
func (f *fakeTransport) Close() error  { return nil }

// TestFetcherBuildsQueueAndKeepsRejectedAfterProposalsDropsIt is the whole
// ingest path: a proposed set and a rejected set are both listed with state and
// excerpt, and after the proposed set is accepted `proposals` stops naming the
// file while the fetcher's remembered path still reaches `groups`, so the
// rejected set does not silently vanish.
func TestFetcherBuildsQueueAndKeepsRejectedAfterProposalsDropsIt(t *testing.T) {
	f := &fakeTransport{
		props: []control.Proposal{
			{Kind: "set", Path: "a.go", Author: 3, Group: 1},
			{Kind: "delete", Path: "gone.go", Author: 3},
		},
		groups: map[string][]control.Group{
			"a.go": {
				{ID: 1, Author: 3, State: "proposed", Bytes: 4},
				{ID: 2, Author: 3, State: "rejected"},
			},
		},
		diffs: map[string][]control.DiffGroup{
			"a.go": {{Group: control.Group{ID: 1, Author: 3, State: "proposed"},
				Hunks: []control.DiffHunk{{Old: "old\n", New: "new\n"}}}},
		},
	}
	fet := NewFetcher(f)
	q, err := fet.Fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 3 {
		t.Fatalf("items: %+v", q.Items)
	}
	if q.Items[0].State != StateProposed || q.Items[0].File != "a.go" || q.Items[0].Excerpt != "new" {
		t.Fatalf("proposed item: %+v", q.Items[0])
	}
	if q.Items[1].Kind != KindDelete || q.Items[1].File != "gone.go" {
		t.Fatalf("delete item: %+v", q.Items[1])
	}
	if q.Items[2].State != StateRejected {
		t.Fatalf("rejected item: %+v", q.Items[2])
	}

	// The proposed set is accepted; proposals no longer names a.go. The
	// remembered path still reaches groups, which now lists the set accepted
	// and the rejected set still rejected.
	f.groups["a.go"] = []control.Group{
		{ID: 1, Author: 3, State: "accepted"},
		{ID: 2, Author: 3, State: "rejected"},
	}
	f.props = []control.Proposal{{Kind: "delete", Path: "gone.go", Author: 3}}
	q2, err := fet.Fetch()
	if err != nil {
		t.Fatal(err)
	}
	var rejected, proposed int
	for _, it := range q2.Items {
		switch {
		case it.File == "a.go" && it.Group == 2 && it.State == StateRejected:
			rejected++
		case it.State == StateProposed && it.Kind == KindEdit:
			proposed++
		}
	}
	if rejected != 1 {
		t.Fatalf("rejected set dropped after proposals stopped listing it: %+v", q2.Items)
	}
	if proposed != 0 {
		t.Fatalf("accepted set still in the queue: %+v", q2.Items)
	}
}

// TestDecideRefusesDriftBeforeSending pins the safety rule: if the target's
// state changed since the screen was drawn, Decide returns a DriftError and
// sends nothing. Without the pre-check the console would accept a set it had
// rendered as proposed after the host had already invalidated it.
func TestDecideRefusesDriftBeforeSending(t *testing.T) {
	f := &fakeTransport{groups: map[string][]control.Group{
		"a.go": {{ID: 1, Author: 3, State: "proposed", Invalid: true}},
	}}
	c := NewController(f)
	item := Item{File: "a.go", Group: 1, Kind: KindEdit, State: StateProposed}
	err := c.Decide(Accept, item)
	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("got %v, want DriftError", err)
	}
	if drift.Got != StateInvalid {
		t.Fatalf("drift got state %q, want %q", drift.Got, StateInvalid)
	}
	if len(f.accepts) != 0 {
		t.Fatalf("accept was sent despite drift: %v", f.accepts)
	}
}

// TestDecideReportsAGoneSet covers the other drift form: the set is no longer
// in the buffer at all.
func TestDecideReportsAGoneSet(t *testing.T) {
	f := &fakeTransport{groups: map[string][]control.Group{"a.go": {}}}
	c := NewController(f)
	err := c.Decide(Reject, Item{File: "a.go", Group: 9, Kind: KindEdit, State: StateProposed})
	var drift *DriftError
	if !errors.As(err, &drift) || !drift.Gone {
		t.Fatalf("got %v, want a gone DriftError", err)
	}
	if len(f.rejects) != 0 {
		t.Fatalf("reject was sent despite a gone set: %v", f.rejects)
	}
}

// TestDecideRefusesAChangedBody covers a target whose state is unchanged but
// whose rendered hunks moved. That is the version half of the spec's "version
// or state": the screen's diff no longer describes the set, so it re-renders
// rather than deciding on stale text.
func TestDecideRefusesAChangedBody(t *testing.T) {
	f := &fakeTransport{
		groups: map[string][]control.Group{
			"a.go": {{ID: 1, Author: 3, State: "proposed"}},
		},
		diffs: map[string][]control.DiffGroup{
			"a.go": {{Group: control.Group{ID: 1, Author: 3, State: "proposed"},
				Hunks: []control.DiffHunk{{Old: "old\n", New: "moved\n"}}}},
		},
	}
	c := NewController(f)
	item := Item{File: "a.go", Group: 1, Kind: KindEdit, State: StateProposed,
		Size: Size{Added: 6, Removed: 4}, Diff: []string{"@@ L1..L1 (bytes 0..4) @@", "-old", "+was"}}
	err := c.Decide(Accept, item)
	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("got %v, want DriftError", err)
	}
	if drift.Detail == "" {
		t.Fatalf("drift detail missing: %+v", drift)
	}
	if len(f.accepts) != 0 {
		t.Fatalf("accept was sent despite a moved body: %v", f.accepts)
	}
}

// TestDecideSendsTheActionWhenTheSetStands checks the success path: a proposed
// set still proposed reaches the transport's accept.
func TestDecideSendsTheActionWhenTheSetStands(t *testing.T) {
	f := &fakeTransport{groups: map[string][]control.Group{
		"a.go": {{ID: 1, Author: 3, State: "proposed"}},
	}}
	c := NewController(f)
	if err := c.Decide(Accept, Item{File: "a.go", Group: 1, Kind: KindEdit, State: StateProposed}); err != nil {
		t.Fatal(err)
	}
	if len(f.accepts) != 1 || f.accepts[0] != "a.go#1" {
		t.Fatalf("accepts: %v", f.accepts)
	}
}

// TestDecideRefusesADeletion names the deliberate hole: a file deletion has no
// change set, so a/r/c cannot decide it. It is refused by name rather than
// silently accepted.
func TestDecideRefusesADeletion(t *testing.T) {
	f := &fakeTransport{}
	c := NewController(f)
	if err := c.Decide(Accept, Item{File: "gone.go", Kind: KindDelete, State: StateProposed}); err == nil {
		t.Fatal("want a refusal for a deletion")
	}
	if len(f.accepts) != 0 {
		t.Fatalf("a deletion decision reached the transport: %v", f.accepts)
	}
}

// TestAcceptAllFiltersByAuthor accepts only the current author's sets and
// leaves another author's alone. Without the filter A would accept the whole
// queue, which is not what the key promises.
func TestAcceptAllFiltersByAuthor(t *testing.T) {
	f := &fakeTransport{groups: map[string][]control.Group{
		"a.go": {
			{ID: 1, Author: 3, State: "proposed"},
			{ID: 2, Author: 3, State: "proposed"},
			{ID: 3, Author: 5, State: "proposed"},
		},
	}}
	c := NewController(f)
	c.Queue = Queue{Items: []Item{
		{File: "a.go", Group: 1, Author: 3, Kind: KindEdit, State: StateProposed},
		{File: "a.go", Group: 2, Author: 3, Kind: KindEdit, State: StateProposed},
		{File: "a.go", Group: 3, Author: 5, Kind: KindEdit, State: StateProposed},
	}}
	n, err := c.AcceptAll(func(it Item) bool { return it.Author == 3 })
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(f.accepts) != 2 {
		t.Fatalf("n=%d accepts=%v", n, f.accepts)
	}
	for _, a := range f.accepts {
		if a == "a.go#3" {
			t.Fatalf("accepted another author's set: %v", f.accepts)
		}
	}
}

// TestFetcherSweepsDirtyBuffersForRejectedAndInvalidSets covers the cold start
// the workspace `proposals` listing cannot: a set a later edit invalidated is
// not pending, but its text keeps the buffer dirty, so the sweep still reaches
// it. Without the dirty-buffer sweep the invalid set would be invisible rather
// than shown and left undecided.
func TestFetcherSweepsDirtyBuffersForRejectedAndInvalidSets(t *testing.T) {
	f := &fakeTransport{
		buffers: []control.Buffer{
			{Path: "dirty.go", Dirty: true},
			{Path: "clean.go"},
		},
		groups: map[string][]control.Group{
			"dirty.go": {
				{ID: 7, Author: 3, State: "proposed", Invalid: true},
				{ID: 8, Author: 3, State: "rejected"},
			},
		},
	}
	q, err := NewFetcher(f).Fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 2 {
		t.Fatalf("items: %+v", q.Items)
	}
	var invalid, rejected int
	for _, it := range q.Items {
		switch it.State {
		case StateInvalid:
			invalid++
		case StateRejected:
			rejected++
		}
	}
	if invalid != 1 || rejected != 1 {
		t.Fatalf("invalid=%d rejected=%d queue=%+v", invalid, rejected, q.Items)
	}
	for _, it := range q.Items {
		if it.File == "clean.go" {
			t.Fatalf("a clean buffer with no proposal was swept: %+v", q.Items)
		}
	}
}
