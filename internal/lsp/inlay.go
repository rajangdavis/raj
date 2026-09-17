package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

// Inlay hints. A hint is text the server wants shown inline in the document
// without being part of it — a parameter name before an argument, an inferred
// type after a declaration. The request is range-scoped because a client is
// expected to ask only about what it is about to draw.

// InlayHint is one hint, flattened to the plain text a cell grid can show.
//
// Label and tooltip are the two fields the protocol gives several shapes to,
// so both arrive here already reduced to strings. Kind is carried raw: 1 is a
// type and 2 a parameter, and which of those to show is a display decision
// rather than a protocol one. Edits are the server's own corrections to the
// document, kept for the apply path; with resolveSupport unadvertised a server
// sends them with the hint or not at all.
type InlayHint struct {
	Pos          Position
	Text         string
	Kind         int
	PaddingLeft  bool
	PaddingRight bool
	Tooltip      string
	Edits        []TextEdit
}

// inlayHint is the wire shape. label and tooltip have more than one legal form
// — a string, an array of parts, a MarkupContent — so both are kept raw and
// interpreted afterwards. TextEdit carries Range and NewText with no JSON tags,
// which the decoder matches case-insensitively against range and newText.
type inlayHint struct {
	Position     Position        `json:"position"`
	Label        json.RawMessage `json:"label"`
	Kind         int             `json:"kind"`
	PaddingLeft  bool            `json:"paddingLeft"`
	PaddingRight bool            `json:"paddingRight"`
	Tooltip      json.RawMessage `json:"tooltip"`
	// TextEdits is kept raw so one malformed edit cannot cost the hint its
	// label and tooltip; the edits are decoded permissively afterwards.
	TextEdits json.RawMessage `json:"textEdits"`
}

// labelText flattens an inlay hint label. It is either a plain string or an
// array of parts, and only each part's value contributes: a part's location
// and command are actions a terminal has no way to offer, and joining their
// text into the flow would show something that is not the label.
func labelText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Value)
	}
	return b.String()
}

// tooltipText flattens an inlay hint tooltip, a string or a MarkupContent, the
// same way hover contents are flattened: markdown fences are stripped rather
// than rendered, because the terminal has no rich text and literal backticks
// are worse than the signature they were wrapping.
func tooltipText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanMarkup(s)
	}
	var obj struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Value != "" {
		return cleanMarkup(obj.Value)
	}
	return ""
}

// textEdits decodes a hint's attached edits permissively. resolveSupport is
// unadvertised, so a server sends the edits with the hint or not at all; a
// malformed list or a malformed element must not cost the hint its label and
// tooltip, so a bad element is dropped and the good ones are kept.
func textEdits(raw json.RawMessage) []TextEdit {
	if isNull(raw) {
		return nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	out := make([]TextEdit, 0, len(parts))
	for _, p := range parts {
		var e TextEdit
		if json.Unmarshal(p, &e) != nil {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RequestInlayHints asks for the hints in a range of a document.
//
// The range is mandatory in the protocol and is how a client asks only about
// what it is drawing. The result is an array or null; null and an empty array
// both mean "no hints here", which is the normal answer for most ranges rather
// than a failure.
func RequestInlayHints(ctx context.Context, c *Conn, path string, r Range) ([]InlayHint, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	params := map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"range":        r,
	}
	if err := c.Call(ctx, "textDocument/inlayHint", params, &raw); err != nil {
		return nil, err
	}
	if isNull(raw) {
		return nil, nil
	}
	// Decoded element by element: one malformed hint must not cost the whole
	// answer, and the edits are decoded separately so a malformed edit is
	// dropped rather than failing the hint it hung off.
	var wire []json.RawMessage
	if json.Unmarshal(raw, &wire) != nil {
		return nil, nil
	}
	out := make([]InlayHint, 0, len(wire))
	for _, el := range wire {
		var w inlayHint
		if json.Unmarshal(el, &w) != nil {
			continue
		}
		out = append(out, InlayHint{
			Pos:          w.Position,
			Text:         labelText(w.Label),
			Kind:         w.Kind,
			PaddingLeft:  w.PaddingLeft,
			PaddingRight: w.PaddingRight,
			Tooltip:      tooltipText(w.Tooltip),
			Edits:        textEdits(w.TextEdits),
		})
	}
	return out, nil
}
