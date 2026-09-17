package lsp

import (
	"context"
	"encoding/json"
)

// Save-time synchronisation: textDocument/willSave and
// textDocument/willSaveWaitUntil.
//
// The protocol has two forms of the same event. willSave is a notification:
// "a save is about to happen", no answer wanted. willSaveWaitUntil is a
// request: the server returns TextEdits to apply to the document *before* the
// bytes are written, which is the only protocol shape that can format on save
// without a second gesture. They are separate server capabilities, so a client
// asks exactly the one the server said it was interested in.

// SaveReason is the protocol's TextDocumentSaveReason: why a document is being
// saved. Only Manual is sent today — every save reachable from the editor is a
// user's cmd+s — but the other two are named so a later autosave is not a new
// constant spelled at a call site.
type SaveReason int

const (
	// SaveManual is a save the user asked for.
	SaveManual SaveReason = 1
	// SaveAfterDelay is an autosave on a timer.
	SaveAfterDelay SaveReason = 2
	// SaveFocusOut is a save when the editor loses focus.
	SaveFocusOut SaveReason = 3
)

// willSaveParams is the shared shape of both save forms: the document and the
// reason. Neither carries formatting options — the server decides what a save
// means, and the answer, not the request, is where formatting lives.
func willSaveParams(path string, reason SaveReason) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"reason":       int(reason),
	}
}

// NotifyWillSave sends textDocument/willSave: the document is about to be
// saved, no answer is wanted.
func NotifyWillSave(c *Conn, path string, reason SaveReason) error {
	if c == nil {
		return ErrClosed
	}
	return c.Notify("textDocument/willSave", willSaveParams(path, reason))
}

// RequestWillSaveWaitUntil asks for the edits to apply before the save. The
// caller bounds the wait with ctx: the protocol expects a client to drop a slow
// answer so the save stays fast, and a server that hangs must not hold a save.
//
// A null, empty or unreadable answer decodes to no edits, exactly as
// formatting does: the save proceeds unformatted rather than being reported as
// failed, because a server with nothing to say is not an error.
func RequestWillSaveWaitUntil(ctx context.Context, c *Conn, path string, reason SaveReason) ([]TextEdit, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/willSaveWaitUntil", willSaveParams(path, reason), &raw); err != nil {
		return nil, err
	}
	return decodeTextEdits(raw), nil
}
