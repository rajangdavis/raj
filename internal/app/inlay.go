package app

import (
	"context"
	"sync"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Inlay hints are asked for per document version, and an answer for a version
// the buffer has left must never reach the renderer: a stale hint does not
// merely look wrong, it moves every display column after it. The store is
// therefore keyed by path and carries the text version the server answered for,
// and the apply path drops an answer whose version no longer matches. It
// mirrors diagnostics: answers arrive from a goroutine, so the store is behind
// a mutex.

// inlayEntry is the last hint answer for one document.
type inlayEntry struct {
	version int
	hints   []editor.LineHint
}

// inlayStore holds the current hints for each file.
type inlayStore struct {
	mu     sync.Mutex
	byPath map[string]inlayEntry
}

func newInlayStore() *inlayStore {
	return &inlayStore{byPath: map[string]inlayEntry{}}
}

// set replaces a file's hints with a fresh answer for a document version.
//
// An empty answer is stored rather than dropped: the version still matters,
// because it is what says the answer describes the current text, and forPath's
// ok is true with no hints, meaning the server answered and there are none here
// rather than that it was never asked. A nil slice is stored as-is and reads
// back as no hints.
func (s *inlayStore) set(path string, version int, hints []editor.LineHint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byPath == nil {
		s.byPath = map[string]inlayEntry{}
	}
	s.byPath[path] = inlayEntry{version: version, hints: hints}
}

// forPath is the last hints for a path and the version they describe. ok is
// false when no answer has been stored, which is not the same as an answer of
// no hints.
func (s *inlayStore) forPath(path string) (hints []editor.LineHint, version int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byPath[path]
	if !ok {
		return nil, 0, false
	}
	return e.hints, e.version, true
}

// clear forgets a file's hints, for when its document is closed.
func (s *inlayStore) clear(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byPath, path)
}

// inlayRequest is the last hint request: which document, at which version, for
// which range. It is the guard that keeps the idle tick from asking the server
// the same question every 150 ms.
type inlayRequest struct {
	path    string
	version int
	top     int
	bottom  int
}

// hintsWanted reports whether a request for this path, version and visible
// line range is worth making given the last one. The path is part of the guard
// because switching tabs must ask about the new file even when the version and
// range happen to line up. The range is kept as line bounds rather than an
// lsp.Range so the guard costs no document walk: the positions are derived only
// once a request is actually going to be made.
func (a *App) hintsWanted(path string, version, top, bottom int) bool {
	return a.inlayReq.path != path || a.inlayReq.version != version ||
		a.inlayReq.top != top || a.inlayReq.bottom != bottom
}

// maybeRequestHints asks the language server for the hints covering the visible
// part of a pane.
//
// It is called only from the idle tick. The request is range-scoped and gated
// on a version, so typing does not ask once per keystroke: a tick whose pane has
// not moved since the last request is dropped here, and an answer that lands
// after an edit is dropped by applyInlay because the version has moved.
func (a *App) maybeRequestHints(p *editor.Pane) {
	if p == nil {
		return
	}
	if a.mode != ModeEdit || !a.InlayHints || !p.Hints {
		// Forget the guard with the request: hints turned back on must ask
		// again even though the text has not moved.
		a.inlayReq = inlayRequest{}
		return
	}
	path := a.docPath(p)
	if path == "" {
		a.inlayReq = inlayRequest{}
		return
	}
	version := int(p.File.Session().Version())
	top, bottom := a.hintLines(p)
	if !a.hintsWanted(path, version, top, bottom) {
		return
	}
	ls, _ := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		// No server, or one still starting. The guard is left in place — it
		// only records a request that was actually made — so the next tick
		// tries again and the hints appear once the server is ready.
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	rng := a.hintRange(p, top, bottom)
	a.inlayReq = inlayRequest{path: path, version: version, top: top, bottom: bottom}
	a.inlayGen++
	gen := a.inlayGen
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hints, _ := lsp.RequestInlayHints(ctx, conn, path, rng)
		a.park(lspAnswer{
			gen:          gen,
			kind:         answerInlay,
			path:         path,
			inlayVersion: version,
			hints:        hints,
		})
	})
}

// hintLines is the visible line range a pane asks about: the lines on screen
// plus half a screen of margin on each side, clamped to the file.
//
// The margin is what stops a one-line scroll from having to re-ask before the
// hint it scrolled to has arrived; the clamp keeps a document shorter than the
// pane from asking past its end. It returns lines rather than positions so the
// per-tick guard costs no document walk.
func (a *App) hintLines(p *editor.Pane) (top, bottom int) {
	lines := p.File.Lines()
	top = p.Viewport.Top - p.Viewport.Rows/2
	bottom = p.Viewport.Bottom() + p.Viewport.Rows/2
	if top < 0 {
		top = 0
	}
	if bottom > lines {
		bottom = lines
	}
	if bottom < top {
		bottom = top
	}
	return top, bottom
}

