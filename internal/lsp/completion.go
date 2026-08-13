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

// CompletionItem is one suggestion.
type CompletionItem struct {
	Label  string
	Insert string // what to type; the label when the server gives nothing else
	Detail string // a type or a signature, shown beside the label
	Kind   int
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
	Label            string          `json:"label"`
	Kind             int             `json:"kind"`
	Detail           string          `json:"detail"`
	SortText         string          `json:"sortText"`
	FilterText       string          `json:"filterText"`
	InsertText       string          `json:"insertText"`
	InsertTextFormat int             `json:"insertTextFormat"`
	TextEdit         json.RawMessage `json:"textEdit"`
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
// A textEdit is likewise ignored. It carries its own range, which may extend
// beyond the prefix being completed, and honouring it correctly means applying
// a server-computed edit rather than typing a word. That is the right thing
// eventually and is a different operation from the one the popup performs.
func insertTextOf(it completionItem) string {
	if it.InsertTextFormat == 2 { // snippet
		return it.Label
	}
	if it.InsertText != "" {
		return it.InsertText
	}
	return it.Label
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
