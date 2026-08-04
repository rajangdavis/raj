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
	matches []int // byte offsets of each match
	at      int   // index into matches
	term    string
}

// Show opens the bar, seeding it with the current selection when there is one —
// select a word, press cmd+f, and it is already the query.
func (f *Find) Show(p *Pane) {
	f.Open = true
	f.input.Focused = true
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
	f.run(p)
}

// Hide closes the bar, leaving the cursor where the last match put it.
func (f *Find) Hide() {
	f.Open = false
	f.matches = nil
}

// Matches returns the match offsets, for highlighting and for tests.
func (f *Find) Matches() []int { return f.matches }

// Query is the current search text.
func (f *Find) Query() string { return f.input.Text }

// Handle applies an action while the bar is open.
func (f *Find) Handle(p *Pane, a keys.Action, text string) {
	switch a {
	case keys.Cancel:
		f.Hide()
		return
	case keys.Confirm, keys.FindInFile, keys.LineDown, keys.Indent:
		// Tab arrives as Indent in the editor's scope. Stepping matches with it
		// is what the hand expects from a search bar, and indentation is not
		// reachable while the bar has the keys anyway.
		f.step(p, +1)
		return
	case keys.LineUp, keys.Outdent:
		f.step(p, -1)
		return
	}
	if f.input.Handle(a, text) {
		f.run(p)
	}
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

// Render draws the bar as a single line across the top of the editor pane.
func (f *Find) Render(s *ui.Screen, x, y, w int, th widget.Theme) {
	if !f.Open || w < 12 {
		return
	}
	s.Fill(x, y, w, 1, th.Selected)
	count := "no results"
	if n := len(f.matches); n > 0 {
		count = itoa(f.at+1) + "/" + itoa(n)
	} else if f.term == "" {
		count = ""
	}
	label := " find: " + f.input.Text
	s.SetString(x, y, widget.Truncate(label, w-len(count)-2), th.Selected, w)
	if count != "" {
		s.SetString(x+w-len(count)-1, y, count, th.Selected, len(count)+1)
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
