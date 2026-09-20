package explorer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tallTree builds a flat root with n files, for a list long enough to scroll.
func tallTree(t *testing.T, n int) *Pane {
	t.Helper()
	root := t.TempDir()
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("f%02d.go", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewPane(root)
}

// On a touch surface one entry is two screen rows: the label on the first and
// padding on the second, so the next entry does not start until the row after
// it. Without the doubled row height the neighbour would be drawn on the second
// row and the block would not exist.
func TestTallEntryOccupiesTwoRows(t *testing.T) {
	p, _ := selectedFixture(t)
	p.Tall = true
	s := render(p, 24, 20)
	if got := p.rowHeight(); got != 2 {
		t.Fatalf("rowHeight = %d, want 2", got)
	}
	if !strings.Contains(s.Row(2), "pkg") {
		t.Fatalf("row 2 does not hold the first entry: %q", s.Row(2))
	}
	if strings.Contains(s.Row(3), "a.go") {
		t.Errorf("row 3 holds the next entry; the first block is not two rows: %q", s.Row(3))
	}
	if !strings.Contains(s.Row(4), "a.go") {
		t.Errorf("row 4 does not hold the second entry: %q", s.Row(4))
	}
}

// Both rows of a block resolve to the same entry, and the first row of the next
// block is the neighbour rather than a second row of this one. Without the
// division the second row would resolve to the next entry.
func TestTallRowAtEitherRowOfTheBlock(t *testing.T) {
	p, _ := selectedFixture(t)
	p.Tall = true
	for _, dy := range []int{2, 3} {
		i, e, ok := p.RowAt(dy, 20)
		if !ok || i != 0 || e.Name != "pkg" {
			t.Errorf("RowAt(%d) = (%d, %q, %v), want entry 0 pkg", dy, i, e.Name, ok)
		}
	}
	i, e, ok := p.RowAt(4, 20)
	if !ok || i != 1 || e.Name != "a.go" {
		t.Errorf("RowAt(4) = (%d, %q, %v), want entry 1 a.go", i, e.Name, ok)
	}
}

// A tap on either row of a file block opens that file, and a tap on either row
// of a directory block selects the directory. Without the tall hit test the
// lower row would fall on the next entry, or past the end.
func TestTallClickUsesTheWholeBlock(t *testing.T) {
	p, file := selectedFixture(t)
	p.Tall = true
	for _, dy := range []int{4, 5} {
		open, ok := p.ClickAt(dy, 20)
		if !ok || open != file {
			t.Errorf("ClickAt(%d) = (%q, %v), want %q", dy, open, ok, file)
		}
	}
	// The directory block's second row selects it (and toggles it closed, so
	// this is checked last: the file row disappears after it).
	if _, ok := p.ClickAt(3, 20); !ok {
		t.Error("a tap on the directory block's second row did nothing")
	}
	if got := p.Selected(); got == "" || filepath.Base(got) != "pkg" {
		t.Errorf("selection = %q, want the pkg directory", got)
	}
}

// The list counts entries, not screen rows: a pane that fits three tall blocks
// shows three, and arrowing onto the sixth scrolls the top to three. Without
// the divided Settle the list thinks six entries fit and leaves Top at zero.
func TestTallScrollUsesItemRows(t *testing.T) {
	p := tallTree(t, 8)
	p.Tall = true
	selectEntry(t, p, "f06.go")
	render(p, 24, 9) // treeRows(9)=6 screen rows -> three tall entries
	if got := p.list.Rows; got != 3 {
		t.Fatalf("visible entries = %d, want 3", got)
	}
	if got := p.list.Top; got != 3 {
		t.Fatalf("Top = %d, want 3 so the sixth entry is on screen", got)
	}
	p.Scroll(1)
	render(p, 24, 9)
	if got := p.list.Top; got != 4 {
		t.Errorf("Top after a scroll = %d, want 4", got)
	}
}

// The ordinary profile is untouched: one entry per row, and the row the tapper
// sees is the entry the hit test resolves.
func TestNormalProfileStaysOneRow(t *testing.T) {
	p, _ := selectedFixture(t)
	s := render(p, 24, 20)
	if got := p.rowHeight(); got != 1 {
		t.Fatalf("rowHeight = %d, want 1 off the phone", got)
	}
	if strings.Contains(s.Row(2), "a.go") {
		t.Errorf("row 2 holds a later entry on the one-row profile: %q", s.Row(2))
	}
	if !strings.Contains(s.Row(3), "a.go") {
		t.Errorf("row 3 does not hold the second entry: %q", s.Row(3))
	}
}

// The header and footer rows are not entries at any height.
func TestTallRowAtRejectsTheChrome(t *testing.T) {
	p, _ := selectedFixture(t)
	p.Tall = true
	for _, dy := range []int{0, 1, 19} {
		if _, _, ok := p.RowAt(dy, 20); ok {
			t.Errorf("RowAt(%d) resolved the pane chrome to an entry", dy)
		}
	}
}
