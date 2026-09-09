package control

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// The wire: length-prefixed frames, an inspectable header, and a raw byte body.
//
//	frame  = u32 length | u32 crc | u32 headerLength | header program | body bytes
//
// Length-prefixed rather than newline-delimited, because the brittleness blamed
// on JSON-RPC is really newline-delimited JSON over stdio, and a length prefix
// removes the newline dependency.
//
// The header was JSON, on the grounds that serialisation is unmeasurable
// against a model round trip so there was nothing to buy by making it opaque.
// It is opcodes now — see header.go — because the argument had stopped holding.
// Document bytes already bypassed JSON, so every header field that could hold
// arbitrary bytes needed a special case to escape into the body; requests
// already arrive as programs, so the frame carrying one was described in an
// encoding nothing else used; and inspectability was worth paying for when a
// header might be read off a socket by a stranger, which is not this. Both ends
// build from one commit.
//
// The split survives it. The header describes the frame, the body carries bytes
// no encoder touches, and that boundary is the thing that matters.
//
// # Why document bytes cannot go in the header
//
// A buffer is a byte string. Go's JSON encoder replaces anything that is not
// valid UTF-8 with U+FFFD, so `caf\xe9` — one Latin-1 byte in an older source — goes
// in as 13 bytes and comes back as 15. Under a protocol that addresses text by
// byte offset against a version, that is not a display problem: every offset
// past it moves, so an apply computed from what was read lands on the wrong
// span, and the base version cannot notice because the buffer never changed.
//
// # Spans, and why the body is a concatenation
//
// The body is not one blob per message; it is the message's byte runs laid end
// to end, with the header naming each one's length. That falls out of how the
// document is already stored. A read is a sequence of `piecetable.Span`s, each
// with an author, so the header carries `[{len, author}, ...]` and the body
// carries the runs — the pieces cross the wire in the shape they are held in,
// and a caller learns who wrote every byte without a second call and without
// the editor concatenating and re-splitting.
//
// This is what makes authorship a property of the transport rather than
// something a harness reconstructs. An agent can see which spans are its own,
// which are the user's, and which came from another agent.

// Header is the action, and everything about it except the bytes.
// No struct tags: nothing marshals a Header. Its fields cross the wire through
// encodeHeader in header.go, and the codes live there beside the encoding
// rather than here beside the declarations, so one file holds both halves of
// the round trip. The types the header carries — Buffer, Group, Participant —
// do keep their tags, because `raj ctl --json` marshals those for people and
// scripts.
type Header struct {
	ID int
	Op string

	// Path is a byte string, like every other field here. It used to need a
	// special case — a filename on Linux is arbitrary bytes, and Go's JSON
	// encoder replaces anything that is not valid UTF-8 with U+FFFD, which
	// addresses a file that does not exist — so an invalid path travelled in
	// the body with a length beside it. The header is not JSON any more and
	// the special case is gone.
	Path string

	// Author identifies the writer. 0 means unset; the editor assigns one per
	// connection and refuses a request that names a different one.
	Author uint8

	// Token is the shared secret a TCP client presents. It rides in the header
	// on every request rather than in a handshake, so the server stays
	// stateless about it and a reconnect needs no special case.
	//
	// It is therefore in every frame, which is worth naming: anything dumping
	// this wire dumps the token with it. The header being opcodes rather than
	// JSON does not hide it — a byte string in a length-prefixed field is not
	// encrypted, only unquoted. The alternative, an opaque handshake, buys
	// nothing, since a dump of the connection would carry it either way.
	Token string

	// Base is a pointer so an apply against version 0 is distinguishable from
	// an apply that forgot to say. The second is refused.
	Base *uint64

	// Hunks carry offsets and the LENGTH of their replacement text; the text
	// itself is in the body, in this order.
	Hunks []HunkMeta

	// Query is a search. It is plain JSON: every field is a pattern the caller
	// typed, so it is exactly what you want visible in a frame.
	Query *SearchQuery

	// Cancel names a request id to abandon. It is answered on the reading
	// goroutine without touching the editor, which is what lets it arrive
	// while the request it cancels is still running.
	Cancel int

	// Line and Col carry a goto: the 1-based position to move the editor's
	// cursor to. Zero means absent; the editor defaults a missing line to the
	// one the cursor is already on and a missing column to the margin.
	Line int
	Col  int

	// Argv and Dir are exec's command. Plain JSON: a command line is exactly
	// what you want legible in a frame.
	Group    uint64
	Identity string
	Name     string
	Argv     []string
	Dir      string

	// Exit, Dirty and Stats are exec's answers. Stream marks an output frame:
	// 1 stdout, 2 stderr, with the bytes in the body.
	Exit         int
	Dirty        []DirtyBuffer
	Stats        ExecStats
	Participants []Participant
	Groups       []Group

	// Messages is what a parked recv answers with. They stay in the header
	// rather than moving to the body: a message is text a person typed into a
	// prompt, not document bytes off a disk, so it is valid UTF-8 by
	// construction and legible in a frame dump is worth more than a length
	// prefix here.
	Messages []Message

	Stream uint8
	OutLen int

	// Final marks the last frame of a response. A streamed result is several
	// frames sharing an id; everything else is one frame with Final set.
	Final bool

	// Response fields.
	OK      bool
	Err     string
	Root    string
	PID     int
	Version uint64
	Buffers []Buffer
	Files   int
	Capped  bool
	// Matches carry their position in the header and their path and line text
	// in the body, for the same reason document bytes are not in the JSON: a
	// path is arbitrary bytes and a matched line is document content.
	Matches   []MatchMeta
	Conflicts []Conflict

	// Spans describe the body of a read: one entry per authored run, in
	// document order. Their lengths sum to the body that follows them.
	Spans []SpanMeta
}

