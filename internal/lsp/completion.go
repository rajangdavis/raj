package lsp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// Completion. The response is the largest and least consistent thing servers
// send: gopls returns a few hundred items with sort keys and filter text that
// differ from the label, tsserver returns items whose insert text is a snippet
// template, and both mark the list incomplete when they have more.

// TextEdit is a range replacement as a server reports it. Positions are LSP
// coordinates, converted to buffer offsets at the point the candidate is built
// for acceptance.
type TextEdit struct {
	Range   Range
	NewText string
}

// CompletionItem is one suggestion.
type CompletionItem struct {
	Label  string
	Insert string // what to type; the label when the server gives nothing else
	Detail string // a type or a signature, shown beside the label
	Kind   int
	// Documentation is the server's documentation for the item, as it sent it.
	// It is markdown by construction and is rendered, not flattened, by the
	// popup; it is empty when the server defers it to completionItem/resolve.
	Documentation string
	// Data is the opaque state a server attaches to an item so that a later
	// completionItem/resolve can finish it. An item with no data cannot be
	// resolved. It is echoed back unchanged.
	Data json.RawMessage
	// Edit, when the server sent a textEdit, is its own replacement for this
	// candidate; Additional are the further edits — an import line, usually —
	// that must land with it. Both are in server coordinates and are converted
	// to buffer offsets where the candidate is built for acceptance.
	Edit       *TextEdit
	Additional []TextEdit
	// Snippet, when non-empty, is the snippet template for this item: the
	// insert text with `$1`/`${1:default}` stops, unexpanded. Insert stays the
	// label so the popup and the filter key are unchanged; the completion
	// engine reads Snippet and inserts the template's literal text instead of
	// the label. It is empty for an item the server sent as plain text.
	Snippet string
	// InsertTextFormat is the server's format for the insert: 1 for plain text
	// and 2 for a snippet. It is kept so the app can tell the two apart after
	// this package has flattened the item.
	InsertTextFormat int
	// sortText and filterText are the server's own ordering and matching keys.
	// They exist because a server ranks by things the client cannot see —
	// scope, type compatibility, usage — and encodes the result in a string
	// that sorts correctly. Ignoring them throws away the entire value of
	// asking a language server rather than scanning the buffer.
	sortText   string
	filterText string
	// raw is the item exactly as it arrived. A resolve request echoes it back
	// rather than reconstructing a subset, because a server may key on a field
	// this client does not decode.
	raw json.RawMessage
}

// ResolveKey is the request body a deferred completionItem/resolve would send,
// or "" when the item has no data and so cannot be resolved. It is opaque: the
// client only carries it and hands it back. When the item was decoded from a
// response this is the wire item verbatim; an item built in code falls back to
// the fields a server needs to recognise it.
func (c CompletionItem) ResolveKey() string {
	if isNull(c.Data) {
		return ""
	}
	if len(c.raw) > 0 {
		return string(c.raw)
	}
	b, err := json.Marshal(completionItem{Label: c.Label, Kind: c.Kind, Detail: c.Detail, Data: c.Data})
	if err != nil {
		return ""
	}
	return string(b)
}

// CompletionKind values worth distinguishing. The protocol defines twenty-odd;
// these are the ones whose presence changes what a caller would show.
const (
	KindText     = 1
	KindMethod   = 2
	KindFunction = 3
	KindField    = 5
	KindVariable = 6
	KindKeyword  = 14
	KindSnippet  = 15
)

type completionResponse struct {
	IsIncomplete bool                `json:"isIncomplete"`
	Items        []json.RawMessage   `json:"items"`
	ItemDefaults *completionDefaults `json:"itemDefaults"`
}

// completionDefaults are the per-list defaults LSP 3.17 lets a server set once
// instead of on every item. Only the three raj consumes are decoded: the edit
// range, the insert text format, and the resolve data.
type completionDefaults struct {
	EditRange        json.RawMessage `json:"editRange"`
	InsertTextFormat int             `json:"insertTextFormat"`
	Data             json.RawMessage `json:"data"`
}

type completionItem struct {
	Label               string          `json:"label"`
	Kind                int             `json:"kind"`
	Detail              string          `json:"detail"`
	SortText            string          `json:"sortText"`
	FilterText          string          `json:"filterText"`
	InsertText          string          `json:"insertText"`
	InsertTextFormat    int             `json:"insertTextFormat"`
	TextEdit            json.RawMessage `json:"textEdit"`
	AdditionalTextEdits []TextEdit      `json:"additionalTextEdits"`
	Documentation       json.RawMessage `json:"documentation"`
	Data                json.RawMessage `json:"data"`
	LabelDetails        *labelDetails   `json:"labelDetails"`
}

// labelDetails is the LSP 3.17 object a server uses to put a short type and a
// container description beside the label without repeating the whole signature
// in it. Both are joined into the item's detail, which is the one place the
// popup already shows a small annotation.
type labelDetails struct {
	Detail      string `json:"detail"`
	Description string `json:"description"`
}

