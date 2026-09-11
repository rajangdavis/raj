package control

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"raj/internal/prog"
)

// The header is JSON on purpose: the action and its arguments should be
// readable when something is wrong. This asserts that rather than leaving it as
// an intention — a future change that moved the op into a byte would pass every
// other test in this file.
// The header describes the frame; the body carries the bytes. That split is
// what keeps document text out of a field an encoder might rewrite, and it
// outlived the JSON the header used to be written in.
func TestDocumentBytesStayInTheBody(t *testing.T) {
	base := uint64(41)
	h, body := EncodeRequest(Request{ID: 3, Op: "apply", Path: "/w/a.go", Author: 2, Base: &base,
		Hunks: []Hunk{{Start: 10, End: 20, Text: "hello"}}})

	raw := encodeHeader(h)
	if bytes.Contains(raw, []byte("hello")) {
		t.Errorf("document bytes leaked into the header: %x", raw)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q", body)
	}
	// The header still says where in the body that text is, and how much of it
	// belongs to which hunk.
	back, err := decodeHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Hunks) != 1 || back.Hunks[0].Start != 10 || back.Hunks[0].Len != len("hello") {
		t.Errorf("hunk meta = %+v", back.Hunks)
	}
	if back.Op != "apply" || back.Path != "/w/a.go" || back.Base == nil || *back.Base != 41 {
		t.Errorf("header = %+v", back)
	}
}

func TestReadSpanRoundTripsThroughHeader(t *testing.T) {
	start, end := 10, 20
	h, _ := EncodeRequest(Request{Op: "text", Path: "/w/a.go", Start: &start, End: &end})
	raw := encodeHeader(h)
	back, err := decodeHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Op != "text" || back.Path != "/w/a.go" {
		t.Errorf("header = %+v", back)
	}
	if back.Start == nil || *back.Start != 10 || back.End == nil || *back.End != 20 {
		t.Errorf("span = %v..%v, want 10..20", back.Start, back.End)
	}
}

// The reason document bytes are not in the JSON.
//
// A buffer is a byte string. Go's encoder replaces anything that is not valid
// UTF-8 with U+FFFD, so `caf\xe9` goes in as 13 bytes and comes back as 15.
// Under a protocol that addresses text by byte offset against a version, every
// offset past it moves — an apply computed from what was read lands on the
// wrong span, and the base version cannot notice because the buffer never
// changed.
func TestBodyCarriesBytesJSONWouldMangle(t *testing.T) {
	nasty := []string{
		"caf\xe9 au lait\n",
		"\xff\xfe not really UTF-16\n",
		"lone surrogate \xed\xa0\x80",
		"nul\x00byte",
	}
	for _, s := range nasty {
		// Show that JSON really does mangle it, so this documents the reason.
		enc, _ := json.Marshal(map[string]string{"t": s})
		var back map[string]string
		json.Unmarshal(enc, &back)
		if len(back["t"]) == len(s) && back["t"] == s {
			t.Logf("note: JSON happened to preserve %q", s)
		}

		var buf bytes.Buffer
		h, body := EncodeResponse(Response{OK: true, Spans: []Span{{Text: s, Author: 2}}})
		if err := WriteFrame(&buf, h, body); err != nil {
			t.Fatal(err)
		}
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeResponse(f)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text() != s {
			t.Errorf("%q came back %q", s, got.Text())
		}
		if len(got.Text()) != len(s) {
			t.Errorf("length changed: %d in, %d out — every offset past this moves",
				len(s), len(got.Text()))
		}
	}
}

func TestFrameRoundTrip(t *testing.T) {
	base := uint64(7)
	reqs := []Request{
		{ID: 1, Op: "ping"},
		{ID: 2, Op: "text", Path: "/w/\xe9.go"},
		{ID: 3, Op: "apply", Path: "/w/a.go", Author: 5, Base: &base, Hunks: []Hunk{
			{Start: 0, End: 5, Text: "x"},
			{Start: 9, End: 9, Text: ""},
			{Start: 12, End: 14, Text: "\x00\xff multi\nline"},
		}},
		{ID: 4, Op: "patch", Path: "/w/a.go", Author: 5, DumpID: 9, PatchText: "whole\nfile\ntext"},
	}
	var buf bytes.Buffer
	for _, r := range reqs {
		h, body := EncodeRequest(r)
		if err := WriteFrame(&buf, h, body); err != nil {
			t.Fatal(err)
		}
	}
	// Read them back off one stream: framing has to survive several messages
	// in the buffer at once, which is the case a newline-delimited reader gets
	// wrong once anything else writes to the stream.
	for _, want := range reqs {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRequest(f)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID || got.Op != want.Op || got.Path != want.Path || got.Author != want.Author {
			t.Errorf("got %+v, want %+v", got, want)
		}
		if got.DumpID != want.DumpID || got.PatchText != want.PatchText {
			t.Errorf("got %+v, want %+v", got, want)
		}
		if len(got.Hunks) != len(want.Hunks) {
			t.Fatalf("hunks = %d, want %d", len(got.Hunks), len(want.Hunks))
		}
		for i := range got.Hunks {
			if got.Hunks[i] != want.Hunks[i] {
				t.Errorf("hunk %d = %+v, want %+v", i, got.Hunks[i], want.Hunks[i])
			}
		}
	}
}