// HunkMeta is a hunk with its text moved to the body.
type HunkMeta struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Len   int `json:"len"`
}

// MatchMeta is a search hit with its path and line text moved to the body.
type MatchMeta struct {
	Line    int `json:"line"`
	Col     int `json:"col"`
	Len     int `json:"len"`
	PathLen int `json:"path_len"`
	TextLen int `json:"text_len"`
}

// SpanMeta is one authored run of the document.
type SpanMeta struct {
	Len    int   `json:"len"`
	Author uint8 `json:"author"`
}

// MaxFrame bounds one message. A length field is a request to allocate, so it
// is checked before it is used; 64 MiB is past the largest file raj will open
// and far short of a denial of service.
// crcTable is Castagnoli, which Go implements with the SSE4.2 instruction on
// any machine raj runs on — so the check costs a few nanoseconds per frame
// rather than a pass over the bytes.
//
// What it is for is worth being honest about. On a Unix socket, which is how
// raj is normally driven, the kernel copies memory and there is no corruption
// to catch; over TCP the transport has already checksummed the segment. This
// does not meaningfully protect against a flipped bit on the wire.
//
// What it does catch is us. A frame whose length says one thing and whose
// payload says another is an encoder bug — a field written with the wrong
// width, a body assembled from runs that do not sum to their header — and
// those are silent otherwise: the reader parses whatever bytes it was handed
// and hands the caller a header that decoded cleanly and means something else.
// The check turns that class of bug into a named error at the boundary where
// it happened rather than into a wrong offset three calls later.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

const MaxFrame = 64 << 20

var (
	errFrameTooLarge = errors.New("control: frame exceeds the maximum size")
	errBadFrame      = errors.New("control: malformed frame")
)

// Frame is a decoded message: the action, and its bytes.
type Frame struct {
	Header Header
	Body   []byte
}

