package gitleaves

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/git"
	"raj/internal/hooks"
	"raj/internal/hooks/builtin"
)

// requireGit skips an integration test on a machine without git. These tests
// exercise the leaves against real git, not a fake.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// runGit runs git in dir for fixture setup. The identity is pinned so a host
// with no git user configured still commits.
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
	writeFile(t, filepath.Join(dir, "a.go"), []byte("package a\n"))
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "first")
	return dir
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runLeaf looks a leaf up by name and runs it with root pinned in args, the
// way a test hands it a workspace. Any error fails the test.
func runLeaf(t *testing.T, name string, args map[string]any) builtin.Result {
	t.Helper()
	leaf, _, ok := builtin.Lookup(name)
	if !ok || leaf == nil {
		t.Fatalf("leaf %s is not registered", name)
	}
	res, err := leaf.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("%s.Run: %v (output %q)", name, err, res.Output)
	}
	return res
}

// TestReadLeavesAgentCallable pins the five read leaves as AgentDefault and
// proves the policy reaches admission: a hook naming git.status parses, is
// admitted to an agent, and runs against a real repository.
func TestReadLeavesAgentCallable(t *testing.T) {
	dir := initRepo(t)
	for _, name := range []string{"git.status", "git.diff", "git.numstat", "git.log", "git.show"} {
		leaf, policy, ok := builtin.Lookup(name)
		if !ok || leaf == nil {
			t.Fatalf("read leaf %s is not registered", name)
		}
		if policy != builtin.AgentDefault {
			t.Errorf("policy of %s = %v; want AgentDefault", name, policy)
		}
		if leaf.Name() != name {
			t.Errorf("%s reports name %q", name, leaf.Name())
		}
	}

	raw := hooks.Raw{Name: "context", Action: `{"builtin":"git.status"}`, Trigger: "agent", Agent: true, Enabled: true}
	set, errs := hooks.NewSet([]hooks.Raw{raw})
	if len(errs) != 0 {
		t.Fatalf("NewSet: %v", errs)
	}
	if _, err := set.Admit("context", true); err != nil {
		t.Fatalf("Admit(context, agent) = %v; want admitted", err)
	}

	res := runLeaf(t, "git.status", map[string]any{"root": dir})
	if res.Exit != 0 || !strings.Contains(res.Output, `"root"`) {
		t.Errorf("git.status = %q exit %d; want JSON containing a root", res.Output, res.Exit)
	}
}

// TestObjectLeafDeniesAgentByDefault pins the four object leaves as
// HumanOnly, and proves Admit refuses an agent a hook naming one even when the
// hook's own agent flag is set, while the human may still run it.
func TestObjectLeafDeniesAgentByDefault(t *testing.T) {
	for _, name := range []string{"git.blob", "git.tree", "git.commit", "git.note"} {
		_, policy, ok := builtin.Lookup(name)
		if !ok {
			t.Fatalf("object leaf %s is not registered", name)
		}
		if policy != builtin.HumanOnly {
			t.Errorf("policy of %s = %v; want HumanOnly", name, policy)
		}
	}

	set, errs := hooks.NewSet([]hooks.Raw{{Name: "export", Action: `{"builtin":"git.note"}`, Trigger: "agent", Agent: true, Enabled: true}})
	if len(errs) != 0 {
		t.Fatalf("NewSet: %v", errs)
	}
	if _, err := set.Admit("export", true); !errors.Is(err, hooks.ErrHumanOnlyLeaf) {
		t.Fatalf("Admit(export, agent) = %v; want ErrHumanOnlyLeaf", err)
	}
	if _, err := set.Admit("export", false); err != nil {
		t.Fatalf("Admit(export, human) = %v; want admitted", err)
	}
}

