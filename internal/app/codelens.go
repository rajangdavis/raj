package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Code lenses are server-supplied actions attached to lines — "3 references",
// "Run test" — and this file owns the request, the store keyed by document
// version, the install that turns them into inline text at the start of their
// line, and the run path behind the chord.
//
// The rendering reuses the inlay-hint lane. A lens is an editor.Hint anchored
// at offset zero, carrying the joined titles, and the shared hint-aware column
// map shifts the line right for it. A lens is not a row of its own because the
// byte-offset row model cannot represent a row with no bytes — the inlay-hint
// work reached the same limit and abandoned it — while an offset-zero hint is
// exactly "before the first character" and already works.

// lensEntry is the last code-lens answer for one document.
type lensEntry struct {
	version int
	lenses  []lsp.CodeLens
	// resolves counts the lazy codeLens/resolve round-trips spent against this
	// answer. The fetch-time fan-out bound now lives at the point of use: a
	// server that answers every resolve with fresh data can be asked at most
	// maxLensResolves times before the run path refuses, so a pathological
	// answer cannot spin requests forever. A fresh answer resets it.
	resolves int
}

// lensStore holds the current lenses for each file.
//
// It mirrors inlayStore: answers arrive on a goroutine and are installed on the
// event thread, so the map is behind a mutex, and an entry carries the document
// version it describes so an answer the buffer has left never reaches the
// renderer.
type lensStore struct {
	mu     sync.Mutex
	byPath map[string]lensEntry
}

func newLensStore() *lensStore {
	return &lensStore{byPath: map[string]lensEntry{}}
}

// set replaces a file's lenses with a fresh answer for a document version. An
// empty answer is stored rather than dropped: the version still says which text
// it describes, and forPath's ok is true with no lenses, meaning the server
// answered "none here" rather than that it was never asked.
func (s *lensStore) set(path string, version int, lenses []lsp.CodeLens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byPath == nil {
		s.byPath = map[string]lensEntry{}
	}
	e := lensEntry{version: version, lenses: lenses}
	// The resolve budget belongs to the answer, not to the install: replacing
	// the list for the same version must not hand a pathological server a fresh
	// budget on every run.
	if old, ok := s.byPath[path]; ok && old.version == version {
		e.resolves = old.resolves
	}
	s.byPath[path] = e
}

// forPath is the last lenses for a path and the version they describe. ok is
// false when no answer has been stored, which is not the same as an answer of
// no lenses.
func (s *lensStore) forPath(path string) (lenses []lsp.CodeLens, version int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byPath[path]
	if !ok {
		return nil, 0, false
	}
	return e.lenses, e.version, true
}

// clear forgets a file's lenses, for when its document is closed.
func (s *lensStore) clear(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byPath, path)
}

// claimResolve spends one lazy resolve against a stored answer and reports
// whether it was within the bound. The answer must still be the current one for
// the version, so a claim for text the buffer has left resolves nothing.
func (s *lensStore) claimResolve(path string, version int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byPath[path]
	if !ok || e.version != version {
		return false
	}
	if e.resolves >= maxLensResolves {
		return false
	}
	e.resolves++
	s.byPath[path] = e
	return true
}

// lensReq is the last lens request: which document, at which version. It is the
// guard that keeps the idle tick from asking the server the same question
// forever. There is no visible range in it because a lens request is
// whole-document — the protocol has no range parameter — so only the text
// version can make a new answer worth requesting.
type lensReq struct {
	path    string
	version int
}

// lensWire is the language-server surface the code-lens path calls. Every field
// is a variable so a test can drive the fetch-and-run ordering without a live
// connection: the app package has no fake LSP server, and "a fetch sends no
// resolve, a run sends exactly one" is otherwise unobservable at this layer.
// The production values are the lsp package functions unchanged.
var lensWire = struct {
	server  func(*App, string) (*langServer, serverState)
	request func(context.Context, *lsp.Conn, string) ([]lsp.CodeLens, error)
	resolve func(context.Context, *lsp.Conn, lsp.CodeLens) (lsp.CodeLens, error)
}{
	server: func(a *App, path string) (*langServer, serverState) {
		return a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	},
	request: lsp.RequestCodeLenses,
	resolve: lsp.ResolveCodeLens,
}

