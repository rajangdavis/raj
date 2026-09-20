package app

import (
	"path/filepath"
	"testing"
)

// Tab on the tree opens the selected file and hands focus to the editor.
// Without the rework Tab cycled the explorer's focus stops and reached the
// editor without opening anything.
func TestTabInExplorerOpensFileAndFocusesEditor(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()
	explorerSelect(t, h, "README.md")
	h.press("tab")
	if h.Focused() != FocusEditor {
		t.Fatalf("focus = %v, want the editor", h.Focused())
	}
	p := h.Tabs.Active()
	if p == nil || filepath.Base(p.File.Path) != "README.md" {
		t.Fatalf("active tab = %v, want README.md", p)
	}
}

// Tab on a file that is already an open tab focuses that tab instead of
// opening a duplicate.
func TestTabOnAnOpenFileFocusesItWithoutDuplicate(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	before := h.Tabs.Count()
	explorerSelect(t, h, "README.md")
	h.press("tab")

	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab opened a duplicate: tabs %d -> %d", before, got)
	}
	p := h.Tabs.Active()
	if p == nil || filepath.Base(p.File.Path) != "README.md" {
		t.Errorf("active tab = %v, want the existing README.md", p)
	}
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor", h.Focused())
	}
}

// Tab on a directory opens it in place and keeps focus in the explorer.
func TestTabOnADirectoryOpensIt(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()
	pkg := filepath.Join(h.Explorer.Tree.Root, "pkg")
	explorerSelect(t, h, "pkg")
	if h.Explorer.Tree.Expanded(pkg) {
		t.Fatal("setup: pkg is already open")
	}
	h.press("tab")
	if !h.Explorer.Tree.Expanded(pkg) {
		t.Error("tab did not open the directory")
	}
	if h.Focused() != FocusSidebar {
		t.Errorf("focus = %v, want the sidebar", h.Focused())
	}
}
