package explorer

import (
	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Pane is the file tree sidebar.
//
// Tab moves forward through the pane's components and, at the end, leaves for
// the editor. Shift+tab moves back but stops at the first component: once focus
// has crossed into the editor, tab is indentation and cannot bring you back.
// Returning is deliberately a chord (shift+cmd+e), so that editing is never one
// stray keypress away from being interrupted.
type Pane struct {
	Tree    *Tree
	list    widget.List
	spot    int // 0 = the changed-files toggle, 1 = the tree
	visited bool
}

// The focus stops, in the order they are drawn. The toggle used to be last and
// at the bottom of the pane, which made it reachable only by tabbing through
// the whole tree; putting it under the heading means tab order and reading
// order are the same thing, and cmd+up/down jump between the two directly.
const (
	spotFilter = iota
	spotTree
	spotCount
)

// NewPane opens the tree at root, focused on the tree rather than the toggle:
// the toggle is drawn first now, but it is not what you came to the pane for.
func NewPane(root string) *Pane { return &Pane{Tree: NewTree(root), spot: spotTree} }

// Focus restores the pane to wherever focus was when it was last left, so
// returning after opening a file does not lose your place in the tree.
func (p *Pane) Focus() {
	if !p.visited {
		p.spot = spotTree
		p.visited = true
	}
}

// Handle applies an action. Exit reports that focus should leave for the
// editor; open is a file to open, empty when nothing was chosen.
func (p *Pane) Handle(a keys.Action, text string) (open string, exit bool) {
	switch a {
	// cmd+up/down jump between the toggle and the tree, the same shortcut the
	// search pane uses to get between its query and its results. They are
	// claimed before anything else, because a list would read them as "first
	// entry" and "last entry" and the jump would do nothing where it is most
	// useful.
	case keys.DocStart:
		p.spot = spotFilter
	case keys.DocEnd:
		p.spot = spotTree
	case keys.CycleFocus:
		if p.spot+1 >= spotCount {
			return "", true
		}
		p.spot++
	case keys.CycleFocusBack:
		if p.spot > 0 {
			p.spot--
		}
	case keys.LineUp, keys.CharLeft:
		if p.spot == spotTree {
			p.list.Move(-1, len(p.Tree.Entries()))
		}
	case keys.LineDown, keys.CharRight:
		if p.spot == spotTree {
			p.list.Move(+1, len(p.Tree.Entries()))
		}
	case keys.Confirm:
		return p.activate()
	case keys.None:
		if text == " " && p.spot == spotFilter {
			p.toggleFilter()
		}
	}
	return "", false
}

// activate opens a file, or expands a directory.
func (p *Pane) activate() (string, bool) {
	if p.spot == spotFilter {
		p.toggleFilter()
		return "", false
	}
	entries := p.Tree.Entries()
	if p.list.Sel >= len(entries) {
		return "", false
	}
	e := entries[p.list.Sel]
	if e.Dir {
		p.Tree.Toggle(e.Path)
		return "", false
	}
	return e.Path, false
}

func (p *Pane) toggleFilter() {
	p.Tree.ChangedOnly = !p.Tree.ChangedOnly
	p.Tree.Refresh()
	p.list.Reset()
}

// Selected is the highlighted entry's path, empty when the tree is empty.
func (p *Pane) Selected() string {
	entries := p.Tree.Entries()
	if p.list.Sel < len(entries) {
		return entries[p.list.Sel].Path
	}
	return ""
}

// Render draws the pane. focused dims the whole thing when the editor has
// focus, so it is obvious where keystrokes are going.
func (p *Pane) Render(s *ui.Screen, x, y, w, h int, th widget.Theme, focused bool) {
	if w < 4 || h < 3 {
		return
	}
	s.Fill(x, y, w, 1, ui.DefaultStyle)
	title := " EXPLORER "
	if p.Tree.ChangedOnly {
		title = " EXPLORER — CHANGED "
	}
	s.SetString(x+1, y, widget.Truncate(title, w-2), th.Heading(focused && p.spot == spotTree), w-2)

	p.renderFilter(s, x, y+1, w, th, focused)
	p.renderPath(s, x, y+h-1, w, th)

	rows := h - 3
	p.list.Rows = rows
	p.list.Follow(len(p.Tree.Entries()))
	entries := p.Tree.Entries()

	for row := 0; row < rows; row++ {
		i := p.list.Top + row
		if i >= len(entries) {
			break
		}
		e := entries[i]
		style := th.Focus(i == p.list.Sel, focused && p.spot == spotTree)
		indent := 1 + e.Depth*2
		marker := "  "
		if e.Dir {
			marker = "▸ "
			if e.Open {
				marker = "▾ "
			}
		}
		label := marker + e.Name
		s.Fill(x, y+2+row, w, 1, ui.DefaultStyle)
		s.SetString(x+indent, y+2+row, widget.Truncate(label, w-indent-1), style, w-indent-1)
	}
}

// renderFilter draws the changed-files toggle as the pane's second focus stop.
func (p *Pane) renderFilter(s *ui.Screen, x, y, w int, th widget.Theme, focused bool) {
	box, style := "[ ]", th.Dim
	if p.Tree.ChangedOnly {
		box, style = "[x]", th.Text.Plus(ui.Bold)
	}
	if focused && p.spot == spotFilter {
		style = th.Selected
	}
	s.Fill(x, y, w, 1, ui.DefaultStyle)
	s.SetString(x+1, y, widget.Truncate(box+" changed only", w-2), style, w-2)
}

// renderPath spells out the selected entry on the last row. Rows are indented
// and then truncated, so past three or four levels the name is cut before it
// says anything and two files with the same tail look identical. The path is
// truncated from the LEFT, because the end of a path is what disambiguates it.
func (p *Pane) renderPath(s *ui.Screen, x, y, w int, th widget.Theme) {
	s.Fill(x, y, w, 1, ui.DefaultStyle)
	rel := p.Tree.Rel(p.Selected())
	if rel == "" {
		return
	}
	if avail := w - 2; len([]rune(rel)) > avail && avail > 1 {
		r := []rune(rel)
		rel = "…" + string(r[len(r)-(avail-1):])
	}
	s.SetString(x+1, y, rel, th.Dim, w-2)
}
