package lsp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func codeActionCtx(t *testing.T) (context.Context, func()) {
	t.Helper()
	return context.WithTimeout(context.Background(), time.Second)
}

// A bare Command result is the legacy shape: the entry's own title and command
// fields are the action. Without decoding it as a Command, a server that
// returns commands only would decode an empty list and the picker would never
// open.
func TestDecodeCommandLiteral(t *testing.T) {
	got := decodeCodeActions(json.RawMessage(`[
		{"title":"Run go test","command":"gopls.run_tests","arguments":[1,"x"]}
	]`))
	if len(got) != 1 {
		t.Fatalf("decoded %d actions, want 1", len(got))
	}
	a := got[0]
	if a.Title != "Run go test" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Edit != nil {
		t.Errorf("edit = %+v, want none", a.Edit)
	}
	if a.Command == nil || a.Command.Command != "gopls.run_tests" {
		t.Fatalf("command = %+v, want gopls.run_tests", a.Command)
	}
	if len(a.Command.Arguments) != 2 {
		t.Errorf("arguments = %d, want 2", len(a.Command.Arguments))
	}
	if a.NeedsResolve() {
		t.Error("a command action was reported as needing resolve")
	}
}

// A CodeAction literal with an edit and a command decodes both: the edit is
// what applying means, and the command is what the server wants run after it.
// Dropping either would apply half an action.
func TestDecodeCodeActionWithEditAndCommand(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"title":"Import fmt",
			"kind":"quickfix",
			"isPreferred":true,
			"diagnostics":[{"range":{"start":{"line":2,"character":1},"end":{"line":2,"character":4}},"message":"undefined: fmt"}],
			"edit":{"changes":{"file:///w/a.go":[
				{"range":{"start":{"line":2,"character":0},"end":{"line":2,"character":0}},"newText":"\t\"fmt\"\n"}
			]}},
			"command":{"title":"Run gopls","command":"gopls.fix","arguments":[true]}
		}
	]`)
	got := decodeCodeActions(raw)
	if len(got) != 1 {
		t.Fatalf("decoded %d actions, want 1", len(got))
	}
	a := got[0]
	if a.Kind != "quickfix" || !a.IsPreferred {
		t.Errorf("kind = %q preferred = %v", a.Kind, a.IsPreferred)
	}
	if a.NeedsResolve() {
		t.Error("an action with an edit was reported as needing resolve")
	}
	if a.Edit == nil {
		t.Fatal("the edit was dropped")
	}
	if len(a.Edit.Docs) != 1 || a.Edit.Docs[0].Path != "/w/a.go" {
		t.Fatalf("docs = %+v, want one /w/a.go", a.Edit.Docs)
	}
	if len(a.Edit.Docs[0].Edits) != 1 || a.Edit.Docs[0].Edits[0].NewText != "\t\"fmt\"\n" {
		t.Errorf("edits = %+v", a.Edit.Docs[0].Edits)
	}
	if a.Command == nil || a.Command.Command != "gopls.fix" {
		t.Fatalf("command = %+v", a.Command)
	}
	if len(a.Command.Arguments) != 1 {
		t.Errorf("arguments = %d, want 1", len(a.Command.Arguments))
	}
	if len(a.Diagnostics) != 1 || a.Diagnostics[0].Message != "undefined: fmt" {
		t.Errorf("diagnostics = %+v", a.Diagnostics)
	}
}

// A CodeAction literal with a command but no edit is still actionable: the
// command runs. Reading only "edit" would leave the picker row selecting
// nothing.
func TestDecodeCodeActionCommandOnly(t *testing.T) {
	got := decodeCodeActions(json.RawMessage(`[
		{"title":"Organize imports","command":{"title":"Organize","command":"gopls.organize"}}
	]`))
	if len(got) != 1 {
		t.Fatalf("decoded %d actions, want 1", len(got))
	}
	a := got[0]
	if a.Edit != nil {
		t.Errorf("edit = %+v, want none", a.Edit)
	}
	if a.Command == nil || a.Command.Command != "gopls.organize" {
		t.Fatalf("command = %+v", a.Command)
	}
	if a.Command.Title != "Organize" {
		t.Errorf("command title = %q, want the nested title", a.Command.Title)
	}
}

// A resolve-only action carries data and nothing else. It must survive decoding
// so the caller can refuse it by name; dropping it would leave the user
// choosing from a list that silently omits the fix they can see the server
// meant to offer.
func TestDecodeResolveOnlyActionIsKept(t *testing.T) {
	got := decodeCodeActions(json.RawMessage(`[
		{"title":"Extract function","kind":"refactor.extract","data":{"token":"abc"}}
	]`))
	if len(got) != 1 {
		t.Fatalf("decoded %d actions, want 1", len(got))
	}
	if !got[0].NeedsResolve() {
		t.Error("a data-only action did not report that it needs resolve")
	}
	if len(got[0].Data) == 0 {
		t.Error("the resolve data was dropped")
	}
}

// A code action's edit can mix text edits with file operations. The shared
// WorkspaceEdit decoder records the operations rather than dropping them, and
// the code-action caller refuses the whole edit when any are present. This
// pins that the decode surfaces them: a dropped resource op would be a
// half-applied action.
func TestCodeActionEditSurfacesResourceOps(t *testing.T) {
	raw := json.RawMessage(`[
		{"title":"Split file","edit":{"documentChanges":[
			{"textDocument":{"uri":"file:///w/a.go","version":3},
			 "edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"var"}]},
			{"kind":"create","uri":"file:///w/new.go"}
		]}}
	]`)
	got := decodeCodeActions(raw)
	if len(got) != 1 || got[0].Edit == nil {
		t.Fatalf("actions = %+v, want one with an edit", got)
	}
	we := got[0].Edit
	if len(we.Docs) != 1 || we.Docs[0].Path != "/w/a.go" {
		t.Errorf("docs = %+v, want /w/a.go", we.Docs)
	}
	if len(we.ResourceOps) != 1 || we.ResourceOps[0] != "create" {
		t.Errorf("resource ops = %v, want create", we.ResourceOps)
	}
}

// The request carries the caret's range and the diagnostics the range is about,
// which is the context a quick-fix server keys on. Without them a server can
// only offer actions that apply to the whole file.
func TestRequestCodeActionsSendsRangeAndDiagnostics(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/codeAction", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[{"title":"Fix it","command":"x"}]`), nil
	})
	ctx, cancel := codeActionCtx(t)
	defer cancel()

	r := Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 9}}
	diags := []Diagnostic{{
		Range:   Range{Start: Position{Line: 2, Character: 1}, End: Position{Line: 2, Character: 4}},
		Message: "undefined: fmt",
	}}
	acts, err := RequestCodeActions(ctx, f.conn, "/w/a.go", r, diags)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Title != "Fix it" {
		t.Fatalf("actions = %+v", acts)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("textDocument = %v", doc)
	}
	sent, _ := got["range"].(map[string]any)
	if sent == nil {
		t.Fatalf("no range sent: %v", got)
	}
	ctxObj, _ := got["context"].(map[string]any)
	if ctxObj == nil {
		t.Fatalf("no context sent: %v", got)
	}
	sentDiags, _ := ctxObj["diagnostics"].([]any)
	if len(sentDiags) != 1 {
		t.Fatalf("context.diagnostics = %v, want one", ctxObj["diagnostics"])
	}
	first, _ := sentDiags[0].(map[string]any)
	if first["message"] != "undefined: fmt" {
		t.Errorf("diagnostic message = %v", first["message"])
	}
}

