package app

import (
	"reflect"
	"testing"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/ui"
)

// ih is a terse fixture: a hint at a line-relative offset.
func ih(off int, text string) editor.LineHint {
	return editor.LineHint{Hint: editor.Hint{Off: off, Text: text}}
}

// ihl is ih on a specific line, for where the line itself is the point.
func ihl(line, off int, text string) editor.LineHint {
	return editor.LineHint{Line: line, Hint: editor.Hint{Off: off, Text: text}}
}

// An answer round-trips with the version it describes, which is what lets the
// apply path drop an answer for text the buffer has left.
func TestInlaySetAndForPath(t *testing.T) {
	s := newInlayStore()
	s.set("/w/a.go", 7, []editor.LineHint{ih(1, "x")})

	got, version, ok := s.forPath("/w/a.go")
	if !ok {
		t.Fatal("forPath did not find the stored answer")
	}
	if version != 7 {
		t.Errorf("version = %d, want 7", version)
	}
	if len(got) != 1 || got[0].Hint.Text != "x" {
		t.Errorf("hints = %v, want the stored hint", got)
	}
}

// A second answer for the same path replaces the first: each answer is the
// complete set for one version, not an increment.
func TestInlaySetReplaces(t *testing.T) {
	s := newInlayStore()
	s.set("/w/a.go", 1, []editor.LineHint{ih(1, "old")})
	s.set("/w/a.go", 2, []editor.LineHint{ih(9, "new")})

	got, version, _ := s.forPath("/w/a.go")
	if version != 2 {
		t.Errorf("version = %d, want 2", version)
	}
	if len(got) != 1 || got[0].Hint.Text != "new" {
		t.Errorf("hints = %v, want only the newest answer", got)
	}
}

// Files are independent: an answer for one must not disturb another.
func TestInlayPathsAreIndependent(t *testing.T) {
	s := newInlayStore()
	s.set("/w/a.go", 1, []editor.LineHint{ih(0, "a")})
	s.set("/w/b.go", 2, []editor.LineHint{ih(0, "b")})
	s.clear("/w/a.go")

	if _, _, ok := s.forPath("/w/a.go"); ok {
		t.Error("a cleared path still reports an answer")
	}
	got, version, ok := s.forPath("/w/b.go")
	if !ok || version != 2 || len(got) != 1 || got[0].Hint.Text != "b" {
		t.Errorf("clearing one path disturbed another: %v %d %v", got, version, ok)
	}
}

// An unknown path has no answer, which is distinct from an answer of none.
func TestInlayForPathUnknown(t *testing.T) {
	s := newInlayStore()
	if _, _, ok := s.forPath("/w/never.go"); ok {
		t.Error("an untouched path reported an answer")
	}
}

// An empty answer is stored, not dropped: the version still says which text it
// describes, so a later answer for the same version can replace it. A nil slice
// reads back as no hints.
func TestInlayEmptyAnswerIsStored(t *testing.T) {
	s := newInlayStore()
	s.set("/w/a.go", 3, nil)

	got, version, ok := s.forPath("/w/a.go")
	if !ok {
		t.Fatal("an empty answer was dropped rather than stored")
	}
	if version != 3 {
		t.Errorf("version = %d, want 3", version)
	}
	if len(got) != 0 {
		t.Errorf("hints = %v, want none", got)
	}
}

// A store built by hand with no byPath — the shape newInlayStore is the real
// constructor for — still answers, records and clears without panicking, so the
// nil-map guard in set is exercised rather than assumed.
func TestInlayHandBuiltStore(t *testing.T) {
	s := &inlayStore{}
	if _, _, ok := s.forPath("/w/a.go"); ok {
		t.Error("a hand-built store claimed an answer it never saw")
	}
	s.set("/w/a.go", 1, nil)
	if _, _, ok := s.forPath("/w/a.go"); !ok {
		t.Error("a hand-built store did not record the answer")
	}
	s.clear("/w/a.go")
	if _, _, ok := s.forPath("/w/a.go"); ok {
		t.Error("a hand-built store did not clear")
	}
}

