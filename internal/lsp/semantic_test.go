package lsp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// semantic fetches a full response through the real request path, so a test of
// the decoder also proves the request is made and wired to the method the
// protocol names.
func semantic(t *testing.T, raw string, legend SemanticTokensLegend) []SemanticToken {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	res, err := RequestSemanticTokensFull(ctx, f.conn, "/w/a.go", legend)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tokens
}

// The delta encoding is relative. A token on a later line carries the line gap
// and an absolute start character; the token after it on the same line carries
// a start delta. Reading the numbers as absolute positions misplaces every
// token after the first line jump — which, without the delta walk, silently
// colours the wrong bytes rather than failing to colour anything at all.
func TestSemanticTokensDeltaDecode(t *testing.T) {
	legend := SemanticTokensLegend{
		TokenTypes:     []string{"keyword", "function", "parameter"},
		TokenModifiers: []string{"declaration", "deprecated"},
	}
	// token 1: line 2, char 4, len 3, keyword.
	// token 2: line 5 (a gap of 3), char 2 (absolute), len 8, function,
	//          declaration.
	raw := `{"resultId":"r1","data":[2,4,3,0,0, 3,2,8,1,1]}`
	toks := semantic(t, raw, legend)
	if len(toks) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(toks), toks)
	}
	if got := toks[0]; got.Start != (Position{Line: 2, Character: 4}) || got.Length != 3 || got.Type != "keyword" {
		t.Errorf("token 0 = %+v, want 2:4 len 3 keyword", got)
	}
	if got := toks[1]; got.Start != (Position{Line: 5, Character: 2}) || got.Length != 8 || got.Type != "function" {
		t.Errorf("token 1 = %+v, want 5:2 len 8 function", got)
	}
	if !toks[1].HasModifier("declaration") {
		t.Errorf("token 1 modifiers = %v, want declaration", toks[1].Modifiers)
	}

	// A same-line token's start character is relative to the previous token's
	// start, not to zero: the second token here starts at char 10.
	same := DecodeSemanticTokens([]uint32{
		2, 4, 3, 0, 0,
		0, 6, 2, 1, 0,
	}, legend)
	if len(same) != 2 || same[1].Start != (Position{Line: 2, Character: 10}) {
		t.Errorf("same-line token = %+v, want line 2 char 10", same)
	}

	// A line jump resets the start character to the absolute value: the third
	// token is at char 1 of line 4, not char 11.
	jump := DecodeSemanticTokens([]uint32{
		2, 4, 3, 0, 0,
		0, 6, 2, 1, 0,
		2, 1, 4, 0, 0,
	}, legend)
	if len(jump) != 3 || jump[2].Start != (Position{Line: 4, Character: 1}) {
		t.Errorf("token after a line jump = %+v, want line 4 char 1", jump)
	}
}

// An index past the end of the server's legend resolves to no type rather than
// a wrong one, and a modifier bit past the legend is dropped. The span is still
// decoded so the app can skip it; the alternative is painting an arbitrary
// colour, which looks exactly like a correct one.
func TestSemanticTokensLegendOutOfRange(t *testing.T) {
	legend := SemanticTokensLegend{
		TokenTypes:     []string{"keyword"},
		TokenModifiers: []string{"declaration"},
	}
	toks := DecodeSemanticTokens([]uint32{
		0, 0, 1, 5, 0, // type index 5, past the one-entry legend
		0, 2, 1, 0, 2, // modifier bit 1, past the one-entry modifier legend
	}, legend)
	if len(toks) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(toks), toks)
	}
	if toks[0].Type != "" {
		t.Errorf("type = %q, want empty for an out-of-range index", toks[0].Type)
	}
	if toks[0].Length != 1 {
		t.Errorf("length = %d, want the span still decoded", toks[0].Length)
	}
	if toks[1].Type != "keyword" || len(toks[1].Modifiers) != 0 {
		t.Errorf("token 1 = %+v, want keyword with no modifiers", toks[1])
	}
}

// A partial trailing group is dropped, not fatal. The complete tokens before it
// are real, and the data is a flat array with no per-token framing to reject, so
// treating a short tail as an error would throw away every valid token in the
// answer.
func TestSemanticTokensMalformedData(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	toks := DecodeSemanticTokens([]uint32{0, 0, 1, 0, 0, 0, 2}, legend)
	if len(toks) != 1 {
		t.Fatalf("got %d tokens, want the one complete group: %+v", len(toks), toks)
	}
	if toks[0].Start != (Position{Line: 0, Character: 0}) {
		t.Errorf("token = %+v, want the complete first group", toks[0])
	}
	if got := DecodeSemanticTokens(nil, legend); len(got) != 0 {
		t.Errorf("empty data = %+v, want none", got)
	}
}

