package app

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"raj/internal/git"
	"raj/internal/intent"
	"raj/internal/piecetable"
)

// TestIntentNextExportsSeamSliceAndReportsBranchAndCommit is strand 3.5's key
// property: `intent next` builds the artifact for a seam -- the members alone,
// so another seam's accepted hunk in the same file stays out -- and reports the
// branch and the commit publish would push. It fails before the change because
// the subcommand does not exist, and it fails if the export starts from the
// agreed text instead of the base plus the seam's members.
func TestIntentNextExportsSeamSliceAndReportsBranchAndCommit(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	sess := paneAt(t, h, "a.go").File.Session()
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package aA\n"})
	gB := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package aA\n"), End: len("package aA\n"), Text: "// seam B\n"})
	sess.AcceptGroup(gA)
	sess.AcceptGroup(gB)
	doIntent(t, h, intent.Command{Mode: "new", Name: "A", Base: "HEAD", Groups: []uint64{gA}})
	doIntent(t, h, intent.Command{Mode: "new", Name: "B", Base: "HEAD", Groups: []uint64{gB}})
	// The agreed text really does hold both accepted seams; otherwise the
	// fixture would not exercise the leak.
	if agreed := sess.Project(piecetable.AcceptedOnly).Text(); !strings.Contains(agreed, "package aA") || !strings.Contains(agreed, "seam B") {
		t.Fatalf("fixture: agreed text %q must hold both accepted seams", agreed)
	}

	res := doIntent(t, h, intent.Command{Mode: "next", Name: "A", Title: "seam A", Body: "the why"})
	if res.Next == nil {
		t.Fatal("intent next returned no next")
	}
	if want := intent.BranchForWave("A"); res.Next.Branch != want {
		t.Errorf("reported branch = %q, want publish's own name %q", res.Next.Branch, want)
	}
	if res.Next.Commit == "" {
		t.Fatal("intent next reported no commit")
	}
	// The reported commit is the one a later push would carry: publish reads
	// LastExport, and next's export is the row it finds.
	rec, found, err := h.App.state.LastExport("A")
	if err != nil || !found {
		t.Fatalf("last export: found=%v err=%v", found, err)
	}
	if rec.CommitSHA != res.Next.Commit {
		t.Errorf("reported commit %s, want the exported row's %s", res.Next.Commit, rec.CommitSHA)
	}
	// The artifact holds the seam's slice alone: A's hunk, not B's accepted one.
	got, err := svc.Show(ctx, res.Next.Commit, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "package aA\n" {
		t.Errorf("artifact a.go = %q, want A's slice alone", got)
	}
	if strings.Contains(got, "seam B") {
		t.Errorf("artifact a.go = %q holds seam B's accepted change", got)
	}
	// The title and body flags reached the artifact commit.
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%B", res.Next.Commit).Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if msg := strings.TrimSpace(string(out)); msg != "seam A\n\nthe why" {
		t.Errorf("commit message = %q, want the title and body", msg)
	}
}

// TestIntentNextEmptySeamRefused pins the empty seam: nothing selected means
// nothing to export, so the call is refused by name and writes no export row.
// It fails before the change because the subcommand does not exist.
func TestIntentNextEmptySeamRefused(t *testing.T) {
	_, h := intentRoot(t)
	doIntent(t, h, intent.Command{Mode: "new", Name: "empty", Base: "HEAD"})

	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "next", Name: "empty"})
	if err == nil {
		t.Fatal("a seam with no members must be refused, not exported as an empty artifact")
	}
	if !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "member") {
		t.Errorf("refusal %q must name the intention and its empty membership", err)
	}
	if _, found, ferr := h.App.state.LastExport("empty"); ferr != nil || found {
		t.Fatalf("empty seam wrote an export (found=%v err=%v); nothing may be half-done", found, ferr)
	}
}

// TestIntentNextMissingBaseRefusedCleanly pins the other half: a base the ref
// namespace cannot resolve is refused with a named error before any commit or
// export row is written. "parent" is an intention, not a git ref, so a child
// based on it carries no pinned base SHA and resolving it against git fails.
// It fails before the change because the subcommand does not exist.
func TestIntentNextMissingBaseRefusedCleanly(t *testing.T) {
	_, h := intentRoot(t)
	g := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package g1\n"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "parent", Base: "HEAD"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "child", Base: "parent", Groups: []uint64{g}})

	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "next", Name: "child"})
	if err == nil {
		t.Fatal("a base ref that does not resolve must be refused, not exported")
	}
	if !strings.Contains(err.Error(), "child") || !strings.Contains(err.Error(), "parent") {
		t.Errorf("refusal %q must name the intention and its unresolvable base", err)
	}
	if _, found, ferr := h.App.state.LastExport("child"); ferr != nil || found {
		t.Fatalf("a missing base wrote an export (found=%v err=%v); nothing may be half-done", found, ferr)
	}
}