// The line is carried with the hint. A Hint's Off is line-relative, so a store
// that kept only []editor.Hint would lose which line each belongs on.
func TestInlayStoreKeepsLine(t *testing.T) {
	s := newInlayStore()
	s.set("/w/a.go", 1, []editor.LineHint{ihl(7, 3, "x")})

	got, _, ok := s.forPath("/w/a.go")
	if !ok || len(got) != 1 {
		t.Fatalf("got %v, ok = %v, want one stored answer", got, ok)
	}
	if got[0].Line != 7 {
		t.Errorf("line = %d, want 7", got[0].Line)
	}
	if got[0].Hint.Off != 3 || got[0].Hint.Text != "x" {
		t.Errorf("hint = %+v, want the stored hint", got[0].Hint)
	}
}

// inlayVersion is the active pane's current document version.
func (h *harness) inlayVersion() int { return int(h.Pane().File.Session().Version()) }

// parkInlay parks and applies an inlay answer without touching the pane's wrap
// state, so a test can drive the apply path directly.
func (h *harness) parkInlay(hints ...lsp.InlayHint) {
	h.inlayGen++
	h.park(lspAnswer{
		gen:          h.inlayGen,
		kind:         answerInlay,
		path:         h.docPath(h.Pane()),
		inlayVersion: h.inlayVersion(),
		hints:        hints,
	})
	h.applyAnswer()
}

// installInlay parks and applies an inlay answer as a server would, so a test
// can get hints onto the active pane without a real server process. It neither
// pins nor unpins wrapping: the fallback keeps hints on lines that fit one row
// whether the pane wraps or not, and the fit and wrap behaviour have their own
// tests in inlay_wrap_test.go.
func (h *harness) installInlay(hints ...lsp.InlayHint) {
	h.parkInlay(hints...)
}

// The guard: the idle tick asks again only when the path, version or range has
// moved. Without it every tick would put a request on the wire forever.
func TestHintsWantedGuard(t *testing.T) {
	a := &App{}
	if !a.hintsWanted("/w/a.go", 1, 0, 40) {
		t.Fatal("a fresh app did not want hints")
	}
	a.inlayReq = inlayRequest{path: "/w/a.go", version: 1, top: 0, bottom: 40}
	if a.hintsWanted("/w/a.go", 1, 0, 40) {
		t.Error("the same path, version and range were requested again")
	}
	if !a.hintsWanted("/w/a.go", 2, 0, 40) {
		t.Error("a moved version did not re-request")
	}
	if !a.hintsWanted("/w/a.go", 1, 5, 45) {
		t.Error("a moved range did not re-request")
	}
	if !a.hintsWanted("/w/b.go", 1, 0, 40) {
		t.Error("a different path did not re-request")
	}
}

// Without a server no request is made and no guard is recorded: the guard marks
// a request that actually happened, so the hints still appear once a lazily
// started server becomes ready rather than being suppressed for the session.
func TestMaybeRequestHintsWithoutAServerRecordsNothing(t *testing.T) {
	h := newHarness(t, "package main\n")
	// A file type with no configured server: for_ answers without spawning, so
	// this test cannot start a real gopls even where one is installed.
	h.Pane().File.Path = "/w/notes.txt"
	before := h.inlayGen
	h.Handle(ui.Tick{})

	if h.inlayGen != before {
		t.Errorf("inlayGen moved from %d to %d with no server", before, h.inlayGen)
	}
	if h.inlayReq.path != "" {
		t.Errorf("recorded a request for %q with no server", h.inlayReq.path)
	}
}

// The toggle is authoritative: hints already on screen go before the next
// frame, and no request is made while the feature is off.
func TestInlayHintsOffClearsAndRequestsNothing(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1, Character: 0}, Text: "x"})
	if h.Pane().File.Hints == nil {
		t.Fatal("setup: hints were not installed")
	}

	h.InlayHints = false
	cleared := h.inlayGen
	h.Draw()
	if h.Pane().File.Hints != nil {
		t.Error("turning hints off left the overlay on screen")
	}
	if h.inlayGen <= cleared {
		t.Error("turning hints off did not invalidate the installed answer")
	}

	h.inlayReq = inlayRequest{path: h.docPath(h.Pane()), version: h.inlayVersion()}
	gen := h.inlayGen
	h.maybeRequestHints(h.Pane())
	if h.inlayReq.path != "" {
		t.Error("a disabled app recorded a request")
	}
	if h.inlayGen != gen {
		t.Errorf("a disabled app bumped the generation from %d to %d", gen, h.inlayGen)
	}
}

