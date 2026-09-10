package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/ui"

	"raj/internal/safe"
)

// Language server integration.
//
// The rule everything here follows is that no language feature may make the
// editor worse when it is unavailable. A server that is missing, slow, crashed
// or confused produces no answer and no interruption — never a stall, never an
// error the user has to dismiss, never a modal waiting on a subprocess.

// servers is the language servers for this workspace, one per language.
//
// Started on the first request that needs one rather than at launch: most
// sessions never ask for a hover, and paying gopls's startup on every launch to
// serve the sessions that do is the wrong trade. It also means a broken server
// costs nothing until it is asked for.
type servers struct {
	root string
	mu   sync.Mutex
	byID map[string]*langServer
}

type langServer struct {
	srv  *lsp.Server
	sync *lsp.Sync
	caps lsp.InitializeResult
	// starting guards against a second start while the handshake is running,
	// which is easy to trigger by pressing the key twice.
	starting bool
}

// command is the server to run for a language. Only the ones that are
// installed as a single binary with no configuration are listed: a language
// server that needs a config file to start is a setup problem raj should not
// pretend to solve silently.
var command = map[string][]string{
	"go":              {"gopls"},
	"rust":            {"rust-analyzer"},
	"python":          {"pylsp"},
	"typescript":      {"typescript-language-server", "--stdio"},
	"typescriptreact": {"typescript-language-server", "--stdio"},
	"javascript":      {"typescript-language-server", "--stdio"},
	"javascriptreact": {"typescript-language-server", "--stdio"},
	"ruby":            {"solargraph", "stdio"},
	"c":               {"clangd"},
	"cpp":             {"clangd"},
}

func newServers(root string) *servers {
	return &servers{root: root, byID: map[string]*langServer{}}
}

// serverState is why there is or is not a server, which the caller turns into
// a message. The distinction matters because the four reasons need four
// different reactions from the user and only one of them is "nothing to be
// done": install something, wait a moment, look at why it keeps dying, or
// accept that this file type has no server.
type serverState int

const (
	serverReady    serverState = iota // usable now
	serverStarting                    // handshaking; ask again shortly
	serverMissing                     // the binary is not on PATH
	serverNone                        // no server is configured for this language
	serverGaveUp                      // it kept failing and will not be retried
)

// running returns the server already handling a path's language, without
// starting one. Closing a document must not spawn a language server: the tab is
// going away, there is nothing left to ask about it, and for_ would start one
// only to be told immediately to forget the only file it had been given.
func (s *servers) running(path string) *langServer {
	id := lsp.LanguageID(path)
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.byID[id]
	if ls == nil || ls.sync == nil {
		return nil
	}
	return ls
}

// for_ returns the server for a path's language, starting it if needed, along
// with why it is or is not available.
func (s *servers) for_(path string, notify func()) (*langServer, serverState) {
	id := lsp.LanguageID(path)
	if id == "" {
		return nil, serverNone
	}
	argv, ok := command[id]
	if !ok {
		return nil, serverNone
	}
	// Checked before spawning so a missing binary is reported as missing
	// rather than as a start failure that burns a restart attempt and then
	// says something vaguer.
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, serverMissing
	}

	s.mu.Lock()
	ls := s.byID[id]
	if ls == nil {
		ls = &langServer{srv: &lsp.Server{
			Command: argv[0], Args: argv[1:], Dir: s.root, Notify: notify,
		}}
		s.byID[id] = ls
	}
	starting := ls.starting
	live := ls.srv.Conn() != nil && ls.sync != nil
	needsStart := !live && !starting && ls.srv.ShouldRestart(time.Now())
	if needsStart {
		ls.starting = true
	}
	s.mu.Unlock()

	switch {
	case live:
		return ls, serverReady
	case needsStart:
		go s.start(ls, id)
		return nil, serverStarting
	case starting:
		return nil, serverStarting
	case ls.srv.GaveUp():
		return nil, serverGaveUp
	default:
		// Between attempts, waiting out the backoff. Reported as starting
		// because that is what it looks like from outside and what the user
		// should do about it: try again.
		return nil, serverStarting
	}
}

