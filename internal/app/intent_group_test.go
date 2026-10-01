package app

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"raj/internal/intent"
	"raj/internal/piecetable"
)

// taskGroupFixture proposes one set on each of a.go and b.go under one task and
// files both under it. The two buffers number their first set the same
// (intentRootColliding), so only the path-qualified member can tell them apart:
// with bare ids one buffer's set would be silently dropped.
func taskGroupFixture(t *testing.T, task string) (string, *harness, uint64, uint64) {
	t.Helper()
	dir, h := intentRootColliding(t)
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package a2\n"})
	gB := proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package b2\n"})
	paneAt(t, h, "a.go").File.Session().SetGroupTask(gA, task)
	paneAt(t, h, "b.go").File.Session().SetGroupTask(gB, task)
	return dir, h, gA, gB
}

// One command turns a task's sets on two buffers into one seam whose members
// are all present and path-qualified. It fails before the change because the
// subcommand does not exist, and it fails if the members are not qualified --
// the two buffers number their sets the same, so a bare-id membership would
// collapse them into one and drop a buffer.
func TestIntentGroupTakesATasksSetsAcrossBuffers(t *testing.T) {
	_, h, gA, gB := taskGroupFixture(t, "T")
	if gA != gB {
		t.Fatalf("fixture: a.go numbered %d and b.go %d; the ids must collide", gA, gB)
	}
	res := doIntent(t, h, intent.Command{Mode: "group", Name: "seam-x", Task: "T", Base: "HEAD"})
	if res.Group == nil {
		t.Fatal("intent group returned no report")
	}
	g := res.Group
	if !g.Created || !g.Changed || len(g.Added) != 2 {
		t.Errorf("created=%v changed=%v added=%+v, want a new seam with the two sets", g.Created, g.Changed, g.Added)
	}
	paths := map[string]uint64{}
	for _, m := range g.Members {
		if m.Path == "" {
			t.Fatalf("member %+v is not path-qualified", m)
		}
		paths[m.Path] = m.ID
	}
	if len(paths) != 2 || paths["a.go"] != gA || paths["b.go"] != gB {
		t.Fatalf("members = %+v, want a.go=%d and b.go=%d", g.Members, gA, gB)
	}
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "seam-x"})
	if show.Intention == nil {
		t.Fatal("seam-x was not stored")
	}
	if show.Intention.Task != "T" || show.Intention.Base != "HEAD" {
		t.Errorf("stored seam task=%q base=%q, want T over HEAD", show.Intention.Task, show.Intention.Base)
	}
	if !reflect.DeepEqual(show.Intention.Members, g.Members) {
		t.Errorf("stored members %+v, want the reported %+v", show.Intention.Members, g.Members)
	}
}

// A second run with the same task and name adds nothing, reports the same
// members, and leaves the stored membership unchanged. It fails before the
// change because the subcommand does not exist, and it fails if grouping is not
// idempotent (a duplicate member, or a created-again seam).
func TestIntentGroupIsIdempotent(t *testing.T) {
	_, h, _, _ := taskGroupFixture(t, "T")
	first := doIntent(t, h, intent.Command{Mode: "group", Name: "seam-x", Task: "T", Base: "HEAD"})
	second := doIntent(t, h, intent.Command{Mode: "group", Name: "seam-x", Task: "T", Base: "HEAD"})
	if second.Group == nil {
		t.Fatal("the re-run returned no report")
	}
	if second.Group.Created || second.Group.Changed || len(second.Group.Added) != 0 {
		t.Errorf("re-run created=%v changed=%v added=%+v, want a no-op",
			second.Group.Created, second.Group.Changed, second.Group.Added)
	}
	if !reflect.DeepEqual(first.Group.Members, second.Group.Members) {
		t.Errorf("re-run members %+v, want the first run's %+v", second.Group.Members, first.Group.Members)
	}
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "seam-x"})
	if show.Intention == nil || len(show.Intention.Members) != 2 {
		t.Fatalf("stored members = %+v, want exactly two", show.Intention)
	}
}

// A task set another seam already names is reported by name and not moved. It
// fails before the change because the subcommand does not exist, and it fails
// if the set is silently taken (the other seam loses its member) or silently
// dropped with no reason.
func TestIntentGroupReportsASetInAnotherSeam(t *testing.T) {
	_, h, gA, _ := taskGroupFixture(t, "T")
	doIntent(t, h, intent.Command{Mode: "new", Name: "other", Base: "HEAD",
		Members: []intent.Member{{ID: gA, Path: "a.go"}}})
	res := doIntent(t, h, intent.Command{Mode: "group", Name: "mine", Task: "T", Base: "HEAD"})
	if res.Group == nil {
		t.Fatal("no report")
	}
	if len(res.Group.Added) != 1 || res.Group.Added[0].Path != "b.go" {
		t.Errorf("added = %+v, want only b.go", res.Group.Added)
	}
	if len(res.Group.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want the a.go set", res.Group.Skipped)
	}
	skip := res.Group.Skipped[0]
	if skip.Member.Path != "a.go" || skip.Member.ID != gA || skip.Reason != intent.SkipOtherSeam || skip.Seam != "other" {
		t.Errorf("skip = %+v, want a.go=%d reported as held by other", skip, gA)
	}
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "other"})
	if show.Intention == nil || len(show.Intention.Members) != 1 || show.Intention.Members[0].Path != "a.go" {
		t.Errorf("other's members = %+v, want its a.go set untouched", show.Intention)
	}
}

