package lsp

import (
	"context"
	"encoding/json"
)

// Code lenses. A lens is a small, actionable annotation a server attaches to a
// range — "3 references", "Run test" — carrying either a command to run or an
// opaque token for a codeLens/resolve round-trip. The range names where the
// lens applies; the client decides where to draw it, and raj draws it inline at
// the start of the range's first line.

// CodeLens is one lens. Which of Command and Data is set decides how it runs:
// an inline command runs directly, a data-only lens needs a resolve first, and
// NeedsResolve names that case so the caller can act on it rather than run
// nothing.
type CodeLens struct {
	Range   Range
	Command *Command
	// Data is the server's opaque payload for a later codeLens/resolve. It is
	// kept raw so a resolve sends it back byte for byte, and a lens this client
	// cannot run is still named rather than dropped.
	Data json.RawMessage
}

// NeedsResolve reports whether the lens carries no command of its own and must
// be resolved before it can be run. A lens with neither a command nor data is
// also reported as needing resolve, so the caller refuses it by name rather
// than running nothing.
func (l CodeLens) NeedsResolve() bool { return l.Command == nil }

// CodeLensResolve reports whether the server advertised the resolve half of
// code lenses. The provider is an options object or a boolean, so it stays raw
// and only the one option a caller needs is read here; a server that advertised
// a bare true asked for no resolves and a resolve request to it is a
// method-not-found rather than a feature.
func (c ServerCapabilities) CodeLensResolve() bool {
	if !Supports(c.CodeLensProvider) {
		return false
	}
	var opts struct {
		ResolveProvider bool `json:"resolveProvider"`
	}
	if json.Unmarshal(c.CodeLensProvider, &opts) != nil {
		return false
	}
	return opts.ResolveProvider
}

// wireCodeLens is the wire shape. command is a Command object or absent; data
// is opaque and kept raw.
type wireCodeLens struct {
	Range   Range           `json:"range"`
	Command json.RawMessage `json:"command"`
	Data    json.RawMessage `json:"data"`
}

// RequestCodeLenses asks for every lens in a document.
//
// The request names the document and nothing else: the protocol has no range
// parameter, so a server answers for the whole file and the client decides what
// to draw. The result is an array or null; null and an empty array both mean
// "no lenses", the normal answer for most documents.
func RequestCodeLenses(ctx context.Context, c *Conn, path string) ([]CodeLens, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{"textDocument": map[string]any{"uri": URI(path)}}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/codeLens", params, &raw); err != nil {
		return nil, err
	}
	return decodeCodeLenses(raw), nil
}

// ResolveCodeLens fills in a lens's command through codeLens/resolve.
//
// The lens is sent back with its data, which is the server's token for this
// lens; a server that answers with a command makes the lens runnable. A reply
// that carries no command is returned as-is rather than turned into an error,
// so the caller refuses it by name; a malformed reply leaves the input lens
// unchanged, because a failed resolve is not a reason to forget what the
// request was about.
func ResolveCodeLens(ctx context.Context, c *Conn, lens CodeLens) (CodeLens, error) {
	if c == nil {
		return CodeLens{}, ErrClosed
	}
	params := map[string]any{"range": lens.Range}
	if len(lens.Data) > 0 {
		params["data"] = lens.Data
	}
	if lens.Command != nil {
		params["command"] = lens.Command
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "codeLens/resolve", params, &raw); err != nil {
		return CodeLens{}, err
	}
	out, ok := decodeCodeLens(raw)
	if !ok {
		return lens, nil
	}
	return out, nil
}

// decodeCodeLenses reads the result, an array or null. A lens with no command
// is not dropped: a CodeLens has no title of its own — the command carries it —
// so an entry that can only be resolved later must survive decoding to be named
// at all.
func decodeCodeLenses(raw json.RawMessage) []CodeLens {
	if isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]CodeLens, 0, len(items))
	for _, it := range items {
		if lens, ok := decodeCodeLens(it); ok {
			out = append(out, lens)
		}
	}
	return out
}

// decodeCodeLens reads one wire lens. A malformed entry is dropped rather than
// failing the whole answer, the same permissiveness the code-action decoder
// applies: one bad entry must not hide the lenses beside it.
func decodeCodeLens(raw json.RawMessage) (CodeLens, bool) {
	if isNull(raw) {
		return CodeLens{}, false
	}
	var w wireCodeLens
	if json.Unmarshal(raw, &w) != nil {
		return CodeLens{}, false
	}
	return CodeLens{Range: w.Range, Command: decodeCommand(w.Command, "", nil), Data: w.Data}, true
}