// message is what to show for a state, or "" when there is nothing to say.
func (st serverState) message(path string) string {
	switch st {
	case serverStarting:
		return "starting language server\u2026"
	case serverMissing:
		if argv, ok := command[lsp.LanguageID(path)]; ok {
			return argv[0] + " not found on PATH"
		}
		return "language server not found"
	case serverGaveUp:
		return "language server kept failing; not retrying"
	case serverNone:
		return "no language server for this file type"
	}
	return ""
}

// start performs the handshake off the event thread, because it can take tens
// of seconds against a large repository and the editor may not stop for it.
func (s *servers) start(ls *langServer, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := ls.srv.Start(ctx, lsp.URI(s.root), clientCapabilities())
	s.mu.Lock()
	ls.starting = false
	if err == nil && res != nil {
		ls.caps = *res
		ls.sync = lsp.NewSync(ls.srv.Conn(), lsp.SyncKindOf(res.Capabilities.TextDocumentSync))
	}
	s.mu.Unlock()
}

// stopAll shuts every server down. Called when the editor quits, because an
// editor that leaves language servers running is a bug people find in their
// process list rather than in the editor.
func (s *servers) stopAll() {
	s.mu.Lock()
	all := make([]*langServer, 0, len(s.byID))
	for _, ls := range s.byID {
		all = append(all, ls)
	}
	s.byID = map[string]*langServer{}
	s.mu.Unlock()
	for _, ls := range all {
		ls.srv.Stop()
	}
}

// clientCapabilities is what raj can actually do. Claiming more than that makes
// servers send richer responses that are then discarded — snippet completions
// arrive as templates raj would insert literally, and markdown hovers arrive
// with formatting a cell grid cannot show.
func clientCapabilities() map[string]any {
	return map[string]any{
		"textDocument": map[string]any{
			"hover":      map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
			"definition": map[string]any{"linkSupport": true},
			"synchronization": map[string]any{
				"didSave": true,
			},
			"completion": map[string]any{
				"completionItem": map[string]any{
					"snippetSupport": false,
				},
			},
		},
	}
}

// syncDoc tells the server about a pane's current contents. It is called before
// a request rather than on every keystroke: the server only needs to be current
// at the moment it is asked something, and telling it on every keystroke would
// send a whole document per character.
func (a *App) syncDoc(ls *langServer, p *editor.Pane) bool {
	path := a.docPath(p)
	if ls == nil || ls.sync == nil || path == "" {
		return false
	}
	id := lsp.LanguageID(path)
	if id == "" {
		return false
	}
	sess := p.File.Session()
	version := int(sess.Version())
	text := p.File.Text()
	if !ls.sync.IsOpen(path) {
		return ls.sync.Open(path, id, text, version) == nil
	}
	// The journal window since the version the server last saw is the edit
	// history, already in application order. A window that cannot be read —
	// none, or a version the journal no longer reaches — is nil edits, which
	// the sync layer reads as "history unavailable" and pays the whole
	// document for.
	last, _ := ls.sync.Version(path)
	ls.sync.Change(path, text, version, editsSince(sess, piecetable.Version(last)))
	return true
}

// editsSince renders the journal window (since, present] as LSP edits, in
// application order. Undo and redo ops convert the same way as edits: each
// carries the range it replaces and the pieces it inserts in the frame its
// predecessors produced, which is the frame the server applies the change to.
func editsSince(sess *piecetable.Session, since piecetable.Version) []lsp.Edit {
	ops := sess.OpsSince(since)
	if len(ops) == 0 {
		return nil
	}
	store := sess.Store()
	edits := make([]lsp.Edit, 0, len(ops))
	for _, o := range ops {
		edits = append(edits, lsp.Edit{
			Start: o.Pos,
			End:   o.Pos + o.DelLen(),
			Text:  recsText(store, o.Ins),
		})
	}
	return edits
}

// recsText reads the bytes a piece list spans out of the stores. Nothing is
// ever erased, so the pieces an op inserted still read back exactly.
func recsText(store *piecetable.Store, recs []piecetable.PieceRec) string {
	var b strings.Builder
	for _, r := range recs {
		b.Write(store.Slice(piecetable.Author(r.Buf), r.Start, r.Length))
	}
	return b.String()
}

