package app

import (
	"context"
	"time"

	"raj/internal/complete"
	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Format on save.
//
// A save is the one gesture whose bytes must be on disk when it returns, so
// textDocument/willSaveWaitUntil is the request that fits: the server returns
// edits to apply *before* the write, unlike textDocument/formatting, which is
// a request about a buffer the user then decides to save separately. The hook
// lives in App.write, the one interactive save path, and the edits go through
// applyServerEdits — the same one-undo-step application seam completion and
// formatting use — never through a second applier.
//
// The request is asynchronous, because blocking the event thread on a server
// is exactly what no language feature may do. App.write hands the write to
// finishWrite through a pendingWrite and returns; the answer is parked on its
// own slot and applied on the event thread, then the write resumes. Every
// failure — timeout, transport error, a null, empty or unreadable answer, or a
// buffer that moved under the request — falls through to the unformatted
// write, so a formatter can never cost the user their save.

const (
	// willSaveTimeout bounds the wait before the save proceeds unformatted.
	// The protocol expects a client to drop a slow answer to keep the save
	// fast; three seconds is the same bound the point queries use and far
	// longer than a formatter needs.
	willSaveTimeout = 3 * time.Second
)

// willSaveAction is what the pre-write hook does with a live server.
type willSaveAction int

const (
	// willSaveNone means the server asked for nothing, or the buffer is in a
	// state where a server rewrite must not be applied.
	willSaveNone willSaveAction = iota
	// willSaveNotify sends the fire-and-forget willSave notification.
	willSaveNotify
	// willSaveWait sends willSaveWaitUntil and defers the write for its edits.
	willSaveWait
)

// willSaveActionFor decides the hook purely from the server's advertised
// textDocumentSync and the buffer's state, so the rule is testable without a
// language server.
//
// The wait form is skipped in Review mode — a server rewrite there is the same
// edit the user is not allowed to make — and for a buffer holding proposed
// change sets: the save gesture already opens a review for those, and a server
// rewrite must not be folded into work the user has not decided. The
// notification form carries no edits, so it is safe in both states and is sent
// whenever the server asked for it and the wait did not apply.
func willSaveActionFor(caps lsp.ServerCapabilities, hasPending, review bool) willSaveAction {
	if !review && !hasPending && caps.WillSaveWaitUntil() {
		return willSaveWait
	}
	if caps.WillSave() {
		return willSaveNotify
	}
	return willSaveNone
}

// pendingWrite is a save waiting on a willSaveWaitUntil answer. It is
// event-thread only, like the buffers it names; the answer that resumes it
// arrives through App.saveAnswer.
type pendingWrite struct {
	pane    *editor.Pane
	path    string
	force   bool
	renamed bool
	was     string
	// version is the buffer version the request was measured on. Edits for any
	// other version are dropped, because their offsets name text that is gone.
	version int
	gen     int
	// thens are every save continuation waiting on this write: a later save
	// gesture that supersedes an earlier one appends to the list, so a close or
	// quit that was waiting on the first still completes.
	thens []func(bool)
}

// beginWillSave is the pre-write half of the hook. It returns true when the
// write is waiting on a server answer, in which case finishWrite must not run;
// false means the caller writes now, notification sent or not.
func (a *App) beginWillSave(p *editor.Pane, path string, force bool, thens []func(bool)) bool {
	doc := a.docPath(p)
	if doc == "" {
		return false
	}
	// live, not for_: a save must not spawn a language server, matching the
	// rule lspSaved follows for didSave.
	ls := a.servers.live(doc)
	if ls == nil || ls.sync == nil {
		return false
	}
	switch willSaveActionFor(ls.caps.Capabilities, len(p.File.Session().Pending()) > 0, a.mode == ModeReview) {
	case willSaveNotify:
		// Best effort: a heads-up that fails is not a save that failed.
		_ = lsp.NotifyWillSave(ls.srv.Conn(), doc, lsp.SaveManual)
		return false
	case willSaveWait:
	default:
		return false
	}
	// The server answers against the text it holds, so it has to hold the
	// current text first; a sync failure drops to the plain save.
	version := int(p.File.Session().Version())
	if !a.syncDoc(ls, p) {
		return false
	}
	a.saveGen++
	gen := a.saveGen
	a.pendingWrite = &pendingWrite{
		pane: p, path: path, force: force,
		version: version, gen: gen, thens: thens,
	}
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), willSaveTimeout)
		defer cancel()
		edits, err := lsp.RequestWillSaveWaitUntil(ctx, conn, doc, lsp.SaveManual)
		if err != nil {
			edits = nil // a failed request is no edits; the save proceeds
		}
		a.parkSave(lspAnswer{
			gen: gen, kind: answerWillSave,
			edits: edits, docVersion: version, path: doc,
		})
		// The answer is parked; wake the event loop so the write resumes.
		a.host.Post(ui.Wake{})
	})
	return true
}

