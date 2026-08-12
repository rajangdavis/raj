package editor

import "strings"

// Auto-indent and bracket handling are typing conveniences, so the rule they
// are all held to is that typing must never lose a keystroke. Every behaviour
// here either does the obvious thing or does nothing, and "does nothing" always
// means the plain character is inserted. A clever guess that eats a bracket is
// worse than no guess at all, because the second is predictable.
//
// None of this tries to understand the language. It reads the current line and
// the byte on either side of the cursor, which is enough for the cases that
// happen constantly and honest about the ones it cannot see.

// pairs are the brackets that close themselves. Quotes are handled separately:
// they are their own closer, which makes them ambiguous in a way brackets are
// not.
var pairs = map[byte]byte{'(': ')', '[': ']', '{': '}'}

// closers maps a closing byte back to its opener, for the type-over check.
var closers = map[byte]byte{')': '(', ']': '[', '}': '{'}

// quotes close themselves, and are only auto-closed in places where an opening
// quote is unambiguous — see shouldCloseQuote.
var quotes = map[byte]bool{'"': true, '\'': true, '`': true}

// InsertRune types one character, applying pairing and indentation.
//
// It is deliberately the only entry point that knows about any of this:
// InsertText stays a plain insertion, so a paste, a distributed paste and an
// agent edit all keep their exact bytes. Typing a bracket is a keystroke;
// pasting one is data.
func (p *Pane) InsertRune(text string) {
	if len(text) == 0 {
		p.InsertText(text)
		return
	}
	if text == "\n" {
		p.insertNewline()
		return
	}
	if len(text) > 1 {
		p.InsertText(text) // a multi-byte rune: nothing here applies
		return
	}
	b := text[0]

	// Typing a closer where that closer already sits moves over it instead of
	// adding a second one. This is what makes auto-closing bearable: the
	// muscle memory of typing the closing bracket yourself still works.
	if _, ok := closers[b]; ok && p.AutoPairs && p.typeOver(b) {
		return
	}
	if quotes[b] && p.AutoPairs && p.typeOver(b) {
		return
	}

	if p.AutoPairs {
		if close, ok := pairs[b]; ok && p.shouldClosePair() {
			p.surround(text, string(close))
			return
		}
		if quotes[b] && p.shouldCloseQuote(b) {
			p.surround(text, text)
			return
		}
	}
	p.InsertText(text)
}

// typeOver advances every cursor that is sitting immediately before b, and
// reports whether it did. It applies only when every cursor agrees: a partial
// type-over would insert at some cursors and move at others, which is not an
// edit anyone asked for.
func (p *Pane) typeOver(b byte) bool {
	cursors := p.Cursors.All()
	for _, c := range cursors {
		if c.HasSelection() || p.byteAt(c.Head) != b {
			return false
		}
	}
	for i := range cursors {
		p.Cursors.list[i].Head++
		p.Cursors.list[i].Anchor = p.Cursors.list[i].Head
	}
	p.FollowCursor()
	return true
}

// shouldClosePair reports whether an opening bracket should bring its closer.
//
// Not when the next byte is a word character: typing ( before an existing name
// means you are wrapping it, and adding ) there puts the closer in the middle
// of the word. Before a closer, whitespace or the end of a line, auto-closing
// is what was wanted.
func (p *Pane) shouldClosePair() bool {
	for _, c := range p.Cursors.All() {
		if c.HasSelection() {
			// A selection means "wrap this", which surround handles by
			// putting the pair around it. That is always right.
			continue
		}
		if isWordByte(p.byteAt(c.Head)) {
			return false
		}
	}
	return true
}

// shouldCloseQuote is stricter than shouldClosePair, because a quote is its own
// closer and therefore ambiguous: the editor cannot tell an opening quote from
// a closing one by looking at the character.
//
// The apostrophe is the case that matters. In prose and in comments, "don't"
// and "it's" are far more common than a single-quoted string, so a quote
// directly after a word character is treated as an apostrophe and left alone.
// Getting this wrong is not a small annoyance: it turns every contraction into
// a stray quote that has to be deleted.
func (p *Pane) shouldCloseQuote(b byte) bool {
	for _, c := range p.Cursors.All() {
		if c.HasSelection() {
			continue
		}
		if isWordByte(p.byteAt(c.Head)) || isWordByte(p.byteBefore(c.Head)) {
			return false
		}
		// Two of the same quote already behind the cursor means a pair was
		// just completed and this is the third: a docstring or a fenced
		// block, where a fourth quote is not wanted.
		if p.byteBefore(c.Head) == b {
			return false
		}
	}
	return true
}

