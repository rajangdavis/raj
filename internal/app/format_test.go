package app

import (
	"encoding/json"
	"strings"
	"testing"

	"raj/internal/complete"
	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/keys"
	"raj/internal/lsp"
)

// The options are the buffer's own style, not a hardcoded four spaces: a Go
// file must tell the server tabs and a two-space file two spaces, or every
// format rewrites the file to a style the editor does not use. Without the
// change formatOptions does not exist and the request has no options at all.
func TestFormatOptionsComeFromTheBufferIndent(t *testing.T) {
	cases := []struct {
		name   string
		style  editor.Indent
		tab    int
		spaces bool
	}{
		{"tabs", editor.Indent{Tabs: true, Width: 8}, 8, false},
		{"two spaces", editor.Indent{Width: 2}, 2, true},
		{"unset width", editor.Indent{}, editor.DefaultIndentWidth, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatOptions(c.style)
			if got.TabSize != c.tab || got.InsertSpaces != c.spaces {
				t.Errorf("formatOptions(%+v) = %+v, want tabSize %d insertSpaces %v",
					c.style, got, c.tab, c.spaces)
			}
		})
	}
}

// The two formatting requests read their own capability: a server that
// advertises one and not the other must be refused for the one it lacks, in
// the same voice as the other feature gates, rather than asked a method it
// answers method-not-found. Without the change neither provider is read.
func TestFormattingCapabilityGatesReadTheirOwnProvider(t *testing.T) {
	caps := lsp.ServerCapabilities{
		DocumentFormattingProvider:      json.RawMessage(`true`),
		DocumentRangeFormattingProvider: json.RawMessage(`false`),
	}
	if got := capabilityGap(documentFormat().provider(caps), documentFormat().feature); got != "" {
		t.Errorf("formatting gap = %q, want none", got)
	}
	if got := capabilityGap(rangeFormat().provider(caps), rangeFormat().feature); got != "language server does not support range formatting" {
		t.Errorf("range formatting gap = %q", got)
	}
	// An absent provider is a gap for each: a server that never mentioned the
	// feature must not be asked.
	for _, req := range []formatRequest{documentFormat(), rangeFormat()} {
		want := "language server does not support " + req.feature
		if got := capabilityGap(req.provider(lsp.ServerCapabilities{}), req.feature); got != want {
			t.Errorf("%s: absent provider gap = %q, want %q", req.feature, got, want)
		}
	}
}

// Range formatting takes the primary selection: the selection is head-to-anchor
// on the cursor, and the protocol wants one range. A collapsed caret is refused
// rather than sent as a zero-byte range. Without the change selectionRange does
// not exist and requestFormat has no range to send.
func TestSelectionRangeIsThePrimarySelection(t *testing.T) {
	h := newHarness(t, "one two\nthree\n")
	if _, ok := selectionRange(h.Pane()); ok {
		t.Fatal("a collapsed caret produced a range")
	}
	h.press("super+a") // select the whole document
	rng, ok := selectionRange(h.Pane())
	if !ok {
		t.Fatal("a selection produced no range")
	}
	if rng.Start != (lsp.Position{Line: 0, Character: 0}) {
		t.Errorf("range start = %+v, want 0:0", rng.Start)
	}
	if rng.End != (lsp.Position{Line: 2, Character: 0}) {
		t.Errorf("range end = %+v, want the start of the line after the text", rng.End)
	}
}

// A server edit list is applied as one change set: the whole batch is one undo
// step, so undoing a format is not once per edit. Without applyServerEdits the
// edits go through the old per-candidate path and each is its own undo.
func TestApplyFormatIsOneUndo(t *testing.T) {
	h := newHarness(t, "one two\nthree four\n")
	before := h.text()
	h.lspGen = 1
	edits := []lsp.TextEdit{
		{Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 7}}, NewText: "2"},
		{Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 5}}, NewText: "3"},
	}
	h.park(lspAnswer{gen: 1, kind: answerFormat, edits: edits,
		docVersion: int(h.Pane().File.Session().Version()), path: h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != "one 2\n3 four\n" {
		t.Fatalf("buffer = %q, want both edits applied", got)
	}
	h.press("super+z")
	if got := h.text(); got != before {
		t.Errorf("one undo left %q, want %q", got, before)
	}
}

// An empty edit list is a no-op with a word, not an error and not a panic:
// "already formatted" is the normal answer for a clean file. Without the change
// an empty answer would be applied as nothing and say nothing.
func TestApplyFormatEmptyListIsANoOp(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerFormat,
		docVersion: int(h.Pane().File.Session().Version()), path: h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("an empty edit list changed the buffer: %q", got)
	}
	if !strings.Contains(h.Status(), "already formatted") {
		t.Errorf("status = %q, want the no-op word", h.Status())
	}
}

