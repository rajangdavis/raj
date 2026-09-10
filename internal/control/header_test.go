package control

import (
	"bytes"
	"reflect"
	"testing"

	"raj/internal/prog"
)

// fullHeader is a header with every field set, so a test can assert that all of
// them cross the wire.
func fullHeader() Header {
	base := uint64(41)
	return Header{
		ID: 7, Op: "apply", Path: "/w/main.go", Author: 3, Token: "t0ken",
		Base: &base, Cancel: 2, Group: 9, Identity: "agent-1", Name: "Agent",
		Argv: []string{"go", "test", "./..."}, Dir: "/w",
		Query: &SearchQuery{Text: "f.*o", Include: "*.go", Exclude: "vendor/**", Regex: true, Word: true},
		Hunks: []HunkMeta{{Start: 0, End: 4, Len: 2}, {Start: 10, End: 10, Len: 5}},
		Exit:  3, Stream: 2, OutLen: 12, Final: true, OK: true, Err: "boom",
		Root: "/w", PID: 4242, Version: 70000, Bytes: 12345, Lines: 678, Files: 12, Capped: true,
		Dirty:        []DirtyBuffer{{Path: "/w/a.go", AgentOnly: true}, {Path: "/w/b.go"}},
		Stats:        ExecStats{Runs: 5, Stale: 1, AgentOnly: 2},
		Participants: []Participant{{ID: 1, Identity: "i", Name: "n", Kind: KindAgent, Connected: true}},
		Groups:       []Group{{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 3, Bytes: 40, First: 1, Last: 9}},
		Messages:     []Message{{From: 1, Text: "hello"}},
		Buffers:      []Buffer{{Path: "/w/a.go", Version: 3, Dirty: true, Bytes: 90, Lines: 5}},
		Matches:      []MatchMeta{{Line: 2, Col: 3, Len: 4, PathLen: 7, TextLen: 8, ByteStart: 10, ByteEnd: 14}},
		Conflicts:    []Conflict{{Index: 1, At: 8, Hunk: Hunk{Start: 1, End: 2, Text: "x"}}},
		Spans:        []SpanMeta{{Len: 5, Author: 1}, {Len: 6, Author: 2}},
		DiffJSON:     `[{"id":4,"hunks":[{"start":1,"end":2,"old":"a","new":"b"}],"moved":0}]`,
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	base := uint64(41)
	want := fullHeader()

	got, err := decodeHeader(encodeHeader(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Base == nil || *got.Base != base {
		t.Fatalf("base = %v", got.Base)
	}
	got.Base, want.Base = nil, nil
	if got.Query == nil || *got.Query != *want.Query {
		t.Fatalf("query = %+v, want %+v", got.Query, want.Query)
	}
	got.Query, want.Query = nil, nil

	if len(got.Hunks) != 2 || got.Hunks[1] != want.Hunks[1] {
		t.Errorf("hunks = %+v", got.Hunks)
	}
	if len(got.Groups) != 1 || got.Groups[0] != want.Groups[0] {
		t.Errorf("groups = %+v, want %+v", got.Groups, want.Groups)
	}
	if len(got.Participants) != 1 || got.Participants[0] != want.Participants[0] {
		t.Errorf("participants = %+v", got.Participants)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != want.Conflicts[0] {
		t.Errorf("conflicts = %+v", got.Conflicts)
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"id", got.ID, want.ID}, {"op", got.Op, want.Op}, {"path", got.Path, want.Path},
		{"author", got.Author, want.Author}, {"token", got.Token, want.Token},
		{"cancel", got.Cancel, want.Cancel}, {"group", got.Group, want.Group},
		{"identity", got.Identity, want.Identity}, {"name", got.Name, want.Name},
		{"dir", got.Dir, want.Dir}, {"exit", got.Exit, want.Exit},
		{"stream", got.Stream, want.Stream}, {"outlen", got.OutLen, want.OutLen},
		{"final", got.Final, want.Final}, {"ok", got.OK, want.OK}, {"err", got.Err, want.Err},
		{"root", got.Root, want.Root}, {"pid", got.PID, want.PID},
		{"version", got.Version, want.Version},
		{"bytes", got.Bytes, want.Bytes}, {"lines", got.Lines, want.Lines},
		{"files", got.Files, want.Files},
		{"capped", got.Capped, want.Capped}, {"stats", got.Stats, want.Stats},
		{"diffjson", got.DiffJSON, want.DiffJSON},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(got.Argv) != 3 || got.Argv[2] != "./..." {
		t.Errorf("argv = %q", got.Argv)
	}
	if len(got.Spans) != 2 || got.Spans[1] != want.Spans[1] {
		t.Errorf("spans = %+v", got.Spans)
	}
}

// A header states what it means and nothing else. The JSON form carried every
// field name in every frame whether the verb used it or not.
func TestEmptyHeaderIsTiny(t *testing.T) {
	b := encodeHeader(Header{})
	if len(b) != 2 {
		t.Errorf("an empty header is %d bytes, want just the two program bytes: %x", len(b), b)
	}
	ping := encodeHeader(Header{ID: 3, Op: "ping"})
	if len(ping) > 16 {
		t.Errorf("a ping header is %d bytes: %x", len(ping), ping)
	}
}

// Zero is a real version and "not stated" is not the same thing: an apply with
// no base is refused, an apply based on version zero is the first one against a
// fresh buffer.
func TestBaseDistinguishesZeroFromAbsent(t *testing.T) {
	zero := uint64(0)
	got, err := decodeHeader(encodeHeader(Header{Base: &zero}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Base == nil {
		t.Fatal("a base of zero decoded as absent")
	}
	if *got.Base != 0 {
		t.Errorf("base = %d, want 0", *got.Base)
	}
	if got, _ := decodeHeader(encodeHeader(Header{})); got.Base != nil {
		t.Error("an absent base decoded as present")
	}
}

// The case that forced PathLen into the JSON header, and the reason it is gone:
// a filename on Linux is arbitrary bytes.
func TestNonUTF8PathSurvivesTheHeader(t *testing.T) {
	name := "/w/caf\xe9.txt"
	got, err := decodeHeader(encodeHeader(Header{Path: name, Op: "open"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != name {
		t.Fatalf("path = %q, want %q", got.Path, name)
	}
	// And through a whole frame, where it used to travel in the body.
	var buf bytes.Buffer
	h, body := EncodeRequest(Request{Op: "open", Path: name})
	if err := WriteFrame(&buf, h, body); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	req, err := DecodeRequest(f)
	if err != nil {
		t.Fatal(err)
	}
	if req.Path != name {
		t.Fatalf("path across a frame = %q, want %q", req.Path, name)
	}
}

// A header is a description, not a request, so a field this build has never
// heard of is skipped rather than refused.
func TestUnknownHeaderFieldIsSkipped(t *testing.T) {
	b := encodeHeader(Header{ID: 5, Op: "ping"})
	// Splice in a field from a later raj, in the argument range.
	later := prog.Encode([]prog.Op{{Code: 0x7e, Payload: []byte("from the future")}})
	spliced := append(append([]byte{}, b...), later[2:]...)

	got, err := decodeHeader(spliced)
	if err != nil {
		t.Fatalf("an unknown field was fatal: %v", err)
	}
	if got.ID != 5 || got.Op != "ping" {
		t.Errorf("header = %+v, want the known fields intact", got)
	}
}

func TestMalformedHeaderIsRefused(t *testing.T) {
	if _, err := decodeHeader([]byte("not a program")); err == nil {
		t.Error("garbage decoded as a header")
	}
	if _, err := decodeHeader(nil); err == nil {
		t.Error("an empty header decoded")
	}
}

// The frame that carries a program should itself be free of zero bytes, so
// nothing downstream that treats a byte string as a C string can truncate it.
func TestHeaderHasNoZeroBytes(t *testing.T) {
	base := uint64(65536)
	h := Header{ID: 300, Op: "apply", Path: "/w/a.go", Base: &base, Version: 1 << 20,
		Buffers: []Buffer{{Path: "/w/a.go", Version: 256, Bytes: 70000}}}
	if prog.HasNul(encodeHeader(h)) {
		t.Error("the header put a zero byte in the frame")
	}
}

// Header has no struct tags because nothing marshals it. This is the test that
// notices if something starts to — a field added with a tag, or a caller
// reaching for json.Marshal on a Header, means two encodings again and the
// second one will drift silently.
func TestHeaderHasNoJSONTags(t *testing.T) {
	rt := reflect.TypeOf(Header{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if tag, ok := f.Tag.Lookup("json"); ok {
			t.Errorf("Header.%s carries a json tag (%q); the wire encodes it "+
				"through encodeHeader, so the tag is read by nothing", f.Name, tag)
		}
	}
}

// Every exported field is carried. A field added to Header without a line in
// encodeHeader would cross the wire as its zero value, silently — which JSON
// could not do, since it marshalled whatever was there.
func TestEveryHeaderFieldRoundTrips(t *testing.T) {
	rt := reflect.TypeOf(Header{})
	filled := fullHeader()
	got, err := decodeHeader(encodeHeader(filled))
	if err != nil {
		t.Fatal(err)
	}
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(filled)
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if gv.Field(i).IsZero() && !wv.Field(i).IsZero() {
			t.Errorf("Header.%s did not survive the round trip; is it missing "+
				"a line in encodeHeader?", name)
		}
	}
}

// A verb is one byte on the wire, not its name. It stays a string in Go,
// because that is what the handlers switch on and what a person reads.
func TestVerbsTravelAsCodes(t *testing.T) {
	b := encodeHeader(Header{ID: 1, Op: "buffers"})
	if bytes.Contains(b, []byte("buffers")) {
		t.Errorf("the verb name went out as text: %x", b)
	}
	got, err := decodeHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Op != "buffers" {
		t.Errorf("op = %q, want buffers", got.Op)
	}
}

// Every op string the code sends must be in the table, or it costs bytes for
// nothing. This is the test that fails when someone adds a verb.
func TestEveryVerbHasACode(t *testing.T) {
	for _, op := range []string{
		"ping", "buffers", "text", "open", "apply", "save", "version", "search",
		"groups", "accept", "reject", "exec", "execcheck", "stats", "hello",
		"cancel", "recv", "snapshot", "prog", "diff",
	} {
		if _, ok := verbCodes[op]; !ok {
			t.Errorf("op %q has no code, so it crosses the wire as text", op)
		}
	}
}

// A verb the table does not know still has to arrive. Falling back to text
// costs a few bytes; falling back to zero would silently deliver an empty op,
// which is the failure mode worth spending bytes to avoid.
func TestUnknownVerbFallsBackToText(t *testing.T) {
	got, err := decodeHeader(encodeHeader(Header{Op: "something-new"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Op != "something-new" {
		t.Errorf("op = %q, want it carried as text", got.Op)
	}
}

// The two enums the header carries are closed sets, so they are numbers too —
// with the same text fallback for a member this build has not heard of.
func TestEnumsTravelAsCodes(t *testing.T) {
	h := Header{
		Participants: []Participant{{ID: 1, Identity: "i", Name: "n", Kind: KindAgent, Connected: true}},
		Groups:       []Group{{ID: 2, Path: "/w/a.go", State: "proposed", Ops: 1}},
	}
	b := encodeHeader(h)
	for _, word := range []string{"agent", "proposed"} {
		if bytes.Contains(b, []byte(word)) {
			t.Errorf("%q went out as text: %x", word, b)
		}
	}
	got, err := decodeHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Participants[0].Kind != KindAgent {
		t.Errorf("kind = %q", got.Participants[0].Kind)
	}
	if got.Groups[0].State != "proposed" {
		t.Errorf("state = %q", got.Groups[0].State)
	}

	odd := Header{
		Participants: []Participant{{ID: 1, Kind: Kind("robot"), Connected: true}},
		Groups:       []Group{{ID: 2, State: "half-accepted"}},
	}
	back, err := decodeHeader(encodeHeader(odd))
	if err != nil {
		t.Fatal(err)
	}
	if back.Participants[0].Kind != Kind("robot") || !back.Participants[0].Connected {
		t.Errorf("participant = %+v", back.Participants[0])
	}
	if back.Groups[0].State != "half-accepted" {
		t.Errorf("state = %q", back.Groups[0].State)
	}
}
