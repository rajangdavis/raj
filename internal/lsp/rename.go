package lsp

import (
	"context"
	"encoding/json"
	"sort"
)

// Rename. textDocument/prepareRename asks whether a place can be renamed and
// what span the rename covers; textDocument/rename asks for the edits that
// carry a new name through every file the symbol appears in.
//
// The result of rename is a WorkspaceEdit, which names documents by URI — some
// of which are not open in the editor. Decoding therefore keeps the per-file
// grouping the server sent rather than flattening it into one edit list: where
// an edit applies is as much a part of the answer as the text it inserts, and a
// caller that cannot reach a document has to refuse the whole edit rather than
// silently apply the rest.

// RenameTarget is a prepareRename answer: the span the rename would replace,
// the name the server suggests for it, or the server's own "use the word under
// the cursor" marker.
//
// It is not a refusal. A server that refuses the position answers null (or an
// error), which decodes to a nil target with no error — the distinction
// between "you cannot rename this" and "rename this word" is the whole reason
// the prepare request exists.
type RenameTarget struct {
	// Range is the span the rename applies to. It is meaningful unless Default
	// is set.
	Range Range
	// Placeholder is the text the server suggests prefilling the new-name field
	// with; servers usually send the current name. Empty is fine — the caller
	// falls back to the buffer's word at the position.
	Placeholder string
	// Default is the server deferring to the client's own "word under the
	// cursor" rule (the protocol's defaultBehavior). Range and Placeholder are
	// then empty and the caller picks the span.
	Default bool
}

// WorkspaceEdit is a rename's edits grouped by the document they apply to.
//
// Docs is a slice rather than a map so the order the server sent is preserved:
// a map would make the order of application an accident of hashing, and while
// the edits in different documents do not overlap, a stable order is what makes
// a failure reproducible.
type WorkspaceEdit struct {
	Docs []DocumentEdits
	// ResourceOps names file operations the edit asked for — create, rename,
	// delete — that a text-only application cannot perform. Any entry means
	// the whole edit is unapplicable: applying the text edits and ignoring the
	// file operation would leave the workspace half-changed.
	ResourceOps []string
}

// DocumentEdits is one document's share of a WorkspaceEdit.
type DocumentEdits struct {
	// Path is the absolute path the server's URI named.
	Path  string
	Edits []TextEdit
}

// RequestPrepareRename asks whether the symbol at a position can be renamed
// and what span the rename covers.
//
// A nil result with a nil error is a refusal: the server answered null, meaning
// there is nothing renameable at that position. An error is a refusal too, and
// often carries the server's own explanation, which is more specific than
// anything the client could invent.
func RequestPrepareRename(ctx context.Context, c *Conn, path string, p Position) (*RenameTarget, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/prepareRename", positionParams(path, p), &raw); err != nil {
		return nil, err
	}
	return decodeRenameTarget(raw), nil
}

// decodeRenameTarget reads the three legal prepareRename result shapes: a bare
// range, a range with a placeholder, or the protocol's defaultBehavior marker.
// Anything else — null, an empty object, nonsense — is a refusal.
//
// The bare range is what makes this a probe rather than a plain struct: the two
// object shapes disagree about where the range lives (at the top level, or under
// a "range" key), so both are decoded from one pass and whichever is present
// wins.
func decodeRenameTarget(raw json.RawMessage) *RenameTarget {
	if isNull(raw) {
		return nil
	}
	var shape struct {
		Start           *Position `json:"start"`
		End             *Position `json:"end"`
		Range           *Range    `json:"range"`
		Placeholder     string    `json:"placeholder"`
		DefaultBehavior *bool     `json:"defaultBehavior"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return nil
	}
	switch {
	case shape.Start != nil && shape.End != nil:
		return &RenameTarget{Range: Range{Start: *shape.Start, End: *shape.End}}
	case shape.Range != nil:
		return &RenameTarget{Range: *shape.Range, Placeholder: shape.Placeholder}
	case shape.DefaultBehavior != nil && *shape.DefaultBehavior:
		return &RenameTarget{Default: true}
	}
	return nil
}

// RequestRename asks for the edits that rename the symbol at a position.
//
// A nil result with a nil error means the server returned no workspace edit
// (null): there was nothing to rename, or the position the request named no
// longer held a symbol. It is not an error, and the caller reports it as "no
// changes" rather than as a failure.
func RequestRename(ctx context.Context, c *Conn, path string, p Position, newName string) (*WorkspaceEdit, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := positionParams(path, p)
	params["newName"] = newName
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/rename", params, &raw); err != nil {
		return nil, err
	}
	return decodeWorkspaceEdit(raw), nil
}

// decodeWorkspaceEdit reads a WorkspaceEdit in both of the shapes servers send:
// a `changes` map keyed by URI, and a `documentChanges` array of
// TextDocumentEdit or file-operation entries. Servers use one or the other and
// some send both, so both are read and the two are merged by path.
//
// A file operation is recorded, not applied: a text-only editor cannot create,
// rename or delete a file through the same path, and quietly dropping the
// operation while applying the text would be a partial rename.
func decodeWorkspaceEdit(raw json.RawMessage) *WorkspaceEdit {
	if isNull(raw) {
		return nil
	}
	var shape struct {
		Changes map[string][]TextEdit `json:"changes"`
		// documentChanges is a union the specification leaves open; each entry
		// is decoded loosely and classified by whether it carries an `edits`
		// array and a textDocument, or a `kind` for a file operation.
		DocumentChanges []json.RawMessage `json:"documentChanges"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return nil
	}

	out := &WorkspaceEdit{}
	index := map[string]int{}
	add := func(path string, edits []TextEdit) {
		if path == "" || len(edits) == 0 {
			return
		}
		if i, ok := index[path]; ok {
			out.Docs[i].Edits = append(out.Docs[i].Edits, edits...)
			return
		}
		index[path] = len(out.Docs)
		out.Docs = append(out.Docs, DocumentEdits{Path: path, Edits: edits})
	}

	// A map has no wire order, so the keys are sorted to make the result
	// deterministic: two decodes of the same answer must produce the same
	// application order, or a partial failure is not reproducible.
	uris := make([]string, 0, len(shape.Changes))
	for uri := range shape.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		add(Path(uri), shape.Changes[uri])
	}

	for _, raw := range shape.DocumentChanges {
		var op struct {
			Kind         string `json:"kind"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []TextEdit `json:"edits"`
		}
		if json.Unmarshal(raw, &op) != nil {
			continue
		}
		if op.Kind != "" {
			out.ResourceOps = append(out.ResourceOps, op.Kind)
			continue
		}
		add(Path(op.TextDocument.URI), op.Edits)
	}

	if len(out.Docs) == 0 && len(out.ResourceOps) == 0 {
		return nil
	}
	return out
}