// Intersecting is the context's selection rule. A zero-width range inside a
// diagnostic counts, because that is a caret resting on the problem with
// nothing selected; a range on another line does not.
func TestIntersectingDiagnostics(t *testing.T) {
	diags := []Diagnostic{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}}, Message: "a"},
		{Range: Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 3}}, Message: "b"},
	}
	cases := []struct {
		name string
		r    Range
		want []string
	}{
		{"zero width inside", Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 3}}, []string{"a"}},
		{"line range covering both ends", Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}}, []string{"a"}},
		{"another line", Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 0}}, nil},
		{"second diagnostic", Range{Start: Position{Line: 2, Character: 1}, End: Position{Line: 2, Character: 2}}, []string{"b"}},
		{"across both", Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 2, Character: 3}}, []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Intersecting(diags, c.r)
			if len(got) != len(c.want) {
				t.Fatalf("intersecting = %+v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i].Message != c.want[i] {
					t.Errorf("got %q at %d, want %q", got[i].Message, i, c.want[i])
				}
			}
		})
	}
}

// ExecuteCommand sends the server's own command and arguments through
// unchanged. Rewriting or reordering them would break an opaque protocol the
// client cannot interpret.
func TestExecuteCommandSendsNameAndArguments(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("workspace/executeCommand", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return nil, nil
	})
	ctx, cancel := codeActionCtx(t)
	defer cancel()

	err := ExecuteCommand(ctx, f.conn, Command{
		Command:   "gopls.apply_fix",
		Arguments: []json.RawMessage{json.RawMessage(`"addImport"`), json.RawMessage(`3`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["command"] != "gopls.apply_fix" {
		t.Errorf("command = %v", got["command"])
	}
	args, _ := got["arguments"].([]any)
	if len(args) != 2 || args[0] != "addImport" {
		t.Errorf("arguments = %v", got["arguments"])
	}
}

// A resolve sends the action's title, kind and data back, and the returned edit
// is decoded so the caller can apply it. Dropping the data would send the
// server an action it cannot identify; dropping the edit would apply nothing.
func TestResolveCodeActionSendsTitleDataAndDecodesTheEdit(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("codeAction/resolve", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{
			"title":"Extract function",
			"kind":"refactor.extract",
			"edit":{"changes":{"file:///w/a.go":[
				{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"newText":"func f(){}"}
			]}}
		}`), nil
	})
	ctx, cancel := codeActionCtx(t)
	defer cancel()

	in := CodeAction{
		Title: "Extract function",
		Kind:  "refactor.extract",
		Data:  json.RawMessage(`{"token":"abc"}`),
	}
	out, err := ResolveCodeAction(ctx, f.conn, in)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Extract function" || got["kind"] != "refactor.extract" {
		t.Errorf("sent title/kind = %v/%v", got["title"], got["kind"])
	}
	data, _ := got["data"].(map[string]any)
	if data == nil || data["token"] != "abc" {
		t.Errorf("data sent = %v, want the token", got["data"])
	}
	if out.Edit == nil || len(out.Edit.Docs) != 1 {
		t.Fatalf("resolved = %+v, want the returned edit", out)
	}
	if out.Edit.Docs[0].Path != "/w/a.go" || out.Edit.Docs[0].Edits[0].NewText != "func f(){}" {
		t.Errorf("edit = %+v", out.Edit.Docs[0])
	}
}

// A resolve that answers with nothing usable leaves the action as it was, so
// the caller can refuse it by name rather than applying an empty result. The
// data survives, so the action is still identifiable for a retry.
func TestResolveCodeActionWithoutAnEditLeavesItUnresolved(t *testing.T) {
	f := newFake(t)
	f.on("codeAction/resolve", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"title":"Extract function","data":{"token":"abc"}}`), nil
	})
	ctx, cancel := codeActionCtx(t)
	defer cancel()

	in := CodeAction{Title: "Extract function", Data: json.RawMessage(`{"token":"abc"}`)}
	out, err := ResolveCodeAction(ctx, f.conn, in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedsResolve() {
		t.Errorf("resolved = %+v, want it still needing resolve", out)
	}
	if len(out.Data) == 0 {
		t.Error("the data was dropped by an empty resolve")
	}
}

// The resolve capability is read from codeActionProvider's resolveProvider
// option: a bare true asks for no resolves, and only an object naming it does.
// Reading the provider alone would send a method a server never advertised.
func TestCodeActionResolveCapability(t *testing.T) {
	cases := []struct {
		name string
		caps ServerCapabilities
		want bool
	}{
		{"absent", ServerCapabilities{}, false},
		{"false", ServerCapabilities{CodeActionProvider: json.RawMessage(`false`)}, false},
		{"bare true", ServerCapabilities{CodeActionProvider: json.RawMessage(`true`)}, false},
		{"options without resolve", ServerCapabilities{CodeActionProvider: json.RawMessage(`{"codeActionKinds":["quickfix"]}`)}, false},
		{"resolve provider", ServerCapabilities{CodeActionProvider: json.RawMessage(`{"resolveProvider":true}`)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.caps.CodeActionResolve(); got != c.want {
				t.Errorf("CodeActionResolve = %v, want %v", got, c.want)
			}
		})
	}
}
