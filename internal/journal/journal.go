// Package journal is the on-disk format for the editor's op log: the origin
// base, each author's appended piece blobs, the ops, the group decisions, the
// author table, the session blob and the last-write marker.
//
// It is deliberately independent of internal/piecetable. The log outlives any
// one in-memory representation, and the spec puts the engine choice (phase 0 a
// plain append-only file, later SQLite) behind a record interface, so this
// package defines its own record structs and its own integer widths rather
// than importing the types that happen to hold the same facts today. A later
// task converts piecetable.Op to journal.Op and back at the seam; the two
// packages never import each other.
//
// # Format
//
// A file is an 8-byte magic ("RAJLOG01"), a format version, a flags word, the
// header body's length and CRC, then the body (workspace root and identity),
// then records. Every integer is fixed-width little-endian: framing lengths
// are u32, byte-blob lengths and document coordinates are u64, authors, kinds
// and states are u8. Fixed widths are what make the tail scan possible — the
// reader knows where every length lives without parsing a varint, so a short
// or corrupt record is a boundary, not a guess.
//
// Each record is a u32 payload length, a u8 kind, a u32 CRC (IEEE, over the
// length, kind and payload), then the payload. A record is assembled first and
// appended in one Write, so a crash leaves a short final record rather than a
// splice of two; the reader stops at the first short or bad-checksum record,
// reports the offset, and the caller truncates there.
//
// Nil and empty byte blobs are the same on the wire: both encode as a zero
// length and decode as a non-nil empty slice.
package journal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// Magic identifies a journal file. It precedes the version so a reader can
// tell this apart from any other file without trusting a length.
const Magic = "RAJLOG01"

// FormatVersion is the version stamped in the header. A reader refuses a file
// from a different version rather than guessing at its layout.
//
// Version 2 adds the session version to the Written marker.
const FormatVersion uint16 = 2

// Framing. headerPrefixLen is the fixed part before the variable body: magic,
// version, flags, body length and body CRC. recordHeaderLen is the fixed part
// before a record payload: length, kind and CRC. pieceBytes is the encoded
// width of one piece, used to bound a decode allocation by the bytes present.
const (
	headerPrefixLen = len(Magic) + 2 + 2 + 4 + 4
	recordHeaderLen = 4 + 1 + 4
	pieceBytes      = 1 + 8 + 8
)

// Limits. Every length on disk is checked against one of these before it is
// used to slice, so a corrupt or hostile length is a damaged tail rather than
// an allocation.
const (
	// MaxHeaderBytes caps the workspace root and identity.
	MaxHeaderBytes = 1 << 20
	// MaxStringBytes caps a single path, identity, name or hash.
	MaxStringBytes = 1 << 20
	// MaxBlobBytes caps one base, store or session blob.
	MaxBlobBytes = 1 << 30
	// MaxRecs caps the piece records on one side of an op.
	MaxRecs = 1 << 20
	// MaxRecordBytes caps one whole record payload. A u32 could name 4 GiB;
	// the lower bound keeps a corrupt length off a 32-bit int.
	MaxRecordBytes = 1 << 30
)

// crcTable is the checksum algorithm for the whole format. IEEE is the one
// hash/crc32 makes cheap without a polynomial to carry around.
var crcTable = crc32.MakeTable(crc32.IEEE)

var (
	// ErrBadMagic is a file that is not a journal.
	ErrBadMagic = errors.New("journal: bad magic")
	// ErrVersion is a journal from a format version this build does not know.
	ErrVersion = errors.New("journal: unsupported format version")
	// ErrHeader is a header that is present but malformed.
	ErrHeader = errors.New("journal: malformed header")
	// ErrDamaged is a log whose tail must be truncated before appending.
	ErrDamaged = errors.New("journal: damaged tail")

	errNilRecord = errors.New("journal: nil record")
)

// RecordKind tags a record. It starts at 1 so a zeroed byte is not a valid
// record, which makes an all-zero region fail loudly rather than decode.
type RecordKind uint8

