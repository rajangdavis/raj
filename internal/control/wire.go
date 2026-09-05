package control

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// The wire: length-prefixed frames, an inspectable header, and a raw byte body.
//
//	frame  = u32 length | u32 headerLength | header JSON | body bytes
//
// HARNESS-BROKER-AGENT.md asks for two things that look opposed. Length-prefixed
// frames, because the brittleness blamed on JSON-RPC is really newline-delimited
// JSON over stdio and a length prefix removes the newline dependency. And
// "optimise the wire for inspectability", because serialisation is unmeasurable
// against a model round trip so there is nothing to buy by making it opaque.
//
// They are only opposed if the whole message has one encoding. Splitting the
// frame resolves it: the header is JSON — an action with its arguments, which is
// what you want to read when something is wrong — and the body is document bytes
// that no encoder touches.
//
// # Why document bytes cannot go in the JSON
//
// A buffer is a byte string. Go's encoder replaces anything that is not valid
// UTF-8 with U+FFFD, so `caf\xe9` — one Latin-1 byte in an older source — goes
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
type Header struct {
	ID int    `json:"id"`
	Op string `json:"op,omitempty"`

	// Path is in the header when it is valid UTF-8, which is nearly always, so
	// that a frame stays readable. When it is not — a filename on Linux is
	// arbitrary bytes, and one written in Latin-1 is a real file — it moves to
	// the body with everything else and PathLen says so. The alternative was to
	// let JSON replace those bytes with U+FFFD and address a file that does not
	// exist.
	Path    string `json:"path,omitempty"`
	PathLen int    `json:"path_len,omitempty"`

	// Author identifies the writer. 0 means unset; the editor assigns one per
	// connection and refuses a request that names a different one.
	Author uint8 `json:"author,omitempty"`

	// Token is the shared secret a TCP client presents. It rides in the header
	// on every request rather than in a handshake, so the server stays
	// stateless about it and a reconnect needs no special case.
	//
	// It is therefore in the readable part of every frame, which is a real cost
	// of having made frames readable: anything dumping this wire dumps the
	// token with it. The alternative — an opaque handshake — buys nothing,
	// since a dump of the connection would carry it either way.
	Token string `json:"token,omitempty"`

	// Base is a pointer so an apply against version 0 is distinguishable from
	// an apply that forgot to say. The second is refused.
	Base *uint64 `json:"base,omitempty"`

	// Hunks carry offsets and the LENGTH of their replacement text; the text
	// itself is in the body, in this order.
	Hunks []HunkMeta `json:"hunks,omitempty"`

	// Query is a search. It is plain JSON: every field is a pattern the caller
	// typed, so it is exactly what you want visible in a frame.
	Query *SearchQuery `json:"query,omitempty"`

	// Cancel names a request id to abandon. It is answered on the reading
	// goroutine without touching the editor, which is what lets it arrive
	// while the request it cancels is still running.
	Cancel int `json:"cancel,omitempty"`

	// Argv and Dir are exec's command. Plain JSON: a command line is exactly
	// what you want legible in a frame.
	Group    uint64   `json:"group,omitempty"`
	Identity string   `json:"identity,omitempty"`
	Name     string   `json:"name,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	Dir      string   `json:"dir,omitempty"`

	// Exit, Dirty and Stats are exec's answers. Stream marks an output frame:
	// 1 stdout, 2 stderr, with the bytes in the body.
	Exit         int           `json:"exit,omitempty"`
	Dirty        []DirtyBuffer `json:"dirty,omitempty"`
	Stats        ExecStats     `json:"stats,omitempty"`
	Participants []Participant `json:"participants,omitempty"`
	Groups       []Group       `json:"groups,omitempty"`
	Stream       uint8         `json:"stream,omitempty"`
	OutLen       int           `json:"out_len,omitempty"`

	// Final marks the last frame of a response. A streamed result is several
	// frames sharing an id; everything else is one frame with Final set.
	Final bool `json:"final,omitempty"`

	// Response fields.
	OK      bool     `json:"ok,omitempty"`
	Err     string   `json:"error,omitempty"`
	Root    string   `json:"root,omitempty"`
	PID     int      `json:"pid,omitempty"`
	Version uint64   `json:"version,omitempty"`
	Buffers []Buffer `json:"buffers,omitempty"`
	Files   int      `json:"files,omitempty"`
	Capped  bool     `json:"capped,omitempty"`
	// Matches carry their position in the header and their path and line text
	// in the body, for the same reason document bytes are not in the JSON: a
	// path is arbitrary bytes and a matched line is document content.
	Matches   []MatchMeta `json:"matches,omitempty"`
	Conflicts []Conflict  `json:"conflicts,omitempty"`

	// Spans describe the body of a read: one entry per authored run, in
	// document order. Their lengths sum to the body that follows them.
	Spans []SpanMeta `json:"spans,omitempty"`
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
	head, err := json.Marshal(h)
	if err != nil {
		return err
	}
	total := 4 + len(head) + len(body)
	if total > MaxFrame {
		return errFrameTooLarge
	}
	// One write. A length that reaches the peer without its payload is a reader
	// blocked mid-frame, and on a socket two writes can be split by anything.
	frame := make([]byte, 4+total)
	binary.LittleEndian.PutUint32(frame, uint32(total))
	binary.LittleEndian.PutUint32(frame[4:], uint32(len(head)))
	copy(frame[8:], head)
	copy(frame[8+len(head):], body)
	_, err = w.Write(frame)
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
	if n < 4 {
		return Frame{}, fmt.Errorf("%w: frame of %d bytes has no header length", errBadFrame, n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, err
	}
	hn := binary.LittleEndian.Uint32(payload)
	if uint64(hn)+4 > uint64(n) {
		return Frame{}, fmt.Errorf("%w: header of %d bytes in a frame of %d", errBadFrame, hn, n)
	}
	var f Frame
	if err := json.Unmarshal(payload[4:4+hn], &f.Header); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	f.Body = payload[4+hn:]
	return f, nil
}

// ---------- request and response as frames ----------

// EncodeRequest lays a request out as a header and a body: the path, then each
// hunk's text, in order.
func EncodeRequest(req Request) (Header, []byte) {
	h := Header{ID: req.ID, Op: req.Op, Author: req.Author, Base: req.Base, Token: req.Token,
		Query: req.Query, Cancel: req.Cancel, Argv: req.Argv, Dir: req.Dir,
		Identity: req.Identity, Name: req.Name, Group: req.Group}
	var body []byte
	if utf8.ValidString(req.Path) {
		h.Path = req.Path
	} else {
		h.PathLen = len(req.Path)
		body = append(body, req.Path...)
	}
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
		Identity: f.Header.Identity, Name: f.Header.Name, Group: f.Header.Group}
	lengths := make([]int, 0, len(f.Header.Hunks)+1)
	if f.Header.PathLen > 0 {
		lengths = append(lengths, f.Header.PathLen)
	}
	pathRuns := len(lengths)
	for _, m := range f.Header.Hunks {
		lengths = append(lengths, m.Len)
	}
	runs, err := f.Split(lengths)
	if err != nil {
		return req, err
	}
	if pathRuns == 1 {
		req.Path = string(runs[0])
	}
	for i, m := range f.Header.Hunks {
		req.Hunks = append(req.Hunks, Hunk{Start: m.Start, End: m.End, Text: string(runs[pathRuns+i])})
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
		Participants: res.Participants, Groups: res.Groups}
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
		Participants: f.Header.Participants, Groups: f.Header.Groups}
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
