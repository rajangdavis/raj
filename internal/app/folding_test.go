package app

import (
	"encoding/json"
	"strings"
	"testing"

	"raj/internal/lsp"
)

// installFolding parks and applies a folding-range answer as a server would, so
// a test can get ranges onto the active pane without a real server process.
func (h *harness) installFolding(ranges ...lsp.FoldingRange) {
	h.servers.foldGen++
	h.park(lspAnswer{
		gen:         h.servers.foldGen,
		kind:        answerFolding,
		path:        h.docPath(h.Pane()),
		foldVersion: int(h.Pane().File.Session().Version()),
		folds:       ranges,
	})
	h.applyAnswer()
}

// The guard keeps the idle tick from asking the server the same question every
// 150 ms: only a moved path or version makes a new request worth making.
func TestFoldWantedGuard(t *testing.T) {
	if !foldWanted(foldRequest{}, "/w/a.go", 1) {
		t.Fatal("a fresh guard did not want the ranges")
	}
	last := foldRequest{path: "/w/a.go", version: 1}
	if foldWanted(last, "/w/a.go", 1) {
		t.Error("the same path and version were requested again")
	}
	if !foldWanted(last, "/w/a.go", 2) {
		t.Error("a moved version did not re-request")
	}
	if !foldWanted(last, "/w/b.go", 1) {
		t.Error("a different path did not re-request")
	}
}

// The feature never starts a server: with nothing live the guard is left
// unset so the next tick tries again, rather than spawning a process for a
// decoration. Without the live lookup this would call for_ and start one.
func TestMaybeRequestFoldingNeverStartsAServer(t *testing.T) {
	h := newHarness(t, "a\nb\nc\n")
	h.maybeRequestFolding(h.Pane())
	if h.servers.foldReq != (foldRequest{}) {
		t.Errorf("recorded %+v with no live server", h.servers.foldReq)
	}
	// A nil pane clears the guard rather than asking about nothing.
	h.servers.foldReq = foldRequest{path: "/w/a.go", version: 1}
	h.maybeRequestFolding(nil)
	if h.servers.foldReq != (foldRequest{}) {
		t.Errorf("a nil pane kept the guard %+v", h.servers.foldReq)
	}
}

// The gate reads FoldingRangeProvider in the shared "does not support" voice,
// and a present-and-false provider means no. Without this read the feature
// would ask a method the server answers method-not-found.
func TestFoldingCapabilityGate(t *testing.T) {
	const want = "language server does not support folding ranges"
	if got := foldingGap(lsp.ServerCapabilities{}); got != want {
		t.Errorf("absent provider: %q", got)
	}
	if got := foldingGap(lsp.ServerCapabilities{FoldingRangeProvider: json.RawMessage(`false`)}); got != want {
		t.Errorf("false provider: %q", got)
	}
	advertised := lsp.ServerCapabilities{FoldingRangeProvider: json.RawMessage(`true`)}
	if got := foldingGap(advertised); got != "" {
		t.Errorf("advertised provider: %q, want none", got)
	}
}

// editorFolds turns the server's lines into whole-row byte folds: the header
// line stays visible and the body from the next line through the end line is
// hidden. A range with no body, or one past the document, is dropped rather
// than folding a line that is not there.
func TestEditorFoldsConvertsLines(t *testing.T) {
	h := newHarness(t, "a\nb\nc\nd\ne\n")
	f := h.Pane().File
	folds := editorFolds(f, []lsp.FoldingRange{{StartLine: 1, EndLine: 3}})
	if len(folds) != 1 {
		t.Fatalf("got %d folds, want 1", len(folds))
	}
	got := folds[0]
	if got.StartLine != 1 || got.EndLine != 3 {
		t.Errorf("lines = %d..%d, want 1..3", got.StartLine, got.EndLine)
	}
	if got.Lo != f.LineStart(2) || got.Hi != f.LineStart(4) {
		t.Errorf("bytes = [%d,%d), want [%d,%d)", got.Lo, got.Hi, f.LineStart(2), f.LineStart(4))
	}
	// A one-line range has no body; an end past the document clamps back onto
	// its own start. Both are dropped.
	dropped := editorFolds(f, []lsp.FoldingRange{{StartLine: 0, EndLine: 0}, {StartLine: 4, EndLine: 9}})
	if len(dropped) != 0 {
		t.Errorf("got %d folds for bodyless ranges, want none", len(dropped))
	}
	if got := editorFolds(f, nil); got != nil {
		t.Errorf("editorFolds(nil) = %+v, want nil", got)
	}
}

// An answer installs the ranges on the pane, and the toggle chord then closes
// the range at the caret so the display hides its body. Without the install the
// decoded ranges never reach a pane and nothing folds.
func TestFoldingAnswerInstallsAndToggles(t *testing.T) {
	h := newHarness(t, "a\nb\nc\nd\ne\n")
	h.installFolding(lsp.FoldingRange{StartLine: 1, EndLine: 3})
	if got := h.Pane().FoldCount(); got != 1 {
		t.Fatalf("FoldCount = %d, want the installed range", got)
	}
	// Caret on the header line (1-based line 2) closes the fold.
	h.jumpTo(2)
	h.press("ctrl+super+c")
	if !strings.Contains(h.Status(), "folded") {
		t.Errorf("status = %q, want the folded note", h.Status())
	}
	if got := h.Pane().DisplayLines(); got != 5 {
		t.Errorf("DisplayLines = %d, want 5 after folding", got)
	}
	// The same chord opens it again.
	h.press("ctrl+super+c")
	if !strings.Contains(h.Status(), "unfolded") {
		t.Errorf("status = %q, want the unfolded note", h.Status())
	}
	if got := h.Pane().DisplayLines(); got != 6 {
		t.Errorf("DisplayLines = %d, want 6 after unfolding", got)
	}
}

// An answer for a version the buffer has left is dropped rather than folded
// onto lines the edit moved. Without the version check the stale ranges would
// hide the wrong region.
func TestStaleFoldingAnswerIsDropped(t *testing.T) {
	h := newHarness(t, "a\nb\nc\nd\ne\n")
	h.servers.foldGen++
	h.park(lspAnswer{
		gen:         h.servers.foldGen,
		kind:        answerFolding,
		path:        h.docPath(h.Pane()),
		foldVersion: int(h.Pane().File.Session().Version()) + 1,
		folds:       []lsp.FoldingRange{{StartLine: 1, EndLine: 3}},
	})
	h.applyAnswer()
	if got := h.Pane().FoldCount(); got != 0 {
		t.Errorf("FoldCount = %d, want a stale answer dropped", got)
	}
}

// The toggle with no range under the caret says so rather than doing nothing
// silently, which is the difference between "no fold here" and a dead chord.
func TestToggleFoldNamesTheMiss(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.toggleFold()
	if !strings.Contains(h.Status(), "no fold at the cursor") {
		t.Errorf("status = %q, want the miss", h.Status())
	}
}