const (
	KindBase     RecordKind = 1 + iota // origin path, hash and bytes
	KindStore                          // one author's appended blob
	KindOp                             // one applied change
	KindDecision                       // a group's state
	KindAuthor                         // one author-table row
	KindSession                        // the session blob
	KindWritten                        // the bytes a save last put on disk
)

func (k RecordKind) String() string {
	switch k {
	case KindBase:
		return "base"
	case KindStore:
		return "store"
	case KindOp:
		return "op"
	case KindDecision:
		return "decision"
	case KindAuthor:
		return "author"
	case KindSession:
		return "session"
	case KindWritten:
		return "written"
	}
	return fmt.Sprintf("RecordKind(%d)", uint8(k))
}

// OpKind mirrors piecetable.OpKind: an ordinary edit, an undo or a redo.
type OpKind uint8

const (
	OpEdit OpKind = iota
	OpUndo
	OpRedo
)

// GroupState mirrors piecetable.GroupState. Its order is the persisted
// format, so Accepted stays the zero value and a group nothing mentions is
// accepted.
type GroupState uint8

const (
	StateAccepted GroupState = iota
	StateProposed
	StateRejected
)

// ParticipantKind is what an author is, stored rather than inferred from the
// id so a second human is a row and not a renumbering.
type ParticipantKind uint8

const (
	Human ParticipantKind = iota
	Agent
)

// Record is one entry in the log. The unexported method seals the set: the
// codec only ever writes the kinds below.
type Record interface{ recordKind() RecordKind }

// Base is the origin: the file the log started from. Bytes is the content as
// first read, Hash a digest of it to detect a workspace that moved underneath
// the editor.
type Base struct {
	Path  string
	Hash  string
	Bytes []byte
}

func (Base) recordKind() RecordKind { return KindBase }

// StoreAppend is one author's append to the piece store: Blob was appended at
// byte Start in that author's buffer. Replaying the appends in order rebuilds
// the store; no other record carries these bytes.
type StoreAppend struct {
	Author uint8
	Start  uint64
	Blob   []byte
}

func (StoreAppend) recordKind() RecordKind { return KindStore }

// Piece addresses bytes in the store: Buf is the author's buffer (the store
// index), and [Start, Start+Length) is the span.
type Piece struct {
	Buf    uint8
	Start  uint64
	Length uint64
}

// Op is one applied change, the same facts piecetable.Op holds: replace the
// Del pieces with the Ins pieces at Pos. Seq is the version the op produced,
// Undoes the op it reverses or 0, Group ties ops that undo together, and Kind
// labels an ordinary edit against an undo or redo.
type Op struct {
	Seq    uint64
	Author uint8
	Pos    uint64
	Del    []Piece
	Ins    []Piece
	Undoes uint64
	Group  uint64
	Kind   OpKind
}

func (Op) recordKind() RecordKind { return KindOp }

// Decision records a group's review state. Accepted is the default, so a log
// that never mentions a group means accepted.
type Decision struct {
	Group uint64
	State GroupState
}

func (Decision) recordKind() RecordKind { return KindDecision }

// Author is one author-table row: the byte its text carries, the durable
// identity behind it, a display name, and what kind of writer it is.
type Author struct {
	ID       uint8
	Identity string
	Name     string
	Kind     ParticipantKind
}

func (Author) recordKind() RecordKind { return KindAuthor }

// Session is the view-state blob: tabs, cursors, scroll, focus. The journal
// stores the bytes; the shape of them is internal/app's business.
type Session struct {
	Blob []byte
}

func (Session) recordKind() RecordKind { return KindSession }

// Written records the file bytes a save put on disk: Path names the file, Hash
// digests the exact encoded bytes written, and Version is the session version
// those bytes were written at. It is additive to Base, which stays the origin:
// with history kept a save does not truncate the log, so restore accepts a log
// whose disk matches either the origin or the last write, and a restore whose
// disk matches the last write baselines clean at Version rather than dirty
// against the origin.
type Written struct {
	Path    string
	Hash    string
	Version uint64
}

func (Written) recordKind() RecordKind { return KindWritten }