// maxLensResolves bounds the resolve work one fetched answer can cost. Lenses
// are resolved lazily, one per run, so the bound is no longer a fetch-time
// fan-out; it is the ceiling on resolve round-trips the run path will spend
// against a single answer, which stops a server that returns fresh data on
// every resolve from spinning forever. Past it the run path refuses by name.
const maxLensResolves = 32

// maybeRequestLenses asks the language server for the active pane's lenses when
// its document version has moved.
//
// It is called only from the idle tick and mirrors maybeRequestHints: the guard
// means a still buffer asks once, an edit moves the version and asks again, and
// an answer that lands after the text moved is dropped by applyLenses. Unlike
// hints there is no visible range to track, so the guard is path and version.
func (a *App) maybeRequestLenses(p *editor.Pane) {
	if p == nil || a.mode != ModeEdit {
		a.lensReq = lensReq{}
		return
	}
	path := a.docPath(p)
	if path == "" {
		a.lensReq = lensReq{}
		return
	}
	version := int(p.File.Session().Version())
	if a.lensReq.path == path && a.lensReq.version == version {
		return
	}
	ls, _ := lensWire.server(a, path)
	if ls == nil {
		// No server, or one still starting. The guard is left as it was — it
		// only records a request that actually happened — so the next tick
		// tries again and the lenses appear once the server is ready.
		return
	}
	if msg := codeLensGap(ls.caps.Capabilities); msg != "" {
		// The server does not serve lenses. Record the guard so the gate is not
		// re-evaluated every tick, and drop anything already on screen: a
		// server that changed its answer must not leave stale text behind.
		a.lensReq = lensReq{path: path, version: version}
		p.File.ClearLenses()
		a.lensGen++
		a.lenses.clear(path)
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.lensReq = lensReq{path: path, version: version}
	a.lensGen++
	gen := a.lensGen
	conn := ls.srv.Conn()
	safe.Go(func() { a.fetchLenses(gen, path, version, conn) })
}

// fetchLenses requests a document's lenses and parks them as they arrived.
//
// Nothing is resolved here. A data-only lens is stored unresolved and drawn as
// a placeholder; resolving it in this goroutine would pay a round trip per lens
// for every fetch, including the lenses the user never runs. The resolve
// happens on the run, against the one lens the caret is on.
func (a *App) fetchLenses(gen int, path string, version int, conn *lsp.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lenses, _ := lensWire.request(ctx, conn, path)
	a.park(lspAnswer{
		gen:         gen,
		kind:        answerCodeLens,
		path:        path,
		lensVersion: version,
		lenses:      lenses,
	})
}

// applyLenses installs a lens answer on the active pane.
//
// The same three things must hold as for a hint answer: the request has not
// been superseded, the pane is still the one that asked, and the text is still
// the version the server answered for. A lens anchored to a moved line is not
// merely wrong text, it is on the wrong line.
func (a *App) applyLenses(ans lspAnswer) {
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
	if int(p.File.Session().Version()) != ans.lensVersion {
		if ans.text == lensResolveRunMarker {
			a.status = "the buffer changed before the code lens could run"
		}
		return
	}
	p.File.SetLensesFiltered(lensLineHints(ans.lenses), p.TextWidth())
	a.lenses.set(path, ans.lensVersion, ans.lenses)
	// A resolve answer carries the run: the text is still the version the
	// server answered for, so the resolved command may go, and a resolve that
	// produced no command is refused by name in runOneCodeLens rather than
	// reported as success.
	if ans.text == lensResolveRunMarker {
		if lens, ok := lensAtLine(ans.lenses, ans.line); ok {
			a.runOneCodeLens(path, lens)
		}
	}
}

// lensLineHints converts a lens answer to the inline runs the renderer draws:
// one run per line, at offset zero, holding every lens title on that line
// joined with a separator.
//
// One run per line rather than one per lens keeps a line's lenses together in a
// single Hint, which is what the column map and the width fit already handle. A
// lens whose resolve has not happened yet contributes a placeholder rather than
// nothing, so the line shows that the server offered something and the run
// chord has a target.
func lensLineHints(lenses []lsp.CodeLens) []editor.LineHint {
	byLine := map[int][]string{}
	var order []int
	for _, l := range lenses {
		title := lensText(l)
		if title == "" {
			continue
		}
		line := l.Range.Start.Line
		if _, ok := byLine[line]; !ok {
			order = append(order, line)
		}
		byLine[line] = append(byLine[line], title)
	}
	out := make([]editor.LineHint, 0, len(order))
	for _, line := range order {
		out = append(out, editor.LineHint{
			Line: line,
			Hint: editor.Hint{
				Off:   0,
				Text:  strings.Join(byLine[line], "    "),
				Left:  true,
				Right: true,
				Lens:  true,
			},
		})
	}
	return out
}

// lensText is the display title of one lens: the command's title when the
// server sent one, otherwise a placeholder that names what it is. A data-only
// lens has no title of its own — the resolve is what produces one — so the
// placeholder is what makes it drawable at all, and the run chord is what turns
// it into its real title.
func lensText(l lsp.CodeLens) string {
	if l.Command == nil {
		if len(l.Data) == 0 {
			return ""
		}
		return "code lens"
	}
	return strings.TrimSpace(l.Command.Title)
}

// invalidateLenses drops the active pane's lenses when its text has moved since
// they were installed.
//
// It runs at the top of every draw for the reason invalidateHints does: an edit
// is not one code path — typed keys, paste, undo, redo, an accept, a control
// write — and comparing the document version against the installed answer
// catches every one before the frame that would have drawn a lens on the wrong
// line. Bumping the generation also discards any answer still in flight.
func (a *App) invalidateLenses() {
	p := a.Tabs.Active()
	if p == nil || p.File.Lenses == nil {
		return
	}
	path := a.docPath(p)
	if a.mode != ModeEdit || path == "" {
		p.File.ClearLenses()
		a.lensGen++
		return
	}
	if _, version, ok := a.lenses.forPath(path); ok && int(p.File.Session().Version()) == version {
		return
	}
	p.File.ClearLenses()
	a.lensGen++
}

// fitLenses reinstalls the active pane's lenses when its text width changes.
//
// It mirrors fitHints: File.Lenses holds only the lenses that fit the width
// recorded with them, because File.LineCol and File.OffsetAt read them without
// knowing the pane, so a resize, a sidebar toggle or a split rebuilds the set
// from the store at the width the renderer is about to use.
func (a *App) fitLenses(p *editor.Pane, width int) {
	if p == nil || width < 1 {
		return
	}
	if a.mode != ModeEdit {
		return
	}
	if p.File.LensWidth() == width {
		return
	}
	lenses, version, ok := a.lenses.forPath(a.docPath(p))
	if !ok || int(p.File.Session().Version()) != version {
		return
	}
	p.File.SetLensesFiltered(lensLineHints(lenses), width)
}

// runCodeLens runs the lens on the caret's line, which is the human path behind
// the chord.
//
// The lookup is by the line the caret is on, not by the range containing it:
// the lens is drawn at the start of its line, so that is where the user aims.
// A line with several lenses runs the first in server order, resolving it first
// if the server deferred its command; picking among them is a later wave.
func (a *App) runCodeLens() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	if path == "" {
		return
	}
	lenses, version, ok := a.lenses.forPath(path)
	if !ok {
		a.status = "no code lenses loaded for this file"
		return
	}
	if int(p.File.Session().Version()) != version {
		a.status = "the code lenses are stale; wait for the next refresh"
		return
	}
	line := p.File.LineOf(p.Cursors.Primary().Head)
	lens, ok := lensAtLine(lenses, line)
	if !ok {
		a.status = "no code lens on this line"
		return
	}
	if lens.NeedsResolve() && len(lens.Data) > 0 {
		a.resolveCodeLens(path, lens, lenses, version, line)
		return
	}
	a.runOneCodeLens(path, lens)
}