// Split returns the body cut into the runs the header names. It fails rather
// than truncating: a body that does not match its lengths means the sender and
// this reader disagree about the format, and guessing which is right is how a
// protocol bug becomes a corrupted buffer.
func (f Frame) Split(lengths []int) ([][]byte, error) {
	out := make([][]byte, 0, len(lengths))
	off := 0
	for i, n := range lengths {
		if n < 0 || off+n > len(f.Body) {
			return nil, fmt.Errorf("%w: run %d wants %d bytes at %d of %d",
				errBadFrame, i, n, off, len(f.Body))
		}
		out = append(out, f.Body[off:off+n])
		off += n
	}
	if off != len(f.Body) {
		return nil, fmt.Errorf("%w: %d unclaimed body bytes", errBadFrame, len(f.Body)-off)
	}
	return out, nil
}

// WriteFrame emits one message.
func WriteFrame(w io.Writer, h Header, body []byte) error {
	head := encodeHeader(h)
	total := 8 + len(head) + len(body)
	if total > MaxFrame {
		return errFrameTooLarge
	}
	// One write. A length that reaches the peer without its payload is a reader
	// blocked mid-frame, and on a socket two writes can be split by anything.
	frame := make([]byte, 4+total)
	binary.LittleEndian.PutUint32(frame, uint32(total))
	binary.LittleEndian.PutUint32(frame[8:], uint32(len(head)))
	copy(frame[12:], head)
	copy(frame[12+len(head):], body)
	binary.LittleEndian.PutUint32(frame[4:], crc32.Checksum(frame[8:], crcTable))
	_, err := w.Write(frame)
	return err
}

// ReadFrame reads one message.
func ReadFrame(r io.Reader) (Frame, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return Frame{}, err
	}
	n := binary.LittleEndian.Uint32(size[:])
	if n > MaxFrame {
		return Frame{}, errFrameTooLarge
	}
	if n < 8 {
		return Frame{}, fmt.Errorf("%w: frame of %d bytes has no checksum and header length", errBadFrame, n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, err
	}
	want := binary.LittleEndian.Uint32(payload)
	if got := crc32.Checksum(payload[4:], crcTable); got != want {
		return Frame{}, fmt.Errorf("%w: checksum %08x, computed %08x over %d bytes",
			errBadFrame, want, got, n-4)
	}
	hn := binary.LittleEndian.Uint32(payload[4:])
	if uint64(hn)+8 > uint64(n) {
		return Frame{}, fmt.Errorf("%w: header of %d bytes in a frame of %d", errBadFrame, hn, n)
	}
	var f Frame
	h, err := decodeHeader(payload[8 : 8+hn])
	if err != nil {
		return Frame{}, err
	}
	f.Header = h
	f.Body = payload[8+hn:]
	return f, nil
}

// ---------- request and response as frames ----------

// EncodeRequest lays a request out as a header and a body: the path, then each
// hunk's text, in order.
func EncodeRequest(req Request) (Header, []byte) {
	h := Header{ID: req.ID, Op: req.Op, Author: req.Author, Base: req.Base, Token: req.Token,
		Query: req.Query, Cancel: req.Cancel, Argv: req.Argv, Dir: req.Dir,
		Identity: req.Identity, Name: req.Name, Group: req.Group, Line: req.Line, Col: req.Col}

	var body []byte
	if req.Op == "prog" {
		// The whole body, unclaimed by any header length: see DecodeRequest.
		return h, req.Program
	}
	h.Path = req.Path
	for _, x := range req.Hunks {
		h.Hunks = append(h.Hunks, HunkMeta{Start: x.Start, End: x.End, Len: len(x.Text)})
		body = append(body, x.Text...)
	}
	return h, body
}