// A matching answer becomes editor hints: the line from the server position,
// the offset relative to that line, and each text edit converted to absolute
// byte offsets exactly as completion edits are.
func TestApplyInlayInstallsHints(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.installInlay(lsp.InlayHint{
		Pos:          lsp.Position{Line: 1, Character: 1},
		Text:         "int",
		Kind:         1,
		PaddingLeft:  true,
		PaddingRight: true,
		Tooltip:      "the type",
		Edits: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 1}},
			NewText: "int ",
		}},
	})

	got := h.Pane().File.HintsAt(1)
	if len(got) != 1 {
		t.Fatalf("line 1 hints = %v, want one", got)
	}
	want := editor.Hint{
		Off: 1, Text: "int", Left: true, Right: true, Kind: 1, Tooltip: "the type",
		Edits: []editor.HintEdit{{Start: 4, End: 5, Text: "int "}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("hint = %+v, want %+v", got[0], want)
	}
	if h.Pane().File.HintsAt(0) != nil {
		t.Error("a hint landed on the wrong line")
	}

	stored, version, ok := h.inlays.forPath(h.docPath(h.Pane()))
	if !ok || version != h.inlayVersion() || len(stored) != 1 {
		t.Fatalf("store = (%v, %d, %v), want the installed answer", stored, version, ok)
	}
	if stored[0].Line != 1 || stored[0].Hint.Text != "int" {
		t.Errorf("stored = %+v, want line 1 and the hint text", stored[0])
	}
}

// A superseded answer is dropped rather than installed, even when its version
// happens to match.
func TestStaleInlayGenerationIsDropped(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.inlayGen = 5
	h.park(lspAnswer{
		gen: 4, kind: answerInlay,
		path: h.docPath(h.Pane()), inlayVersion: h.inlayVersion(),
		hints: []lsp.InlayHint{{Pos: lsp.Position{Line: 1}, Text: "x"}},
	})
	h.applyAnswer()

	if h.Pane().File.Hints != nil {
		t.Error("a superseded answer was installed")
	}
	if _, _, ok := h.inlays.forPath(h.docPath(h.Pane())); ok {
		t.Error("a superseded answer reached the store")
	}
}

// An answer for a document version the buffer has left is dropped: a stale
// hint's offsets have moved, so it would shift every display column after it.
func TestInlayAnswerForAMovedVersionIsDropped(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.inlayGen++
	h.park(lspAnswer{
		gen: h.inlayGen, kind: answerInlay,
		path: h.docPath(h.Pane()), inlayVersion: h.inlayVersion() + 1,
		hints: []lsp.InlayHint{{Pos: lsp.Position{Line: 1}, Text: "x"}},
	})
	h.applyAnswer()

	if h.Pane().File.Hints != nil {
		t.Error("an answer for a version the buffer has left was installed")
	}
}

// An answer that arrives after the user switched tabs belongs to the pane that
// asked, not to whatever is active now.
func TestInlayAnswerForAnotherPaneIsDropped(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.inlayGen++
	h.park(lspAnswer{
		gen: h.inlayGen, kind: answerInlay,
		path: "/w/other.go", inlayVersion: h.inlayVersion(),
		hints: []lsp.InlayHint{{Pos: lsp.Position{Line: 1}, Text: "x"}},
	})
	h.applyAnswer()

	if h.Pane().File.Hints != nil {
		t.Error("an answer for another pane was installed")
	}
}

// An answer arriving while the feature is off is refused, whatever the
// generation and version say.
func TestApplyInlayRefusesWhenHintsAreOff(t *testing.T) {
	cases := []struct {
		name  string
		alter func(*harness)
	}{
		{"app default", func(h *harness) { h.InlayHints = false }},
		{"pane", func(h *harness) { h.Pane().Hints = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "abc\ndef\n")
			tc.alter(h)
			h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1}, Text: "x"})
			if h.Pane().File.Hints != nil {
				t.Error("an answer installed while hints were off")
			}
		})
	}
}

// An edit clears the hints before the next frame, so a stale hint cannot be
// drawn even once. The generation moves too, discarding any answer in flight.
func TestEditClearsHintsAndBumpsGeneration(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1}, Text: "x"})
	if h.Pane().File.Hints == nil {
		t.Fatal("setup: no hints installed")
	}
	gen := h.inlayGen

	h.typeText("z")
	if h.Pane().File.Hints != nil {
		t.Error("an edit left stale hints on the file")
	}
	if h.inlayGen <= gen {
		t.Errorf("generation = %d, want it bumped past %d", h.inlayGen, gen)
	}
}

