package control

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"raj/internal/prog"
)

// i64p is an int64 pointer for a literal Entry whose Size is present.
func i64p(n int64) *int64 { return &n }

// fullHeader is a header with every field set, so a test can assert that all of
// them cross the wire.
func fullHeader() Header {
	base := uint64(41)
	return Header{
		ID: 7, Op: "apply", Path: "/w/main.go", NewPath: "/w/renamed.go", Author: 3, Token: "t0ken",
		Base: &base, Cancel: 2, Group: 9, Identity: "agent-1", Name: "Agent",
		Argv: []string{"go", "test", "./..."}, Dir: "/w",
		Query: &SearchQuery{Text: "f.*o", Include: "*.go", Exclude: "vendor/**", Path: "internal", Regex: true, Word: true, Hidden: true},
		Hunks: []HunkMeta{{Start: 0, End: 4, Len: 2}, {Start: 10, End: 10, Len: 5}},
		Exit:  3, Stream: 2, OutLen: 12, Final: true, OK: true, Err: "boom",
		Root: "/w", PID: 4242, Version: 70000, Bytes: 12345, Lines: 678, Files: 12, Capped: true,
		Considered: 34,
		Found:      true, FindStart: 1234, FindEnd: 1239, FindCount: 7,
		Dirty:        []DirtyBuffer{{Path: "/w/a.go", AgentOnly: true}, {Path: "/w/b.go"}},
		Stats:        ExecStats{Runs: 5, Stale: 1, AgentOnly: 2},
		Participants: []Participant{{ID: 1, Identity: "i", Name: "n", Kind: KindAgent, Connected: true}},
		Groups:       []Group{{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 3, Bytes: 40, First: 1, Last: 9, Hunks: 2, Moved: 1}},
		Messages:     []Message{{From: 1, Text: "hello"}},
		Buffers:      []Buffer{{Path: "/w/a.go", Version: 3, Dirty: true, Bytes: 90, Pending: 2, Moved: 1, Lines: 5}},
		Truncated:    []TruncatedFile{{Path: "/w/big.md", Shown: 20, Total: 214}},
		Matches:      []MatchMeta{{Line: 2, Col: 3, Len: 4, PathLen: 7, TextLen: 8, LineStart: 9, LineEnd: 18, ByteStart: 10, ByteEnd: 14, Version: 6}},
		Conflicts:    []Conflict{{Index: 1, At: 8, Group: 7, Hunk: Hunk{Start: 1, End: 2, Text: "x"}}},
		Warnings:     []GroupOverlap{{Group: 7, Author: 3, Start: 6, End: 12}},
		Spans:        []SpanMeta{{Len: 5, Author: 1}, {Len: 6, Author: 2}},
		DiffJSON:     `[{"id":4,"hunks":[{"start":1,"end":2,"old":"a","new":"b"}],"moved":0}]`,
		StatesJSON:   `[{"off":0,"len":5,"group":0,"state":"accepted"}]`,
		ReviewList:   true,
		Annotated:    true,
		Discard:      true,
		Withdraw:     true,
		Hidden:       true,
		Remains:      true,
		Created:      true,

		Paths:         []string{"/w/a.go", "/w/b.go"},
		ClaimAdd:      true,
		ClaimClear:    true,
		Claims:        []string{"/w/a.go"},
		ClaimWarnings: []string{"/w/gone.go skipped"},
		ClaimOverlaps: []ClaimOverlap{{Path: "/w/a.go", Identity: "bob", Author: 4}},
		Deletions:     []Deletion{{Path: "/w/a.go", Author: 4}},
		DirRemovals:   []DirRemoval{{Path: "/w/sub", Author: 4}},
		Proposals: []Proposal{
			{Kind: "set", Path: "/w/a.go", Author: 3, Group: 4, Start: -1, End: -1},
			{Kind: "delete", Path: "/w/b.go", Author: 4, Start: 7, End: 9},
		},
		Entries: []Entry{
			{Name: "pkg", Path: "/w/pkg", Dir: true},
			{Name: "main.go", Path: "/w/main.go", Size: i64p(9)},
		},
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
	if len(got.Matches) != 1 || got.Matches[0] != want.Matches[0] {
		t.Errorf("matches = %+v, want %+v", got.Matches, want.Matches)
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"id", got.ID, want.ID}, {"op", got.Op, want.Op}, {"path", got.Path, want.Path},
		{"newpath", got.NewPath, want.NewPath},
		{"author", got.Author, want.Author}, {"token", got.Token, want.Token},
		{"cancel", got.Cancel, want.Cancel}, {"group", got.Group, want.Group},
		{"identity", got.Identity, want.Identity}, {"name", got.Name, want.Name},
		{"dir", got.Dir, want.Dir}, {"exit", got.Exit, want.Exit},
		{"stream", got.Stream, want.Stream}, {"outlen", got.OutLen, want.OutLen},
		{"final", got.Final, want.Final}, {"ok", got.OK, want.OK}, {"err", got.Err, want.Err},
		{"root", got.Root, want.Root}, {"pid", got.PID, want.PID},
		{"version", got.Version, want.Version},
		{"bytes", got.Bytes, want.Bytes}, {"lines", got.Lines, want.Lines},
		{"files", got.Files, want.Files}, {"considered", got.Considered, want.Considered},
		{"capped", got.Capped, want.Capped}, {"stats", got.Stats, want.Stats},
		{"diffjson", got.DiffJSON, want.DiffJSON},
		{"statesjson", got.StatesJSON, want.StatesJSON},
		{"reviewlist", got.ReviewList, want.ReviewList},
		{"annotated", got.Annotated, want.Annotated},
		{"withdraw", got.Withdraw, want.Withdraw},
		{"hidden", got.Hidden, want.Hidden},
		{"remains", got.Remains, want.Remains},
		{"created", got.Created, want.Created},
		{"found", got.Found, want.Found},
		{"findstart", got.FindStart, want.FindStart},
		{"findend", got.FindEnd, want.FindEnd},
		{"findcount", got.FindCount, want.FindCount},
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
	if len(got.Deletions) != 1 || got.Deletions[0] != want.Deletions[0] {
		t.Errorf("deletions = %+v, want %+v", got.Deletions, want.Deletions)
	}
	if len(got.DirRemovals) != 1 || got.DirRemovals[0] != want.DirRemovals[0] {
		t.Errorf("dir removals = %+v, want %+v", got.DirRemovals, want.DirRemovals)
	}
	if len(got.Proposals) != 2 || got.Proposals[0] != want.Proposals[0] || got.Proposals[1] != want.Proposals[1] {
		t.Errorf("proposals = %+v, want %+v", got.Proposals, want.Proposals)
	}
}

// An ls reply's entries cross as a flat record list: name, path, kind, and a
// size that is absent for anything that is not a regular file. A zero-byte
// regular file keeps its zero rather than reading as absent, which is why the
// wire carries -1 rather than zero for "no size".
func TestHeaderKeepsEntries(t *testing.T) {
	zero, nine := int64(0), int64(9)
	want := []Entry{
		{Name: "pkg", Path: "/w/pkg", Dir: true},
		{Name: "empty", Path: "/w/empty", Size: &zero},
		{Name: "main.go", Path: "/w/main.go", Size: &nine},
	}
	got, err := decodeHeader(encodeHeader(Header{Entries: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != len(want) {
		t.Fatalf("decoded %d entries, want %d", len(got.Entries), len(want))
	}
	for i := range want {
		g, w := got.Entries[i], want[i]
		if g.Name != w.Name || g.Path != w.Path || g.Dir != w.Dir {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
		switch {
		case w.Size == nil && g.Size != nil:
			t.Errorf("entry %d size = %d, want absent", i, *g.Size)
		case w.Size != nil && g.Size == nil:
			t.Errorf("entry %d size absent, want %d", i, *w.Size)
		case w.Size != nil && *g.Size != *w.Size:
			t.Errorf("entry %d size = %d, want %d", i, *g.Size, *w.Size)
		}
	}
}

// A group's Bytes is the net change in document length, negative for any edit
// that removes text. It is the one signed number on the wire, and a negative
// one used to corrupt the groups record and drop every group after it — which
// is how `groups` listed one set while `diff` rendered three.
func TestHeaderKeepsNegativeGroupBytes(t *testing.T) {
	want := []Group{
		{ID: 1, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 3, Bytes: -42, First: 1, Last: 3},
		{ID: 2, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, Bytes: -4, First: 4, Last: 4},
	}
	got, err := decodeHeader(encodeHeader(Header{Groups: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != len(want) {
		t.Fatalf("decoded %d groups, want %d — a negative byte count truncated the list", len(got.Groups), len(want))
	}
	for i := range want {
		if got.Groups[i] != want[i] {
			t.Errorf("group %d = %+v, want %+v", i, got.Groups[i], want[i])
		}
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
		"groups", "accept", "reject", "clear", "exec", "execcheck", "stats", "hello",
		"cancel", "recv", "snapshot", "prog", "diff", "review", "claim",
		"mkdir", "delete", "deletions", "rename",
		"proposals", "revert", "token",
		"rmdir", "rmdirs", "ls",
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

// A match's LineStart rides in a sparse field of its own, so a zero line start
// must survive next to a nonzero one, and each start must stay with its own
// hit rather than shift onto the next.
func TestHeaderKeepsMatchLineStart(t *testing.T) {
	want := []MatchMeta{
		{Line: 1, Col: 0, Len: 6, PathLen: 4, LineStart: 0, ByteStart: 0, ByteEnd: 6},
		{Line: 2, Col: 1, Len: 6, PathLen: 4, LineStart: 9, ByteStart: 10, ByteEnd: 16},
	}
	got, err := decodeHeader(encodeHeader(Header{Matches: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != len(want) {
		t.Fatalf("decoded %d matches, want %d", len(got.Matches), len(want))
	}
	for i := range want {
		if got.Matches[i] != want[i] {
			t.Errorf("match %d = %+v, want %+v", i, got.Matches[i], want[i])
		}
	}
}

// A match's LineEnd rides in its own sparse field, so a zero end must survive
// next to a nonzero one and each end must stay with its own hit.
func TestHeaderKeepsMatchLineEnd(t *testing.T) {
	want := []MatchMeta{
		{Line: 1, Col: 0, Len: 6, PathLen: 4, LineStart: 0, LineEnd: 0, ByteStart: 0, ByteEnd: 6},
		{Line: 2, Col: 1, Len: 6, PathLen: 4, LineStart: 9, LineEnd: 21, ByteStart: 10, ByteEnd: 16},
	}
	got, err := decodeHeader(encodeHeader(Header{Matches: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != len(want) {
		t.Fatalf("decoded %d matches, want %d", len(got.Matches), len(want))
	}
	for i := range want {
		if got.Matches[i] != want[i] {
			t.Errorf("match %d = %+v, want %+v", i, got.Matches[i], want[i])
		}
	}
}

// A match's buffer version rides in its own sparse field, so a disk hit's zero
// must survive next to a buffer hit's revision, and each version must stay with
// its own hit rather than shift onto the next.
func TestHeaderKeepsMatchVersion(t *testing.T) {
	want := []MatchMeta{
		{Line: 1, Col: 0, Len: 6, PathLen: 4, ByteStart: 0, ByteEnd: 6},
		{Line: 2, Col: 1, Len: 6, PathLen: 4, LineStart: 9, ByteStart: 10, ByteEnd: 16, Version: 41},
		{Line: 3, Col: 1, Len: 6, PathLen: 4, LineStart: 20, ByteStart: 21, ByteEnd: 27, Version: 42},
	}
	got, err := decodeHeader(encodeHeader(Header{Matches: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != len(want) {
		t.Fatalf("decoded %d matches, want %d", len(got.Matches), len(want))
	}
	for i := range want {
		if got.Matches[i] != want[i] {
			t.Errorf("match %d = %+v, want %+v", i, got.Matches[i], want[i])
		}
	}
}

// A conflict's lease owner rides in its own sparse field, so a zero owner must
// survive next to a nonzero one and each owner must stay with its own conflict.
func TestHeaderKeepsConflictGroup(t *testing.T) {
	want := []Conflict{
		{Index: 0, At: 3, Hunk: Hunk{Start: 0, End: 5, Text: "x"}},
		{Index: 1, At: 4, Group: 12, Hunk: Hunk{Start: 8, End: 8, Text: "y"}},
	}
	got, err := decodeHeader(encodeHeader(Header{Conflicts: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != len(want) {
		t.Fatalf("decoded %d conflicts, want %d", len(got.Conflicts), len(want))
	}
	for i := range want {
		if got.Conflicts[i] != want[i] {
			t.Errorf("conflict %d = %+v, want %+v", i, got.Conflicts[i], want[i])
		}
	}
}

// A lease refusal's author and span ride with the owner in their own sparse
// field, so a zero span must survive next to a nonzero one and each triple must
// stay with its own conflict rather than shift onto the next.
func TestHeaderKeepsConflictLease(t *testing.T) {
	want := []Conflict{
		{Index: 0, At: 3, Hunk: Hunk{Start: 0, End: 5, Text: "x"}},
		{Index: 1, Group: 12, Author: 3, Start: 6, End: 11, Hunk: Hunk{Start: 8, End: 8, Text: "y"}},
	}
	got, err := decodeHeader(encodeHeader(Header{Conflicts: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != len(want) {
		t.Fatalf("decoded %d conflicts, want %d", len(got.Conflicts), len(want))
	}
	for i := range want {
		if got.Conflicts[i] != want[i] {
			t.Errorf("conflict %d = %+v, want %+v", i, got.Conflicts[i], want[i])
		}
	}
}

// A successful apply warnings ride in their own sparse field, one
// {group, author, start, end} record per warning, separate from the positional
// conflict records. An absent field decodes to nil, and the warnings coexist
// with conflicts without shifting them. Modelled on TestHeaderKeepsConflictLease.
func TestHeaderKeepsApplyWarnings(t *testing.T) {
	conflicts := []Conflict{{Index: 0, At: 3, Hunk: Hunk{Start: 0, End: 5, Text: "x"}}}
	want := []GroupOverlap{{Group: 12, Author: 3, Start: 6, End: 12}}
	got, err := decodeHeader(encodeHeader(Header{Conflicts: conflicts, Warnings: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != conflicts[0] {
		t.Errorf("conflicts = %+v, want %+v", got.Conflicts, conflicts)
	}
	if len(got.Warnings) != len(want) || got.Warnings[0] != want[0] {
		t.Errorf("warnings = %+v, want %+v", got.Warnings, want)
	}

	// Absent means none: a header with no warnings decodes to a nil slice.
	clean, err := decodeHeader(encodeHeader(Header{Version: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if clean.Warnings != nil {
		t.Errorf("clean warnings = %+v, want nil", clean.Warnings)
	}
}

// A group's overlap list rides in its own sparse field, keyed to the groups by
// position, so a group with no overlaps must decode to nil next to one that has
// them rather than borrowing the other's list.
func TestHeaderKeepsGroupOverlaps(t *testing.T) {
	want := []Group{
		{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1},
		{ID: 5, Path: "/w/a.go", Author: 3, State: "proposed", Ops: 1,
			Overlaps: &GroupOverlaps{Sets: []GroupOverlap{
				{Group: 4, Author: 2, Start: 6, End: 11},
			}}},
	}
	got, err := decodeHeader(encodeHeader(Header{Groups: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != len(want) {
		t.Fatalf("decoded %d groups, want %d", len(got.Groups), len(want))
	}
	if got.Groups[0].Overlaps != nil {
		t.Errorf("group 0 borrowed an overlap list: %+v", got.Groups[0].Overlaps)
	}
	if got.Groups[1].Overlaps == nil || len(got.Groups[1].Overlaps.Sets) != 1 ||
		got.Groups[1].Overlaps.Sets[0] != want[1].Overlaps.Sets[0] {
		t.Errorf("group 1 overlaps = %+v, want %+v", got.Groups[1].Overlaps, want[1].Overlaps)
	}
}

// A group's Invalid flag and collider ride in their own sparse field, keyed to
// the groups by position, so a valid group next to an invalid one must decode
// to invalid=false/nil and an invalid set with no namable collider must keep
// its flag without borrowing one. Modelled on TestHeaderKeepsGroupOverlaps.
func TestHeaderKeepsGroupInvalid(t *testing.T) {
	want := []Group{
		{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1},
		{ID: 5, Path: "/w/a.go", Author: 3, State: "proposed", Ops: 1,
			Invalid: true, InvalidBy: &GroupOverlap{Group: 4, Author: 2, Start: 6, End: 11}},
		{ID: 6, Path: "/w/a.go", Author: 3, State: "proposed", Ops: 1, Invalid: true},
	}
	got, err := decodeHeader(encodeHeader(Header{Groups: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != len(want) {
		t.Fatalf("decoded %d groups, want %d", len(got.Groups), len(want))
	}
	if got.Groups[0].Invalid || got.Groups[0].InvalidBy != nil {
		t.Errorf("group 0 borrowed invalid state: %+v", got.Groups[0])
	}
	if !got.Groups[1].Invalid || got.Groups[1].InvalidBy == nil ||
		*got.Groups[1].InvalidBy != *want[1].InvalidBy {
		t.Errorf("group 1 invalid = %+v, want %+v", got.Groups[1], want[1])
	}
	if !got.Groups[2].Invalid || got.Groups[2].InvalidBy != nil {
		t.Errorf("group 2 invalid/no-collider = %+v, want invalid with a nil collider", got.Groups[2])
	}
}

// A frame from before the lease owner existed carries only the five original
// conflict fields. It must decode to a zero group rather than fail, which is
// what keeps an old server talking to a new client.
func TestOldShapedConflictRecordStillDecodes(t *testing.T) {
	var w prog.Writer
	w.Num(0).Num(3).Num(0).Num(5).Str("x")
	w.Num(1).Num(4).Num(8).Num(8).Str("y")
	old := prog.Encode([]prog.Op{{Code: hConflicts, Payload: w.Done()}})

	got, err := decodeHeader(old)
	if err != nil {
		t.Fatalf("an old-shaped conflicts field was refused: %v", err)
	}
	if len(got.Conflicts) != 2 {
		t.Fatalf("conflicts = %+v, want two", got.Conflicts)
	}
	for i, c := range got.Conflicts {
		if c.Group != 0 {
			t.Errorf("conflict %d carries group %d; none was on the wire", i, c.Group)
		}
	}
	if c := got.Conflicts[1]; c.Index != 1 || c.At != 4 || c.Hunk.Text != "y" {
		t.Errorf("conflict 1 = %+v, want the five original fields intact", c)
	}
}

// -path is the newest field on the query record; a payload from before it
// existed must decode with an empty path rather than a frame error.
func TestOldShapedQueryStillDecodes(t *testing.T) {
	var w prog.Writer
	w.Str("needle").Str("*.go").Str("vendor").Bool(false).Bool(false).Bool(false)
	old := prog.Encode([]prog.Op{{Code: hQuery, Payload: w.Done()}})

	got, err := decodeHeader(old)
	if err != nil {
		t.Fatalf("an old-shaped query was refused: %v", err)
	}
	if got.Query == nil || got.Query.Text != "needle" || got.Query.Path != "" {
		t.Errorf("query = %+v, want the six original fields and no path", got.Query)
	}
}

// Pending and Moved are per-buffer counts that must survive the wire: they are
// what lets one `buffers` call say which open files hold proposed changes.
func TestBufferPendingAndMovedRoundTrip(t *testing.T) {
	want := []Buffer{
		{Path: "/w/a.go", Version: 3, Dirty: true, Bytes: 90, Lines: 5, Pending: 2, Moved: 1},
		{Path: "/w/b.go", Version: 9, Bytes: 4, Lines: 1},
	}
	got, err := decodeHeader(encodeHeader(Header{Buffers: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buffers) != len(want) {
		t.Fatalf("buffers = %+v, want %d", got.Buffers, len(want))
	}
	for i := range want {
		if got.Buffers[i] != want[i] {
			t.Errorf("buffer %d = %+v, want %+v", i, got.Buffers[i], want[i])
		}
	}
}

// Headless is per-buffer state and must cross the wire: it is how buffers tells
// a loaded buffer with no tab from one the user can see. It rides in a sparse
// field of its own, so a buffer without it decodes as tabbed.
func TestBufferHeadlessRoundTrip(t *testing.T) {
	want := []Buffer{
		{Path: "/w/a.go", Version: 3, Bytes: 90, Lines: 5, Headless: true},
		{Path: "/w/b.go", Version: 9, Bytes: 4, Lines: 1},
	}
	got, err := decodeHeader(encodeHeader(Header{Buffers: want}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buffers) != len(want) {
		t.Fatalf("buffers = %+v, want %d", got.Buffers, len(want))
	}
	for i := range want {
		if got.Buffers[i] != want[i] {
			t.Errorf("buffer %d = %+v, want %+v", i, got.Buffers[i], want[i])
		}
	}
}

// A frame from before pending and moved existed carries only the six original
// hBuffers fields. It must decode to zero counts rather than fail or invent a
// seventh buffer: the counts ride in their own field, so their absence is
// ordinary.
func TestOldShapedBufferRecordStillDecodes(t *testing.T) {
	var w prog.Writer
	w.Str("/w/a.go").Num(3).Bool(true).Num(90).Num(5).Bool(false)
	w.Str("/w/b.go").Num(1).Bool(false).Num(0).Num(1).Bool(false)
	old := prog.Encode([]prog.Op{{Code: hBuffers, Payload: w.Done()}})

	got, err := decodeHeader(old)
	if err != nil {
		t.Fatalf("an old-shaped buffers field was refused: %v", err)
	}
	if len(got.Buffers) != 2 {
		t.Fatalf("buffers = %+v, want two", got.Buffers)
	}
	for i, b := range got.Buffers {
		if b.Pending != 0 || b.Moved != 0 {
			t.Errorf("buffer %d carries counts %d/%d; neither was on the wire", i, b.Pending, b.Moved)
		}
		if b.Headless {
			t.Errorf("buffer %d reads as headless; the field was not on the wire", i)
		}
	}
	if b := got.Buffers[0]; b.Path != "/w/a.go" || !b.Dirty || b.Bytes != 90 || b.Lines != 5 {
		t.Errorf("buffer 0 = %+v, want the six original fields intact", b)
	}
}

// A record list cut off mid-record sets Reader.Bad, which used to make More
// report false and end the list one element early. It must be a named frame
// error instead: that is how a malformed groups reply once listed one change
// set while three existed.
func TestTruncatedRecordListIsRefused(t *testing.T) {
	var w prog.Writer
	w.Str("/w/a.go").Num(3).Bool(true).Num(90).Num(5).Bool(false)
	payload := w.Done()
	payload = payload[:len(payload)-1] // drop the final Active flag
	b := prog.Encode([]prog.Op{{Code: hBuffers, Payload: payload}})

	_, err := decodeHeader(b)
	if err == nil {
		t.Fatal("a truncated record list decoded as a short list")
	}
	if !errors.Is(err, errBadFrame) {
		t.Errorf("err = %v, want it to wrap errBadFrame", err)
	}
}
