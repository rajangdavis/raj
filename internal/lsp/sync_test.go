package lsp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
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

// A change for a document the server was never told about is dropped rather
// than sent. The server would reject it, and worse, some accept it and build a
// phantom document that answers every later request from nothing.
func TestChangeWithoutOpenIsDropped(t *testing.T) {
	f, s := syncFixture(t)
	if err := s.Change("/w/never-opened.go", "text\n", 2, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.notes("textDocument/didChange")); got != 0 {
		t.Errorf("sent %d changes for an unopened document", got)
	}
}

// A version that has not moved, with text that has not moved, means the buffer
// is unchanged, and a didChange claiming otherwise makes the server redo work
// on every keystroke that does not edit.
func TestChangeRequiresANewVersion(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 2, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	s.Change("/w/a.go", "two\n", 2, nil) // same version, same text
	if got := len(f.notes("textDocument/didChange")); got != 1 {
		t.Errorf("sent %d changes, want 1 — only a moved buffer is a change", got)
	}
	if v, _ := s.Version("/w/a.go"); v != 2 {
		t.Errorf("tracked version %d, want 2", v)
	}
}

// A version moving backwards is not a stale echo to drop: versions count
// journal entries, and a session replaced by a reload starts counting from
// zero again, so a smaller version can sit on entirely new text. Dropping it
// would freeze the server's copy at whatever it held before the reload — the
// one outcome this package exists to prevent — so the whole document goes
// out under the new, lower version.
func TestVersionMovingBackwardsResendsTheWholeDocument(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "gen one\n", 5)
	s.Change("/w/a.go", "gen one point five\n", 6, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	s.Change("/w/a.go", "rewritten\n", 3, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 2 })

	n := f.notes("textDocument/didChange")[1]
	doc, _ := n["textDocument"].(map[string]any)
	if doc["version"] != float64(3) {
		t.Errorf("version = %v, want 3 — the reloaded session's own", doc["version"])
	}
	first := contentChanges(t, n)[0]
	if _, hasRange := first["range"]; hasRange {
		t.Error("a range was sent against a history the server does not have")
	}
	if first["text"] != "rewritten\n" {
		t.Errorf("text = %v, want the whole reloaded document", first["text"])
	}
	if v, _ := s.Version("/w/a.go"); v != 3 {
		t.Errorf("tracked version %d, want 3", v)
	}
}

// Same version, different text: the session was replaced and happened to grow
// back to the same number. The text mismatch is the tell, and the whole
// document is the only honest answer.
func TestSameVersionDifferentTextResends(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "a\n", 2)
	s.Change("/w/a.go", "b\n", 2, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	first := contentChanges(t, f.notes("textDocument/didChange")[0])[0]
	if first["text"] != "b\n" {
		t.Errorf("text = %v, want the new document at the same version", first["text"])
	}
}

// The version travels with every change. A server answering against the wrong
// version does not error — it answers confidently at a position that no longer
// means anything, which is the silent failure this tracking exists to prevent.
func TestChangeCarriesTheVersionAndText(t *testing.T) {
	f, s := syncFixture(t)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 7, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	n := f.notes("textDocument/didChange")[0]
	doc, _ := n["textDocument"].(map[string]any)
	if doc == nil || doc["version"] != float64(7) {
		t.Errorf("version = %v, want 7", doc["version"])
	}
	changes := contentChanges(t, n)
	if len(changes) != 1 {
		t.Fatalf("sent %d content changes, want one whole document", len(changes))
	}
	first := changes[0]
	if first["text"] != "two\n" {
		t.Errorf("text = %v, want the whole new document", first["text"])
	}
	if _, hasRange := first["range"]; hasRange {
		t.Error("a range was sent; whole-document sync must not carry one")
	}
}

