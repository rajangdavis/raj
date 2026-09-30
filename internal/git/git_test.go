package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips an integration test on a machine without git. The package
// is exercised against real git here, not only against a fake runner.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// runGit runs git in dir for fixture setup, so a test builds real state rather
// than a fake. The identity is pinned so a host with no git user configured
// still commits.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo makes a repository with one commit and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "first")
	return dir
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestViewReadsTheRealRepository builds a repo with a modified tracked file,
// an untracked file and an ignored file, and asserts the structured read sees
// all three without parsing any of git's text.
func TestViewReadsTheRealRepository(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc f() {}\n")
	writeFile(t, filepath.Join(dir, "new.go"), "package a\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "secret\n")

	v, err := New(dir).View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Root == "" {
		t.Error("View.Root is empty")
	}
	if v.Head == "" {
		t.Error("View.Head is empty on a repo with a commit")
	}
	if v.Branch == "" || v.Detached {
		t.Errorf("branch = %q detached=%v, want an attached branch", v.Branch, v.Detached)
	}
	modified := false
	for _, e := range v.Entries {
		if e.Path == "a.go" && strings.Contains(e.Index+e.Worktree, "M") {
			modified = true
		}
	}
	if !modified {
		t.Errorf("entries = %+v, want a.go modified", v.Entries)
	}
	if !containsString(v.Untracked, "new.go") {
		t.Errorf("untracked = %v, want new.go", v.Untracked)
	}
	if !containsString(v.Ignored, "ignored.txt") {
		t.Errorf("ignored = %v, want ignored.txt", v.Ignored)
	}
}

func TestViewRefusesNoRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := New(dir).View(context.Background())
	if !errors.Is(err, ErrNoRepository) {
		t.Fatalf("View of a plain directory = %v, want ErrNoRepository", err)
	}
}

func TestViewRefusesBareRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "--bare")
	_, err := New(dir).View(context.Background())
	if !errors.Is(err, ErrBareRepository) {
		t.Fatalf("View of a bare repo = %v, want ErrBareRepository", err)
	}
}

// failingRunner stands in for a git that cannot start, so the unavailable
// refusal is testable without removing git from the machine.
type failingRunner struct{ err error }

func (f failingRunner) Run(context.Context, string, []byte, ...string) ([]byte, error) {
	return nil, f.err
}

func TestViewRefusesGitUnavailable(t *testing.T) {
	svc := NewWithRunner("/w", failingRunner{err: ErrGitUnavailable})
	_, err := svc.View(context.Background())
	if !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("View = %v, want ErrGitUnavailable", err)
	}
}

func TestViewReportsUnborn(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	v, err := New(dir).View(context.Background())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if !v.Unborn || v.Head != "" {
		t.Errorf("unborn=%v head=%q, want unborn with no head", v.Unborn, v.Head)
	}
	if v.Branch == "" {
		t.Error("an unborn HEAD still names the branch it will be born on")
	}
}

