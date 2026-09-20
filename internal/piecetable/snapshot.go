package piecetable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
)

// Session snapshots: an exact, self-contained capture of a Session that can be
// rebuilt into one indistinguishable from the original.
//
// This is the seam a client-side renderer needs: a second process renders the
// daemon's buffer without holding the model, and it must see the same text,
// the same attribution and the same review state. The journal persistence path
// (internal/app/journal.go) records only enough to reconstruct incrementally;
// a snapshot is the whole value, so it needs no append-only log on the other
// side and cannot drift from an out-of-order replay.
//
// The encoding is versioned and self-describing. JSON v1 is deliberate: this
// crosses a process boundary and a wire, so being able to read a snapshot with
// a human eye is worth more than compactness, and the store blobs are already
// byte strings, which JSON carries as base64.

// snapshotVersion is the snapshot encoding's version. A reader refuses any
// other value rather than guessing at the shape.
const snapshotVersion = 1

// Snapshot is an exact capture of a Session. Every field is needed to rebuild
// one: the base document, each author's store buffer, the journal, the group
// decisions, the next group id, and the compacted origins (store ranges
// compaction created, which carry no journal op of their own).
type Snapshot struct {
	Version    int                   `json:"version"`
	OrigLen    int                   `json:"orig_len"`
	Base       []byte                `json:"base"`
	Store      [][]byte              `json:"store"`
	Journal    []Op                  `json:"journal"`
	GroupState map[uint64]GroupState `json:"group_state,omitempty"`
	NextGroup  uint64                `json:"next_group"`
	Compacted  []SnapshotOrigin      `json:"compacted,omitempty"`
}

// SnapshotOrigin is the wire form of the session's unexported insOrigin: a
// store range one included edit claimed, and the change set that claimed it.
type SnapshotOrigin struct {
	Buf   int    `json:"buf"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Group uint64 `json:"group"`
}

// SnapshotState captures the session. The name avoids the existing Snapshot,
// which captures a clip as piece records; this captures the whole session for
// another process to restore. The returned value aliases nothing: the store
// blobs and the journal are copied, so the session can keep editing while the
// snapshot is encoded or carried.
func (s *Session) SnapshotState() (Snapshot, error) {
	store := s.buf.Store()
	texts := make([][]byte, len(store.texts))
	for i, t := range store.texts {
		texts[i] = append([]byte(nil), t...)
	}
	if s.origLen < 0 || (len(texts) > 0 && s.origLen > len(texts[0])) {
		return Snapshot{}, fmt.Errorf("piecetable: base length %d exceeds the original store %d", s.origLen, len(texts[0]))
	}
	base := make([]byte, s.origLen)
	if len(texts) > 0 {
		copy(base, texts[0][:s.origLen])
	}
	snap := Snapshot{
		Version:   snapshotVersion,
		OrigLen:   s.origLen,
		Base:      base,
		Store:     texts,
		NextGroup: s.group,
	}
	snap.Journal = make([]Op, len(s.journal))
	for i, o := range s.journal {
		o.Del = append([]PieceRec(nil), o.Del...)
		o.Ins = append([]PieceRec(nil), o.Ins...)
		snap.Journal[i] = o
	}
	if len(s.groupState) > 0 {
		snap.GroupState = maps.Clone(s.groupState)
	}
	for _, c := range s.compacted {
		snap.Compacted = append(snap.Compacted, SnapshotOrigin{Buf: c.buf, Start: c.start, End: c.end, Group: c.group})
	}
	return snap, nil
}

// Restore rebuilds a session from a snapshot. It validates the structure before
// replaying anything, so a truncated or hand-edited snapshot is refused with an
// error rather than reaching an index panic in the piece tree.
//
// The restored document is built over the captured store and the base piece,
// then the journal is replayed exactly as NewRestoredSession expects; the store
// already holds every blob the replay reads, so no append is needed.
func Restore(snap Snapshot) (*Session, error) {
	if snap.Version != snapshotVersion {
		return nil, fmt.Errorf("piecetable: snapshot version %d, want %d", snap.Version, snapshotVersion)
	}
	if snap.OrigLen < 0 {
		return nil, fmt.Errorf("piecetable: negative base length %d", snap.OrigLen)
	}
	if len(snap.Store) == 0 {
		return nil, fmt.Errorf("piecetable: snapshot has no store")
	}
	if snap.OrigLen > len(snap.Store[0]) {
		return nil, fmt.Errorf("piecetable: base length %d exceeds the original store %d", snap.OrigLen, len(snap.Store[0]))
	}
	if !bytes.Equal(snap.Base, snap.Store[0][:snap.OrigLen]) {
		return nil, fmt.Errorf("piecetable: base bytes do not match the original store")
	}
	for i, o := range snap.Journal {
		if o.Seq != Version(i) {
			return nil, fmt.Errorf("piecetable: journal op %d has seq %d", i, o.Seq)
		}
		if err := checkRecs(o.Del, snap.Store); err != nil {
			return nil, fmt.Errorf("piecetable: journal op %d deleted pieces: %w", i, err)
		}
		if err := checkRecs(o.Ins, snap.Store); err != nil {
			return nil, fmt.Errorf("piecetable: journal op %d inserted pieces: %w", i, err)
		}
	}
	for _, c := range snap.Compacted {
		if c.Buf < 0 || c.Buf >= len(snap.Store) || c.Start < 0 || c.End < c.Start || c.End > len(snap.Store[c.Buf]) {
			return nil, fmt.Errorf("piecetable: compacted origin %d..%d is outside the store", c.Start, c.End)
		}
	}

	texts := make([][]byte, len(snap.Store))
	for i, t := range snap.Store {
		texts[i] = append([]byte(nil), t...)
	}
	store := &Store{texts: texts}
	var recs []PieceRec
	if snap.OrigLen > 0 {
		recs = []PieceRec{{Buf: int(Original), Start: 0, Length: snap.OrigLen}}
	}
	doc := &Doc{store: store, tree: NewPieceBTree(WidthFor(snap.OrigLen+1<<20), recs)}
	s := NewRestoredSession(doc, snap.Journal, snap.GroupState, snap.OrigLen)
	// NewRestoredSession continues numbering after the highest group in the
	// journal, which can be lower than the original's next id when a group was
	// opened and produced no op. The snapshot carries the exact value.
	s.group = snap.NextGroup
	for _, c := range snap.Compacted {
		s.compacted = append(s.compacted, insOrigin{buf: c.Buf, start: c.Start, end: c.End, group: c.Group})
	}
	return s, nil
}

// Encode serialises a snapshot. The byte blobs cross as base64 through JSON.
func (snap Snapshot) Encode() ([]byte, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("piecetable: encode snapshot: %w", err)
	}
	return data, nil
}

// DecodeSnapshot parses and validates an encoded snapshot, returning a live
// session. A truncated or garbage payload is an error, never a panic.
func DecodeSnapshot(data []byte) (*Session, error) {
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("piecetable: decode snapshot: %w", err)
	}
	return Restore(snap)
}

// checkRecs refuses a piece record that points outside the captured stores,
// before a replay could index past a buffer.
func checkRecs(recs []PieceRec, store [][]byte) error {
	for _, r := range recs {
		if r.Buf < 0 || r.Buf >= len(store) {
			return fmt.Errorf("buffer %d is outside the store", r.Buf)
		}
		if r.Start < 0 || r.Length < 0 || r.Start+r.Length > len(store[r.Buf]) {
			return fmt.Errorf("span %d..%d is outside buffer %d", r.Start, r.Start+r.Length, r.Buf)
		}
	}
	return nil
}