// Header is the file-level preamble: which workspace the log belongs to and
// which identity wrote it. It is written once, at create.
type Header struct {
	Root     string
	Identity string
}

// Writer appends records to a journal. It holds the file open and does no
// buffering of its own: Append writes one record in one call and Flush fsyncs.
// A caller that wants to batch says so by calling Flush once, after as many
// appends as it dares to lose.
type Writer struct {
	f     *os.File
	h     Header
	frame []byte
}

// Create writes a fresh header and returns a writer positioned after it. An
// existing file at path is replaced.
func Create(path string, h Header) (*Writer, error) {
	body, err := encodeHeader(h)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxHeaderBytes {
		return nil, fmt.Errorf("journal: header of %d bytes exceeds %d", len(body), MaxHeaderBytes)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, 0, headerPrefixLen+len(body))
	prefix = append(prefix, Magic...)
	prefix = binary.LittleEndian.AppendUint16(prefix, FormatVersion)
	prefix = binary.LittleEndian.AppendUint16(prefix, 0) // flags, reserved
	prefix = binary.LittleEndian.AppendUint32(prefix, uint32(len(body)))
	prefix = binary.LittleEndian.AppendUint32(prefix, crc32.Checksum(body, crcTable))
	prefix = append(prefix, body...)
	if _, err := f.Write(prefix); err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{f: f, h: h}, nil
}

// Header reports the header the writer was created with.
func (w *Writer) Header() Header { return w.h }

// OpenWriter opens an existing journal for appending. A damaged tail is
// refused: appending after it would bury the records before it behind bytes
// the reader stops on. A caller repairs by opening the log, truncating and
// calling OpenWriter again.
func OpenWriter(path string) (*Writer, error) {
	l, err := Open(path)
	if err != nil {
		return nil, err
	}
	if l.Damaged {
		return nil, fmt.Errorf("%w: %d bytes past offset %d", ErrDamaged, l.Size-l.Tail, l.Tail)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, h: l.Header}, nil
}

// Append writes one record. The frame is assembled first and written with a
// single Write, so an interrupted append leaves a short record the reader
// truncates rather than a splice of two. Nothing is flushed until Flush or
// Close.
func (w *Writer) Append(r Record) error {
	kind, payload, err := encodeRecord(r)
	if err != nil {
		return err
	}
	if len(payload) > MaxRecordBytes {
		return fmt.Errorf("journal: record of %d bytes exceeds %d", len(payload), MaxRecordBytes)
	}
	w.frame = w.frame[:0]
	w.frame = binary.LittleEndian.AppendUint32(w.frame, uint32(len(payload)))
	w.frame = append(w.frame, byte(kind))
	sum := crc32.Update(0, crcTable, w.frame) // length and kind
	sum = crc32.Update(sum, crcTable, payload)
	w.frame = binary.LittleEndian.AppendUint32(w.frame, sum)
	w.frame = append(w.frame, payload...)
	n, err := w.f.Write(w.frame)
	if err != nil {
		return err
	}
	if n != len(w.frame) {
		return io.ErrShortWrite
	}
	return nil
}

// Flush fsyncs the journal so every record appended so far survives a crash.
func (w *Writer) Flush() error { return w.f.Sync() }

