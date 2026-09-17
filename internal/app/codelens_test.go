package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"raj/internal/lsp"
)

// cl is a terse fixture: a lens on a line with a runnable title.
func cl(line int, title string) lsp.CodeLens {
	return lsp.CodeLens{
		Range:   lsp.Range{Start: lsp.Position{Line: line}, End: lsp.Position{Line: line, Character: 1}},
		Command: &lsp.Command{Title: title, Command: "raj.test"},
	}
}

// An answer round-trips with the version it describes, which is what lets the
// install path drop a lens for text the buffer has left.
func TestLensStoreRoundTrips(t *testing.T) {
	s := newLensStore()
	s.set("/w/a.go", 7, []lsp.CodeLens{cl(1, "A")})
	got, version, ok := s.forPath("/w/a.go")
	if !ok {
		t.Fatal("forPath did not find the stored answer")
	}
	if version != 7 || len(got) != 1 || got[0].Command.Title != "A" {
		t.Fatalf("forPath = %+v version %d", got, version)
	}
	if _, _, ok := s.forPath("/w/b.go"); ok {
		t.Error("an untouched path reported an answer")
	}
	s.clear("/w/a.go")
	if _, _, ok := s.forPath("/w/a.go"); ok {
		t.Error("a cleared path still reports an answer")
	}
}

// An empty answer is stored, not dropped: the version still says which text it
// describes, so "no lenses" is distinct from "never asked".
func TestLensStoreKeepsEmptyAnswer(t *testing.T) {
	s := newLensStore()
	s.set("/w/a.go", 3, nil)
	got, version, ok := s.forPath("/w/a.go")
	if !ok || version != 3 || len(got) != 0 {
		t.Fatalf("empty answer = %+v version %d ok %v", got, version, ok)
	}
}

// Several lenses on one line become one inline run anchored at offset zero, so
// the shared column map places it before the line's first byte. If the anchors
// were the lens ranges' columns instead, the run would land mid-line and the
// caret maths would not match the renderer.
func TestLensLineHintsGroupsAndAnchors(t *testing.T) {
	hints := lensLineHints([]lsp.CodeLens{cl(2, "A"), cl(2, "B"), cl(5, "C")})
	if len(hints) != 2 {
		t.Fatalf("hints = %+v, want two lines", hints)
	}
	for _, lh := range hints {
		if !lh.Hint.Lens {
			t.Errorf("line %d was not marked as a lens", lh.Line)
		}
		if lh.Hint.Off != 0 {
			t.Errorf("line %d anchored at %d, want offset zero", lh.Line, lh.Hint.Off)
		}
	}
	if hints[0].Line != 2 || !strings.Contains(hints[0].Hint.Text, "A") || !strings.Contains(hints[0].Hint.Text, "B") {
		t.Errorf("line 2 run = %+v, want A and B joined", hints[0].Hint)
	}
	if hints[1].Line != 5 || hints[1].Hint.Text != "C" {
		t.Errorf("line 5 run = %+v, want C", hints[1].Hint)
	}
}

// A data-only lens is drawn as a placeholder instead of vanishing: the resolve
// that would name it has not happened yet, and hiding the line would leave the
// user with nothing to run. A lens with neither a command nor data is still
// skipped, because there is nothing to resolve and nothing to name.
func TestLensLineHintsPlaceholderForUnresolved(t *testing.T) {
	hints := lensLineHints([]lsp.CodeLens{{
		Range: lsp.Range{Start: lsp.Position{Line: 1}},
		Data:  json.RawMessage(`{"token":"x"}`),
	}})
	if len(hints) != 1 || hints[0].Line != 1 {
		t.Fatalf("hints = %+v, want one placeholder on line 1", hints)
	}
	if !strings.Contains(hints[0].Hint.Text, "code lens") {
		t.Errorf("placeholder = %q, want it to name the lens", hints[0].Hint.Text)
	}
	if got := lensLineHints([]lsp.CodeLens{{Range: lsp.Range{Start: lsp.Position{Line: 2}}}}); len(got) != 0 {
		t.Errorf("hints = %+v, want none for a lens with nothing to resolve", got)
	}
}

