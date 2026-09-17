package app

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Rename moves a symbol's name through every file the language server names.
//
// The flow has four steps, each able to stop before any edit lands:
//
//  1. textDocument/prepareRename, when the server advertised it, checks that
//     the caret is on something renameable and returns the span and suggested
//     name. A refusal is spoken in the editor's voice here, before a name is
//     asked for — collecting a name and only then failing is the interaction
//     the prepare request exists to remove.
//  2. The new name comes from the shared prompt (AskSuggestion, the field
//     save-as and go-to-line use); there is no second text widget.
//  3. textDocument/rename returns a WorkspaceEdit: edits grouped by document.
//  4. Every target document is resolved before any is touched. An open buffer
//     is used as-is; an unopened file is loaded headlessly and announced as a
//     tab. A target that cannot be loaded refuses the whole rename, naming the
//     file — a half-renamed symbol is worse than an unrenamed one.
//
// The edits are the user's own, not a proposal: the chord was theirs, so the
// ordinary editor path applies them and their save is what writes them. That
// also means an unopened file the rename touches appears as a normal unsaved
// tab, which is the only honest way to show work that is not on disk yet.

// renameSymbol is the chord's entry point. It gates on the server's rename
// capability and, when the server advertised the prepare half, checks the
// position before asking for a name.
func (a *App) renameSymbol() {
	if a.mode == ModeReview {
		a.status = reviewReadOnlyNote()
		return
	}
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := capabilityGap(ls.caps.Capabilities.RenameProvider, "rename"); msg != "" {
		a.status = msg
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	if !ls.caps.Capabilities.RenamePrepare() {
		// No prepare half: the name is collected for the word under the caret,
		// and the rename request itself is the only check the server offers.
		a.beginRename(nil)
		return
	}

	a.lspGen++
	gen := a.lspGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		target, err := lsp.RequestPrepareRename(ctx, conn, path, pos)
		a.park(lspAnswer{
			gen: gen, kind: answerPrepareRename, target: target,
			text: renameRefusal(err),
		})
	})
}

// renameRefusal turns a prepareRename failure into the sentence the editor
// shows. A server error carries its own explanation, which is more specific
// than anything the client could invent; a transport failure is stated in the
// editor's own voice rather than as a Go error string.
func renameRefusal(err error) string {
	if err == nil {
		return ""
	}
	var rerr *lsp.ResponseError
	if errors.As(err, &rerr) && rerr.Message != "" {
		return rerr.Message
	}
	return "the language server did not answer the rename check"
}

// applyPrepareRename is the event-thread half of the prepare answer: it either
// speaks the refusal or opens the name prompt.
func (a *App) applyPrepareRename(r lspAnswer) {
	if r.target == nil {
		if r.text != "" {
			a.status = r.text
		} else {
			a.status = "nothing to rename here"
		}
		return
	}
	a.beginRename(r.target)
}

// beginRename opens the new-name prompt, seeded with the server's suggested
// name, the text its span covers, or the word under the caret.
//
// The seed is selected rather than placed at the end (AskSuggestion, not Ask):
// a rename almost always replaces the whole name, so the first keystroke should
// type over it rather than extend it. Enter keeps the suggestion untouched,
// which is what makes a name the server already chose one keypress away.
func (a *App) beginRename(target *lsp.RenameTarget) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	text := p.File.Text()
	head := p.Cursors.Primary().Head
	name := ""
	switch {
	case target != nil && target.Placeholder != "":
		name = target.Placeholder
	case target != nil && !target.Default:
		doc := lsp.NewDocument(text)
		lo, hi := doc.Span(target.Range)
		if lo < hi && hi <= len(text) {
			name = text[lo:hi]
		}
	}
	if name == "" {
		if lo, hi := renameWord(text, head); lo < hi {
			name = text[lo:hi]
		}
	}
	if name == "" {
		a.status = "nothing to rename here"
		return
	}
	a.askSuggestion("Rename", name, func(answer string, ok bool) {
		name := strings.TrimSpace(answer)
		if !ok || name == "" {
			a.status = "rename cancelled"
			return
		}
		a.renameTo(name)
	})
}

