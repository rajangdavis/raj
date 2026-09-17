package lsp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ctx1s(t *testing.T) (context.Context, func()) {
	t.Helper()
	return context.WithTimeout(context.Background(), time.Second)
}

// Hover contents has had three legal shapes across versions of the protocol,
// and servers in the wild still send all of them. Decoding is permissive about
// shape and strict about meaning.
func TestHoverContentShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"markup object", `{"contents":{"kind":"markdown","value":"func F()"}}`, "func F()"},
		{"plain string", `{"contents":"func F()"}`, "func F()"},
		{"marked string object", `{"contents":{"language":"go","value":"func F()"}}`, "func F()"},
		{"array of strings", `{"contents":["func F()","does a thing"]}`, "func F()\n\ndoes a thing"},
		{"array of objects", `{"contents":[{"language":"go","value":"func F()"},"docs"]}`, "func F()\n\ndocs"},
		{"fenced markdown", "{\"contents\":{\"kind\":\"markdown\",\"value\":\"```go\\nfunc F()\\n```\\n\\ndocs\"}}", "func F()\n\ndocs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("textDocument/hover", func(*Message) (any, *ResponseError) {
				return json.RawMessage(c.raw), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			h, err := RequestHover(ctx, f.conn, "/w/a.go", Position{Line: 1, Character: 2})
			if err != nil {
				t.Fatal(err)
			}
			if h == nil {
				t.Fatal("no hover decoded")
			}
			if h.Text != c.want {
				t.Errorf("text = %q, want %q", h.Text, c.want)
			}
		})
	}
}

// Nothing at the cursor is the normal answer for most positions in most files.
// It is not an error, and reporting it as one would show a failure every time
// the cursor rested on a comma.
func TestHoverEmptyIsNotAnError(t *testing.T) {
	for _, raw := range []string{`null`, `{"contents":""}`, `{"contents":[]}`, `{"contents":null}`} {
		f := newFake(t)
		f.on("textDocument/hover", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		h, err := RequestHover(ctx, f.conn, "/w/a.go", Position{})
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if h != nil {
			t.Errorf("%s produced a hover: %+v", raw, h)
		}
	}
}

// Markdown is flattened rather than rendered: raj draws into a cell grid, and a
// hover showing literal backticks is worse than one showing the signature they
// were wrapping.
func TestMarkupIsFlattened(t *testing.T) {
	in := "```go\nfunc F(x int) error\n```\n\n\n\nDoes a thing.\n\n\n\nAnd another.\n\n"
	got := cleanMarkup(in)
	want := "func F(x int) error\n\nDoes a thing.\n\nAnd another."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "```") {
		t.Error("a fence survived")
	}
}