// Base 0 is a real version and must not be read as "no base". That is the whole
// reason it is a pointer: an apply with no base is refused, an apply against
// version 0 is not.
func TestBaseZeroSurvives(t *testing.T) {
	zero := uint64(0)
	h, body := EncodeRequest(Request{Op: "apply", Base: &zero})
	var buf bytes.Buffer
	WriteFrame(&buf, h, body)
	f, _ := ReadFrame(&buf)
	got, err := DecodeRequest(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Base == nil || *got.Base != 0 {
		t.Errorf("base = %v, want a present zero", got.Base)
	}
	h, body = EncodeRequest(Request{Op: "apply"})
	buf.Reset()
	WriteFrame(&buf, h, body)
	f, _ = ReadFrame(&buf)
	if got, _ = DecodeRequest(f); got.Base != nil {
		t.Errorf("base = %v, want absent", got.Base)
	}
}

// A stated -start 0 asks for the head of the file; an absent one asks for the
// whole of it. The encoder must not drop the zero, or the two collapse into
// one request and the head-read comes back as the whole buffer.
func TestStartZeroSurvives(t *testing.T) {
	zero, fourHundred := 0, 400
	h, body := EncodeRequest(Request{Op: "text", Start: &zero, End: &fourHundred})
	var buf bytes.Buffer
	WriteFrame(&buf, h, body)
	f, _ := ReadFrame(&buf)
	got, err := DecodeRequest(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Start == nil || *got.Start != 0 {
		t.Errorf("start = %v, want a present zero", got.Start)
	}
	if got.End == nil || *got.End != 400 {
		t.Errorf("end = %v, want 400", got.End)
	}
	h, body = EncodeRequest(Request{Op: "text"})
	buf.Reset()
	WriteFrame(&buf, h, body)
	f, _ = ReadFrame(&buf)
	if got, _ = DecodeRequest(f); got.Start != nil || got.End != nil {
		t.Errorf("start/end = %v/%v, want both absent", got.Start, got.End)
	}
}

// A body that does not match the lengths its header names is a disagreement
// about the format. Truncating to fit would turn a protocol bug into a
// corrupted buffer.
func TestBodyMustMatchItsHeader(t *testing.T) {
	f := Frame{Header: Header{Spans: []SpanMeta{{Len: 10}}}, Body: []byte("short")}
	if _, err := DecodeResponse(f); err == nil {
		t.Error("a body shorter than its header claimed was accepted")
	}
	f = Frame{Header: Header{Spans: []SpanMeta{{Len: 2}}}, Body: []byte("longer")}
	if _, err := DecodeResponse(f); err == nil {
		t.Error("unclaimed body bytes were ignored")
	}
	f = Frame{Header: Header{Hunks: []HunkMeta{{Len: -1}}}, Body: nil}
	if _, err := DecodeRequest(f); err == nil {
		t.Error("a negative run length was accepted")
	}
}

// A length field is a request to allocate, so it is bounded before it is used.
func TestFrameSizeIsBounded(t *testing.T) {
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], MaxFrame+1)
	if _, err := ReadFrame(bytes.NewReader(head[:])); err != errFrameTooLarge {
		t.Errorf("err = %v, want a size refusal", err)
	}
	if err := WriteFrame(&bytes.Buffer{}, Header{}, make([]byte, MaxFrame)); err != errFrameTooLarge {
		t.Errorf("err = %v, want a size refusal", err)
	}
}

func TestMalformedFramesAreRejected(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, Header{Op: "ping"}, nil)
	full := buf.Bytes()
	for n := 1; n < len(full); n++ {
		if _, err := ReadFrame(bytes.NewReader(full[:n])); err == nil {
			t.Errorf("truncated to %d of %d bytes read cleanly", n, len(full))
		}
	}
	// A header length larger than the frame that contains it.
	bad := make([]byte, 12)
	binary.LittleEndian.PutUint32(bad, 8)
	binary.LittleEndian.PutUint32(bad[4:], 999)
	if _, err := ReadFrame(bytes.NewReader(bad)); err == nil {
		t.Error("a header longer than its frame was accepted")
	}
}