// A null or absent response decodes to no tokens, the normal answer for a file
// the server has not indexed. It is not a failure, and nothing should treat it
// as one.
func TestSemanticTokensEmptyAnswer(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	for _, raw := range []string{`null`, `{}`, `{"data":[]}`} {
		if toks := semantic(t, raw, legend); len(toks) != 0 {
			t.Errorf("%s: got %+v, want no tokens", raw, toks)
		}
	}
}

// The legend is read from the server's own provider, and a provider with no
// legend is as unusable as no provider: the indices cannot be resolved, so the
// caller is told there is no legend rather than handed an empty one that would
// silently mis-name every token.
func TestSemanticTokensLegendRead(t *testing.T) {
	caps := ServerCapabilities{SemanticTokensProvider: json.RawMessage(
		`{"legend":{"tokenTypes":["keyword","string"],"tokenModifiers":["deprecated"],"range":true,"full":true}}`)}
	legend, ok := caps.SemanticTokensLegend()
	if !ok {
		t.Fatal("a provider with a legend did not read")
	}
	if len(legend.TokenTypes) != 2 || legend.TokenTypes[1] != "string" {
		t.Errorf("token types = %v, want the server's order", legend.TokenTypes)
	}
	if len(legend.TokenModifiers) != 1 || legend.TokenModifiers[0] != "deprecated" {
		t.Errorf("token modifiers = %v", legend.TokenModifiers)
	}

	for _, raw := range []string{``, `false`, `true`, `{"range":true}`, `{"legend":{}}`} {
		if _, ok := (ServerCapabilities{SemanticTokensProvider: json.RawMessage(raw)}).SemanticTokensLegend(); ok {
			t.Errorf("provider %q: read a legend from nothing usable", raw)
		}
	}
}

