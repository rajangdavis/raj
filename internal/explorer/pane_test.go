package explorer

import (
	"os"
	"path/filepath"
	"testing"

	"raj/internal/keys"
)

// allTree builds a nested fixture: alpha/beta/deep under a root that also holds
// a sibling folder and top-level files, so "everything under alpha" is a real
// subtree and not the whole tree.
func allTree(t *testing.T) (*Pane, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{
		filepath.Join("alpha", "beta", "deep"),
		"gamma",
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join("alpha", "alpha.txt"),
		filepath.Join("alpha", "beta", "beta.txt"),
		filepath.Join("alpha", "beta", "deep", "leaf.txt"),
		filepath.Join("gamma", "g.txt"),
		"top.txt",
	} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewPane(root), root
}

// The gesture is one toggle, not two commands: the first press opens the whole
// subtree under the selection, the second closes it again.
//
// Without the change Pane.Handle has no case for ToggleExpandAll, so the action
// is dropped: alpha stays collapsed and neither beta nor deep is ever expanded.
func TestExpandAllTogglesTheWholeSubtree(t *testing.T) {
	p, root := allTree(t)
	alpha := filepath.Join(root, "alpha")
	deep := filepath.Join(alpha, "beta", "deep")
	selectEntry(t, p, "alpha")

	p.Handle(keys.ToggleExpandAll, "")
	for _, d := range []string{alpha, filepath.Join(alpha, "beta"), deep} {
		if !p.Tree.Expanded(d) {
			t.Fatalf("%s not expanded after the first press", p.Tree.Rel(d))
		}
	}

	p.Handle(keys.ToggleExpandAll, "")
	for _, d := range []string{filepath.Join(alpha, "beta"), deep} {
		if p.Tree.Expanded(d) {
			t.Errorf("%s still expanded after the second press", p.Tree.Rel(d))
		}
	}
	// The selected directory keeps its own open state, so its direct children
	// stay on screen and the gesture reads as folding the subtree under it.
	if !p.Tree.Expanded(alpha) {
		t.Error("the selected directory was closed with its subtree")
	}
}

// A file has no subtree, so the target is the directory it lives in. Without
// that rule the handler would pass the file path to the tree, expand nothing,
// and the folder around the selection would stay shut.
func TestExpandAllOnAFileTargetsItsParent(t *testing.T) {
	p, root := allTree(t)
	beta := filepath.Join(root, "alpha", "beta")
	// Open alpha and beta so beta.txt is a visible row; deep is left closed.
	p.Tree.Expand(beta)
	p.Tree.Refresh()
	selectEntry(t, p, "beta.txt")

	p.Handle(keys.ToggleExpandAll, "")
	if !p.Tree.Expanded(filepath.Join(beta, "deep")) {
		t.Error("selecting a file did not expand its parent's subtree")
	}
}

// Nothing selected is a no-op, not a panic on an out-of-range selection.
//
// This one is a guard rather than a feature test: with no case in Handle it
// passes vacuously. It fails as soon as the handler indexes the selection
// without checking it first.
func TestExpandAllWithNothingSelectedDoesNothing(t *testing.T) {
	p, root := allTree(t)
	alpha := filepath.Join(root, "alpha")
	p.list.Sel = len(p.Tree.Entries()) // past the end: nothing selected
	before := len(p.Tree.Entries())

	p.Handle(keys.ToggleExpandAll, "")

	if p.Tree.Expanded(alpha) {
		t.Error("alpha was expanded with no row selected")
	}
	if got := len(p.Tree.Entries()); got != before {
		t.Errorf("entries changed with no selection: %d -> %d", before, got)
	}
}

// The toggle reads the state of the whole subtree, not the selected
// directory's own flag: when only some descendants are open, the first press
// must expand rather than fold the rest away.
//
// If it decided from alpha's own flag, alpha already being open would make the
// press collapse, and deep would never open.
func TestExpandAllExpandsAPartiallyOpenSubtree(t *testing.T) {
	p, root := allTree(t)
	alpha := filepath.Join(root, "alpha")
	deep := filepath.Join(alpha, "beta", "deep")
	// beta open, deep closed: some of the subtree is already expanded.
	p.Tree.Expand(filepath.Join(alpha, "beta"))
	p.Tree.Refresh()
	selectEntry(t, p, "alpha")

	p.Handle(keys.ToggleExpandAll, "")

	if !p.Tree.Expanded(deep) {
		t.Error("a partially open subtree was not fully expanded")
	}
}

// The row the user is on does not move when the subtree opens or closes, so
// the gesture does not jump them somewhere else. Like the nothing-selected
// test, this guards the new handler rather than the feature: it would pass
// vacuously before the change.
func TestExpandAllKeepsTheSelection(t *testing.T) {
	p, root := allTree(t)
	alpha := filepath.Join(root, "alpha")
	selectEntry(t, p, "alpha")

	p.Handle(keys.ToggleExpandAll, "")
	if got := p.Selected(); got != alpha {
		t.Errorf("selection moved to %q after expanding", p.Tree.Rel(got))
	}
	p.Handle(keys.ToggleExpandAll, "")
	if got := p.Selected(); got != alpha {
		t.Errorf("selection moved to %q after collapsing", p.Tree.Rel(got))
	}
}