// Authorship travels with the connection, not with the request. Two connected
// agents must land in distinguishable spans, and neither may claim the user.
func TestServerAssignsAnAuthorPerConnection(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	a, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Force each connection to be served, then compare what the editor saw.
	if _, err := a.Do(Request{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Do(Request{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	ed.mu.Lock()
	seen := append([]uint8(nil), ed.authors...)
	ed.mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("saw authors %v, want one per connection", seen)
	}
	if seen[0] == seen[1] {
		t.Errorf("both connections were assigned author %d", seen[0])
	}
	for _, id := range seen {
		if id < FirstAgent {
			t.Errorf("author %d is the file or the user, which a socket may not claim", id)
		}
	}
}

// Messages ride in the header rather than the body, so the one thing that can
// go wrong is EncodeResponse or DecodeResponse forgetting the field — which
// costs nothing at compile time and delivers an empty recv at runtime.
func TestResponseCarriesMessages(t *testing.T) {
	want := []Message{
		{From: AuthorUser, Text: "stop what you are doing"},
		{From: AuthorUser, Text: "second thoughts: carry on"},
	}
	h, body := EncodeResponse(Response{ID: 9, OK: true, Final: true, Messages: want})
	got, err := DecodeResponse(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != len(want) {
		t.Fatalf("messages = %+v, want %+v", got.Messages, want)
	}
	for i := range want {
		if got.Messages[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got.Messages[i], want[i])
		}
	}
}

// Truncated rides in the header like buffers and messages; the failure mode is
// EncodeResponse or DecodeResponse dropping it, which compiles and silently
// loses the only sign that a per-file cap cut a file down.
func TestResponseCarriesTruncatedFiles(t *testing.T) {
	want := []TruncatedFile{{Path: "/w/big.md", Shown: 20, Total: 214}}
	h, body := EncodeResponse(Response{ID: 9, OK: true, Final: true, Truncated: want})
	got, err := DecodeResponse(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Truncated) != len(want) {
		t.Fatalf("truncated = %+v, want %+v", got.Truncated, want)
	}
	if got.Truncated[0] != want[0] {
		t.Errorf("truncated[0] = %+v, want %+v", got.Truncated[0], want[0])
	}
}

// A frame carries a checksum, and a frame that fails it is refused rather than
// parsed. The point is not bit rot on a Unix socket — there is none — it is
// that an encoder bug becomes a named error here instead of a wrong offset
// three calls later.
func TestCorruptFrameIsRefused(t *testing.T) {
	var buf bytes.Buffer
	h, body := EncodeRequest(Request{ID: 1, Op: "apply", Path: "/w/a.go",
		Hunks: []Hunk{{Start: 0, End: 4, Text: "hello"}}})
	if err := WriteFrame(&buf, h, body); err != nil {
		t.Fatal(err)
	}
	good := buf.Bytes()

	// Flip one bit at every position past the length prefix. Every one of them
	// must be caught: this is the assertion that the checksum covers the whole
	// frame and not just the part that was convenient.
	for i := 4; i < len(good); i++ {
		corrupt := append([]byte{}, good...)
		corrupt[i] ^= 0x01
		if _, err := ReadFrame(bytes.NewReader(corrupt)); err == nil {
			t.Errorf("a frame with byte %d flipped was accepted", i)
		}
	}
}

func TestChecksumSurvivesAnEmptyBody(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Header{ID: 2, Op: "ping"}, nil); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Op != "ping" || len(f.Body) != 0 {
		t.Errorf("frame = %+v", f.Header)
	}
}

// A truncated frame is a different failure from a corrupt one, and both are
// refused.
func TestShortFrameIsRefused(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Header{ID: 3, Op: "ping"}, nil); err != nil {
		t.Fatal(err)
	}
	good := buf.Bytes()
	for cut := 1; cut < len(good); cut++ {
		if _, err := ReadFrame(bytes.NewReader(good[:cut])); err == nil {
			t.Errorf("a frame cut to %d bytes was accepted", cut)
		}
	}
}

// The build revision rides every response, so a client that missed the
// handshake still learns what it is talking to; empty means unknown, and
// unknown is absence — no field is emitted, so a peer that predates the
// field decodes the frame unchanged.
func TestSrcVersionCrossesTheWire(t *testing.T) {
	var buf bytes.Buffer
	h, body := EncodeResponse(Response{ID: 4, OK: true, Final: true, SrcVersion: "abc123"})
	if err := WriteFrame(&buf, h, body); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.SrcVersion != "abc123" {
		t.Errorf("SrcVersion = %q, want abc123", got.SrcVersion)
	}

	// Empty is not sent: the header program carries no hSrcVersion op at
	// all, which is what "an old peer ignores it" and "an old server omits
	// it" both reduce to.
	h, _ = EncodeResponse(Response{ID: 4, OK: true, Final: true})
	ops, err := prog.Decode(encodeHeader(h), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Code == hSrcVersion {
			t.Error("an empty SrcVersion still emitted a header field")
		}
	}
}

// The stamp is in connection.send, not in the handlers, so it cannot be
// forgotten by a verb: whatever the client asks first — handshake or not —
// the answer names the build it came from.
func TestServerStampsItsBuildRevision(t *testing.T) {
	old := srcVersion
	defer func() { srcVersion = old }()
	srcVersion = "srv-abc"

	ed := newFakeEditor(t, nil)
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, op := range []string{"ping", "buffers"} {
		res, err := c.Do(Request{Op: op})
		if err != nil {
			t.Fatal(err)
		}
		if res.SrcVersion != "srv-abc" {
			t.Errorf("%s: SrcVersion = %q, want the stamped build revision", op, res.SrcVersion)
		}
	}
}
