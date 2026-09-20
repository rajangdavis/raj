package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"raj/internal/lsp"
	"raj/internal/piecetable"
)

// The pre-write hook asks for exactly what the server advertised: the wait
// request when it exists, the notification when only that exists, and nothing
// when neither does. Without the capability read the hook would ask every
// server for an answer the protocol says it does not provide.
func TestWillSaveActionOnlyWhenAdvertised(t *testing.T) {
	none := lsp.ServerCapabilities{}
	wait := lsp.ServerCapabilities{TextDocumentSync: json.RawMessage(`{"willSaveWaitUntil":true}`)}
	notify := lsp.ServerCapabilities{TextDocumentSync: json.RawMessage(`{"willSave":true}`)}
	both := lsp.ServerCapabilities{TextDocumentSync: json.RawMessage(`{"willSave":true,"willSaveWaitUntil":true}`)}
	cases := []struct {
		name       string
		caps       lsp.ServerCapabilities
		hasPending bool
		review     bool
		want       willSaveAction
	}{
		{"neither advertised", none, false, false, willSaveNone},
		{"wait advertised", wait, false, false, willSaveWait},
		{"notify advertised", notify, false, false, willSaveNotify},
		{"both advertised prefers the wait", both, false, false, willSaveWait},
		{"pending skips the wait but still notifies", both, true, false, willSaveNotify},
		{"review skips the wait but still notifies", both, false, true, willSaveNotify},
		{"pending with no notify is nothing", wait, true, false, willSaveNone},
		{"review with no notify is nothing", wait, false, true, willSaveNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := willSaveActionFor(c.caps, c.hasPending, c.review); got != c.want {
				t.Errorf("willSaveActionFor = %v, want %v", got, c.want)
			}
		})
	}
}

// waitForWillSave installs the state beginWillSave leaves behind: a save held
// on a server answer whose result is already parked. It drives the resume path
// without a language server, which the app tests have no fake for.
func waitForWillSave(h *harness, edits []lsp.TextEdit) {
	p := h.Pane()
	ver := int(p.File.Session().Version())
	h.saveGen++
	gen := h.saveGen
	h.pendingWrite = &pendingWrite{
		pane: p, path: p.File.Path, version: ver, gen: gen,
	}
	h.parkSave(lspAnswer{gen: gen, kind: answerWillSave, edits: edits,
		docVersion: ver, path: h.docPath(p)})
}

// The server edits are on disk when the save completes: applyServerEdits runs
// before finishWrite, so the write materialises the formatted text. Without
// the ordering the save would write the unformatted bytes first and the server
// edits would only dirty the buffer afterwards.
func TestWillSaveEditsLandBeforeTheWrite(t *testing.T) {
	h := newHarness(t, "package main\n")
	waitForWillSave(h, []lsp.TextEdit{{
		Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 8}, End: lsp.Position{Line: 0, Character: 12}},
		NewText: "fmt",
	}})
	h.applyAnswer()
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "package fmt\n" {
		t.Errorf("on disk = %q, want the formatted bytes", got)
	}
	if h.Pane().File.Dirty() {
		t.Error("the buffer is dirty after the save")
	}
}

// A failed request arrives as no edits, and the write still happens: the save
// must not be lost to a server that timed out, crashed or had nothing to say.
// Without the fall-through a failed formatter would strand the buffer dirty
// with the bytes never reaching disk.
func TestWillSaveFailureStillSaves(t *testing.T) {
	h := newHarness(t, "package main\n")
	waitForWillSave(h, nil)
	h.applyAnswer()
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Errorf("on disk = %q, want the unformatted save to have landed", string(data))
	}
	if h.Pane().File.Dirty() {
		t.Error("the buffer is dirty after a save that fell back")
	}
}

// An answer measured on a version the buffer has left is dropped, and the
// current text is written instead: applying stale offsets would mangle the
// file, and skipping the write would lose the save. The disk shows the edit
// did not land and the user text did.
func TestWillSaveStaleEditsAreDroppedButTheSaveLands(t *testing.T) {
	h := newHarness(t, "package main\n")
	waitForWillSave(h, []lsp.TextEdit{{
		Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 8}, End: lsp.Position{Line: 0, Character: 12}},
		NewText: "fmt",
	}})
	h.typeText("x") // the buffer moves while the server is thinking
	want := h.text()
	h.applyAnswer()
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("on disk = %q, want the current buffer %q", string(data), want)
	}
	if strings.Contains(string(data), "fmt") {
		t.Error("the stale server edit was applied anyway")
	}
}

