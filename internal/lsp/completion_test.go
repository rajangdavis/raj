package lsp

import (
	"encoding/json"
	"testing"
)

func labels(items []CompletionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Label)
	}
	return out
}

func complete(t *testing.T, raw string) ([]CompletionItem, bool) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/completion", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	items, incomplete, err := Completions(ctx, f.conn, "/w/a.go", Position{})
	if err != nil {
		t.Fatal(err)
	}
	return items, incomplete
}

// The result is a list object or a bare array, and servers differ.
func TestCompletionShapes(t *testing.T) {
	object := `{"isIncomplete":false,"items":[{"label":"Foo"},{"label":"Bar"}]}`
	array := `[{"label":"Foo"},{"label":"Bar"}]`
	for _, raw := range []string{object, array} {
		items, _ := complete(t, raw)
		if len(items) != 2 {
			t.Errorf("%s: got %d items", raw, len(items))
		}
	}
	if items, _ := complete(t, `null`); len(items) != 0 {
		t.Errorf("null produced %d items", len(items))
	}
}

// isIncomplete is reported rather than acted on: whether to re-ask is a policy
// question about keystrokes and belongs where the keystrokes are.
func TestIncompleteIsReported(t *testing.T) {
	if _, inc := complete(t, `{"isIncomplete":true,"items":[{"label":"Foo"}]}`); !inc {
		t.Error("isIncomplete was lost")
	}
	if _, inc := complete(t, `{"isIncomplete":false,"items":[{"label":"Foo"}]}`); inc {
		t.Error("a complete list was reported as incomplete")
	}
}

// A snippet is a template with tab stops. raj has no snippet engine, so
// inserting one literally would type ${1:format} into the buffer — worse than
// inserting a plain identifier.
func TestSnippetsInsertTheLabel(t *testing.T) {
	raw := `{"items":[{"label":"Errorf","insertText":"Errorf(${1:format})","insertTextFormat":2}]}`
	items, _ := complete(t, raw)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Insert != "Errorf" {
		t.Errorf("insert = %q, want the plain label", items[0].Insert)
	}
}

// A plain insertText is honoured, since it is what the server wants typed.
func TestPlainInsertTextIsUsed(t *testing.T) {
	raw := `{"items":[{"label":"Foo(x int)","insertText":"Foo","insertTextFormat":1}]}`
	items, _ := complete(t, raw)
	if items[0].Insert != "Foo" {
		t.Errorf("insert = %q, want Foo", items[0].Insert)
	}
	if items[0].Label != "Foo(x int)" {
		t.Errorf("label = %q, want the full signature", items[0].Label)
	}
}

// Label and filter text differ more often than it looks: gopls labels a method
// with its signature and filters on the bare name, so matching the label would
// require typing a parenthesis to keep a match alive.
func TestFilterKeyPrefersFilterText(t *testing.T) {
	raw := `{"items":[{"label":"Foo(x int) error","filterText":"Foo"}]}`
	items, _ := complete(t, raw)
	if got := items[0].FilterKey(); got != "Foo" {
		t.Errorf("filter key = %q, want Foo", got)
	}
	kept := FilterItems(items, "Fo")
	if len(kept) != 1 {
		t.Error("filtering on the prefix dropped a match")
	}
	if len(FilterItems(items, "Foo(")) != 0 {
		t.Error("the label was matched instead of the filter text")
	}
}

// The server's sort keys encode ranking the client cannot reproduce — scope,
// type compatibility, recency. Ignoring them throws away the reason to ask a
// language server rather than scan the buffer.
func TestServerOrderIsHonoured(t *testing.T) {
	raw := `{"items":[
		{"label":"Zebra","sortText":"00"},
		{"label":"Apple","sortText":"99"},
		{"label":"Mango","sortText":"50"}]}`
	items, _ := complete(t, raw)
	SortItems(items)
	want := []string{"Zebra", "Mango", "Apple"}
	for i, w := range want {
		if items[i].Label != w {
			t.Fatalf("order = %v, want %v", labels(items), want)
		}
	}
}

// Without sort keys the label orders, and the order is total either way: an
// unstable list reshuffles between identical keystrokes.
func TestSortIsTotal(t *testing.T) {
	raw := `{"items":[{"label":"b","sortText":"x"},{"label":"a","sortText":"x"},{"label":"c"}]}`
	items, _ := complete(t, raw)
	SortItems(items)
	first := labels(items)
	for i := 0; i < 20; i++ {
		SortItems(items)
		got := labels(items)
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d: %v, want %v", i, got, first)
			}
		}
	}
}

// Filtering is case-insensitive and keeps the server's order.
func TestFilterKeepsOrderAndIgnoresCase(t *testing.T) {
	items := []CompletionItem{
		{Label: "Handler"}, {Label: "handleRequest"}, {Label: "Other"}, {Label: "HANDOFF"},
	}
	got := labels(FilterItems(items, "hand"))
	want := []string{"Handler", "handleRequest", "HANDOFF"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if len(FilterItems(items, "")) != 4 {
		t.Error("an empty prefix should keep everything")
	}
}

// Items with no label are dropped rather than shown as blank rows.
func TestUnlabelledItemsAreDropped(t *testing.T) {
	items, _ := complete(t, `{"items":[{"label":""},{"label":"Foo"},{"detail":"no label"}]}`)
	if len(items) != 1 || items[0].Label != "Foo" {
		t.Errorf("got %v, want just Foo", labels(items))
	}
}

// Nonsense from a server produces nothing rather than a panic.
func TestMalformedCompletionsAreSurvived(t *testing.T) {
	for _, raw := range []string{`{}`, `[]`, `"text"`, `42`, `{"items":null}`, `{"items":[null]}`} {
		complete(t, raw)
	}
	if _, _, err := Completions(nil, nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}