// renameTo sends the rename request and parks the WorkspaceEdit answer.
//
// It pins every open document's version at the moment of the request, so an
// answer computed against text the user has since changed can be refused whole
// rather than applied at stale offsets. Only open documents have a version to
// pin; an unopened file is read at apply time, which is the one staleness this
// cannot see.
func (a *App) renameTo(name string) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := capabilityGap(ls.caps.Capabilities.RenameProvider, "rename"); msg != "" {
		a.status = msg
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	// Bring every dirty open document the live server can see up to its
	// current text before asking, so the edit is computed against what is on
	// screen rather than a snapshot from before the last few keystrokes. The
	// version pin below then catches anything that moves after this point.
	a.syncDirtyDocs()
	a.lspGen++
	gen := a.lspGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	conn := ls.srv.Conn()
	versions := a.openDocVersions()
	safe.Go(func() {
		// A project-wide rename can touch many packages and some servers walk
		// the whole index, so this bound is wider than a point query's.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		edit, _ := lsp.RequestRename(ctx, conn, path, pos, name)
		a.park(lspAnswer{gen: gen, kind: answerRename, edit: edit, versions: versions, text: name})
	})
}

// applyRename resolves a WorkspaceEdit and applies it, or refuses it whole.
//
// All-or-nothing is the rule, and it is enforced in three places: a file
// operation refuses the edit, a version mismatch refuses the edit, and a
// document that cannot be loaded refuses the edit. Only after every target is
// resolved does the first edit land.
func (a *App) applyRename(r lspAnswer) {
	if r.edit == nil {
		a.status = "rename made no changes"
		return
	}
	if len(r.edit.ResourceOps) > 0 {
		a.status = "rename needs file operations raj cannot apply (" +
			strings.Join(r.edit.ResourceOps, ", ") + "); nothing was changed"
		return
	}
	if len(r.edit.Docs) == 0 {
		a.status = "rename made no changes"
		return
	}
	for path, version := range r.versions {
		if p, ok := a.paneByPath(path); ok && int(p.File.Session().Version()) != version {
			a.status = "rename cancelled: " + filepath.Base(path) +
				" changed while the server was working"
			return
		}
	}

	// Resolve every document before touching any. A document that is already
	// open is used as-is; an unopened file is loaded headlessly. A path that
	// cannot be loaded — missing, outside the workspace, binary, unreadable —
	// refuses the whole rename rather than leaving a symbol half-renamed, and
	// the buffers loaded for the attempt are dropped again.
	type target struct {
		pane  *editor.Pane
		edits []lsp.TextEdit
	}
	targets := make([]target, 0, len(r.edit.Docs))
	var loaded []*editor.Pane
	for _, d := range r.edit.Docs {
		p, ok := a.paneByPath(d.Path)
		if !ok {
			q, err := a.loadHeadless(d.Path)
			if err != nil {
				for _, l := range loaded {
					a.dropHeadless(l)
				}
				a.status = "rename would change " + d.Path +
					", which is not open and cannot be loaded; nothing was changed"
				return
			}
			p = q
			loaded = append(loaded, q)
		}
		targets = append(targets, target{pane: p, edits: d.Edits})
	}

	// The original tab stays the one on screen; announcing a touched file adds
	// it to the bar without pulling the view off what the user was reading.
	orig := a.Tabs.Active()
	// Reveal every touched buffer before its edit lands: one loaded just now and
	// one already loaded headlessly both need a tab, and announceIfHeadless is a
	// no-op for a tab. Announcing first is what keeps the invariant that a dirty
	// buffer is never headless and so never dropped by the headless cap.
	for _, t := range targets {
		a.announceIfHeadless(t.pane)
	}
	for _, t := range targets {
		a.applyDocEdits(t.pane, t.edits)
	}
	if orig != nil && a.Tabs.Focus(orig) {
		a.focus = FocusEditor
	}
	a.status = "renamed to " + r.text + " in " + itoa(len(targets)) + " file(s); save to write"
}