// An answer measured on an older version is dropped: the offsets name text
// that is gone, and applying them would mangle the file. Without the version
// check the stale edits land at whatever happens to be there now.
func TestApplyFormatDropsAnAnswerForAnEarlierVersion(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerFormat,
		edits: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 7}},
			NewText: "package other",
		}},
		docVersion: int(h.Pane().File.Session().Version()) - 1,
		path:       h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("stale formatting offsets were applied: %q", got)
	}
	if !strings.Contains(h.Status(), "changed") {
		t.Errorf("status = %q, want the stale answer named", h.Status())
	}
}

// Formatting a buffer with a pending change set is refused whole rather than
// applied around it: which of the two texts is right is the user's decision,
// and a half-applied format with a moved proposal is the worst of both. Without
// the batch lease check the edits that do not overlap land and the buffer is
// left half-formatted with no note of which part.
func TestApplyFormatRefusesOverAProposedSpan(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 5, Text: "HELLO"}}}); !r.OK {
		t.Fatalf("setup apply = %+v", r)
	}
	before := h.text()
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerFormat,
		edits: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 5}},
			NewText: "hi",
		}},
		docVersion: int(h.Pane().File.Session().Version()), path: h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("formatting landed over a proposed span: %q", got)
	}
	if !strings.Contains(h.Status(), "change set") {
		t.Errorf("status = %q, want the lease refusal to name the set", h.Status())
	}
}

// Formatting rewrites the document, so Review mode refuses it like any other
// edit and names the chord that leaves the mode. Without the gate the global
// action bypasses the editor's read-only check and formats under a review.
func TestFormatIsRefusedInReviewMode(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.mode = ModeReview
	h.handleKeyAction(keys.Format)
	if got := h.text(); got != before {
		t.Errorf("review mode allowed a format: %q", got)
	}
	if !strings.Contains(h.Status(), "read-only in review mode") {
		t.Errorf("status = %q, want the review refusal", h.Status())
	}
}

// The cursor is carried through the edits rather than left wherever the last
// replacement put it. Without shiftThroughEdits a whole-file format leaves the
// caret at the lowest edit's start, which is the top of the file.
func TestShiftThroughEdits(t *testing.T) {
	edits := []complete.Edit{
		{Start: 0, End: 3, Text: "xx"},  // -1 delta
		{Start: 10, End: 10, Text: "!"}, // pure insertion
	}
	cases := []struct {
		name string
		off  int
		want int
	}{
		{"at the replacement", 0, 2},
		{"inside the replacement", 1, 2},
		{"at the replacement end", 3, 2},
		{"after the replacement", 5, 4},
		{"at the insertion point", 10, 10},
		{"after the insertion", 12, 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shiftThroughEdits(c.off, edits); got != c.want {
				t.Errorf("shiftThroughEdits(%d) = %d, want %d", c.off, got, c.want)
			}
		})
	}
}

// onTypeFormatFor is the trigger decision: only a changed keystroke that is one
// of the server's triggers fires, and the character it returns is the one that
// matched. Without it the typing path either asks on every keystroke or never
// asks at all.
func TestOnTypeFormatForSelectsTriggers(t *testing.T) {
	caps := lsp.ServerCapabilities{DocumentOnTypeFormattingProvider: json.RawMessage(
		`{"firstTriggerCharacter":"}","moreTriggerCharacter":["\n",";"]}`)}
	cases := []struct {
		name    string
		typed   string
		changed bool
		want    bool
	}{
		{"first trigger", "}", true, true},
		{"additional trigger", ";", true, true},
		{"newline trigger", "\n", true, true},
		{"non-trigger", "{", true, false},
		{"empty keystroke", "", true, false},
		{"unchanged keystroke", "}", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := onTypeFormatFor(caps, c.typed, c.changed)
			if ok != c.want {
				t.Fatalf("onTypeFormatFor(%q, %v) = %q,%v want %v", c.typed, c.changed, got, ok, c.want)
			}
			if ok && got != c.typed {
				t.Errorf("matched trigger = %q, want %q", got, c.typed)
			}
		})
	}
	// A server with no provider never fires, however trigger-like the keystroke.
	if _, ok := onTypeFormatFor(lsp.ServerCapabilities{}, "}", true); ok {
		t.Error("a server with no on-type provider fired")
	}
}

