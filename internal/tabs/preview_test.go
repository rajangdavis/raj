package tabs

import (
	"os"
	"path/filepath"
	"testing"
)

// previewFixture builds a tab set over n files and returns the directory, so a
// test can name paths without repeating the temp dir.
func previewFixture(t *testing.T, names ...string) (string, *Tabs) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, New(2)
}

// Arrowing through several files must reuse the one preview slot rather than
// leaving a trail of tabs. Without OpenPreview, or with it appending instead of
// replacing, the count grows with every path.
func TestOpenPreviewReusesOneSlot(t *testing.T) {
	dir, tb := previewFixture(t, "a.go", "b.go", "c.go")
	for _, n := range []string{"a.go", "b.go", "c.go"} {
		p, err := tb.OpenPreview(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.File.Name(); got != n {
			t.Fatalf("previewing %s returned %s", n, got)
		}
	}
	if tb.Count() != 1 {
		t.Fatalf("previewing three files left %d tabs, want 1", tb.Count())
	}
	if got := tb.Active().File.Name(); got != "c.go" {
		t.Errorf("active = %s, want c.go", got)
	}
	if got := tb.Preview().File.Name(); got != "c.go" {
		t.Errorf("preview = %s, want c.go", got)
	}
}

// Opening a path that is already previewed shows that same pane instead of a
// second copy.
func TestOpenPreviewReusesAnOpenTab(t *testing.T) {
	dir, tb := previewFixture(t, "a.go")
	a := filepath.Join(dir, "a.go")
	if _, err := tb.Open(a); err != nil {
		t.Fatal(err)
	}
	p, err := tb.OpenPreview(a)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Count() != 1 {
		t.Fatalf("count = %d, want 1", tb.Count())
	}
	if p != tb.Active() {
		t.Error("the preview did not focus the existing pane")
	}
	if tb.Preview() != nil {
		t.Error("an already-open file must not be marked as the preview")
	}
}

// A preview with unsaved work is promoted, not discarded: the new preview takes
// a second slot and the dirty pane keeps its text. Without the dirty check the
// edit would be silently dropped when the slot is replaced.
func TestDirtyPreviewIsPromotedNotDiscarded(t *testing.T) {
	dir, tb := previewFixture(t, "a.go", "b.go")
	dirty, err := tb.OpenPreview(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	dirty.InsertText("edit")
	if !dirty.File.ViewDirty() {
		t.Fatal("setup: the preview is not dirty")
	}

	if _, err := tb.OpenPreview(filepath.Join(dir, "b.go")); err != nil {
		t.Fatal(err)
	}
	if tb.Count() != 2 {
		t.Fatalf("count = %d, want 2 (the dirty preview plus the new one)", tb.Count())
	}
	if !tb.Contains(dirty) {
		t.Fatal("the dirty preview was discarded")
	}
	if tb.Preview() == dirty {
		t.Error("the dirty pane is still marked as the preview")
	}
	if got := dirty.File.Text(); got != "editx\n" {
		t.Errorf("the promoted pane lost its text: %q", got)
	}
	if got := tb.Preview().File.Name(); got != "b.go" {
		t.Errorf("preview = %s, want b.go", got)
	}
}

// Enter opens a previewed path through the normal path, which must promote it:
// the tab is no longer provisional, and a later preview takes a fresh slot
// rather than overwriting it.
func TestOpenPromotesThePreview(t *testing.T) {
	dir, tb := previewFixture(t, "a.go", "b.go")
	a := filepath.Join(dir, "a.go")
	if _, err := tb.OpenPreview(a); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Open(a); err != nil {
		t.Fatal(err)
	}
	if tb.Preview() != nil {
		t.Error("Open left the pane marked as a preview")
	}
	if tb.Count() != 1 {
		t.Fatalf("Open duplicated the previewed path: count = %d", tb.Count())
	}
	if _, err := tb.OpenPreview(filepath.Join(dir, "b.go")); err != nil {
		t.Fatal(err)
	}
	if tb.Count() != 2 {
		t.Fatalf("a committed tab was overwritten: count = %d, want 2", tb.Count())
	}
}

// Closing the preview forgets it, so a later preview starts a fresh slot rather
// than pointing at a pane that is gone.
func TestCloseClearsThePreview(t *testing.T) {
	dir, tb := previewFixture(t, "a.go")
	if _, err := tb.OpenPreview(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	tb.Close()
	if tb.Preview() != nil {
		t.Error("the closed pane is still the preview")
	}
}

// Paths is what a session restores, so a transient preview must not be in it.
func TestPathsOmitsThePreview(t *testing.T) {
	dir, tb := previewFixture(t, "a.go", "b.go")
	a, b := filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")
	if _, err := tb.Open(a); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.OpenPreview(b); err != nil {
		t.Fatal(err)
	}
	paths := tb.Paths()
	if len(paths) != 1 || paths[0] != a {
		t.Fatalf("Paths = %q, want just the committed tab %q", paths, a)
	}
}