func DecodeRequest(f Frame) (Request, error) {
	req := Request{ID: f.Header.ID, Op: f.Header.Op, Path: f.Header.Path,
		Author: f.Header.Author, Base: f.Header.Base, Query: f.Header.Query, Token: f.Header.Token,
		Cancel: f.Header.Cancel, Argv: f.Header.Argv, Dir: f.Header.Dir,
		Identity: f.Header.Identity, Name: f.Header.Name, Group: f.Header.Group, Line: f.Header.Line, Col: f.Header.Col}

	if f.Header.Op == "prog" {
		// The program is the body, whole — and it is claimed here rather than
		// through Split, which exists to cut a body into the runs a header
		// names and would call an unnamed one unclaimed. It goes in the body
		// for the same reason document bytes do: it is bytes, and an encoder
		// that rewrote anything invalid in it would move every offset it
		// contains.
		req.Program = f.Body
		return req, nil
	}
	lengths := make([]int, 0, len(f.Header.Hunks))
	for _, m := range f.Header.Hunks {
		lengths = append(lengths, m.Len)
	}
	runs, err := f.Split(lengths)
	if err != nil {
		return req, err
	}
	for i, m := range f.Header.Hunks {
		req.Hunks = append(req.Hunks, Hunk{Start: m.Start, End: m.End, Text: string(runs[i])})
	}
	return req, nil
}

// EncodeResponse puts the document bytes in the body, span by span, so a read
// carries authorship in the shape the store holds it.
func EncodeResponse(res Response) (Header, []byte) {
	h := Header{ID: res.ID, OK: res.OK, Err: res.Err, Root: res.Root, PID: res.PID,
		Version: res.Version, Buffers: res.Buffers, Conflicts: res.Conflicts,
		Files: res.Files, Capped: res.Capped, Final: res.Final, Author: res.Author,
		Exit: res.Exit, Dirty: res.Dirty, Stats: res.Stats, Stream: res.Stream,
		Participants: res.Participants, Groups: res.Groups, Messages: res.Messages}
	var body []byte
	if res.Stream != 0 {
		// Command output is bytes off a pipe: whatever the process wrote, not
		// text, so JSON would replace anything invalid in it.
		h.OutLen = len(res.Out)
		body = append(body, res.Out...)
	}
	for _, m := range res.Matches {
		h.Matches = append(h.Matches, MatchMeta{Line: m.Line, Col: m.Col, Len: m.Len,
			PathLen: len(m.Path), TextLen: len(m.Text)})
		body = append(body, m.Path...)
		body = append(body, m.Text...)
	}
	for _, s := range res.Spans {
		h.Spans = append(h.Spans, SpanMeta{Len: len(s.Text), Author: s.Author})
		body = append(body, s.Text...)
	}
	return h, body
}

func DecodeResponse(f Frame) (Response, error) {
	res := Response{ID: f.Header.ID, OK: f.Header.OK, Err: f.Header.Err, Root: f.Header.Root,
		PID: f.Header.PID, Version: f.Header.Version,
		Buffers: f.Header.Buffers, Conflicts: f.Header.Conflicts,
		Files: f.Header.Files, Capped: f.Header.Capped, Final: f.Header.Final,
		Author: f.Header.Author, Exit: f.Header.Exit, Dirty: f.Header.Dirty,
		Stats: f.Header.Stats, Stream: f.Header.Stream,
		Participants: f.Header.Participants, Groups: f.Header.Groups,
		Messages: f.Header.Messages}
	lengths := make([]int, 0, 2*len(f.Header.Matches)+len(f.Header.Spans)+1)
	if f.Header.Stream != 0 {
		lengths = append(lengths, f.Header.OutLen)
	}
	outRuns := len(lengths)
	for _, m := range f.Header.Matches {
		lengths = append(lengths, m.PathLen, m.TextLen)
	}
	matchRuns := len(lengths)
	for _, m := range f.Header.Spans {
		lengths = append(lengths, m.Len)
	}
	runs, err := f.Split(lengths)
	if err != nil {
		return res, err
	}
	if outRuns == 1 {
		res.Out = string(runs[0])
	}
	for i, m := range f.Header.Matches {
		res.Matches = append(res.Matches, SearchMatch{
			Path: string(runs[outRuns+2*i]), Text: string(runs[outRuns+2*i+1]),
			Line: m.Line, Col: m.Col, Len: m.Len})
	}
	for i, m := range f.Header.Spans {
		res.Spans = append(res.Spans, Span{Text: string(runs[matchRuns+i]), Author: m.Author})
	}
	return res, nil
}
