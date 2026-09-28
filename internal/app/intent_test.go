package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/editor"
	"raj/internal/git"
	"raj/internal/intent"
	"raj/internal/piecetable"
)

// intentRoot makes the harness workspace a real git repository with a.go and
// b.go committed, so runIntent resolves a real base and materialises real
// trees. The app-level tests below drive runIntent, the app projection and the
// store, not a hand-built projection.
//
// The two sessions number their change sets independently, so this fixture
// keeps their ids apart: open and close a throwaway group on a.go so its next
// local id is above b.go's. A test that needs the collision uses
// intentRootColliding; with bare ids the collision would be ambiguous, which is
// the defect qualified membership closes.
func intentRoot(t *testing.T) (string, *harness) {
	return intentRootAt(t, true)
}

// intentRootColliding is intentRoot without the id separation, so a.go and b.go
// both number their first change set 1. It is the fixture for the qualified
// membership test.
func intentRootColliding(t *testing.T) (string, *harness) {
	return intentRootAt(t, false)
}

func intentRootAt(t *testing.T, separate bool) (string, *harness) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for name, text := range map[string]string{"a.go": "package a\n", "b.go": "package b\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd := func(args ...string) {
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
	gitCmd("init", "-q")
	gitCmd("add", "-A")
	gitCmd("commit", "-q", "--no-gpg-sign", "-m", "base")

	h := newHarnessAt(t, dir)
	h.OpenFile(filepath.Join(dir, "a.go"))
	h.OpenFile(filepath.Join(dir, "b.go"))
	if separate {
		aPane := paneAt(t, h, "a.go")
		aPane.File.Begin()
		aPane.File.End()
	}
	return dir, h
}

// TestIntentTaskQualifiesCollidingGroupIDs is the whole point of qualified
// membership: two buffers each number a group 1, and a task spanning them must
// resolve each member to the buffer at its own path. Before qualification the
// finder collapsed the two ids and one buffer's content was silently dropped.
func TestIntentTaskQualifiesCollidingGroupIDs(t *testing.T) {
	dir, h := intentRootColliding(t)
	ctx := context.Background()
	svc := git.New(dir)

	g1 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package a2\n"})
	g2 := proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package b2\n"})
	if g1 != g2 {
		t.Fatalf("fixture: a.go numbered %d and b.go %d; the ids must collide", g1, g2)
	}
	paneAt(t, h, "a.go").File.Session().SetGroupTask(g1, "task-x")
	paneAt(t, h, "b.go").File.Session().SetGroupTask(g2, "task-x")

	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Task: "task-x"})
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "i"})
	if show.Intention == nil || len(show.Intention.Members) != 2 {
		t.Fatalf("members = %+v, want two qualified members", show.Intention)
	}
	if show.Intention.Members[0].Path == show.Intention.Members[1].Path {
		t.Fatalf("members = %+v, want two different buffer paths", show.Intention.Members)
	}

	res := doIntent(t, h, intent.Command{Mode: "materialise", Name: "i"})
	gotA, err := svc.Show(ctx, res.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if gotA != "package a2\n" {
		t.Errorf("tree a.go = %q, want the a.go member", gotA)
	}
	gotB, err := svc.Show(ctx, res.Tree, "b.go")
	if err != nil {
		t.Fatal(err)
	}
	if gotB != "package b2\n" {
		t.Errorf("tree b.go = %q, want the b.go member, not a's or the base", gotB)
	}
}

func paneAt(t *testing.T, h *harness, name string) *editor.Pane {
	t.Helper()
	want := filepath.Join(h.primaryRoot(), name)
	for _, p := range h.Tabs.All() {
		if p.File.Path == want {
			return p
		}
	}
	t.Fatalf("no pane for %s", name)
	return nil
}

// proposeOn lands hunks on a named pane as an agent and marks the set Proposed,
// exactly the shape host.Buffers reports.
func proposeOn(t *testing.T, h *harness, name string, hunks ...piecetable.Hunk) uint64 {
	t.Helper()
	p := paneAt(t, h, name)
	sess := p.File.Session()
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Agent, sess.Version(), hunks)
	p.File.End()
	id := sess.LastGroup()
	sess.MarkGroup(id, piecetable.Proposed)
	return id
}

func doIntent(t *testing.T, h *harness, cmd intent.Command) intent.Result {
	t.Helper()
	res, err := h.App.runIntent(context.Background(), cmd)
	if err != nil {
		t.Fatalf("runIntent %s: %v", cmd.Mode, err)
	}
	return res
}

