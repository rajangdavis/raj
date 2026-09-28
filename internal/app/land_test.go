package app

import (
	"context"
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