// docPath is a pane's path made absolute.
//
// A URI must name a file the server can open, and a relative path resolves
// against the server's working directory rather than the editor's idea of the
// workspace — which produces file://internal/editor/actions.go, a URI with a
// host of "internal" that names nothing. Every path handed to LSP goes through
// here.
func (a *App) docPath(p *editor.Pane) string {
	if p == nil || p.File.Path == "" {
		return ""
	}
	if filepath.IsAbs(p.File.Path) {
		return p.File.Path
	}
	return filepath.Join(a.root, p.File.Path)
}

// An answer is parked where the event thread already looks and a Wake is
// posted, which is the same shape the search pane uses. Applying it from the
// request goroutine would touch panes and the screen from somewhere that must
// not; ui.Event is sealed, so a result cannot be an event either.
type lspAnswer struct {
	gen    int
	kind   int
	text   string
	locs   []lsp.Location
	items  []lsp.CompletionItem
	prefix string
	// incomplete is the server's isIncomplete flag, carried rather than dropped
	// so the event thread can decide whether the answer is cacheable. A
	// complete list is everything; a truncated one has to be asked for again.
	incomplete bool
	// line and col anchor the word the answer describes, so a cached list can
	// be matched against a later keystroke's word rather than only its text.
	line, col int
}

const (
	answerHover = iota
	answerDefinition
	answerCompletion
)

// park stores an answer and wakes the event loop.
func (a *App) park(ans lspAnswer) {
	a.lspMu.Lock()
	a.lspAnswer = &ans
	a.lspMu.Unlock()
	a.host.Post(ui.Wake{})
}

// takeAnswer collects a parked answer, if there is one. Event thread only.
func (a *App) takeAnswer() *lspAnswer {
	a.lspMu.Lock()
	ans := a.lspAnswer
	a.lspAnswer = nil
	a.lspMu.Unlock()
	return ans
}

// applyAnswer routes a parked answer, dropping anything the cursor has moved
// past. Called from the event loop after every event.
func (a *App) applyAnswer() {
	ans := a.takeAnswer()
	if ans == nil {
		return
	}
	// Two generations, because the two kinds are superseded by different
	// things: a hover is stale once the cursor moves, a completion once the
	// typing moves on. Checking one counter for both made every completion
	// answer look stale, which is silent — the buffer words stay up and the
	// server's answer simply never appears.
	current := a.lspGen
	if ans.kind == answerCompletion {
		current = a.completeGen
	}
	if ans.gen != current {
		return
	}
	switch ans.kind {
	case answerHover:
		a.applyHover(*ans)
	case answerDefinition:
		a.applyDefinition(*ans)
	case answerCompletion:
		a.applyCompletion(*ans)
	}
}

// closeDoc forgets everything that was keyed on a pane's path.
//
// Two things outlive a closed tab if nothing does this. The server keeps the
// document open and keeps publishing about it, and the diagnostics store keeps
// the last set it published — counted in nothing visible, since the status line
// only reports the active file, but held for as long as the session runs and
// resurrected the moment the file is reopened, stale.
//
// It runs before the tab is removed rather than after, because the path is read
// off the pane and a removed pane is one nobody can be asked about.
func (a *App) closeDoc(p *editor.Pane) {
	path := a.docPath(p)
	if path == "" {
		return // an unnamed buffer was never opened with a server
	}
	if ls := a.servers.running(path); ls != nil {
		// Best effort: a server that has already exited has nothing to forget,
		// and failing to tell it so is not something the user can act on.
		_ = ls.sync.Close(path)
	}
	a.diags.clear(path)
	a.refreshProblems()
}

// hover asks what is under the cursor.
//
// The generation is the cancellation seam, and it is the same shape the search
// pane uses: an answer whose generation has moved on is dropped, because a
// hover for a position the cursor has left is worse than no hover — it is shown
// as though it described where the cursor is now.
func (a *App) hover() {
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
	if !a.syncDoc(ls, p) {
		return
	}

	a.lspGen++
	gen := a.lspGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	conn := ls.srv.Conn()
	// Captured now rather than read when the answer lands, so the panel is
	// pinned to the position that was asked about. Reading the cursor later
	// would anchor the box to wherever it had got to.
	line, col := p.File.LineCol(head)

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		h, err := lsp.RequestHover(ctx, conn, path, pos)
		text := ""
		if err == nil && h != nil {
			text = h.Text
		}
		a.park(lspAnswer{gen: gen, kind: answerHover, text: text, line: line, col: col})
	})
}

