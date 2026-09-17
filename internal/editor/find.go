package editor

import (
	"strings"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Find is the in-buffer search bar (cmd+f). It sits inside the editor pane
// rather than the sidebar, because it is about one file and its results are
// positions in the text you are already looking at.
//
// Matching is incremental and case-insensitive until you type a capital, which
// is the smart-case convention: it does the obvious thing without a toggle.
type Find struct {
	Open    bool
	input   widget.Input
	replace widget.Input
	matches []int // byte offsets of each match
	at      int   // index into matches
	term    string

	// spot is which row holds focus, spotQuery or spotReplace, and replaceShown
	// whether the replace row has been revealed. The row stays hidden until tab
	// asks for it, so a plain find looks exactly as it always has.
	spot         int
	replaceShown bool
}

// The two focus spots in the find bar: the query row and the replace row. The
// replace field is revealed and focused by spotReplace.
const (
	spotQuery   = 0
	spotReplace = 1
)

// Show opens the bar, seeding it with the current selection when there is one —
// select a word, press cmd+f, and it is already the query.
func (f *Find) Show(p *Pane) {
	f.Open = true
	f.input.Focused = true
	f.replace.Focused = false
	f.spot = spotQuery
	f.replaceShown = false
	// Always start from the selection, or from empty. Retaining the previous
	// query would mean the next thing typed silently appends to it, which is
	// the kind of surprise that makes a search box feel broken.
	seed := ""
	if c := p.Cursors.Primary(); c.HasSelection() {
		lo, hi := c.Range()
		seed = p.File.Slice(lo, hi-lo)
	}
	// SetText rather than assigning Text: the field carries a selection anchor
	// now, and leaving it pointing into the old contents indexes past the end
	// of the new ones.
	f.input.SetText(seed)
	// The replacement is cleared rather than kept, like the query: reopening
	// the bar should not silently re-use the previous replacement.
	f.replace.SetText("")
	f.run(p)
}

// Hide closes the bar, leaving the cursor where the last match put it. The
// entered text is kept, like the query, so reopening finds the same thing; the
// replace row is hidden again so the bar comes back one row tall.
func (f *Find) Hide() {
	f.Open = false
	f.matches = nil
	f.spot = spotQuery
	f.replaceShown = false
}

// Matches returns the match offsets, for highlighting and for tests.
func (f *Find) Matches() []int { return f.matches }

// Rows is how many rows the bar draws: none when it is closed, one for a plain
// find, and two once the replace row has been revealed.
func (f *Find) Rows() int {
	if !f.Open {
		return 0
	}
	if f.replaceShown {
		return 2
	}
	return 1
}

// ActiveInput is the focused field while the bar is open. The bar sits inside
// the editor pane, so without this cmd+c while typing a query would copy from
// the document being searched, and cmd+x in the replace row would cut the
// document instead of the replacement.
func (f *Find) ActiveInput() *widget.Input {
	if !f.Open {
		return nil
	}
	if f.spot == spotReplace {
		return &f.replace
	}
	return &f.input
}

// WouldEdit reports whether an action, handled by the bar, would change the
// document. The application asks this before letting a bar key through in
// Review mode, because handleEditor delegates to the bar before the ordinary
// read-only check and a replace would otherwise bypass it.
func (f *Find) WouldEdit(a keys.Action) bool {
	switch a {
	case keys.LineBelow:
		return true // replace all
	case keys.Confirm:
		return f.spot == spotReplace
	}
	return false
}

// Query is the current search text.
func (f *Find) Query() string { return f.input.Text }

// Handle applies an action while the bar is open. The focused row receives
// typing: the query re-runs the match set on every keystroke, the replacement
// does not, or the matches would jump around as a replacement is typed.
func (f *Find) Handle(p *Pane, a keys.Action, text string) {
	switch a {
	case keys.Cancel:
		f.Hide()
		return
	case keys.FindNext:
		f.step(p, +1)
		return
	case keys.FindPrev:
		f.step(p, -1)
		return
	case keys.Confirm:
		if f.spot == spotReplace {
			f.replaceCurrent(p)
			return
		}
		f.step(p, +1)
		return
	case keys.LineBelow:
		// cmd+enter arrives as LineBelow; while the replace row is shown it
		// means replace all. With the row hidden it is ignored, so an
		// unrevealed bar cannot delete every match.
		if f.replaceShown {
			f.replaceAll(p)
		}
		return
	case keys.FindInFile, keys.LineDown:
		f.step(p, +1)
		return
	case keys.LineUp:
		f.step(p, -1)
		return
	case keys.Indent:
		// Tab reveals the replace row from the query, then toggles focus
		// between the two rows once it is shown.
		if f.spot == spotQuery {
			f.replaceShown = true
			f.spot = spotReplace
		} else {
			f.spot = spotQuery
		}
		return
	case keys.Outdent:
		// Shift+tab walks back to the query. It never hides the row, so the
		// bar's height depends on whether replacement was asked for rather
		// than on where focus happens to be.
		if f.spot == spotReplace {
			f.spot = spotQuery
		}
		return
	}
	if f.spot == spotReplace {
		// Editing the replacement must not recompute matches: the field is not
		// the query, and a half-typed replacement is not a search.
		f.replace.Handle(a, text)
		return
	}
	if f.input.Handle(a, text) {
		f.run(p)
	}
}

// replaceCurrent replaces the current match and advances to the next one. It
// runs through the pane's own edit path, so it is one undo step and a leased
// run refuses it exactly as a typed edit would. The match set is recomputed
// afterwards because the text under it moved.
func (f *Find) replaceCurrent(p *Pane) {
	if len(f.matches) == 0 {
		return
	}
	off := f.matches[f.at]
	p.File.Begin()
	p.applyEdit(off, len(f.term), f.replace.Text)
	p.File.End()
	f.run(p)
	p.FollowCursor()
}

// replaceAll replaces every match in one change set, so a single undo puts the
// buffer back. The leases are checked together before anything moves, so a set
// that owns any match refuses the whole action rather than mutating the rest —
// the same rule every multi-cursor action follows.
func (f *Find) replaceAll(p *Pane) {
	if len(f.matches) == 0 {
		return
	}
	edits := make([]cursorEdit, 0, len(f.matches))
	for _, off := range f.matches {
		edits = append(edits, cursorEdit{pos: off, remove: len(f.term), insert: f.replace.Text})
	}
	if p.leaseBlocks(edits) {
		return
	}
	p.File.Begin()
	// Highest offset first: earlier matches stay valid while later ones are
	// edited away.
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		p.applyEdit(e.pos, e.remove, e.insert)
	}
	p.File.End()
	f.run(p)
}