// Undo is an edit like any other: the version moves, and the clearing at draw
// time catches it without the undo path knowing hints exist.
func TestUndoClearsHintsAtTheNextDraw(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.typeText("z") // one undoable op
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1}, Text: "x"})
	if h.Pane().File.Hints == nil {
		t.Fatal("setup: no hints installed")
	}

	h.press("super+z")
	if h.Pane().File.Hints != nil {
		t.Error("undo left stale hints on the file")
	}
}

// A mutation that never passes through the key handler — an accept, a control
// write, an applied hint edit — is caught by the version check at draw time.
func TestHintsClearAtTheNextDrawAfterAnyEdit(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1}, Text: "x"})
	if h.Pane().File.Hints == nil {
		t.Fatal("setup: no hints installed")
	}

	h.Pane().InsertText("Z")
	h.Draw()
	if h.Pane().File.Hints != nil {
		t.Error("a mutation outside the key path left stale hints")
	}
}

// The per-pane toggle is authoritative: turning hints off clears what is on
// screen immediately, forgets the request guard and stops further requests;
// turning them back on leaves the guard clear so the next idle tick asks
// again. The application default is untouched.
func TestToggleInlayHintsClearsAndReRequests(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	// A file type with no configured server: the request path is exercised
	// without spawning a real gopls, and the pane guard stays observable.
	h.Pane().File.Path = "/w/notes.txt"
	h.Draw()
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1, Character: 0}, Text: "x"})
	if h.Pane().File.Hints == nil {
		t.Fatal("setup: hints were not installed")
	}
	if !h.Pane().Hints || !h.InlayHints {
		t.Fatal("setup: hints should start on, on the pane and on the app")
	}

	// Off: the overlay goes, the in-flight answer is invalidated, and the
	// request guard is forgotten.
	gen := h.inlayGen
	h.press("shift+super+i")
	if h.Pane().Hints {
		t.Error("the toggle did not turn the pane hints off")
	}
	if h.Pane().File.Hints != nil {
		t.Error("toggling off left the installed hints on screen")
	}
	if h.inlayGen <= gen {
		t.Error("toggling off did not invalidate the installed answer")
	}
	if !h.InlayHints {
		t.Error("the pane toggle changed the application default")
	}

	// A disabled pane makes no request: a guard it was given is dropped.
	h.inlayReq = inlayRequest{path: h.docPath(h.Pane()), version: h.inlayVersion()}
	before := h.inlayGen
	h.maybeRequestHints(h.Pane())
	if h.inlayReq != (inlayRequest{}) {
		t.Errorf("a disabled pane kept a request guard: %+v", h.inlayReq)
	}
	if h.inlayGen != before {
		t.Errorf("a disabled pane bumped the generation from %d to %d", before, h.inlayGen)
	}

	// On: the guard is clear, so the next idle tick wants this document again
	// even though its text has not moved.
	h.press("shift+super+i")
	if !h.Pane().Hints {
		t.Error("the toggle did not turn the pane hints back on")
	}
	if !h.hintsWanted(h.docPath(h.Pane()), h.inlayVersion(), 0, 0) {
		t.Error("turning hints back on left the request guard in place")
	}
}

// Selection over a hint works in document bytes, and vertical movement's goal
// column is display-aware: the hint pushes the byte after it to a later column,
// and moving down onto an un-hinted line lands on the byte that column names.
func TestSelectionDownOntoAHintUsesDisplayColumns(t *testing.T) {
	h := newHarness(t, "abcdef\nabcdef\n")
	h.Draw()
	// The plain vertical move is the one under test; the wrap fallback has its
	// own tests in inlay_wrap_test.go.
	h.Pane().Wrap = false
	// Width 4 at byte 1 on line 0: byte 2 sits at display column 6 (the anchor
	// byte 1, then the hint's four cells).
	h.installInlay(lsp.InlayHint{
		Pos:          lsp.Position{Line: 0, Character: 1},
		Text:         "XY",
		PaddingLeft:  true,
		PaddingRight: true,
	})
	h.Pane().Cursors.Set(2, 2)
	h.press("shift+down")
	c := h.Pane().Cursors.Primary()
	// Line 1 has no hint, so display column 6 is byte 6 of the line: 7 + 6.
	if c.Head != 13 {
		t.Errorf("head = %d, want 13 (line 1 display column 6)", c.Head)
	}
	if c.Anchor != 2 {
		t.Errorf("anchor = %d, want 2 (the selection kept its anchor)", c.Anchor)
	}
}

