package app

import (
	"context"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
)

// Folding ranges are the language server's collapsible regions. The request is
// whole-document and changes only with the text, so it is driven from the idle
// tick and memoised on (path, version) exactly like code lenses and semantic
// tokens. The decoded ranges become reader folds on the pane, which owns which
// are closed and hands the closed byte ranges to the same display projection
// that folds rejected runs — one fold model, two producers.
//
// It never starts a server: a folding range decorates a file the user is
// already editing, and launching a server for a feature nothing asked for is
// the cost this editor deliberately declines to pay.

// foldRequest is the last folding-range request: which document, at which
// version. Folding ranges are whole-document and have no visible window, so
// path and version are the whole guard.
type foldRequest struct {
	path    string
	version int
}

// foldWanted reports whether a request for this path and version is worth
// making given the last one. The path is part of the guard because switching
// tabs must ask about the new file even when its version happens to line up.
func foldWanted(last foldRequest, path string, version int) bool {
	return last.path != path || last.version != version
}

// foldingGap names why a live server cannot serve folding ranges, or "" when it
// can. It is the one reading rule capabilityGap applies to the raw provider,
// named here so a test can assert the feature reads FoldingRangeProvider
// without a live server.
func foldingGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.FoldingRangeProvider, "folding ranges")
}

// maybeRequestFolding asks the language server for the active pane's folding
// ranges when its document version has moved.
//
// It is called from the idle tick, through syncDirtyDocs, so a still buffer
// asks once. The lookup is live, not for_: asking for folds must not start a
// server. A server whose provider is absent or false is gated with the shared
// voice, and the guard is recorded so the gate is not re-evaluated every tick.
func (a *App) maybeRequestFolding(p *editor.Pane) {
	if a == nil || a.servers == nil {
		return
	}
	if p == nil || a.mode != ModeEdit {
		a.servers.foldReq = foldRequest{}
		return
	}
	path := a.docPath(p)
	if path == "" {
		a.servers.foldReq = foldRequest{}
		return
	}
	version := int(p.File.Session().Version())
	if !foldWanted(a.servers.foldReq, path, version) {
		return
	}
	ls := a.servers.live(path)
	if ls == nil {
		// No server, or one still starting. The guard is left as it was — it
		// only records a request that actually happened — so the next tick
		// tries again and the folds appear once the server is ready.
		return
	}
	if msg := foldingGap(ls.caps.Capabilities); msg != "" {
		a.servers.foldReq = foldRequest{path: path, version: version}
		a.servers.foldGen++
		p.ClearFolds()
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.servers.foldReq = foldRequest{path: path, version: version}
	a.servers.foldGen++
	gen := a.servers.foldGen
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ranges, err := lsp.RequestFoldingRanges(ctx, conn, path)
		if err != nil {
			return
		}
		a.park(lspAnswer{
			gen:         gen,
			kind:        answerFolding,
			path:        path,
			foldVersion: version,
			folds:       ranges,
		})
	})
}

// applyFolding installs a folding-range answer on the active pane.
//
// The same three things must hold as for a lens answer: the request has not
// been superseded, the pane is still the one that asked, and the text is still
// the version the server answered for. A range measured on other text names
// lines that have moved, so installing it would fold the wrong region.
func (a *App) applyFolding(ans lspAnswer) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	if path == "" || path != ans.path {
		return
	}
	if a.mode != ModeEdit {
		return
	}
	if int(p.File.Session().Version()) != ans.foldVersion {
		return
	}
	p.SetFolds(editorFolds(p.File, ans.folds), ans.foldVersion)
}

// editorFolds converts the server's line ranges into the pane's byte folds. A
// fold keeps its header line visible and hides the body from the line after the
// header through the end line inclusive, which is the shape the display can
// hide as whole rows. A range with no body — one line, or an end that clamps
// back to its start — is dropped rather than guessed at.
//
// The optional character offsets are not used: the display folds whole rows, so
// a character-precise range is treated as the line range it lies on. A
// same-line character fold therefore has no body and is dropped.
func editorFolds(f *editor.File, ranges []lsp.FoldingRange) []editor.Fold {
	if f == nil || len(ranges) == 0 {
		return nil
	}
	last := f.Lines() - 1
	if last < 0 {
		return nil
	}
	out := make([]editor.Fold, 0, len(ranges))
	seen := map[[2]int]bool{}
	for _, r := range ranges {
		start, end := r.StartLine, r.EndLine
		if start < 0 {
			start = 0
		}
		if end > last {
			end = last
		}
		if start >= end {
			continue
		}
		lo := f.LineStart(start + 1)
		hi := f.Len()
		if end < last {
			hi = f.LineStart(end + 1)
		}
		if hi <= lo {
			continue
		}
		key := [2]int{start, end}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, editor.Fold{StartLine: start, EndLine: end, Lo: lo, Hi: hi})
	}
	return out
}

// toggleFold folds or unfolds the language server's range at the caret. The
// toggle is display-only: it changes what the pane projects, never the text.
// It is Edit-mode only, because Review renders the annotated composition and a
// fold there would hide the very text being reviewed.
func (a *App) toggleFold() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if a.mode != ModeEdit {
		a.status = "folding is an edit-mode view"
		return
	}
	line := p.File.LineOf(p.Cursors.Primary().Head)
	f, ok := p.ToggleFoldAt(line)
	if !ok {
		a.status = "no fold at the cursor"
		return
	}
	// The projection is stale until this rebuild, and FollowCursor reads it, so
	// bring the display up to date before scrolling: otherwise a fold above the
	// caret moves it without the viewport noticing.
	p.UpdateDisplay(a.displayPolicy())
	p.FollowCursor()
	if f.Closed {
		a.status = "folded"
	} else {
		a.status = "unfolded"
	}
}
