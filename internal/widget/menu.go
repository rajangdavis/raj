package widget

import (
	"raj/internal/keys"
	"raj/internal/ui"
)

// MenuItem is one row of a Menu.
type MenuItem struct {
	Label   string // what the row says
	Key     string // opaque value the app maps back to an action
	Detail  string // optional right-aligned hint (a chord, a path); "" for none
	Enabled bool   // false draws dim and cannot be chosen
}

// Menu is a context menu: a small box anchored at the pointer, one row per
// item, opened by a right-click on an explorer entry or a tab.
//
// It follows the package's conventions rather than the literal interface a
// first sketch proposed, and the two places it does are deliberate:
//
//   - Handle takes a resolved keys.Action, like Input.Handle, hover.Panel.Handle
//     and complete.Popup.Handle. The widget does not decode chords and does not
//     hold a keymap; the application resolves the key for the menu exactly as it
//     does for every other pane. A raw ui.Key here would put a second keymap
//     inside a shared widget and ignore the application's bindings.
//   - Render takes this package's Theme, not ui.Theme. ui.Theme is the host
//     terminal's colours; Theme is the palette every widget draws with.
//
// Placement is the hover/complete rule restated where it applies: anchor at the
// pointer, slide left when the right edge would leave the screen, flip above
// when the bottom would, and clamp so the box is always drawn whole.
type Menu struct {
	open  bool
	title string
	items []MenuItem

	// sel is the highlighted item, or -1 when nothing can be chosen (every
	// item disabled). It never points at a disabled item.
	sel int

	// x/y/w/h are the rectangle the last Render drew, and are zero until then.
	// RowAt and Origin read them so a click is hit-tested against what was
	// painted rather than against a second copy of the placement arithmetic.
	x, y, w, h int
}

const (
	// menuMinWidth keeps a one-word menu from looking like a rendering fault.
	menuMinWidth = 10
	// menuPad is the blank cell between the border and a row's label.
	menuPad = 1
)

// Show opens the menu with title and items, anchored where the pointer was;
// Render's col/row is that anchor, so a caller carries the mouse position from
// the click to the frame rather than the widget keeping a stale copy of it.
//
// An empty menu is a programming error and does nothing: a box with no rows
// responds to no key and chooses nothing. The caller should not open a menu it
// has no items for — leaving an empty box over the code is worse than not
// opening — and Open reports false here so the caller can gate on it.
func (m *Menu) Show(title string, items []MenuItem) {
	if len(items) == 0 {
		m.Hide()
		return
	}
	m.open = true
	m.title = title
	m.items = items
	m.sel = edgeItem(items, +1)
}

// Open reports whether the menu is showing.
func (m *Menu) Open() bool { return m.open }

// Hide closes the menu and forgets its contents.
func (m *Menu) Hide() {
	m.open = false
	m.title = ""
	m.items = nil
	m.sel = -1
}

// Handle consumes a key while the menu is open and reports an item chosen.
//
// The claimed keys are up, down, home, end, enter and escape; anything else
// returns false and leaves the menu exactly as it was, so the caller can treat
// an open menu as modal without this widget silently eating keys it does not
// understand.
//
// Navigation clamps at the ends rather than wrapping. That is the package
// convention, not an accident: widget.List.Move clamps, and complete.Popup
// asserts the same with a reason — wrapping past the end of a short list makes
// the highlight jump the full height of the box. Disabled rows are skipped, so
// the highlight never rests somewhere enter cannot act.
//
// Enter on a disabled item, or with nothing highlighted (a menu of only
// disabled items), does nothing and leaves the menu open: a disabled row is not
// a way out. Escape cancels by closing without a key. Choosing closes the menu
// and returns the item's key.
func (m *Menu) Handle(a keys.Action) (key string, chosen bool) {
	if !m.open {
		return "", false
	}
	switch a {
	case keys.Cancel:
		m.Hide()
	case keys.Confirm:
		if m.sel >= 0 && m.sel < len(m.items) && m.items[m.sel].Enabled {
			key = m.items[m.sel].Key
			m.Hide()
			return key, true
		}
	case keys.LineUp:
		m.move(-1)
	case keys.LineDown:
		m.move(+1)
	case keys.LineStart, keys.DocStart:
		m.sel = edgeItem(m.items, +1)
	case keys.LineEnd, keys.DocEnd:
		m.sel = edgeItem(m.items, -1)
	}
	return "", false
}

// move steps the highlight by delta, clamped to the list, and skips disabled
// rows in the direction of travel. With no enabled row that way it stays put,
// which is the same outcome as the clamp at the end of a fully enabled list.
func (m *Menu) move(delta int) {
	i := m.sel + delta
	if i < 0 {
		i = 0
	}
	if i >= len(m.items) {
		i = len(m.items) - 1
	}
	for i >= 0 && i < len(m.items) && !m.items[i].Enabled {
		i += delta
	}
	if i < 0 || i >= len(m.items) {
		return
	}
	m.sel = i
}

// edgeItem is the first enabled item from the top (dir >= 0) or the bottom, or
// -1 when none is enabled.
func edgeItem(items []MenuItem, dir int) int {
	if dir >= 0 {
		for i := range items {
			if items[i].Enabled {
				return i
			}
		}
		return -1
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Enabled {
			return i
		}
	}
	return -1
}

