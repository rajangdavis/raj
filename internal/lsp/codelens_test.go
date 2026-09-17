package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// lens decodes one server answer through the real request path.
func lens(t *testing.T, raw string) ([]CodeLens, error) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/codeLens", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	return RequestCodeLenses(ctx, f.conn, "/w/a.go")
}

// A command-carrying lens is the common shape: the range names where it is
// drawn and the command is what running it sends. Decoding only the range
// would leave every lens a blank label.
func TestCodeLensDecodesInlineCommand(t *testing.T) {
	lenses, err := lens(t, `[
		{"range":{"start":{"line":4,"character":0},"end":{"line":4,"character":3}},
		 "command":{"title":"3 references","command":"gopls.references","arguments":[{"line":4}]}}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(lenses) != 1 {
		t.Fatalf("got %d lenses, want 1", len(lenses))
	}
	l := lenses[0]
	if l.Range.Start.Line != 4 || l.Range.Start.Character != 0 {
		t.Errorf("range = %+v, want start 4:0", l.Range)
	}
	if l.NeedsResolve() {
		t.Error("a command-carrying lens was reported as needing resolve")
	}
	if l.Command == nil {
		t.Fatal("the command was dropped")
	}
	if l.Command.Title != "3 references" || l.Command.Command != "gopls.references" {
		t.Errorf("command = %+v", l.Command)
	}
	if len(l.Command.Arguments) != 1 {
		t.Errorf("arguments = %d, want 1", len(l.Command.Arguments))
	}
}

// A data-only lens carries no title until it is resolved. It must survive
// decoding with NeedsResolve true and its data intact, or the caller would drop
// the lens the server meant to offer.
func TestCodeLensDecodesDataOnly(t *testing.T) {
	lenses, err := lens(t, `[
		{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":5}},
		 "data":{"uri":"file:///w/a.go","token":"abc"}}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(lenses) != 1 {
		t.Fatalf("got %d lenses, want 1", len(lenses))
	}
	l := lenses[0]
	if !l.NeedsResolve() {
		t.Error("a data-only lens did not report that it needs resolve")
	}
	if l.Command != nil {
		t.Errorf("command = %+v, want none", l.Command)
	}
	if len(l.Data) == 0 {
		t.Fatal("the resolve data was dropped")
	}
	var data struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(l.Data, &data) != nil || data.Token != "abc" {
		t.Errorf("data = %s, want the token preserved", l.Data)
	}
}

// Null and an empty array both mean "no lenses"; a malformed entry is dropped
// without taking the valid lenses beside it with it.
func TestCodeLensEmptyAndMalformed(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, ``, `{}`} {
		got, err := lens(t, raw)
		if err != nil {
			t.Fatalf("raw %q: %v", raw, err)
		}
		if len(got) != 0 {
			t.Errorf("raw %q decoded %d lenses, want none", raw, len(got))
		}
	}

	got, err := lens(t, `[
		{"range":{"start":{"line":0}}, "command":{"title":"A","command":"a"}},
		"not a lens",
		{"range":{"start":{"line":1}}, "command":{"title":"B","command":"b"}}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Command.Command != "a" || got[1].Command.Command != "b" {
		t.Fatalf("lenses = %+v, want the two valid entries", got)
	}
}

// The request names the document, which is all the protocol asks for.
func TestRequestCodeLensesSendsURI(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/codeLens", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()

	if _, err := RequestCodeLenses(ctx, f.conn, "/w/a.go"); err != nil {
		t.Fatal(err)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("textDocument = %v, want the file URI", doc)
	}
}

// A resolve sends the lens back with its data, and the resolved command is
// returned so the caller can run it. Dropping the data would send the server a
// lens it cannot identify.
func TestResolveCodeLensSendsData(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("codeLens/resolve", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{"range":{"start":{"line":2}},
			"command":{"title":"Run test","command":"gopls.run_tests","arguments":["TestF"]}}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()

	in := CodeLens{
		Range: Range{Start: Position{Line: 2}},
		Data:  json.RawMessage(`{"token":"abc"}`),
	}
	out, err := ResolveCodeLens(ctx, f.conn, in)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := got["data"].(map[string]any)
	if data == nil || data["token"] != "abc" {
		t.Errorf("data sent = %v, want the token", got["data"])
	}
	if got["range"] == nil {
		t.Errorf("no range sent: %v", got)
	}
	if out.Command == nil || out.Command.Command != "gopls.run_tests" || out.Command.Title != "Run test" {
		t.Errorf("resolved = %+v, want the returned command", out)
	}
}

// A resolve that answers with no command leaves the lens as it was, so the
// caller can refuse it by name rather than running an empty command.
func TestResolveCodeLensWithoutACommandLeavesItUnresolved(t *testing.T) {
	f := newFake(t)
	f.on("codeLens/resolve", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"range":{"start":{"line":2}},"data":{"token":"abc"}}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	in := CodeLens{Range: Range{Start: Position{Line: 2}}, Data: json.RawMessage(`{"token":"abc"}`)}
	out, err := ResolveCodeLens(ctx, f.conn, in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedsResolve() {
		t.Errorf("resolved = %+v, want it still unresolved", out)
	}
	if len(out.Data) == 0 {
		t.Error("the data was dropped by an empty resolve")
	}
}

// A nil connection fails with ErrClosed rather than dereferencing it, the same
// guard every request in this package has.
func TestCodeLensesOnANilConn(t *testing.T) {
	if _, err := RequestCodeLenses(context.Background(), nil, "/w/a.go"); err != ErrClosed {
		t.Errorf("RequestCodeLenses err = %v, want ErrClosed", err)
	}
	if _, err := ResolveCodeLens(context.Background(), nil, CodeLens{}); err != ErrClosed {
		t.Errorf("ResolveCodeLens err = %v, want ErrClosed", err)
	}
}

// Running a decoded lens is ExecuteCommand with the lens's own name and
// arguments, through the wire unchanged. This is the run half of the human
// path: without it the command is decoded and never sent.
func TestExecuteDecodedCodeLensSendsNameAndArguments(t *testing.T) {
	lenses, err := lens(t, `[
		{"range":{"start":{"line":1}},
		 "command":{"title":"Run test","command":"gopls.run_tests","arguments":["TestF",3]}}
	]`)
	if err != nil || len(lenses) != 1 || lenses[0].Command == nil {
		t.Fatalf("setup: lenses = %+v, err = %v", lenses, err)
	}
	f := newFake(t)
	var got map[string]any
	f.on("workspace/executeCommand", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return nil, nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()

	if err := ExecuteCommand(ctx, f.conn, *lenses[0].Command); err != nil {
		t.Fatal(err)
	}
	if got["command"] != "gopls.run_tests" {
		t.Errorf("command = %v", got["command"])
	}
	args, _ := got["arguments"].([]any)
	if len(args) != 2 || args[0] != "TestF" || args[1] != float64(3) {
		t.Errorf("arguments = %v", got["arguments"])
	}
}

// The resolve capability is read from codeLensProvider's resolveProvider
// option: a bare true asks for no resolves, and only an object naming it does.
// Reading the provider alone would send a method a server never advertised.
func TestCodeLensResolveCapability(t *testing.T) {
	cases := []struct {
		name string
		caps ServerCapabilities
		want bool
	}{
		{"absent", ServerCapabilities{}, false},
		{"false", ServerCapabilities{CodeLensProvider: json.RawMessage(`false`)}, false},
		{"bare true", ServerCapabilities{CodeLensProvider: json.RawMessage(`true`)}, false},
		{"options without resolve", ServerCapabilities{CodeLensProvider: json.RawMessage(`{"workDoneProgress":true}`)}, false},
		{"resolve provider", ServerCapabilities{CodeLensProvider: json.RawMessage(`{"resolveProvider":true}`)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.caps.CodeLensResolve(); got != c.want {
				t.Errorf("CodeLensResolve = %v, want %v", got, c.want)
			}
		})
	}
}