// An incremental server gets only the changed ranges, in application order,
// each expressed against the document the previous change produced — which is
// both the journal's frame for an op and the server's frame for the change.
func TestIncrementalSendsOnlyTheChangedRanges(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	old := "one\ntwo\nthree\n"
	s.Open("/w/a.go", "go", old, 1)

	// Replace "two" with "2" (shrinks the document), then "three" with "3" at
	// its new offset in the frame the first edit produced.
	mid := "one\n2\nthree\n"
	new := "one\n2\n3\n"
	edits := []Edit{
		{Start: strings.Index(old, "two"), End: strings.Index(old, "two") + len("two"), Text: "2"},
		{Start: strings.Index(mid, "three"), End: strings.Index(mid, "three") + len("three"), Text: "3"},
	}
	s.Change("/w/a.go", new, 2, edits)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	n := f.notes("textDocument/didChange")[0]
	doc, _ := n["textDocument"].(map[string]any)
	if doc["version"] != float64(2) {
		t.Errorf("version = %v, want 2", doc["version"])
	}
	changes := contentChanges(t, n)
	if len(changes) != 2 {
		t.Fatalf("sent %d content changes, want the two ranges", len(changes))
	}
	assertRange(t, changes[0], Range{Start: wantPosition(t, old, edits[0].Start), End: wantPosition(t, old, edits[0].End)})
	assertRange(t, changes[1], Range{Start: wantPosition(t, mid, edits[1].Start), End: wantPosition(t, mid, edits[1].End)})
	if changes[0]["text"] != "2" || changes[1]["text"] != "3" {
		t.Errorf("texts = %v, %v, want the replacement strings", changes[0]["text"], changes[1]["text"])
	}
	// The proof: applied as a server applies them, the ranges reproduce the
	// buffer exactly.
	if got := applyContentChanges(t, old, changes); got != new {
		t.Errorf("server copy %q, want %q", got, new)
	}
}

// Columns are UTF-16 code units, not bytes: an emoji is two units and the
// range ends past it must count both, or the server deletes the wrong span.
func TestIncrementalConvertsColumnsToUTF16(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	old := "a😀b\nλ日\n"
	s.Open("/w/a.go", "go", old, 1)

	start := strings.Index(old, "😀")
	edits := []Edit{{Start: start, End: start + len("😀"), Text: "e"}}
	new := "aeb\nλ日\n"
	s.Change("/w/a.go", new, 2, edits)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	changes := contentChanges(t, f.notes("textDocument/didChange")[0])
	if len(changes) != 1 {
		t.Fatalf("sent %d content changes, want one range", len(changes))
	}
	want := Range{Start: wantPosition(t, old, start), End: wantPosition(t, old, start+len("😀"))}
	if want.Start.Character != 1 || want.End.Character != 3 {
		t.Fatalf("test itself is wrong: a😀 must be 3 UTF-16 units, got %+v", want)
	}
	assertRange(t, changes[0], want)
	if got := applyContentChanges(t, old, changes); got != new {
		t.Errorf("server copy %q, want %q", got, new)
	}
}

// An edit whose span crosses a newline converts to positions on different
// lines, and the replacement's own newlines are none of the range's business.
func TestIncrementalEditSpanningLines(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	old := "alpha\nbeta\ngamma\n"
	s.Open("/w/a.go", "go", old, 1)

	start := strings.Index(old, "ta\ngamma")
	edits := []Edit{{Start: start, End: start + len("ta\ngamma"), Text: "X\nY"}}
	new := old[:start] + "X\nY" + old[start+len("ta\ngamma"):]
	s.Change("/w/a.go", new, 2, edits)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	changes := contentChanges(t, f.notes("textDocument/didChange")[0])
	if len(changes) != 1 {
		t.Fatalf("sent %d content changes, want one range", len(changes))
	}
	assertRange(t, changes[0], Range{Start: wantPosition(t, old, start), End: wantPosition(t, old, start+len("ta\ngamma"))})
	if got := applyContentChanges(t, old, changes); got != new {
		t.Errorf("server copy %q, want %q", got, new)
	}
}

