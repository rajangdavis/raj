package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/syntax"
	"raj/internal/ui"
)

// Semantic tokens are the language server's own tokenisation: typed runs of the
// document that refine the local highlighter's colours where the server has an
// opinion. This file owns the request, the store keyed by document version, the
// conversion to the line-relative spans the renderer overlays, and the gate.
//
// The overlay never replaces chroma. Where the server sent a token the overlay
// wins; every byte it did not claim keeps the chroma colour, or the terminal
// foreground, so a file whose server has no semantic tokens is indistinguishable
// from one that was never asked. That is the whole design: the server augments
// the base highlight, it does not become the base.

// semanticEntry is the last semantic-token overlay for one document, plus the
// server result it was decoded from: the resultId and flat data array that a
// later full/delta request is computed against.
type semanticEntry struct {
	version int
	set     *editor.SemanticSet
	// baseID and baseData are the server's latest result for the path. They are
	// written whenever a reply arrives, even one whose overlay is dropped for a
	// version the buffer has left, because the resultId still names a state the
	// server can compute a delta from. The pair is stored together so the edits
	// a later reply sends are always applied to the array the resultId describes.
	baseID   string
	baseData []uint32
}

// semanticStore holds the current overlay for each file. It mirrors inlayStore:
// answers arrive on a goroutine and are installed on the event thread, so the
// map is behind a mutex, and an entry carries the document version it describes
// so an answer the buffer has left never reaches the renderer.
type semanticStore struct {
	mu     sync.Mutex
	byPath map[string]semanticEntry
}

func newSemanticStore() *semanticStore {
	return &semanticStore{byPath: map[string]semanticEntry{}}
}

// set replaces a file's overlay with a fresh answer for a document version. A
// nil set is stored rather than dropped: the version still says which text it
// describes, so forPath's ok is true with no overlay, meaning the server
// answered "no tokens here" rather than that it was never asked. The delta base
// is left alone: it belongs to the reply rather than the paint, and is written
// by note.
func (s *semanticStore) set(path string, version int, set *editor.SemanticSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byPath == nil {
		s.byPath = map[string]semanticEntry{}
	}
	e := s.byPath[path]
	e.version = version
	e.set = set
	s.byPath[path] = e
}

// note records the server's latest result for a path: the resultId and the flat
// data array it decoded to. It is called from the request goroutine, which the
// mutex makes safe, and it is what makes a later delta possible. Both fields
// are written together, so a delta reply's edits are always applied to the
// array the resultId it names was decoded from.
func (s *semanticStore) note(path, resultID string, data []uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byPath == nil {
		s.byPath = map[string]semanticEntry{}
	}
	e := s.byPath[path]
	e.baseID = resultID
	e.baseData = data
	s.byPath[path] = e
}

// base returns the last server result for a path, the base of a full/delta
// request. ok is false when no reply has been noted. A resultId may be empty
// even when ok is true, which the caller reads as no base too: a server that
// sent none leaves nothing to chain a delta from.
func (s *semanticStore) base(path string) (string, []uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byPath[path]
	if !ok {
		return "", nil, false
	}
	return e.baseID, e.baseData, true
}

// semanticRefreshes records a server's unconsumed
// workspace/semanticTokens/refresh request. The request arrives on the
// connection's reader goroutine, which must not touch the memo that lives on
// the event thread; this one-bit handoff connects the two. It is keyed by
// server because a refresh names no document: it means every semantic-token
// result that server holds is stale.
var semanticRefreshes sync.Map // map[*langServer]bool

// markSemanticRefresh records that a server asked for a semantic-token refresh
// and wakes the event loop so the next idle tick acts on it. It is safe to call
// from the reader goroutine.
func (ls *langServer) markSemanticRefresh() {
	if ls == nil {
		return
	}
	semanticRefreshes.Store(ls, true)
	if ls.srv != nil && ls.srv.Notify != nil {
		ls.srv.Notify()
	}
}

// takeSemanticRefresh consumes a server's pending refresh request, if there is
// one. It runs on the event thread.
func (ls *langServer) takeSemanticRefresh() bool {
	if ls == nil {
		return false
	}
	_, ok := semanticRefreshes.LoadAndDelete(ls)
	return ok
}

