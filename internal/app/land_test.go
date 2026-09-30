package app

import (
	"context"
	"strings"
	"testing"

	"raj/internal/git"
	"raj/internal/intent"
	"raj/internal/piecetable"
)

// TestIntentLandCommitsOnBaseline is S3's core: a land exports the wave's
// reviewed composition as one commit on raj/baseline parented on the base head
// and records the row; a second land of the unchanged wave adds no commit.
func TestIntentLandCommitsOnBaseline(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	id := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package landed\n"})
	paneAt(t, h, "a.go").File.Session().SetGroupTask(id, "task-1")

	res := doIntent(t, h, intent.Command{Mode: "land", Task: "task-1", Base: "HEAD"})
	if res.Commit == "" || res.Tree == "" {
		t.Fatalf("land = %+v, want a commit and tree", res)
	}
	got, err := svc.RevParse(ctx, git.BaselineRef)
	if err != nil {
		t.Fatal(err)
	}
	if got != res.Commit {
		t.Errorf("baseline = %s, want the landed commit %s", got, res.Commit)
	}
	if res.Parent != res.BaseSHA {
		t.Errorf("parent = %s, want the base head %s", res.Parent, res.BaseSHA)
	}
	if rows, err := h.App.state.Exports("task-1"); err != nil || len(rows) != 1 {
		t.Fatalf("export rows = %d, %v; want 1", len(rows), err)
	}

	// A second land of the unchanged wave: the same commit, no new row, and
	// the baseline stays put.
	again := doIntent(t, h, intent.Command{Mode: "land", Task: "task-1", Base: "HEAD"})
	if again.Commit != res.Commit {
		t.Errorf("second land commit = %s, want the same %s", again.Commit, res.Commit)
	}
	if rows, _ := h.App.state.Exports("task-1"); len(rows) != 1 {
		t.Errorf("second land added an export row: %d", len(rows))
	}
	if got, _ := svc.RevParse(ctx, git.BaselineRef); got != res.Commit {
		t.Errorf("second land moved the baseline to %s", got)
	}
}

// TestIntentLandExcludesAcceptedNonWaveSet is the land twin of
// TestIntentDiffExcludesOtherAcceptedSeam: two accepted sets share a.go, only
// one belongs to the wave's task, and the wave's landed tree must hold the
// wave's change and not the other accepted set. Land returns a commit and a
// tree, not a diff, so the assertion is on the tree the export record and the
// baseline commit name -- that tree is the artifact publish reads and pushes.
func TestIntentLandExcludesAcceptedNonWaveSet(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	sess := paneAt(t, h, "a.go").File.Session()
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package aW\n"})
	gB := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package aW\n"), End: len("package aW\n"), Text: "// other wave\n"})
	sess.AcceptGroup(gA)
	sess.AcceptGroup(gB)
	sess.SetGroupTask(gA, "task-1")

	// The agreed text really does hold both accepted sets; otherwise the
	// fixture would not exercise the defect.
	if agreed := sess.Project(piecetable.AcceptedOnly).Text(); !strings.Contains(agreed, "other wave") || !strings.Contains(agreed, "package aW") {
		t.Fatalf("fixture: agreed text %q must hold both accepted sets", agreed)
	}

	res := doIntent(t, h, intent.Command{Mode: "land", Task: "task-1", Base: "HEAD"})
	if res.Tree == "" {
		t.Fatal("land returned no tree")
	}
	treeText, err := svc.Show(ctx, res.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if treeText != "package aW\n" {
		t.Errorf("landed a.go = %q, want only the wave's slice", treeText)
	}
	if strings.Contains(treeText, "other wave") {
		t.Errorf("landed a.go = %q holds an accepted set that is not in the wave", treeText)
	}

	// The recorded export names the same tree, and the baseline commit carries
	// it, so the assertion is on the artifact publish would push.
	rows, err := h.App.state.Exports("task-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("export rows = %d, %v; want 1", len(rows), err)
	}
	if rows[0].TreeSHA != res.Tree {
		t.Errorf("export record tree = %s, want the landed tree %s", rows[0].TreeSHA, res.Tree)
	}
	if baseline, _ := svc.RevParse(ctx, git.BaselineRef); baseline != res.Commit {
		t.Errorf("baseline = %s, want the landed commit %s", baseline, res.Commit)
	}
	committed, err := svc.Show(ctx, res.Commit, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if committed != treeText {
		t.Errorf("the landed commit's a.go = %q, want the materialised tree %q", committed, treeText)
	}
}
