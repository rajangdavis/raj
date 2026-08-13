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
		ctx, cancel := ctx1s(t)
		RequestDefinition(ctx, f.conn, "/w/a.go", Position{})
		RequestHover(ctx, f.conn, "/w/a.go", Position{})
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

	f := newFake(t)
	f.die()
	waitFor(t, f.conn.Closed)
	if _, err := RequestHover(context.Background(), f.conn, "/w/a.go", Position{}); err == nil {
		t.Error("a hover on a dead connection succeeded")
	}
}
