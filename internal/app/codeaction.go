package app

import (
	"context"
	"strings"
	"time"

	"raj/internal/complete"
	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/picker"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Code actions are the language server's fixes and refactors for the caret:
// "import this", "remove the unused variable", "extract a function". The
// request is caret-scoped and the answer is heterogeneous -- an action may
// carry an edit, a command, or a data blob for a resolve round-trip -- so this
// file owns the request, the picker listing, and the three ways an action can
// be carried out.
//
// Whole-file kinds such as source.fixAll and source.organizeImports are not
// requested: they are not caret-scoped and would need their own entry point.
// The capability advertises the quickfix and refactor kinds only, so a server
// does not offer one here.

// codeActions asks the server for the actions that apply to the caret and parks
// them for the event thread to list.
//
// The request range is the selection when there is one and the caret's line
// otherwise. A line rather than a zero-width caret: the common request is a
// quick fix for the problem under the cursor, and a diagnostic's span does not
// always contain the caret's exact column (the caret at the end of a squiggled
// word, say), so the line is the range that means "here".
func (a *App) codeActions() {
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
	if msg := codeActionGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}

	rng := a.caretRange(p)
	ctxDiags := lsp.Intersecting(a.diags.forPath(path), rng)

	a.lspGen++
	gen := a.lspGen
	conn := ls.srv.Conn()
	// The open documents are pinned at request time so an edit computed against
	// text the user has since changed is refused whole rather than applied at
	// stale offsets, the same guard a rename uses.
	versions := a.openDocVersions()
	// The path is held with the answer so a chosen command finds the same
	// server, and the list is cleared so a stale row cannot index a new answer
	// before applyCodeActions installs it.
	a.codeActionPath = path
	a.codeActionList = nil

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		actions, err := lsp.RequestCodeActions(ctx, conn, path, rng, ctxDiags)
		if err != nil {
			actions = nil
		}
		a.park(lspAnswer{gen: gen, kind: answerCodeAction, actions: actions, versions: versions})
	})
}

// caretRange is the range a code-action request asks about. The selection is
// sent as-is when there is one; a bare caret asks about its whole line, for the
// reason in codeActions.
func (a *App) caretRange(p *editor.Pane) lsp.Range {
	doc := lsp.NewDocument(p.File.Text())
	c := p.Cursors.Primary()
	if c.HasSelection() {
		lo, hi := c.Range()
		return lsp.Range{Start: doc.Position(lo), End: doc.Position(hi)}
	}
	line, _ := p.File.LineCol(c.Head)
	start := doc.Position(p.File.LineStart(line))
	end := doc.Position(p.File.LineEnd(line))
	return lsp.Range{Start: start, End: end}
}

// applyCodeActions handles a parked code-action answer. A fresh list opens the
// picker; the answer to a codeAction/resolve is applied directly, because the
// user already chose the row and a second list would ask them again.
func (a *App) applyCodeActions(ans lspAnswer) {
	if ans.feature == codeActionResolveFeature {
		a.applyResolvedCodeAction(ans)
		return
	}
	if len(ans.actions) == 0 {
		a.status = "no code actions here"
		return
	}
	a.codeActionList = ans.actions
	a.codeActionVersions = ans.versions
	rows := make([]picker.CodeAction, 0, len(ans.actions))
	for i, act := range ans.actions {
		rows = append(rows, picker.CodeAction{Label: codeActionLabel(act), Index: i})
	}
	a.status = ""
	a.Picker.ShowCodeActions(rows, "Code actions")
	a.focus = FocusPicker
}

// codeActionLabel is the picker row for one action. The title is the whole row
// most of the time; an action that only carries resolve data is marked, because
// choosing it makes a second round trip to the server before anything can be
// applied and the mark is the one warning the user gets.
func codeActionLabel(act lsp.CodeAction) string {
	label := act.Title
	if act.Kind != "" {
		label += "  " + act.Kind
	}
	if act.NeedsResolve() {
		label += "  (needs resolve)"
	}
	return label
}

