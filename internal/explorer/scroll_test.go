package explorer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/ui"
	"raj/internal/widget"
)

// deepTree builds a nested tree with names long enough to overflow a sidebar,
// fully expanded, and returns the pane.
func deepTree(t *testing.T) *Pane {
	t.Helper()
	root := t.TempDir()
	dir := root
	for _, name := range []string{"application", "components", "rendering"} {
		dir = filepath.Join(dir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []struct{ dir, name string }{
		{root, "a.go"},
		{dir, "an-extremely-long-component-name.go"},
		{dir, "b.go"}, // a sibling short enough to sit inside the same window
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(f.dir, f.name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPane(root)
	for i := 0; i < 8; i++ {
		for _, e := range p.Tree.Entries() {
			if e.Dir && !e.Open {
				p.Tree.Toggle(e.Path)
			}
		}
	}
	return p
}

// render draws the pane and returns the screen.
func render(p *Pane, w, h int) *ui.Screen {
	s := ui.NewScreen(w, h)
	p.Render(s, 0, 0, w, h, widget.DefaultTheme(), true)
	return s
}

// selectEntry puts the selection on the first entry whose name matches.
func selectEntry(t *testing.T, p *Pane, name string) {
	t.Helper()
	for i, e := range p.Tree.Entries() {
		if e.Name == name {
			p.list.Sel = i
			return
		}
	}
	t.Fatalf("%q is not in the tree", name)
}

// frame joins every row, for containment checks.
func frame(s *ui.Screen) string {
	_, rows := s.Size()
	var b strings.Builder
	for y := 0; y < rows; y++ {
		b.WriteString(s.Row(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// The point of the offset: a name too deep and too long to fit is readable
// once it is selected, rather than being permanently cut off.
func TestSelectingADeepRowScrollsToIt(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	s := render(p, 24, 20)

	if p.left == 0 {
		t.Fatal("the offset did not move for a row that cannot fit")
	}
	if !strings.Contains(frame(s), "component-name.go") {
		t.Errorf("the selected name is still not visible:\n%s", frame(s))
	}
}

// The rule that makes it usable: the offset moves the MINIMUM needed. Arrowing
// between rows that both already fit must not move it at all, or the names
// slide sideways while your eye is on them.
func TestMovingBetweenFittingRowsDoesNotScroll(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	render(p, 24, 20)
	scrolled := p.left
	if scrolled == 0 {
		t.Fatal("setup: nothing scrolled")
	}

	// A sibling at the same depth and short enough to sit entirely inside the
	// window the long name opened. Its parent would not do: a shallower row
	// starts left of the window and genuinely is not visible, so moving for it
	// is correct rather than jitter.
	selectEntry(t, p, "b.go")
	render(p, 24, 20)
	if p.left != scrolled {
		t.Errorf("offset %d -> %d; a row already inside the window should not move it",
			scrolled, p.left)
	}
}

// It returns to zero on its own when the selection reaches something shallow
// enough to sit left of the window. For a tree that means the top level, which
// is a rule needing no special case for "go back".
func TestReturningToTheTopLevelResetsTheOffset(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	render(p, 24, 20)
	if p.left == 0 {
		t.Fatal("setup: nothing scrolled")
	}

	selectEntry(t, p, "a.go") // depth 0
	render(p, 24, 20)
	if p.left != 0 {
		t.Errorf("offset = %d, want 0 at the top level", p.left)
	}
}

// A row too long to fit at all is aligned to its own start: a name you can read
// the front of is identifiable, one you can read the back of usually is not.
func TestAnOverlongRowShowsItsStart(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	s := render(p, 14, 20) // narrower than the name alone
	if !strings.Contains(frame(s), "an-extrem") {
		t.Errorf("the start of the name is not visible:\n%s", frame(s))
	}
}

// Rows above and below the selection are scrolled with it, and keep the tail of
// their names rather than being redrawn from column zero as though they were
// shallow — which would make the tree read as a flat list.
func TestOtherRowsScrollTogether(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	s := render(p, 24, 20)

	body := frame(s)
	// "components" is a shallower ancestor; at this offset its start is off to
	// the left, so what remains must be a tail of it rather than the whole.
	if strings.Contains(body, "▾ components") {
		t.Errorf("an ancestor was drawn unscrolled:\n%s", body)
	}
}

// A width too small to draw anything must not panic or produce a negative
// offset, which is the shape a resize down to nothing takes.
func TestTinyWidthIsHarmless(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	for w := 0; w < 6; w++ {
		render(p, w, 20)
		if p.left < 0 {
			t.Fatalf("width %d produced offset %d", w, p.left)
		}
	}
}

// Clipping is by display column, not by byte. The disclosure marker is
// multi-byte, so a byte-based clip would cut it in half and leave a replacement
// character where the offset landed.
func TestClipLeftIsByColumn(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"▾ name", 0, "▾ name"},
		{"▾ name", 1, " name"},
		{"▾ name", 2, "name"},
		{"▾ name", 99, ""},
		{"日本語", 2, "本語"},
	}
	for _, c := range cases {
		if got := clipLeft(c.in, c.n); got != c.want {
			t.Errorf("clipLeft(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// Clicking still resolves to the right row whatever the offset: hit-testing is
// by row, and a horizontal offset must not have quietly become part of it.
func TestClickingIsUnaffectedByTheOffset(t *testing.T) {
	p := deepTree(t)
	selectEntry(t, p, "an-extremely-long-component-name.go")
	render(p, 24, 20)
	if p.left == 0 {
		t.Fatal("setup: nothing scrolled")
	}

	want := p.Tree.Entries()[p.list.Top+1]
	p.ClickAt(headRows+1, 20)
	if got := p.Tree.Entries()[p.list.Sel]; got.Path != want.Path {
		t.Errorf("clicked row 1 selected %q, want %q", got.Name, want.Name)
	}
}