// Running a lens means the one on the caret's line, first in server order. The
// rule is pure so it is pinned without a server.
func TestLensAtLinePicksTheFirst(t *testing.T) {
	lenses := []lsp.CodeLens{cl(3, "A"), cl(3, "B"), cl(9, "C")}
	got, ok := lensAtLine(lenses, 3)
	if !ok || got.Command.Title != "A" {
		t.Errorf("lensAtLine(3) = %+v ok %v, want A", got, ok)
	}
	if _, ok := lensAtLine(lenses, 4); ok {
		t.Error("a line with no lens reported one")
	}
}

func (h *harness) lensVersion() int { return int(h.Pane().File.Session().Version()) }

// installLens parks and applies a lens answer as a server would, so a test can
// get lenses onto the active pane without a real server process.
func (h *harness) installLens(lenses ...lsp.CodeLens) {
	h.lensGen++
	h.park(lspAnswer{
		gen:         h.lensGen,
		kind:        answerCodeLens,
		path:        h.docPath(h.Pane()),
		lensVersion: h.lensVersion(),
		lenses:      lenses,
	})
	h.applyAnswer()
}

// A lens answer installs inline text at the start of its line, and the frame
// draws it. Without the install the answer decodes and never reaches the
// screen, which is the whole feature.
func TestLensAppearsAtItsLine(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Draw()
	h.installLens(cl(2, "1 reference"))

	got := h.Pane().File.HintsAt(2)
	if len(got) != 1 || !got[0].Lens || got[0].Text != "1 reference" {
		t.Fatalf("line 2 inline runs = %+v, want the lens", got)
	}
	if got := h.Pane().File.HintsAt(0); got != nil {
		t.Errorf("line 0 gained an inline run: %+v", got)
	}
	// Anchored before the line's first byte: the caret at the line start sits
	// in front of the lens, and a click inside it clamps to the line start
	// rather than to a byte that is not there.
	if _, col := h.Pane().File.LineCol(h.Pane().File.LineStart(2)); col != 0 {
		t.Errorf("column at offset zero = %d, want 0 (before the lens)", col)
	}
	if off := h.Pane().File.OffsetAt(2, 3); off != h.Pane().File.LineStart(2) {
		t.Errorf("a column inside the lens resolved to %d, want the line start", off)
	}

	h.Draw()
	if !strings.Contains(h.host.Text(), "1 reference") {
		t.Errorf("the lens was not rendered:\n%s", h.host.Text())
	}
}

// A lens anchored to a version the buffer has left must not be installed: its
// line has moved, so it would annotate the wrong one.
func TestStaleLensAnswerIsDropped(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Draw()
	h.lensGen++
	h.park(lspAnswer{
		gen:         h.lensGen,
		kind:        answerCodeLens,
		path:        h.docPath(h.Pane()),
		lensVersion: h.lensVersion() + 1,
		lenses:      []lsp.CodeLens{cl(2, "1 reference")},
	})
	h.applyAnswer()
	if got := h.Pane().File.HintsAt(2); got != nil {
		t.Errorf("a stale lens was installed: %+v", got)
	}
}

// An edit moves the lines the lenses were anchored to, so the next frame drops
// them before it can draw one on the wrong line.
func TestLensIsDroppedWhenTheTextMoves(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Draw()
	h.installLens(cl(2, "1 reference"))
	if h.Pane().File.Lenses == nil {
		t.Fatal("setup: the lens was not installed")
	}
	h.typeText("x")
	h.Draw()
	if got := h.Pane().File.HintsAt(2); got != nil {
		t.Errorf("a lens survived the edit: %+v", got)
	}
}

