package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// Formatting sends the buffer's indent style as FormattingOptions and decodes
// the TextEdit list back. A hardcoded or dropped option would reformat every
// file to a style the editor does not use, or leave the server guessing; the
// two requests share the options shape and the edit decode.
func TestFormattingSendsOptionsAndDecodesEdits(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/formatting", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"newText":"ok"}]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	edits, err := RequestFormatting(ctx, f.conn, "/w/a.go", FormattingOptions{TabSize: 2, InsertSpaces: true})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	opts, _ := got["options"].(map[string]any)
	if opts == nil || opts["tabSize"] != float64(2) || opts["insertSpaces"] != true {
		t.Errorf("options = %v, want tabSize 2 insertSpaces true", opts)
	}
	if _, hasRange := got["range"]; hasRange {
		t.Errorf("a whole-document request sent a range: %v", got["range"])
	}
	if len(edits) != 1 || edits[0].NewText != "ok" {
		t.Fatalf("edits = %+v", edits)
	}
	if edits[0].Range.Start != (Position{Line: 1, Character: 2}) ||
		edits[0].Range.End != (Position{Line: 1, Character: 5}) {
		t.Errorf("range = %+v", edits[0].Range)
	}
}

// A null answer is "nothing to change", not an error: the caller applies it as
// a no-op rather than reporting a failure for an already-formatted file. An
// empty array is the same answer.
func TestFormattingNullAndEmptyListsAreNoChange(t *testing.T) {
	for _, raw := range []string{`null`, `[]`} {
		f := newFake(t)
		f.on("textDocument/formatting", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		edits, err := RequestFormatting(ctx, f.conn, "/w/a.go", FormattingOptions{})
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(edits) != 0 {
			t.Errorf("%s produced %d edits", raw, len(edits))
		}
	}
}

// The range form carries its range: a request without the selection would
// format the wrong span, and the options still ride along.
func TestRangeFormattingSendsTheRange(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/rangeFormatting", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	r := Range{Start: Position{Line: 3, Character: 0}, End: Position{Line: 9, Character: 4}}
	if _, err := RequestRangeFormatting(ctx, f.conn, "/w/a.go", r, FormattingOptions{TabSize: 8}); err != nil {
		t.Fatal(err)
	}
	wire, _ := got["range"].(map[string]any)
	if wire == nil {
		t.Fatalf("no range sent: %v", got)
	}
	start, _ := wire["start"].(map[string]any)
	end, _ := wire["end"].(map[string]any)
	if start["line"] != float64(3) || start["character"] != float64(0) ||
		end["line"] != float64(9) || end["character"] != float64(4) {
		t.Errorf("range sent as %v", wire)
	}
	opts, _ := got["options"].(map[string]any)
	if opts["tabSize"] != float64(8) {
		t.Errorf("options = %v, want tabSize 8", opts)
	}
}

// A malformed answer decodes to an empty list rather than an error: a server
// that sent something unreadable has nothing to say, and that must not stop
// the editor.
func TestMalformedFormattingIsAnEmptyList(t *testing.T) {
	for _, raw := range []string{`{}`, `"nonsense"`, `[{"newText":42}]`} {
		f := newFake(t)
		f.on("textDocument/formatting", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		edits, err := RequestFormatting(ctx, f.conn, "/w/a.go", FormattingOptions{})
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(edits) != 0 {
			t.Errorf("%s produced %d edits", raw, len(edits))
		}
	}
}

// A caller with no connection gets ErrClosed, not a nil-pointer panic.
func TestFormattingWithoutAConnection(t *testing.T) {
	if _, err := RequestFormatting(context.Background(), nil, "/w/a.go", FormattingOptions{}); err != ErrClosed {
		t.Errorf("formatting err = %v, want ErrClosed", err)
	}
	if _, err := RequestRangeFormatting(context.Background(), nil, "/w/a.go", Range{}, FormattingOptions{}); err != ErrClosed {
		t.Errorf("range formatting err = %v, want ErrClosed", err)
	}
}

// The 3.17 provider shape: a required firstTriggerCharacter string plus the
// optional moreTriggerCharacter array. The list is the union in order, because
// a server that named a second trigger expects it to fire; without the decode
// the feature has no triggers and never asks.
func TestOnTypeFormattingTriggersDecode(t *testing.T) {
	caps := ServerCapabilities{DocumentOnTypeFormattingProvider: json.RawMessage(
		`{"firstTriggerCharacter":"}","moreTriggerCharacter":[";","\n"]}`)}
	got := caps.OnTypeFormattingTriggers()
	want := []string{"}", ";", "\n"}
	if len(got) != len(want) {
		t.Fatalf("triggers = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("triggers = %q, want %q", got, want)
			break
		}
	}
}

// A server that spelt the array plural is still read: the 3.17 meta model says
// singular, but losing a real trigger to a spelling costs the feature for that
// server.
func TestOnTypeFormattingTriggersAcceptPlural(t *testing.T) {
	caps := ServerCapabilities{DocumentOnTypeFormattingProvider: json.RawMessage(
		`{"firstTriggerCharacter":"}","moreTriggerCharacters":[";"]}`)}
	got := caps.OnTypeFormattingTriggers()
	if len(got) != 2 || got[0] != "}" || got[1] != ";" {
		t.Errorf("triggers = %q, want [} ;]", got)
	}
}

// Absent, false, and a provider with no usable character all mean no trigger,
// which is exactly "the keystroke asks nothing".
func TestOnTypeFormattingTriggersEmpty(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"absent", ""},
		{"false", `false`},
		{"no first", `{"moreTriggerCharacter":[";"]}`},
		{"empty first", `{"firstTriggerCharacter":""}`},
		{"garbage", `"nonsense"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps := ServerCapabilities{}
			if c.raw != "" {
				caps.DocumentOnTypeFormattingProvider = json.RawMessage(c.raw)
			}
			if got := caps.OnTypeFormattingTriggers(); got != nil {
				t.Errorf("triggers = %q, want nil", got)
			}
		})
	}
}

// The trigger rule is exact string equality: "}" fires only for "}", "\n"
// fires for the newline keystroke, and an empty keystroke never fires.
func TestOnTypeTriggerSelects(t *testing.T) {
	triggers := []string{"}", ";", "\n"}
	cases := []struct {
		text string
		want bool
	}{
		{"}", true},
		{";", true},
		{"\n", true},
		{"{", false},
		{")", false},
		{"", false},
		{"}}", false},
	}
	for _, c := range cases {
		got, ok := OnTypeTrigger(triggers, c.text)
		if ok != c.want {
			t.Errorf("OnTypeTrigger(%q) = %q,%v want %v", c.text, got, ok, c.want)
		}
	}
	if _, ok := OnTypeTrigger(nil, "}"); ok {
		t.Error("a nil trigger list matched a character")
	}
}

// The request sends the 3.17 params — document, position, ch and options — and
// decodes the TextEdit list like the other two formatting requests. A dropped
// ch or position is a request the server cannot answer about the right place.
func TestOnTypeFormattingSendsPositionChAndOptions(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/onTypeFormatting", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}},"newText":"{}"}]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	pos := Position{Line: 1, Character: 1}
	edits, err := RequestOnTypeFormatting(ctx, f.conn, "/w/a.go", pos, "}", FormattingOptions{TabSize: 4, InsertSpaces: false})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	if got["ch"] != "}" {
		t.Errorf("ch = %v, want }", got["ch"])
	}
	wire, _ := got["position"].(map[string]any)
	if wire == nil || wire["line"] != float64(1) || wire["character"] != float64(1) {
		t.Errorf("position = %v, want 1:1", got["position"])
	}
	opts, _ := got["options"].(map[string]any)
	if opts == nil || opts["tabSize"] != float64(4) || opts["insertSpaces"] != false {
		t.Errorf("options = %v", opts)
	}
	if len(edits) != 1 || edits[0].NewText != "{}" {
		t.Fatalf("edits = %+v", edits)
	}
}

// A null answer is "nothing to change", not an error, so the caller applies a
// no-op rather than reporting a failure for a character that needed nothing.
func TestOnTypeFormattingNullIsNoChange(t *testing.T) {
	f := newFake(t)
	f.on("textDocument/onTypeFormatting", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`null`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	edits, err := RequestOnTypeFormatting(ctx, f.conn, "/w/a.go", Position{}, "}", FormattingOptions{})
	if err != nil {
		t.Errorf("a null answer produced an error: %v", err)
	}
	if len(edits) != 0 {
		t.Errorf("a null answer produced %d edits", len(edits))
	}
}

// A caller with no connection gets ErrClosed, not a nil-pointer panic.
func TestOnTypeFormattingWithoutAConnection(t *testing.T) {
	if _, err := RequestOnTypeFormatting(context.Background(), nil, "/w/a.go", Position{}, "}", FormattingOptions{}); err != ErrClosed {
		t.Errorf("on-type formatting err = %v, want ErrClosed", err)
	}
}