// Completions asks what could go at a position.
//
// isIncomplete means the server has more and wants to be asked again as the
// prefix grows. It is returned rather than acted on here: whether to re-ask is
// a policy question about keystrokes, which belongs where the keystrokes are.
func Completions(ctx context.Context, c *Conn, path string, p Position) (items []CompletionItem, incomplete bool, err error) {
	if c == nil {
		return nil, false, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/completion", positionParams(path, p), &raw); err != nil {
		return nil, false, err
	}
	if isNull(raw) {
		return nil, false, nil
	}

	var res completionResponse
	// The result is either a list object or a bare array, and servers differ.
	if json.Unmarshal(raw, &res) != nil || res.Items == nil {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return nil, false, nil
		}
		res = completionResponse{Items: arr}
	}

	out := make([]CompletionItem, 0, len(res.Items))
	for _, rawItem := range res.Items {
		var it completionItem
		if json.Unmarshal(rawItem, &it) != nil || it.Label == "" {
			continue
		}
		out = append(out, decodeItem(it, rawItem, res.ItemDefaults))
	}
	return out, res.IsIncomplete, nil
}

// insertTextOf decides the plain text a candidate carries: the word the popup
// shows, the key filtering matches, and the fallback for an item with no server
// edit attached.
//
// A snippet's label is used here rather than its template. Inserting
// `fmt.Errorf(${1:format})` literally would type the placeholder syntax into the
// buffer — but the template is not discarded: decodeItem carries it in Snippet
// and the app expands it through the completion engine. Deciding the plain word
// here keeps the popup and the filter key identical whether or not a snippet is
// involved.
//
// A textEdit is not read here either. It is parsed separately into Edit, and
// honouring it means applying a server-computed range replacement rather than
// typing a word, which is a different operation from the one the popup performs.
func insertTextOf(it completionItem) string {
	if it.InsertTextFormat == 2 { // snippet
		return it.Label
	}
	if it.InsertText != "" {
		return it.InsertText
	}
	return it.Label
}

// decodeItem turns one wire item into a CompletionItem, applying the list's
// itemDefaults to anything the item left unset. Defaults exist so a server can
// avoid repeating the same edit range, insert format and resolve data on every
// item; applying them here means the rest of the package sees items that are
// each complete on their own.
func decodeItem(it completionItem, raw json.RawMessage, def *completionDefaults) CompletionItem {
	if def != nil {
		if it.InsertTextFormat == 0 {
			it.InsertTextFormat = def.InsertTextFormat
		}
		if len(it.Data) == 0 {
			it.Data = def.Data
		}
	}
	realEdit := decodeTextEdit(it.TextEdit)
	edit := realEdit
	if edit == nil && def != nil {
		if r := decodeEditRange(def.EditRange); r != nil {
			edit = &TextEdit{Range: *r, NewText: insertTextOf(it)}
		}
	}
	// Snippet is the raw template the completion engine expands. The textEdit's
	// newText is the text the accept path applies, so it is preferred when the
	// server sent one; insertText and then the label are the fallbacks for a
	// server that only set the format. It stays empty for a plain item, which is
	// what tells the app not to start a snippet session.
	snippet := ""
	if it.InsertTextFormat == 2 {
		switch {
		case realEdit != nil && realEdit.NewText != "":
			snippet = realEdit.NewText
		case it.InsertText != "":
			snippet = it.InsertText
		default:
			snippet = it.Label
		}
	}
	return CompletionItem{
		Label:            strings.TrimSpace(it.Label),
		Insert:           insertTextOf(it),
		Detail:           itemDetail(it),
		Snippet:          snippet,
		InsertTextFormat: it.InsertTextFormat,
		Documentation:    documentationText(it.Documentation),
		Kind:             it.Kind,
		Data:             it.Data,
		Edit:             edit,
		Additional:       it.AdditionalTextEdits,
		sortText:         it.SortText,
		filterText:       it.FilterText,
		raw:              raw,
	}
}

// itemDetail is the text shown beside the label: the server's detail plus the
// LSP 3.17 labelDetails, which are the same kind of small annotation in two
// fields. Joining them keeps the popup's one place to show detail.
func itemDetail(it completionItem) string {
	detail := strings.TrimSpace(it.Detail)
	if it.LabelDetails == nil {
		return detail
	}
	for _, s := range []string{it.LabelDetails.Detail, it.LabelDetails.Description} {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if detail != "" {
			detail += " "
		}
		detail += s
	}
	return detail
}

// documentationText extracts a completion item's documentation, which the spec
// allows as a plain string or a MarkupContent object.
//
// Unlike hoverText this does not strip code fences or collapse blank lines:
// documentation is markdown, the popup renders it with the markdown subset, and
// a fence is the one thing that tells that renderer where code begins.
func documentationText(raw json.RawMessage) string {
	if len(raw) == 0 || isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return strings.TrimSpace(obj.Value)
	}
	return ""
}