// resumeSave finishes a write that waited on willSaveWaitUntil. It applies the
// server's edits to the exact version they were measured on and then runs the
// same finishWrite the synchronous path runs. Every reason not to apply —
// superseded generation, an empty answer, a moved buffer, or a change set that
// landed in the meantime — still reaches finishWrite, so the save always lands.
func (a *App) resumeSave(ans lspAnswer) {
	ps := a.pendingWrite
	if ps == nil || ps.gen != ans.gen {
		return // superseded by a later save gesture
	}
	a.pendingWrite = nil
	p := ps.pane
	// A tab closed while the server was thinking is the one move a save must
	// not survive. The pane outlives its tab in memory -- closeDoc and
	// CloseIndex drop it from the tab set, they do not free it -- so without
	// this check the resumed write would put the closed buffer's bytes on disk
	// as though the close had never happened, and report a save for a file the
	// user just deliberately closed. Contains is the right test rather than a
	// focus check: a save that merely moved behind another tab is still a save
	// the user asked for, and the pane's own finishWrite comment says so.
	// Report the save as not having happened to every continuation, so a close
	// or quit that had been waiting on it does not treat the dropped write as
	// permission to carry on.
	if !a.Tabs.Contains(p) {
		a.status = "save dropped: the buffer was closed before the save completed"
		runThens(ps.thens, false)
		return
	}
	// A change set that arrived while the server was thinking was not in the
	// buffer the save gesture approved. Applying the server's edits over it, or
	// letting File.Save accept it, would approve work the user never saw, so
	// the formatted write is abandoned and the save is re-routed through the
	// same review the gesture would have opened had the set been there. The
	// save is not dropped; it is re-asked.
	if len(p.File.Session().Pending()) > 0 {
		a.status = "save paused: a proposed change set arrived; review and save again"
		a.Tabs.Focus(p)
		a.savePane(p, func(saved bool) { runThens(ps.thens, saved) })
		return
	}
	if len(ans.edits) > 0 {
		if int(p.File.Session().Version()) != ps.version {
			a.status = "the buffer changed before the save formatter could apply"
		} else {
			a.applyWillSaveEdits(p, ans.edits)
		}
	}
	a.finishWrite(p, ps.path, ps.force, ps.renamed, ps.was, ps.thens)
}

// applyWillSaveEdits installs a willSaveWaitUntil answer through the shared
// server-edit path as one undo step. Unlike formatting's install it says
// nothing on an empty list and does not touch the status line on success: a
// save must be quiet whether or not the server had edits.
func (a *App) applyWillSaveEdits(p *editor.Pane, edits []lsp.TextEdit) {
	if group, ok := a.installTextEdits(p, edits); !ok {
		// A rejected or invalidated set can still own text even with nothing
		// proposed; name it and let the save proceed unformatted.
		a.status = leaseNote(group)
	}
}

// parkSave stores a willSaveWaitUntil answer without waking the event loop.
// The request goroutine posts the Wake after parking, so the resume runs on the
// event thread against the buffer's version at that moment. Waking here made a
// parked answer resume before a buffer move that followed it, defeating the
// version pin the resume exists to enforce. It has its own slot rather than the
// shared lspAnswer one, because a save must not be lost: any other answer
// arriving between the request and its reply would overwrite the shared slot
// and the write would never resume.
func (a *App) parkSave(ans lspAnswer) {
	a.lspMu.Lock()
	a.saveAnswer = &ans
	a.lspMu.Unlock()
}

// takeSaveAnswer collects a parked save answer, if there is one. Event thread
// only.
func (a *App) takeSaveAnswer() *lspAnswer {
	a.lspMu.Lock()
	ans := a.saveAnswer
	a.saveAnswer = nil
	a.lspMu.Unlock()
	return ans
}

// runThens reports one save's outcome to every caller waiting on it.
func runThens(thens []func(bool), ok bool) {
	for _, then := range thens {
		report(then, ok)
	}
}

// installTextEdits converts a server LSP TextEdit list — positions in p's
// current text — into byte edits, applies them through applyServerEdits as one
// undo step, and carries the primary cursor through them. ok is false with the
// lease-owning group when the batch touched a pending, rejected or invalidated
// change set; the caller decides what to say about it. It is the shared tail of
// document formatting and format-on-save, so both install a server answer the
// same way.
func (a *App) installTextEdits(p *editor.Pane, te []lsp.TextEdit) (group uint64, ok bool) {
	doc := lsp.NewDocument(p.File.Text())
	edits := make([]complete.Edit, 0, len(te))
	for _, t := range te {
		start, end := doc.Span(t.Range)
		edits = append(edits, complete.Edit{Start: start, End: end, Text: t.NewText})
	}
	head := p.Cursors.Primary().Head
	if g, ok := applyServerEdits(p, edits); !ok {
		return g, false
	}
	at := shiftThroughEdits(head, edits)
	if n := p.File.Len(); at > n {
		at = n
	}
	if at < 0 {
		at = 0
	}
	p.Cursors.Set(at, at)
	p.FollowCursor()
	a.Explorer.Tree.MarkChanged(p.File.Path)
	return 0, true
}
