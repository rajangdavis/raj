package piecetable

// Buffer is the editing surface every pane and every agent talks to.
//
// Two implementations satisfy it and are fuzzed against each other: Naive, a
// flat slice of pieces that is obviously correct and serves as the oracle, and
// Doc, the leaf-embedded order-statistics B-tree that is the resident
// structure. Keeping both behind one interface is what lets the fast one be
// trusted — every property is checked against the simple one on random edit
// sequences.
//
// All positions are document BYTE offsets. Offsets always land on rune
// boundaries in practice because they originate from cursor positions, but
// nothing here enforces that; callers splitting arbitrary byte ranges are
// responsible for staying on boundaries.
type Buffer interface {
	// Len is the document size in bytes.
	Len() int

	// Insert places text at pos, attributed to author.
	Insert(author Author, pos int, text string)

	// Delete removes length bytes starting at pos.
	Delete(pos, length int)

	// Slice returns the document text in [pos, pos+length).
	Slice(pos, length int) string

	// Spans reports authorship across [pos, pos+length) as contiguous runs.
	// This is what the renderer walks to tint agent-written text, so it must
	// be cheap for a screenful: O(log n + k) for k pieces in range.
	Spans(pos, length int) []Span

	// Pieces is the number of pieces currently representing the document.
	// Piece count grows with edits, not with file size; watching it is how
	// compaction decides when to run.
	Pieces() int
}

// Span is a contiguous run of document bytes written by one author.
type Span struct {
	Off    int // document byte offset of the run's start
	Len    int
	Author Author
}

// clampRange normalises a requested range against a document of size total,
// returning the usable start and length. Out-of-range requests clamp rather
// than panic: the renderer routinely asks for a screenful past EOF.
func clampRange(pos, length, total int) (int, int) {
	if pos < 0 {
		length += pos
		pos = 0
	}
	if pos > total {
		return total, 0
	}
	if length < 0 {
		length = 0
	}
	if pos+length > total {
		length = total - pos
	}
	return pos, length
}

// appendSpan adds a run to out, merging into the previous run when the author
// matches so the renderer sees one span per colour change rather than one per
// piece. Pieces fragment as edits accumulate; spans should not.
func appendSpan(out []Span, off, length int, a Author) []Span {
	if length <= 0 {
		return out
	}
	if n := len(out); n > 0 && out[n-1].Author == a && out[n-1].Off+out[n-1].Len == off {
		out[n-1].Len += length
		return out
	}
	return append(out, Span{Off: off, Len: length, Author: a})
}
