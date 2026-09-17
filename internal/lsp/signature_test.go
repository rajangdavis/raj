package lsp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A full answer decodes into the active signature, its parameters and its
// documentation. The label is shown whole with the active parameter marked;
// the documentation is flattened the same way hover contents are, because the
// panel renders both through the same markdown subset.
func TestSignatureHelpDecodes(t *testing.T) {
	raw := `{
	  "signatures": [
	    {"label": "func F(a int, b string) error",
	     "documentation": {"kind": "markdown", "value": "F does a thing."},
	     "parameters": [{"label": [7, 12]}, {"label": [14, 22]}]},
	    {"label": "func F() error"}
	  ],
	  "activeSignature": 0,
	  "activeParameter": 1
	}`
	f := newFake(t)
	f.on("textDocument/signatureHelp", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	got, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{Line: 3, Character: 9})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no signature help decoded")
	}
	if len(got.Signatures) != 2 {
		t.Fatalf("signatures = %d, want 2", len(got.Signatures))
	}
	sig, active, ok := got.Active()
	if !ok || sig.Label != "func F(a int, b string) error" {
		t.Fatalf("active signature = %+v ok=%v", sig, ok)
	}
	if active != 1 {
		t.Errorf("active parameter = %d, want 1", active)
	}
	want := "func F(a int, `b string`) error\n\nF does a thing."
	if text := got.Text(); text != want {
		t.Errorf("Text() = %q, want %q", text, want)
	}
}

// A position that is not inside a call is the normal answer, not an error:
// null, an empty signature list and nonsense all decode to nothing.
func TestSignatureHelpNoSignatureIsNotAnError(t *testing.T) {
	for _, raw := range []string{`null`, `{"signatures":[]}`, `{}`, `"nope"`, `42`} {
		f := newFake(t)
		f.on("textDocument/signatureHelp", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		got, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{})
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if got != nil {
			t.Errorf("%s produced %+v, want nothing", raw, got)
		}
	}
}

// The request names the position it was asked about. An answer for another
// position is not an error — it is a confidently wrong signature.
func TestSignatureHelpSendsThePosition(t *testing.T) {
	f := newFake(t)
	var gotParams map[string]any
	f.on("textDocument/signatureHelp", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &gotParams)
		return json.RawMessage(`{"signatures":[{"label":"F()"}]}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{Line: 12, Character: 34}); err != nil {
		t.Fatal(err)
	}
	pos, _ := gotParams["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(12) || pos["character"] != float64(34) {
		t.Errorf("position sent as %v, want 12:34", pos)
	}
	doc, _ := gotParams["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	if _, sent := gotParams["context"]; sent {
		t.Error("a context was sent; absence is defined as an explicit invocation")
	}
}

// A parameter whose label arrived as a plain string cannot be located reliably,
// so the signature is shown unmarked. Marking by searching for the string could
// emphasise the wrong occurrence of a repeated parameter.
func TestSignatureHelpPlainLabelIsUnmarked(t *testing.T) {
	raw := `{"signatures":[{"label":"F(lo, lo)","parameters":[{"label":"lo"},{"label":"lo"}]}],
	         "activeSignature":0,"activeParameter":0}`
	f := newFake(t)
	f.on("textDocument/signatureHelp", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	got, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Text(); strings.Contains(text, "`") {
		t.Errorf("Text() = %q; a plain label must not be marked", text)
	}
}

// Offsets are UTF-16 code units into the label, the protocol's string unit, not
// bytes. A label with a multi-byte name before the parameter would mark the
// wrong span if the mapping were skipped.
func TestSignatureHelpOffsetsAreUTF16(t *testing.T) {
	// "f(名前 int)": the parameter occupies UTF-16 units [2,4], bytes [2,8].
	raw := `{"signatures":[{"label":"f(名前 int)","parameters":[{"label":[2,4]}]}],
	         "activeSignature":0,"activeParameter":0}`
	f := newFake(t)
	f.on("textDocument/signatureHelp", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	got, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Text(); text != "f(`名前` int)" {
		t.Errorf("Text() = %q, want the name inside the marks", text)
	}
}

// A signature may carry its own active parameter, which overrides the
// top-level one. Collapsing the two would mark the wrong parameter.
func TestSignatureHelpPerSignatureActiveParameter(t *testing.T) {
	raw := `{"signatures":[{"label":"F(a,b)","parameters":[{"label":[2,3]},{"label":[4,5]}],"activeParameter":1}],
	         "activeSignature":0,"activeParameter":0}`
	f := newFake(t)
	f.on("textDocument/signatureHelp", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	got, err := RequestSignatureHelp(ctx, f.conn, "/w/a.go", Position{})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Text(); text != "F(a,`b`)" {
		t.Errorf("Text() = %q, want b marked", text)
	}
}

// Out-of-range indices are clamped rather than panicking. Servers send them.
func TestSignatureHelpClampsIndices(t *testing.T) {
	h := &SignatureHelp{ActiveSignature: 9, ActiveParameter: 9}
	if _, _, ok := h.Active(); ok {
		t.Error("an empty help reported an active signature")
	}
	h = &SignatureHelp{
		Signatures:      []Signature{{Label: "F()"}},
		ActiveSignature: 9,
		ActiveParameter: 9,
	}
	sig, active, ok := h.Active()
	if !ok || sig.Label != "F()" || active != -1 {
		t.Errorf("got %q active=%d ok=%v, want F() active=-1", sig.Label, active, ok)
	}
}

// A dead connection fails rather than panicking, since the server may die
// between any two keystrokes.
func TestSignatureHelpOnADeadConnection(t *testing.T) {
	if _, err := RequestSignatureHelp(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("nil conn err = %v, want ErrClosed", err)
	}
	f := newFake(t)
	f.die()
	waitFor(t, f.conn.Closed)
	if _, err := RequestSignatureHelp(context.Background(), f.conn, "/w/a.go", Position{}); err == nil {
		t.Error("a request on a dead connection succeeded")
	}
}