// The gate reads CodeLensProvider in the shared "does not support" voice, and
// a present-and-false provider means no. Without this read the action would ask
// a method the server answers method-not-found.
func TestCodeLensCapabilityGate(t *testing.T) {
	const want = "language server does not support code lenses"
	if got := codeLensGap(lsp.ServerCapabilities{}); got != want {
		t.Errorf("absent provider: %q", got)
	}
	if got := codeLensGap(lsp.ServerCapabilities{CodeLensProvider: json.RawMessage(`false`)}); got != want {
		t.Errorf("false provider: %q", got)
	}
	if got := codeLensGap(lsp.ServerCapabilities{CodeLensProvider: json.RawMessage(`{"resolveProvider":true}`)}); got != "" {
		t.Errorf("advertised provider: %q, want none", got)
	}
}

// The chord reaches the run path. A file with no lenses loaded says so rather
// than reporting "unhandled", which is what an unbound action would say.
func TestRunCodeLensChordIsHandled(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("ctrl+super+l")
	if strings.HasPrefix(h.Status(), "unhandled:") {
		t.Errorf("status = %q; the chord is bound but not handled", h.Status())
	}
	if h.Status() == "" {
		t.Error("running a lens with none loaded said nothing")
	}
}

// A line with no lens on it names the miss rather than running something else.
// The no-server state is deliberately kept out of the way by using a file type
// with no configured server, so this asserts the line lookup.
func TestRunCodeLensNamesTheLineMiss(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installLens(cl(2, "Run test"))
	h.Pane().Cursors.Set(0, 0)
	h.runCodeLens()
	if !strings.Contains(h.Status(), "no code lens on this line") {
		t.Errorf("status = %q, want the line miss", h.Status())
	}
}

// A lens on the caret's line with no live server refuses rather than silently
// doing nothing, and changes no text.
func TestRunCodeLensWithoutAServerRefuses(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installLens(cl(2, "Run test"))
	at := h.Pane().File.LineStart(2)
	h.Pane().Cursors.Set(at, at)
	h.runCodeLens()
	got := h.Status()
	if got == "" || strings.Contains(got, "no code lens on this line") {
		t.Errorf("status = %q, want the no-server note", got)
	}
	if h.text() != "package main\n\nfunc F() {}\n" {
		t.Error("running a lens changed the buffer")
	}
}

// stubLensWire swaps the code-lens wire for one test and restores it after. The
// app package has no fake LSP server, so this is how the fetch-versus-run
// ordering is observed without one.
func stubLensWire(t *testing.T) {
	t.Helper()
	oldServer, oldRequest, oldResolve := lensWire.server, lensWire.request, lensWire.resolve
	t.Cleanup(func() {
		lensWire.server, lensWire.request, lensWire.resolve = oldServer, oldRequest, oldResolve
	})
}

// waitAnswer waits for a background path to park an answer and applies it, so a
// test can drive a goroutine-backed path without racing the scheduler.
func (h *harness) waitAnswer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.lspMu.Lock()
		pending := h.lspAnswer != nil
		h.lspMu.Unlock()
		if pending {
			h.applyAnswer()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no answer was parked")
}

// The fetch stores a data-only lens unresolved and sends no resolve. If the
// eager resolve returned to the fetch this would send one round trip per lens,
// which is the cost laziness removes; the test fails with resolves != 0.
func TestFetchStoresLensesWithoutSendingAResolve(t *testing.T) {
	h := newHarness(t, "package main\n")
	stubLensWire(t)
	var requests, resolves int
	lensWire.request = func(context.Context, *lsp.Conn, string) ([]lsp.CodeLens, error) {
		requests++
		return []lsp.CodeLens{{
			Range: lsp.Range{Start: lsp.Position{Line: 1}},
			Data:  json.RawMessage(`{"token":"abc"}`),
		}}, nil
	}
	lensWire.resolve = func(context.Context, *lsp.Conn, lsp.CodeLens) (lsp.CodeLens, error) {
		resolves++
		return lsp.CodeLens{}, nil
	}

	h.lensGen++
	gen := h.lensGen
	h.fetchLenses(gen, h.docPath(h.Pane()), h.lensVersion(), nil)
	if requests != 1 {
		t.Fatalf("the fetch made %d lens requests, want 1", requests)
	}
	if resolves != 0 {
		t.Fatalf("the fetch sent %d resolve request(s); a resolve belongs at the run", resolves)
	}
	h.applyAnswer()

	lenses, version, ok := h.lenses.forPath(h.docPath(h.Pane()))
	if !ok || version != h.lensVersion() || len(lenses) != 1 || !lenses[0].NeedsResolve() {
		t.Fatalf("stored lenses = %+v version %d ok=%v, want the unresolved lens", lenses, version, ok)
	}
	if got := h.Pane().File.HintsAt(1); len(got) == 0 {
		t.Error("the unresolved lens was not drawn as a placeholder")
	}
}