// Size is the box's drawn size, border included. Before the first Render it is
// the size the menu wants; after, it is exactly the rectangle Render drew, so a
// caller can compare it against what is on screen.
func (m *Menu) Size() (w, h int) {
	if m.w > 0 && m.h > 0 {
		return m.w, m.h
	}
	return m.measure()
}

// Origin is the top-left cell the last Render placed the box at. A caller that
// hit-tests a click must pass this to RowAt rather than the anchor, because the
// box is clamped or flipped away from the anchor near an edge.
func (m *Menu) Origin() (col, row int) { return m.x, m.y }

// measure is the size the content wants, before the screen bounds it.
func (m *Menu) measure() (w, h int) {
	inner := 0
	for _, it := range m.items {
		n := menuPad + runeCols(it.Label)
		if it.Detail != "" {
			n += 2 + runeCols(it.Detail)
		}
		if n > inner {
			inner = n
		}
	}
	// The title sits on the top border as " title ". It needs its two spaces
	// plus one cell of dash before the corner, hence +3.
	if t := runeCols(m.title) + 3; t > inner {
		inner = t
	}
	if inner < menuMinWidth {
		inner = menuMinWidth
	}
	return inner + 2, len(m.items) + 2
}

// Render draws the menu anchored at (col, row), inside a cols x rows screen.
// col/row is the pointer position, not a precomputed origin: the box opens down
// and right of it, then slides left and flips above to stay on screen — the
// completion popup's rule, restated where it applies. A menu near the
// bottom-right corner therefore draws whole instead of being clipped exactly
// where the pointer usually is.
func (m *Menu) Render(s *ui.Screen, col, row, cols, rows int, th Theme) {
	if !m.open || len(m.items) == 0 || cols <= 0 || rows <= 0 {
		return
	}

	w, h := m.measure()
	if w > cols {
		w = cols
	}
	if h > rows {
		h = rows
	}
	if w < 4 || h < 3 {
		// No room for a box worth drawing. Nothing is painted, but the menu
		// stays open so its keys still work on a terminal too small to show it.
		m.w, m.h = 0, 0
		return
	}
	m.w, m.h = w, h
	m.x, m.y = place(col, row, w, h, cols, rows)

	s.Fill(m.x, m.y, w, h, th.Text)
	Box(s, m.x, m.y, w, h, th.Border)
	if m.title != "" {
		s.SetString(m.x+2, m.y, " "+Truncate(m.title, w-4)+" ", th.Border, w-4)
	}
	for i := 0; i < len(m.items) && i < h-2; i++ {
		m.row(s, m.x+1, m.y+1+i, w-2, i, th)
	}
}

// place resolves the top-left so a w x h box anchored at (col,row) is fully on
// a cols x rows screen.
func place(col, row, w, h, cols, rows int) (x, y int) {
	x = col
	if x+w > cols {
		x = cols - w // slide left rather than clip the labels
	}
	if x < 0 {
		x = 0
	}

	y = row
	if y+h > rows {
		if above := row - h; above >= 0 {
			y = above // flip above the pointer when it fits
		} else {
			y = rows - h // otherwise pin to the bottom edge
		}
	}
	if y < 0 {
		y = 0
	}
	return
}

// row draws one item: filled across, label left, detail right-aligned. Disabled
// items are dim; the highlighted one is reverse video. The fill is what makes
// the highlight cover the whole row rather than just the glyphs.
func (m *Menu) row(s *ui.Screen, x, y, w, i int, th Theme) {
	it := m.items[i]
	st := th.Text
	switch {
	case !it.Enabled:
		st = th.Dim
	case i == m.sel:
		st = th.Focus(true, true)
	}
	s.Fill(x, y, w, 1, st)

	label := " " + it.Label
	if it.Detail != "" {
		dw := runeCols(it.Detail)
		if dw+2 <= w { // one cell of gap and one of right pad
			s.SetString(x+w-dw-1, y, it.Detail, st, dw)
			label = Truncate(label, w-dw-2)
		}
	}
	if label != "" {
		s.SetString(x, y, Truncate(label, w), st, w)
	}
}

// RowAt reports which item a screen cell hits, given the box origin the last
// Render used (also available from Origin).
//
// ok reports whether the cell is anywhere inside the menu; key is the item's
// key on an enabled item row and "" otherwise. That split is what the mouse
// needs: a click inside the menu is consumed even when it landed on the frame
// or a dim row, and only a non-empty key is dispatched. Disabled rows keep
// their cell — the mapping is positional, so later rows do not shift — but
// cannot be chosen, by mouse or keyboard. A cell outside the box returns
// ok=false so the caller can dismiss the menu and let the click fall through.
func (m *Menu) RowAt(col, row, originCol, originRow int) (key string, ok bool) {
	if !m.open || m.w <= 0 || m.h <= 0 {
		return "", false
	}
	if col < originCol || col >= originCol+m.w || row < originRow || row >= originRow+m.h {
		return "", false
	}
	rel := row - originRow
	if rel <= 0 || rel >= m.h-1 {
		return "", true // the title and border rows: inside, but nothing to choose
	}
	i := rel - 1
	if i >= len(m.items) || i >= m.h-2 {
		return "", true
	}
	if !m.items[i].Enabled {
		return "", true
	}
	return m.items[i].Key, true
}
