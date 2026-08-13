package app

import (
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/lsp"
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

	if strings.Contains(h.Status(), "stale") {
		t.Errorf("status = %q; a superseded answer was shown", h.Status())
	}

	h.park(lspAnswer{gen: 5, kind: answerHover, text: "current"})
	h.applyAnswer()
	if !strings.Contains(h.Status(), "current") {
		t.Errorf("status = %q, want the current answer", h.Status())
	}
}

// A hover with nothing in it clears rather than reporting a failure: most
// positions in most files have nothing to say about them.
func TestEmptyHoverIsNotAnError(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: ""})
	h.applyAnswer()
	if h.Status() != "" {
		t.Errorf("status = %q, want it left clear", h.Status())
	}
}

// A multi-line hover is folded onto the status line rather than truncated at
// the first newline, which would hide the signature under its doc comment.
func TestMultiLineHoverIsFolded(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F(x int) error\n\nDoes a thing."})
	h.applyAnswer()

	if strings.Contains(h.Status(), "\n") {
		t.Error("a newline reached the status line")
	}
	for _, want := range []string{"func F(x int) error", "Does a thing."} {
		if !strings.Contains(h.Status(), want) {
			t.Errorf("status = %q, missing %q", h.Status(), want)
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
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "hand",
		items: []lsp.CompletionItem{
			{Label: "handleRequest", Insert: "handleRequest", Detail: "func()"},
		},
	})
	h.applyAnswer()

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

	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "hand",
		items: []lsp.CompletionItem{{Label: "nomatch", Insert: "nomatch"}},
	})
	h.applyAnswer()

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

	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "hand",
		items: []lsp.CompletionItem{
			{Label: "handZebra", Insert: "handZebra"},
			{Label: "handApple", Insert: "handApple"},
		},
	})
	h.applyAnswer()

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