// The capability gate speaks the shared voice. The typing path stays silent
// when it names a gap — a keystroke is not a request for the feature — but the
// provider is read in exactly one place, and this is it.
func TestOnTypeFormatGap(t *testing.T) {
	if got := onTypeFormatGap(lsp.ServerCapabilities{}); got != "language server does not support on-type formatting" {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := onTypeFormatGap(lsp.ServerCapabilities{DocumentOnTypeFormattingProvider: json.RawMessage(`false`)}); got != "language server does not support on-type formatting" {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := onTypeFormatGap(lsp.ServerCapabilities{DocumentOnTypeFormattingProvider: json.RawMessage(`{"firstTriggerCharacter":"}"}`)}); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// An on-type answer is applied as one change set: the whole batch is one undo
// step, so undoing a brace reformat is one undo rather than one per edit.
// Without installTextEdits the edits go through a per-edit path and each is its
// own undo.
func TestApplyOnTypeFormatIsOneUndo(t *testing.T) {
	h := newHarness(t, "one two\nthree four\n")
	before := h.text()
	h.onTypeGen = 1
	edits := []lsp.TextEdit{
		{Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 7}}, NewText: "2"},
		{Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 5}}, NewText: "3"},
	}
	h.park(lspAnswer{gen: 1, kind: answerOnType, edits: edits,
		docVersion: int(h.Pane().File.Session().Version()), path: h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != "one 2\n3 four\n" {
		t.Fatalf("buffer = %q, want both edits applied", got)
	}
	h.press("super+z")
	if got := h.text(); got != before {
		t.Errorf("one undo left %q, want %q", got, before)
	}
}

// An answer measured on an older version is dropped: the offsets name text that
// is gone, and applying them at the new offsets would mangle the file. Without
// the version check the stale edits land wherever those offsets now point.
func TestApplyOnTypeFormatDropsAStaleAnswer(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.onTypeGen = 1
	h.park(lspAnswer{gen: 1, kind: answerOnType,
		edits: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 7}},
			NewText: "package other",
		}},
		docVersion: int(h.Pane().File.Session().Version()) - 1,
		path:       h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("stale on-type offsets were applied: %q", got)
	}
}

// A second trigger supersedes the first request's answer by generation, so a
// fast pair of keystrokes cannot apply the older formatting over the newer
// text. Without the generation check the first answer lands after the second
// request was made.
func TestApplyOnTypeFormatDropsASupersededAnswer(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.onTypeGen = 2
	h.park(lspAnswer{gen: 1, kind: answerOnType,
		edits: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 7}},
			NewText: "package other",
		}},
		docVersion: int(h.Pane().File.Session().Version()),
		path:       h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("a superseded on-type answer was applied: %q", got)
	}
}

// An empty edit list is a no-op: the server had nothing to change, which is the
// normal answer for most trigger characters and not a failure.
func TestApplyOnTypeFormatEmptyIsANoOp(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.text()
	h.onTypeGen = 1
	h.park(lspAnswer{gen: 1, kind: answerOnType,
		docVersion: int(h.Pane().File.Session().Version()), path: h.docPath(h.Pane())})
	h.applyAnswer()
	if got := h.text(); got != before {
		t.Errorf("an empty on-type answer changed the buffer: %q", got)
	}
}

// The trigger is only reached through a live server: with none running the
// keystroke path must not start one, park anything, or panic. The request and
// its wire shape are covered by the lsp package's tests; internal/app has no
// fake language server to observe the round trip here.
func TestMaybeOnTypeFormatWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.onTypeGen = 0
	h.maybeOnTypeFormat(h.Pane(), "}", true)
	if h.onTypeGen != 0 {
		t.Error("a keystroke with no live server started an on-type request")
	}
	if ans := h.takeAnswer(); ans != nil {
		t.Errorf("a keystroke with no live server parked an answer: %+v", ans)
	}
}
