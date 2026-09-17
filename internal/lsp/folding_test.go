package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// foldingRanges decodes one server answer through the real request path, so the
// method name and the params are exercised as well as the decoder.
func foldingRanges(t *testing.T, raw string) ([]FoldingRange, error) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/foldingRange", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	return RequestFoldingRanges(ctx, f.conn, "/w/a.go")
}

// The specification permits a line-only range: startCharacter and endCharacter
// are optional, and a client that assumes they are present reads a zero or
// drops the range. Without the pointer decode this answer comes back with
// HasStartChar true at zero and no way to tell the line-only form apart.
func TestFoldingRangeDecodesLineOnly(t *testing.T) {
	rs, err := foldingRanges(t, `[{"startLine":2,"endLine":8}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d ranges, want 1", len(rs))
	}
	r := rs[0]
	if r.StartLine != 2 || r.EndLine != 8 {
		t.Errorf("lines = %d..%d, want 2..8", r.StartLine, r.EndLine)
	}
	if r.HasStartChar || r.HasEndChar {
		t.Errorf("characters present for a line-only range: start=%v end=%v", r.HasStartChar, r.HasEndChar)
	}
	if r.StartCharacter != 0 || r.EndCharacter != 0 {
		t.Errorf("characters = %d..%d, want the zero values", r.StartCharacter, r.EndCharacter)
	}
	if r.Kind != "" {
		t.Errorf("kind = %q, want empty", r.Kind)
	}
}

// A range that does send characters must keep them, and an explicit zero is a
// legal character, so the presence flags have to be true rather than the value
// being non-zero. Without the flags an explicit startCharacter 0 is
// indistinguishable from an absent one.
func TestFoldingRangeDecodesCharactersAndKind(t *testing.T) {
	rs, err := foldingRanges(t, `[{"startLine":1,"endLine":4,"startCharacter":0,"endCharacter":3,"kind":"comment"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d ranges, want 1", len(rs))
	}
	r := rs[0]
	if !r.HasStartChar || r.StartCharacter != 0 {
		t.Errorf("start character = %d (present %v), want an explicit 0", r.StartCharacter, r.HasStartChar)
	}
	if !r.HasEndChar || r.EndCharacter != 3 {
		t.Errorf("end character = %d (present %v), want 3", r.EndCharacter, r.HasEndChar)
	}
	if r.Kind != "comment" {
		t.Errorf("kind = %q, want comment", r.Kind)
	}
}

// null and [] both mean no folds, not an error. A client that treated either as
// a protocol failure would refuse a file that simply has nothing to fold.
func TestFoldingRangeNullAndEmptyAreNoRanges(t *testing.T) {
	for _, raw := range []string{`null`, `[]`} {
		rs, err := foldingRanges(t, raw)
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(rs) != 0 {
			t.Errorf("%s produced %d ranges", raw, len(rs))
		}
	}
}

// One malformed entry does not hide the ranges beside it: a whole answer
// dropped because of one bad element would make a file unusable. A range whose
// end precedes its start cannot fold, so it is dropped; a non-object is too.
func TestFoldingRangeMalformedEntryIsDropped(t *testing.T) {
	rs, err := foldingRanges(t, `[
		{"startLine":1,"endLine":3},
		{"startLine":5,"endLine":2},
		"nonsense",
		{"startLine":7,"endLine":9}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d ranges, want the two good ones", len(rs))
	}
	if rs[0].StartLine != 1 || rs[1].StartLine != 7 {
		t.Errorf("ranges = %+v, want startLines 1 and 7", rs)
	}
}

// A nil connection fails with ErrClosed rather than dereferencing it, the same
// guard every request in this package has.
func TestFoldingRangesOnANilConn(t *testing.T) {
	if _, err := RequestFoldingRanges(context.Background(), nil, "/w/a.go"); err != ErrClosed {
		t.Errorf("RequestFoldingRanges err = %v, want ErrClosed", err)
	}
}