// Close flushes and closes, reporting the first error and closing regardless.
func (w *Writer) Close() error {
	err := w.f.Sync()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Log is a scanned journal: the header, every valid record in order, and how
// far the valid prefix reaches.
type Log struct {
	Path    string
	Header  Header
	Records []Record
	// Tail is the length of the valid prefix: the offset of the first short
	// or checksum-bad record, or the file size when the whole file scanned.
	Tail int64
	// Size is the file size at scan time.
	Size int64
	// Damaged is Tail < Size: records past Tail were not read.
	Damaged bool
}

// Open scans path and returns the valid prefix. It stops at the first record
// whose header is short, whose length is out of range, whose bytes are cut
// off, or whose checksum does not match; everything before that is returned
// and Tail points at the bad record so the caller can truncate.
//
// An I/O error, a bad magic or an unknown version is an error. A torn header
// is a damaged, empty log: its records have not been written yet, so the
// caller truncates to zero and starts again.
func Open(path string) (*Log, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l := &Log{Path: path, Size: int64(len(data))}
	if len(data) == 0 {
		return l, nil // not yet written; Tail 0 is not damage
	}
	if len(data) < headerPrefixLen {
		l.Damaged = true
		return l, nil // header torn mid-write; truncate to 0
	}
	if got := string(data[:len(Magic)]); got != Magic {
		return nil, fmt.Errorf("%w: %q", ErrBadMagic, got)
	}
	if v := binary.LittleEndian.Uint16(data[len(Magic):]); v != FormatVersion {
		return nil, fmt.Errorf("%w: %d", ErrVersion, v)
	}
	bodyLen := binary.LittleEndian.Uint32(data[len(Magic)+4:])
	bodyCRC := binary.LittleEndian.Uint32(data[len(Magic)+8:])
	if bodyLen > MaxHeaderBytes {
		return nil, fmt.Errorf("%w: body of %d bytes", ErrHeader, bodyLen)
	}
	end := headerPrefixLen + int(bodyLen)
	if end > len(data) {
		l.Damaged = true
		return l, nil // header body torn; truncate to 0
	}
	body := data[headerPrefixLen:end]
	if crc32.Checksum(body, crcTable) != bodyCRC {
		return nil, fmt.Errorf("%w: checksum", ErrHeader)
	}
	if l.Header, err = decodeHeader(body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHeader, err)
	}
	off := end
	for off < len(data) {
		start := off
		if len(data)-off < recordHeaderLen {
			return damaged(l, start)
		}
		n := binary.LittleEndian.Uint32(data[off:])
		kind := RecordKind(data[off+4])
		want := binary.LittleEndian.Uint32(data[off+5:])
		off += recordHeaderLen
		if n > MaxRecordBytes || int64(n) > int64(len(data)-off) {
			return damaged(l, start)
		}
		payload := data[off : off+int(n)]
		sum := crc32.Update(0, crcTable, data[start:start+5]) // length and kind
		sum = crc32.Update(sum, crcTable, payload)
		if sum != want {
			return damaged(l, start)
		}
		rec, err := decodeRecord(kind, payload)
		if err != nil {
			return nil, fmt.Errorf("journal: record at %d: %w", start, err)
		}
		l.Records = append(l.Records, rec)
		off += int(n)
	}
	l.Tail = int64(off)
	l.Damaged = l.Tail < l.Size
	return l, nil
}

// damaged records that the valid prefix ends at start and hands back the log.
func damaged(l *Log, start int) (*Log, error) {
	l.Tail = int64(start)
	l.Damaged = true
	return l, nil
}

// Truncate cuts path to at bytes. It is how a caller repairs the damaged tail
// Open reported; truncating to a record boundary leaves the records before it
// intact.
func Truncate(path string, at int64) error {
	if at < 0 {
		return fmt.Errorf("journal: negative truncation offset %d", at)
	}
	return os.Truncate(path, at)
}

// Truncate cuts the file to the end of its valid prefix.
func (l *Log) Truncate() error { return Truncate(l.Path, l.Tail) }

// encodeHeader lays out the header body: root then identity.
func encodeHeader(h Header) ([]byte, error) {
	e := &encoder{}
	e.str(h.Root)
	e.str(h.Identity)
	return e.b, e.err
}

func decodeHeader(b []byte) (Header, error) {
	d := &decoder{b: b}
	h := Header{Root: d.str(), Identity: d.str()}
	if d.err != nil {
		return Header{}, d.err
	}
	if d.off != len(b) {
		return Header{}, fmt.Errorf("journal: %d trailing header bytes", len(b)-d.off)
	}
	return h, nil
}