// surround wraps each selection in open and close, or inserts the pair with the
// cursor between them when there is no selection.
//
// Wrapping a selection is why this is worth having at all: select a name, type
// a quote, and it is quoted. The selection is preserved rather than collapsed,
// so the same thing can be done twice.
func (p *Pane) surround(open, close string) {
	p.File.Begin()
	defer p.File.End()

	cursors := p.Cursors.All()
	for i := len(cursors) - 1; i >= 0; i-- {
		lo, hi := cursors[i].Range()
		if hi > lo {
			p.applyEdit(hi, 0, close)
			p.applyEdit(lo, 0, open)
			continue
		}
		p.applyEdit(lo, 0, open+close)
		// applyEdit bumps a cursor at the insertion point to the end of what
		// was inserted, which for a pair is past the closer. The cursor
		// belongs between them.
		for j, c := range p.Cursors.All() {
			if c.Head == lo+len(open)+len(close) {
				p.Cursors.list[j].Head = lo + len(open)
				p.Cursors.list[j].Anchor = p.Cursors.list[j].Head
			}
		}
	}
	p.Cursors.Normalize()
	p.FollowCursor()
}

// insertNewline breaks the line and carries the current indentation onto the
// new one.
//
// Between a bracket pair it does more: the closer is pushed onto a third line
// at the original indentation and the cursor is left on a blank middle line,
// indented one unit further. That shape — open brace, newline, body, closer —
// is the one that would otherwise be typed by hand every single time.
func (p *Pane) insertNewline() {
	p.File.Begin()
	defer p.File.End()

	unit := strings.Repeat(" ", p.File.Cols.Tab)
	cursors := p.Cursors.All()
	for i := len(cursors) - 1; i >= 0; i-- {
		c := cursors[i]
		lo, hi := c.Range()
		indent := p.lineIndent(lo)

		// A bracket pair straddling the cursor opens up into a block. Checked
		// on the collapsed range, so it does not fire for a selection whose
		// ends happen to be a bracket apart.
		if lo == hi && isOpener(p.byteBefore(lo)) && isCloser(p.byteAt(lo)) {
			p.applyEdit(lo, 0, "\n"+indent+unit+"\n"+indent)
			// The cursor lands after everything inserted; it belongs at the
			// end of the indented middle line.
			want := lo + 1 + len(indent) + len(unit)
			for j, cc := range p.Cursors.All() {
				if cc.Head == lo+len("\n"+indent+unit+"\n"+indent) {
					p.Cursors.list[j].Head = want
					p.Cursors.list[j].Anchor = want
				}
			}
			continue
		}
		p.applyEdit(lo, hi-lo, "\n"+indent)
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
	p.FollowCursor()
}

// lineIndent is the leading whitespace of the line containing off, copied
// verbatim so a file indented with tabs stays indented with tabs.
func (p *Pane) lineIndent(off int) string {
	line := p.File.Line(p.File.LineOf(off))
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[:i]
}

// DeleteBackward removes the character before each cursor, taking an empty pair
// with it.
//
// Backspacing over the opener of a pair the editor just inserted removes both.
// Without this, auto-pairing quietly costs a keystroke every time a bracket is
// typed and then thought better of.
func (p *Pane) deletePair() bool {
	if !p.AutoPairs {
		return false
	}
	cursors := p.Cursors.All()
	for _, c := range cursors {
		if c.HasSelection() {
			return false
		}
		before, after := p.byteBefore(c.Head), p.byteAt(c.Head)
		if pairs[before] != after && !(quotes[before] && before == after) {
			return false
		}
	}
	p.File.Begin()
	defer p.File.End()
	for i := len(cursors) - 1; i >= 0; i-- {
		p.applyEdit(cursors[i].Head-1, 2, "")
	}
	p.Cursors.Normalize()
	p.FollowCursor()
	return true
}

// byteAt is the byte at off, or 0 at the end of the document. Zero doubles as
// "nothing here", which is safe because a NUL in a text buffer is already
// treated as binary elsewhere.
//
// It reads through Slice rather than reaching into the piece table, so a single
// byte costs one lookup and no piece is materialised beyond it.
func (p *Pane) byteAt(off int) byte {
	if off < 0 || off >= p.File.Len() {
		return 0
	}
	s := p.File.Slice(off, 1)
	if s == "" {
		return 0
	}
	return s[0]
}

func (p *Pane) byteBefore(off int) byte {
	if off <= 0 {
		return 0
	}
	return p.byteAt(off - 1)
}

func isOpener(b byte) bool { _, ok := pairs[b]; return ok }
func isCloser(b byte) bool { _, ok := closers[b]; return ok }

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}