// runCodeAction carries out the chosen action. An edit goes through the shared
// buffer-edit path, a command is sent to the server, and an action with neither
// is sent through codeAction/resolve: deferring a fix to a second round trip is
// the server's right, and applying nothing while reporting success is the
// failure the refusals in this file exist to prevent.
func (a *App) runCodeAction(i int) {
	if i < 0 || i >= len(a.codeActionList) {
		return
	}
	act := a.codeActionList[i]
	switch {
	case act.Edit != nil:
		a.applyCodeActionEdit(*act.Edit)
	case act.Command != nil:
		a.executeCodeActionCommand(*act.Command)
	default:
		a.resolveCodeAction(act)
	}
}

// codeActionResolveFeature marks a parked answer as the result of a
// codeAction/resolve round-trip rather than a fresh action list. There is no
// dedicated answer kind for it: app/lsp.go's answer union is left untouched and
// applyCodeActions is the one function that reads this field for code actions.
const codeActionResolveFeature = "codeAction.resolve"

// resolveCodeAction asks the server to complete a deferred action and parks the
// answer for the event thread, which applies whatever came back through the
// same edit and command paths a direct action uses.
//
// The version pin is the one the action list was fetched with: the data token
// names an action the server computed against that text, so an edit since then
// makes the resolved edit stale and the apply path refuses it whole rather than
// splicing at shifted offsets. Sending the request from here would be pointless
// in that case, but the gate that matters is the one on the answer.
func (a *App) resolveCodeAction(act lsp.CodeAction) {
	path := a.codeActionPath
	if path == "" {
		a.status = act.Title + " cannot be resolved: no document is known for it; nothing was applied"
		return
	}
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := codeActionGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if msg := codeActionResolveGap(ls.caps.Capabilities); msg != "" {
		a.status = act.Title + " " + msg
		return
	}
	conn := ls.srv.Conn()
	a.lspGen++
	gen := a.lspGen
	versions := a.codeActionVersions
	a.status = "resolving " + act.Title + "\u2026"
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resolved, err := lsp.ResolveCodeAction(ctx, conn, act)
		if err != nil {
			// The resolve failed at the transport. Park a refusal rather than
			// silence: the gesture was deliberate and deserves a word.
			a.park(lspAnswer{
				gen: gen, kind: answerCodeAction, feature: codeActionResolveFeature,
				actions:  []lsp.CodeAction{{Title: act.Title, Data: act.Data}},
				versions: versions,
			})
			return
		}
		a.park(lspAnswer{
			gen: gen, kind: answerCodeAction, feature: codeActionResolveFeature,
			actions:  []lsp.CodeAction{resolved},
			versions: versions,
		})
	})
}

// applyResolvedCodeAction applies the action a codeAction/resolve returned.
// A reply with neither an edit nor a command is refused by name rather than
// reported as success, and the pin captured when the resolve was sent is the
// one applyCodeActionEdit checks.
func (a *App) applyResolvedCodeAction(ans lspAnswer) {
	if len(ans.actions) == 0 {
		a.status = "the codeAction/resolve returned nothing; nothing was applied"
		return
	}
	act := ans.actions[0]
	if act.Edit == nil && act.Command == nil {
		a.status = act.Title + " resolved to nothing raj can apply; nothing was applied"
		return
	}
	a.codeActionList = ans.actions
	a.codeActionVersions = ans.versions
	switch {
	case act.Edit != nil:
		a.applyCodeActionEdit(*act.Edit)
	case act.Command != nil:
		a.executeCodeActionCommand(*act.Command)
	}
}

// executeCodeActionCommand sends a command action to the server.
//
// The executeCommand capability is gated separately from code actions: a server
// can offer actions without accepting commands, and sending one it never
// advertised yields a method-not-found error. The result is usually null; a
// command that edits a buffer would answer with workspace/applyEdit, which raj
// does not yet handle, so the command is sent and its answer discarded rather
// than awaited for text.
func (a *App) executeCodeActionCommand(cmd lsp.Command) {
	path := a.codeActionPath
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := executeCommandGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	conn := ls.srv.Conn()
	a.status = "ran " + cmd.Title
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = lsp.ExecuteCommand(ctx, conn, cmd)
	})
}