// lensAtLine is the first lens whose range starts on line, in the order the
// server sent them. It is pure so the selection rule is testable without a
// server.
func lensAtLine(lenses []lsp.CodeLens, line int) (lsp.CodeLens, bool) {
	for _, l := range lenses {
		if l.Range.Start.Line == line {
			return l, true
		}
	}
	return lsp.CodeLens{}, false
}

// lensResolveRunMarker marks a parked lens answer as a resolve performed for a
// run rather than a fresh fetch. There is no dedicated answer kind: the answer
// is a lens answer in every other respect, so applyLenses is the one function
// that reads this marker and app/lsp.go's answer union stays untouched.
const lensResolveRunMarker = "codeLens.resolve.run"

// resolveCodeLens asks the server to complete the lens on the caret's line and
// parks the answer for the event thread, which installs it and then runs the
// command the resolve filled in.
//
// Resolution is at the point of use, so a resolve-only server pays for its
// lenses only when one is run. The answer is pinned to the document version the
// lens was fetched for, and the store's budget bounds how many lazy resolves
// one fetched answer can spend, so neither a stale answer nor a pathological
// server can do harm. The resolved lens is cached by applyLenses, so a second
// run of the same lens does not ask again.
func (a *App) resolveCodeLens(path string, lens lsp.CodeLens, lenses []lsp.CodeLens, version, line int) {
	ls, st := lensWire.server(a, path)
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := codeLensGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if !ls.caps.Capabilities.CodeLensResolve() {
		a.status = "the code lens needs a codeLens/resolve step the server does not support; nothing ran"
		return
	}
	if msg := executeCommandGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if !a.lenses.claimResolve(path, version) {
		a.status = "too many code-lens resolves for this file; edit it to refresh the lenses"
		return
	}
	a.lensGen++
	gen := a.lensGen
	conn := ls.srv.Conn()
	a.status = "resolving code lens\u2026"
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resolved, err := lensWire.resolve(ctx, conn, lens)
		if err != nil {
			resolved = lens
		}
		// The range is pinned to the one the lens was drawn at: a resolve
		// completes a lens, it does not move it to another line where a command
		// would run for something the user did not point at.
		resolved.Range = lens.Range
		a.park(lspAnswer{
			gen:         gen,
			kind:        answerCodeLens,
			path:        path,
			lensVersion: version,
			lenses:      replaceLensAtLine(lenses, line, resolved),
			line:        line,
			text:        lensResolveRunMarker,
		})
	})
}