// Each batch converts against the text the server was pinned at, so a second
// batch is correct on the document the first one left behind — the
// version-pinning the whole design rests on.
func TestIncrementalChangeSurvivesASecondBatch(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	old := "start\nmiddle\nend\n"
	s.Open("/w/a.go", "go", old, 1)

	mid := "start\nMIDDLE\nend\n"
	at := strings.Index(old, "middle")
	s.Change("/w/a.go", mid, 2, []Edit{{Start: at, End: at + len("middle"), Text: "MIDDLE"}})
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	server := applyContentChanges(t, old, contentChanges(t, f.notes("textDocument/didChange")[0]))
	if server != mid {
		t.Fatalf("after batch one the server holds %q, want %q", server, mid)
	}

	new := "start\nMIDDLE\nEND\n"
	at = strings.Index(mid, "end")
	s.Change("/w/a.go", new, 3, []Edit{{Start: at, End: at + len("end"), Text: "END"}})
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 2 })

	changes := contentChanges(t, f.notes("textDocument/didChange")[1])
	assertRange(t, changes[0], Range{Start: wantPosition(t, mid, at), End: wantPosition(t, mid, at+len("end"))})
	if got := applyContentChanges(t, server, changes); got != new {
		t.Errorf("server copy after batch two %q, want %q", got, new)
	}
}

// Without an edit history the whole document goes out, however the server
// asked to be told: nil edits never disables sync, it just costs the ranges.
func TestIncrementalWithoutHistorySendsTheWholeDocument(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 2, nil)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	first := contentChanges(t, f.notes("textDocument/didChange")[0])[0]
	if _, hasRange := first["range"]; hasRange {
		t.Error("a range was sent with no history to compute it from")
	}
	if first["text"] != "two\n" {
		t.Errorf("text = %v, want the whole document", first["text"])
	}
}

// A full-sync server is sent the whole document even when the edit history is
// right there: the advertised kind is honoured, not second-guessed.
func TestSyncFullIgnoresTheEditHistory(t *testing.T) {
	f, s := syncFixture(t)
	old := "one\ntwo\n"
	s.Open("/w/a.go", "go", old, 1)
	at := strings.Index(old, "two")
	s.Change("/w/a.go", "one\nTWO\n", 2, []Edit{{Start: at, End: at + 3, Text: "TWO"}})
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	first := contentChanges(t, f.notes("textDocument/didChange")[0])[0]
	if _, hasRange := first["range"]; hasRange {
		t.Error("a full-sync server was sent a range")
	}
	if first["text"] != "one\nTWO\n" {
		t.Errorf("text = %v, want the whole document", first["text"])
	}
}

// A batch that is all bookkeeping — the version moved, the bytes did not —
// sends nothing, and the tracked version still advances.
func TestNoopEditsSendNoNotification(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	s.Open("/w/a.go", "go", "one\n", 1)
	waitFor(t, func() bool { return len(f.notes("textDocument/didOpen")) >= 1 })
	s.Change("/w/a.go", "one\n", 2, []Edit{{Start: 0, End: 0, Text: ""}})

	if got := len(f.notes("textDocument/didChange")); got != 0 {
		t.Errorf("sent %d changes for edits that changed nothing", got)
	}
	if v, _ := s.Version("/w/a.go"); v != 2 {
		t.Errorf("tracked version %d, want 2", v)
	}
}

// Past the cap the whole document is the cheaper message: conversion replays
// every edit over the pinned text, which is edits × document.
func TestIncrementalPastTheCapSendsTheWholeDocument(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncIncremental)
	s.Open("/w/a.go", "go", "", 1)

	edits := make([]Edit, 0, maxIncrementalEdits+1)
	for i := 0; i <= maxIncrementalEdits; i++ {
		edits = append(edits, Edit{Start: 0, End: 0, Text: "x"})
	}
	text := strings.Repeat("x", maxIncrementalEdits+1)
	s.Change("/w/a.go", text, 2, edits)
	waitFor(t, func() bool { return len(f.notes("textDocument/didChange")) >= 1 })

	first := contentChanges(t, f.notes("textDocument/didChange")[0])[0]
	if _, hasRange := first["range"]; hasRange {
		t.Error("an over-cap batch was sent as ranges")
	}
	if first["text"] != text {
		t.Errorf("text = %v, want the whole document", first["text"])
	}
}

