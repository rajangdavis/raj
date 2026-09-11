package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// inlay decodes one server answer through the real request path.
func inlay(t *testing.T, raw string) ([]InlayHint, error) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/inlayHint", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	return RequestInlayHints(ctx, f.conn, "/w/a.go", Range{})
}

// The label is a plain string in the common case. Position, kind and the two
// padding flags are carried through so the renderer can place the text.
func TestInlayHintDecodes(t *testing.T) {
	raw := `[{"position":{"line":3,"character":12},"label":": int","kind":1,"paddingLeft":true,"paddingRight":false}]`
	hints, err := inlay(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1", len(hints))
	}
	h := hints[0]
	if h.Pos.Line != 3 || h.Pos.Character != 12 {
		t.Errorf("position = %+v, want 3:12", h.Pos)
	}
	if h.Text != ": int" {
		t.Errorf("text = %q, want %q", h.Text, ": int")
	}
	if h.Kind != 1 {
		t.Errorf("kind = %d, want 1", h.Kind)
	}
	if !h.PaddingLeft || h.PaddingRight {
		t.Errorf("padding = left %v right %v, want left only", h.PaddingLeft, h.PaddingRight)
	}
	if h.Tooltip != "" {
		t.Errorf("tooltip = %q, want empty", h.Tooltip)
	}
	if h.Edits != nil {
		t.Errorf("edits = %+v, want nil", h.Edits)
	}
}

// A parts label is a sequence of spans; only each part's value is text. The
// location and command on a part are actions a terminal cannot offer and must
// not leak into the label.
func TestInlayHintPartsLabelIsJoined(t *testing.T) {
	raw := `[{"label":[{"value":"value "},{"value":"int","tooltip":"the type"},{"value":" bytes","location":{"uri":"file:///w/b.go"},"command":"x"}]}]`
	hints, err := inlay(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1", len(hints))
	}
	if got, want := hints[0].Text, "value int bytes"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// An absent, null or empty label flattens to nothing rather than a panic or a
// stray "[]".
func TestInlayHintEmptyLabels(t *testing.T) {
	for _, raw := range []string{`{"label":""}`, `{"label":null}`, `{}`, `{"label":[]}`} {
		hints, err := inlay(t, "["+raw+"]")
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if len(hints) != 1 || hints[0].Text != "" {
			t.Errorf("%s: got %+v, want one hint with no text", raw, hints)
		}
	}
}

// The tooltip is a string or a MarkupContent, and markdown is reduced to the
// text a cell grid can show — fences stripped, exactly as hover does it.
func TestInlayHintTooltip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent", `{}`, ""},
		{"empty", `{"tooltip":""}`, ""},
		{"string", `{"tooltip":"a type"}`, "a type"},
		{"markup", "{\"tooltip\":{\"kind\":\"markdown\",\"value\":\"```go\\nint\\n```\\n\\nA type.\"}}", "int\n\nA type."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hints, err := inlay(t, "["+c.raw+"]")
			if err != nil {
				t.Fatal(err)
			}
			if len(hints) != 1 {
				t.Fatalf("got %d hints, want 1", len(hints))
			}
			if got := hints[0].Tooltip; got != c.want {
				t.Errorf("tooltip = %q, want %q", got, c.want)
			}
		})
	}
}

// textEdits are decoded inline: with resolveSupport unadvertised a server
// sends them with the hint or not at all. TextEdit has no JSON tags, so this
// also pins the case-insensitive match of newText onto NewText.
func TestInlayHintTextEditsDecode(t *testing.T) {
	raw := `[{"label":": int","textEdits":[{"range":{"start":{"line":3,"character":12},"end":{"line":3,"character":12}},"newText":" int"}]}]`
	hints, err := inlay(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || len(hints[0].Edits) != 1 {
		t.Fatalf("got %+v, want one hint with one edit", hints)
	}
	want := TextEdit{
		Range:   Range{Start: Position{Line: 3, Character: 12}, End: Position{Line: 3, Character: 12}},
		NewText: " int",
	}
	if got := hints[0].Edits[0]; got != want {
		t.Errorf("edit = %+v, want %+v", got, want)
	}
}

// No hints is the normal answer for most ranges, and it is not an error.
func TestInlayHintEmptyResults(t *testing.T) {
	for _, raw := range []string{`null`, `[]`} {
		hints, err := inlay(t, raw)
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(hints) != 0 {
			t.Errorf("%s produced %d hints", raw, len(hints))
		}
	}
}

// Nonsense from a server produces nothing rather than a panic.
func TestInlayHintMalformedIsSurvived(t *testing.T) {
	for _, raw := range []string{`{}`, `"text"`, `42`, `[null]`, `[{"label":42}]`} {
		if _, err := inlay(t, raw); err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
	}
}

// The request names the document and the exact range it wants. Asking about
// the wrong range would draw hints over text they do not describe.
func TestInlayHintsSendRangeAndURI(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/inlayHint", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	r := Range{Start: Position{Line: 4, Character: 2}, End: Position{Line: 9, Character: 7}}
	if _, err := RequestInlayHints(ctx, f.conn, "/w/a.go", r); err != nil {
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
}

// A nil connection fails with ErrClosed rather than dereferencing it, the same
// contract every other request keeps.
func TestInlayHintsOnANilConn(t *testing.T) {
	if _, err := RequestInlayHints(context.Background(), nil, "/w/a.go", Range{}); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}