// decodeEditRange parses a completion list's itemDefaults.editRange, which is
// either one range or the insert/replace pair. As with a textEdit, the replace
// span covers the whole word and is what accepting should overwrite.
func decodeEditRange(raw json.RawMessage) *Range {
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var shape struct {
		Range   *Range `json:"range"`
		Insert  *Range `json:"insert"`
		Replace *Range `json:"replace"`
	}
	if json.Unmarshal(raw, &shape) == nil {
		switch {
		case shape.Replace != nil:
			return shape.Replace
		case shape.Range != nil:
			return shape.Range
		case shape.Insert != nil:
			return shape.Insert
		}
	}
	var r struct {
		Start *Position `json:"start"`
		End   *Position `json:"end"`
	}
	if json.Unmarshal(raw, &r) == nil && r.Start != nil && r.End != nil {
		return &Range{Start: *r.Start, End: *r.End}
	}
	return nil
}

// ResolveCompletion asks the server to finish an item it sent with only a label
// and a resolve handle. A server defers documentation — and sometimes detail —
// to a second request so the completion list stays small; the item's data field
// is the opaque state that lets it answer. The request body is the item as it
// arrived, echoed back, so no field the server sent is lost.
func ResolveCompletion(ctx context.Context, c *Conn, item json.RawMessage) (CompletionItem, error) {
	if c == nil {
		return CompletionItem{}, ErrClosed
	}
	if len(item) == 0 {
		return CompletionItem{}, nil
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "completionItem/resolve", item, &raw); err != nil {
		return CompletionItem{}, err
	}
	if isNull(raw) {
		return CompletionItem{}, nil
	}
	var it completionItem
	if json.Unmarshal(raw, &it) != nil || it.Label == "" {
		return CompletionItem{}, nil
	}
	return decodeItem(it, raw, nil), nil
}

// decodeTextEdit parses a completion item textEdit, which comes in two shapes.
// The plain TextEdit names one range; InsertReplaceEdit names both the span an
// insertion would extend and the span the completed word replaces. The replace
// span covers the whole word, which is what accepting should overwrite, so it
// is preferred. raj advertises insertReplaceSupport, so a server may send this
// richer shape; the plain shape is still read for the servers that send it.
//
// A textEdit with no range at all, or nonsense, becomes nil: the candidate
// still works as a plain word.
func decodeTextEdit(raw json.RawMessage) *TextEdit {
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var shape struct {
		Range   *Range `json:"range"`
		Insert  *Range `json:"insert"`
		Replace *Range `json:"replace"`
		NewText string `json:"newText"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return nil
	}
	switch {
	case shape.Replace != nil:
		return &TextEdit{Range: *shape.Replace, NewText: shape.NewText}
	case shape.Range != nil:
		return &TextEdit{Range: *shape.Range, NewText: shape.NewText}
	case shape.Insert != nil:
		return &TextEdit{Range: *shape.Insert, NewText: shape.NewText}
	}
	return nil
}

// FilterKey is what a candidate should be matched against: the server's own
// filter text when it gives one, and the label otherwise.
//
// They differ more often than it looks. gopls labels a method `Foo(x int)` and
// filters on `Foo`, so matching the label would require typing the parenthesis
// to keep a match alive.
func (c CompletionItem) FilterKey() string {
	if c.filterText != "" {
		return c.filterText
	}
	return c.Label
}

// SortKey is the server's ordering key, which encodes ranking the client cannot
// reproduce — scope, type compatibility, how recently something was used.
func (c CompletionItem) SortKey() string {
	if c.sortText != "" {
		return c.sortText
	}
	return c.Label
}

// SortItems orders items the way the server intended, with the label as a
// tiebreak so the order is total: an unstable list reshuffles between identical
// keystrokes, which is worse than one in a debatable order.
func SortItems(items []CompletionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].SortKey(), items[j].SortKey()
		if a != b {
			return a < b
		}
		return items[i].Label < items[j].Label
	})
}

// FilterItems keeps the items matching prefix, in the server's order.
//
// A case-insensitive prefix test on the filter key, not a fuzzy match: the
// server has already decided what is relevant here, and re-ranking its answer
// with a client-side score would throw away the type information that made it
// worth asking. The prefix test only removes what the growing prefix has
// excluded since the request was sent.
func FilterItems(items []CompletionItem, prefix string) []CompletionItem {
	if prefix == "" {
		return items
	}
	lower := strings.ToLower(prefix)
	out := items[:0:0]
	for _, it := range items {
		if strings.HasPrefix(strings.ToLower(it.FilterKey()), lower) {
			out = append(out, it)
		}
	}
	return out
}
