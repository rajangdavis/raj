package app

import (
	"path/filepath"
	"testing"
)

// explorerFile moves the explorer selection onto a file and returns its path.
// It presses down until SelectedPath reports a file, so the test does not
// depend on the directory/file ordering of the fixture.
func explorerFile(t *testing.T, h *harness) string {
	t.Helper()
	for i := 0; i < 10; i++ {
		if p, ok := h.Explorer.SelectedPath(); ok && h.Tabs.Preview() != nil {
			return p
		}
		h.press("down")
	}
	t.Fatal("no file was previewed in the explorer fixture")
	return ""
}

// Arrowing onto a file must load it as a preview while the keys stay in the
// sidebar. Without previewFile the editor never loads the file; without the
// focus rule, or with OpenFile on arrow, focus jumps out of the tree and the
// user cannot keep scrolling.
func TestExplorerArrowPreviewsWithoutLeavingTheSidebar(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)

	first := explorerFile(t, h)
	if h.Focused() != FocusSidebar {
		t.Fatalf("arrowing moved focus to %v; keys must stay in the tree", h.Focused())
	}
	if h.Pane() == nil || h.Pane().File.Path != first {
		t.Fatalf("active pane = %v, want the previewed %s", h.Pane(), filepath.Base(first))
	}
	if h.Tabs.Count() != 1 {
		t.Fatalf("preview left %d tabs, want 1", h.Tabs.Count())
	}

	// The next file must reuse the preview slot, not add a second tab.
	var second string
	for i := 0; i < 10; i++ {
		h.press("down")
		if p, ok := h.Explorer.SelectedPath(); ok && p != first {
			second = p
			break
		}
	}
	if second == "" {
		t.Fatal("the fixture has no second file")
	}
	if h.Tabs.Count() != 1 {
		t.Fatalf("a second preview left %d tabs, want 1", h.Tabs.Count())
	}
	if h.Pane() == nil || h.Pane().File.Path != second {
		t.Fatalf("preview did not switch to %s", filepath.Base(second))
	}
	if h.Focused() != FocusSidebar {
		t.Errorf("focus left the sidebar while arrowing: %v", h.Focused())
	}
}

// Enter is the commit: it opens the previewed file for real, focuses the editor
// and leaves one ordinary tab. Without the promotion the tab stays marked as a
// preview and the next arrow could overwrite it.
func TestExplorerEnterCommitsThePreview(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	file := explorerFile(t, h)

	h.press("enter")
	if h.Focused() != FocusEditor {
		t.Errorf("enter left focus in %v, want the editor", h.Focused())
	}
	if h.Tabs.Count() != 1 {
		t.Errorf("enter opened %d tabs for one previewed file", h.Tabs.Count())
	}
	if h.Pane() == nil || h.Pane().File.Path != file {
		t.Fatal("the active pane is not the file enter was pressed on")
	}
	if h.Tabs.Preview() != nil {
		t.Error("enter left the tab marked as a preview")
	}
}

// Directories are never previewed: arrowing across the directory rows must
// leave the tab bar untouched. Without the SelectedPath directory rule the app
// would try to load a directory path.
func TestExplorerArrowOverDirectoriesDoesNotPreview(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	for i := 0; i < 10; i++ {
		if _, ok := h.Explorer.SelectedPath(); ok {
			break
		}
		if h.Tabs.Count() != 0 {
			t.Fatalf("a directory selection opened %d tabs", h.Tabs.Count())
		}
		h.press("down")
	}
}
