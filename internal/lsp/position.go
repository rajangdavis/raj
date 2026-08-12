// Package lsp speaks the Language Server Protocol.
//
// The protocol is not the hard part; positions are. LSP counts characters in
// UTF-16 code units and raj counts bytes, which agree exactly for ASCII and
// disagree for everything else — so a file with one accented letter, one CJK
// character or one emoji shifts every position after it in a response. The
// failure mode is not a crash but a jump to the wrong column, which is why the
// mapping is fuzzed against the byte offsets rather than argued about.
//
// Three coordinate systems now exist in raj, and it is worth naming them so
// they are not confused: byte offsets (what the buffer and every edit use),
// display columns (what the renderer and the caret use, where a CJK character
// is two wide), and UTF-16 code units (what LSP uses, where a CJK character is
// one and an emoji is two). This package converts between the first and the
// third. It never touches the second.
package lsp

import "unicode/utf8"

// Position is a place in a document as LSP counts it: a zero-based line, and a
// zero-based character offset in UTF-16 code units within that line.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span, end-exclusive.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// ToUTF16 converts a byte offset within line to a UTF-16 code-unit offset.
//
// An offset that lands inside a multi-byte rune is rounded down to that rune's
// start rather than rejected. A caller asking about a position mid-rune has a
// bug, but answering with a plausible neighbour keeps that bug local instead of
// turning it into an out-of-range position the server rejects.
func ToUTF16(line string, byteOff int) int {
	if byteOff <= 0 {
		return 0
	}
	if byteOff > len(line) {
		byteOff = len(line)
	}
	units := 0
	for i := 0; i < byteOff; {
		r, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			break
		}
		if i+size > byteOff {
			break // mid-rune: stop at the last whole one
		}
		units += utf16Len(r)
		i += size
	}
	return units
}

// FromUTF16 converts a UTF-16 code-unit offset within line to a byte offset.
//
// An offset landing on the low half of a surrogate pair — the second unit of an
// emoji — resolves to the start of that rune. A server that reports such a
// position is describing a place inside a character that has no byte boundary,
// and the start is the only answer that is a real position in the buffer.
func FromUTF16(line string, units int) int {
	if units <= 0 {
		return 0
	}
	seen := 0
	for i := 0; i < len(line); {
		if seen >= units {
			return i
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			break
		}
		n := utf16Len(r)
		if seen+n > units {
			return i // the offset splits this rune: its start is the position
		}
		seen += n
		i += size
	}
	return len(line)
}

// utf16Len is how many UTF-16 code units a rune occupies: two above the basic
// multilingual plane, one otherwise. This is the entire disagreement between
// the two counting schemes.
func utf16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// Document maps between a text's byte offsets and LSP positions.
//
// Line starts are computed once per version rather than per query, because
// hover and completion both convert a position on every keystroke and a scan
// from the start of the file each time would put the cost on the typing path.
type Document struct {
	text   string
	starts []int // byte offset of each line's first byte
}

// NewDocument indexes text.
func NewDocument(text string) *Document {
	d := &Document{text: text, starts: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			d.starts = append(d.starts, i+1)
		}
	}
	return d
}

// Text is the indexed content.
func (d *Document) Text() string { return d.text }

// Lines is how many lines the document has. A trailing newline produces a final
// empty line, which is what every editor shows and what LSP expects.
func (d *Document) Lines() int { return len(d.starts) }

// Position converts a byte offset to an LSP position. Offsets past the end
// clamp to the end, since a server asked about a stale offset should get the
// nearest real place rather than an error.
func (d *Document) Position(off int) Position {
	if off < 0 {
		off = 0
	}
	if off > len(d.text) {
		off = len(d.text)
	}
	line := d.lineOf(off)
	return Position{Line: line, Character: ToUTF16(d.line(line), off-d.starts[line])}
}

// Offset converts an LSP position to a byte offset. Out-of-range lines clamp,
// because a server working from a version raj has already edited past will send
// positions that no longer exist and dropping the response entirely would be
// worse than aiming at the end of the file.
func (d *Document) Offset(p Position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(d.starts) {
		return len(d.text)
	}
	return d.starts[p.Line] + FromUTF16(d.line(p.Line), p.Character)
}

// Span converts an LSP range to a byte range, ordered so lo <= hi even if the
// server sent the ends the other way round.
func (d *Document) Span(r Range) (lo, hi int) {
	lo, hi = d.Offset(r.Start), d.Offset(r.End)
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi
}

// line is the text of a line without its newline.
func (d *Document) line(n int) string {
	start := d.starts[n]
	end := len(d.text)
	if n+1 < len(d.starts) {
		end = d.starts[n+1] - 1 // drop the newline
	}
	if end < start {
		end = start
	}
	return d.text[start:end]
}

// lineOf is a binary search over the line starts.
func (d *Document) lineOf(off int) int {
	lo, hi := 0, len(d.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if d.starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
