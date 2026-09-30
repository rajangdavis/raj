package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/intent"
	"raj/internal/piecetable"
	"raj/internal/store"
)

// publishRoot is the intentRoot workspace plus a real S3 export for task-1: a
// member edit on a.go is landed as one commit on raj/baseline and recorded in
// the store. Publish pins that export — the commit, its parent, its tree and
// the row's id. The pinned hook script and the origin remote are present so
// propose's dry run and accept's pin re-check find them.
func publishRoot(t *testing.T) (string, *harness, store.ExportRecord) {
	t.Helper()
	dir, h := intentRoot(t)
	ctx := context.Background()
	g := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package a2\n"})
	paneAt(t, h, "a.go").File.Session().SetGroupTask(g, "task-1")
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "land", Name: "task-1"}); err != nil {
		t.Fatalf("land: %v", err)
	}
	rec, found, err := h.App.state.LastExport("task-1")
	if err != nil || !found {
		t.Fatalf("last export: found=%v err=%v", found, err)
	}
	runGitDir(t, dir, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	hookDir := filepath.Join(dir, "examples", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "publish-single.sh"),
		[]byte("#!/bin/sh\n# pinned publish hook\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, h, rec
}

func runGitDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@localhost",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@localhost")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestPublishProposePinsAndLists is the propose half: the proposal pins the
// export commit, its parent as the base, the export tree and row id, the
// branch, remote URL, hook hash and argv (--commit and --base, no --title), and
// rides the unified `proposals` surface.
func TestPublishProposePinsAndLists(t *testing.T) {
	_, h, rec := publishRoot(t)
	ctx := context.Background()
	var ran []string
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		ran = append(ran, strings.Join(argv, " "))
		return "publish-single: git push --dry-run origin " + rec.CommitSHA + ":refs/heads/raj/wave-task-1:\n*\trefs/heads/raj/wave-task-1\t[new branch]\n", "", 0, nil
	}
	res, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	p := res.Publish
	if p == nil {
		t.Fatal("no publish in the result")
	}
	if p.Commit != rec.CommitSHA || p.BaseSHA != rec.BaseSHA || p.BaseRef != rec.BaseSHA {
		t.Errorf("pins = commit %s base %s ref %s, want %s/%s", p.Commit, p.BaseSHA, p.BaseRef, rec.CommitSHA, rec.BaseSHA)
	}
	if p.ExportTree != rec.TreeSHA || p.ExportID != rec.ID {
		t.Errorf("export pins = tree %s row %d, want %s/%d", p.ExportTree, p.ExportID, rec.TreeSHA, rec.ID)
	}
	if p.Branch != "raj/wave-task-1" {
		t.Errorf("branch = %q", p.Branch)
	}
	if p.Remote != "origin" || p.RemoteURL != "https://github.com/acme/widgets.git" {
		t.Errorf("remote = %q %q", p.Remote, p.RemoteURL)
	}
	if p.HookHash == "" || len(p.Argv) == 0 {
		t.Errorf("hook not pinned: hash=%q argv=%v", p.HookHash, p.Argv)
	}
	argv := strings.Join(p.Argv, " ")
	for _, want := range []string{"--commit " + rec.CommitSHA, "--base " + rec.BaseSHA} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q missing %q", argv, want)
		}
	}
	if strings.Contains(argv, "--title") {
		t.Errorf("argv still carries --title: %q", argv)
	}
	if !strings.Contains(p.DryRun, "git push --dry-run") {
		t.Errorf("dry-run output not carried inline: %q", p.DryRun)
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "--dry-run") {
		t.Errorf("hook invocations = %v, want one --dry-run", ran)
	}
	var listed bool
	for _, pr := range h.App.Proposals() {
		if pr.Kind == "publish" && pr.Path == "task-1" && pr.Author == 7 {
			listed = true
		}
	}
	if !listed {
		t.Error("publish proposal not on the proposals surface")
	}
}

// TestPublishAcceptRefusesAfterSecondExport is the export-row pin: a later
// export of the same wave replaces the row, so the proposal is stale, the hook
// does not run, and the proposal is not left pending.
func TestPublishAcceptRefusesAfterSecondExport(t *testing.T) {
	_, h, _ := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	// The wave gains a member and lands again, so LastExport is a new row.
	g2 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package a2\n"), End: len("package a2\n"), Text: "// second\n"})
	paneAt(t, h, "a.go").File.Session().SetGroupTask(g2, "task-1")
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "land", Task: "task-1"}); err != nil {
		t.Fatalf("second land: %v", err)
	}
	calls := 0
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	}
	_, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err == nil || !strings.Contains(err.Error(), "re-propose") {
		t.Fatalf("accept after a second export = %v, want a re-propose refusal", err)
	}
	if calls != 0 {
		t.Errorf("the hook ran %d time(s) on a stale export, want 0", calls)
	}
	if _, ok := h.App.pendingPublishes["task-1"]; ok {
		t.Error("a stale proposal is still pending; it must be re-proposed")
	}
}