// A task with no sets is refused by name and writes no seam. It fails before
// the change because the subcommand does not exist, and it fails if the refusal
// is anonymous or an empty seam is stored anyway.
func TestIntentGroupRefusesATaskWithNoSets(t *testing.T) {
	_, h, _, _ := taskGroupFixture(t, "T")
	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "group", Name: "seam-x", Task: "ghost", Base: "HEAD"})
	if err == nil {
		t.Fatal("a task with no sets must be refused")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("refusal %q must name the task", err)
	}
	list := doIntent(t, h, intent.Command{Mode: "list"})
	for _, in := range list.Intentions {
		if in.Name == "seam-x" {
			t.Errorf("a refused grouping created %q", in.Name)
		}
	}
}

// Two seams that touch one file are reported as an overlap and never stacked.
// It fails before the change because the subcommand does not exist, and it
// fails if the second seam's base is silently pointed at the first (a guessed
// stack) instead of the git base.
func TestIntentGroupReportsOverlapBetweenSeams(t *testing.T) {
	_, h, _, _ := taskGroupFixture(t, "A")
	// A second task proposes another set on a.go, the file task A also touches.
	a := paneAt(t, h, "a.go")
	gB := proposeOn(t, h, "a.go", piecetable.Hunk{Start: a.File.Len(), End: a.File.Len(), Text: "// task B\n"})
	a.File.Session().SetGroupTask(gB, "B")

	doIntent(t, h, intent.Command{Mode: "group", Name: "one", Task: "A", Base: "HEAD"})
	res := doIntent(t, h, intent.Command{Mode: "group", Name: "two", Task: "B", Base: "HEAD"})
	if res.Group == nil {
		t.Fatal("no report")
	}
	if len(res.Group.Overlaps) != 1 {
		t.Fatalf("overlaps = %+v, want a.go shared with one", res.Group.Overlaps)
	}
	if o := res.Group.Overlaps[0]; o.Path != "a.go" || o.Seam != "one" {
		t.Errorf("overlap = %+v, want a.go shared with one", o)
	}
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "two"})
	if show.Intention == nil || show.Intention.Base != "HEAD" {
		t.Errorf("two's base = %+v, want HEAD: an overlap is reported, never stacked", show.Intention)
	}
}

// A dry run reports the members it would add and writes nothing. It fails
// before the change because the subcommand does not exist, and it fails if
// --dry-run still records the seam.
func TestIntentGroupDryRunWritesNothing(t *testing.T) {
	_, h, _, _ := taskGroupFixture(t, "T")
	res := doIntent(t, h, intent.Command{Mode: "group", Name: "seam-x", Task: "T", Base: "HEAD", DryRun: true})
	if res.Group == nil || !res.Group.DryRun {
		t.Fatalf("dry-run report = %+v, want a dry-run report", res.Group)
	}
	if len(res.Group.Added) != 2 {
		t.Errorf("dry-run added = %+v, want the two sets it would add", res.Group.Added)
	}
	list := doIntent(t, h, intent.Command{Mode: "list"})
	for _, in := range list.Intentions {
		if in.Name == "seam-x" {
			t.Errorf("dry run wrote %q", in.Name)
		}
	}
}

// Scope is pending (Proposed) sets: an accepted set is a decision already made,
// so it is reported as not-pending and left where it is. It fails before the
// change because the subcommand does not exist, and it fails if the scope is
// widened silently.
func TestIntentGroupSkipsANonPendingSet(t *testing.T) {
	_, h, gA, _ := taskGroupFixture(t, "T")
	paneAt(t, h, "a.go").File.Session().AcceptGroup(gA)
	res := doIntent(t, h, intent.Command{Mode: "group", Name: "seam-x", Task: "T", Base: "HEAD"})
	if res.Group == nil {
		t.Fatal("no report")
	}
	if len(res.Group.Added) != 1 || res.Group.Added[0].Path != "b.go" {
		t.Errorf("added = %+v, want only the pending b.go set", res.Group.Added)
	}
	if len(res.Group.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want the accepted a.go set", res.Group.Skipped)
	}
	if s := res.Group.Skipped[0]; s.Member.Path != "a.go" || s.Reason != intent.SkipNotPending || s.State != "accepted" {
		t.Errorf("skip = %+v, want a.go reported as not-pending (accepted)", s)
	}
}

// With no --ref the base is the checked-out branch, never a hardcoded trunk
// name (Q9). It fails before the change because the subcommand does not exist,
// and it fails if the base is left empty or guessed as main.
func TestIntentGroupAutoDetectsABaseRef(t *testing.T) {
	dir, h, _, _ := taskGroupFixture(t, "T")
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref: %v", err)
	}
	want := strings.TrimSpace(string(out))
	doIntent(t, h, intent.Command{Mode: "group", Name: "seam-auto", Task: "T"})
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "seam-auto"})
	if show.Intention == nil {
		t.Fatal("auto-detected seam was not stored")
	}
	if show.Intention.Base != want || show.Intention.BaseSHA == "" {
		t.Errorf("base = %q sha %q, want the checked-out branch %q", show.Intention.Base, show.Intention.BaseSHA, want)
	}
}