// applyDocEdits replaces a document's spans with the server's text as one undo
// step, highest offset first so earlier spans stay valid. It is the same shape
// acceptCompletion uses for a completion's textEdit and additionalTextEdits,
// extended from one document to each document a workspace edit touches.
func (a *App) applyDocEdits(p *editor.Pane, edits []lsp.TextEdit) {
	if p == nil || p.File == nil || len(edits) == 0 {
		return
	}
	doc := lsp.NewDocument(p.File.Text())
	type span struct {
		lo, hi int
		text   string
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		lo, hi := doc.Span(e.Range)
		spans = append(spans, span{lo: lo, hi: hi, text: e.NewText})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].lo != spans[j].lo {
			return spans[i].lo > spans[j].lo
		}
		return spans[i].hi > spans[j].hi
	})
	head := p.Cursors.Primary().Head
	at := renameShiftOffset(head, edits, doc)
	p.File.Begin()
	for _, s := range spans {
		p.ReplaceRange(s.lo, s.hi, s.text)
	}
	p.File.End()
	if n := p.File.Len(); at > n {
		at = n
	}
	if at < 0 {
		at = 0
	}
	p.Cursors.Set(at, at)
	p.FollowCursor()
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// renameShiftOffset maps an original byte offset through a document's edits so
// the caret stays where it was looking instead of jumping to whichever edit
// happened to be applied last. Edits are spans in the document's original
// coordinates; the result is in the edited document's coordinates.
func renameShiftOffset(off int, edits []lsp.TextEdit, doc *lsp.Document) int {
	type span struct{ lo, hi, n int }
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		lo, hi := doc.Span(e.Range)
		spans = append(spans, span{lo: lo, hi: hi, n: len(e.NewText)})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].lo != spans[j].lo {
			return spans[i].lo < spans[j].lo
		}
		return spans[i].hi < spans[j].hi
	})
	delta := 0
	for _, s := range spans {
		switch {
		case s.hi <= off:
			delta += s.n - (s.hi - s.lo)
		case s.lo <= off:
			return s.lo + delta + s.n
		}
	}
	return off + delta
}

// paneByPath finds the open or headless buffer for an absolute path, the read
// side of the control host's find. It never loads: a path with no buffer is the
// caller's to load, so the decision to reach into a file nobody opened stays in
// one place.
func (a *App) paneByPath(path string) (*editor.Pane, bool) {
	if path == "" {
		return nil, false
	}
	for _, p := range a.Tabs.All() {
		if p.File != nil && p.File.Path != "" && sameFile(path, p.File.Path) {
			return p, true
		}
	}
	return a.findHeadless(path)
}

// openDocVersions pins every open document to its current version. A rename
// answer carries these so an edit computed against text the buffer has since
// left is refused whole.
func (a *App) openDocVersions() map[string]int {
	out := map[string]int{}
	for _, p := range a.Tabs.All() {
		if path := a.docPath(p); path != "" {
			out[path] = int(p.File.Session().Version())
		}
	}
	return out
}

// renameWord returns the identifier around a byte offset. A caret inside a word
// renames the whole word, not the half before it, which is why this is not the
// completion prefix: PrefixAt deliberately stops at the caret.
func renameWord(text string, off int) (lo, hi int) {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	lo, hi = off, off
	for lo > 0 && renameWordByte(text[lo-1]) {
		lo--
	}
	for hi < len(text) && renameWordByte(text[hi]) {
		hi++
	}
	return lo, hi
}

// renameWordByte is the identifier byte class, the same one completion uses:
// an underscore, a digit, a letter, or the start of a multi-byte rune.
func renameWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}
