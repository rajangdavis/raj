package tabs

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, names ...string) (string, *Tabs) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644)
	}
	return dir, New(2)
}

func TestOpenReusesExistingTab(t *testing.T) {
	dir, tb := fixture(t, "a.go", "b.go")
	tb.Open(filepath.Join(dir, "a.go"))
	tb.Open(filepath.Join(dir, "b.go"))
	tb.Open(filepath.Join(dir, "a.go"))
	if tb.Count() != 2 {
		t.Fatalf("count = %d, want 2", tb.Count())
	}
	if tb.Index() != 0 {
		t.Errorf("reopening a.go should focus its existing tab, got index %d", tb.Index())
	}
}

// Closing the last tab must leave the set empty rather than quitting anything.
func TestCloseLastLeavesEmpty(t *testing.T) {
	dir, tb := fixture(t, "a.go")
	tb.Open(filepath.Join(dir, "a.go"))
	tb.Close()
	if tb.Count() != 0 || tb.Active() != nil {
		t.Errorf("count = %d active = %v", tb.Count(), tb.Active())
	}
}

func TestReopenRestoresMostRecent(t *testing.T) {
	dir, tb := fixture(t, "a.go", "b.go")
	tb.Open(filepath.Join(dir, "a.go"))
	tb.Open(filepath.Join(dir, "b.go"))
	tb.Close() // closes b
	tb.Close() // closes a
	reopen(t, tb)
	if got := tb.Active().File.Name(); got != "a.go" {
		t.Errorf("reopened %s, want a.go (most recently closed)", got)
	}
	reopen(t, tb)
	if got := tb.Active().File.Name(); got != "b.go" {
		t.Errorf("second reopen gave %s, want b.go", got)
	}
}

func TestCycleWraps(t *testing.T) {
	dir, tb := fixture(t, "a.go", "b.go", "c.go")
	for _, n := range []string{"a.go", "b.go", "c.go"} {
		tb.Open(filepath.Join(dir, n))
	}
	tb.Next()
	if tb.Index() != 0 {
		t.Errorf("next from the last tab should wrap to 0, got %d", tb.Index())
	}
	tb.Prev()
	if tb.Index() != 2 {
		t.Errorf("prev from the first should wrap to the last, got %d", tb.Index())
	}
}

// An out-of-range jump does nothing rather than clamping: cmd+7 with three
// tabs open should not land on the third.
func TestGotoIgnoresOutOfRange(t *testing.T) {
	dir, tb := fixture(t, "a.go", "b.go")
	tb.Open(filepath.Join(dir, "a.go"))
	tb.Open(filepath.Join(dir, "b.go"))
	tb.Goto(1)
	tb.Goto(7)
	if tb.Index() != 0 {
		t.Errorf("index = %d, want 0", tb.Index())
	}
}

// Opening a path that does not exist yet creates an empty buffer for it.
func TestOpenMissingFile(t *testing.T) {
	dir, tb := fixture(t)
	p, err := tb.Open(filepath.Join(dir, "new.go"))
	if err != nil {
		t.Fatalf("opening a missing file: %v", err)
	}
	if p.File.Len() != 0 {
		t.Errorf("expected an empty buffer, got %d bytes", p.File.Len())
	}
}

// reopen mirrors what the application does: pop the closed path and open it
// through the ordinary path, so a reopened tab is treated like any other.
func reopen(t *testing.T, tb *Tabs) {
	t.Helper()
	path, ok := tb.PopClosed()
	if !ok {
		t.Fatal("nothing to reopen")
	}
	if _, err := tb.Open(path); err != nil {
		t.Fatal(err)
	}
}

// Files sharing a base name must be distinguishable in the bar; three tabs all
// reading "main.go" is worse than no labels.
func TestLabelsDisambiguate(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"alpha", "beta"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
		os.WriteFile(filepath.Join(dir, sub, "main.go"), []byte("x\n"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "solo.go"), []byte("x\n"), 0o644)

	tb := New(2)
	tb.Open(filepath.Join(dir, "alpha", "main.go"))
	tb.Open(filepath.Join(dir, "beta", "main.go"))
	tb.Open(filepath.Join(dir, "solo.go"))

	got := tb.labels()
	want := []string{" alpha/main.go ", " beta/main.go ", " solo.go "}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("label %d = %q, want %q", i, got[i], want[i])
		}
	}
}
