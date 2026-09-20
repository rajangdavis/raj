package app

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"raj/internal/complete"
	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Formatting. textDocument/formatting formats the whole buffer and
// textDocument/rangeFormatting formats the selection. Both ask the server with
// the buffer's own indent style as FormattingOptions, park the server's
// TextEdit list, and apply it through applyServerEdits — the same one-undo-step
// path completion's server edits take — so a formatting answer is one change
// set rather than one per edit.
//
// The selection is the primary cursor's head-to-anchor span, which is one
// range; the protocol's rangeFormatting also takes one range. Several cursors
// therefore send one request for the primary selection rather than one per
// cursor: merging N independently-formatted answers would be unsound (each one
// may rewrite a whole line the others also touch) and each would want its own
// undo step. Multi-cursor range formatting is left to a later wave.

// formatRequest is one of the two formatting asks: the noun the capability gate
// names, the provider it reads, and whether it formats the selection rather
// than the whole buffer.
type formatRequest struct {
	feature  string
	provider func(lsp.ServerCapabilities) json.RawMessage
	// needsRange reports whether the request formats the selection, which
	// decides both the wire params and the requirement that something is
	// selected at all.
	needsRange bool
}

func documentFormat() formatRequest {
	return formatRequest{
		feature:  "formatting",
		provider: func(c lsp.ServerCapabilities) json.RawMessage { return c.DocumentFormattingProvider },
	}
}

func rangeFormat() formatRequest {
	return formatRequest{
		feature:    "range formatting",
		provider:   func(c lsp.ServerCapabilities) json.RawMessage { return c.DocumentRangeFormattingProvider },
		needsRange: true,
	}
}

// selectionRange is the primary cursor's selection as the protocol's range. It
// reports false when nothing is selected, because a collapsed range would ask
// the server to format a zero-byte span. Multiple cursors use the primary
// selection only: the protocol takes one range, and merging N independently
// formatted answers would be unsound at the boundaries where they meet.
func selectionRange(p *editor.Pane) (lsp.Range, bool) {
	lo, hi := p.Cursors.Primary().Range()
	if lo == hi {
		return lsp.Range{}, false
	}
	doc := lsp.NewDocument(p.File.Text())
	return lsp.Range{Start: doc.Position(lo), End: doc.Position(hi)}, true
}

// formatOptions is the buffer's own indent style as the protocol's formatting
// options. tabSize is the width one level indents by — and a tab's display
// width — and insertSpaces is the style not being tabs, so gopls is told tabs
// for a Go file and two spaces for a two-space file. Hardcoding four spaces
// here would make every formatting answer rewrite the file to a style the
// editor does not use.
func formatOptions(style editor.Indent) lsp.FormattingOptions {
	width := style.Width
	if width <= 0 {
		width = editor.DefaultIndentWidth
	}
	return lsp.FormattingOptions{TabSize: width, InsertSpaces: !style.Tabs}
}

// requestFormat runs one formatting ask: find or start the server, check the
// capability, sync the buffer, and park the answer for the event thread. The
// request goroutine decodes nothing; the edit list is decoded and applied on
// the event thread against the version captured here, so a buffer that moved
// under a slow formatter drops the answer rather than having stale offsets
// applied to it.
func (a *App) requestFormat(r formatRequest) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	// Formatting rewrites the document, so Review mode refuses it like any
	// other text edit. The gate is here rather than in handleGlobal because a
	// global action never reaches the editor's read-only check.
	if a.readOnly() {
		a.status = a.readOnlyNote()
		return
	}
	path := a.docPath(p)
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := capabilityGap(r.provider(ls.caps.Capabilities), r.feature); msg != "" {
		a.status = msg
		return
	}
	// A range request with nothing selected is refused before the server is
	// asked: the protocol has no "format where the cursor is" form, and a
	// collapsed range would have the server format a zero-byte span.
	var rng lsp.Range
	if r.needsRange {
		sel, ok := selectionRange(p)
		if !ok {
			a.status = "select something to format"
			return
		}
		rng = sel
	}
	if !a.syncDoc(ls, p) {
		return
	}

	a.lspGen++
	gen := a.lspGen
	conn := ls.srv.Conn()
	version := int(p.File.Session().Version())
	opts := formatOptions(p.File.Indent)

	safe.Go(func() {
		// Formatting a large file can take a moment, so the bound is wider
		// than the point-query 3s rather than the same by habit.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var edits []lsp.TextEdit
		var err error
		if r.needsRange {
			edits, err = lsp.RequestRangeFormatting(ctx, conn, path, rng, opts)
		} else {
			edits, err = lsp.RequestFormatting(ctx, conn, path, opts)
		}
		if err != nil {
			return
		}
		a.park(lspAnswer{
			gen: gen, kind: answerFormat, edits: edits,
			docVersion: version, path: path,
		})
	})
}

// formatDocument formats the whole active buffer.
func (a *App) formatDocument() { a.requestFormat(documentFormat()) }

// formatSelection formats the active buffer's selected range.
func (a *App) formatSelection() { a.requestFormat(rangeFormat()) }