// forPath is the last overlay for a path and the version it describes. ok is
// false when no answer has been stored, which is not the same as an answer of
// no tokens.
func (s *semanticStore) forPath(path string) (*editor.SemanticSet, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byPath[path]
	if !ok {
		return nil, 0, false
	}
	return e.set, e.version, true
}

// clear forgets a file's overlay, for when its document is closed.
func (s *semanticStore) clear(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byPath, path)
}

// semanticReq is the last semantic-token request: which document, at which
// version. A full request is whole-document and has no visible range to track,
// so the version is the only thing that can make a new answer worth asking for.
type semanticReq struct {
	path    string
	version int
}

// semanticWanted reports whether a request for this path and version is worth
// making given the last one. The path is part of the guard because switching
// tabs must ask about the new file even when its version happens to line up.
func (a *App) semanticWanted(path string, version int) bool {
	return a.semanticReq.path != path || a.semanticReq.version != version
}

// semanticGap names why a live server cannot serve semantic tokens, or "" when
// it can. It is the one reading rule capabilityGap applies to the raw provider,
// named here so a test can assert the feature reads SemanticTokensProvider
// without a live server.
func semanticGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.SemanticTokensProvider, "semantic tokens")
}

// maybeRequestSemantic asks the language server for the active pane's semantic
// tokens when its document version has moved, or when the server has asked for
// a refresh.
//
// It is called only from the idle tick and mirrors maybeRequestLenses: the
// guard means a still buffer asks once, an edit moves the version and asks
// again, and an answer that lands after the text moved is dropped by
// applySemantic. A request is a delta against the stored result when there is
// one, and a full request otherwise or when the delta cannot be applied. A
// server whose provider is absent or false is gated with the shared voice, and
// the guard is recorded so the gate is not re-evaluated every tick.
func (a *App) maybeRequestSemantic(p *editor.Pane) {
	if p == nil || a.mode != ModeEdit {
		a.semanticReq = semanticReq{}
		return
	}
	path := a.docPath(p)
	if path == "" {
		a.semanticReq = semanticReq{}
		return
	}
	// A server that asked for a refresh must be re-asked even though the text
	// has not moved: consume the marker, forget the memo and the base, and drop
	// the overlay so the request below is made in full rather than chained from
	// a result the server already said was stale. The marker is consumed only
	// while a server for this path is live, because only then can the tick act.
	if a.servers != nil {
		if ls := a.servers.live(path); ls != nil && ls.takeSemanticRefresh() {
			a.semanticReq = semanticReq{}
			a.semanticGen++
			a.semantics.clear(path)
			p.File.ClearSemantic()
		}
	}
	version := int(p.File.Session().Version())
	if !a.semanticWanted(path, version) {
		return
	}
	ls, _ := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		// No server, or one still starting. The guard is left as it was — it
		// only records a request that actually happened — so the next tick
		// tries again and the tokens appear once the server is ready.
		return
	}
	// The response indices are into the server's legend, so a provider without
	// one cannot be read at all. That is the same "no" as an absent provider:
	// the shared voice names the feature, and the guard is recorded so the gate
	// is not re-evaluated every tick.
	legend, ok := ls.caps.Capabilities.SemanticTokensLegend()
	if msg := semanticGap(ls.caps.Capabilities); msg != "" || !ok {
		a.semanticReq = semanticReq{path: path, version: version}
		a.semanticGen++
		a.semantics.clear(path)
		if p.File.Semantic != nil {
			p.File.ClearSemantic()
		}
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.semanticReq = semanticReq{path: path, version: version}
	a.semanticGen++
	gen := a.semanticGen
	conn := ls.srv.Conn()
	// The stored result is read on the event thread and captured before the
	// goroutine starts, so the request never touches the store map. A missing
	// resultId means there is nothing to diff against and the full method is
	// used; a resultId the server no longer knows is recovered inside
	// RequestSemanticTokens with a full request.
	var previous *lsp.SemanticTokens
	if id, data, ok := a.semantics.base(path); ok && id != "" {
		previous = &lsp.SemanticTokens{ResultID: id, Data: data}
	}
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		res, err := lsp.RequestSemanticTokens(ctx, conn, path, legend, previous)
		if err != nil {
			return
		}
		// The reply is the base of the next request whether or not its overlay
		// is installed: the resultId names a server state, and the data is what
		// a later delta's edits index.
		a.semantics.note(path, res.ResultID, res.Data)
		a.park(lspAnswer{
			gen:             gen,
			kind:            answerSemantic,
			path:            path,
			semanticVersion: version,
			semTokens:       res.Tokens,
		})
	})
}