// A delta reply's edits index the flat integer array, not tokens. An insert
// (deleteCount 0), a delete and a replacement must each land at the right
// integer offset; reading them as token indices would splice the array in the
// wrong place and decode a token the server never sent.
func TestSemanticTokensDeltaApplyEdits(t *testing.T) {
	base := []uint32{0, 0, 1, 0, 0, 0, 2, 1, 1, 0}
	cases := []struct {
		name  string
		delta SemanticTokensDelta
		want  []uint32
	}{
		{
			name:  "insert at the front",
			delta: SemanticTokensDelta{Edits: []SemanticTokensEdit{{Start: 0, DeleteCount: 0, Data: []uint32{1, 0, 1, 0, 0}}}},
			want:  []uint32{1, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 2, 1, 1, 0},
		},
		{
			name:  "delete the first token",
			delta: SemanticTokensDelta{Edits: []SemanticTokensEdit{{Start: 0, DeleteCount: 5}}},
			want:  []uint32{0, 2, 1, 1, 0},
		},
		{
			name:  "replace the second token",
			delta: SemanticTokensDelta{Edits: []SemanticTokensEdit{{Start: 5, DeleteCount: 5, Data: []uint32{0, 7, 2, 0, 0}}}},
			want:  []uint32{0, 0, 1, 0, 0, 0, 7, 2, 0, 0},
		},
		{
			name:  "insert in the middle",
			delta: SemanticTokensDelta{Edits: []SemanticTokensEdit{{Start: 5, DeleteCount: 0, Data: []uint32{0, 9, 1, 0, 0}}}},
			want:  []uint32{0, 0, 1, 0, 0, 0, 9, 1, 0, 0, 0, 2, 1, 1, 0},
		},
	}
	for _, c := range cases {
		got, ok := ApplySemanticTokensDelta(base, c.delta)
		if !ok {
			t.Errorf("%s: not applied", c.name)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
		if !reflect.DeepEqual(base, []uint32{0, 0, 1, 0, 0, 0, 2, 1, 1, 0}) {
			t.Fatalf("%s mutated the previous array: %v", c.name, base)
		}
	}
}

// The protocol says the edits in one response are all based on the same state
// and must not be assumed sorted. Applying them in the order they arrived, or
// front to back, shifts the later offsets and corrupts the array; sorting and
// applying from the back keeps every edit's coordinates valid.
func TestSemanticTokensDeltaApplyUnsortedEdits(t *testing.T) {
	base := []uint32{0, 0, 1, 0, 0, 0, 2, 1, 1, 0, 0, 3, 1, 0, 0}
	delta := SemanticTokensDelta{Edits: []SemanticTokensEdit{
		{Start: 10, DeleteCount: 5, Data: []uint32{0, 7, 1, 2, 0}},
		{Start: 0, DeleteCount: 5, Data: []uint32{1, 0, 1, 0, 0}},
	}}
	got, ok := ApplySemanticTokensDelta(base, delta)
	if !ok {
		t.Fatal("edits were not applied")
	}
	want := []uint32{1, 0, 1, 0, 0, 0, 2, 1, 1, 0, 0, 7, 1, 2, 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An edit outside the array, or two edits that overlap, describes no coherent
// new state. Reporting failure lets the caller fall back to a full request; a
// partial application would decode tokens that exist in neither version.
func TestSemanticTokensDeltaRejectsMalformed(t *testing.T) {
	base := []uint32{0, 0, 1, 0, 0}
	cases := map[string]SemanticTokensDelta{
		"start past the end":  {Edits: []SemanticTokensEdit{{Start: 6, DeleteCount: 0}}},
		"delete past the end": {Edits: []SemanticTokensEdit{{Start: 3, DeleteCount: 5}}},
		"overlapping edits":   {Edits: []SemanticTokensEdit{{Start: 0, DeleteCount: 3}, {Start: 2, DeleteCount: 1}}},
	}
	for name, d := range cases {
		if _, ok := ApplySemanticTokensDelta(base, d); ok {
			t.Errorf("%s: applied a malformed delta", name)
		}
	}
}

// A reply with no edits array is the protocol's full-replacement form. It must
// replace the array wholesale: applying an empty edit list would keep the old
// tokens under the new resultId, painting text the server no longer says.
func TestSemanticTokensDeltaFullReplacement(t *testing.T) {
	base := []uint32{0, 0, 1, 0, 0}
	d := SemanticTokensDelta{ResultID: "r2", Full: true, Data: []uint32{0, 4, 2, 1, 0}}
	got, ok := ApplySemanticTokensDelta(base, d)
	if !ok {
		t.Fatal("full replacement reported as unapplied")
	}
	want := []uint32{0, 4, 2, 1, 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The delta request names the previous resultId and applies the edits to the
// previous array, so the decoded tokens describe the new state rather than a
// new resultId wrapped around the old tokens.
func TestSemanticTokensDeltaRequestApplies(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword", "function"}}
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/semanticTokens/full/delta", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{"resultId":"r2","edits":[{"start":0,"deleteCount":1,"data":[3]}]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{2, 5, 3, 0, 3}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if got["previousResultId"] != "r1" {
		t.Errorf("previousResultId = %v, want r1", got["previousResultId"])
	}
	if res.ResultID != "r2" {
		t.Errorf("resultId = %q, want r2", res.ResultID)
	}
	if want := []uint32{3, 5, 3, 0, 3}; !reflect.DeepEqual(res.Data, want) {
		t.Errorf("data = %v, want %v", res.Data, want)
	}
	if len(res.Tokens) != 1 || res.Tokens[0].Start != (Position{Line: 3, Character: 5}) {
		t.Errorf("tokens = %+v, want the single token at 3:5", res.Tokens)
	}
}

// A server may answer full/delta with a whole result instead of edits. With the
// edits array absent the reply is a replacement, so the previous array must be
// ignored; treating the missing edits as an empty list would keep the old
// tokens under the new resultId.
func TestSemanticTokensDeltaAbsentEditsIsFullReplacement(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r9","data":[0,1,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{5, 5, 5, 5, 5}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "r9" {
		t.Errorf("resultId = %q, want r9", res.ResultID)
	}
	if want := []uint32{0, 1, 1, 0, 0}; !reflect.DeepEqual(res.Data, want) {
		t.Errorf("data = %v, want the replacement %v", res.Data, want)
	}
}

// A delta whose edits cannot be applied must not be painted. The request falls
// back to the full method, so the result is the server's complete answer and
// not an array spliced in the wrong place.
func TestSemanticTokensDeltaBadEditsFallBackToFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r2","edits":[{"start":99,"deleteCount":1}]}`), nil
	})
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r3","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{0, 0, 1, 0, 0}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "r3" {
		t.Errorf("resultId = %q, want the full answer r3", res.ResultID)
	}
	if !has(f.methods(), "textDocument/semanticTokens/full/delta") ||
		!has(f.methods(), "textDocument/semanticTokens/full") {
		t.Errorf("methods = %v, want the delta then the full request", f.methods())
	}
}

// A resultId the server no longer knows is an error reply, not an empty delta.
// The client recovers with a full request rather than painting nothing, or the
// old tokens, against the new text.
func TestSemanticTokensDeltaErrorFallsBackToFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		return nil, &ResponseError{Code: -32602, Message: "unknown result id"}
	})
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r3","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{0, 0, 1, 0, 0}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "r3" {
		t.Errorf("resultId = %q, want the full answer r3", res.ResultID)
	}
}