// TestPlumbingWritesObjectsWithoutMovingHead drives the whole object pipeline
// (blob, tree, commit, note) and asserts HEAD is where it started, even though
// the note updates the refs/notes/commits ref.
func TestPlumbingWritesObjectsWithoutMovingHead(t *testing.T) {
	dir := initRepo(t)
	before := runGit(t, dir, "rev-parse", "HEAD")
	svc := New(dir)
	ctx := context.Background()

	blob, err := svc.HashObject(ctx, []byte("hello\n"))
	if err != nil {
		t.Fatalf("HashObject: %v", err)
	}
	if len(blob) != 40 {
		t.Errorf("blob = %q, want a 40-hex id", blob)
	}
	if got := runGit(t, dir, "cat-file", "-t", blob); got != "blob" {
		t.Errorf("object type = %q, want blob", got)
	}

	tree, err := svc.Mktree(ctx, []TreeEntry{{Mode: "100644", Type: "blob", Hash: blob, Name: "hello.txt"}})
	if err != nil {
		t.Fatalf("Mktree: %v", err)
	}
	if got := runGit(t, dir, "cat-file", "-t", tree); got != "tree" {
		t.Errorf("tree type = %q, want tree", got)
	}

	commit, err := svc.CommitTree(ctx, tree, nil, "a dangling commit")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if got := runGit(t, dir, "cat-file", "-t", commit); got != "commit" {
		t.Errorf("commit type = %q, want commit", got)
	}

	if err := svc.Note(ctx, commit, "task=abc"); err != nil {
		t.Fatalf("Note: %v", err)
	}

	if after := runGit(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
}

func TestShowReturnsTheCommittedBlob(t *testing.T) {
	dir := initRepo(t)
	text, err := New(dir).Show(context.Background(), "HEAD", filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if text != "package a\n" {
		t.Errorf("Show = %q, want the committed bytes", text)
	}
}

func TestCallNumstatAndLog(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc f() {}\n")
	svc := New(dir)
	ctx := context.Background()

	res, err := svc.Call(ctx, Query{Mode: "numstat"})
	if err != nil {
		t.Fatalf("Call numstat: %v", err)
	}
	if len(res.Numstat) != 1 || res.Numstat[0].Path != "a.go" || res.Numstat[0].Additions == 0 {
		t.Errorf("numstat = %+v, want a.go with additions", res.Numstat)
	}
	logRes, err := svc.Call(ctx, Query{Mode: "log", Count: 1})
	if err != nil {
		t.Fatalf("Call log: %v", err)
	}
	if len(logRes.Log) != 1 || logRes.Log[0].Subject != "first" {
		t.Errorf("log = %+v, want the first commit", logRes.Log)
	}
	if _, err := svc.Call(ctx, Query{Mode: "bogus"}); err == nil {
		t.Error("Call with an unknown mode was accepted")
	}
	if _, err := svc.Call(ctx, Query{}); err == nil {
		t.Error("Call with no mode was accepted")
	}
}

// TestStatusDigestMovesWithTheWorktree pins the provenance stamp: a clean tree
// yields one digest and reading it twice yields the same one, while modifying a
// tracked file moves it. Precondition: initRepo makes a repository with a
// committed a.go and a clean worktree; the test then rewrites a.go. Without
// StatusDigest this does not compile; a digest over a constant, or over only
// HEAD, would not move when the worktree does and the inequality fails.
func TestStatusDigestMovesWithTheWorktree(t *testing.T) {
	dir := initRepo(t)
	svc := New(dir)
	ctx := context.Background()

	clean, err := svc.StatusDigest(ctx)
	if err != nil {
		t.Fatalf("StatusDigest: %v", err)
	}
	if len(clean) != 64 {
		t.Errorf("StatusDigest = %q, want a 64-hex sha256", clean)
	}
	again, err := svc.StatusDigest(ctx)
	if err != nil {
		t.Fatalf("StatusDigest: %v", err)
	}
	if again != clean {
		t.Errorf("StatusDigest of a clean tree moved: %q != %q", again, clean)
	}

	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc f() {}\n")
	moved, err := svc.StatusDigest(ctx)
	if err != nil {
		t.Fatalf("StatusDigest after edit: %v", err)
	}
	if moved == clean {
		t.Errorf("StatusDigest unchanged after a tracked file moved: %q", moved)
	}

	// A file added inside an untracked directory must move the digest too:
	// plain `git status --porcelain` collapses the directory into one `?? dir/`
	// line, so without --untracked-files=all the second file is invisible.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "one.txt"), "one\n")
	before, err := svc.StatusDigest(ctx)
	if err != nil {
		t.Fatalf("StatusDigest with an untracked dir: %v", err)
	}
	writeFile(t, filepath.Join(sub, "two.txt"), "two\n")
	after, err := svc.StatusDigest(ctx)
	if err != nil {
		t.Fatalf("StatusDigest after a second untracked file: %v", err)
	}
	if after == before {
		t.Errorf("StatusDigest ignored a file added inside an untracked directory: %q", after)
	}
}

// TestDiffTreesReadsTwoObjects is the object-only read an intention diff uses:
// a commit and a tree object are compared without a worktree read, and the
// patch and numstat describe exactly the change between them.
func TestDiffTreesReadsTwoObjects(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a.go"), "package a2\n")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "second")

	svc := New(dir)
	ctx := context.Background()
	base := runGit(t, dir, "rev-parse", "HEAD^")
	tree := runGit(t, dir, "rev-parse", "HEAD^{tree}")
	patch, stat, err := svc.DiffTrees(ctx, base, tree)
	if err != nil {
		t.Fatalf("DiffTrees: %v", err)
	}
	if !strings.Contains(patch, "-package a") || !strings.Contains(patch, "+package a2") {
		t.Errorf("patch = %q, want the change between the two objects", patch)
	}
	if len(stat) != 1 || stat[0].Path != "a.go" || stat[0].Additions != 1 || stat[0].Deletions != 1 {
		t.Errorf("stat = %+v, want one a.go row at 1/1", stat)
	}
	if _, _, err := svc.DiffTrees(ctx, "", tree); err == nil {
		t.Error("DiffTrees with no base must be refused, not defaulted to HEAD")
	}
}