// replaceLensAtLine swaps a resolved lens back into the answer at the position
// of the lens it resolves. The answer is in server order, so the first lens on
// the line is the one the run path chose; the resolved lens keeps that range,
// so the lookup that installs it lands on the same line.
func replaceLensAtLine(lenses []lsp.CodeLens, line int, resolved lsp.CodeLens) []lsp.CodeLens {
	out := append([]lsp.CodeLens(nil), lenses...)
	for i := range out {
		if out[i].Range.Start.Line == line {
			out[i] = resolved
			return out
		}
	}
	return out
}

// runOneCodeLens sends a lens's command to the server.
//
// The gates are separate and each speaks before the request: the server must
// still advertise code lenses, the lens must carry a command (a resolve that
// produced none is refused by name rather than run as nothing), and the server
// must accept commands at all — a server can offer lenses without accepting
// workspace/executeCommand, and sending one is a method-not-found error rather
// than a feature.
func (a *App) runOneCodeLens(path string, lens lsp.CodeLens) {
	ls, st := lensWire.server(a, path)
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := codeLensGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if lens.Command == nil {
		a.status = "the code lens needs a codeLens/resolve step that returned no command; nothing ran"
		return
	}
	if msg := executeCommandGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	cmd := *lens.Command
	if cmd.Title == "" {
		cmd.Title = "code lens"
	}
	conn := ls.srv.Conn()
	a.status = "ran " + cmd.Title
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = lsp.ExecuteCommand(ctx, conn, cmd)
	})
}

// codeLensGap names why a live server cannot serve code lenses, or "" when it
// can. It is the one reading rule capabilityGap applies to the raw provider,
// named here so a test can assert the feature reads CodeLensProvider without a
// live server.
func codeLensGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.CodeLensProvider, "code lenses")
}
