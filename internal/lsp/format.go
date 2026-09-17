package lsp

import (
	"context"
	"encoding/json"
)

// Formatting. textDocument/formatting and textDocument/rangeFormatting share
// one shape: a FormattingOptions and a list of TextEdits back. The edits are
// positions in the version of the document the server was last told about,
// which for a synced buffer is the text in front of the user, unsaved edits
// included — so the caller applies them against that same version and drops
// them if the buffer has moved on.

// FormattingOptions is what both formatting requests take. It is a small typed
// shape rather than a map so a caller cannot spell a key wrong and send an
// option the server silently ignores. tabSize and insertSpaces are the two an
// editor can answer from the buffer's own indent style.
type FormattingOptions struct {
	TabSize      int
	InsertSpaces bool
}

// wireTextEdit is the protocol's plain TextEdit. A formatting result is always
// an array of these, so unlike a completion item's textEdit it has no
// insert/replace alternative to disambiguate; it is decoded into the TextEdit
// the rest of the package already uses so a formatting answer and a completion
// answer name the same thing.
type wireTextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// formattingParams is the shape both requests take, with an optional range.
func formattingParams(path string, opts FormattingOptions, r *Range) map[string]any {
	params := map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"options": map[string]any{
			"tabSize":      opts.TabSize,
			"insertSpaces": opts.InsertSpaces,
		},
	}
	if r != nil {
		params["range"] = *r
	}
	return params
}

// RequestFormatting asks the server to format the whole document.
//
// A nil edit list is "nothing to change", which is the normal answer for a file
// already formatted: it is not an error, and the caller applies the empty list
// as a no-op rather than telling the user the formatter failed.
func RequestFormatting(ctx context.Context, c *Conn, path string, opts FormattingOptions) ([]TextEdit, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/formatting", formattingParams(path, opts, nil), &raw); err != nil {
		return nil, err
	}
	return decodeTextEdits(raw), nil
}

// RequestRangeFormatting asks the server to format one range. The range is in
// the same UTF-16 coordinates as every other position this package sends, and
// the result is the same TextEdit list document formatting returns. The
// protocol takes a single range, so a caller with several selections chooses
// one rather than merging answers.
func RequestRangeFormatting(ctx context.Context, c *Conn, path string, r Range, opts FormattingOptions) ([]TextEdit, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/rangeFormatting", formattingParams(path, opts, &r), &raw); err != nil {
		return nil, err
	}
	return decodeTextEdits(raw), nil
}

// decodeTextEdits reads an edit list, or null. A malformed answer is an empty
// list rather than an error: the caller applies an empty list as a no-op, and
// a server that sent something unreadable is a server with nothing to say,
// which must not stop the editor.
func decodeTextEdits(raw json.RawMessage) []TextEdit {
	if isNull(raw) {
		return nil
	}
	var wire []wireTextEdit
	if json.Unmarshal(raw, &wire) != nil {
		return nil
	}
	out := make([]TextEdit, 0, len(wire))
	for _, w := range wire {
		out = append(out, TextEdit{Range: w.Range, NewText: w.NewText})
	}
	return out
}

// On-type formatting. A server that advertises documentOnTypeFormattingProvider
// names the characters that should make it reformat around the cursor — "}" in
// Go, "\n" in some other languages — and answers textDocument/onTypeFormatting
// with a TextEdit list the client applies on that keystroke.
//
// The spec baseline implemented here is 3.17: documentOnTypeFormattingProvider
// is a DocumentOnTypeFormattingOptions object whose required firstTriggerCharacter
// is a string and whose optional moreTriggerCharacter is an array — the singular
// spelling of the array field, which the 3.17 meta model and the 3.14 spec both
// carry. Some servers send the plural moreTriggerCharacters, so both are read
// rather than losing a server's triggers to a spelling it did not follow. The
// request params are the same in 3.14 and 3.17: the document, the position the
// formatting applies around, the typed character ch, and FormattingOptions.

// OnTypeFormattingTriggers is the trigger characters the server advertised for
// on-type formatting, in the order it named them: the required
// firstTriggerCharacter followed by moreTriggerCharacter. A provider that is
// absent, false, or carries no usable character returns nil, which is no
// trigger at all.
func (c ServerCapabilities) OnTypeFormattingTriggers() []string {
	if !Supports(c.DocumentOnTypeFormattingProvider) {
		return nil
	}
	var opts struct {
		First string   `json:"firstTriggerCharacter"`
		More  []string `json:"moreTriggerCharacter"`
		// The plural spelling is not in the 3.17 meta model, but servers emit
		// it; accepting it costs one field and losing a real trigger does not.
		Plural []string `json:"moreTriggerCharacters"`
	}
	if json.Unmarshal(c.DocumentOnTypeFormattingProvider, &opts) != nil {
		return nil
	}
	// firstTriggerCharacter is required by the 3.17 options shape. A provider
	// that omits it, or sends it empty, names no trigger at all: the
	// more-trigger array is a supplement, not a substitute, so an empty first
	// character means nil rather than firing on the array alone.
	if opts.First == "" {
		return nil
	}
	triggers := make([]string, 0, 1+len(opts.More)+len(opts.Plural))
	triggers = append(triggers, opts.First)
	for _, ch := range append(append([]string{}, opts.More...), opts.Plural...) {
		if ch != "" {
			triggers = append(triggers, ch)
		}
	}
	return triggers
}

// OnTypeTrigger reports whether text is one of the trigger characters, and
// which one it matched. Matching is exact: the trigger is the character the
// server named, and "\n" is a character. An empty text never matches, so a
// keystroke that inserted nothing cannot fire a request.
func OnTypeTrigger(triggers []string, text string) (string, bool) {
	if text == "" {
		return "", false
	}
	for _, ch := range triggers {
		if ch != "" && ch == text {
			return ch, true
		}
	}
	return "", false
}

// onTypeFormattingParams is the 3.17 request shape: the document, the position
// the formatting applies around, the character that was typed, and the same
// FormattingOptions the other two formatting requests carry.
func onTypeFormattingParams(path string, p Position, ch string, opts FormattingOptions) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"position":     p,
		"ch":           ch,
		"options": map[string]any{
			"tabSize":      opts.TabSize,
			"insertSpaces": opts.InsertSpaces,
		},
	}
}

// RequestOnTypeFormatting asks the server to format around a just-typed
// character.
//
// A nil edit list is "nothing to change", the normal answer for a character
// that needed no formatting; the caller applies it as a no-op rather than
// reporting a failure.
func RequestOnTypeFormatting(ctx context.Context, c *Conn, path string, p Position, ch string, opts FormattingOptions) ([]TextEdit, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/onTypeFormatting", onTypeFormattingParams(path, p, ch, opts), &raw); err != nil {
		return nil, err
	}
	return decodeTextEdits(raw), nil
}
