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
	Tree *Tree
	list widget.List
	// left is the horizontal offset, in columns. See follow: it moves only
	// when the selected row does not fit, which is what keeps the tree from
	// sliding sideways every time the selection changes depth.
	left    int
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
		// Symmetric with tab walking off the last component: the pane is a
		// segment of the ring with an exit at each end, and both lead to the
		// editor. Wrapping round to the results instead would make backwards
		// mean something different from forwards, and land focus on the far
		// end of the pane rather than out of it.
		//
		// The original reason for stopping here does not apply to leaving. It
		// was that tab indents in the editor, so a one-key route back IN would
		// make editing interruptible — and that is untouched, since shift+tab
		// outdents once focus is in the document.
		if p.spot == 0 {
			return "", true
		}
		p.spot--
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
// List exposes the scroll state, so a caller can assert on where the view is.
func (p *Pane) List() *widget.List { return &p.list }

// Scroll moves the view without moving the selection, for the wheel.
func (p *Pane) Scroll(delta int) { p.list.Scroll(delta, len(p.Tree.Entries())) }

func (p *Pane) Selected() string {
	entries := p.Tree.Entries()
	if p.list.Sel < len(entries) {
		return entries[p.list.Sel].Path
	}
	return ""
}

// Rows above and below the tree: the heading, the changed-only toggle, and the
// selected-path line at the bottom. Render and ClickAt both measure from these
// rather than from literals, so the tree cannot be drawn at one offset and hit
// tested at another.
const (
	headRows = 2 // heading, filter toggle
	footRows = 1 // the selected path
)

// treeRows is how many entries fit in a pane h rows tall.
func treeRows(h int) int { return h - headRows - footRows }

// ClickAt handles a press at (dy) rows below the pane's origin, in a pane h
// rows tall. It returns a file to open, and reports whether the press landed on
// anything at all.
//
// A directory toggles rather than opening, which is what enter does on one; a
// file opens on the first click rather than the second, because a tree in a
// sidebar is a list of destinations and requiring a double click would make the
// pointer slower than the arrow keys it is meant to save.
func (p *Pane) ClickAt(dy, h int) (open string, ok bool) {
	if dy < 0 || dy >= h {
		return "", false
	}
	if dy == 1 {
		p.spot = spotFilter
		p.toggleFilter()
		return "", true
	}
	rows := treeRows(h)
	row := dy - headRows
	if row < 0 || row >= rows {
		return "", false // the heading or the path line
	}
	entries := p.Tree.Entries()
	i := p.list.Top + row
	if i >= len(entries) {
		return "", false // empty space below the last entry
	}
	p.spot = spotTree
	p.list.Sel = i
	return p.activate()
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

	rows := treeRows(h)
	p.list.Settle(rows, len(p.Tree.Entries()))
	entries := p.Tree.Entries()
	p.follow(w)

	for row := 0; row < rows; row++ {
		i := p.list.Top + row
		if i >= len(entries) {
			break
		}
		e := entries[i]
		style := th.Focus(i == p.list.Sel, focused && p.spot == spotTree)
		at, label := indentOf(e)-p.left, labelOf(e)
		s.Fill(x, y+2+row, w, 1, ui.DefaultStyle)
		if at < 0 {
			// Scrolled past this row's start: drop the columns that are off to
			// the left, so a deep row keeps the tail of its name rather than
			// being drawn from column zero as though it were shallow.
			label, at = clipLeft(label, -at), 0
		}
		if at >= w-1 || label == "" {
			continue
		}
		s.SetString(x+at, y+2+row, widget.Truncate(label, w-at-1), style, w-at-1)
	}
}

// indentOf is the column a row's marker starts at, before any offset.
func indentOf(e Entry) int { return 1 + e.Depth*2 }

// labelOf is the disclosure marker and the name.
func labelOf(e Entry) string {
	marker := "  "
	if e.Dir {
		marker = "▸ "
		if e.Open {
			marker = "▾ "
		}
	}
	return marker + e.Name
}

// follow moves the horizontal offset the minimum needed to show the selected
// row, and otherwise leaves it alone.
//
// "The minimum needed" is the whole rule, and it is what the TODO warned about:
// an offset that recentres on the selection makes the tree slide sideways every
// time you arrow between rows of different depth, which is far worse than not
// scrolling at all — the names you are reading move while your eye is on them.
//
// So the offset is a window that the selected row has to be inside, exactly
// like the document's own viewport. Arrowing between rows that both fit changes
// nothing. It returns to zero on its own when the selection reaches something
// shallow enough to sit left of the window, which for a tree means the top
// level — a rule that needs no special case for "go back".
//
// Showing the start of a name beats showing the end: a row too long to fit at
// all is left aligned to its own indent, because a name you can read the front
// of is identifiable and one you can read the back of is usually not.
func (p *Pane) follow(w int) {
	entries := p.Tree.Entries()
	if p.list.Sel < 0 || p.list.Sel >= len(entries) || w < 4 {
		p.left = 0
		return
	}
	e := entries[p.list.Sel]
	start := indentOf(e)
	end := start + cols(labelOf(e))
	avail := w - 1 // the last column is left clear, as the renderer does

	if end-p.left > avail {
		p.left = end - avail
	}
	if p.left > start {
		// Scrolling left goes to the row's own start rather than further:
		// anything less would be more movement than the row needs, and
		// movement is the thing to avoid.
		p.left = start
	}
	// The first column is padding that belongs to no row, so an offset of one
	// shows exactly what an offset of zero shows. Normalising means the top
	// level reads as "not scrolled" rather than as scrolled by an amount with
	// no visible effect.
	if p.left <= indentOf(Entry{}) {
		p.left = 0
	}
}

// clipLeft drops n display columns from the front of a string.
//
// Columns rather than bytes, because the marker is a multi-byte rune and names
// are arbitrary text: dropping bytes would cut one in half and put a
// replacement character where the offset landed.
func clipLeft(s string, n int) string {
	at := 0
	for i, r := range s {
		if at >= n {
			return s[i:]
		}
		at += ui.RuneWidth(r)
	}
	return ""
}

// cols is a string's display width, measured the way the renderer measures it.
func cols(s string) (n int) {
	for _, r := range s {
		n += ui.RuneWidth(r)
	}
	return
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