// encodeRecord returns a record's kind and its payload. Pointers and values
// are both accepted so a caller can pass whichever it built.
func encodeRecord(r Record) (RecordKind, []byte, error) {
	if r == nil {
		return 0, nil, errNilRecord
	}
	switch v := r.(type) {
	case *Base:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *StoreAppend:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *Op:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *Decision:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *Author:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *Written:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	case *Session:
		if v == nil {
			return 0, nil, errNilRecord
		}
		r = *v
	}
	e := &encoder{}
	switch v := r.(type) {
	case Base:
		e.str(v.Path)
		e.str(v.Hash)
		e.blob(v.Bytes)
	case StoreAppend:
		e.u8(v.Author)
		e.u64(v.Start)
		e.blob(v.Blob)
	case Op:
		encodeOp(e, v)
	case Decision:
		e.u64(v.Group)
		e.u8(uint8(v.State))
	case Author:
		e.u8(v.ID)
		e.str(v.Identity)
		e.str(v.Name)
		e.u8(uint8(v.Kind))
	case Written:
		e.str(v.Path)
		e.str(v.Hash)
		e.u64(v.Version)
	case Session:
		e.blob(v.Blob)
	default:
		return 0, nil, fmt.Errorf("journal: unknown record type %T", r)
	}
	if e.err != nil {
		return 0, nil, e.err
	}
	return r.recordKind(), e.b, nil
}

// encodeOp is separate because an op is the one record with a nested shape.
// The Del list comes before the Ins list; within a piece, Buf then Start then
// Length.
func encodeOp(e *encoder, o Op) {
	e.u64(o.Seq)
	e.u8(o.Author)
	e.u64(o.Pos)
	e.count(len(o.Del))
	for _, p := range o.Del {
		e.piece(p)
	}
	e.count(len(o.Ins))
	for _, p := range o.Ins {
		e.piece(p)
	}
	e.u64(o.Undoes)
	e.u64(o.Group)
	e.u8(uint8(o.Kind))
}

// decodeRecord rebuilds one record from a checksum-verified payload. It
// insists the payload is consumed exactly, so a decode is unambiguous and a
// valid record re-encodes to the same bytes.
func decodeRecord(kind RecordKind, b []byte) (Record, error) {
	d := &decoder{b: b}
	var r Record
	switch kind {
	case KindBase:
		r = Base{Path: d.str(), Hash: d.str(), Bytes: d.blob()}
	case KindStore:
		r = StoreAppend{Author: d.u8(), Start: d.u64(), Blob: d.blob()}
	case KindOp:
		o := Op{Seq: d.u64(), Author: d.u8(), Pos: d.u64()}
		o.Del = d.pieces()
		o.Ins = d.pieces()
		o.Undoes = d.u64()
		o.Group = d.u64()
		o.Kind = OpKind(d.u8())
		r = o
	case KindDecision:
		r = Decision{Group: d.u64(), State: GroupState(d.u8())}
	case KindAuthor:
		r = Author{ID: d.u8(), Identity: d.str(), Name: d.str(), Kind: ParticipantKind(d.u8())}
	case KindWritten:
		r = Written{Path: d.str(), Hash: d.str(), Version: d.u64()}
	case KindSession:
		r = Session{Blob: d.blob()}
	default:
		return nil, fmt.Errorf("journal: unknown record kind %d", kind)
	}
	if d.err != nil {
		return nil, d.err
	}
	if d.off != len(b) {
		return nil, fmt.Errorf("journal: %d trailing record bytes", len(b)-d.off)
	}
	if err := validate(r); err != nil {
		return nil, err
	}
	return r, nil
}

// validate rejects an enum value this build does not know. The checksum has
// already passed, so this is a version problem rather than a torn record.
func validate(r Record) error {
	switch v := r.(type) {
	case Op:
		if v.Kind > OpRedo {
			return fmt.Errorf("journal: unknown op kind %d", v.Kind)
		}
	case Decision:
		if v.State > StateRejected {
			return fmt.Errorf("journal: unknown group state %d", v.State)
		}
	case Author:
		if v.Kind > Agent {
			return fmt.Errorf("journal: unknown participant kind %d", v.Kind)
		}
	}
	return nil
}

