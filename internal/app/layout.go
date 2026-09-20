package app

import "raj/internal/tabs"

// Sidebar is which sidebar pane is showing.
type Sidebar int

const (
	SidebarNone Sidebar = iota
	SidebarExplorer
	SidebarSearch
	SidebarProblems
	SidebarSettings
)

// Breakpoints for how many panes fit. Below Narrow, only one pane shows — the
// one that last had focus — because a 34-column editor beside a 26-column tree
// is worse than either alone.
const (
	NarrowCols = 90
	WideCols   = 140

	sidebarMin = 24
	sidebarMax = 40

	// sidebarPad is how many rows of breathing room the sidebar content keeps
	// below the tab bar. Only the sidebar is pushed down; the editor keeps the
	// full height, so TopY and Rows are unchanged.
	sidebarPad = 1

	// minSidebarRows is the fewest rows the sidebar may be squeezed to: the
	// explorer's heading, changed-only toggle and footer, plus one tree row.
	// The pad yields before the pane becomes unusable.
	minSidebarRows = 4

	// phoneDrawerRows is the height of the phone profile collapsed action
	// drawer: the whole bottom strip, so the handle is a two-row tap target.
	phoneDrawerRows = 2
)

// Layout is the computed geometry for one frame.
type Layout struct {
	SidebarX, SidebarW      int
	EditorX, EditorW        int
	TabY, TopY, Rows        int
	TabRows                 int
	SidebarTop, SidebarRows int
	ShowSidebar             bool
	ShowEditor              bool
	// BarY is the row the phone profile's review bar occupies, or -1 when the
	// profile has no bar (the ordinary layout).
	BarY int
}

// computeLayout decides the geometry from the terminal size, which sidebar is
// open, and where focus is.
//
// The narrow rule is a real mode change rather than a squeeze: under
// NarrowCols exactly one pane is drawn, and which one follows focus. That makes
// the sidebar chords behave as a switcher on a small window and as a toggle on
// a large one, without either feeling like a special case.
func computeLayout(cols, rows int, side Sidebar, focus Focus) Layout {
	return computeLayoutChrome(cols, rows, side, focus, 1, 1, -1)
}

// layoutFor is the frame geometry for an explicit profile and bottom-row
// choice. The ordinary profile is computeLayout unchanged; the phone profile
// reserves a two-row tab strip, and the bottom row only when bar is set.
func layoutFor(cols, rows int, side Sidebar, focus Focus, phone, bar bool) Layout {
	if !phone {
		return computeLayout(cols, rows, side, focus)
	}
	tabRows := tabs.PhoneStripRows
	if bar {
		return computeLayoutChrome(cols, rows, side, focus, tabRows, phoneDrawerRows, rows-phoneDrawerRows)
	}
	return computeLayoutChrome(cols, rows, side, focus, tabRows, 0, -1)
}

// layout is the frame geometry for the active profile. The phone profile always
// reserves one bottom row for the action drawer's collapsed handle, in Edit and
// Review alike; the expanded panel is drawn over the content rather than
// reserving more. The renderer and the pointer both call this, so they cannot
// disagree about which rows exist.
func (a *App) layout(cols, rows int) Layout {
	return layoutFor(cols, rows, a.sidebar, a.focus, a.phone, a.phone)
}

// computeLayoutChrome reserves the chrome rows above and below the panes, then
// lays out the sidebar and editor exactly as before. tabRows is the tab strip
// height, bottomRows the rows held out at the bottom (the status strip, the
// phone bar, or neither) and barY the phone bar's row, -1 when there is none.
func computeLayoutChrome(cols, rows int, side Sidebar, focus Focus, tabRows, bottomRows, barY int) Layout {
	l := Layout{TabY: 0, TabRows: tabRows, TopY: tabRows, BarY: barY, Rows: rows - tabRows - bottomRows}
	if l.Rows < 1 {
		l.Rows = 1
	}
	// The sidebar is pushed below the tab bar and gives those rows back from
	// its own height, so its content still ends level with the editor's. The
	// inset yields to a floor so a short terminal cannot leave the pane with
	// no room to draw its header and a tree row; when there is no room at all
	// the pre-inset geometry is preserved.
	pad := sidebarPad
	if l.Rows-pad < minSidebarRows {
		pad = l.Rows - minSidebarRows
	}
	if pad < 0 {
		pad = 0
	}
	l.SidebarTop = l.TopY + pad
	l.SidebarRows = l.Rows - pad
	if l.SidebarRows < 1 {
		l.SidebarRows = 1
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
	FocusPrompt
)
