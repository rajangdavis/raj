package git

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// assertNoHardlinks fails on any scratch file with more than one link. A
// shared inode would let a build in the scratch tree corrupt the source, so
// this is the property reflink/copy exists to keep.
func assertNoHardlinks(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Nlink != 1 {
			t.Errorf("%s has %d links, want 1 (no hardlinks)", path, st.Nlink)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMaterialiseCopiesAndOverlaysWithoutTouchingTheSource asserts the whole
// section 3 contract: the projection wins, untracked files ride along, ignored
// dependencies appear only under the policy, and the source worktree and index
// are byte-identical afterwards.
func TestMaterialiseCopiesAndOverlaysWithoutTouchingTheSource(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dep/\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package b\n")
	mustMkdir(t, filepath.Join(dir, "dep"))
	writeFile(t, filepath.Join(dir, "dep", "lib.txt"), "dependency\n")

	// Settle the index with one status call, then snapshot it, so a later
	// status does not change the bytes we compare.
	beforeStatus := runGit(t, dir, "status", "--porcelain")
	beforeIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	beforeA, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(dir)
	m, err := svc.Materialise(context.Background(), Projection{
		"a.go": []byte("package a\n\nfunc f() {}\n"),
		"c.go": []byte("package c\n"),
	}, MaterialiseOptions{IncludeIgnored: true, Tree: true})
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	defer m.Remove()

	if got, _ := os.ReadFile(filepath.Join(m.Dir, "a.go")); string(got) != "package a\n\nfunc f() {}\n" {
		t.Errorf("a.go = %q, want the projected bytes", got)
	}
	if got, _ := os.ReadFile(filepath.Join(m.Dir, "c.go")); string(got) != "package c\n" {
		t.Errorf("c.go = %q, want the projected file", got)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "b.go")); err != nil {
		t.Errorf("untracked b.go missing from the scratch tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "dep", "lib.txt")); err != nil {
		t.Errorf("ignored dependency missing under the include policy: %v", err)
	}

	// The source worktree and the user's index are untouched.
	if afterA, _ := os.ReadFile(filepath.Join(dir, "a.go")); !bytes.Equal(beforeA, afterA) {
		t.Errorf("source a.go changed: %q -> %q", beforeA, afterA)
	}
	afterIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeIndex, afterIndex) {
		t.Error("the user's index changed")
	}
	if afterStatus := runGit(t, dir, "status", "--porcelain"); afterStatus != beforeStatus {
		t.Errorf("worktree status changed: %q -> %q", beforeStatus, afterStatus)
	}

	assertNoHardlinks(t, m.Dir)

	if m.Tree == "" {
		t.Fatal("Materialise with Tree reported no tree id")
	}
	if got := runGit(t, dir, "cat-file", "-t", m.Tree); got != "tree" {
		t.Errorf("tree type = %q, want tree", got)
	}
	if got := runGit(t, dir, "ls-tree", "-r", "--name-only", m.Tree); !strings.Contains(got, "c.go") {
		t.Errorf("tree %s = %q, want it to name c.go", m.Tree, got)
	}
}

// An ignored dependency is present on purpose or not at all: without the
// policy it is absent rather than copied by accident.
func TestMaterialiseOmitsIgnoredWithoutThePolicy(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "dep/\n")
	mustMkdir(t, filepath.Join(dir, "dep"))
	writeFile(t, filepath.Join(dir, "dep", "lib.txt"), "dependency\n")

	m, err := New(dir).Materialise(context.Background(), nil, MaterialiseOptions{})
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	defer m.Remove()
	if _, err := os.Stat(filepath.Join(m.Dir, "dep", "lib.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ignored dependency present without the policy: %v", err)
	}
}

func TestMaterialiseRefusesAnEscapingProjection(t *testing.T) {
	dir := initRepo(t)
	_, err := New(dir).Materialise(context.Background(),
		Projection{"../evil.go": []byte("x")}, MaterialiseOptions{})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Materialise = %v, want an escape refusal", err)
	}
}

func TestMaterialiseRefusesWithoutARepository(t *testing.T) {
	requireGit(t)
	_, err := New(t.TempDir()).Materialise(context.Background(), nil, MaterialiseOptions{})
	if !errors.Is(err, ErrNoRepository) {
		t.Fatalf("Materialise outside a repo = %v, want ErrNoRepository", err)
	}
}

func TestMaterialisedRemove(t *testing.T) {
	dir := initRepo(t)
	m, err := New(dir).Materialise(context.Background(), nil, MaterialiseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scratch still exists after Remove: %v", err)
	}
}
