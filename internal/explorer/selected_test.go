package explorer

import (
	"os"
	"path/filepath"
	"testing"
)

// selectedFixture builds a root with one directory holding one file, with the
// directory open so the file is a row.
func selectedFixture(t *testing.T) (*Pane, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPane(root)
	p.Tree.Expand(dir)
	p.Tree.Refresh()
	return p, file
}

// Arrowing onto a file must report it so the app can preview, and a directory
// must not: a directory expands on enter rather than being previewed. Without
// the Dir check this passes the directory path on as though it were a file.
func TestSelectedPathReportsFilesOnly(t *testing.T) {
	p, file := selectedFixture(t)
	selectEntry(t, p, "pkg")
	if got, ok := p.SelectedPath(); ok || got != "" {
		t.Errorf("SelectedPath on a directory = (%q, %v), want (\"\", false)", got, ok)
	}
	selectEntry(t, p, "a.go")
	if got, ok := p.SelectedPath(); !ok || got != file {
		t.Errorf("SelectedPath on a file = (%q, %v), want (%q, true)", got, ok, file)
	}
}

// An empty or absent selection is not a file, and must not panic on the index.
func TestSelectedPathWithNoSelection(t *testing.T) {
	p, _ := selectedFixture(t)
	p.list.Sel = -1
	if got, ok := p.SelectedPath(); ok || got != "" {
		t.Errorf("SelectedPath with sel -1 = (%q, %v), want (\"\", false)", got, ok)
	}
	p.list.Sel = len(p.Tree.Entries())
	if got, ok := p.SelectedPath(); ok || got != "" {
		t.Errorf("SelectedPath past the end = (%q, %v), want (\"\", false)", got, ok)
	}
}