// TestIntentMaterialiseMemberFilter is the member-filtered projection: of two
// groups on two files, only the member is composed in, the unrelated
// view-dirty buffer keeps its base bytes, and re-materialising is stable.
func TestIntentMaterialiseMemberFilter(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	g1 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package g1\n"})
	proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package g2\n"})

	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Groups: []uint64{g1}})
	res := doIntent(t, h, intent.Command{Mode: "materialise", Name: "i"})
	if res.Tree == "" {
		t.Fatal("materialise returned no tree")
	}

	got, err := svc.Show(ctx, res.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "package g1\n" {
		t.Errorf("a.go = %q, want the member composition %q", got, "package g1\n")
	}

	// (c) b.go holds the non-member group and is view-dirty, yet its path is
	// not touched by a member, so it stays exactly the base blob.
	if !paneAt(t, h, "b.go").File.ViewDirty() {
		t.Fatal("fixture: b.go must be view-dirty")
	}
	base, err := svc.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	baseB, err := svc.Show(ctx, base, "b.go")
	if err != nil {
		t.Fatal(err)
	}
	treeB, err := svc.Show(ctx, res.Tree, "b.go")
	if err != nil {
		t.Fatal(err)
	}
	if treeB != baseB || treeB != "package b\n" {
		t.Errorf("b.go = %q, want the base blob %q", treeB, baseB)
	}

	// (d) the same inputs give the same tree id.
	again := doIntent(t, h, intent.Command{Mode: "materialise", Name: "i"})
	if again.Tree != res.Tree {
		t.Errorf("re-materialise tree = %s, want %s", again.Tree, res.Tree)
	}
}

// TestIntentMaterialiseExcludesNonMemberGroupInSameFile is the same-file half:
// two proposed groups share a buffer, only one is a member, and the
// non-member's hunk must be absent from the materialised bytes.
func TestIntentMaterialiseExcludesNonMemberGroupInSameFile(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	g1 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package g1\n"})
	proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package g1\n"), End: len("package g1\n"), Text: "// g2\n"})

	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Groups: []uint64{g1}})
	res := doIntent(t, h, intent.Command{Mode: "materialise", Name: "i"})
	got, err := svc.Show(ctx, res.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "package g1\n" {
		t.Errorf("a.go = %q, want only the member group's bytes", got)
	}
	if strings.Contains(got, "g2") {
		t.Errorf("a.go = %q holds the non-member hunk", got)
	}
}

// TestIntentDryRunStackedWritesNothing is item 4: materialise and export
// --dry-run on a stacked intention whose parent is not exported must refuse,
// not export the parent and write a record row behind the caller's back.
func TestIntentDryRunStackedWritesNothing(t *testing.T) {
	_, h := intentRoot(t)
	g1 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package g1\n"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "parent", Base: "HEAD", Groups: []uint64{g1}})
	doIntent(t, h, intent.Command{Mode: "new", Name: "child", Base: "parent", Groups: []uint64{g1}})

	if _, err := h.App.runIntent(context.Background(), intent.Command{Mode: "materialise", Name: "child"}); err == nil {
		t.Fatal("materialise of a stacked intention with no parent export succeeded, want a refusal")
	}
	if _, found, err := h.App.state.LastExport("parent"); err != nil || found {
		t.Fatalf("materialise exported the parent (found=%v err=%v); a dry run must write nothing", found, err)
	}
	if _, err := h.App.runIntent(context.Background(), intent.Command{Mode: "export", Name: "child", DryRun: true}); err == nil {
		t.Fatal("export --dry-run of a stacked intention with no parent export succeeded, want a refusal")
	}
	if _, found, err := h.App.state.LastExport("parent"); err != nil || found {
		t.Fatalf("export --dry-run exported the parent (found=%v err=%v)", found, err)
	}
}

// TestIntentTaskPersists is D6: the task is stored at new and show reads it
// back. The export writes no note any more (H4 revision).
func TestIntentTaskPersists(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	g1 := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package g1\n"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Groups: []uint64{g1}, Task: "task-9"})

	show := doIntent(t, h, intent.Command{Mode: "show", Name: "i"})
	if show.Intention == nil || show.Intention.Task != "task-9" {
		t.Fatalf("stored intention task = %+v, want task-9", show.Intention)
	}

	res := doIntent(t, h, intent.Command{Mode: "export", Name: "i"})
	base, err := svc.RevParse(ctx, res.Commit+"^")
	if err != nil {
		t.Fatal(err)
	}
	if base != res.BaseSHA {
		t.Errorf("commit parent = %s, want the base %s", base, res.BaseSHA)
	}
}