// TestPublishAcceptRefusesSeamChange is the seam pin: a member edit that
// changes what the current buffers would export is refused, even with no second
// export, because the artifact is immutable.
func TestPublishAcceptRefusesSeamChange(t *testing.T) {
	_, h, _ := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	g2 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package a2\n"), End: len("package a2\n"), Text: "// member edit\n"})
	paneAt(t, h, "a.go").File.Session().SetGroupTask(g2, "task-1")
	calls := 0
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	}
	_, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err == nil || !strings.Contains(err.Error(), "seam changed") {
		t.Fatalf("accept after a member edit = %v, want a seam-changed refusal", err)
	}
	if calls != 0 {
		t.Errorf("the hook ran %d time(s) on a changed seam, want 0", calls)
	}
	if _, ok := h.App.pendingPublishes["task-1"]; ok {
		t.Error("a seam-refused proposal is still pending; it must be re-proposed")
	}
}

// TestPublishAcceptIgnoresUnrelatedEdit is the other half of the seam pin: an
// edit to a buffer that is not a member of the wave leaves the seam equal to
// the export, so accept proceeds. The artifact is immutable; only the wave's
// own composition can invalidate the proposal.
func TestPublishAcceptIgnoresUnrelatedEdit(t *testing.T) {
	_, h, rec := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	// b.go is not a member of task-1: its edit must not block.
	proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package b2\n"})
	calls := 0
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		calls++
		return "publish-single: committed " + rec.CommitSHA + " on branch raj/wave-task-1 (HEAD and index untouched)\n", "", 0, nil
	}
	res, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err != nil {
		t.Fatalf("accept after an unrelated edit: %v", err)
	}
	if res.Publish == nil || res.Publish.Decided != "published" {
		t.Fatalf("publish result = %+v", res.Publish)
	}
	if calls != 1 {
		t.Errorf("the hook ran %d time(s), want 1", calls)
	}
}

// TestPublishAcceptRefusesHookDrift is the hook pin: the script edited between
// propose and accept is refused, and the hook does not run.
func TestPublishAcceptRefusesHookDrift(t *testing.T) {
	dir, h, _ := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	hook := filepath.Join(dir, "examples", "hooks", "publish-single.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n# changed after the proposal\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	}
	_, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("accept after a hook edit = %v, want a hook-drift refusal", err)
	}
	if calls != 0 {
		t.Errorf("the hook ran %d time(s) on drift, want 0", calls)
	}
}

// TestPublishAcceptRunsHookAndRecordsResult is the accept half: the pinned
// hook runs, and the result carries the exit code, the pushed sha, the remote
// ref and the URL.
func TestPublishAcceptRunsHookAndRecordsResult(t *testing.T) {
	_, h, rec := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "publish-single: committed " + rec.CommitSHA + " on branch raj/wave-task-1 (HEAD and index untouched)\n" +
			"publish-single: open a pull request: https://github.com/acme/widgets/pull/new/raj/wave-task-1\n", "", 0, nil
	}
	res, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	p := res.Publish
	if p.Decided != "published" || p.ExitCode != 0 {
		t.Fatalf("publish result = %+v", p)
	}
	if p.Pushed != rec.CommitSHA {
		t.Errorf("pushed sha = %q, want %q", p.Pushed, rec.CommitSHA)
	}
	if p.URL != "https://github.com/acme/widgets/pull/new/raj/wave-task-1" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.RemoteRef != "refs/heads/raj/wave-task-1" {
		t.Errorf("remote ref = %q", p.RemoteRef)
	}
	if _, ok := h.App.pendingPublishes["task-1"]; ok {
		t.Error("a published proposal is still pending")
	}
}

// TestPublishAcceptRecordsNonZeroExit pins the refusal: a non-fast-forward
// (the hook's exit 12) is recorded, not turned into a force.
func TestPublishAcceptRecordsNonZeroExit(t *testing.T) {
	_, h, _ := publishRoot(t)
	ctx := context.Background()
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "", 0, nil
	}
	if _, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Owner: "7"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	h.App.publishRun = func(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
		return "", "publish-single: refusing to publish raj/wave-task-1: not a fast-forward\n", 12, nil
	}
	res, err := h.App.runIntent(ctx, intent.Command{Mode: "publish", Name: "task-1", Approve: true, Owner: "7"})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	p := res.Publish
	if p.Decided != "refused" || p.ExitCode != 12 {
		t.Fatalf("refused result = %+v", p)
	}
	if !strings.Contains(p.Stderr, "not a fast-forward") {
		t.Errorf("stderr tail = %q", p.Stderr)
	}
}
