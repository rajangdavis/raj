package piecetable

import "strings"

// Naive is a piece table over a plain slice. Every structural edit is an O(n)
// slice splice and every lookup is an O(n) scan, so it is unusable as the
// resident structure — EDIT-BUFFER-STRATEGY.md §5 rules it out at a million
// pieces. It exists because it is obviously correct: Doc is fuzzed against it,
// so the tree's O(log n) machinery has something to be wrong against.
type Naive struct {
	store  *Store
	pieces []PieceRec
}

// NewNaive builds a buffer from the original file contents.
func NewNaive(orig string) *Naive {
	n := &Naive{store: NewStore([]byte(orig))}
	if len(orig) > 0 {
		n.pieces = []PieceRec{{Buf: int(Original), Start: 0, Length: len(orig)}}
	}
	return n
}

func (n *Naive) Store() *Store { return n.store }
func (n *Naive) Pieces() int   { return len(n.pieces) }

func (n *Naive) Len() (total int) {
	for _, p := range n.pieces {
		total += p.Length
	}
	return
}

func (n *Naive) Insert(author Author, pos int, text string) {
	if text == "" {
		return
	}
	start := n.store.Append(author, []byte(text))
	n.insertRecs(pos, []PieceRec{{Buf: int(author), Start: start, Length: len(text)}})
}

func (n *Naive) Delete(pos, length int) { n.removeRange(pos, length) }

func (n *Naive) removeRange(pos, length int) []PieceRec {
	pos, length = clampRange(pos, length, n.Len())
	var out []PieceRec
	for length > 0 {
		i, off := n.locate(pos)
		if i == len(n.pieces) {
			return out
		}
		p := n.pieces[i]
		take := p.Length - off
		if take > length {
			take = length
		}
		out = append(out, PieceRec{p.Buf, p.Start + off, take})
		switch {
		case off == 0 && take == p.Length:
			n.pieces = removeRec(n.pieces, i)
		case off == 0:
			n.pieces[i] = PieceRec{p.Buf, p.Start + take, p.Length - take}
		case off+take == p.Length:
			n.pieces[i].Length = off
		default:
			n.pieces[i].Length = off
			n.pieces = insertRec(n.pieces, i+1,
				PieceRec{p.Buf, p.Start + off + take, p.Length - off - take})
		}
		length -= take
	}
	return out
}

func (n *Naive) insertRecs(pos int, recs []PieceRec) {
	if len(recs) == 0 {
		return
	}
	i, off := n.locate(pos)
	if off > 0 {
		left := n.pieces[i]
		n.pieces[i].Length = off
		n.pieces = insertRec(n.pieces, i+1,
			PieceRec{left.Buf, left.Start + off, left.Length - off})
		i++
	}
	for k, r := range recs {
		n.pieces = insertRec(n.pieces, i+k, r)
	}
}

func (n *Naive) Slice(pos, length int) string {
	pos, length = clampRange(pos, length, n.Len())
	var sb strings.Builder
	sb.Grow(length)
	n.walk(pos, length, func(p PieceRec, off, take, _ int) {
		sb.Write(n.store.Slice(Author(p.Buf), p.Start+off, take))
	})
	return sb.String()
}

func (n *Naive) Spans(pos, length int) []Span {
	pos, length = clampRange(pos, length, n.Len())
	var out []Span
	n.walk(pos, length, func(p PieceRec, _, take, docOff int) {
		out = appendSpan(out, docOff, take, Author(p.Buf))
	})
	return out
}

// walk visits each piece overlapping [pos, pos+length), reporting the offset
// within the piece, how many bytes to take, and the document offset.
func (n *Naive) walk(pos, length int, fn func(p PieceRec, off, take, docOff int)) {
	cur := 0
	for _, p := range n.pieces {
		if length <= 0 {
			return
		}
		if cur+p.Length > pos {
			off := pos - cur
			if off < 0 {
				off = 0
			}
			take := p.Length - off
			if take > length {
				take = length
			}
			fn(p, off, take, pos)
			pos += take
			length -= take
		}
		cur += p.Length
	}
}

// locate maps a document offset to (piece index, offset within that piece).
// A position at a piece boundary returns the piece starting there.
func (n *Naive) locate(pos int) (int, int) {
	cur := 0
	for i, p := range n.pieces {
		if pos < cur+p.Length {
			return i, pos - cur
		}
		cur += p.Length
	}
	return len(n.pieces), 0
}

func insertRec(a []PieceRec, i int, r PieceRec) []PieceRec {
	a = append(a, PieceRec{})
	copy(a[i+1:], a[i:])
	a[i] = r
	return a
}

func removeRec(a []PieceRec, i int) []PieceRec {
	copy(a[i:], a[i+1:])
	return a[:len(a)-1]
}

// pieceRange reports the pieces covering a range without modifying anything.
// An op records what it is about to delete, and a deleted piece stays valid
// because stores are append-only — so this is all an inverse op needs.
func (n *Naive) pieceRange(pos, length int) []PieceRec {
	pos, length = clampRange(pos, length, n.Len())
	var out []PieceRec
	n.walk(pos, length, func(p PieceRec, off, take, _ int) {
		out = append(out, PieceRec{p.Buf, p.Start + off, take})
	})
	return out
}