// There is nothing to diff against when no previous result is held, so the full
// method is used rather than sending a delta with an empty previousResultId.
func TestSemanticTokensNoPreviousUsesFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r1","data":[0,0,1,0,0]}`), nil
	})
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		t.Error("a delta was sent with no previous result")
		return nil, nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "r1" {
		t.Errorf("resultId = %q, want the full answer r1", res.ResultID)
	}
}

// A delta reply with no resultId has nothing to chain from, so it is dropped
// and the full method answers. Painting the delta would leave the next edit
// without a base the server can diff against.
func TestSemanticTokensMissingResultIdFallsBackToFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	deltas, fulls := 0, 0
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		deltas++
		return json.RawMessage(`{"edits":[{"start":0,"deleteCount":1,"data":[1]}]}`), nil
	})
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		fulls++
		return json.RawMessage(`{"resultId":"rF","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{0, 0, 1, 0, 0}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "rF" {
		t.Errorf("resultId = %q, want the full answer rF", res.ResultID)
	}
	if deltas != 1 || fulls != 1 {
		t.Errorf("delta calls = %d, full calls = %d; want one delta then one full", deltas, fulls)
	}
}

// A delta reply that reuses the previous resultId has not advanced, so the
// client cannot know the server's snapshot for that id still matches the array
// it holds. It falls back to full rather than chain edits against an id that
// names two different states.
func TestSemanticTokensUnchangedResultIdFallsBackToFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r1","edits":[{"start":0,"deleteCount":1,"data":[1]}]}`), nil
	})
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"rF","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{0, 0, 1, 0, 0}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "rF" {
		t.Errorf("resultId = %q, want the full answer rF", res.ResultID)
	}
}

// The range request carries the exact range it wants and decodes the same data
// shape as the full request. Asking about the wrong range would draw tokens
// over text they do not describe.
func TestSemanticTokensRangeRequest(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/semanticTokens/range", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{"resultId":"r1","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	r := Range{Start: Position{Line: 4, Character: 2}, End: Position{Line: 9, Character: 7}}
	res, err := RequestSemanticTokensRange(ctx, f.conn, "/w/a.go", r, legend)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	rng, _ := got["range"].(map[string]any)
	if rng == nil {
		t.Fatalf("range sent as %v", got["range"])
	}
	start, _ := rng["start"].(map[string]any)
	end, _ := rng["end"].(map[string]any)
	if start == nil || start["line"] != float64(4) || start["character"] != float64(2) {
		t.Errorf("range start sent as %v, want 4:2", start)
	}
	if end == nil || end["line"] != float64(9) || end["character"] != float64(7) {
		t.Errorf("range end sent as %v, want 9:7", end)
	}
	if len(res.Tokens) != 1 || res.Tokens[0].Start != (Position{Line: 0, Character: 0}) {
		t.Errorf("tokens = %+v, want the one token from the range reply", res.Tokens)
	}
}

// A nil connection fails with ErrClosed rather than dereferencing it, the same
// contract every other request keeps.
func TestSemanticTokensRangeAndDeltaOnANilConn(t *testing.T) {
	if _, err := RequestSemanticTokensRange(context.Background(), nil, "/w/a.go", Range{}, SemanticTokensLegend{}); err != ErrClosed {
		t.Errorf("range err = %v, want ErrClosed", err)
	}
	if _, err := RequestSemanticTokensFullDelta(context.Background(), nil, "/w/a.go", "r1"); err != ErrClosed {
		t.Errorf("delta err = %v, want ErrClosed", err)
	}
}

// A reply that does not parse is a delta that cannot be applied, so the client
// falls back to a full request. Treating it as an empty replacement would clear
// tokens the server never said were gone.
func TestSemanticTokensDeltaMalformedFallsBackToFull(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"keyword"}}
	f := newFake(t)
	f.on("textDocument/semanticTokens/full/delta", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r2","edits":"not-an-array"}`), nil
	})
	f.on("textDocument/semanticTokens/full", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"resultId":"r3","data":[0,0,1,0,0]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	prev := &SemanticTokens{ResultID: "r1", Data: []uint32{0, 0, 1, 0, 0}}
	res, err := RequestSemanticTokens(ctx, f.conn, "/w/a.go", legend, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultID != "r3" || len(res.Tokens) != 1 {
		t.Errorf("result = %q with %d tokens, want the full answer r3 and one token", res.ResultID, len(res.Tokens))
	}
}
