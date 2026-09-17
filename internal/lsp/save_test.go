package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// The will-save wait request carries the document and the manual reason, and
// decodes the server TextEdit list. Without the params a server cannot tell
// which document is being saved; without the decode the edits never land.
func TestWillSaveWaitUntilSendsTheDocumentAndReason(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/willSaveWaitUntil", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"// x\n"}]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	edits, err := RequestWillSaveWaitUntil(ctx, f.conn, "/w/a.go", SaveManual)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	if got["reason"] != float64(SaveManual) {
		t.Errorf("reason = %v, want %d", got["reason"], SaveManual)
	}
	if len(edits) != 1 || edits[0].NewText != "// x\n" {
		t.Fatalf("edits = %+v", edits)
	}
	if edits[0].Range.Start != (Position{Line: 0, Character: 0}) ||
		edits[0].Range.End != (Position{Line: 0, Character: 3}) {
		t.Errorf("range = %+v", edits[0].Range)
	}
}

// A null, empty or unreadable answer is no edits, not an error: a server with
// nothing to say must leave the save to proceed unformatted. Without the
// shared decode a malformed answer would either panic or be applied as
// nonsense offsets.
func TestWillSaveWaitUntilEmptyOrMalformedIsNoEdits(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `"nonsense"`, `[{"newText":42}]`} {
		f := newFake(t)
		f.on("textDocument/willSaveWaitUntil", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		edits, err := RequestWillSaveWaitUntil(ctx, f.conn, "/w/a.go", SaveManual)
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if len(edits) != 0 {
			t.Errorf("%s produced %d edits", raw, len(edits))
		}
	}
}

// The notification half carries the same document and reason. Without it a
// server that advertised willSave but not willSaveWaitUntil never hears that a
// save is happening.
func TestWillSaveNotificationCarriesTheDocumentAndReason(t *testing.T) {
	f := newFake(t)
	if err := NotifyWillSave(f.conn, "/w/a.go", SaveManual); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.notes("textDocument/willSave")) >= 1 })
	got := f.notes("textDocument/willSave")[0]
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
	if got["reason"] != float64(SaveManual) {
		t.Errorf("reason = %v, want %d", got["reason"], SaveManual)
	}
}

// Both flags live inside textDocumentSync, which may be a bare change-kind
// number with no options at all. Reading them from the top level, or treating
// a bare number as supporting them, would send a request a server never asked
// for.
func TestWillSaveCapabilitiesComeFromTextDocumentSyncOptions(t *testing.T) {
	cases := []struct {
		name      string
		sync      string
		willSave  bool
		waitUntil bool
	}{
		{"bare change kind", `2`, false, false},
		{"absent", ``, false, false},
		{"null", `null`, false, false},
		{"notify only", `{"willSave":true}`, true, false},
		{"wait only", `{"willSaveWaitUntil":true}`, false, true},
		{"both", `{"willSave":true,"willSaveWaitUntil":true}`, true, true},
		{"both false", `{"willSave":false,"willSaveWaitUntil":false}`, false, false},
		{"object without the flags", `{"change":2}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps := ServerCapabilities{TextDocumentSync: json.RawMessage(c.sync)}
			if got := caps.WillSave(); got != c.willSave {
				t.Errorf("WillSave() = %v, want %v", got, c.willSave)
			}
			if got := caps.WillSaveWaitUntil(); got != c.waitUntil {
				t.Errorf("WillSaveWaitUntil() = %v, want %v", got, c.waitUntil)
			}
		})
	}
}

// A caller with no connection gets ErrClosed, not a nil-pointer panic, from
// both forms.
func TestWillSaveWithoutAConnection(t *testing.T) {
	if err := NotifyWillSave(nil, "/w/a.go", SaveManual); err != ErrClosed {
		t.Errorf("notify err = %v, want ErrClosed", err)
	}
	if _, err := RequestWillSaveWaitUntil(context.Background(), nil, "/w/a.go", SaveManual); err != ErrClosed {
		t.Errorf("request err = %v, want ErrClosed", err)
	}
}