// encoder appends fixed-width little-endian fields to a growing buffer. The
// first error sticks, so a layout can be written field by field and checked
// once at the end.
type encoder struct {
	b   []byte
	err error
}

func (e *encoder) u8(v uint8)   { e.b = append(e.b, v) }
func (e *encoder) u32(v uint32) { e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = binary.LittleEndian.AppendUint64(e.b, v) }

func (e *encoder) count(n int) {
	if e.err != nil {
		return
	}
	if n < 0 || n > MaxRecs {
		e.err = fmt.Errorf("journal: %d items exceeds %d", n, MaxRecs)
		return
	}
	e.u32(uint32(n))
}

func (e *encoder) piece(p Piece) {
	e.u8(p.Buf)
	e.u64(p.Start)
	e.u64(p.Length)
}

func (e *encoder) str(s string) {
	if e.err != nil {
		return
	}
	if len(s) > MaxStringBytes {
		e.err = fmt.Errorf("journal: string of %d bytes exceeds %d", len(s), MaxStringBytes)
		return
	}
	e.u32(uint32(len(s)))
	e.b = append(e.b, s...)
}

func (e *encoder) blob(b []byte) {
	if e.err != nil {
		return
	}
	if len(b) > MaxBlobBytes {
		e.err = fmt.Errorf("journal: blob of %d bytes exceeds %d", len(b), MaxBlobBytes)
		return
	}
	e.u64(uint64(len(b)))
	e.b = append(e.b, b...)
}

// decoder reads fields back in the order they were written. It records the
// first failure and otherwise yields zero values, so a malformed payload
// cannot panic its way through a layout.
type decoder struct {
	b   []byte
	off int
	err error
}

func (d *decoder) fail(msg string) {
	if d.err == nil {
		d.err = fmt.Errorf("journal: %s", msg)
	}
}

func (d *decoder) need(n int) bool {
	if d.err != nil {
		return false
	}
	if n < 0 || len(d.b)-d.off < n {
		d.fail(fmt.Sprintf("short field at byte %d", d.off))
		return false
	}
	return true
}

func (d *decoder) u8() uint8 {
	if !d.need(1) {
		return 0
	}
	v := d.b[d.off]
	d.off++
	return v
}

func (d *decoder) u32() uint32 {
	if !d.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v
}

func (d *decoder) u64() uint64 {
	if !d.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v
}

func (d *decoder) count() int {
	n := d.u32()
	if d.err != nil {
		return 0
	}
	if n > MaxRecs {
		d.fail(fmt.Sprintf("%d items exceeds %d", n, MaxRecs))
		return 0
	}
	return int(n)
}

func (d *decoder) piece() Piece {
	return Piece{Buf: d.u8(), Start: d.u64(), Length: d.u64()}
}

func (d *decoder) pieces() []Piece {
	n := d.count()
	if n == 0 {
		return nil
	}
	// Bound the allocation by the bytes actually present, so a corrupt count
	// cannot name a million pieces that are not there.
	if n > (len(d.b)-d.off)/pieceBytes {
		d.fail(fmt.Sprintf("%d pieces exceeds %d remaining bytes", n, len(d.b)-d.off))
		return nil
	}
	out := make([]Piece, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, d.piece())
		if d.err != nil {
			return out
		}
	}
	return out
}

func (d *decoder) str() string {
	n := d.u32()
	if d.err != nil {
		return ""
	}
	if n > MaxStringBytes {
		d.fail(fmt.Sprintf("string of %d bytes exceeds %d", n, MaxStringBytes))
		return ""
	}
	if !d.need(int(n)) {
		return ""
	}
	s := string(d.b[d.off : d.off+int(n)])
	d.off += int(n)
	return s
}

func (d *decoder) blob() []byte {
	n := d.u64()
	if d.err != nil {
		return nil
	}
	if n > MaxBlobBytes {
		d.fail(fmt.Sprintf("blob of %d bytes exceeds %d", n, MaxBlobBytes))
		return nil
	}
	if !d.need(int(n)) {
		return nil
	}
	b := d.b[d.off : d.off+int(n)]
	d.off += int(n)
	return b
}
