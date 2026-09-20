package tabs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/ui"
	"raj/internal/widget"
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

// SetTabWidth names an explicit width, and a file opened afterwards takes it
// even though its own content detects a different one; before that the
// detection is the width, which is what a default should do. A non-positive
// width is refused rather than pinned.
func TestSetTabWidthOutranksDetectionOnOpen(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte("    alpha\n"), 0o644)
	os.WriteFile(b, []byte("    beta\n"), 0o644)

	// A default width is only a fallback: the file's own four spaces win,
	// while the tab is drawn at the configured two.
	plain := New(2)
	q, err := plain.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	if q.File.Indent.Width != 4 || q.File.Cols.Tab != 2 {
		t.Errorf("default open = indent %d cols %d, want 4/2", q.File.Indent.Width, q.File.Cols.Tab)
	}

	pinned := New(2)
	if pinned.TabWidth() != 2 {
		t.Fatalf("New width = %d, want 2", pinned.TabWidth())
	}
	pinned.SetTabWidth(8)
	if pinned.TabWidth() != 8 || !pinned.TabWidthPinned() {
		t.Fatalf("after SetTabWidth: width %d pinned %v, want 8/true",
			pinned.TabWidth(), pinned.TabWidthPinned())
	}
	p, err := pinned.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.File.Indent.Width != 8 || p.File.Cols.Tab != 8 {
		t.Errorf("pinned open = indent %d cols %d, want 8/8", p.File.Indent.Width, p.File.Cols.Tab)
	}
	if got := len(p.File.Indent.Unit()); got != 8 {
		t.Errorf("indent unit = %d spaces, want 8", got)
	}

	pinned.SetTabWidth(0)
	if pinned.TabWidth() != 8 {
		t.Errorf("SetTabWidth(0) changed width to %d, want the guard to keep 8", pinned.TabWidth())
	}
}

// The phone strip is two rows of touch-sized chips: every chip is at least
// phoneChipW wide and no narrower than its label. The ordinary one-row layout
// gives spans of exactly len(label), below the touch minimum, and reports one
// strip row.
func TestPhoneStripIsTouchSized(t *testing.T) {
	tb := open(t, "a.go", "b.go", "c.go")
	tb.SetPhone(true)
	if got := tb.StripRows(); got != PhoneStripRows {
		t.Fatalf("StripRows = %d, want %d", got, PhoneStripRows)
	}
	if got := New(2).StripRows(); got != 1 {
		t.Errorf("ordinary StripRows = %d, want 1", got)
	}
	labels, spans := tb.layout(200)
	for i, sp := range spans {
		if w := sp.End - sp.Start; w < phoneChipW {
			t.Errorf("chip %d is %d columns, below the touch minimum %d", i, w, phoneChipW)
		} else if w < len(labels[i]) {
			t.Errorf("chip %d is %d columns, narrower than its label %q", i, w, labels[i])
		}
	}
}

// A phone chip is filled on every strip row and its label sits on the middle
// row, horizontally centred; hit-testing any column of the chip returns it, and
// the caller row gate is StripRows tall, so every chip row is a tap target.
// The ordinary bar is one row, so this fails without the taller phone strip.
func TestPhoneChipCentresLabelAndFillsEveryRow(t *testing.T) {
	tb := open(t, "a.go", "b.go")
	tb.SetPhone(true)
	tb.Goto(1)
	const w = 60
	s := ui.NewScreen(w, PhoneStripRows)
	tb.Render(s, 0, 0, w, widget.DefaultTheme())
	labels, spans := tb.layout(w)
	if len(spans) == 0 {
		t.Fatal("fixture has no chips to check")
	}
	name := strings.TrimSpace(labels[0])
	sp := spans[0]
	if sp.End-sp.Start < phoneChipW {
		t.Fatalf("chip span = %d..%d, narrower than %d", sp.Start, sp.End, phoneChipW)
	}
	// Read the cells, not a trimmed row string: an all-space chip row trims to
	// "" and a fixed span slice over it overruns.
	mid := PhoneStripRows / 2
	first, last := -1, -1
	for col := sp.Start; col < sp.End; col++ {
		if s.At(col, mid).Rune != ' ' {
			if first < 0 {
				first = col
			}
			last = col
		}
	}
	if first < 0 {
		t.Fatalf("middle row carries no label: %q", s.Row(mid))
	}
	var got []rune
	for col := first; col <= last; col++ {
		got = append(got, s.At(col, mid).Rune)
	}
	if string(got) != name {
		t.Errorf("middle row label = %q, want %q", string(got), name)
	}
	left := first - sp.Start
	right := sp.End - 1 - last
	if left != right {
		t.Errorf("label pad left=%d right=%d, want centred", left, right)
	}
	// Every row of the chip carries the chip style, and only the middle row
	// carries the label.
	chipStyle := s.At(sp.Start, mid).Style
	for row := range PhoneStripRows {
		for col := sp.Start; col < sp.End; col++ {
			c := s.At(col, row)
			if c.Style != chipStyle {
				t.Fatalf("chip cell (%d,%d) style = %v, want the chip style %v", col, row, c.Style, chipStyle)
			}
			if row != mid && c.Rune != ' ' {
				t.Errorf("row %d col %d carries %q; the label belongs on the middle row", row, col, c.Rune)
			}
		}
	}
	for col := sp.Start; col < sp.End; col++ {
		if got, ok := tb.HitTest(0, w, col); !ok || got != 0 {
			t.Fatalf("column %d hit (%d, %v), want tab 0", col, got, ok)
		}
	}
}

// An overflowing phone strip scrolls in columns: activating the last tab pulls
// it on screen, and a scroll back moves every span by the same amount. Both
// the reveal and the scroll are the phone layout's; the ordinary layout has no
// offset to move.
func TestPhoneStripScrollsAndRevealsActive(t *testing.T) {
	tb := open(t, "a.go", "b.go", "c.go", "d.go", "e.go")
	tb.SetPhone(true)
	const w = 24
	tb.Goto(tb.Count()) // the last tab, forcing the strip to reveal it
	_, before := tb.layout(w)
	last := before[tb.Count()-1]
	if last.Start < 0 || last.End > w {
		t.Fatalf("active chip span = %+v, want it within [0,%d)", last, w)
	}
	// Hit-testing the chip's own columns returns that tab, because the renderer
	// and the pointer share layout.
	for col := last.Start; col < last.End; col++ {
		if got, ok := tb.HitTest(0, w, col); !ok || got != tb.Count()-1 {
			t.Fatalf("column %d of the active chip hit (%d, %v), want tab %d", col, got, ok, tb.Count()-1)
		}
	}
	tb.ScrollTabs(-4)
	_, after := tb.layout(w)
	if after[0].Start != before[0].Start+4 {
		t.Errorf("scrolling back 4 moved the strip %d columns, want 4", after[0].Start-before[0].Start)
	}
}