// applySemantic installs a semantic-token answer on the active pane.
//
// The same three things must hold as for a hint answer: the request has not
// been superseded, the pane is still the one that asked, and the text is still
// the version the server answered for. A token measured on other text has byte
// offsets that have moved, so it would recolour the wrong bytes.
func (a *App) applySemantic(ans lspAnswer) {
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
	if int(p.File.Session().Version()) != ans.semanticVersion {
		return
	}
	doc := lsp.NewDocument(p.File.Text())
	set := editor.NewSemanticSet(semanticSpans(doc, ans.semTokens))
	p.File.SetSemantic(set)
	a.semantics.set(path, ans.semanticVersion, set)
}

// semanticSpans converts decoded tokens into the line-relative byte spans the
// renderer overlays, using the same position mapping every other LSP answer
// goes through. The response's characters are UTF-16 code units unless the
// encoding negotiation changes that; this assumes the protocol default.
//
// A token whose type the palette has no role for is skipped, leaving the chroma
// colour in place; that is the honest answer for an unknown or out-of-range
// legend index, and it is the only one that cannot be mistaken for a correct
// colour. A token that crosses a line is skipped too: the client tells the
// server it does not support multi-line tokens, so one crossing a line is
// malformed, and splitting or clamping it would paint a span the server did not
// describe.
func semanticSpans(doc *lsp.Document, tokens []lsp.SemanticToken) map[int][]syntax.Span {
	if len(tokens) == 0 {
		return nil
	}
	byLine := map[int][]syntax.Span{}
	for _, tok := range tokens {
		style, ok := syntax.SemanticStyle(tok.Type)
		if !ok {
			continue
		}
		if tok.HasModifier("deprecated") {
			// Deprecated is the one modifier with a role the palette can carry
			// without inventing one: a strike-through marks it while the type's
			// colour still says what it is.
			style = style.Plus(ui.Strike)
		}
		lo, hi := doc.SpanFrom(tok.Start, tok.Length)
		if hi <= lo {
			continue
		}
		line := tok.Start.Line
		if doc.Position(hi).Line != line {
			continue // a multi-line token we told the server not to send
		}
		lineStart := doc.Offset(lsp.Position{Line: line})
		byLine[line] = append(byLine[line], syntax.Span{
			Start: lo - lineStart,
			End:   hi - lineStart,
			Style: style,
		})
	}
	// Spans on a line are kept in start order so the overlay reads top to
	// bottom the way the chroma tokens do. StyleAt is a linear scan that does
	// not need the order, but a stable order makes the set inspectable and the
	// tests exact.
	for line, spans := range byLine {
		sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
		byLine[line] = spans
	}
	return byLine
}

// invalidateSemantic drops the active pane's overlay when its text has moved
// since it was installed, and re-installs it from the store when the pane has
// just switched to a file whose answer is still current.
//
// It runs at the top of every draw for the reason invalidateHints does: an edit
// is not one code path — typed keys, paste, undo, redo, an accept, a control
// write — and comparing the document version against the version the installed
// answer describes catches every one before the frame that would have painted a
// token on the wrong bytes. The overlay is not width-dependent, so the tab
// switch re-install rides here rather than on a resize hook.
func (a *App) invalidateSemantic() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	set, version, ok := a.semantics.forPath(path)
	if a.mode != ModeEdit || path == "" || !ok || int(p.File.Session().Version()) != version {
		if p.File.Semantic != nil {
			p.File.ClearSemantic()
			a.semanticGen++
		}
		return
	}
	if p.File.Semantic != set {
		p.File.SetSemantic(set)
	}
}
