package intent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/git"
	"raj/internal/store"
)

// requireGit skips an integration test on a machine without git. The intent
// tests exercise real object plumbing, not a fake runner.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// runGit runs git in dir for fixture setup with a pinned identity.
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

// runGitMayFail runs git and returns its combined output and error, for a
// command whose failure is the assertion.
func runGitMayFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// initRepo makes a repository with an ordinary file, an executable file and a
// file to delete, and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n", 0o644)
	writeTestFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n", 0o755)
	writeTestFile(t, filepath.Join(dir, "b.go"), "package b\n", 0o644)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "first")
	return dir
}

func writeTestFile(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// newTestStore opens a fresh workspace store in a temp directory.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func headSHA(t *testing.T, svc *git.Service) string {
	t.Helper()
	sha, err := svc.RevParse(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// TestIntentBaseRef pins an intention's base ref to the commit it named at
// creation, so re-materialising does not follow a moved ref.
func TestIntentBaseRef(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	sha := headSHA(t, svc)

	m1 := Member{ID: 1, Path: "a.go"}
	m2 := Member{ID: 2, Path: "b.go"}
	in := New("i", "owner", "HEAD", sha, []Member{m1, m2}, time.Unix(1, 0))
	if in.BaseSHA != sha {
		t.Errorf("BaseSHA = %q, want %q", in.BaseSHA, sha)
	}
	if in.State != Open {
		t.Errorf("State = %q, want open", in.State)
	}
	if !in.Has(m1) || !in.Has(m2) || in.Has(Member{ID: 3, Path: "a.go"}) {
		t.Errorf("membership = %v, want 1 in a.go and 2 in b.go", in.Members)
	}
	if in.Created.IsZero() {
		t.Error("Created is zero; the date must be pinned at creation")
	}
}

// TestIntentBaseChain resolves a stack in dependency order and refuses a cycle.
func TestIntentBaseChain(t *testing.T) {
	all := Set{
		"A": {Name: "A", Base: "HEAD"},
		"B": {Name: "B", Base: "A"},
		"C": {Name: "C", Base: "B"},
	}
	chain, err := all.Chain("C")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 || chain[0].Name != "A" || chain[1].Name != "B" || chain[2].Name != "C" {
		t.Fatalf("chain = %v, want [A B C]", chain)
	}
}

// TestIntentCycleRejected refuses a base-intention cycle rather than walking it.
func TestIntentCycleRejected(t *testing.T) {
	all := Set{
		"A": {Name: "A", Base: "B"},
		"B": {Name: "B", Base: "A"},
	}
	if _, err := all.Chain("A"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Chain cycle = %v, want a cycle refusal", err)
	}
}

// TestMissingDepGroup is D2: a selected member group that a live buffer does
// not hold is a hard error naming the intention, the missing group and the
// dependent group of the selection, never a silent auto-include.
func TestMissingDepGroup(t *testing.T) {
	in := Intention{Name: "I", Members: []Member{{ID: 7, Path: "a.go"}, {ID: 8, Path: "b.go"}}}
	find := func(m Member) (Member, bool) {
		if m.ID == 7 {
			return Member{}, false
		}
		return m, true
	}
	_, err := in.ResolveMembers(find)
	if err == nil {
		t.Fatal("ResolveMembers with a missing group succeeded, want a hard error")
	}
	for _, want := range []string{"I", "7", "8", "a.go", "b.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestMaterialiseTreeMatchesGroups pins the member filter: of two groups
// touching two files, only the member is in the exported tree and the other
// group stays out, so the base content stands there. Without the filter both
// files would carry the projected bytes and this fails.
func TestMaterialiseTreeMatchesGroups(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	ctx := context.Background()
	base := headSHA(t, svc)

	// Two groups in two buffers; only group 1 is a member. full is what the
	// app composes from the live buffers before the selection.
	full := Projection{
		"a.go": []byte("package member\n"),
		"b.go": []byte("package other\n"),
	}
	find := func(m Member) (Member, bool) {
		switch m.ID {
		case 1:
			return Member{ID: 1, Path: "a.go"}, true
		case 2:
			return Member{ID: 2, Path: "b.go"}, true
		}
		return Member{}, false
	}
	in := New("i", "owner", "HEAD", base, []Member{{ID: 1, Path: "a.go"}}, time.Unix(3, 0))

	sel, err := in.Select(full, find)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Export(ctx, svc, in, base, sel)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Show(ctx, res.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "package member\n" {
		t.Errorf("tree a.go = %q, want the member projected bytes", got)
	}
	other, err := svc.Show(ctx, res.Tree, "b.go")
	if err != nil {
		t.Fatal(err)
	}
	if other != "package b\n" {
		t.Errorf("tree b.go = %q, want the base bytes, not the non-member group", other)
	}
}

// TestMaterialiseNoRefMove proves materialising writes no ref.
func TestMaterialiseNoRefMove(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	before := headSHA(t, svc)

	if _, err := Materialise(context.Background(), svc, "HEAD", Projection{"a.go": []byte("x\n")}); err != nil {
		t.Fatal(err)
	}
	if after := headSHA(t, svc); after != before {
		t.Errorf("HEAD moved from %s to %s", before, after)
	}
}

// TestMaterialiseHeadIndexWorktreeUntouched proves the user's index and
// worktree are untouched: only a temporary index and dangling objects are
// written.
func TestMaterialiseHeadIndexWorktreeUntouched(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	head := headSHA(t, svc)
	indexBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	fileBefore, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Materialise(context.Background(), svc, "HEAD", Projection{"a.go": []byte("different\n")}); err != nil {
		t.Fatal(err)
	}
	if after := headSHA(t, svc); after != head {
		t.Errorf("HEAD moved to %s", after)
	}
	indexAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Error("the user's index changed")
	}
	fileAfter, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(fileAfter) != string(fileBefore) {
		t.Error("the worktree file changed")
	}
}

// TestMaterialiseIdempotentTree is D3: the same base and projection give the
// same tree id.
func TestMaterialiseIdempotentTree(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	proj := Projection{"a.go": []byte("one\n"), "run.sh": []byte("#!/bin/sh\necho\n")}

	t1, err := Materialise(context.Background(), svc, "HEAD", proj)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := Materialise(context.Background(), svc, "HEAD", proj)
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Errorf("tree %s != %s; materialisation is not deterministic", t1, t2)
	}
}

// TestDeleteAndModeBits proves a nil projection value deletes a path and an
// overlaid path keeps the executable bit base's index gave it.
func TestDeleteAndModeBits(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	proj := Projection{
		"run.sh": []byte("#!/bin/sh\necho hi\n"),
		"b.go":   nil,
	}

	tree, err := Materialise(context.Background(), svc, "HEAD", proj)
	if err != nil {
		t.Fatal(err)
	}
	listing := runGit(t, dir, "ls-tree", "-r", tree)
	if strings.Contains(listing, "b.go") {
		t.Errorf("deleted path survived:\n%s", listing)
	}
	if !strings.Contains(listing, "100755 blob") || !strings.Contains(listing, "run.sh") {
		t.Errorf("executable mode not preserved:\n%s", listing)
	}
}

// TestBaseDriftWarns is D3's warning: a base ref moved past the pinned BaseSHA
// warns and keeps the pinned SHA.
func TestBaseDriftWarns(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	ctx := context.Background()
	pinned := headSHA(t, svc)
	in := New("i", "owner", "HEAD", pinned, nil, time.Unix(1, 0))

	writeTestFile(t, filepath.Join(dir, "c.go"), "package c\n", 0o644)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "second")

	warn, err := Drift(ctx, svc, in)
	if err != nil {
		t.Fatal(err)
	}
	if warn == "" {
		t.Fatal("no warning for a moved base")
	}
	if !strings.Contains(warn, pinned) {
		t.Errorf("warning %q does not name the pinned sha %s", warn, pinned)
	}
	if in.BaseSHA != pinned {
		t.Errorf("BaseSHA = %s, want the pinned %s", in.BaseSHA, pinned)
	}
}

// TestExportRecordFields round-trips one export row through the store.
func TestExportRecordFields(t *testing.T) {
	st := newTestStore(t)
	rec := store.ExportRecord{
		Intention: "i", Groups: []store.IntentionMember{{ID: 4, Path: "a.go"}, {ID: 5, Path: "b.go"}},
		CommitSHA: "c1", ParentSHA: "p0", BaseSHA: "b0", TreeSHA: "t1", Time: 1234,
	}
	if err := st.PutExport(rec); err != nil {
		t.Fatal(err)
	}
	got, err := st.Exports("i")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Exports = %d rows, want 1", len(got))
	}
	g := got[0]
	if g.Intention != "i" || len(g.Groups) != 2 || g.Groups[0].ID != 4 || g.Groups[1].ID != 5 ||
		g.Groups[0].Path != "a.go" || g.Groups[1].Path != "b.go" ||
		g.CommitSHA != "c1" || g.ParentSHA != "p0" || g.BaseSHA != "b0" ||
		g.TreeSHA != "t1" || g.Time != 1234 {
		t.Errorf("export row = %+v, want the written fields", g)
	}
	last, ok, err := st.LastExport("i")
	if err != nil || !ok || last.CommitSHA != "c1" {
		t.Errorf("LastExport = %+v, %v, %v; want c1", last, ok, err)
	}
}

// TestIntentExportParentedOnBase proves one export parented on the base ref's
// head, with no chain to a previous export (no stacks: one MR per wave).
func TestIntentExportParentedOnBase(t *testing.T) {
	dir := initRepo(t)
	svc := git.New(dir)
	ctx := context.Background()
	base := headSHA(t, svc)
	in := New("i", "owner", "HEAD", base, nil, time.Unix(2, 0))

	res, err := Export(ctx, svc, in, base, Projection{"a.go": []byte("one\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent != base {
		t.Errorf("parent = %s, want the base %s", res.Parent, base)
	}
	if res.Commit == "" || res.Commit == base {
		t.Errorf("commit = %q, want a new commit", res.Commit)
	}
}