// The conversion refuses — and the caller falls back to the whole document —
// for anything it cannot prove right: no history, an edit that does not fit
// the text it describes, or a replay that disagrees with the buffer.
func TestIncrementalChangesRefusals(t *testing.T) {
	cases := []struct {
		name  string
		old   string
		edits []Edit
		new   string
	}{
		{"no history", "abc\n", nil, "abd\n"},
		{"edit past the end", "abc\n", []Edit{{Start: 0, End: 99, Text: "x"}}, "xbc\n"},
		{"negative start", "abc\n", []Edit{{Start: -1, End: 1, Text: "x"}}, "xbc\n"},
		{"ends crossed", "abc\n", []Edit{{Start: 2, End: 1, Text: "x"}}, "axc\n"},
		{"replay disagrees", "abc\n", []Edit{{Start: 0, End: 1, Text: "x"}}, "abc\n"},
		{"a mid-rune edge", "λx\n", []Edit{{Start: 1, End: 2, Text: "y"}}, "\xceyx\n"},
	}
	for _, c := range cases {
		if _, ok := incrementalChanges(c.old, c.edits, c.new); ok {
			t.Errorf("%s: accepted, want refusal and the whole document", c.name)
		}
	}
}

// A server that wants no change notifications gets none, but is still told
// about opens: it may still answer requests against the document it was given.
func TestSyncNoneSendsNoChanges(t *testing.T) {
	f := newFake(t)
	s := NewSync(f.conn, SyncNone)
	s.Open("/w/a.go", "go", "one\n", 1)
	s.Change("/w/a.go", "two\n", 2, nil)
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
	s.Change("/w/a.go", "y\n", 2, nil)
	s.Save("/w/a.go", "y\n")
	s.Close("/w/a.go")

	nilSync := NewSync(nil, SyncFull)
	if err := nilSync.Open("/w/a.go", "go", "x", 1); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	if err := nilSync.Change("/w/a.go", "x", 2, nil); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}

// contentChanges pulls the change list out of a didChange's params.
func contentChanges(t *testing.T, n map[string]any) []map[string]any {
	t.Helper()
	raw, ok := n["contentChanges"].([]any)
	if !ok {
		t.Fatalf("didChange carries no contentChanges: %v", n)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, c := range raw {
		out = append(out, c.(map[string]any))
	}
	return out
}

// assertRange checks a content change's range against a want derived by the
// test — never a restatement of this package's own arithmetic.
func assertRange(t *testing.T, c map[string]any, want Range) {
	t.Helper()
	r, ok := c["range"].(map[string]any)
	if !ok {
		t.Fatalf("change carries no range: %v", c)
	}
	got := Range{Start: jsonPosition(t, r, "start"), End: jsonPosition(t, r, "end")}
	if got != want {
		t.Errorf("range = %+v, want %+v", got, want)
	}
}

func jsonPosition(t *testing.T, r map[string]any, key string) Position {
	t.Helper()
	p, ok := r[key].(map[string]any)
	if !ok {
		t.Fatalf("range has no %q: %v", key, r)
	}
	return Position{Line: int(p["line"].(float64)), Character: int(p["character"].(float64))}
}

// wantPosition derives the LSP position of a byte offset using Go's standard
// UTF-16 encoder, so an expected value is independently computed, not the
// package's conversion restated in the test.
func wantPosition(t *testing.T, text string, off int) Position {
	t.Helper()
	if off < 0 || off > len(text) {
		t.Fatalf("offset %d outside %d bytes", off, len(text))
	}
	line := strings.Count(text[:off], "\n")
	start := strings.LastIndex(text[:off], "\n") + 1 // absent is -1; the line starts at 0
	return Position{Line: line, Character: len(utf16.Encode([]rune(text[start:off])))}
}

// applyContentChanges applies a didChange's content changes to text the way a
// server does: a whole-document change replaces everything, and a ranged one
// is read back to bytes against the document the previous changes produced.
// One shared implementation, so the table tests and the fuzz check the wire
// against the same application semantics.
func applyContentChanges(t *testing.T, text string, changes []map[string]any) string {
	t.Helper()
	for _, c := range changes {
		r, ok := c["range"]
		if !ok {
			text = c["text"].(string)
			continue
		}
		rng := r.(map[string]any)
		lo, hi := NewDocument(text).Span(Range{
			Start: jsonPosition(t, rng, "start"),
			End:   jsonPosition(t, rng, "end"),
		})
		text = text[:lo] + c["text"].(string) + text[hi:]
	}
	return text
}

// throughJSON round-trips changes as the wire would, so the fuzz applies what
// a server receives rather than what the conversion happened to build.
func throughJSON(t *testing.T, changes []map[string]any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// deriveEdits runs a byte-driven edit program over text and returns the batch
// it produced, each edit in the frame its predecessors produced — the same
// contract the journal's OpsSince satisfies and the same one the server
// applies against. Insertions and replacements draw from a rune set heavy in
// the characters that make bytes and UTF-16 units disagree.
func deriveEdits(text string, program []byte) ([]Edit, string) {
	working := text
	var edits []Edit
	runes := []string{"a", "λ", "日", "\n", "→", "😀"}
	for i := 0; i+1 < len(program); i += 2 {
		op, arg := program[i], program[i+1]
		switch op % 3 {
		case 0: // insert a rune at a rune boundary
			pos := runeBoundary(working, int(arg)%(len(working)+1))
			r := runes[int(arg)%len(runes)]
			edits = append(edits, Edit{Start: pos, End: pos, Text: r})
			working = working[:pos] + r + working[pos:]
		case 1: // delete exactly one rune
			if len(working) == 0 {
				continue
			}
			pos := runeBoundary(working, int(arg)%len(working))
			_, size := utf8.DecodeRuneInString(working[pos:])
			edits = append(edits, Edit{Start: pos, End: pos + size})
			working = working[:pos] + working[pos+size:]
		case 2: // replace a two-rune span, which may cross a newline
			if len(working) == 0 {
				continue
			}
			lo := runeBoundary(working, int(arg)%len(working))
			_, size := utf8.DecodeRuneInString(working[lo:])
			hi := lo + size
			if hi < len(working) {
				_, size = utf8.DecodeRuneInString(working[hi:])
				hi += size
			}
			r := runes[int(arg>>3)%len(runes)] + runes[int(arg>>5)%len(runes)]
			edits = append(edits, Edit{Start: lo, End: hi, Text: r})
			working = working[:lo] + r + working[hi:]
		}
	}
	return edits, working
}

// runeBoundary advances off to the first rune start at or after it, so a
// generated edit never splits a rune.
func runeBoundary(s string, off int) int {
	for off < len(s) && !utf8.RuneStart(s[off]) {
		off++
	}
	return off
}

func lessPosition(a, b Position) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Character < b.Character)
}