// hintRange converts the line bounds hintLines chose into the byte positions
// the protocol wants. It is called only once a request is going to be made, so
// the document walk is paid per request rather than per idle tick.
func (a *App) hintRange(p *editor.Pane, top, bottom int) lsp.Range {
	doc := lsp.NewDocument(p.File.Text())
	startOff := p.File.LineStart(top)
	endOff := p.File.Len()
	if bottom < p.File.Lines() {
		endOff = p.File.LineStart(bottom)
	}
	return lsp.Range{Start: doc.Position(startOff), End: doc.Position(endOff)}
}

// applyInlay installs a hint answer on the active pane.
//
// Three things must still hold for the answer to describe what is on screen:
// the request has not been superseded, the pane is still the one that asked,
// and the text is still the version the server answered for. A stale hint is
// not merely wrong text — its byte offsets have moved, so it shifts every
// display column after it.
func (a *App) applyInlay(ans lspAnswer) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	if path == "" || path != ans.path {
		return
	}
	if a.mode != ModeEdit || !a.InlayHints || !p.Hints {
		return
	}
	if int(p.File.Session().Version()) != ans.inlayVersion {
		return
	}

	doc := lsp.NewDocument(p.File.Text())
	lineHints := make([]editor.LineHint, 0, len(ans.hints))
	for _, h := range ans.hints {
		line := h.Pos.Line
		if last := p.File.Lines() - 1; line > last {
			line = last
		}
		if line < 0 {
			line = 0
		}
		off := doc.Offset(h.Pos) - p.File.LineStart(line)
		if off < 0 {
			off = 0
		}
		hint := editor.Hint{
			Off:     off,
			Text:    h.Text,
			Left:    h.PaddingLeft,
			Right:   h.PaddingRight,
			Kind:    h.Kind,
			Tooltip: h.Tooltip,
		}
		if len(h.Edits) > 0 {
			hint.Edits = make([]editor.HintEdit, 0, len(h.Edits))
			for _, te := range h.Edits {
				start, end := doc.Span(te.Range)
				hint.Edits = append(hint.Edits, editor.HintEdit{Start: start, End: end, Text: te.NewText})
			}
		}
		lineHints = append(lineHints, editor.LineHint{Line: line, Hint: hint})
	}
	// The store keeps the whole answer; the file gets only the lines whose
	// hints still fit one row, so File.LineCol and the renderer agree. The
	// width is recorded with the set, and a later width change refilters from
	// the store rather than trusting columns measured for another pane.
	p.File.SetHintsFiltered(lineHints, p.TextWidth())
	a.inlays.set(path, ans.inlayVersion, lineHints)
}

// invalidateHints drops the active pane's hints when the feature is off or its
// text has moved since they were installed.
//
// This runs at the top of every draw rather than at each mutation site. An edit
// is not one code path — typed keys, paste, undo, redo, an accept, an applied
// hint edit, a write from the control channel — and a list of hooks grows wrong
// the first time one is missed. The document version is the single fact all of
// them move, so comparing it against the version the installed answer describes
// catches every one before the frame that would have drawn the stale hint.
// Bumping the generation also discards any answer still in flight.
func (a *App) invalidateHints() {
	p := a.Tabs.Active()
	if p == nil || p.File.Hints == nil {
		return
	}
	path := a.docPath(p)
	if a.mode != ModeEdit || !a.InlayHints || !p.Hints || path == "" {
		p.File.ClearHints()
		a.inlayGen++
		return
	}
	if _, version, ok := a.inlays.forPath(path); ok && int(p.File.Session().Version()) == version {
		return
	}
	p.File.ClearHints()
	a.inlayGen++
}

// fitHints reinstalls the active pane hints when its text width has changed.
//
// File.Hints holds only the hints that fit the width recorded with them,
// because File.LineCol and File.OffsetAt read it without knowing the pane. A
// resize, a sidebar toggle or a split moves that width, so the set is rebuilt
// from the store at the width the renderer is about to use. It runs from
// drawEditor, the first point in the frame where the new width exists, rather
// than at the top of Draw, where the pane has not been resized yet and the
// previous frame width would leave the new frame drawing stale columns.
func (a *App) fitHints(p *editor.Pane, width int) {
	if p == nil || width < 1 {
		return
	}
	if a.mode != ModeEdit || !a.InlayHints || !p.Hints {
		return
	}
	if p.File.HintWidth() == width {
		return
	}
	hints, version, ok := a.inlays.forPath(a.docPath(p))
	if !ok || int(p.File.Session().Version()) != version {
		return
	}
	p.File.SetHintsFiltered(hints, width)
}
