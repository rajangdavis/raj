package editor

import (
	"strings"

	"raj/internal/piecetable"
)

// Clip is what a copy produces.
//
// It carries two representations of the same thing. Text is for the system
// clipboard, which can only hold a string. Spans is the piece records the text
// came from — and pasting those appends nothing at all, because the bytes are
// already in the buffer's stores and those stores are append-only, so a copied
// span stays addressable even after the original is deleted.
//
// The consequence is that copy and paste within a document are O(pieces)
// rather than O(bytes): copying a megabyte and pasting it ten times costs a few
// dozen piece records and no new storage.
//
// The catch is that "the buffer" has to be the one the spans came from. Spans
// are offsets into one document's stores, so they splice only back into that
// document; Source and Gen name it. A clip pasted anywhere else carries Text
// alone.
type Clip struct {
	Text  string
	Spans [][]piecetable.PieceRec // one entry per cursor selection

	// Source is the file the spans were captured from and Gen the document
	// generation it held then. Spans are offsets into one document's stores, so
	// they splice only back into Source at Gen; Reload replaces the store in
	// place, and Gen is what makes that visible.
	Source *File
	Gen    uint64
}

// Empty reports whether there is nothing to paste.
func (c Clip) Empty() bool { return c.Text == "" && len(c.Spans) == 0 }

// Internal reports whether this clip still refers to live pieces, meaning it
// can be pasted without copying. A clip from another program has text only.
func (c Clip) Internal() bool { return len(c.Spans) > 0 }

// internalTo reports whether this clip's spans address f's current store, the
// only case where PasteClip may splice them. A clip from another file, or one
// captured before a reload replaced this file's store, goes through Text.
func (c Clip) internalTo(f *File) bool {
	return len(c.Spans) > 0 && c.Source == f && c.Gen == f.docGen
}

// Copy captures every cursor's selection, as text for the system clipboard and
// as pieces for pasting back into this buffer.
//
// With several cursors the text is newline-joined in document order, which is
// the half of the round-trip that makes a distributed paste possible: returning
// only the primary selection loses the rest, and no paste can recover it.
func (p *Pane) Copy() Clip {
	c := Clip{Source: p.File, Gen: p.File.docGen}
	parts := make([]string, 0, p.Cursors.Count())
	// A whole-line copy publishes a trailing newline so the line pastes back as
	// a line. The captured span has to cover that newline too, or the two
	// representations describe different text and cmd+c then cmd+v depends on
	// which path the paste takes: the internal splice dropped the newline while
	// pasting the same clipboard externally kept it.
	lineCopy := p.Cursors.Count() == 1 && !p.Cursors.Primary().HasSelection()
	for _, cur := range p.Cursors.All() {
		lo, hi := cur.Range()
		snapHi := hi
		if hi == lo {
			// No selection: take the whole line, the way every editor does.
			line := p.File.LineOf(cur.Head)
			lo = p.File.LineStart(line)
			hi = p.File.LineEnd(line)
			snapHi = hi
			if lineCopy && hi < p.File.Len() {
				snapHi = hi + 1
			}
		}
		parts = append(parts, p.File.Slice(lo, hi-lo))
		c.Spans = append(c.Spans, p.File.Snapshot(lo, snapHi-lo))
	}
	// Text and Spans are deliberately NOT the same byte count with several
	// cursors. The "\n" separators here are structural: they carry the cursor
	// split to any other program that reads the clipboard, and PasteClip splits
	// on them to distribute a foreign clipboard back across cursors. Folding
	// the newline into each part instead breaks that — the part count no longer
	// matches the cursor count and distribution silently stops firing.
	c.Text = strings.Join(parts, "\n")
	if lineCopy {
		c.Text += "\n"
	}
	return c
}

// Cut copies and then removes, leaving the cursor somewhere sensible.
func (p *Pane) Cut() Clip {
	c := p.Copy()
	if c.Empty() {
		return c
	}
	p.File.Begin()
	defer p.File.End()

	if p.Cursors.Primary().HasSelection() {
		p.editEachCursor(func(cur Cursor) (int, int, string) {
			lo, hi := cur.Range()
			return lo, hi - lo, ""
		})
		return c
	}
	// Cutting whole lines leaves the cursor at the start of the line that moved
	// up to take the place of the one removed, rather than at a column that is
	// usually past the end of it.
	line := p.File.LineOf(p.Cursors.Primary().Head)
	p.DeleteLine()
	at := p.File.LineStart(clamp(line, 0, p.File.Lines()-1))
	p.Cursors.Set(at, at)
	return c
}

// PasteClip inserts a clip, distributing it across cursors when the counts
// match and inserting it whole otherwise.
//
// An internal clip splices pieces and stores nothing; an external one has to be
// appended, since its bytes are not in the buffer yet.
func (p *Pane) PasteClip(c Clip) {
	if c.Empty() {
		return
	}
	p.File.Begin()
	defer p.File.End()

	cursors := p.Cursors.All()
	internal := c.internalTo(p.File)
	if internal && len(c.Spans) == len(cursors) && len(cursors) > 1 {
		p.distribute(c.Spans)
		return
	}
	// A whole-line clip (its text ends in a newline) pasted at a single caret
	// with nothing selected goes in as a line, the way Vim's linewise p does:
	// below the current line, or filling an empty one. Splicing the newline at
	// the raw caret would split the destination line instead, leaving the
	// copied line's indent after the caret and the rest of it on the next line.
	if len(cursors) == 1 && !p.Cursors.Primary().HasSelection() &&
		strings.HasSuffix(c.Text, "\n") && (!internal || len(c.Spans) == 1) {
		p.pasteLinewise(c, internal)
		return
	}
	if internal && len(cursors) == 1 {
		p.spliceAtPrimary(p.flatten(c.Spans))
		return
	}
	lines := strings.Split(c.Text, "\n")
	if len(lines) == len(cursors) && len(cursors) > 1 {
		p.PasteDistributed(lines)
		return
	}
	if len(cursors) > 1 {
		p.PasteAtEachCursor(c.Text)
		return
	}
	p.Paste(c.Text)
}

