package app

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"raj/internal/control"
	"raj/internal/piecetable"
)

// The projection fixtures below drive the real app harness: each opens (or
// creates) a file, lands a change set through File.ApplyDiff, and marks it
// Proposed with session state, exactly the shape host.Buffers reports. The
// fixture text and offsets come from review_test.go's single scenario, so the
// two files cannot drift.

// projectKeys returns a map's keys sorted, so an assertion can compare the
// exact key set without depending on Go's map iteration order.
func projectKeys(m map[string][]byte) []string {
	return slices.Sorted(maps.Keys(m))
}

// Precondition: the harness opens test.go holding "hello world\n" (clean), and
// the proposal replaces "world" with "socket", leaving the change set Proposed.
// The view then differs from disk while the agreed composition does not.
//
// Without the change there is no App.Project at all; a Project that keyed on
// Dirty instead of ViewDirty would omit this proposed-only buffer, so the
// presence check fails and the key set is empty.
func TestProjectIncludesProposedRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	got := h.Project(piecetable.AcceptedAndProposed)
	path := h.Pane().File.Path
	if _, ok := got[path]; !ok {
		t.Fatalf("Project(AcceptedAndProposed) omitted the view-dirty %s; keys = %v", path, projectKeys(got))
	}
	if want := "hello socket\n"; string(got[path]) != want {
		t.Errorf("Project(AcceptedAndProposed)[%s] = %q, want %q", path, got[path], want)
	}
}

// Precondition: the same proposed-only fixture as the test above — the view is
// dirty, the accepted composition is still the disk text "hello world\n".
//
// AcceptedOnly drops the proposed run but ViewDirty still admits the buffer, so
// the path is present and holds the disk text. Without the change there is no
// App.Project; a Project that returned the session view for every policy would
// return "hello socket\n" and fail the text assertion.
func TestProjectAcceptedOnlyExcludesProposedRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	got := h.Project(piecetable.AcceptedOnly)
	path := h.Pane().File.Path
	if _, ok := got[path]; !ok {
		t.Fatalf("Project(AcceptedOnly) omitted the view-dirty %s; keys = %v", path, projectKeys(got))
	}
	if want := reviewFixture; string(got[path]) != want {
		t.Errorf("Project(AcceptedOnly)[%s] = %q, want the disk text %q", path, got[path], want)
	}
}

// Precondition: the harness opens test.go holding "base\n" and touches nothing,
// so the pane is clean (ViewDirty false) and its agreed composition equals disk.
//
// Without the change there is no App.Project; a Project that listed every open
// buffer regardless of ViewDirty would return this clean pane and the empty
// assertion fails.
func TestProjectSkipsCleanBuffer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "base\n")
	if h.Pane().File.ViewDirty() {
		t.Fatal("fixture: a freshly opened buffer must be clean")
	}
	got := h.Project(piecetable.AcceptedAndProposed)
	if len(got) != 0 {
		t.Fatalf("Project listed clean buffers: %v", projectKeys(got))
	}
}

// Precondition: test.go is opened dirty by a proposal, then a second file
// clean.go is created on disk and opened clean; exactly one open buffer differs
// from disk.
//
// The key set must be exactly test.go. Without the change there is no
// App.Project; omitting the clean file or including it (a Dirty/always-include
// predicate) fails the exact-key assertion either way.
func TestProjectKeysAreExactlyTheDirtyPaths(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	dirty := h.Pane().File.Path
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	clean := filepath.Join(h.primaryRoot(), "clean.go")
	if err := os.WriteFile(clean, []byte("package clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(clean)
	if got := h.Pane().File.Path; got != clean {
		t.Fatalf("fixture: the clean file is not active; on %s", got)
	}
	if h.Pane().File.ViewDirty() {
		t.Fatal("fixture: clean.go must be clean")
	}

	got := h.Project(piecetable.AcceptedAndProposed)
	if want := []string{dirty}; !slices.Equal(projectKeys(got), want) {
		t.Errorf("Project keys = %v, want exactly %v", projectKeys(got), want)
	}
}

// TestHostProjectionMapsPolicy pins the mapping the app host owns: the control
// policy selects the piecetable composition. Precondition: the harness opens
// test.go holding reviewFixture and a proposal replaces "world" with "socket",
// so the pane is view-dirty while the accepted composition still equals disk.
// Without the mapping method this does not compile; a host that ignored the
// policy or swapped the two arms would return the wrong composition and
// disagree with App.Project for one of the two policies.
func TestHostProjectionMapsPolicy(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	host := hostOf(h.App)
	if got, want := host.Projection(control.ProjectionWithProposed), h.Project(piecetable.AcceptedAndProposed); !reflect.DeepEqual(got, want) {
		t.Errorf("Projection(ProjectionWithProposed) = %v, want App.Project(AcceptedAndProposed) = %v", got, want)
	}
	if got, want := host.Projection(control.ProjectionAccepted), h.Project(piecetable.AcceptedOnly); !reflect.DeepEqual(got, want) {
		t.Errorf("Projection(ProjectionAccepted) = %v, want App.Project(AcceptedOnly) = %v", got, want)
	}
}

// A change set that only deletes text must reach the tree a verification
// surface materialises like any other. The deleted bytes are live in the
// session, so the verification composition drops them; the hook projected
// check reads this same map, so an omission here would make a deletion-only
// proposal invisible to `check`. The edit-mode composition still defers the
// deletion so the human sees the bytes before deciding, and AcceptedOnly stays
// the unagreed disk text: the three policies are deliberately different.
//
// Precondition: test.go holds reviewFixture and one proposed hunk deletes
// "world". A verification composition that kept the deleted run would return
// "hello world\n" and fail the text assertion; the two other halves pin the
// deferral and the agreement.
func TestProjectVerificationAppliesProposedDeletion(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: ""})

	path := h.Pane().File.Path
	got := h.Project(piecetable.AcceptedAndProposedApplied)
	if _, ok := got[path]; !ok {
		t.Fatalf("Project(AcceptedAndProposedApplied) omitted the view-dirty deletion %s; keys = %v", path, projectKeys(got))
	}
	if want := "hello \n"; string(got[path]) != want {
		t.Errorf("Project(AcceptedAndProposedApplied)[%s] = %q, want %q (the deletion applied)", path, got[path], want)
	}
	if got := h.Project(piecetable.AcceptedAndProposed); string(got[path]) != reviewFixture {
		t.Errorf("Project(AcceptedAndProposed)[%s] = %q, want the deferred disk text %q", path, got[path], reviewFixture)
	}
	if got := h.Project(piecetable.AcceptedOnly); string(got[path]) != reviewFixture {
		t.Errorf("Project(AcceptedOnly)[%s] = %q, want the unagreed disk text %q", path, got[path], reviewFixture)
	}

	host := hostOf(h.App)
	if got := host.Projection(control.ProjectionVerifying); string(got[path]) != "hello \n" {
		t.Errorf("Projection(ProjectionVerifying)[%s] = %q, want the applied deletion %q", path, got[path], "hello \n")
	}
	if got := host.Projection(control.ProjectionWithProposed); string(got[path]) != reviewFixture {
		t.Errorf("Projection(ProjectionWithProposed)[%s] = %q, want the deferred disk text %q", path, got[path], reviewFixture)
	}
}
