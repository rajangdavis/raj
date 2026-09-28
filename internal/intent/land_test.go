package intent

import (
	"context"
	"testing"
	"time"

	"raj/internal/git"
)

// TestLandMovesBaselineAndRecordsOneRow pins the core deliverable: one commit
// parented on the base ref head, raj/baseline moved to it, and one export row.
func TestLandMovesBaselineAndRecordsOneRow(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	st := newTestStore(t)
	ctx := context.Background()
	base := headSHA(t, svc)
	in := New("task-1", "owner", "HEAD", base, []Member{{ID: 1, Path: "a.go"}}, time.Unix(5, 0))

	res, err := Land(ctx, svc, st, in, base, Projection{"a.go": []byte("package landed\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped {
		t.Fatal("first land reported skipped")
	}
	if res.Commit == "" || res.Tree == "" {
		t.Fatalf("land result = %+v, want a commit and tree", res)
	}
	if res.Parent != base {
		t.Errorf("parent = %s, want the base %s", res.Parent, base)
	}
	if got, err := svc.RevParse(ctx, git.BaselineRef); err != nil || got != res.Commit {
		t.Fatalf("baseline = %q, %v; want %q", got, err, res.Commit)
	}
	rows, err := st.Exports("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("export rows = %d, want 1", len(rows))
	}
	if rows[0].CommitSHA != res.Commit || rows[0].BaseSHA != base || rows[0].TreeSHA != res.Tree {
		t.Errorf("export row = %+v, want the committed objects", rows[0])
	}

	// A second land of the unchanged wave is a no-op: same commit, no new row,
	// baseline unmoved.
	again, err := Land(ctx, svc, st, in, base, Projection{"a.go": []byte("package landed\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Skipped || again.Commit != res.Commit {
		t.Errorf("second land = %+v, want a skip of %s", again, res.Commit)
	}
	if rows2, _ := st.Exports("task-1"); len(rows2) != 1 {
		t.Errorf("second land added an export row: %d", len(rows2))
	}
	if got, _ := svc.RevParse(ctx, git.BaselineRef); got != res.Commit {
		t.Errorf("second land moved the baseline to %s", got)
	}

	// New work under the same wave name exports a sibling on the same base and
	// moves the ref: one commit per wave, no chain to the first export.
	next, err := Land(ctx, svc, st, in, base, Projection{"a.go": []byte("package landed more\n")})
	if err != nil {
		t.Fatal(err)
	}
	if next.Skipped || next.Commit == res.Commit {
		t.Fatalf("changed land = %+v, want a new commit", next)
	}
	if next.Parent != base {
		t.Errorf("sibling parent = %s, want the base %s (no chain)", next.Parent, base)
	}
	if rows3, _ := st.Exports("task-1"); len(rows3) != 2 {
		t.Errorf("changed land export rows = %d, want 2", len(rows3))
	}
}

// TestLandLeavesHeadIndexAndWorktree proves the ref move does not disturb the
// user: HEAD stays and the worktree and index stay clean.
func TestLandLeavesHeadIndexAndWorktree(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	st := newTestStore(t)
	ctx := context.Background()
	base := headSHA(t, svc)
	in := New("task-2", "owner", "HEAD", base, nil, time.Unix(6, 0))
	if _, err := Land(ctx, svc, st, in, base, Projection{"a.go": []byte("package changed\n")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.RevParse(ctx, "HEAD"); got != base {
		t.Errorf("HEAD moved to %s, want %s", got, base)
	}
	if out := runGit(t, dir, "status", "--porcelain"); out != "" {
		t.Errorf("land disturbed the worktree or index:\n%s", out)
	}
}
