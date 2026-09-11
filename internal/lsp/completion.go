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
	// Edit, when the server sent a textEdit, is its own replacement for this
	// candidate; Additional are the further edits — an import line, usually —
	// that must land with it. Both are in server coordinates and are converted
	// to buffer offsets where the candidate is built for acceptance.
	Edit       *TextEdit
	Additional []TextEdit
	// sortText and filterText are the server's own ordering and matching keys.
	// They exist because a server ranks by things the client cannot see —
	// scope, type compatibility, usage — and encodes the result in a string
	// that sorts correctly. Ignoring them throws away the entire value of
	// asking a language server rather than scanning the buffer.
	sortText   string
	filterText string
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
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
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
		var arr []completionItem
		if json.Unmarshal(raw, &arr) != nil {
			return nil, false, nil
		}
		res = completionResponse{Items: arr}
	}

	out := make([]CompletionItem, 0, len(res.Items))
	for _, it := range res.Items {
		if it.Label == "" {
			continue
		}
		out = append(out, CompletionItem{
			Label:      strings.TrimSpace(it.Label),
			Insert:     insertTextOf(it),
			Detail:     strings.TrimSpace(it.Detail),
			Kind:       it.Kind,
			Edit:       decodeTextEdit(it.TextEdit),
			Additional: it.AdditionalTextEdits,
			sortText:   it.SortText,
			filterText: it.FilterText,
		})
	}
	return out, res.IsIncomplete, nil
}

// insertTextOf decides what typing a candidate should produce.
//
// Snippets are refused and the label used instead. A snippet is a template with
// tab stops and placeholders (`fmt.Errorf(${1:format})`), and raj has no
// snippet engine — inserting one literally would type the placeholder syntax
// into the buffer, which is worse than inserting a plain identifier. raj also
// advertises snippetSupport: false, so a server sending one anyway is not
// being obliged.
//
// A textEdit is not read here. It is parsed separately into Edit, and honouring
// it means applying a server-computed range replacement rather than typing a
// word, which is a different operation from the one the popup performs. This
// function still decides the plain word used for display and filtering, and for
// the accept path when no textEdit was sent.
func insertTextOf(it completionItem) string {
	if it.InsertTextFormat == 2 { // snippet
		return it.Label
	}
	if it.InsertText != "" {
		return it.InsertText
	}
	return it.Label
}

// decodeTextEdit parses a completion item textEdit, which comes in two shapes.
// The plain TextEdit names one range; InsertReplaceEdit names both the span an
// insertion would extend and the span the completed word replaces. The replace
// span covers the whole word, which is what accepting should overwrite, so it
// is preferred. raj does not advertise insertReplaceSupport, but a server is
// free to send the richer shape anyway.
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
