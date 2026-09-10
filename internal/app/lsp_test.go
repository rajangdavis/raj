package app

import (
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/lsp"
	"raj/internal/piecetable"
)

// The rule the whole integration follows: no language feature may make the
// editor worse when it is unavailable. A file with no server says so once and
// changes nothing else.
func TestHoverWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "some plain text\n")
	before := h.Pane().File.Text()
	h.press("super+i")

	if got := h.Pane().File.Text(); got != before {
		t.Error("asking for a hover changed the buffer")
	}
	// The harness fixture is a .go file, so which message appears depends on
	// whether gopls is installed where the tests run. Either is correct; what
	// matters is that something explains it and nothing else changed.
	if h.Status() == "" {
		t.Error("nothing happened and nothing said why")
	}
}

func TestDefinitionWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "some plain text\n")
	head := h.Pane().Cursors.Primary().Head
	h.press("alt+super+d")

	if got := h.Pane().Cursors.Primary().Head; got != head {
		t.Error("the cursor moved with no server to say where to")
	}
	if len(h.Tabs.All()) != 1 {
		t.Error("a tab was opened with no definition to open")
	}
}

// An answer for a position the cursor has left is dropped. It is worse than no
// answer, because it is shown as though it described where the cursor is now.
func TestStaleAnswersAreDropped(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 5
	h.park(lspAnswer{gen: 4, kind: answerHover, text: "stale"})
	h.applyAnswer()

	if strings.Contains(h.Hover.Text(), "stale") {
		t.Errorf("panel = %q; a superseded answer was shown", h.Hover.Text())
	}

	h.park(lspAnswer{gen: 5, kind: answerHover, text: "current"})
	h.applyAnswer()
	if !strings.Contains(h.Hover.Text(), "current") {
		t.Errorf("panel = %q, want the current answer", h.Hover.Text())
	}
}

// A hover with nothing in it opens no panel: an empty box is worse than a
// word, and most positions in most files have nothing to say about them. The
// status line is where that word goes.
func TestEmptyHoverOpensNoPanel(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: ""})
	h.applyAnswer()
	if h.Hover.Open {
		t.Error("an empty answer opened a panel")
	}
	if h.Status() == "" {
		t.Error("nothing happened and nothing said why")
	}
}

// A multi-line hover keeps its lines. Folding them onto one row was the status
// line's limitation and the reason the panel exists: a signature without its
// line breaks is a signature that has lost its shape.
func TestMultiLineHoverKeepsItsLines(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F(x int) error\n\nDoes a thing."})
	h.applyAnswer()

	if !h.Hover.Open {
		t.Fatal("no panel opened")
	}
	got := h.Hover.Text()
	if !strings.Contains(got, "\n") {
		t.Error("the answer was folded onto one line")
	}
	for _, want := range []string{"func F(x int) error", "Does a thing."} {
		if !strings.Contains(got, want) {
			t.Errorf("panel = %q, missing %q", got, want)
		}
	}
}

// A definition result moves the cursor to the named position, converting from
// the server's UTF-16 coordinates to the buffer's byte offsets.
func TestDefinitionJumps(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerDefinition,
		locs: []lsp.Location{{
			Path:  h.Pane().File.Path,
			Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
		}},
	})
	h.applyAnswer()

	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 || col != 5 {
		t.Errorf("cursor at %d:%d, want 2:5", line, col)
	}
}

// Nothing found says so rather than jumping somewhere arbitrary.
func TestDefinitionNotFound(t *testing.T) {
	h := newHarness(t, "package main\n")
	head := h.Pane().Cursors.Primary().Head
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerDefinition})
	h.applyAnswer()

	if h.Pane().Cursors.Primary().Head != head {
		t.Error("the cursor moved with nothing found")
	}
	if !strings.Contains(h.Status(), "no definition") {
		t.Errorf("status = %q", h.Status())
	}
}

// A language with no configured server is not an error, and neither is a file
// type with no language at all.
func TestServerSelection(t *testing.T) {
	s := newServers("/w")
	for _, path := range []string{"/w/notes.txt", "/w/Makefile", "/w/a.unknown", ""} {
		ls, st := s.for_(path, nil)
		if ls != nil || st != serverNone {
			t.Errorf("%q got a server (state %d)", path, st)
		}
	}
	if _, st := s.for_("/w/a.md", nil); st != serverNone {
		t.Error("markdown has a language id but no configured server")
	}
}

