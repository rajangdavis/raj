package lsp

import (
	"encoding/json"
	"strings"
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

// A textEdit carries its own range, which is what lets a server overwrite the
// word rather than only extend it. Both fields are decoded so acceptance can
// apply the server edit instead of typing a word.
func TestTextEditDecodes(t *testing.T) {
	raw := `{"items":[{"label":"rand","textEdit":{"range":{"start":{"line":3,"character":8},"end":{"line":3,"character":12}},"newText":"rand.Intn"}}]}`
	items, _ := complete(t, raw)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	got := items[0].Edit
	if got == nil {
		t.Fatal("textEdit was not decoded")
	}
	want := TextEdit{
		Range:   Range{Start: Position{Line: 3, Character: 8}, End: Position{Line: 3, Character: 12}},
		NewText: "rand.Intn",
	}
	if *got != want {
		t.Errorf("edit = %+v, want %+v", *got, want)
	}
}

// additionalTextEdits are the other half of an import completion: the import
// line has to land in the same action as the word.
func TestAdditionalTextEditsDecode(t *testing.T) {
	raw := `{"items":[{"label":"rand","additionalTextEdits":[
		{"range":{"start":{"line":2,"character":0},"end":{"line":2,"character":0}},"newText":"\t\"math/rand\"\n"}]}]}`
	items, _ := complete(t, raw)
	add := items[0].Additional
	if len(add) != 1 {
		t.Fatalf("additional edits = %v, want one", add)
	}
	if add[0].NewText != "\t\"math/rand\"\n" {
		t.Errorf("newText = %q", add[0].NewText)
	}
	if add[0].Range.Start.Line != 2 || add[0].Range.Start.Character != 0 ||
		add[0].Range.End.Line != 2 || add[0].Range.End.Character != 0 {
		t.Errorf("range = %+v, want an insertion at 2:0", add[0].Range)
	}
}

// The richer InsertReplaceEdit shape names both the span an insertion would
// extend and the span the whole word occupies. raj does not advertise
// insertReplaceSupport, but a server may send it anyway, and replace is the
// span acceptance has to overwrite.
func TestInsertReplaceEditPrefersReplace(t *testing.T) {
	raw := `{"items":[{"label":"Foo","textEdit":{"newText":"Foo","insert":{"start":{"line":1,"character":2},"end":{"line":1,"character":4}},"replace":{"start":{"line":1,"character":0},"end":{"line":1,"character":9}}}}]}`
	items, _ := complete(t, raw)
	got := items[0].Edit
	if got == nil {
		t.Fatal("insertReplace textEdit was not decoded")
	}
	if got.Range.Start.Character != 0 || got.Range.End.Character != 9 {
		t.Errorf("range = %+v, want the replace span", got.Range)
	}
	if got.NewText != "Foo" {
		t.Errorf("newText = %q, want Foo", got.NewText)
	}
}

// An item with no textEdit leaves Edit nil, so the accept path still types the
// word exactly as before.
func TestNoTextEditLeavesEditNil(t *testing.T) {
	items, _ := complete(t, `{"items":[{"label":"Foo","insertText":"Foo"}]}`)
	if items[0].Edit != nil {
		t.Errorf("edit = %+v, want nil", items[0].Edit)
	}
	if items[0].Additional != nil {
		t.Errorf("additional = %+v, want nil", items[0].Additional)
	}
}

// Documentation is legal as a plain string or as a MarkupContent object, and
// servers use both. Only the value is kept; the rendering decision is the
// popup's.
func TestDocumentationDecodesBothShapes(t *testing.T) {
	raw := `{"items":[
		{"label":"A","documentation":"plain docs"},
		{"label":"B","documentation":{"kind":"markdown","value":"**markdown** docs"}}]}`
	items, _ := complete(t, raw)
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Documentation != "plain docs" {
		t.Errorf("string form = %q", items[0].Documentation)
	}
	if items[1].Documentation != "**markdown** docs" {
		t.Errorf("MarkupContent form = %q", items[1].Documentation)
	}
}

// Completion documentation is rendered as markdown by the popup, so a fence is
// kept rather than flattened the way hoverText does it. Stripping it here would
// leave the popup with prose where the server sent code.
func TestDocumentationKeepsMarkdown(t *testing.T) {
	raw := "{\"items\":[{\"label\":\"F\",\"documentation\":{\"kind\":\"markdown\",\"value\":\"Docs.\\n\\n```go\\nfunc F()\\n```\\n\"}}]}"
	items, _ := complete(t, raw)
	if !strings.Contains(items[0].Documentation, "```go") {
		t.Errorf("documentation = %q; the fence was flattened", items[0].Documentation)
	}
}

// An item with no data is already complete and must never be sent to resolve;
// one with data carries the opaque handle verbatim so the server can finish it.
func TestResolveKeyOnlyWithData(t *testing.T) {
	items, _ := complete(t, `{"items":[
		{"label":"NoData","detail":"d"},
		{"label":"HasData","data":{"import":"x"}}]}`)
	if got := items[0].ResolveKey(); got != "" {
		t.Errorf("an item with no data has a resolve key: %q", got)
	}
	key := items[1].ResolveKey()
	if !strings.Contains(key, `"import":"x"`) {
		t.Errorf("resolve key = %q, want the item verbatim", key)
	}
}

// resolveSupport lets a server defer documentation, so the client has to be
// able to ask for it. The request body is the item as it arrived — the server's
// own data included — and the answer is decoded like any other item.
func TestResolveCompletionFillsDocumentation(t *testing.T) {
	f := newFake(t)
	f.on("completionItem/resolve", func(*Message) (any, *ResponseError) {
		return json.RawMessage(`{"label":"Foo","documentation":{"kind":"markdown","value":"Does the thing."}}`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()

	item := CompletionItem{Label: "Foo", Data: json.RawMessage(`{"import":"x"}`)}
	got, err := ResolveCompletion(ctx, f.conn, json.RawMessage(item.ResolveKey()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Documentation != "Does the thing." {
		t.Errorf("documentation = %q", got.Documentation)
	}
	sent := f.params["completionItem/resolve"]
	if len(sent) != 1 || !strings.Contains(string(sent[0]), `"import":"x"`) {
		t.Errorf("resolve params = %s, want the item with its data", sent)
	}
}

// A resolve that answers nothing is a nil item, not an error: a server may
// decline to fill in an item it did not intend to defer.
func TestResolveCompletionNullIsNoItem(t *testing.T) {
	f := newFake(t)
	f.on("completionItem/resolve", func(*Message) (any, *ResponseError) {
		return nil, nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	got, err := ResolveCompletion(ctx, f.conn, json.RawMessage(`{"label":"Foo","data":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "" {
		t.Errorf("item = %+v, want the zero value", got)
	}
}

// labelDetails is the 3.17 way to put a short type and a container beside the
// label; the popup has one place to show detail, so both join it.
func TestLabelDetailsJoinDetail(t *testing.T) {
	raw := `{"items":[{"label":"F","detail":"func()","labelDetails":{"detail":"error","description":"pkg"}}]}`
	items, _ := complete(t, raw)
	if got := items[0].Detail; got != "func() error pkg" {
		t.Errorf("detail = %q, want the joined detail", got)
	}
}

// itemDefaults hoists the fields every item shares out of the list. The three
// this client reads are the edit range, the insert format and the resolve data;
// leaving them unapplied would lose the edit range and re-type a snippet.
func TestItemDefaultsApply(t *testing.T) {
	raw := `{
		"itemDefaults":{
			"editRange":{"start":{"line":1,"character":0},"end":{"line":1,"character":3}},
			"insertTextFormat":2,
			"data":{"import":"x"}},
		"items":[{"label":"Foo","insertText":"Foo(${1:x})"}]}`
	items, _ := complete(t, raw)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Insert != "Foo" {
		t.Errorf("insert = %q, want the label for a defaulted snippet", it.Insert)
	}
	if it.Edit == nil {
		t.Fatal("the default editRange was not applied")
	}
	if it.Edit.Range.Start.Line != 1 || it.Edit.Range.End.Character != 3 {
		t.Errorf("edit range = %+v", it.Edit.Range)
	}
	if it.Edit.NewText != "Foo" {
		t.Errorf("newText = %q, want the refused-snippet insert text", it.Edit.NewText)
	}
	if it.ResolveKey() == "" {
		t.Error("the default data was not applied")
	}
}

// A default editRange may be the insert/replace pair, and replace is the span
// accepting has to overwrite.
func TestItemDefaultsEditRangePrefersReplace(t *testing.T) {
	raw := `{"itemDefaults":{"editRange":{"insert":{"start":{"line":2,"character":4},"end":{"line":2,"character":6}},"replace":{"start":{"line":2,"character":0},"end":{"line":2,"character":9}}}},"items":[{"label":"Bar"}]}`
	items, _ := complete(t, raw)
	if items[0].Edit == nil {
		t.Fatal("the default editRange was not applied")
	}
	if got := items[0].Edit.Range; got.Start.Character != 0 || got.End.Character != 9 {
		t.Errorf("range = %+v, want the replace span", got)
	}
}