// Find works in document bytes, so a match after a hint lands on the byte it
// names rather than one shifted by the hint's display columns.
func TestFindAfterAHintLandsOnTheByte(t *testing.T) {
	h := newHarness(t, "abcdef\n")
	h.Draw()
	h.installInlay(lsp.InlayHint{
		Pos:          lsp.Position{Line: 0, Character: 1},
		Text:         "XY",
		PaddingLeft:  true,
		PaddingRight: true,
	})
	h.press("super+f")
	h.typeText("e")
	c := h.Pane().Cursors.Primary()
	if c.Anchor != 4 || c.Head != 5 {
		t.Errorf("find selection = %d..%d, want match bytes 4..5 after the hint", c.Anchor, c.Head)
	}
}

// The inlay request window is measured in display rows — the viewport holds
// those — and converted to the session line range the protocol speaks. With a
// fold above the window the two spaces differ, so the bounds must come through
// the pane map; the old code compared the display top to File.Lines directly.
func TestHintLinesMapDisplayWindowToSession(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	p := h.Pane()
	// Display rows: 0 aaa, 1 fold, 2 bbb, 3 ccc, 4 ddd, 5 the trailing empty
	// line. Session lines: 0 aaa, 1-2 the hidden insertion, 3 bbb, 4 ccc,
	// 5 ddd, 6 the trailing empty line. The fold drops one row, so six rows
	// stand for seven session lines.
	if got := p.DisplayLines(); got != 6 {
		t.Fatalf("DisplayLines = %d, want 6", got)
	}
	p.Viewport.Top, p.Viewport.Rows = 3, 1
	top, bottom := h.hintLines(p)
	if top != 4 || bottom != 5 {
		t.Errorf("hintLines = (%d,%d), want (4,5): display row 3 is session line 4", top, bottom)
	}
}

// Without the fold the window is the identity: the same top and rows return the
// same session lines the old code did, and hintRange covers the line at the
// top through the one past the bottom.
func TestHintLinesIdentityWithoutFolds(t *testing.T) {
	h := newHarness(t, foldedFixture)
	p := h.Pane()
	p.Viewport.Top, p.Viewport.Rows = 3, 1
	if got := p.DisplayLines(); got != p.File.Lines() {
		t.Fatalf("DisplayLines = %d, want File.Lines = %d", got, p.File.Lines())
	}
	top, bottom := h.hintLines(p)
	if top != 3 || bottom != 4 {
		t.Errorf("hintLines = (%d,%d), want (3,4) with no decisions", top, bottom)
	}
}

// hintRange turns the session bounds into byte positions: top maps to that
// line's start, bottom to its start as the exclusive end.
func TestHintRangeUsesSessionBounds(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	p := h.Pane()
	// Session lines 4 (ccc) and 5 (ddd) after the folded insertion.
	rng := h.hintRange(p, 4, 5)
	if rng.Start.Line != 4 || rng.End.Line != 5 {
		t.Errorf("range = %d..%d, want 4..5", rng.Start.Line, rng.End.Line)
	}
	if rng.Start.Character != 0 || rng.End.Character != 0 {
		t.Errorf("range columns = %d..%d, want line starts", rng.Start.Character, rng.End.Character)
	}
	// The start is the top line's byte start, converted to an LSP position:
	// the request sends the document's own coordinates, not a row number.
	doc := lsp.NewDocument(p.File.Text())
	if want := doc.Position(p.File.LineStart(4)); rng.Start != want {
		t.Errorf("range start = %+v, want %+v", rng.Start, want)
	}
}

// A window that runs off the end of the display asks to the end of the file,
// not to the start of the last line: bottom stays exclusive and the hidden
// lines between do not make it overshoot.
func TestHintLinesReachesFileEnd(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	p := h.Pane()
	p.Viewport.Top, p.Viewport.Rows = 0, 50
	top, bottom := h.hintLines(p)
	if top != 0 {
		t.Errorf("top = %d, want 0", top)
	}
	if bottom != p.File.Lines() {
		t.Errorf("bottom = %d, want File.Lines = %d", bottom, p.File.Lines())
	}
	// bottom == File.Lines is exclusive, so hintRange runs to the file end.
	if end := lsp.NewDocument(p.File.Text()).Position(p.File.Len()); h.hintRange(p, top, bottom).End != end {
		t.Errorf("range end = %+v, want the file end %+v", h.hintRange(p, top, bottom).End, end)
	}
}