// The request carries the position it was asked about. A hover answered for
// the wrong position is not an error — it is a confidently wrong answer.
func TestHoverSendsThePosition(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/hover", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{"contents":"x"}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	RequestHover(ctx, f.conn, "/w/a.go", Position{Line: 12, Character: 34})

	pos, _ := got["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(12) || pos["character"] != float64(34) {
		t.Errorf("position sent as %v, want 12:34", pos)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
}

// A definition may come back as one location, an array, or null. A server that
// returns one for most symbols returns an array for an interface method.
func TestDefinitionShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"single location", `{"uri":"file:///w/b.go","range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}}}`, 1},
		{"array", `[{"uri":"file:///w/b.go","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":4}}},{"uri":"file:///w/c.go","range":{"start":{"line":2,"character":0},"end":{"line":2,"character":4}}}]`, 2},
		{"null", `null`, 0},
		{"empty array", `[]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("textDocument/definition", func(*Message) (any, *ResponseError) {
				return json.RawMessage(c.raw), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			locs, err := RequestDefinition(ctx, f.conn, "/w/a.go", Position{})
			if err != nil {
				t.Fatal(err)
			}
			if len(locs) != c.want {
				t.Errorf("got %d locations, want %d: %+v", len(locs), c.want, locs)
			}
		})
	}
}

// A LocationLink names its target under different keys, and carries both the
// whole declaration and the identifier within it. Jumping to the identifier is
// what "where is this defined" means.
func TestDefinitionLocationLink(t *testing.T) {
	raw := `[{"targetUri":"file:///w/b.go",
	  "targetRange":{"start":{"line":8,"character":0},"end":{"line":12,"character":1}},
	  "targetSelectionRange":{"start":{"line":10,"character":5},"end":{"line":10,"character":9}}}]`
	f := newFake(t)
	f.on("textDocument/definition", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	locs, err := RequestDefinition(ctx, f.conn, "/w/a.go", Position{})
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 {
		t.Fatalf("got %d locations", len(locs))
	}
	if locs[0].Path != "/w/b.go" {
		t.Errorf("path = %q", locs[0].Path)
	}
	if locs[0].Range.Start.Line != 10 {
		t.Errorf("jumped to line %d, want 10 — the identifier, not the doc comment",
			locs[0].Range.Start.Line)
	}
}

// A URI with escapes comes back as the path it named, or the jump opens
// nothing.
func TestDefinitionDecodesEscapedURIs(t *testing.T) {
	raw := `{"uri":"file:///w/my%20project/a.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}`
	f := newFake(t)
	f.on("textDocument/definition", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	locs, _ := RequestDefinition(ctx, f.conn, "/w/a.go", Position{})
	if len(locs) != 1 || locs[0].Path != "/w/my project/a.go" {
		t.Errorf("got %+v, want the unescaped path", locs)
	}
}

// References is the inverse of a definition: every place a symbol is used
// rather than the one place it is declared. The result is a location array or
// null, and an empty list is "no references" rather than an error.
func TestReferencesShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"array", `[{"uri":"file:///w/b.go","range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}}},{"uri":"file:///w/c.go","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":4}}}]`, 2},
		{"location link", `[{"targetUri":"file:///w/b.go","targetRange":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}},"targetSelectionRange":{"start":{"line":3,"character":7},"end":{"line":3,"character":8}}}]`, 1},
		{"empty array", `[]`, 0},
		{"null", `null`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("textDocument/references", func(*Message) (any, *ResponseError) {
				return json.RawMessage(c.raw), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			locs, err := RequestReferences(ctx, f.conn, "/w/a.go", Position{}, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(locs) != c.want {
				t.Errorf("got %d locations, want %d: %+v", len(locs), c.want, locs)
			}
		})
	}
}

// The request names the position it was asked about and asks the server to
// include the declaration. A list of callers without the definition it belongs
// to, or one answered for the wrong position, is confidently wrong.
func TestReferencesSendsContext(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/references", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestReferences(ctx, f.conn, "/w/a.go", Position{Line: 12, Character: 34}, true); err != nil {
		t.Fatal(err)
	}
	pos, _ := got["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(12) || pos["character"] != float64(34) {
		t.Errorf("position sent as %v, want 12:34", pos)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	refctx, _ := got["context"].(map[string]any)
	if refctx == nil || refctx["includeDeclaration"] != true {
		t.Errorf("context sent as %v, want includeDeclaration=true", got["context"])
	}
}

// workspace/symbol is an array of SymbolInformation or the newer
// WorkspaceSymbol; both carry a name, a kind and a location. A location with a
// document URI and no range is legal, and the symbol must be kept rather than
// dropped: a list quietly missing symbols is wrong in a way the caller cannot
// see.
func TestWorkspaceSymbolShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  int
		first WorkspaceSymbol
	}{
		{
			name: "symbol information",
			raw:  `[{"name":"F","kind":12,"containerName":"pkg","location":{"uri":"file:///w/b.go","range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}}}}]`,
			want: 1,
			first: WorkspaceSymbol{Name: "F", Kind: 12, Container: "pkg",
				Location: Location{Path: "/w/b.go", Range: Range{
					Start: Position{Line: 3, Character: 5}, End: Position{Line: 3, Character: 9},
				}}, HasRange: true},
		},
		{
			name:  "no range",
			raw:   `[{"name":"G","kind":13,"location":{"uri":"file:///w/c.go"}}]`,
			want:  1,
			first: WorkspaceSymbol{Name: "G", Kind: 13, Location: Location{Path: "/w/c.go"}},
		},
		{
			name: "null",
			raw:  `null`,
			want: 0,
		},
		{
			name: "empty array",
			raw:  `[]`,
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("workspace/symbol", func(*Message) (any, *ResponseError) {
				return json.RawMessage(c.raw), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			syms, err := RequestWorkspaceSymbols(ctx, f.conn, "F")
			if err != nil {
				t.Fatal(err)
			}
			if len(syms) != c.want {
				t.Fatalf("got %d symbols, want %d: %+v", len(syms), c.want, syms)
			}
			if c.want == 0 {
				return
			}
			if syms[0] != c.first {
				t.Errorf("first symbol = %+v, want %+v", syms[0], c.first)
			}
		})
	}
}