// applyFormat installs a formatting answer on the active pane.
//
// The answer is applied only to the pane and the version it was measured on. A
// formatter that ran while the user kept typing has produced offsets for text
// that is gone, and applying them would mangle the file rather than format it.
// An empty edit list is a no-op with a word on the status line, not an error:
// "already formatted" is the normal answer for a clean file.
func (a *App) applyFormat(r lspAnswer) {
	p := a.Tabs.Active()
	if p == nil || a.docPath(p) != r.path {
		return
	}
	if int(p.File.Session().Version()) != r.docVersion {
		a.status = "the buffer changed before formatting could apply"
		return
	}
	if len(r.edits) == 0 {
		a.status = "already formatted"
		return
	}
	if group, ok := a.installTextEdits(p, r.edits); !ok {
		// The batch touched a pending or rejected change set. Nothing landed;
		// name the set rather than formatting around it, because which of the
		// two texts is right is the user's decision, not the formatter's.
		a.status = leaseNote(group)
		return
	}
	a.status = "formatted"
}

// shiftThroughEdits carries a byte offset through a batch of replacements
// measured on the same text. An offset before every edit is unchanged, an
// offset after one shifts by that edit's length delta, and an offset inside a
// replaced span lands at the end of the replacement — the place in the new text
// the cursor was pointing at.
func shiftThroughEdits(off int, edits []complete.Edit) int {
	sorted := append([]complete.Edit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	shift := 0
	for _, e := range sorted {
		if off < e.Start {
			break
		}
		if off <= e.End {
			return e.Start + shift + len(e.Text)
		}
		shift += len(e.Text) - (e.End - e.Start)
	}
	return off + shift
}

// On-type formatting. A server that advertises documentOnTypeFormattingProvider
// names the characters that should make it reformat around the cursor — "}" in
// Go, "\n" in some other languages — and answers textDocument/onTypeFormatting
// with a TextEdit list the client applies on that keystroke.
//
// The trigger hooks the one place a keystroke becomes text, handleEditor's
// literal-text path, after the insert. The rule is deliberately narrow: only a
// server already running for the document is asked, never one the keystroke
// would start; only the exact trigger character fires; and only a keystroke
// that actually changed the text. The request is asynchronous, so the answer is
// parked with the version it was measured on and dropped if more was typed
// while the server thought — applying stale offsets would mangle the file
// rather than format it. A second trigger supersedes the first by generation.

// onTypeFormatFor decides whether a keystroke should ask for on-type
// formatting, and which trigger character it matched. It is pure, so the rule —
// a changed keystroke and a trigger the server named, and nothing else — is
// testable without a server.
func onTypeFormatFor(caps lsp.ServerCapabilities, typed string, changed bool) (string, bool) {
	if !changed || typed == "" {
		return "", false
	}
	return lsp.OnTypeTrigger(caps.OnTypeFormattingTriggers(), typed)
}

// onTypeFormatGap names why a live server cannot serve on-type formatting, or
// "" when it can. It is the shared capabilityGap voice, exposed like the other
// feature gates so the provider is read from one place rather than inlined; the
// typing path stays silent when it names a gap, because a keystroke is not a
// request for the feature and a message per keypress would be noise.
func onTypeFormatGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.DocumentOnTypeFormattingProvider, "on-type formatting")
}

// maybeOnTypeFormat asks the server to format around a just-typed trigger
// character.
//
// It is called from the keystroke path, so it must never start a server: a live
// lookup, not for_, keeps typing from spawning a subprocess whose answer nobody
// asked for. A server that is not already running for the document means the
// keystroke is ordinary text.
func (a *App) maybeOnTypeFormat(p *editor.Pane, typed string, changed bool) {
	if p == nil || a.mode != ModeEdit || !changed {
		return
	}
	// On-type formatting is one position. Several cursors typed the character
	// at several places, and the protocol has no request that formats all of
	// them; formatting around the primary and leaving the rest would be a half
	// answer, so none is asked.
	if len(p.Cursors.All()) != 1 {
		return
	}
	path := a.docPath(p)
	if path == "" {
		return
	}
	ls := a.servers.live(path)
	if ls == nil {
		return
	}
	// The shared gate reads the provider in the one place; the typing path does
	// not show the gap it names, because a keystroke is not a request for the
	// feature and a message per keypress would be noise.
	if onTypeFormatGap(ls.caps.Capabilities) != "" {
		return
	}
	ch, ok := onTypeFormatFor(ls.caps.Capabilities, typed, changed)
	if !ok {
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.onTypeGen++
	gen := a.onTypeGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	version := int(p.File.Session().Version())
	opts := formatOptions(p.File.Indent)
	conn := ls.srv.Conn()

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		edits, err := lsp.RequestOnTypeFormatting(ctx, conn, path, pos, ch, opts)
		if err != nil {
			return
		}
		a.park(lspAnswer{
			gen: gen, kind: answerOnType, edits: edits,
			docVersion: version, path: path,
		})
	})
}

// applyOnTypeFormat installs an on-type formatting answer.
//
// The version check is the whole guard: the request named a position in a
// version of the text, and if the user has typed since, those offsets address
// text that is no longer there. A stale answer is dropped with no status-line
// word, because it is the normal outcome of typing faster than the server and a
// message on every other keystroke would be noise. An empty answer is the same
// kind of nothing: the server had no edits, which is not a failure.
func (a *App) applyOnTypeFormat(ans lspAnswer) {
	p := a.Tabs.Active()
	if p == nil || a.docPath(p) != ans.path || a.mode != ModeEdit {
		return
	}
	if int(p.File.Session().Version()) != ans.docVersion {
		return
	}
	if len(ans.edits) == 0 {
		return
	}
	if group, ok := a.installTextEdits(p, ans.edits); !ok {
		// The server's edit touches a pending or rejected change set. Name the
		// set rather than formatting around it; the typing path normally drains
		// its own refusal, and this is the same message.
		a.status = leaseNote(group)
	}
}
