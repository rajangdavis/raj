package app

import (
	"encoding/json"
	"testing"

	"raj/internal/editor"
	"raj/internal/lsp"
)

// dhl is a terse fixture: one document highlight over a line/character range.
func dhl(sl, sc, el, ec int, kind lsp.DocumentHighlightKind) lsp.DocumentHighlight {
	return lsp.DocumentHighlight{
		Range: lsp.Range{Start: lsp.Position{Line: sl, Character: sc}, End: lsp.Position{Line: el, Character: ec}},
		Kind:  kind,
	}
}

// installHighlight parks and applies a highlight answer as a server would, so a
// test can get an overlay onto the active pane without a real server process.
func (h *harness) installHighlight(version, caret int, hs ...lsp.DocumentHighlight) {
	h.servers.highlightGen++
	h.park(lspAnswer{
		gen:              h.servers.highlightGen,
		kind:             answerHighlight,
		path:             h.docPath(h.Pane()),
		highlightVersion: version,
		highlightCaret:   caret,
		highlight:        hs,
	})
	h.applyAnswer()
}

// The gate reads DocumentHighlightProvider in the shared "does not support"
// voice, and a present-and-false provider means no. Without this read the idle
// tick would ask a method the server answers method-not-found.
func TestHighlightCapabilityGate(t *testing.T) {
	const want = "language server does not support document highlights"
	if got := highlightGap(lsp.ServerCapabilities{}); got != want {
		t.Errorf("absent provider: %q", got)
	}
	if got := highlightGap(lsp.ServerCapabilities{DocumentHighlightProvider: json.RawMessage(`false`)}); got != want {
		t.Errorf("false provider: %q", got)
	}
	if got := highlightGap(lsp.ServerCapabilities{DocumentHighlightProvider: json.RawMessage(`{}`)}); got != "" {
		t.Errorf("advertised provider: %q, want none", got)
	}
}

// The memo key is (path, version, caret): a caret move re-requests even though
// the text has not moved, and a still caret does not. Without the caret in the
// key the highlight would follow an edit but never the cursor.
func TestHighlightWantedGuard(t *testing.T) {
	last := highlightReq{path: "/w/a.go", version: 1, caret: 5}
	if highlightWanted(last, "/w/a.go", 1, 5) {
		t.Error("the same path, version and caret were requested again")
	}
	if !highlightWanted(last, "/w/a.go", 1, 6) {
		t.Error("a moved caret did not re-request")
	}
	if !highlightWanted(last, "/w/a.go", 2, 5) {
		t.Error("a moved version did not re-request")
	}
	if !highlightWanted(last, "/w/b.go", 1, 5) {
		t.Error("a different path did not re-request")
	}
}

// The conversion maps LSP ranges to line-relative runs and carries the write
// flag: without the kind mapping a write would paint like a read, and without
// the line mapping the renderer would have nothing to consult.
func TestHighlightRunsConvert(t *testing.T) {
	doc := lsp.NewDocument("aa bb\ncc\n")
	got := highlightRuns(doc, []lsp.DocumentHighlight{
		dhl(0, 0, 0, 2, lsp.HighlightRead),
		dhl(0, 3, 0, 5, lsp.HighlightWrite),
	})
	runs := got[0]
	if len(runs) != 2 {
		t.Fatalf("line 0 runs = %+v, want two", runs)
	}
	if runs[0] != (editor.HighlightRun{Start: 0, End: 2}) {
		t.Errorf("first run = %+v, want 0..2 read", runs[0])
	}
	if runs[1] != (editor.HighlightRun{Start: 3, End: 5, Write: true}) {
		t.Errorf("second run = %+v, want 3..5 write", runs[1])
	}
}

// A range that crosses a line is split at the boundaries rather than dropped,
// because a server may report one occurrence spanning the newline and losing it
// would look like the server never answered.
func TestHighlightRunsSplitAcrossLines(t *testing.T) {
	doc := lsp.NewDocument("ab\ncd\n")
	got := highlightRuns(doc, []lsp.DocumentHighlight{dhl(0, 1, 1, 1, lsp.HighlightText)})
	if len(got[0]) != 1 || got[0][0] != (editor.HighlightRun{Start: 1, End: 3}) {
		t.Errorf("line 0 = %+v, want one run 1..3 through the newline", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != (editor.HighlightRun{Start: 0, End: 1}) {
		t.Errorf("line 1 = %+v, want one run 0..1", got[1])
	}
}

// The install pins the answer to the caret and version it was requested at. A
// stale answer for older text must not paint, and neither must one for a caret
// the user has left: both are an emphasis on the wrong bytes.
func TestStaleHighlightAnswerIsDropped(t *testing.T) {
	h := newHarness(t, "hello world\n")
	version := int(h.Pane().File.Session().Version())
	caret := h.Pane().Cursors.Primary().Head

	h.installHighlight(version+1, caret, dhl(0, 0, 0, 5, lsp.HighlightRead))
	if h.Pane().File.Highlight != nil {
		t.Errorf("an answer for older text was installed: %+v", h.Pane().File.Highlight.At(0))
	}

	h.installHighlight(version, caret+1, dhl(0, 0, 0, 5, lsp.HighlightRead))
	if h.Pane().File.Highlight != nil {
		t.Errorf("an answer for a caret the user left was installed: %+v", h.Pane().File.Highlight.At(0))
	}
}

// A live answer installs an overlay whose pin matches, and an edit moves the
// version so the same frame drops it. Without the version pin the emphasis
// would survive on moved bytes until the next server answer.
func TestHighlightInstallAndEditInvalidates(t *testing.T) {
	h := newHarness(t, "hello world\n")
	version := int(h.Pane().File.Session().Version())
	caret := h.Pane().Cursors.Primary().Head

	h.installHighlight(version, caret, dhl(0, 0, 0, 5, lsp.HighlightRead))
	if h.Pane().File.Highlight == nil {
		t.Fatal("the answer was not installed")
	}
	if !h.Pane().File.Highlight.Live(version, caret) {
		t.Error("the installed set is not live for its own version and caret")
	}

	h.typeText("x")
	if h.Pane().File.Highlight.Live(int(h.Pane().File.Session().Version()), caret) {
		t.Error("the overlay is still live after an edit moved the text")
	}
}

// The server's "no occurrences" answer clears the overlay rather than leaving
// the previous one up: a nil set is an answer, not silence.
func TestHighlightEmptyAnswerClearsOverlay(t *testing.T) {
	h := newHarness(t, "hello world\n")
	version := int(h.Pane().File.Session().Version())
	caret := h.Pane().Cursors.Primary().Head

	h.installHighlight(version, caret, dhl(0, 0, 0, 5, lsp.HighlightRead))
	if h.Pane().File.Highlight == nil {
		t.Fatal("setup: nothing installed")
	}
	h.installHighlight(version, caret)
	if h.Pane().File.Highlight != nil {
		t.Errorf("an empty answer left the overlay up: %+v", h.Pane().File.Highlight.At(0))
	}
}
