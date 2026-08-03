package app

// Sidebar is which sidebar pane is showing.
type Sidebar int

const (
	SidebarNone Sidebar = iota
	SidebarExplorer
	SidebarSearch
)

// Breakpoints for how many panes fit. Below Narrow, only one pane shows — the
// one that last had focus — because a 34-column editor beside a 26-column tree
// is worse than either alone.
const (
	NarrowCols = 90
	WideCols   = 140

	sidebarMin = 24
	sidebarMax = 40
)

// Layout is the computed geometry for one frame.
type Layout struct {
	SidebarX, SidebarW int
	EditorX, EditorW   int
	TabY, TopY, Rows   int
	ShowSidebar        bool
	ShowEditor         bool
}

// computeLayout decides the geometry from the terminal size, which sidebar is
// open, and where focus is.
//
// The narrow rule is a real mode change rather than a squeeze: under
// NarrowCols exactly one pane is drawn, and which one follows focus. That makes
// the sidebar chords behave as a switcher on a small window and as a toggle on
// a large one, without either feeling like a special case.
func computeLayout(cols, rows int, side Sidebar, focus Focus) Layout {
	l := Layout{TabY: 0, TopY: 1, Rows: rows - 2} // tab bar above, status below
	if l.Rows < 1 {
		l.Rows = 1
	}

	sidebarOpen := side != SidebarNone
	if !sidebarOpen {
		l.ShowEditor = true
		l.EditorX, l.EditorW = 0, cols
		return l
	}

	if cols < NarrowCols {
		// One pane only. Sidebar focus shows the sidebar; anything else shows
		// the editor, so opening a file from the tree switches the view.
		if focus == FocusSidebar {
			l.ShowSidebar = true
			l.SidebarX, l.SidebarW = 0, cols
		} else {
			l.ShowEditor = true
			l.EditorX, l.EditorW = 0, cols
		}
		return l
	}

	w := cols / 4
	if w < sidebarMin {
		w = sidebarMin
	}
	if w > sidebarMax {
		w = sidebarMax
	}
	l.ShowSidebar, l.ShowEditor = true, true
	l.SidebarX, l.SidebarW = 0, w
	l.EditorX, l.EditorW = w, cols-w
	return l
}

// Focus is which pane receives keys.
type Focus int

const (
	FocusEditor Focus = iota
	FocusSidebar
	FocusPicker
)
