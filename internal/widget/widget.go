package widget

import "raj/internal/ui"

// Theme is the palette the shared widgets draw with. Everything defaults to the
// terminal's own colours except where a boundary has to be visible.
type Theme struct {
	Text        ui.Style
	Dim         ui.Style
	Border      ui.Style
	BorderFocus ui.Style
	Selected    ui.Style // the focused item in the focused pane
	Inactive    ui.Style // the same item when its pane is not focused
	Match       ui.Style
	Title       ui.Style // a focused pane's heading
	TitleDim    ui.Style // an unfocused pane's heading
	Active      ui.Style // the current tab
}

// Focus resolves an item's style from whether it is selected and whether its
// pane has focus.
//
// Three states rather than two. "Selected but the pane is not focused" has to
// look different from both — otherwise a sidebar you have tabbed away from
// still looks like it is taking keystrokes, which is the single most confusing
// thing a multi-pane editor can do.
func (t Theme) Focus(selected, focused bool) ui.Style {
	switch {
	case selected && focused:
		return t.Selected
	case selected:
		return t.Inactive
	default:
		return t.Text
	}
}

// Heading resolves a pane title's style from whether the pane has focus.
func (t Theme) Heading(focused bool) ui.Style {
	if focused {
		return t.Title
	}
	return t.TitleDim
}

// DefaultTheme names as little as possible so panes inherit the host terminal.
func DefaultTheme() Theme {
	return Theme{
		Text:        ui.DefaultStyle,
		Dim:         ui.DefaultStyle.With(ui.Ansi(8)),
		Border:      ui.DefaultStyle.With(ui.Ansi(8)),
		BorderFocus: ui.DefaultStyle.With(ui.Ansi(4)),
		Selected:    ui.DefaultStyle.Plus(ui.Reverse),
		Inactive:    ui.DefaultStyle.On(ui.Ansi(238)),
		Match:       ui.DefaultStyle.With(ui.Ansi(3)),
		Title:       ui.DefaultStyle.Plus(ui.Bold | ui.Reverse),
		TitleDim:    ui.DefaultStyle.With(ui.Ansi(8)),
		Active:      ui.DefaultStyle.Plus(ui.Bold | ui.Reverse),
	}
}

// Box draws a rounded border. Rounded rather than square because it reads as a
// softer boundary at small sizes, and every terminal that can show box drawing
// can show these.
func Box(s *ui.Screen, x, y, w, h int, st ui.Style) {
	if w < 2 || h < 2 {
		return
	}
	s.Set(x, y, '╭', st)
	s.Set(x+w-1, y, '╮', st)
	s.Set(x, y+h-1, '╰', st)
	s.Set(x+w-1, y+h-1, '╯', st)
	for i := 1; i < w-1; i++ {
		s.Set(x+i, y, '─', st)
		s.Set(x+i, y+h-1, '─', st)
	}
	for i := 1; i < h-1; i++ {
		s.Set(x, y+i, '│', st)
		s.Set(x+w-1, y+i, '│', st)
	}
}

// List tracks selection and scrolling for a vertical list of items.
type List struct {
	Sel  int
	Top  int
	Rows int
}

// Move changes the selection by delta, clamped to n items, and scrolls to keep
// it visible.
func (l *List) Move(delta, n int) {
	if n == 0 {
		l.Sel, l.Top = 0, 0
		return
	}
	l.Sel += delta
	if l.Sel < 0 {
		l.Sel = 0
	}
	if l.Sel >= n {
		l.Sel = n - 1
	}
	l.Follow(n)
}

// Follow scrolls so the selection is on screen.
func (l *List) Follow(n int) {
	if l.Rows <= 0 {
		return
	}
	if l.Sel < l.Top {
		l.Top = l.Sel
	}
	if l.Sel >= l.Top+l.Rows {
		l.Top = l.Sel - l.Rows + 1
	}
	if max := n - l.Rows; l.Top > max {
		l.Top = max
	}
	if l.Top < 0 {
		l.Top = 0
	}
}

// Reset returns to the top of the list, for when its contents change wholesale.
func (l *List) Reset() { l.Sel, l.Top = 0, 0 }

// Truncate shortens text to w display columns, marking the cut with an ellipsis
// so a clipped path is visibly clipped rather than looking like a shorter name.
func Truncate(text string, w int) string {
	if w <= 0 {
		return ""
	}
	cols, cut := 0, len(text)
	for i, r := range text {
		rw := ui.RuneWidth(r)
		if cols+rw > w {
			cut = i
			break
		}
		cols += rw
	}
	if cut == len(text) {
		return text
	}
	if w < 2 {
		return "…"
	}
	for cut > 0 && runeCols(text[:cut]) > w-1 {
		cut = prevBoundary(text, cut)
	}
	return text[:cut] + "…"
}

// TruncateLeft shortens from the left, keeping the tail. Paths are more
// recognisable by their end than their beginning.
func TruncateLeft(text string, w int) string {
	if runeCols(text) <= w {
		return text
	}
	if w < 2 {
		return "…"
	}
	i := 0
	for i < len(text) && runeCols(text[i:]) > w-1 {
		i = nextBoundary(text, i)
	}
	return "…" + text[i:]
}