// Running a data-only lens resolves it first and then runs the command the
// resolve returned. Without the lazy resolve the run path sees no command and
// refuses, so the resolve count stays zero and no command runs.
func TestRunResolvesThenRunsTheLens(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installLens(lsp.CodeLens{
		Range: lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 2, Character: 1}},
		Data:  json.RawMessage(`{"token":"abc"}`),
	})

	stubLensWire(t)
	ls := &langServer{srv: &lsp.Server{}, caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
		CodeLensProvider:       json.RawMessage(`{"resolveProvider":true}`),
		ExecuteCommandProvider: json.RawMessage(`{"commands":["raj.test"]}`),
	}}}
	lensWire.server = func(*App, string) (*langServer, serverState) { return ls, serverReady }
	var resolves int
	lensWire.resolve = func(_ context.Context, _ *lsp.Conn, lens lsp.CodeLens) (lsp.CodeLens, error) {
		resolves++
		return lsp.CodeLens{
			Range:   lens.Range,
			Command: &lsp.Command{Title: "Run test", Command: "raj.test"},
			Data:    lens.Data,
		}, nil
	}

	at := h.Pane().File.LineStart(2)
	h.Pane().Cursors.Set(at, at)
	h.runCodeLens()
	h.waitAnswer(t)
	if resolves != 1 {
		t.Fatalf("the run sent %d resolve request(s), want exactly 1", resolves)
	}
	if got := h.Status(); !strings.Contains(got, "ran Run test") {
		t.Fatalf("status = %q, want the resolved command run", got)
	}
	// The resolved lens is cached, so a second run does not ask again.
	h.runCodeLens()
	if resolves != 1 {
		t.Errorf("a second run sent %d resolve request(s); the cached lens was not reused", resolves)
	}
	lenses, _, _ := h.lenses.forPath("/w/notes.txt")
	if len(lenses) != 1 || lenses[0].Command == nil || lenses[0].Command.Command != "raj.test" {
		t.Errorf("stored lenses = %+v, want the resolved command cached", lenses)
	}
}

// A resolve answer measured on a version the buffer has left does not run: the
// lens's line may have moved, so the command is dropped rather than sent. This
// is the same version pin a fetch answer obeys.
func TestStaleResolveAnswerDoesNotRun(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	resolved := lsp.CodeLens{
		Range:   lsp.Range{Start: lsp.Position{Line: 2}},
		Command: &lsp.Command{Title: "Run test", Command: "raj.test"},
	}
	h.lensGen++
	h.park(lspAnswer{
		gen:         h.lensGen,
		kind:        answerCodeLens,
		path:        "/w/notes.txt",
		lensVersion: h.lensVersion() + 1,
		lenses:      []lsp.CodeLens{resolved},
		line:        2,
		text:        lensResolveRunMarker,
	})
	h.applyAnswer()
	if !strings.Contains(h.Status(), "changed before") {
		t.Errorf("status = %q, want the stale answer named", h.Status())
	}
	if strings.Contains(h.Status(), "ran ") {
		t.Errorf("status = %q; a stale resolve answer ran", h.Status())
	}
}