// TestObjectLeavesNoRefMove drives the whole object pipeline through the
// leaves and asserts no ref moved. Precondition: a real repository. Without
// NoteObject using notes-add, git.note would move refs/notes/commits.
func TestObjectLeavesNoRefMove(t *testing.T) {
	dir := initRepo(t)
	before := runGit(t, dir, "for-each-ref")
	head := runGit(t, dir, "rev-parse", "HEAD")

	blob := strings.TrimSpace(runLeaf(t, "git.blob", map[string]any{"root": dir, "data": "hello\n"}).Output)
	tree := strings.TrimSpace(runLeaf(t, "git.tree", map[string]any{"root": dir,
		"entries": []any{map[string]any{"mode": "100644", "type": "blob", "hash": blob, "name": "hello.txt"}}}).Output)
	commit := strings.TrimSpace(runLeaf(t, "git.commit", map[string]any{"root": dir, "tree": tree, "message": "dangling"}).Output)
	note := runLeaf(t, "git.note", map[string]any{"root": dir, "object": commit, "message": "task=abc"}).Output
	if !strings.Contains(note, `"blob"`) || !strings.Contains(note, `"tree"`) {
		t.Fatalf("git.note = %q; want both a blob and a tree id", note)
	}

	after := runGit(t, dir, "for-each-ref")
	if after != before {
		t.Errorf("for-each-ref moved:\nbefore %q\nafter  %q", before, after)
	}
	if got := runGit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s -> %s", head, got)
	}
	if strings.Contains(after, "refs/notes") {
		t.Errorf("git.note created a notes ref:\n%s", after)
	}
}

// TestEmptyDiffSuccess pins that an empty diff is success: no output, exit
// zero, no error, so a following step in a chain would still run.
func TestEmptyDiffSuccess(t *testing.T) {
	dir := initRepo(t)
	res := runLeaf(t, "git.diff", map[string]any{"root": dir})
	if res.Exit != 0 {
		t.Errorf("empty diff exit = %d; want 0", res.Exit)
	}
	if res.Output != "" {
		t.Errorf("empty diff output = %q; want empty", res.Output)
	}
}

// TestReadLeafRefusesNoRepository pins the not-a-repo edge: a read leaf names
// the git refusal rather than reading a plain directory.
func TestReadLeafRefusesNoRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	leaf, _, ok := builtin.Lookup("git.status")
	if !ok {
		t.Fatal("git.status is not registered")
	}
	res, err := leaf.Run(context.Background(), map[string]any{"root": dir})
	if err == nil {
		t.Fatalf("git.status outside a repo returned %q, nil; want a refusal", res.Output)
	}
	if !errors.Is(err, git.ErrNoRepository) {
		t.Fatalf("git.status error = %v; want ErrNoRepository", err)
	}
}

// TestStatusDetachedHead pins the detached-HEAD edge: View still reads and
// reports it rather than failing.
func TestStatusDetachedHead(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "checkout", "-q", "--detach")
	res := runLeaf(t, "git.status", map[string]any{"root": dir})
	if !strings.Contains(res.Output, `"detached":true`) {
		t.Errorf("git.status on a detached HEAD = %q; want detached true", res.Output)
	}
}

// TestNumstatBinaryUsesDash pins the binary edge: git prints "-" for a binary
// file's counts, and the leaf reports that as Binary true rather than zero.
func TestNumstatBinaryUsesDash(t *testing.T) {
	dir := initRepo(t)
	path := filepath.Join(dir, "b.bin")
	writeFile(t, path, []byte{0x00, 0x01, 0x02, 0x00})
	runGit(t, dir, "add", "b.bin")
	runGit(t, dir, "commit", "-q", "--no-gpg-sign", "-m", "binary")
	writeFile(t, path, []byte{0x00, 0x03, 0x04, 0x00})

	res := runLeaf(t, "git.numstat", map[string]any{"root": dir})
	if !strings.Contains(res.Output, `"binary":true`) {
		t.Errorf("git.numstat of a binary file = %q; want binary true", res.Output)
	}
}
