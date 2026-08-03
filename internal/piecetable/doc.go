package piecetable

import "strings"

// Doc is the resident buffer: pieces live as fixed-width records inside the
// leaves of an order-statistics B-tree (EDIT-BUFFER-STRATEGY.md §6). Structural
// edits are O(log n) plus a bounded in-leaf memmove, lookups are one descent,
// and the whole thing is ~19.5 B/piece with no per-piece allocation.
//
// Doc owns the editing semantics; PieceBTree owns the index. The split matters
// because the tree knows nothing about text — it moves fixed-width records and
// keeps subtree sums — so all the splitting and trimming logic is here, in one
// place, testable against Naive.
type Doc struct {
	store *Store
	tree  *PieceBTree
}

// NewDoc builds a document from the original file contents. width is the
// fixed-width record field size; pass 0 to size it from the file.
func NewDoc(orig string, width int) *Doc {
	if width <= 0 {
		width = WidthFor(len(orig) + 1<<20)
	}
	var recs []PieceRec
	if len(orig) > 0 {
		recs = []PieceRec{{Buf: int(Original), Start: 0, Length: len(orig)}}
	}
	return &Doc{store: NewStore([]byte(orig)), tree: NewPieceBTree(width, recs)}
}

func (d *Doc) Store() *Store { return d.store }
func (d *Doc) Len() int      { return d.tree.Total() }
func (d *Doc) Pieces() int   { return d.tree.Count() }

// Insert appends text to author's store and splices a piece pointing at it.
// The original text is never touched and nothing is copied twice: the document
// grows by one to three piece records regardless of how much text arrives.
func (d *Doc) Insert(author Author, pos int, text string) {
	if text == "" {
		return
	}
	start := d.store.Append(author, []byte(text))
	d.insertRecs(pos, []PieceRec{{Buf: int(author), Start: start, Length: len(text)}})
}

// splitAt returns the piece index at which a piece inserted at document offset
// pos belongs, splitting the straddling piece when pos falls inside one.
func (d *Doc) splitAt(pos int) int {
	if d.tree.Count() == 0 {
		return 0
	}
	if pos >= d.Len() {
		return d.tree.Count()
	}
	if pos < 0 {
		pos = 0
	}
	i := d.tree.find(pos)
	off := pos - d.tree.prefix(i)
	if off == 0 {
		return i
	}
	old := d.tree.Get(i)
	d.tree.addLen(i, off-old.Length)
	d.tree.InsertPiece(i+1, PieceRec{old.Buf, old.Start + off, old.Length - off})
	return i + 1
}

// insertRecs splices existing pieces at pos without touching any store. Undo
// uses it to restore deleted text: the bytes were never erased, so reviving a
// deletion is a piece splice, not a copy.
func (d *Doc) insertRecs(pos int, recs []PieceRec) {
	if len(recs) == 0 {
		return
	}
	at := d.splitAt(pos)
	for k, r := range recs {
		d.tree.InsertPiece(at+k, r)
	}
}

// Delete removes length bytes at pos, trimming or dropping pieces. Text is
// never erased from the store — a deleted span is still addressable, which is
// what lets undo restore it without having copied anything.
func (d *Doc) Delete(pos, length int) { d.removeRange(pos, length) }

// removeRange deletes length bytes at pos and returns the pieces removed, in
// document order. Text is never erased from the store, so the returned records
// stay valid indefinitely — that is what makes an inverse op free.
func (d *Doc) removeRange(pos, length int) []PieceRec {
	pos, length = clampRange(pos, length, d.Len())
	var out []PieceRec
	for length > 0 {
		i := d.tree.find(pos)
		p := d.tree.Get(i)
		off := pos - d.tree.prefix(i)
		take := p.Length - off
		if take > length {
			take = length
		}
		out = append(out, PieceRec{p.Buf, p.Start + off, take})
		switch {
		case off == 0 && take == p.Length:
			d.tree.DeletePiece(i)
		case off == 0:
			d.tree.DeletePiece(i)
			d.tree.InsertPiece(i, PieceRec{p.Buf, p.Start + take, p.Length - take})
		case off+take == p.Length:
			d.tree.addLen(i, -take)
		default:
			d.tree.addLen(i, off-p.Length)
			d.tree.InsertPiece(i+1,
				PieceRec{p.Buf, p.Start + off + take, p.Length - off - take})
		}
		length -= take
	}
	return out
}

func (d *Doc) Slice(pos, length int) string {
	pos, length = clampRange(pos, length, d.Len())
	var sb strings.Builder
	sb.Grow(length)
	d.walk(pos, length, func(p PieceRec, off, take, _ int) {
		sb.Write(d.store.Slice(Author(p.Buf), p.Start+off, take))
	})
	return sb.String()
}

func (d *Doc) Spans(pos, length int) []Span {
	pos, length = clampRange(pos, length, d.Len())
	var out []Span
	d.walk(pos, length, func(p PieceRec, _, take, docOff int) {
		out = appendSpan(out, docOff, take, Author(p.Buf))
	})
	return out
}

// walk visits the pieces overlapping [pos, pos+length). It descends once to
// find the first piece and then iterates the leaves, so a screenful costs
// O(log n + k) rather than a root descent per piece.
func (d *Doc) walk(pos, length int, fn func(p PieceRec, off, take, docOff int)) {
	if length <= 0 || d.tree.Count() == 0 {
		return
	}
	i := d.tree.find(pos)
	cur := d.tree.prefix(i)
	d.tree.Each(i, func(_ int, p PieceRec) bool {
		if length <= 0 {
			return false
		}
		off := pos - cur
		if off < 0 {
			off = 0
		}
		take := p.Length - off
		if take > length {
			take = length
		}
		if take > 0 {
			fn(p, off, take, pos)
			pos += take
			length -= take
		}
		cur += p.Length
		return true
	})
}

// pieceRange reports the pieces covering a range without modifying anything.
// An op records what it is about to delete, and a deleted piece stays valid
// because stores are append-only — so this is all an inverse op needs.
func (d *Doc) pieceRange(pos, length int) []PieceRec {
	pos, length = clampRange(pos, length, d.Len())
	var out []PieceRec
	d.walk(pos, length, func(p PieceRec, off, take, _ int) {
		out = append(out, PieceRec{p.Buf, p.Start + off, take})
	})
	return out
}
