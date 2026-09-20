package explorer

import (
	"os"
	"path/filepath"
	"testing"

	"raj/internal/keys"
)

// navFixture is a collapsed nested tree: root holds a directory with a
// subdirectory and a file, plus a top-level file. Only the root is open, so the
// first Right has something to open.
//
//	root/
//	  pkg/
//	    sub/
//	      leaf.go
//	  mid.go
//	  top.go
func navFixture(t *testing.T) (*Pane, string, string, string) {
	t.Helper()
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(sub, "leaf.go")
	if err := os.WriteFile(leaf, []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mid.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(root, "top.go")
	if err := os.WriteFile(top, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewPane(root), filepath.Join(root, "pkg"), filepath.Join(root, "pkg", "sub"), top
}

func (p *Pane) selectedName(t *testing.T) string {
	t.Helper()
	entries := p.Tree.Entries()
	if p.list.Sel < 0 || p.list.Sel >= len(entries) {
		return ""
	}
	return entries[p.list.Sel].Name
}

// Up and Down move the selection one entry and clamp at both ends rather than
// wrapping. Without the split from Left/Right the arrows would still move, but
// this pins the clamp the rework keeps.
func TestUpDownMoveAndClamp(t *testing.T) {
	p, _, _, _ := navFixture(t)
	n := len(p.Tree.Entries())
	if n < 3 {
		t.Fatalf("fixture has %d entries, want at least 3", n)
	}
	p.list.Sel = 0
	if _, exit := p.Handle(keys.LineUp, ""); exit {
		t.Error("up left the sidebar")
	}
	if p.list.Sel != 0 {
		t.Errorf("up at the top moved to %d, want 0", p.list.Sel)
	}
	p.Handle(keys.LineDown, "")
	if p.list.Sel != 1 {
		t.Fatalf("down = %d, want 1", p.list.Sel)
	}
	for i := 0; i < n+2; i++ {
		p.Handle(keys.LineDown, "")
	}
	if p.list.Sel != n-1 {
		t.Errorf("down past the end = %d, want the last entry %d", p.list.Sel, n-1)
	}
	for i := 0; i < n+2; i++ {
		p.Handle(keys.LineUp, "")
	}
	if p.list.Sel != 0 {
		t.Errorf("up past the top = %d, want 0", p.list.Sel)
	}
}

// Right opens a collapsed directory and, a second time, steps into its first
// child. Without the new mapping Right was a movement alias and the directory
// would never open.
func TestRightOpensThenEntersFirstChild(t *testing.T) {
	p, pkg, _, _ := navFixture(t)
	p.list.Sel = 0 // pkg
	if open, exit := p.Handle(keys.CharRight, ""); open != "" || exit {
		t.Fatalf("right on a collapsed directory = (%q,%v), want no result", open, exit)
	}
	if !p.Tree.Expanded(pkg) {
		t.Fatal("right did not open the directory")
	}
	entries := p.Tree.Entries()
	if len(entries) < 2 || entries[1].Path != filepath.Join(pkg, "sub") {
		t.Fatalf("first child = %+v, want pkg/sub", entries)
	}
	p.Handle(keys.CharRight, "")
	if p.list.Sel != 1 {
		t.Errorf("second right selected %d, want the first child 1", p.list.Sel)
	}
	if got := p.selectedName(t); got != "sub" {
		t.Errorf("second right selected %q, want sub", got)
	}
}

// Right on a file does nothing: only a directory has an open/close meaning.
func TestRightOnAFileDoesNothing(t *testing.T) {
	p, _, _, top := navFixture(t)
	entries := p.Tree.Entries()
	for i, e := range entries {
		if e.Path == top {
			p.list.Sel = i
		}
	}
	before := p.list.Sel
	p.Handle(keys.CharRight, "")
	if p.list.Sel != before {
		t.Errorf("right on a file moved the selection %d -> %d", before, p.list.Sel)
	}
}

// Left collapses an open directory and, on a collapsed one, selects its parent.
// Without the collapse/parent split Left was a movement alias.
func TestLeftCollapsesAndSelectsParent(t *testing.T) {
	p, pkg, sub, _ := navFixture(t)
	p.list.Sel = 0
	p.Handle(keys.CharRight, "") // open pkg
	p.Handle(keys.CharRight, "") // enter pkg/sub, collapsed
	if got := p.selectedName(t); got != "sub" {
		t.Fatalf("setup: selected %q, want sub", got)
	}

	// Left on the collapsed sub steps up to its parent pkg.
	p.Handle(keys.CharLeft, "")
	if got := p.selectedName(t); got != "pkg" {
		t.Fatalf("left on a collapsed directory selected %q, want pkg", got)
	}
	if !p.Tree.Expanded(pkg) {
		t.Fatal("left on the child closed the parent")
	}

	// Left on open pkg collapses it and keeps the selection on pkg.
	p.Handle(keys.CharLeft, "")
	if p.Tree.Expanded(pkg) {
		t.Error("left on an open directory did not collapse it")
	}
	if got := p.selectedName(t); got != "pkg" {
		t.Errorf("collapsing moved the selection to %q, want pkg", got)
	}

	// Left on the now-collapsed top-level pkg has no parent: a no-op.
	p.Handle(keys.CharLeft, "")
	if got := p.selectedName(t); got != "pkg" {
		t.Errorf("left at the top level moved the selection to %q", got)
	}

	// Left on a file steps up to the directory holding it.
	p.Handle(keys.CharRight, "") // reopen pkg
	entries := p.Tree.Entries()
	for i, e := range entries {
		if e.Path == sub {
			p.list.Sel = i
		}
	}
	p.Handle(keys.CharRight, "") // open sub
	for i, e := range p.Tree.Entries() {
		if e.Name == "leaf.go" {
			p.list.Sel = i
		}
	}
	p.Handle(keys.CharLeft, "")
	if got := p.selectedName(t); got != "sub" {
		t.Errorf("left on a file selected %q, want its parent sub", got)
	}
}

// Tab on a file returns its path and leaves for the editor; Tab on a directory
// opens it. Without the rework Tab cycled the focus stops and never opened
// anything.
func TestTabOpensFileAndDirectory(t *testing.T) {
	p, pkg, _, top := navFixture(t)
	p.list.Sel = 0 // pkg
	if open, exit := p.Handle(keys.CycleFocus, ""); open != "" || exit {
		t.Fatalf("tab on a directory = (%q,%v), want no result", open, exit)
	}
	if !p.Tree.Expanded(pkg) {
		t.Fatal("tab on a directory did not open it")
	}
	// A second tab on the open directory is a no-op: only opening is asked
	// for, so it must not close it.
	p.Handle(keys.CycleFocus, "")
	if !p.Tree.Expanded(pkg) {
		t.Error("tab closed an already open directory")
	}

	for i, e := range p.Tree.Entries() {
		if e.Path == top {
			p.list.Sel = i
		}
	}
	open, exit := p.Handle(keys.CycleFocus, "")
	if open != top || !exit {
		t.Errorf("tab on a file = (%q,%v), want (%q,true)", open, exit, top)
	}
}

// Tab on the changed-only toggle steps down to the tree; it is the one part of
// the old focus ring the rework keeps.
func TestTabOnTheToggleStepsToTheTree(t *testing.T) {
	p, _, _, _ := navFixture(t)
	p.spot = spotFilter
	if _, exit := p.Handle(keys.CycleFocus, ""); exit {
		t.Error("tab on the toggle left the sidebar")
	}
	if p.spot != spotTree {
		t.Errorf("spot = %d, want the tree", p.spot)
	}
}

// Enter keeps its activate behaviour: a directory toggles, a file reports its
// path without leaving focus to the pane.
func TestEnterStillActivates(t *testing.T) {
	p, pkg, _, top := navFixture(t)
	p.list.Sel = 0
	if open, exit := p.Handle(keys.Confirm, ""); open != "" || exit {
		t.Fatalf("enter on a directory = (%q,%v), want no result", open, exit)
	}
	if !p.Tree.Expanded(pkg) {
		t.Fatal("enter did not open the directory")
	}
	p.Handle(keys.Confirm, "")
	if p.Tree.Expanded(pkg) {
		t.Error("enter on an open directory did not close it")
	}
	for i, e := range p.Tree.Entries() {
		if e.Path == top {
			p.list.Sel = i
		}
	}
	open, exit := p.Handle(keys.Confirm, "")
	if open != top || exit {
		t.Errorf("enter on a file = (%q,%v), want (%q,false)", open, exit, top)
	}
}

// The pointer hit-test is unchanged: ClickAt still toggles a directory and
// returns a file path, because the key rework did not touch activate.
func TestClickAtStillActivates(t *testing.T) {
	p, pkg, _, top := navFixture(t)
	if _, ok := p.ClickAt(2, 20); !ok {
		t.Fatal("a click on the first row did not land")
	}
	if !p.Tree.Expanded(pkg) {
		t.Fatal("a click on the directory did not open it")
	}
	idx := -1
	for i, e := range p.Tree.Entries() {
		if e.Path == top {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("top.go is not a row")
	}
	open, ok := p.ClickAt(2+idx, 20)
	if !ok || open != top {
		t.Errorf("a click on a file = (%q,%v), want (%q,true)", open, ok, top)
	}
}
