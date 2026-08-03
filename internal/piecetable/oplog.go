package piecetable

// Version identifies a point in the document's history. It is the length of the
// journal, so comparing versions is comparing integers and "what changed since
// you last read?" is a slice.
type Version uint64

// Op is one applied change: replace the pieces in Del with the pieces in Ins,
// at document offset Pos.
//
// Both sides are piece records rather than text, which is what makes the whole
// design cheap. Stores are append-only and nothing is ever erased, so the
// pieces a delete removed stay addressable forever — the inverse of an op is
// the same op with Del and Ins swapped, and it costs no copied bytes.
//
// One journal serves four purposes: undo (apply the inverse), the change list
// (filter by Author), rebasing a stale diff (replay offsets forward), and
// session restore (replay from the top). They are the same structure because
// they are the same question asked from different directions.
type Op struct {
	Seq    Version
	Author Author
	Pos    int
	Del    []PieceRec
	Ins    []PieceRec

	// Undoes is the Seq this op reverses, or 0 for an ordinary edit. Undo is
	// recorded as a new op rather than by rewinding, so the journal stays
	// append-only and offsets recorded against any past version remain
	// rebaseable through it.
	Undoes Version

	// Group ties ops that must undo together. One user action is one undo
	// step, however many buffer edits it took: typing with three cursors is
	// three inserts, and undoing them one at a time leaves the document in a
	// state the user never created and cannot recognise.
	Group uint64

	// Kind distinguishes an ordinary edit from the reversals that undo and redo
	// commit. The journal is append-only, so a redo is itself an op — and
	// without a label there is no way to tell "the newest thing to undo" from
	// "the newest undo to redo" once the two interleave.
	Kind OpKind
}

// OpKind labels an op's role in the undo timeline.
type OpKind uint8

const (
	KindEdit OpKind = iota
	KindUndo
	KindRedo
)

// DelLen is how many document bytes the op removed.
func (o Op) DelLen() int { return recsLen(o.Del) }

// InsLen is how many document bytes the op added.
func (o Op) InsLen() int { return recsLen(o.Ins) }

// Delta is the op's net effect on document length.
func (o Op) Delta() int { return o.InsLen() - o.DelLen() }

// Inverse returns the op that undoes o. Pos is unchanged: after removing Ins
// and restoring Del, the document is byte-identical to before o.
func (o Op) Inverse() Op {
	kind := KindUndo
	if o.Kind == KindUndo {
		kind = KindRedo // reversing an undo is a redo
	}
	return Op{Author: o.Author, Pos: o.Pos, Del: o.Ins, Ins: o.Del,
		Undoes: o.Seq, Group: o.Group, Kind: kind}
}

func recsLen(recs []PieceRec) (n int) {
	for _, r := range recs {
		n += r.Length
	}
	return
}

// pieceEditor is the piece-level surface the journal drives. Both Doc and Naive
// implement it, so the whole session layer is fuzzed against the oracle rather
// than only the tree being fuzzed.
type pieceEditor interface {
	Buffer
	Store() *Store
	insertRecs(pos int, recs []PieceRec)
	removeRange(pos, length int) []PieceRec
	pieceRange(pos, length int) []PieceRec
}

// apply performs an op against a buffer. The removed pieces are discarded
// because a well-formed op already records them in Del.
func apply(b pieceEditor, o Op) {
	if n := o.DelLen(); n > 0 {
		b.removeRange(o.Pos, n)
	}
	b.insertRecs(o.Pos, o.Ins)
}

// mapPoint carries a document offset from before an op to after it. touched
// reports that the point fell strictly inside the op's replaced span, where no
// honest answer exists — the caller decides whether that is a conflict.
func (o Op) mapPoint(p int) (mapped int, touched bool) {
	d := o.DelLen()
	switch {
	case o.Pos+d <= p:
		return p + o.Delta(), false
	case o.Pos >= p:
		return p, false
	default:
		return o.Pos, true
	}
}
