package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// docLinks decodes one server answer through the real request path.
func docLinks(t *testing.T, raw string) ([]DocumentLink, error) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/documentLink", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	return RequestDocumentLinks(ctx, f.conn, "/w/a.go")
}

// A link is a range plus a target; dropping the target leaves a link that can
// never be followed, and dropping the range leaves nothing to aim at. The
// tooltip and data must survive too: the tooltip is what a caller shows, and
// the data is what a resolve sends back.
func TestDocumentLinkDecodesRangeTargetTooltipAndData(t *testing.T) {
	links, err := docLinks(t, `[
		{"range":{"start":{"line":3,"character":2},"end":{"line":3,"character":20}},
		 "target":"file:///w/other.go",
		 "tooltip":"Open other.go",
		 "data":{"token":"abc"}}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	l := links[0]
	if l.Range.Start != (Position{Line: 3, Character: 2}) || l.Range.End != (Position{Line: 3, Character: 20}) {
		t.Errorf("range = %+v", l.Range)
	}
	if l.Target != "file:///w/other.go" {
		t.Errorf("target = %q", l.Target)
	}
	if l.Tooltip != "Open other.go" {
		t.Errorf("tooltip = %q", l.Tooltip)
	}
	if len(l.Data) == 0 {
		t.Error("the data was dropped")
	}
	if l.NeedsResolve() {
		t.Error("a targeted link was reported as needing resolve")
	}
}

// A link with no target is the resolve case: it must survive decoding, with
// NeedsResolve true, or the server's link disappears instead of being named.
func TestDocumentLinkKeepsAnUnresolvedLink(t *testing.T) {
	links, err := docLinks(t, `[
		{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":4}},"data":{"token":"abc"}}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if !links[0].NeedsResolve() {
		t.Error("a targetless link was not reported as needing resolve")
	}
	if len(links[0].Data) == 0 {
		t.Error("the data was dropped")
	}
}

// null and [] both mean no links, not an error.
func TestDocumentLinkNullAndEmptyAreNoLinks(t *testing.T) {
	for _, raw := range []string{`null`, `[]`} {
		links, err := docLinks(t, raw)
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(links) != 0 {
			t.Errorf("%s produced %d links", raw, len(links))
		}
	}
}

// One malformed entry does not hide the links beside it: a whole answer dropped
// because of one bad element would make a link-heavy file unusable.
func TestDocumentLinkMalformedEntryIsDropped(t *testing.T) {
	links, err := docLinks(t, `[
		{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":4}},"target":"file:///w/a.go"},
		"nonsense",
		{"range":{"start":{"line":2,"character":0},"end":{"line":2,"character":4}},"target":"file:///w/b.go"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want the two good ones", len(links))
	}
	if links[0].Target != "file:///w/a.go" || links[1].Target != "file:///w/b.go" {
		t.Errorf("links = %+v", links)
	}
}

// A resolve sends the link back with its data and returns the target the server
// filled in. Dropping the data would send the server a link it cannot identify.
func TestResolveDocumentLinkSendsData(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("documentLink/resolve", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`{"range":{"start":{"line":2}},"target":"file:///w/other.go"}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	in := DocumentLink{Range: Range{Start: Position{Line: 2}}, Data: json.RawMessage(`{"token":"abc"}`)}
	out, err := ResolveDocumentLink(ctx, f.conn, in)
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
	if out.Target != "file:///w/other.go" || out.NeedsResolve() {
		t.Errorf("resolved = %+v, want the target filled in", out)
	}
}

// A resolve that answers with no target leaves the link as it was, so the
// caller can refuse it by name rather than opening nothing.
func TestResolveDocumentLinkWithoutATargetLeavesItUnresolved(t *testing.T) {
	f := newFake(t)
	f.on("documentLink/resolve", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"range":{"start":{"line":2}},"data":{"token":"abc"}}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	in := DocumentLink{Range: Range{Start: Position{Line: 2}}, Data: json.RawMessage(`{"token":"abc"}`)}
	out, err := ResolveDocumentLink(ctx, f.conn, in)
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
func TestDocumentLinksOnANilConn(t *testing.T) {
	if _, err := RequestDocumentLinks(context.Background(), nil, "/w/a.go"); err != ErrClosed {
		t.Errorf("RequestDocumentLinks err = %v, want ErrClosed", err)
	}
	if _, err := ResolveDocumentLink(context.Background(), nil, DocumentLink{}); err != ErrClosed {
		t.Errorf("ResolveDocumentLink err = %v, want ErrClosed", err)
	}
}

// LinkAt picks the link whose half-open range contains the caret, and no other.
// A range end is exclusive, so a caret at the end of one link is at the start
// of the next rather than in both. Without the rule the follow path has no way
// to choose between an adjacent pair.
func TestLinkAtIsHalfOpen(t *testing.T) {
	links := []DocumentLink{
		{Range: Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 5}}, Target: "file:///a"},
		{Range: Range{Start: Position{Line: 1, Character: 5}, End: Position{Line: 1, Character: 9}}, Target: "file:///b"},
	}
	cases := []struct {
		name string
		pos  Position
		want string
	}{
		{"before the first", Position{Line: 1, Character: 1}, ""},
		{"at the first start", Position{Line: 1, Character: 2}, "file:///a"},
		{"inside the first", Position{Line: 1, Character: 4}, "file:///a"},
		{"at the first end", Position{Line: 1, Character: 5}, "file:///b"},
		{"inside the second", Position{Line: 1, Character: 7}, "file:///b"},
		{"at the second end", Position{Line: 1, Character: 9}, ""},
		{"another line", Position{Line: 0, Character: 3}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := LinkAt(links, c.pos)
			if c.want == "" {
				if ok {
					t.Errorf("LinkAt(%+v) = %q, want none", c.pos, got.Target)
				}
				return
			}
			if !ok || got.Target != c.want {
				t.Errorf("LinkAt(%+v) = %q,%v want %q", c.pos, got.Target, ok, c.want)
			}
		})
	}
}

// LinkScheme names the scheme so the follow path can tell a file the editor can
// open from a URI it cannot, and speak the scheme in the refusal.
func TestLinkScheme(t *testing.T) {
	cases := []struct{ target, want string }{
		{"file:///w/a.go", "file"},
		{"https://example.com/x", "https"},
		{"mailto:dev@example.com", "mailto"},
		{"urn:isbn:123", "urn"},
		{"", ""},
		{"relative/path.go", ""},
	}
	for _, c := range cases {
		if got := LinkScheme(c.target); got != c.want {
			t.Errorf("LinkScheme(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

// The resolve capability is read from the provider options, with the same rule
// the other accessors use: a bare true asked for no resolves.
func TestDocumentLinkResolveCapability(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "", false},
		{"false", `false`, false},
		{"bare true", `true`, false},
		{"resolve false", `{"resolveProvider":false}`, false},
		{"resolve true", `{"resolveProvider":true}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps := ServerCapabilities{}
			if c.raw != "" {
				caps.DocumentLinkProvider = json.RawMessage(c.raw)
			}
			if got := caps.DocumentLinkResolve(); got != c.want {
				t.Errorf("DocumentLinkResolve() = %v, want %v", got, c.want)
			}
		})
	}
}
