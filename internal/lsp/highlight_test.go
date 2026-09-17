package lsp

import (
	"encoding/json"
	"testing"
)

// textDocument/documentHighlight has three legal kinds. All three must decode:
// the emphasis the renderer gives a write is the whole reason the server
// distinguishes them, and dropping the kind would paint a write like a read.
func TestDocumentHighlightKindsDecode(t *testing.T) {
	raw := json.RawMessage(`[
		{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"kind":1},
		{"range":{"start":{"line":1,"character":4},"end":{"line":1,"character":7}},"kind":2},
		{"range":{"start":{"line":2,"character":8},"end":{"line":2,"character":11}},"kind":3}
	]`)
	got := DecodeDocumentHighlights(raw)
	if len(got) != 3 {
		t.Fatalf("decoded %d highlights, want 3: %+v", len(got), got)
	}
	want := []DocumentHighlightKind{HighlightText, HighlightRead, HighlightWrite}
	for i, k := range want {
		if got[i].Kind != k {
			t.Errorf("highlight %d kind = %d, want %d", i, got[i].Kind, k)
		}
	}
	if got[2].Range.Start.Line != 2 || got[2].Range.End.Character != 11 {
		t.Errorf("third range = %+v, want line 2 characters 8..11", got[2].Range)
	}
}

// An absent kind is the protocol's Text default, and an unknown number is not a
// reason to drop a range the server reported.
func TestDocumentHighlightDefaultsKind(t *testing.T) {
	got := DecodeDocumentHighlights(json.RawMessage(
		`[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}},
		  {"range":{"start":{"line":0,"character":2},"end":{"line":0,"character":3}},"kind":99}]`))
	if len(got) != 2 {
		t.Fatalf("decoded %d, want 2: %+v", len(got), got)
	}
	for i, h := range got {
		if h.Kind != HighlightText {
			t.Errorf("highlight %d kind = %d, want Text", i, h.Kind)
		}
	}
}

// An empty result is the normal answer over whitespace, whether the server
// sends null or an empty array.
func TestDocumentHighlightEmpty(t *testing.T) {
	for _, raw := range []string{`null`, `[]`} {
		if got := DecodeDocumentHighlights(json.RawMessage(raw)); got != nil {
			t.Errorf("%s decoded to %+v, want nil", raw, got)
		}
	}
}

// A malformed entry drops only itself. A missing range would decode to a zero
// range at the top of the file, and one bad entry must not take the good ones
// with it.
func TestDocumentHighlightMalformedEntryDropped(t *testing.T) {
	got := DecodeDocumentHighlights(json.RawMessage(`[
		{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"kind":2},
		{"kind":3},
		{"range":{"start":{"line":1,"character":1},"end":{"line":1,"character":1}}},
		{"range":"not-a-range"}
	]`))
	if len(got) != 1 || got[0].Kind != HighlightRead {
		t.Fatalf("decoded %+v, want only the well-formed read", got)
	}
	if got[0].Range.Start.Line != 0 || got[0].Range.End.Character != 3 {
		t.Errorf("surviving range = %+v, want line 0 characters 0..3", got[0].Range)
	}
}

// A single object is accepted even though the specification says array: some
// servers send one when there is exactly one occurrence.
func TestDocumentHighlightSingleObject(t *testing.T) {
	got := DecodeDocumentHighlights(json.RawMessage(
		`{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"kind":3}`))
	if len(got) != 1 || got[0].Kind != HighlightWrite {
		t.Fatalf("decoded %+v, want the one write", got)
	}
}

// The request names the position it was asked about. A highlight answered for
// another position is an emphasis on the wrong symbol.
func TestRequestDocumentHighlightsSendsPosition(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/documentHighlight", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestDocumentHighlights(ctx, f.conn, "/w/a.go", Position{Line: 7, Character: 3}); err != nil {
		t.Fatal(err)
	}
	pos, _ := got["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(7) || pos["character"] != float64(3) {
		t.Errorf("position sent as %v, want 7:3", pos)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("textDocument sent as %v", doc)
	}
}