// gotoDefinition jumps to where the thing under the cursor is defined.
func (a *App) gotoDefinition() {
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
	if !a.syncDoc(ls, p) {
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
		locs, _ := lsp.RequestDefinition(ctx, conn, path, pos)
		a.park(lspAnswer{gen: gen, kind: answerDefinition, locs: locs})
	})
}

// applyHover shows an answer, if it is still the answer to the current question.
//
// A floating panel rather than the status line. Folded onto one row, a
// signature loses the shape that makes it readable and anything longer than the
// terminal is simply cut; the status line remains where the "nothing to say"
// case goes, since an empty box is worse than a word.
func (a *App) applyHover(r lspAnswer) {
	if r.text == "" {
		a.Hover.Hide()
		a.status = "no hover information here"
		return
	}
	a.status = ""
	a.Hover.Show(r.text, r.line, r.col)
}

// applyDefinition opens the first result.
func (a *App) applyDefinition(r lspAnswer) {
	if len(r.locs) == 0 {
		a.status = "no definition found"
		return
	}
	loc := r.locs[0]
	a.OpenFile(loc.Path)
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	off := lsp.NewDocument(p.File.Text()).Offset(loc.Range.Start)
	p.Cursors.Set(off, off)
	p.FollowCursor()
	a.status = ""
}

// requestCompletion asks the server what could go at the cursor.
//
// The popup is already showing buffer words by the time this is called, and
// that is deliberate: a language server takes tens of milliseconds on a good
// day and hundreds on a cold index, and a completion list that appears a
// noticeable beat after you stop typing feels broken even when it is better.
// Buffer words are instant and usually right; the server's answer replaces them
// when it arrives, and is dropped if the prefix has moved on.
func (a *App) requestCompletion(p *editor.Pane, prefix string, line, col int) {
	path := a.docPath(p)
	ls, _ := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil || !a.syncDoc(ls, p) {
		return
	}

	a.completeGen++
	gen := a.completeGen
	pos := lsp.NewDocument(p.File.Text()).Position(p.Cursors.Primary().Head)
	conn := ls.srv.Conn()

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		items, incomplete, err := lsp.Completions(ctx, conn, path, pos)
		if err != nil || len(items) == 0 {
			return
		}
		a.parkCompletion(lspAnswer{
			gen: gen, kind: answerCompletion, items: items, prefix: prefix,
			incomplete: incomplete, line: line, col: col,
		})
	})
}

// parkCompletion stores a completion answer. It uses the same slot as the other
// answers, since only one can be current at a time.
func (a *App) parkCompletion(ans lspAnswer) {
	a.lspMu.Lock()
	a.lspAnswer = &ans
	a.lspMu.Unlock()
	a.host.Post(ui.Wake{})
}

// applyCompletion replaces the buffer-word list with the server's, if the
// prefix has not moved on.
//
// The server's own ordering is kept rather than re-ranked. It encodes scope,
// type compatibility and usage — things the client cannot see — and re-scoring
// it client-side would throw away the reason for asking a language server
// rather than scanning the buffer. Only the prefix filter is applied, to remove
// what the keystrokes since the request excluded.
func (a *App) applyCompletion(ans lspAnswer) {
	p := a.Tabs.Active()
	if p == nil || !a.Complete.Open {
		return
	}
	prefix := a.Complete.Prefix()
	if !strings.HasPrefix(prefix, ans.prefix) {
		return // the prefix changed in a way this answer does not cover
	}
	line, col := a.Complete.Anchor()
	if line != ans.line || col != ans.col {
		return // the popup has moved to a different word since the request
	}
	// Cached only when the server called the list complete, and cached before
	// showing so that a list which filters down to nothing right now is still
	// there for the backspace that widens the prefix again.
	if !ans.incomplete {
		a.cached = completionCache{items: ans.items, prefix: ans.prefix, line: line, col: col}
	}
	// Nothing left after filtering: the buffer words already on screen are
	// better than an empty popup.
	a.showItems(prefix, ans.items, line, col)
}