// A change set that lands while the server is thinking is not silently
// accepted by the resumed write: the save is re-routed through the same review
// the gesture opens for pending sets, and nothing is written until the user
// answers. Without the check File.Save would accept the proposal and write it,
// approving work the user never saw.
func TestWillSaveResumeReroutesWhenAProposalArrives(t *testing.T) {
	h := newHarness(t, reviewFixture)
	waitForWillSave(h, []lsp.TextEdit{{
		Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 6}, End: lsp.Position{Line: 0, Character: 11}},
		NewText: "socket",
	}})
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.applyAnswer()
	if !h.Prompt.Open {
		t.Fatal("the save was not re-routed through the review")
	}
	if got := len(h.Pane().File.Session().Pending()); got != 1 {
		t.Errorf("pending = %d, want the set still awaiting the decision", got)
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != reviewFixture {
		t.Errorf("on disk = %q; a pending proposal must not be written", string(data))
	}
	if !strings.Contains(h.Status(), "proposed change set") {
		t.Errorf("status = %q, want the re-route named", h.Status())
	}
}

// With no live server the hook does nothing and the ordinary save path writes
// the buffer. Without the nil-server guard the save would wait on a server
// that was never started and never arrive.
func TestWillSaveWithoutAServerSavesNormally(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.typeText("x")
	want := h.text()
	h.press("super+s")
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("on disk = %q, want %q", string(data), want)
	}
	if h.Pane().File.Dirty() {
		t.Error("the buffer is dirty after an ordinary save")
	}
}

// A pane closed while willSaveWaitUntil is outstanding must not be written when
// the answer lands. The pending write names a pane, not a tab, and the pane is
// still alive in memory after its tab is gone -- closeDoc and CloseIndex drop it
// from the tab set, they do not free it -- so the resumed write would put this
// buffer's bytes on disk as though the close had never happened, and finishWrite
// would go on to report a save for a file the user just deliberately closed and
// chose to discard. resumeSave checks that the pane is still open before it
// writes and drops the answer otherwise.
func TestWillSaveResumeDoesNotWriteAClosedPane(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.typeText("x") // a change, so the close is a decision the user made
	p := h.Pane()
	path := p.File.Path
	want := p.File.Text()

	waitForWillSave(h, []lsp.TextEdit{{
		Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 8}, End: lsp.Position{Line: 0, Character: 12}},
		NewText: "fmt",
	}})

	// Close the pane while the server is thinking. The buffer is dirty, so the
	// close asks first; discarding is the answer that removes the tab.
	h.press("super+w")
	if !h.Prompt.Open {
		t.Fatal("setup: closing a dirty buffer did not ask")
	}
	h.press("right", "enter") // SaveOptions: [Save, Discard, Cancel]
	if h.Tabs.Contains(p) {
		t.Fatal("setup: the pane is still open")
	}

	h.applyAnswer()

	// The write must not have happened: the buffer was closed without saving,
	// so the file keeps its original bytes and no server edit was installed.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Errorf("on disk = %q, want the file untouched by a write to a closed pane", string(data))
	}
	if strings.Contains(string(data), "fmt") {
		t.Error("the willSaveWaitUntil edit was applied to a closed pane")
	}
	if got := p.File.Text(); got != want {
		t.Errorf("closed pane text = %q, want %q (the server edit must not have been installed)", got, want)
	}
	if strings.Contains(h.Status(), "saved") {
		t.Errorf("status = %q, want no save reported for a closed pane", h.Status())
	}
	// The answer must not resurrect the tab either: a resume that wrote the
	// closed pane would be bad enough, but one that reopened it would put a
	// buffer the user discarded back on screen.
	if h.Tabs.Contains(p) {
		t.Error("the save answer recreated the closed pane")
	}
}