// Stopping is safe with nothing started, and safe twice.
func TestStopAllIsSafe(t *testing.T) {
	s := newServers("/w")
	s.stopAll()
	s.stopAll()
}

// Each reason for having no server needs a different reaction from the user —
// install something, wait, look at why it keeps dying, or accept that this
// language has none. One message for all four told nobody anything, and on a
// Go file it said the file type was unsupported while the server was starting.
func TestServerStateMessagesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range []serverState{serverStarting, serverMissing, serverGaveUp, serverNone} {
		msg := st.message("/w/a.go")
		if msg == "" {
			t.Errorf("state %d has no message", st)
		}
		if seen[msg] {
			t.Errorf("state %d repeats the message %q", st, msg)
		}
		seen[msg] = true
	}
	if got := serverReady.message("/w/a.go"); got != "" {
		t.Errorf("a ready server says %q, want nothing", got)
	}
}

// A missing binary says which one, so the fix is obvious rather than a guess.
func TestMissingBinaryIsNamed(t *testing.T) {
	if got := serverMissing.message("/w/a.go"); !strings.Contains(got, "gopls") {
		t.Errorf("message = %q, want it to name gopls", got)
	}
	if got := serverMissing.message("/w/a.rs"); !strings.Contains(got, "rust-analyzer") {
		t.Errorf("message = %q, want it to name rust-analyzer", got)
	}
}

// A Go file must never be told its type is unsupported. That was the reported
// symptom: the message said "no language server for this file" on a .go file,
// which is the one thing that was not true.
func TestGoFileIsNeverCalledUnsupported(t *testing.T) {
	s := newServers("/w")
	_, st := s.for_("/w/main.go", nil)
	if st == serverNone {
		t.Fatal("a Go file was reported as having no server configured")
	}
	if msg := st.message("/w/main.go"); strings.Contains(msg, "file type") {
		t.Errorf("message = %q; Go is a supported file type", msg)
	}
}

// Every path handed to LSP is absolute. A relative one produces
// file://internal/editor/actions.go — a URI whose host is "internal" and which
// names nothing the server can open.
func TestDocumentPathsAreAbsolute(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()

	if got := h.docPath(h.Pane()); !filepath.IsAbs(got) {
		t.Errorf("docPath = %q, want an absolute path", got)
	}
	if got := lsp.URI(h.docPath(h.Pane())); !strings.HasPrefix(got, "file:///") {
		t.Errorf("URI = %q, want three slashes — two means the next segment is a host", got)
	}
}

// A pane with no path on disk has nothing to sync, and must not be made into a
// URI relative to the workspace.
func TestUnnamedBufferHasNoDocumentPath(t *testing.T) {
	h := newHarness(t, "text\n")
	h.Pane().File.Path = ""
	if got := h.docPath(h.Pane()); got != "" {
		t.Errorf("docPath = %q, want empty for an unnamed buffer", got)
	}
	if got := h.docPath(nil); got != "" {
		t.Errorf("docPath(nil) = %q", got)
	}
}

// parkAnswer delivers a completion answer as a server would, anchored to the
// word the popup is currently showing.
//
// The anchor is part of an answer now: a list describes one word at one place,
// and "hand" on line 2 is not the same question as "hand" on line 40. Tests go
// through here so a fake answer carries what a real one does.
func parkAnswer(h *harness, prefix string, items []lsp.CompletionItem) {
	line, col := h.Complete.Anchor()
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: prefix,
		items: items, line: line, col: col,
	})
	h.applyAnswer()
}

// Buffer words show instantly and the server's answer replaces them. A
// completion list that appears a beat after you stop typing feels broken even
// when it is better, so the fast answer goes up first.
func TestLSPCompletionReplacesBufferWords(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	if !h.Complete.Open {
		t.Fatal("no buffer-word popup")
	}
	if c, _ := h.Complete.Selected(); c.Word != "handoff" {
		t.Fatalf("buffer words showed %q", c.Word)
	}

	h.completeGen++
	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest", Detail: "func()"},
	})

	c, ok := h.Complete.Selected()
	if !ok || c.Word != "handleRequest" {
		t.Errorf("selected %q, want the server's answer", c.Word)
	}
	if c.Detail != "func()" {
		t.Errorf("detail = %q, want the type from the server", c.Detail)
	}
}

// An answer for a prefix the typing has moved past is dropped: showing it would
// suggest completions for a word that is no longer being typed.
func TestStaleCompletionIsDropped(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "zzz",
		items: []lsp.CompletionItem{{Label: "wrong", Insert: "wrong"}},
	})
	h.applyAnswer()

	if c, _ := h.Complete.Selected(); c.Word == "wrong" {
		t.Error("a completion for a different prefix was shown")
	}
}

