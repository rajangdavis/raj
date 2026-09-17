package lsp

import (
	"context"
	"encoding/json"
	"testing"
)

// prepareRename has three legal result shapes — a bare range, a range with a
// placeholder, and the newer defaultBehavior marker — plus the null refusal.
// The client has to tell the refusal from the answer: asking for a new name and
// then failing the rename is the bug this request exists to prevent.
//
// The bare-range case is the one that fails without the probe decode: the
// server puts `start`/`end` at the top level, and a struct that only looks for
// `range` reads a perfectly good answer as a refusal.
func TestPrepareRenameShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(*testing.T, *RenameTarget)
	}{
		{
			name: "bare range",
			raw:  `{"start":{"line":1,"character":2},"end":{"line":1,"character":6}}`,
			check: func(t *testing.T, tg *RenameTarget) {
				if tg == nil {
					t.Fatal("a bare range decoded as a refusal")
				}
				if tg.Default || tg.Placeholder != "" {
					t.Errorf("target = %+v, want a plain range", tg)
				}
				if tg.Range.Start.Line != 1 || tg.Range.Start.Character != 2 ||
					tg.Range.End.Character != 6 {
					t.Errorf("range = %+v, want 1:2 to 1:6", tg.Range)
				}
			},
		},
		{
			name: "range and placeholder",
			raw:  `{"range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}},"placeholder":"Old"}`,
			check: func(t *testing.T, tg *RenameTarget) {
				if tg == nil {
					t.Fatal("a range with a placeholder decoded as a refusal")
				}
				if tg.Range.Start.Line != 3 || tg.Range.End.Character != 9 {
					t.Errorf("range = %+v, want 3:5 to 3:9", tg.Range)
				}
				if tg.Placeholder != "Old" {
					t.Errorf("placeholder = %q, want Old", tg.Placeholder)
				}
			},
		},
		{
			name: "default behavior",
			raw:  `{"defaultBehavior":true}`,
			check: func(t *testing.T, tg *RenameTarget) {
				if tg == nil || !tg.Default {
					t.Errorf("defaultBehavior:true decoded as %+v, want Default", tg)
				}
			},
		},
		{
			name: "null is a refusal",
			raw:  `null`,
			check: func(t *testing.T, tg *RenameTarget) {
				if tg != nil {
					t.Errorf("null decoded as %+v, want a refusal", tg)
				}
			},
		},
		{
			name: "an object with neither is a refusal",
			raw:  `{}`,
			check: func(t *testing.T, tg *RenameTarget) {
				if tg != nil {
					t.Errorf("an empty object decoded as %+v, want a refusal", tg)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("textDocument/prepareRename", func(*Message) (any, *ResponseError) {
				return json.RawMessage(c.raw), nil
			})
			ctx, cancel := ctx1s(t)
			defer cancel()
			tg, err := RequestPrepareRename(ctx, f.conn, "/w/a.go", Position{Line: 1, Character: 3})
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, tg)
		})
	}
}