// The property incremental sync stands on, exercised end to end: an arbitrary
// edit program over arbitrary text converts to ranges a server can apply —
// through the actual JSON the wire carries — and reproduce the buffer byte
// for byte. FuzzRangeRoundTrip proves one range converts exactly; this proves
// a whole batch, each in its own frame, composes.
func FuzzIncrementalSyncReplay(f *testing.F) {
	// One seed per tricky shape: inserts, deletes, and replacements around
	// newlines, CJK, and surrogate pairs.
	f.Add("hello world\n", []byte{0x00, 0x03, 0x01, 0x02, 0x02, 0x05})
	f.Add("λ日x\n", []byte{0x01, 0x01, 0x00, 0x00, 0x02, 0x03})
	f.Add("a😀b\n日本語\n", []byte{0x02, 0x07, 0x00, 0x02, 0x01, 0x04})
	f.Add("", []byte{0x00, 0x00, 0x00, 0x01})
	f.Add("one\ntwo\nthree\n", []byte{0x01, 0x08, 0x02, 0x0a, 0x00, 0x05, 0x01, 0x01})

	f.Fuzz(func(t *testing.T, text string, program []byte) {
		if len(text) > 4000 || !utf8.ValidString(text) {
			return
		}
		if len(program) > 64 {
			program = program[:64] // a long program is not a more interesting one
		}
		edits, final := deriveEdits(text, program)
		if len(edits) == 0 {
			return
		}

		changes, ok := incrementalChanges(text, edits, final)
		if !ok {
			t.Fatalf("a replayable edit program was refused\nprogram=%x", program)
		}
		for i, c := range changes {
			r, _ := c["range"].(Range)
			if lessPosition(r.End, r.Start) {
				t.Fatalf("change %d is inverted: %+v\nprogram=%x", i, r, program)
			}
		}
		if got := applyContentChanges(t, text, throughJSON(t, changes)); got != final {
			t.Fatalf("server copy %q, buffer %q\nprogram=%x", got, final, program)
		}
	})
}