// applyCodeActionEdit resolves a WorkspaceEdit and applies it, or refuses it
// whole for the same reasons a rename does: a resource operation cannot be
// performed through the text path, and a target that cannot be loaded would
// leave the action half applied. The edit is the user's own chord, not a
// proposal, so it goes through the ordinary editor path and their save writes
// it, exactly as a rename's edits do.
func (a *App) applyCodeActionEdit(we lsp.WorkspaceEdit) {
	if len(we.Docs) == 0 {
		a.status = "the code action carried no text edits"
		return
	}
	if len(we.ResourceOps) > 0 {
		a.status = "the code action needs file operations raj cannot apply (" +
			strings.Join(we.ResourceOps, ", ") + "); nothing was changed"
		return
	}
	for path, version := range a.codeActionVersions {
		if p, ok := a.paneByPath(path); ok && int(p.File.Session().Version()) != version {
			a.status = "the code action is stale: " + path +
				" changed since the server answered; ask again"
			return
		}
	}

	// Resolve every document before applying any, so a target that cannot be
	// loaded leaves the workspace unchanged rather than half fixed. An open
	// buffer is used as-is; an unopened file is loaded headlessly and announced
	// as a tab, the same trade a rename makes.
	type target struct {
		pane  *editor.Pane
		edits []complete.Edit
	}
	targets := make([]target, 0, len(we.Docs))
	var loaded []*editor.Pane
	for _, d := range we.Docs {
		p, ok := a.paneByPath(d.Path)
		if !ok {
			q, err := a.loadHeadless(d.Path)
			if err != nil {
				for _, l := range loaded {
					a.dropHeadless(l)
				}
				a.status = "the code action would change " + d.Path +
					", which is not open and cannot be loaded; nothing was changed"
				return
			}
			p = q
			loaded = append(loaded, q)
		}
		doc := lsp.NewDocument(p.File.Text())
		edits := make([]complete.Edit, 0, len(d.Edits))
		for _, te := range d.Edits {
			start, end := doc.Span(te.Range)
			edits = append(edits, complete.Edit{Start: start, End: end, Text: te.NewText})
		}
		targets = append(targets, target{pane: p, edits: edits})
	}

	// The leases are checked for every target before any edit lands, so a
	// change set the user has not decided refuses the whole action rather than
	// leaving one file fixed and another untouched.
	for _, t := range targets {
		for _, e := range t.edits {
			if group, leased := t.pane.File.EditLeased(e.Start, e.End-e.Start); leased {
				a.status = leaseNote(group)
				return
			}
		}
	}

	// The original tab stays the one on screen; announcing a touched file adds
	// it to the bar without pulling the view off what the user was reading.
	orig := a.Tabs.Active()
	for _, t := range targets {
		head := t.pane.Cursors.Primary().Head
		applyServerEdits(t.pane, t.edits)
		at := shiftThroughEdits(head, t.edits)
		if n := t.pane.File.Len(); at > n {
			at = n
		}
		if at < 0 {
			at = 0
		}
		t.pane.Cursors.Set(at, at)
		t.pane.FollowCursor()
		a.Explorer.Tree.MarkChanged(t.pane.File.Path)
	}
	for _, p := range loaded {
		a.announceIfHeadless(p)
	}
	if orig != nil && a.Tabs.Focus(orig) {
		a.focus = FocusEditor
	}
	a.status = "applied the code action; save to write"
}

// codeActionGap names why a live server cannot serve code actions, or "" when
// it can. It is the one reading rule capabilityGap applies to the raw provider,
// named here so a test can assert the feature reads CodeActionProvider without
// a live server.
func codeActionGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.CodeActionProvider, "code actions")
}

// codeActionResolveGap names why a live server cannot complete a deferred code
// action, or "" when it can. codeActionProvider alone is not enough: a server
// may offer actions without accepting codeAction/resolve, and sending one it
// never advertised is a method-not-found error rather than a feature. The
// returned clause completes "<action title> ..." in the refusal.
func codeActionResolveGap(caps lsp.ServerCapabilities) string {
	if caps.CodeActionResolve() {
		return ""
	}
	return "needs a codeAction/resolve step the server does not support; nothing was applied"
}

// executeCommandGap names why a live server cannot run a command. It is gated
// separately from code actions: a server can offer actions without accepting
// workspace/executeCommand, and sending one it never advertised is a
// method-not-found error rather than a feature.
func executeCommandGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.ExecuteCommandProvider, "execute command")
}