// run recomputes the matches and jumps to the first at or after the cursor, so
// typing walks forward from where you are rather than from the top of the file.
func (f *Find) run(p *Pane) {
	f.term = f.input.Text
	f.matches, f.at = nil, 0
	if f.term == "" {
		return
	}
	hay, needle := p.File.Text(), f.term
	if !hasUpper(f.term) {
		hay, needle = strings.ToLower(hay), strings.ToLower(needle)
	}
	for i := strings.Index(hay, needle); i >= 0; {
		f.matches = append(f.matches, i)
		next := strings.Index(hay[i+1:], needle)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	from := p.Cursors.Primary().Head
	for i, m := range f.matches {
		if m >= from {
			f.at = i
			break
		}
	}
	f.jump(p)
}

// Step moves to the next or previous match whether or not the bar has focus,
// which is what cmd+g and cmd+shift+g are for. Hide drops the matches but keeps
// the query, so a closed bar recomputes rather than doing nothing — find, keep
// editing, then step to the next hit without reopening anything.
func (f *Find) Step(p *Pane, dir int) {
	if f.input.Text == "" {
		return
	}
	if len(f.matches) == 0 || f.term != f.input.Text {
		f.run(p) // lands on the first match at or after the cursor
		if dir > 0 {
			return
		}
	}
	f.step(p, dir)
}

// step moves to the next or previous match, wrapping at both ends.
func (f *Find) step(p *Pane, d int) {
	if len(f.matches) == 0 {
		return
	}
	f.at = (f.at + d + len(f.matches)) % len(f.matches)
	f.jump(p)
}

// jump selects the current match and scrolls to it.
func (f *Find) jump(p *Pane) {
	if len(f.matches) == 0 {
		return
	}
	off := f.matches[f.at]
	p.Cursors.Set(off+len(f.term), off)
	p.FollowCursor()
}

// Highlight reports whether an offset falls inside any match, and whether it is
// the current one. Every match is marked so you can see the shape of the
// results without stepping through them.
func (f *Find) Highlight(off int) (match, current bool) {
	if !f.Open || f.term == "" {
		return false, false
	}
	for i, m := range f.matches {
		if off >= m && off < m+len(f.term) {
			return true, i == f.at
		}
	}
	return false, false
}

// Render draws the bar across the top of the editor pane. findPrefix and
// replacePrefix are what each row writes before its text. The renderer and the
// pointer share them so the caret cannot land a column away from the character
// it was aimed at.
const (
	findPrefix    = " find: "
	replacePrefix = " replace: "
)

// ClickAt places the caret from a press at (dx, dy) in the bar, reporting
// whether the bar took it. dy 0 is the query row; the replace row is on dy 1
// and only takes a press once it has been revealed. A press on the prefix or
// the match count is still the row's — there is nothing else it could mean.
func (f *Find) ClickAt(dx, dy int) bool {
	if !f.Open {
		return false
	}
	in, prefix := &f.input, findPrefix
	switch dy {
	case 0:
		f.spot = spotQuery
	case 1:
		if !f.replaceShown {
			return false
		}
		f.spot = spotReplace
		in, prefix = &f.replace, replacePrefix
	default:
		return false
	}
	in.PlaceCaret(dx - len(prefix))
	return true
}

func (f *Find) Render(s *ui.Screen, x, y, w int, th widget.Theme) {
	if !f.Open || w < 12 {
		return
	}
	// Keep the fields' own focus flags in step with the spot, so the focused
	// row is the one a caller reading the field sees as active.
	f.input.Focused = f.spot == spotQuery
	f.replace.Focused = f.spot == spotReplace
	s.Fill(x, y, w, 1, th.Selected)
	count := "no results"
	if n := len(f.matches); n > 0 {
		count = itoa(f.at+1) + "/" + itoa(n)
	} else if f.term == "" {
		count = ""
	}
	label := findPrefix + f.input.Text
	s.SetString(x, y, widget.Truncate(label, w-len(count)-2), th.Selected, w)
	if count != "" {
		s.SetString(x+w-len(count)-1, y, count, th.Selected, len(count)+1)
	}
	if f.replaceShown {
		s.Fill(x, y+1, w, 1, th.Selected)
		label := replacePrefix + f.replace.Text
		s.SetString(x, y+1, widget.Truncate(label, w-2), th.Selected, w)
	}
}

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
