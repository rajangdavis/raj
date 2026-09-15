package explorer

import (
	"os"
	"path/filepath"
	"testing"
)

// The tree notices a name added or removed outside raj and settles once it has
// been rebuilt -- the signal the idle tick refreshes on.
func TestChangedOnDiskDetectsNames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := NewTree(root)
	if tr.ChangedOnDisk() {
		t.Fatal("a freshly built tree reports a change")
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !tr.ChangedOnDisk() {
		t.Error("a file created outside raj was not detected")
	}
	tr.Refresh()
	if tr.ChangedOnDisk() {
		t.Error("refresh did not settle the signature")
	}
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	if !tr.ChangedOnDisk() {
		t.Error("a file removed outside raj was not detected")
	}
}

// Only expanded directories are listed, so the scan sees a child only once its
// parent is open -- the same bound the tree itself draws.
func TestChangedOnDiskWatchesExpandedDirs(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tr := NewTree(root)
	if tr.ChangedOnDisk() {
		t.Fatal("a freshly built tree reports a change")
	}
	if err := os.WriteFile(filepath.Join(sub, "inside.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tr.ChangedOnDisk() {
		t.Error("a child of a collapsed directory was seen")
	}
	tr.Toggle(sub)
	if err := os.WriteFile(filepath.Join(sub, "seen.go"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !tr.ChangedOnDisk() {
		t.Error("a child of an expanded directory was not seen")
	}
}