// A server that refuses the position answers with an error, and its message is
// the most specific explanation there is. The call must return it rather than
// swallowing it into a nil target, so the editor can speak the server's words.
func TestPrepareRenameRefusalCarriesTheServerError(t *testing.T) {
	f := newFake(t)
	f.on("textDocument/prepareRename", func(*Message) (any, *ResponseError) {
		return nil, &ResponseError{Code: -32602, Message: "cannot rename this element"}
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	tg, err := RequestPrepareRename(ctx, f.conn, "/w/a.go", Position{})
	if err == nil {
		t.Fatal("a refused prepare reported success")
	}
	if tg != nil {
		t.Errorf("target = %+v, want nil alongside the error", tg)
	}
}

// The prepare request names the position it is asking about. A check answered
// for the wrong place would clear a rename the server would then refuse, which
// is exactly the failure the prepare step exists to move earlier.
func TestPrepareRenameSendsThePosition(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/prepareRename", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`null`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestPrepareRename(ctx, f.conn, "/w/a.go", Position{Line: 12, Character: 34}); err != nil {
		t.Fatal(err)
	}
	pos, _ := got["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(12) || pos["character"] != float64(34) {
		t.Errorf("position sent as %v, want 12:34", pos)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
}

// The changes map groups edits by document URI, and every group must survive
// with its own path: flattening them would apply one file's edits at another
// file's offsets, which is worse than not renaming at all.
func TestWorkspaceEditChangesMap(t *testing.T) {
	raw := `{"changes":{
	  "file:///w/b.go":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":4}},"newText":"New"}],
	  "file:///w/a.go":[{"range":{"start":{"line":0,"character":5},"end":{"line":0,"character":8}},"newText":"New"}]
	}}`
	f := newFake(t)
	f.on("textDocument/rename", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	we, err := RequestRename(ctx, f.conn, "/w/a.go", Position{}, "New")
	if err != nil {
		t.Fatal(err)
	}
	if we == nil || len(we.Docs) != 2 {
		t.Fatalf("decoded %+v, want two documents", we)
	}
	// Sorted by URI, so a fresh decode of the same answer applies in the same
	// order every time.
	if we.Docs[0].Path != "/w/a.go" || we.Docs[1].Path != "/w/b.go" {
		t.Errorf("documents = %q, %q; want them sorted by path", we.Docs[0].Path, we.Docs[1].Path)
	}
	if len(we.Docs[0].Edits) != 1 || we.Docs[0].Edits[0].NewText != "New" {
		t.Errorf("edits = %+v", we.Docs[0].Edits)
	}
	if we.Docs[0].Edits[0].Range.Start.Character != 5 {
		t.Errorf("range = %+v, want the range the URI's group carried", we.Docs[0].Edits[0].Range)
	}
}

// The newer documentChanges shape is an array of TextDocumentEdit entries. A
// file operation in the same array is not a text edit and must be surfaced, not
// skipped: applying the text while dropping the operation is a partial rename.
func TestWorkspaceEditDocumentChanges(t *testing.T) {
	raw := `{"documentChanges":[
	  {"textDocument":{"uri":"file:///w/a.go","version":4},"edits":[
	    {"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"New"}]},
	  {"kind":"rename","oldUri":"file:///w/old.go","newUri":"file:///w/new.go"}
	]}`
	f := newFake(t)
	f.on("textDocument/rename", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	we, err := RequestRename(ctx, f.conn, "/w/a.go", Position{}, "New")
	if err != nil {
		t.Fatal(err)
	}
	if we == nil || len(we.Docs) != 1 || we.Docs[0].Path != "/w/a.go" {
		t.Fatalf("decoded %+v, want /w/a.go", we)
	}
	if len(we.Docs[0].Edits) != 1 || we.Docs[0].Edits[0].NewText != "New" {
		t.Errorf("edits = %+v", we.Docs[0].Edits)
	}
	if len(we.ResourceOps) != 1 || we.ResourceOps[0] != "rename" {
		t.Errorf("ResourceOps = %v, want the file rename recorded", we.ResourceOps)
	}
}

// Both shapes can name the same document; their edits are one list, because
// applying the map's half and missing the array's would leave a renamed symbol
// with one stale use.
func TestWorkspaceEditMergesBothShapes(t *testing.T) {
	raw := `{
	  "changes":{"file:///w/a.go":[{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":3}},"newText":"New"}]},
	  "documentChanges":[{"textDocument":{"uri":"file:///w/a.go"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"New"}]}]
	}`
	f := newFake(t)
	f.on("textDocument/rename", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	we, err := RequestRename(ctx, f.conn, "/w/a.go", Position{}, "New")
	if err != nil {
		t.Fatal(err)
	}
	if we == nil || len(we.Docs) != 1 || len(we.Docs[0].Edits) != 2 {
		t.Fatalf("decoded %+v, want one document with both edits", we)
	}
}

// Null and an empty edit mean "nothing to do", which is not an error and must
// not read as one.
func TestWorkspaceEditNullAndEmpty(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"changes":{}}`, `{"documentChanges":[]}`} {
		f := newFake(t)
		f.on("textDocument/rename", func(*Message) (any, *ResponseError) {
			return json.RawMessage(raw), nil
		})
		ctx, cancel := ctx1s(t)
		we, err := RequestRename(ctx, f.conn, "/w/a.go", Position{}, "New")
		cancel()
		if err != nil {
			t.Errorf("%s produced an error: %v", raw, err)
		}
		if we != nil {
			t.Errorf("%s produced %+v, want nothing to apply", raw, we)
		}
	}
}

// The new name and the position are the whole request. A rename that carried
// the old name, or asked about the wrong place, would apply confidently wrong
// edits rather than fail.
func TestRenameSendsTheNewName(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/rename", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`null`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestRename(ctx, f.conn, "/w/a.go", Position{Line: 7, Character: 9}, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if got["newName"] != "Renamed" {
		t.Errorf("newName sent as %v, want Renamed", got["newName"])
	}
	pos, _ := got["position"].(map[string]any)
	if pos == nil || pos["line"] != float64(7) || pos["character"] != float64(9) {
		t.Errorf("position sent as %v, want 7:9", pos)
	}
	doc, _ := got["textDocument"].(map[string]any)
	if doc == nil || doc["uri"] != "file:///w/a.go" {
		t.Errorf("uri sent as %v", doc)
	}
}

// A dead connection fails rather than panicking, since the server may die
// between the keystroke that asked and the answer.
func TestRenameRequestsOnADeadConnection(t *testing.T) {
	if _, err := RequestPrepareRename(context.Background(), nil, "/w/a.go", Position{}); err != ErrClosed {
		t.Errorf("prepare err = %v, want ErrClosed", err)
	}
	if _, err := RequestRename(context.Background(), nil, "/w/a.go", Position{}, "X"); err != ErrClosed {
		t.Errorf("rename err = %v, want ErrClosed", err)
	}

	f := newFake(t)
	f.die()
	waitFor(t, f.conn.Closed)
	if _, err := RequestPrepareRename(context.Background(), f.conn, "/w/a.go", Position{}); err == nil {
		t.Error("a prepare on a dead connection succeeded")
	}
	if _, err := RequestRename(context.Background(), f.conn, "/w/a.go", Position{}, "X"); err == nil {
		t.Error("a rename on a dead connection succeeded")
	}
}
