package control

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// The header is JSON on purpose: the action and its arguments should be
// readable when something is wrong. This asserts that rather than leaving it as
// an intention — a future change that moved the op into a byte would pass every
// other test in this file.
func TestHeaderIsReadable(t *testing.T) {
	base := uint64(41)
	h, body := EncodeRequest(Request{ID: 3, Op: "apply", Path: "/w/a.go", Author: 2, Base: &base,
		Hunks: []Hunk{{Start: 10, End: 20, Text: "hello"}}})
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"op":"apply"`, `"path":"/w/a.go"`, `"base":41`, `"start":10`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("header %s does not contain %s", raw, want)
		}
	}
	// ...and the text is in the body, not the header.
	if strings.Contains(string(raw), "hello") {
		t.Errorf("document bytes leaked into the header: %s", raw)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q", body)
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