// The lazy resolve budget bounds one fetched answer: a server that answers
// every resolve with fresh data can be asked at most maxLensResolves times, a
// moved version claims nothing, and a fresh answer resets the count. Without
// the bound a pathological answer could spin requests forever.
func TestLensResolveBudgetBoundsTheStore(t *testing.T) {
	s := newLensStore()
	lens := []lsp.CodeLens{{
		Range: lsp.Range{Start: lsp.Position{Line: 1}},
		Data:  json.RawMessage(`{"token":"x"}`),
	}}
	s.set("/w/a.go", 7, lens)
	for i := 0; i < maxLensResolves; i++ {
		if !s.claimResolve("/w/a.go", 7) {
			t.Fatalf("claim %d refused before the bound", i)
		}
	}
	if s.claimResolve("/w/a.go", 7) {
		t.Error("a claim past the bound was allowed")
	}
	if s.claimResolve("/w/a.go", 8) {
		t.Error("a claim for a version the store does not hold was allowed")
	}
	s.set("/w/a.go", 7, lens)
	if s.claimResolve("/w/a.go", 7) {
		t.Error("reinstalling the same version reset the spent budget")
	}
	s.set("/w/a.go", 8, lens)
	if !s.claimResolve("/w/a.go", 8) {
		t.Error("a fresh version did not get a fresh budget")
	}
}

// A server that offers lenses but not the resolve half is refused before the
// request: a data-only lens cannot be completed, and sending a method the
// server never advertised would be a method-not-found rather than a feature.
func TestRunRefusesResolveItDoesNotSupport(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installLens(lsp.CodeLens{
		Range: lsp.Range{Start: lsp.Position{Line: 2}},
		Data:  json.RawMessage(`{"token":"abc"}`),
	})
	stubLensWire(t)
	ls := &langServer{srv: &lsp.Server{}, caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
		CodeLensProvider: json.RawMessage(`true`),
	}}}
	lensWire.server = func(*App, string) (*langServer, serverState) { return ls, serverReady }
	var resolves int
	lensWire.resolve = func(context.Context, *lsp.Conn, lsp.CodeLens) (lsp.CodeLens, error) {
		resolves++
		return lsp.CodeLens{}, nil
	}
	at := h.Pane().File.LineStart(2)
	h.Pane().Cursors.Set(at, at)
	h.runCodeLens()
	if resolves != 0 {
		t.Errorf("a resolve was sent to a server that does not support it")
	}
	if !strings.Contains(h.Status(), "does not support") {
		t.Errorf("status = %q, want the capability refusal", h.Status())
	}
}

// A server that resolves lenses but does not accept commands is refused after
// the capability gate but before the round trip: the resolved command could
// never run, so asking for it would spend a request for nothing.
func TestRunRefusesWhenExecuteCommandIsNotSupported(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F() {}\n")
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installLens(lsp.CodeLens{
		Range: lsp.Range{Start: lsp.Position{Line: 2}},
		Data:  json.RawMessage(`{"token":"abc"}`),
	})
	stubLensWire(t)
	ls := &langServer{srv: &lsp.Server{}, caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
		CodeLensProvider: json.RawMessage(`{"resolveProvider":true}`),
	}}}
	lensWire.server = func(*App, string) (*langServer, serverState) { return ls, serverReady }
	var resolves int
	lensWire.resolve = func(context.Context, *lsp.Conn, lsp.CodeLens) (lsp.CodeLens, error) {
		resolves++
		return lsp.CodeLens{}, nil
	}
	at := h.Pane().File.LineStart(2)
	h.Pane().Cursors.Set(at, at)
	h.runCodeLens()
	if resolves != 0 {
		t.Errorf("a resolve was sent for a command that cannot run")
	}
	if !strings.Contains(h.Status(), "execute command") {
		t.Errorf("status = %q, want the executeCommand refusal", h.Status())
	}
}
