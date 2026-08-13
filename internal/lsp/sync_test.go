package lsp

import (
	"encoding/json"
	"testing"
)

// notes returns the params of every notification the fake saw for a method.
func (f *fakeServer) notes(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, p := range f.params[method] {
		var m map[string]any
		if json.Unmarshal(p, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func syncFixture(t *testing.T) (*fakeServer, *Sync) {
	t.Helper()
	f := newFake(t)
	return f, NewSync(f.conn, SyncFull)
}

// A document is opened once. Servers treat a duplicate didOpen as a protocol
// error, and the editor has several paths that can reach the same file.
func TestOpenIsSentOnce(t *testing.T) {
	f, s := syncFixture(t)
	for i := 0; i < 3; i++ {
		if err := s.Open("/w/a.go", "go", "package a\n", 1); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(f.notes("textDocument/didOpen")) >= 1 })
	if got := len(f.notes("textDocument/didOpen")); got != 1 {
		t.Errorf("sent %d didOpen notifications, want 1", got)
	}
	if s.Count() != 1 {
		t.Errorf("tracking %d documents, want 1", s.Count())
	}
}

// A change for a document the server was never told about is dropped. The
// server would reject it, and worse, some accept it and build a phantom
// document that answers every later request from nothing.
func TestChangeWithoutOpenIsDropped(t *testing.T) {
	f, s := syncFixture(t)
	if err := s.Change("/w/never-opened.go", "text\n", 2); err != nil {
		t.Fatal(err)
	}
	if got := len(f.notes("textDocument/didChange")); got != 0 {
		t.Errorf("sent %d changes for an unopened document", got)
	}
}

// A version that has not moved means the buffer has not changed, and a
// didChange claiming otherwise makes the server redo work on every keystroke
// that does not edit.
func TestChangeRequiresANewVersion(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 2)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	s.Change("/w/a.go", "two\n", 2) // same version
	s.Change("/w/a.go", "old\n", 1) // older version
	if got := len(f.notes("textDocument/didChange")); got != 1 {
		t.Errorf("sent %d changes, want 1 — only a newer version is a change", got)
	}
	if v, _ := s.Version("/w/a.go"); v != 2 {
		t.Errorf("tracked version %d, want 2", v)
	}
}

// The version travels with every change. A server answering against the wrong
// version does not error — it answers confidently at a position that no longer
// means anything, which is the silent failure this tracking exists to prevent.
func TestChangeCarriesTheVersionAndText(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 7)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	n := f.notes("textDocument/didChange")[0]
	doc, _ := n["textDocument"].(map[string]any)
	if doc == nil || doc["version"] != float64(7) {
		t.Errorf("version = %v, want 7", doc["version"])
	}
	changes, _ := n["contentChanges"].([]any)
	if len(changes) != 1 {
		t.Fatalf("sent %d content changes, want one whole document", len(changes))
	}
	first, _ := changes[0].(map[string]any)
	if first["text"] != "two\n" {
		t.Errorf("text = %v, want the whole new document", first["text"])
	}
	if _, hasRange := first["range"]; hasRange {
		t.Error("a range was sent; whole-document sync must not carry one")
	}
}

// A server that wants no change notifications gets none, but is still told
// about opens: it may still answer requests against the document it was given.
func TestSyncNoneSendsNoChanges(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncNone)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 2)
	waitFor(t, func() bool { return len(f.notes("textDocument/didOpen")) >= 1 })
	if got := len(f.notes("textDocument/didChange")); got != 0 {
		t.Errorf("sent %d changes to a server that asked for none", got)
	}
}

// Closing forgets the document, so its diagnostics can be cleared and a later
// open is a real open rather than a silent no-op.
func TestCloseForgets(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Close("/w/a.go")
	waitFor(t, func() bool { return len(f.notes("textDocument/didClose")) >= 1 })
	if s.IsOpen("/w/a.go") {
		t.Error("a closed document is still tracked")
	}

	s.Open("/w/a.go", "go", "one\n", 1)
	waitFor(t, func() bool { return len(f.notes("textDocument/didOpen")) >= 2 })
	if got := len(f.notes("textDocument/didOpen")); got != 2 {
		t.Errorf("reopening sent %d opens, want a second one", got)
	}
}

// Closing something never opened sends nothing.
func TestCloseWithoutOpenSendsNothing(t *testing.T) {
	f, s := syncFixture(t)
	s.Close("/w/never.go")
	if got := len(f.notes("textDocument/didClose")); got != 0 {
		t.Errorf("sent %d closes for an unopened document", got)
	}
}

// Save is only sent for tracked documents, and carries the text for servers
// that run slower checks on write.
func TestSave(t *testing.T) {
	f, s := syncFixture(t)
	s.Save("/w/never.go", "x")
	if got := len(f.notes("textDocument/didSave")); got != 0 {
		t.Errorf("saved an unopened document")
	}
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Save("/w/a.go", "one\n")
	waitFor(t, func() bool { return len(f.notes("textDocument/didSave")) >= 1 })
}

// The advertised sync kind is honoured rather than assumed, and the protocol
// allows either a number or an options object.
func TestSyncKindOf(t *testing.T) {
	cases := map[string]SyncKind{
		`0`:                             SyncNone,
		`1`:                             SyncFull,
		`2`:                             SyncIncremental,
		`{"change":2}`:                  SyncIncremental,
		`{"change":0,"openClose":true}`: SyncNone,
		`{"openClose":true}`:            SyncFull, // no change field: assume full
		`99`:                            SyncFull, // unrecognised
		``:                              SyncFull, // absent
		`null`:                          SyncFull,
	}
	for raw, want := range cases {
		if got := SyncKindOf([]byte(raw)); got != want {
			t.Errorf("SyncKindOf(%q) = %d, want %d", raw, got, want)
		}
	}
}

// An absent value must not become SyncNone. A server that does not say is far
// more likely to be an older one assuming full sync than one wanting no
// updates, and guessing None leaves every feature answering from a document
// frozen at the moment it was opened.
func TestUnknownSyncKindIsFullNotNone(t *testing.T) {
	for _, raw := range []string{``, `null`, `"nonsense"`, `{}`, `-1`} {
		if got := SyncKindOf([]byte(raw)); got == SyncNone {
			t.Errorf("SyncKindOf(%q) = None; a document frozen at open is worse", raw)
		}
	}
}

func TestLanguageID(t *testing.T) {
	cases := map[string]string{
		"/w/a.go":      "go",
		"/w/a.rs":      "rust",
		"/w/a.tsx":     "typescriptreact",
		"/w/a.cpp":     "cpp",
		"/w/deep/a.py": "python",
		"/w/Makefile":  "",
		"/w/a.unknown": "",
		"/w/no-ext":    "",
		"/w/.hidden":   "", // a dotfile has no extension
		"":             "",
	}
	for path, want := range cases {
		if got := LanguageID(path); got != want {
			t.Errorf("LanguageID(%q) = %q, want %q", path, got, want)
		}
	}
}

// Every entry point fails cleanly on a dead connection rather than panicking,
// since the server may die between any two keystrokes.
func TestSyncOnADeadConnection(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	f.die()
	waitFor(t, f.conn.Closed)

	// These must return rather than panic; whether they error is the
	// connection's business, not this package's.
	s.Open("/w/b.go", "go", "x\n", 1)
	s.Change("/w/a.go", "y\n", 2)
	s.Save("/w/a.go", "y\n")
	s.Close("/w/a.go")

	nilSync := NewSync(nil, SyncFull)
	if err := nilSync.Open("/w/a.go", "go", "x", 1); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	if err := nilSync.Change("/w/a.go", "x", 2); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}