// A server answer that filters down to nothing leaves the buffer words up:
// something usually right beats an empty list.
func TestEmptyServerAnswerKeepsBufferWords(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	before, _ := h.Complete.Selected()

	parkAnswer(h, "hand", []lsp.CompletionItem{{Label: "nomatch", Insert: "nomatch"}})

	if c, _ := h.Complete.Selected(); c.Word != before.Word {
		t.Errorf("selected %q, want the buffer words left alone", c.Word)
	}
}

// The server's ordering is kept rather than re-ranked. It encodes scope and
// type compatibility, which is the reason to ask a server at all.
func TestServerOrderingSurvivesToThePopup(t *testing.T) {
	// The buffer needs a word that is a real completion of the prefix, or no
	// popup opens for the server's answer to replace.
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handZebra", Insert: "handZebra"},
		{Label: "handApple", Insert: "handApple"},
	})

	// Neither has a sort key, so the label orders — but the point is that the
	// popup shows what the lsp package ordered rather than re-sorting by
	// length or locality the way buffer words are ranked.
	if c, _ := h.Complete.Selected(); c.Word != "handApple" {
		t.Errorf("first candidate %q, want the lsp ordering", c.Word)
	}
}

// A completion answer arriving with the popup closed must not reopen it.
func TestCompletionDoesNotReopenAClosedPopup(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "",
		items: []lsp.CompletionItem{{Label: "surprise", Insert: "surprise"}},
	})
	h.applyAnswer()
	if h.Complete.Open {
		t.Error("a late answer reopened the popup")
	}
}

// The journal window becomes the batch a sync sends: one edit per op, in
// application order, with the inserted text read back out of the stores.
// Offsets here come from the document, never a hand count — the UTF-16
// conversion downstream stands on them being right.
func TestEditsSince(t *testing.T) {
	sess := piecetable.NewSession(piecetable.NewDoc("one\ntwo\n", 8))
	v0 := sess.Version()

	two := strings.Index("one\ntwo\n", "two")
	sess.ApplyDiff(piecetable.User, sess.Version(), []piecetable.Hunk{
		{Start: two, End: two + len("two"), Text: "TWO"},
	})
	v1 := sess.Version()

	edits := editsSince(sess, v0)
	if len(edits) != 1 {
		t.Fatalf("edits = %v, want the one op", edits)
	}
	want := lsp.Edit{Start: two, End: two + len("two"), Text: "TWO"}
	if edits[0] != want {
		t.Errorf("edit = %+v, want %+v", edits[0], want)
	}

	// The window pins to the version: asking from the present yields nothing.
	if got := editsSince(sess, v1); got != nil {
		t.Errorf("editsSince(present) = %v, want nil — nothing since", got)
	}

	// Undo is an op too, and it restores what the edit removed: the reversal
	// deletes "TWO" and re-inserts the "two" still held in the store.
	if !sess.Undo(piecetable.User) {
		t.Fatal("undo refused its only op")
	}
	edits = editsSince(sess, v1)
	if len(edits) != 1 {
		t.Fatalf("edits after undo = %v, want the reversal", edits)
	}
	want = lsp.Edit{Start: two, End: two + len("TWO"), Text: "two"}
	if edits[0] != want {
		t.Errorf("undo edit = %+v, want %+v", edits[0], want)
	}
}

// A window of several ops stays in order, and an insertion's text reads back
// byte for byte — the multibyte case, since the ranges convert to UTF-16
// against these exact offsets.
func TestEditsSincePreservesOrderAndText(t *testing.T) {
	const doc = "a λ日 😀\n"
	sess := piecetable.NewSession(piecetable.NewDoc(doc, 8))
	v0 := sess.Version()

	at := strings.Index(doc, "λ")
	sess.Insert(piecetable.User, at, "→")
	sess.Delete(piecetable.User, 0, 1)

	edits := editsSince(sess, v0)
	if len(edits) != 2 {
		t.Fatalf("edits = %v, want insert then delete", edits)
	}
	if want := (lsp.Edit{Start: at, End: at, Text: "→"}); edits[0] != want {
		t.Errorf("insert = %+v, want %+v", edits[0], want)
	}
	if want := (lsp.Edit{Start: 0, End: 1, Text: ""}); edits[1] != want {
		t.Errorf("delete = %+v, want %+v", edits[1], want)
	}
}