// The query is the request: a server is asked which symbols match it, and a
// client that dropped it would get everything or the wrong set, neither of
// which reads as an error.
func TestWorkspaceSymbolSendsQuery(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("workspace/symbol", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestWorkspaceSymbols(ctx, f.conn, "Reader"); err != nil {
		t.Fatal(err)
	}
	if got["query"] != "Reader" {
		t.Errorf("query sent as %v, want Reader", got["query"])
	}
}

// Declaration, type definition and implementation are definition's siblings:
// one position in, a location list out, differing only in the method name. Each
// must send its own method and decode through the shared location decode — a
// method typo is invisible until a server answers method-not-found at runtime,
// and a decode that returned nothing would make the feature silently jump
// nowhere.
func TestSiblingLocationRequests(t *testing.T) {
	cases := []struct {
		name   string
		method string
		call   func(context.Context, *Conn, string, Position) ([]Location, error)
	}{
		{"declaration", "textDocument/declaration", RequestDeclaration},
		{"type definition", "textDocument/typeDefinition", RequestTypeDefinition},
		{"implementation", "textDocument/implementation", RequestImplementation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			var got map[string]any
			f.on(c.method, func(m *Message) (any, *ResponseError) {
				json.Unmarshal(m.Params, &got)
				return json.RawMessage(`{"uri":"file:///w/b.go","range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}}}`), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			locs, err := c.call(ctx, f.conn, "/w/a.go", Position{Line: 12, Character: 34})
			if err != nil {
				t.Fatal(err)
			}
			if len(locs) != 1 || locs[0].Path != "/w/b.go" || locs[0].Range.Start.Line != 3 {
				t.Fatalf("got %+v, want the one location at /w/b.go:3", locs)
			}
			pos, _ := got["position"].(map[string]any)
			if pos == nil || pos["line"] != float64(12) || pos["character"] != float64(34) {
				t.Errorf("position sent as %v, want 12:34", pos)
			}
			doc, _ := got["textDocument"].(map[string]any)
			if doc == nil || doc["uri"] != "file:///w/a.go" {
				t.Errorf("uri sent as %v", doc)
			}
		})
	}
}

// Nonsense from a server produces nothing rather than a panic. Servers send
// shapes no version of the specification describes.
func TestMalformedResponsesAreSurvived(t *testing.T) {
	for _, raw := range []string{
		`{}`, `[]`, `"just a string"`, `42`, `{"uri":"file:///a"}`,
		`{"range":{"start":{"line":1,"character":1},"end":{"line":1,"character":2}}}`,
		`[{"uri":""},{"uri":"file:///b.go"}]`,
	} {
		f := newFake(t)
		f.on("textDocument/definition", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		f.on("textDocument/hover", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		f.on("workspace/symbol", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		RequestDefinition(ctx, f.conn, "/w/a.go", Position{})
		RequestHover(ctx, f.conn, "/w/a.go", Position{})
		RequestWorkspaceSymbols(ctx, f.conn, "")
		cancel()
	}
}

// A dead connection fails rather than panicking, since the server may die
// between any two keystrokes.
func TestRequestsOnADeadConnection(t *testing.T) {
	if _, err := RequestHover(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("hover err = %v, want ErrClosed", err)
	}
	if _, err := RequestDefinition(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("definition err = %v, want ErrClosed", err)
	}
	if _, err := RequestReferences(context.Background(), nil, "/w/a.go", Position{}, true); err != ErrClosed {
		t.Errorf("references err = %v, want ErrClosed", err)
	}
	if _, err := RequestDeclaration(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("declaration err = %v, want ErrClosed", err)
	}
	if _, err := RequestTypeDefinition(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("type definition err = %v, want ErrClosed", err)
	}
	if _, err := RequestImplementation(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("implementation err = %v, want ErrClosed", err)
	}
	if _, err := RequestWorkspaceSymbols(context.Background(), nil, "x"); err != ErrClosed {
		t.Errorf("workspace symbols err = %v, want ErrClosed", err)
	}

	f := newFake(t)
	f.die()
	waitFor(t, f.conn.Closed)
	if _, err := RequestHover(context.Background(), f.conn, "/w/a.go", Position{}); err == nil {
		t.Error("a hover on a dead connection succeeded")
	}
}