// pasteLinewise inserts a whole-line clip as a line of its own: below the
// current line, or filling an empty one, the way Vim's linewise p does.
//
// A copy with no selection carries its line's trailing newline so it pastes
// back as a line. Splicing that at the raw caret would put the newline in the
// middle of the destination line, so the caret means "line" here rather than
// "byte". The copied line keeps its own indentation; only the cursor moves to
// the pasted line's first non-blank byte.
//
// internal selects the piece path: a single captured span is spliced, keeping
// an internal paste O(pieces) and storing nothing new, while an external clip
// has no pieces and must be appended. Both produce the same document text.
func (p *Pane) pasteLinewise(c Clip, internal bool) {
	cur := p.Cursors.Primary()
	ln := p.File.LineOf(cur.Head)
	lineStart := p.File.LineStart(ln)
	lineEnd := p.File.LineEnd(ln)
	content := strings.TrimSuffix(c.Text, "\n")
	indent := len(content) - len(strings.TrimLeft(content, " \t"))

	// An empty line is filled in place; any other line gets the paste below it.
	empty := lineEnd == lineStart && cur.Head == lineStart
	at := lineEnd
	pastedStart := lineEnd + 1
	if empty {
		at = lineStart
		pastedStart = lineStart
	}
	if g, ok := p.File.EditLeased(at, 0); ok {
		p.noteLease(g)
		return
	}

	if internal {
		recs := p.flatten(c.Spans)
		var contentRecs []piecetable.PieceRec
		var nl piecetable.PieceRec
		haveNL := recsLen(recs) == len(content)+1
		if haveNL {
			// The span already covers the source line's newline. Peel that
			// last byte off into its own record so it can be the separator
			// here; copy first, so the clip's own slice is never mutated.
			last := recs[len(recs)-1]
			nl = piecetable.PieceRec{Buf: last.Buf, Start: last.Start + last.Length - 1, Length: 1}
			contentRecs = make([]piecetable.PieceRec, len(recs))
			copy(contentRecs, recs)
			contentRecs[len(contentRecs)-1].Length--
			if contentRecs[len(contentRecs)-1].Length == 0 {
				contentRecs = contentRecs[:len(contentRecs)-1]
			}
		} else {
			contentRecs = recs
		}
		newline := func() piecetable.PieceRec {
			if haveNL {
				return nl
			}
			return p.File.NewlinePiece(p.Author)
		}
		ins := contentRecs
		switch {
		case empty && lineEnd < p.File.Len():
			// The empty line's own newline terminates the pasted line.
		case empty:
			ins = append(append(make([]piecetable.PieceRec, 0, len(contentRecs)+1),
				contentRecs...), newline())
		default:
			ins = append(append(make([]piecetable.PieceRec, 0, len(contentRecs)+1),
				newline()), contentRecs...)
		}
		p.File.InsertPieces(p.Author, at, ins)
	} else {
		text := content
		switch {
		case empty && lineEnd < p.File.Len():
		case empty:
			text += "\n"
		default:
			text = "\n" + content
		}
		p.File.Insert(p.Author, at, text)
	}
	p.Cursors.Set(pastedStart+indent, pastedStart+indent)
	p.FollowCursor()
}

// distribute gives each cursor its own captured span, highest offset first so
// earlier cursors stay valid.
func (p *Pane) distribute(spans [][]piecetable.PieceRec) {
	cursors := p.Cursors.All()
	for _, c := range cursors {
		lo, hi := c.Range()
		if g, ok := p.File.EditLeased(lo, hi-lo); ok {
			p.noteLease(g)
			return
		}
	}
	for i := len(cursors) - 1; i >= 0; i-- {
		lo, hi := cursors[i].Range()
		if hi > lo {
			p.File.Delete(p.Author, lo, hi-lo)
			p.Cursors.Shift(lo, hi-lo, 0)
		}
		n := recsLen(spans[i])
		p.File.InsertPieces(p.Author, lo, spans[i])
		p.Cursors.Shift(lo, 0, n)
		p.bumpCursorsAt(lo, n)
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
	p.FollowCursor()
}

// spliceAtPrimary replaces the primary selection with captured pieces.
func (p *Pane) spliceAtPrimary(recs []piecetable.PieceRec) {
	lo, hi := p.Cursors.Primary().Range()
	if g, ok := p.File.EditLeased(lo, hi-lo); ok {
		p.noteLease(g)
		return
	}
	if hi > lo {
		p.File.Delete(p.Author, lo, hi-lo)
	}
	p.File.InsertPieces(p.Author, lo, recs)
	at := lo + recsLen(recs)
	p.Cursors.Set(at, at)
	p.FollowCursor()
}

// flatten concatenates per-cursor spans, inserting a newline piece between them
// so a multi-selection copy pasted at one cursor reads as separate lines.
func (p *Pane) flatten(spans [][]piecetable.PieceRec) []piecetable.PieceRec {
	if len(spans) == 1 {
		return spans[0]
	}
	var out []piecetable.PieceRec
	for i, s := range spans {
		if i > 0 {
			out = append(out, p.File.NewlinePiece(p.Author))
		}
		out = append(out, s...)
	}
	return out
}

func recsLen(recs []piecetable.PieceRec) (n int) {
	for _, r := range recs {
		n += r.Length
	}
	return
}
