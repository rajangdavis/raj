package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/picker"
	"raj/internal/piecetable"
	"raj/internal/ui"
	ws "raj/internal/workspace"

	"raj/internal/safe"
)

// Language server integration.
//
// The rule everything here follows is that no language feature may make the
// editor worse when it is unavailable. A server that is missing, slow, crashed
// or confused produces no answer and no interruption — never a stall, never an
// error the user has to dismiss, never a modal waiting on a subprocess.

// serverKey identifies one server in the table: the workspace root it
// serves and the language it speaks. Two roots with a Go file each are two
// entries, so a lookup that ignored the root would hand one root's files to
// the other's server.
type serverKey struct {
	root string
	lang string
}

// servers is the language servers for this workspace, one per language per
// root.
//
// A server starts on the first request that needs one: most sessions never ask
// for a hover, and paying gopls's startup on every launch to serve the sessions
// that do is the wrong trade. It also means a broken server costs nothing until
// it is asked for. The one exception is WarmServers, which starts the servers
// for the languages of the files already open at launch — a launch with a Go
// file pays gopls's startup up front, in exchange for diagnostics and the
// first request not waiting on a cold handshake.
//
// A server is per (root, language), not per language: the handshake names one
// workspace root, so a file under a second root needs its own server with that
// root's URI and working directory. For a single root there is one server per
// language, exactly as before.
type servers struct {
	roots ws.Roots
	mu    sync.Mutex
	byID  map[serverKey]*langServer

	// highlightReq is the last document-highlight request — path, document
	// version and caret — and highlightGen its cancellation generation. They
	// are the per-workspace analogue of semanticReq/semanticGen, and like
	// those they are touched only on the event thread: the idle tick records a
	// request, the answer install reads it back.
	highlightReq highlightReq
	highlightGen int

	// foldReq is the last folding-range request — path and document version —
	// and foldGen its cancellation generation. Folding is whole-document and
	// text-driven, so path and version are the whole guard.
	foldReq foldRequest
	foldGen int

	// resolveCommand resolves a language's command line from settings and
	// reports whether a server should run at all: false covers both a disabled
	// override and a language with no server. App installs App.lspCommand so
	// the per-language overrides are a cached map read rather than a store read
	// on every request; a servers built without an App (tests) leaves it nil
	// and the built-in command map is the whole answer.
	resolveCommand func(id string) ([]string, bool)
}

type langServer struct {
	// root is the workspace root this server was started for. It is the
	// process's working directory and the workspace URI in the handshake, so
	// two servers for one language do not answer for each other's files.
	root string
	srv  *lsp.Server
	sync *lsp.Sync
	caps lsp.InitializeResult
	// starting guards against a second start while the handshake is running,
	// which is easy to trigger by pressing the key twice.
	starting bool

	// dynamicMu guards dynamic, the methods the server registered after the
	// handshake with client/registerCapability. Registrations arrive on the
	// connection's reader goroutine, so this map has its own lock rather than
	// servers.mu: the reader must not contend with the event loop over the
	// whole server table. A method present here counts as advertised even when
	// initialize did not mention it, which is how some servers register
	// workspace/symbol.
	dynamicMu sync.Mutex
	dynamic   map[string]bool
}

// command is the server to run for a language. Only the ones that are
// installed as a single binary with no configuration are listed: a language
// server that needs a config file to start is a setup problem raj should not
// pretend to solve silently. remark-language-server needs no configuration to
// start, but reports no diagnostics until a .remarkrc exists.
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
	"markdown":        {"remark-language-server", "--stdio"},
}

// serverInitOptions is a language's initializationOptions: the settings a server
// needs to turn on something the editor renders but the server leaves off by
// default. gopls is the case today — every inlay hint is disabled unless asked
// for — so the hint kinds the editor can draw are enabled here. A language with
// nothing to configure gets nil, and Initialize omits the field.
func serverInitOptions(languageID string) any {
	if languageID != "go" {
		return nil
	}
	return map[string]any{
		"hints": map[string]any{
			"assignVariableTypes":    true,
			"rangeVariableTypes":     true,
			"compositeLiteralFields": true,
			"constantValues":         true,
		},
	}
}

// newServers builds the server table for a workspace root set. The roots are
// canonicalised and copied into a ws.Roots, so a later change to the caller's
// slice cannot move a server's root out from under a running handshake and the
// root-for-path rule has one home.
func newServers(roots []string) *servers {
	wsRoots, err := ws.New(roots...)
	if err != nil {
		// Any set the rest of the app accepted is already valid; a caller
		// handing one that is not gets no roots rather than an unusable one.
		wsRoots = ws.Roots{}
	}
	return &servers{roots: wsRoots, byID: map[serverKey]*langServer{}}
}

// lspCommand resolves the command line for a language id, layering the stored
// lsp.<lang>.command/args overrides over the built-in command map. It reports
// false when the language has no server: no override and no built-in, or an
// override whose command is empty, which disables the server for that
// language. The overrides are cached by refreshLSPOverrides, so the request
// path is a map read and never touches the store.
func (a *App) lspCommand(id string) ([]string, bool) {
	a.lspForcedMu.RLock()
	argv, forced := a.lspForced[id]
	a.lspForcedMu.RUnlock()
	if forced {
		return argv, len(argv) > 0
	}
	argv, ok := command[id]
	if !ok || len(argv) == 0 {
		return nil, false
	}
	return argv, true
}

// refreshLSPOverrides rebuilds the cached per-language command overrides from
// the user and workspace scopes. It runs once at construction and again after
// every SetSetting write, so servers.for_ reads a map rather than the store.
// A nil store is not an error: with no scopes the built-ins apply alone.
func (a *App) refreshLSPOverrides() {
	user, workspace := settingScopes(a.state)
	forced := map[string][]string{}
	seen := map[string]bool{}
	scan := func(values map[string]string) {
		for key := range values {
			id, _, ok := parseLPSSettingKey(key)
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			if argv, present := lspOverride(id, user, workspace); present {
				forced[id] = argv
			}
		}
	}
	scan(user)
	scan(workspace)
	a.lspForcedMu.Lock()
	a.lspForced = forced
	a.lspForcedMu.Unlock()
}

// lspOverride resolves one language's override from the two scopes, workspace
// over user. present reports that an lsp.<lang>.* key is set at all; a present
// override with a nil argv means the command is the empty string and the
// server is disabled. A command that is unset falls back to the built-in
// command map, and an explicitly set args list then replaces the built-in
// arguments. Arguments split on whitespace with no quoting: an argument that
// must contain a space is not expressible through this setting.
func lspOverride(id string, user, workspace map[string]string) ([]string, bool) {
	cmd, cmdSet := scopeSetting(user, workspace, lspSettingPrefix+id+"."+lspCommandField)
	args, argsSet := scopeSetting(user, workspace, lspSettingPrefix+id+"."+lspArgsField)
	if !cmdSet && !argsSet {
		return nil, false
	}
	if !cmdSet {
		builtin, ok := command[id]
		if !ok || len(builtin) == 0 {
			return nil, false // args alone name no executable to run
		}
		if !argsSet {
			return append([]string(nil), builtin...), true
		}
		return append(append([]string(nil), builtin[0]), strings.Fields(args)...), true
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil, true // an empty command disables the language
	}
	return append([]string{cmd}, strings.Fields(args)...), true
}

// scopeSetting reads one key the way the resolver layers scopes: a workspace
// value wins when present, else a user value. present distinguishes an empty
// string someone wrote from a key nobody set.
func scopeSetting(user, workspace map[string]string, key string) (string, bool) {
	if v, ok := workspace[key]; ok {
		return v, true
	}
	if v, ok := user[key]; ok {
		return v, true
	}
	return "", false
}

// openLanguages is the distinct language ids of a set of panes, in first-seen
// order. WarmServers starts one server per (language, root) rather than per
// pane: two Go files under one root share a server, a Go file under a second
// root gets its own, and a blank buffer or a file type with no language
// contributes nothing to start. It is pure so the ordering and the skipping can
// be tested without touching PATH or spawning a process.
func openLanguages(panes []*editor.Pane) []string {
	var langs []string
	seen := map[string]bool{}
	for _, p := range panes {
		if p == nil || p.File == nil {
			continue
		}
		id := lsp.LanguageID(p.File.Path)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		langs = append(langs, id)
	}
	return langs
}

// WarmServers starts a server for every (language, root) among the files
// already open, so the first hover, diagnostic or completion does not wait on
// a cold handshake. It is called once from main after the session is restored
// and any file named on the command line is open. It must not be called from
// App.Run or RestoreSession: tests run those, and a warm start there would
// spawn gopls inside a test process.
//
// One server per language is not enough once there are several roots: the
// handshake names the root, so a Go file under each of two roots needs two
// servers. Within a language the first file of each distinct root starts it; a
// second file of the same language and root shares it.
//
// for_ starts asynchronously and returns at once, so this does not block; a
// language with no entry in command is skipped, and no panes is a no-op.
func (a *App) WarmServers() {
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	for _, id := range openLanguages(panes) {
		seen := map[string]bool{}
		for _, p := range panes {
			if p == nil || p.File == nil || lsp.LanguageID(p.File.Path) != id {
				continue
			}
			path := a.docPath(p)
			root := a.servers.rootFor(path)
			if seen[root] {
				continue
			}
			seen[root] = true
			a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
		}
	}
}

// serverState is why there is or is not a server, which the caller turns into
// a message. The distinction matters because the four reasons need four
// different reactions from the user and only one of them is "nothing to be
// done": install something, wait a moment, look at why it keeps dying, or
// accept that this file type has no server.
type serverState int

const (
	serverReady      serverState = iota // usable now
	serverStarting                      // handshaking; ask again shortly
	serverNotStarted                    // available, but nothing has asked for it yet
	serverMissing                       // the binary is not on PATH
	serverNone                          // no server is configured for this language
	serverGaveUp                        // it kept failing and will not be retried
)

// rootFor returns the workspace root that contains path, falling back to the
// primary root when none does.
//
// Roots are not nested, so at most one can contain a path, and the check is
// component-wise — /a/b is not read as inside /a/bc — which is the same
// containment the rest of the app uses. The fallback keeps a file outside
// every root on the primary root's server, which is where the single-root code
// always put it, so one root behaves exactly as before.
func (s *servers) rootFor(path string) string {
	if root := s.roots.RootFor(path); root != "" {
		return root
	}
	return s.roots.Primary()
}

// running returns the server already handling a path's language under the
// path's root, without starting one. Closing a document must not spawn a
// language server: the tab is going away, there is nothing left to ask about
// it, and for_ would start one only to be told immediately to forget the only
// file it had been given.
//
// The lookup resolves the path's root first: a server keyed on the language
// alone would answer for another root's identically-typed file.
func (s *servers) running(path string) *langServer {
	id := lsp.LanguageID(path)
	if id == "" {
		return nil
	}
	key := serverKey{root: s.rootFor(path), lang: id}
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.byID[key]
	if ls == nil || ls.sync == nil {
		return nil
	}
	return ls
}

// live returns the server already running for the language of a path under
// the path's root, without starting one and without checking whether the binary
// is on PATH. It is the liveness test in state with the two things state adds
// for its callers removed -- the PATH search and the reason there is no server
// -- for a caller that only needs to know whether a server is already there.
// The idle sync walks every open pane on every tick, and a PATH search per pane
// per tick is the cost this avoids: a path whose server was never started has
// no entry to find, so nothing is lost.
//
// The root is resolved first, like running: a Go file under root2 must find
// root2's gopls and not root1's.
func (s *servers) live(path string) *langServer {
	id := lsp.LanguageID(path)
	if id == "" {
		return nil
	}
	key := serverKey{root: s.rootFor(path), lang: id}
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.byID[key]
	if ls == nil || ls.srv == nil || ls.sync == nil {
		return nil
	}
	if ls.srv.Conn() == nil {
		return nil
	}
	return ls
}

// argvFor returns the command line to run for a language and whether one
// should run at all. A settings override installed by App wins over the
// built-in command map; with no resolver, the built-ins are the whole answer.
// A nil or empty argv is no server.
//
// The command line is a property of the language, not of a root: the
// lsp.<lang>.command/args overrides are global, so two roots for one language
// run the same argv in two different working directories.
func (s *servers) argvFor(id string) ([]string, bool) {
	if s.resolveCommand != nil {
		return s.resolveCommand(id)
	}
	argv, ok := command[id]
	if !ok || len(argv) == 0 {
		return nil, false
	}
	return argv, true
}

// for_ returns the server for a path's language under the path's root,
// starting it if needed, along with why it is or is not available.
func (s *servers) for_(path string, notify func()) (*langServer, serverState) {
	id := lsp.LanguageID(path)
	if id == "" {
		return nil, serverNone
	}
	argv, ok := s.argvFor(id)
	if !ok || len(argv) == 0 {
		return nil, serverNone
	}
	// Checked before spawning so a missing binary is reported as missing
	// rather than as a start failure that burns a restart attempt and then
	// says something vaguer.
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, serverMissing
	}
	// The root is part of the key: the handshake takes a workspace root, so a
	// file under root2 must be answered by a server started in root2 even when
	// a server for the same language already runs for root1.
	root := s.rootFor(path)
	key := serverKey{root: root, lang: id}

	s.mu.Lock()
	ls := s.byID[key]
	if ls == nil {
		ls = &langServer{root: root, srv: &lsp.Server{
			Command: argv[0], Args: argv[1:], Dir: root, Notify: notify,
			Options: serverInitOptions(id),
		}}
		// The server answers requests through this, including the
		// client/registerCapability that turns on workspace/symbol. It is set
		// before the first Start so a registration cannot arrive before the
		// map it writes exists.
		ls.srv.Handler = ls.handleServerRequest
		s.byID[key] = ls
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
	case serverNotStarted:
		return "language server not running; a hover or completion starts it"
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

	res, err := ls.srv.Start(ctx, lsp.URI(ls.root), clientCapabilities())
	s.mu.Lock()
	ls.starting = false
	if err == nil && res != nil {
		ls.caps = *res
		ls.sync = lsp.NewSync(ls.srv.Conn(), lsp.SyncKindOf(res.Capabilities.TextDocumentSync))
	}
	s.mu.Unlock()
}

// stopAll shuts every server down, across every root. Called when the editor
// quits, because an editor that leaves language servers running is a bug people
// find in their process list rather than in the editor.
func (s *servers) stopAll() {
	s.mu.Lock()
	all := make([]*langServer, 0, len(s.byID))
	for _, ls := range s.byID {
		all = append(all, ls)
	}
	s.byID = map[serverKey]*langServer{}
	s.mu.Unlock()
	for _, ls := range all {
		ls.srv.Stop()
	}
}

// clientCapabilities is what raj can actually do. Claiming more than that makes
// servers send richer responses that are then discarded — markdown hovers, for
// instance, arrive with formatting a cell grid cannot show.
//
// inlayHint is advertised as an empty object: its presence is the whole
// advertisement. resolveSupport is deliberately not sent, so a server includes
// a hint's text edits with the hint rather than deferring them to a resolve
// request raj would have to implement.
//
// foldingRange is advertised as an empty object: the client asks for the
// server's collapsible ranges on the idle tick and folds the ones the user
// closes. Presence is the whole advertisement, as with inlayHint.
//
// A provider has to be named in the handshake before the code that consumes
// it exists. The ones not implemented yet are advertised as presence only,
// because a server should not stream a response this client cannot read.
func clientCapabilities() map[string]any {
	return map[string]any{
		"textDocument": map[string]any{
			"hover":             map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
			"definition":        map[string]any{"linkSupport": true},
			"declaration":       map[string]any{"linkSupport": true},
			"typeDefinition":    map[string]any{"linkSupport": true},
			"implementation":    map[string]any{"linkSupport": true},
			"references":        map[string]any{},
			"documentHighlight": map[string]any{},
			"synchronization": map[string]any{
				"didSave":           true,
				"willSave":          true,
				"willSaveWaitUntil": true,
			},
			"completion": map[string]any{
				"completionItem": map[string]any{
					"snippetSupport": true,
					// documentationFormat names markdown first: the popup
					// renders the markdown subset, so a server that can send
					// either form should send markdown.
					"documentationFormat": []string{"markdown", "plaintext"},
					// resolveSupport names the properties this client consumes
					// from completionItem/resolve. Edits are deliberately not
					// listed: accepting uses the textEdit and additionalTextEdits
					// from the first response, so deferring them would drop the
					// import line rather than save a request.
					"resolveSupport": map[string]any{
						"properties": []string{"documentation", "detail"},
					},
					// The 3.16/3.17 shapes this client reads: labelDetails joins
					// the item's detail, and insertReplaceSupport means a
					// textEdit may carry both an insert and a replace range.
					"labelDetailsSupport":  true,
					"insertReplaceSupport": true,
				},
				// completionList names the itemDefaults this client applies:
				// the edit range, insert format and data a server may hoist out
				// of every item.
				"completionList": map[string]any{
					"itemDefaults": []string{"editRange", "insertTextFormat", "data"},
				},
			},
			"signatureHelp":    map[string]any{},
			"formatting":       map[string]any{},
			"rangeFormatting":  map[string]any{},
			"onTypeFormatting": map[string]any{},
			"rename":           map[string]any{},
			// codeActionLiteralSupport advertises that raj reads CodeAction
			// literals, not only the legacy Command results, and names the kinds
			// it understands. The source.* kinds are deliberately absent: they
			// are whole-file actions and this request is caret-scoped, so a
			// server should not hand one to a feature that would apply it from a
			// caret position.
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{
						"valueSet": []string{
							"",
							"quickfix",
							"refactor",
							"refactor.extract",
							"refactor.inline",
							"refactor.rewrite",
						},
					},
				},
				"resolveSupport": map[string]any{
					"properties": []string{"edit", "command"},
				},
				"dataSupport": true,
			},
			"codeLens": map[string]any{
				"resolveSupport": map[string]any{
					"properties": []string{"command"},
				},
			},
			"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			"documentLink":   map[string]any{},
			// semanticTokens names the token types and modifiers this client
			// can paint, the one coordinate encoding it reads, and the requests
			// it can answer. formats is ["relative"] — named rather than left
			// to the default, so a server never sends the absolute encoding
			// raj would read as relative and misplace. requests claims the full
			// request and its delta form: a full result's resultId is sent back
			// as previousResultId and the reply's edits are applied to the
			// stored data array, falling back to a full request when there is
			// no base or the edits cannot be applied. The range request is
			// implemented in internal/lsp but not sent from the draw path, so
			// it is not claimed.
			"semanticTokens": map[string]any{
				"tokenTypes":     lsp.StandardTokenTypes(),
				"tokenModifiers": lsp.StandardTokenModifiers(),
				"formats":        []string{"relative"},
				"requests": map[string]any{
					"range": false,
					"full":  map[string]any{"delta": true},
				},
			},
			"inlayHint":    map[string]any{},
			"foldingRange": map[string]any{},
		},
		"workspace": map[string]any{
			"symbol":                map[string]any{},
			"configuration":         true,
			"workspaceEdit":         map[string]any{},
			"executeCommand":        map[string]any{},
			"didChangeWatchedFiles": map[string]any{"dynamicRegistration": false},
			// raj is a single-root client: initialize sends rootUri and no
			// workspaceFolders array, and a root change is a new editor
			// session rather than a folder event, so workspace/didChangeWorkspaceFolders
			// is never sent. Announcing false is honest; announcing true made a
			// server expect folder notifications and a workspace/workspaceFolders
			// request this client does not answer.
			"workspaceFolders": false,
		},
	}
}

// capabilityGap names the reason a feature is unavailable, or "" when the
// server advertised the provider. It is the one reading rule every feature's
// gate shares, so an absent provider, a false one and an options object all
// read the same way.
func capabilityGap(provider json.RawMessage, feature string) string {
	if lsp.Supports(provider) {
		return ""
	}
	return "language server does not support " + feature
}

// capabilityGapMethod is capabilityGap extended with dynamic registration: a
// server may register a feature after the handshake instead of advertising it
// in initialize, and the gate has to see that or it refuses a method the server
// would answer. Only workspace/symbol consults it; the rest of the campaign's
// features still read initialize alone and would need the same treatment to
// become dynamically registerable.
func (ls *langServer) capabilityGapMethod(provider json.RawMessage, method, feature string) string {
	if lsp.Supports(provider) || ls.dynamicSupports(method) {
		return ""
	}
	return "language server does not support " + feature
}

// dynamicSupports reports whether the server registered a method with
// client/registerCapability and has not since unregistered it.
func (ls *langServer) dynamicSupports(method string) bool {
	ls.dynamicMu.Lock()
	defer ls.dynamicMu.Unlock()
	return ls.dynamic[method]
}

// handleServerRequest answers a request the server originated. It runs on the
// connection's reader goroutine, so it returns an answer rather than touching
// panes or the screen; anything user-facing goes through the message channel.
//
// Every branch answers, because a server left waiting for a reply stops
// answering everything else. workspace/configuration gets nulls: raj has no
// settings surface to read from, and inventing settings would be worse than
// saying "nothing configured". window/showMessageRequest gets the protocol's
// dismiss answer (null). workspace/applyEdit is refused explicitly with
// applied:false rather than applied unattended. Registration requests update
// the per-language map the capability gate reads. Any unknown request gets
// MethodNotFound, which is what the connection sends with no handler at all.
func (ls *langServer) handleServerRequest(m *lsp.Message) (any, *lsp.ResponseError) {
	switch m.Method {
	case "workspace/configuration":
		return configurationResult(m.Params), nil

	case "window/showMessageRequest":
		// The result is MessageActionItem | null, and null is the dismissal.
		// There is no prompt a reader-goroutine handler could block on, so the
		// honest immediate answer is "dismissed" rather than a hang.
		return nil, nil

	case "workspace/applyEdit":
		// A server-initiated edit is not applied. Rename applies a
		// WorkspaceEdit only because the user asked for that rename; an
		// unattended apply can touch files the user never opened, so the
		// protocol's own refusal is the answer. It is explicit — the server
		// gets applied:false and a reason, not silence.
		return applyEditResult{
			Applied:       false,
			FailureReason: "raj does not apply server-initiated workspace edits; make the edit through a user-requested action",
		}, nil

	case "workspace/semanticTokens/refresh":
		// The request names no document: it means every semantic-token result
		// this server has sent is stale. The reader goroutine must not touch
		// the event-thread memo, so record it on the server and let the next
		// idle tick clear the memo and re-request in full.
		ls.markSemanticRefresh()
		return nil, nil

	case "client/registerCapability":

		ls.registerCapabilities(m.Params)
		return nil, nil

	case "client/unregisterCapability":
		ls.unregisterCapabilities(m.Params)
		return nil, nil

	default:
		return nil, &lsp.ResponseError{
			Code:    -32601,
			Message: "method not found: " + m.Method,
		}
	}
}

// configurationResult answers workspace/configuration with one null per
// requested item, in request order. The client advertised
// workspace.configuration so servers will ask, and with no settings surface the
// truthful value for every section is "unset". A malformed params object yields
// an empty array rather than a guess.
func configurationResult(params json.RawMessage) []any {
	var p struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(params, &p) != nil {
		return []any{}
	}
	return make([]any, len(p.Items))
}

// applyEditResult is the protocol's ApplyWorkspaceEditResult. applied:false
// with a failureReason is how a client refuses an edit it will not perform.
type applyEditResult struct {
	Applied       bool   `json:"applied"`
	FailureReason string `json:"failureReason,omitempty"`
}

// registerCapabilities records the methods from a client/registerCapability
// notification. The documentSelector is deliberately not filtered: the
// registration is stored on the one server that sent it, which is already the
// (root, language) granularity the capability gate works at, so a registration
// is seen by the server it was made against and no other.
func (ls *langServer) registerCapabilities(params json.RawMessage) {
	var p struct {
		Registrations []struct {
			Method string `json:"method"`
		} `json:"registrations"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	ls.dynamicMu.Lock()
	defer ls.dynamicMu.Unlock()
	if ls.dynamic == nil {
		ls.dynamic = map[string]bool{}
	}
	for _, r := range p.Registrations {
		if r.Method != "" {
			ls.dynamic[r.Method] = true
		}
	}
}

// unregisterCapabilities removes the methods from a
// client/unregisterCapability notification. The specification spells the array
// "unregisterations" (its own typo); some implementations send the corrected
// spelling, so both are read.
func (ls *langServer) unregisterCapabilities(params json.RawMessage) {
	var p struct {
		Unregisterations []struct {
			Method string `json:"method"`
		} `json:"unregisterations"`
		Unregistrations []struct {
			Method string `json:"method"`
		} `json:"unregistrations"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	ls.dynamicMu.Lock()
	defer ls.dynamicMu.Unlock()
	for _, r := range p.Unregisterations {
		delete(ls.dynamic, r.Method)
	}
	for _, r := range p.Unregistrations {
		delete(ls.dynamic, r.Method)
	}
}

// drainServerMessages moves every server message into the status line.
//
// Called on the event thread when a Wake arrives, with the same drain shape as
// drainDiagnostics: several notifications can queue between wakes, and the
// status line shows only the last one anyway.
func (a *App) drainServerMessages() {
	a.servers.mu.Lock()
	conns := make([]*lsp.Conn, 0, len(a.servers.byID))
	for _, ls := range a.servers.byID {
		if c := ls.srv.Conn(); c != nil {
			conns = append(conns, c)
		}
	}
	a.servers.mu.Unlock()

	for _, c := range conns {
		for {
			select {
			case m := <-c.ServerMessages:
				a.applyServerMessage(m)
			default:
				goto next
			}
		}
	next:
	}
}

// applyServerMessage puts one server message on the status line.
//
// window/showMessage is user-facing by definition, so every severity is shown.
// window/logMessage is the log channel; only errors and warnings are shown,
// because info and log entries are routine chatter (indexing started, cache
// warmed) that would overwrite the status line set by other features for no
// benefit. Neither opens a panel or a modal: the status line is the least
// invasive surface a human will actually see.
func (a *App) applyServerMessage(m lsp.ServerMessage) {
	if m.Message == "" {
		return
	}
	if m.Method == "window/logMessage" && m.Type > lsp.MessageWarning {
		return
	}
	a.status = m.Message
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
		if err := ls.sync.Open(path, id, text, version); err != nil {
			return false
		}
		// The server has been told about this text. A versionless publish
		// already in the store is older than that and cannot answer for it.
		a.diags.noteSynced(path)
		return true
	}
	// A server already holding this exact text needs no notification, and
	// noting a sync for one would wrongly date its existing publish as stale.
	last, pinned, _ := ls.sync.Pinned(path)
	if version == last && text == pinned {
		return true
	}
	// The journal window since the version the server last saw is the edit
	// history, already in application order. A window that cannot be read —
	// none, or a version the journal no longer reaches — is nil edits, which
	// the sync layer reads as "history unavailable" and pays the whole
	// document for.
	if err := ls.sync.Change(path, text, version, editsSince(sess, piecetable.Version(last))); err != nil {
		return false
	}
	// As above: a versionless publish already in the store predates this sync
	// and cannot be read as describing the text just sent.
	a.diags.noteSynced(path)
	return true
}

// syncDirtyDocs registers each open dirty document with a live server and
// brings one the server already knows about up to the current text of the
// buffer. It runs from the idle tick, so a buffer the user is not looking at is
// registered too: diagnostics are answered from what the server last published,
// and a server that was never told about a document cannot publish for it. A
// clean buffer matches disk, so a server reading the file itself is already
// right and is left closed; only a dirty one is opened. The visible pane already
// reaches a sync through the inlay-hint request; this gives every open pane the
// same guarantee, and runs first so the hint request sees a synced document.
//
// It never starts a server. A server starts on a request that needs one, and
// an idle tick that spawned one for every open tab would turn having a file
// open into a subprocess launch, which is the cost this editor deliberately
// declines to pay. The lookup is live, not for_: it reads the byID table and
// never touches PATH, and a path with no live server is simply skipped.
//
// The version compare in front of a document the server has open is what keeps
// the scan cheap: an unchanged buffer costs one integer comparison, so this can
// run every tick. It does not compare text, because that would materialise every
// open buffer. A reload restarts a session version numbering, so an equal
// version over new text is possible; this guard cannot see that without the
// materialisation it exists to avoid.
//
// The active pane's document highlights are asked for from here as well. They
// are caret-local rather than whole-document, but they ride the same live-server
// lookup, so the idle tick gains one overlay without a second request path.
func (a *App) syncDirtyDocs() {
	for _, p := range a.Tabs.All() {
		a.syncDirtyPane(p)
	}
	// Headless buffers — control-socket proposals among them — follow the same
	// rule, so a live server learns their text too. Neither list can start a
	// server: the lookup is live and finds only one already running.
	for _, p := range a.headless {
		a.syncDirtyPane(p)
	}
	// The active pane's document highlights are caret-local, so they are asked
	// for here rather than with the whole-document overlays: the same tick,
	// keyed to the caret as well as the text. Folding ranges are asked for on
	// the same tick but keyed to the text alone, like the lenses and semantic
	// tokens the app tick requests beside it. Both requests ride only a live
	// server, so this scan still never starts one.
	a.maybeRequestHighlight(a.Tabs.Active())
	a.maybeRequestFolding(a.Tabs.Active())
}

// syncDirtyPane registers or syncs one pane with a live server. A dirty pane
// the server has not opened is opened with the buffer's text; one the server
// already has is pushed when the buffer has moved past the version it saw. A
// pane with no path, no live server, a clean unopened buffer, or an unchanged
// open document is left untouched.
func (a *App) syncDirtyPane(p *editor.Pane) {
	path := a.docPath(p)
	if path == "" {
		return
	}
	ls := a.servers.live(path)
	if ls == nil {
		return
	}
	synced, open := ls.sync.Version(path)
	if !needsSync(open, p.File.ViewDirty(), synced, int(p.File.Session().Version())) {
		return
	}
	a.syncDoc(ls, p)
}

// needsSync reports whether a live server has to be told about a pane's text.
// It is pure so the rule is testable without a language server: a dirty
// document the server has not opened needs a push to register it, and an open
// one needs a push only when its version has moved. A clean buffer matches
// disk, so opening it would tell the server nothing it cannot read itself.
func needsSync(open, dirty bool, syncedVersion, bufVersion int) bool {
	return (!open && dirty) || (open && syncedVersion != bufVersion)
}

// lspSaved tells the live language server that a buffer reached disk.
//
// Two notifications, because servers differ in how they read a save. Changed is
// the watching half: the editor writes with a temp-file rename, which a server's
// own watcher does not always see, so a server that reads the file from disk has
// to be told to drop its cached copy. Save is the synchronisation half, sent
// only for a document the server has open: didSave is what runs the slower
// checks a server skips while typing, and the capability we advertise promises
// it. Both are best-effort — a notification that fails is not a save that
// failed.
func (a *App) lspSaved(p *editor.Pane) {
	if p == nil || p.File == nil {
		return
	}
	path := a.docPath(p)
	if path == "" {
		return // an unnamed buffer was never on disk for a server to read
	}
	// live, not for_: a save is no reason to start a server nothing asked for.
	ls := a.servers.live(path)
	if ls == nil || ls.sync == nil {
		return
	}
	_ = ls.sync.Changed(path)
	if ls.sync.IsOpen(path) {
		_ = ls.sync.Save(path, p.File.Text())
	}
}

// warmSaved starts the language server for a buffer that a save just named (or
// renamed). An unnamed buffer has no language, so the save-as is the first
// moment the server can be chosen; without this, diagnostics and hints only
// appear after a hover/completion or a reopen. An ordinary save of an already-
// named file is not a reason to spawn anything, so the caller warms only on a
// rename.
func (a *App) warmSaved(p *editor.Pane) {
	if p == nil || p.File == nil {
		return
	}
	path := a.docPath(p)
	if path == "" {
		return
	}
	// for_ starts asynchronously and returns nil until ready; a nil server is
	// fine: the idle tick's hint request retries, and this call has started it.
	ls, _ := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls != nil {
		_ = a.syncDoc(ls, p)
	}
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
	return filepath.Join(a.primaryRoot(), p.File.Path)
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
	syms   []lsp.WorkspaceSymbol
	items  []lsp.CompletionItem
	prefix string
	// incomplete is the server's isIncomplete flag, carried rather than dropped
	// so the event thread can decide whether the answer is cacheable. A
	// complete list is everything; a truncated one has to be asked for again.
	incomplete bool
	// line and col anchor the word the answer describes, so a cached list can
	// be matched against a later keystroke's word rather than only its text.
	line, col int
	// path and inlayVersion describe an inlay answer: which document asked and
	// which text version the server answered for, so install can drop one the
	// buffer has already left.
	path         string
	inlayVersion int
	hints        []lsp.InlayHint
	// help carries a signature-help answer: the signatures the server offered
	// and the index it says is active.
	help *lsp.SignatureHelp
	// edits and docVersion carry a formatting answer: the server's TextEdit
	// list and the version of the buffer it was measured on, so install can
	// drop an answer the buffer has moved out from under.
	edits      []lsp.TextEdit
	docVersion int
	// feature, mode and title carry a jump answer: which lookup it was (the
	// feature word), whether several places are listed or the first one is
	// jumped to, and the heading the picker shows.
	feature string
	mode    jumpMode
	title   string
	// lenses and lensVersion carry a code-lens answer: the resolved lenses and
	// the version they were measured on, so install can drop a stale set.
	lenses      []lsp.CodeLens
	lensVersion int
	// folds and foldVersion carry a folding-range answer: the decoded ranges
	// and the document version they name lines in, so install can drop a set
	// the buffer has already left. Folding is whole-document and text-driven,
	// so it carries its own generation — bumped when the idle tick asks for a
	// moved version — rather than the cursor's or the lenses'.
	folds       []lsp.FoldingRange
	foldVersion int
	// semTokens and semanticVersion carry a semantic-token answer: the decoded
	// tokens and the version of the text they were measured on, so install can
	// drop an answer the buffer has already left. The overlay is an edit-driven
	// answer like a lens, so it shares neither the cursor generation nor the
	// panel.
	semTokens       []lsp.SemanticToken
	semanticVersion int
	// highlight, highlightVersion and highlightCaret carry a document-highlight
	// answer: the occurrences the server reported and the caret and text version
	// they were measured at. The overlay is caret-dependent, so the install
	// drops an answer whose caret or version has moved.
	highlight        []lsp.DocumentHighlight
	highlightVersion int
	highlightCaret   int
	// links carries a document-link answer: the document's links plus the line
	// and column the chord asked about them from, so the event thread follows
	// the link the user aimed at even if the caret moved while the server
	// answered.
	links []lsp.DocumentLink
	// file and docSyms carry a document-symbol answer: which document asked and
	// the server's outline of it.
	file    string
	docSyms []lsp.DocumentSymbol
	// actions and versions carry a code-action answer: the server's fixes and
	// the document versions they were computed against, so a stale edit is
	// dropped rather than applied.
	actions  []lsp.CodeAction
	versions map[string]int
	// target carries the prepare half of a rename: the range the server is
	// willing to rename, or nil when it refused the position. text is the
	// refusal wording for the status line.
	target *lsp.RenameTarget
	// edit carries the WorkspaceEdit a rename returned.
	edit *lsp.WorkspaceEdit
	// resolveKey, doc and detail carry a resolved completion item. resolveKey
	// is the opaque item the request was made for, so the answer is applied to
	// that item and no other; doc and detail are what the server filled in.
	resolveKey string
	doc        string
	detail     string
}

const (
	answerHover = iota
	answerDefinition
	answerCompletion
	answerInlay
	answerReferences
	answerSignature
	answerWorkspaceSymbols
	// answerDocumentSymbols carries a file's declarations from the server: a
	// tree the event thread flattens into picker rows, so a hierarchical reply
	// keeps its nesting instead of reading as a flat list.
	answerDocumentSymbols
	// answerJump carries every request whose answer is a set of places:
	// declaration, type definition and implementation. What differs between
	// them is the feature word, the capability gate and whether several places
	// are listed or the first one is jumped to.
	answerJump
	// answerCodeAction carries the fixes and refactors a server offered for the
	// caret. It opens the picker rather than a panel or a jump.
	answerCodeAction
	// answerPrepareRename is the prepare half of a rename: a target, or nil
	// meaning the server refused the position.
	answerPrepareRename
	// answerRename is the WorkspaceEdit a rename returned. It is a mutation
	// rather than an answer about a position, so it is not dropped by the
	// generation counter; the document versions it carries make it stale.
	answerRename
	// answerCodeLens carries a document's code lenses. It installs inline text
	// rather than opening a panel, so it has its own generation: an edit
	// supersedes it, not a cursor move.
	answerCodeLens
	// answerSemantic carries a document's semantic tokens. It installs a
	// colour overlay rather than text, and is superseded by an edit the same
	// way a lens is rather than by a cursor move.
	answerSemantic
	// answerOnType carries an on-type formatting answer: the TextEdit list for
	// the trigger character just typed. It has its own generation because it is
	// superseded by later typing rather than a cursor move, and the install
	// checks the document version as the guard that actually matters.
	answerOnType
	// answerDocumentLink carries the links of one document. It is a position
	// query like hover, so it shares lspGen; the document version checked at
	// install is what keeps a link measured on old text from being followed.
	answerDocumentLink
	// answerFormat carries a whole-document or range formatting answer: the
	// server's TextEdit list, checked against the document version at install.
	answerFormat
	// answerWillSave is the server answer to a save-time willSaveWaitUntil
	// request: edits to apply before the write. It has its own slot rather than
	// the cursor generation, because a save is a mutation, not a query.
	answerWillSave
	// answerCompletionDoc is a resolved completion item: the documentation, and
	// possibly detail, a server deferred from the completion list. It is keyed
	// by the item's opaque resolve key rather than by a generation, because it
	// updates one item wherever it still appears and is harmless if that item
	// is gone.
	answerCompletionDoc
	// answerHighlight carries a document-highlight answer: the occurrences of
	// the symbol at a caret, installed as a caret-pinned background overlay.
	// It is superseded by a caret move or a later edit rather than by either
	// alone, so it carries its own generation and pins the caret and version
	// for the install.
	answerHighlight
	// answerFolding carries a document's folding ranges. It installs reader
	// folds on the pane rather than text. It has its own generation, bumped
	// when a request is made for a moved version, so a cursor move does not
	// drop it and an edit that triggers a new request does.
	answerFolding
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
	// A save answer is a mutation of the save path, not an answer about a
	// position: it is applied before the generation gate and on its own slot,
	// so a cursor move or another request cannot drop a save. It resumes the
	// write the save gesture deferred.
	if ans := a.takeSaveAnswer(); ans != nil {
		a.resumeSave(*ans)
	}
	ans := a.takeAnswer()
	if ans == nil {
		return
	}
	// A resolved completion item is applied by key rather than by generation:
	// it updates the one candidate it names wherever that candidate still
	// appears, and is a harmless no-op if the list has moved on. Gating it on
	// completeGen would drop most answers, because each keystroke bumps it.
	if ans.kind == answerCompletionDoc {
		if a.completionDocs == nil {
			a.completionDocs = map[string]string{}
		}
		a.completionDocs[ans.resolveKey] = ans.doc
		delete(a.resolvePending, ans.resolveKey)
		if a.Complete.Open {
			a.Complete.SetResolved(ans.resolveKey, ans.doc, ans.detail)
		}
		return
	}
	// Inlay answers have their own generation and their own install path: an
	// edit supersedes them rather than a cursor move, and the install has a
	// document version to check as well.
	if ans.kind == answerInlay {
		if ans.gen != a.inlayGen {
			return
		}
		a.applyInlay(*ans)
		return
	}
	// Code-lens answers install inline text the same way, and are superseded
	// the same way: an edit moves the lines the lenses were anchored to, so
	// they share the edit-driven generation rather than the cursor's.
	if ans.kind == answerCodeLens {
		if ans.gen != a.lensGen {
			return
		}
		a.applyLenses(*ans)
		return
	}
	// Semantic-token answers are an overlay keyed to the document version, so
	// they share the edit-driven generation with hints and lenses rather than
	// the cursor's: an edit moves the bytes they were measured on.
	if ans.kind == answerSemantic {
		if ans.gen != a.semanticGen {
			return
		}
		a.applySemantic(*ans)
		return
	}
	// Document-highlight answers are pinned to the caret and the document
	// version, not to the text alone: a caret move supersedes one, so they
	// carry their own generation rather than sharing the edit-driven one.
	if ans.kind == answerHighlight {
		if a.servers == nil || ans.gen != a.servers.highlightGen {
			return
		}
		a.applyHighlight(*ans)
		return
	}
	// Folding answers are whole-document and text-driven, so they are
	// superseded by an edit the way a lens is rather than by a cursor move,
	// and the install checks the document version as the guard that matters.
	if ans.kind == answerFolding {
		if a.servers == nil || ans.gen != a.servers.foldGen {
			return
		}
		a.applyFolding(*ans)
		return
	}
	// On-type formatting has its own generation for the same reason lens and
	// inlay answers do: it is a consequence of typing and is superseded by
	// later typing, not by a cursor move. The install also checks the document
	// version, which is the guard that actually keeps stale offsets out.
	if ans.kind == answerOnType {
		if ans.gen != a.onTypeGen {
			return
		}
		a.applyOnTypeFormat(*ans)
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
	// A rename answer is a mutation, not an answer about a position: it must
	// land even if the user asked something else while the server worked or
	// the caret moved. What makes it stale is the document text, which
	// applyRename checks against the versions pinned at request time.
	if ans.gen != current && ans.kind != answerRename {
		return
	}
	switch ans.kind {
	case answerHover:
		a.applyHover(*ans)
	case answerDefinition:
		a.applyDefinition(*ans)
	case answerCompletion:
		a.applyCompletion(*ans)
	case answerReferences:
		a.applyReferences(*ans)
	case answerSignature:
		a.applySignature(*ans)
	case answerWorkspaceSymbols:
		a.applyWorkspaceSymbols(*ans)
	case answerDocumentSymbols:
		a.applyDocumentSymbols(*ans)
	case answerJump:
		a.applyJump(*ans)
	case answerCodeAction:
		a.applyCodeActions(*ans)
	case answerPrepareRename:
		a.applyPrepareRename(*ans)
	case answerRename:
		a.applyRename(*ans)
	case answerFormat:
		a.applyFormat(*ans)
	case answerDocumentLink:
		a.applyDocumentLink(*ans)
	}
}

// applySignature shows the active signature in the hover panel, with its
// documentation, or says there is none. It reuses the panel hover uses: a
// signature is read, not jumped to, and a second floating box would be a second
// set of placement and scroll rules to keep in step.
func (a *App) applySignature(r lspAnswer) {
	if r.help == nil || len(r.help.Signatures) == 0 {
		a.Hover.Hide()
		a.status = "no signature help here"
		return
	}
	active := r.help.ActiveSignature
	if active < 0 || active >= len(r.help.Signatures) {
		active = 0
	}
	sig := r.help.Signatures[active]
	text := sig.Label
	if sig.Documentation != "" {
		text += "\n\n" + sig.Documentation
	}
	a.status = ""
	a.Hover.Show(text, r.line, r.col)
}

// closeDoc forgets everything that was keyed on a pane's path.
//
// Three things outlive a closed tab if nothing does this. The server keeps the
// document open and keeps publishing about it, the diagnostics store keeps the
// last set it published, and the hint store keeps the last answer. None is
// counted in anything visible for a closed tab — the status line reports only
// the active file — but all are held for as long as the session runs and
// resurrected, stale, the moment the file is reopened.
//
// It runs before the tab is removed rather than after, because the path is read
// off the pane and a removed pane is one nobody can be asked about.
func (a *App) closeDoc(p *editor.Pane) {
	a.closeJournal(p)
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
	a.inlays.clear(path)
	a.lenses.clear(path)
	a.semantics.clear(path)
	// The guard is keyed on a path, so a reopen of the same file at the same
	// version would otherwise be treated as a request already answered. An
	// answer still in flight is dropped by the generation bump.
	if a.inlayReq.path == path {
		a.inlayReq = inlayRequest{}
		a.inlayGen++
	}
	if a.lensReq.path == path {
		a.lensReq = lensReq{}
		a.lensGen++
	}
	if a.semanticReq.path == path {
		a.semanticReq = semanticReq{}
		a.semanticGen++
	}
	if a.servers != nil && a.servers.highlightReq.path == path {
		a.servers.highlightReq = highlightReq{}
		a.servers.highlightGen++
	}
	if a.servers != nil && a.servers.foldReq.path == path {
		a.servers.foldReq = foldRequest{}
		a.servers.foldGen++
	}
	p.File.ClearHighlight()
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

// signatureHelp shows the signature of the call the cursor is inside, with the
// active parameter marked.
//
// It is the same request/sync/generation shape as hover, and it reuses the same
// panel: a signature and a hover are both "what does the server say about this
// position", and a second floating box would be a second set of placement and
// scroll rules to keep in step. It differs in checking the provider first —
// hover asks without a gate, but a signature-help answer is meaningless when
// the server never advertised it, and the gate speaks in the same voice as the
// no-server messages rather than sending a request that returns method-not-found.
func (a *App) signatureHelp() {
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
	if msg := capabilityGap(ls.caps.Capabilities.SignatureHelpProvider, "signature help"); msg != "" {
		a.status = msg
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
	// Captured with the request, like hover: the panel anchors to the call that
	// was asked about, not to wherever the cursor has moved by the time the
	// server answers.
	line, col := p.File.LineCol(head)

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		help, err := lsp.RequestSignatureHelp(ctx, conn, path, pos)
		if err != nil {
			help = nil
		}
		a.park(lspAnswer{gen: gen, kind: answerSignature, help: help, line: line, col: col})
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
	a.jumpToLocation(r.locs[0])
}

// jumpToLocation opens a place and puts the cursor on its start. It is the one
// place the cursor moves for an answer, shared by go-to-definition and the
// sibling jumps so a fix to the UTF-16 conversion or the follow applies to all
// of them.
func (a *App) jumpToLocation(loc lsp.Location) {
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

// findReferences lists every place the symbol under the cursor is used and
// hands the result to the picker, so Enter opens the chosen use. It is the same
// request/sync/generation shape as gotoDefinition; only what happens to the
// locations differs.
//
// A server that did not advertise references is told apart from one with
// nothing to say: the capability gate speaks before the request, in the same
// voice as the no-server messages, rather than asking a server that will answer
// method-not-found.
func (a *App) findReferences() {
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
	if msg := capabilityGap(ls.caps.Capabilities.ReferencesProvider, "references"); msg != "" {
		a.status = msg
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
		locs, _ := lsp.RequestReferences(ctx, conn, path, pos, true)
		a.park(lspAnswer{gen: gen, kind: answerReferences, locs: locs})
	})
}

// applyReferences lists the results in the picker, so Enter opens the chosen
// use. The picker already turns a path plus a 1-based position into a jump
// through openFromPicker — the same seam a symbol choice uses — so there is no
// second listing UI to keep in step with the review and symbol pickers.
func (a *App) applyReferences(r lspAnswer) {
	if len(r.locs) == 0 {
		a.status = "no references found"
		return
	}
	a.showLocations(r.locs, "References")
}

// workspaceSymbols asks the server for every declaration matching a query and
// lists them in the picker. It is the project-wide sibling of the file-local
// GotoSymbol scanner: the scanner reads keywords out of one buffer and needs no
// server, this asks an index that sees every package and ranks by scope.
//
// The human path asks with an empty query and lets the picker's own fuzzy
// filter narrow the rows as the user types. That is the least machinery: the
// picker already filters every mode's rows on each keystroke, so a prompt whose
// only job is to collect text the picker collects anyway is a second focus
// state, a second confirm path and a second thing to keep in step. A query
// still reaches the server on the `raj ctl lsp symbols` path, where the caller
// has one query and no picker to type into.
//
// The gate reads workspaceSymbolProvider from the initialize reply, and also
// accepts a server that registered workspace/symbol dynamically over
// client/registerCapability after the handshake. The dynamic case is the one
// the connection used to ignore, which left such a server reported as
// unsupported even though it would answer.
func (a *App) workspaceSymbols() {
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
	if msg := ls.capabilityGapMethod(ls.caps.Capabilities.WorkspaceSymbolProvider, "workspace/symbol", "workspace symbols"); msg != "" {
		a.status = msg
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}

	a.lspGen++
	gen := a.lspGen
	conn := ls.srv.Conn()

	safe.Go(func() {
		// A cold project index answers in seconds, so this bound is wider than
		// the point-query 3s rather than the same by habit.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		syms, _ := lsp.RequestWorkspaceSymbols(ctx, conn, "")
		a.park(lspAnswer{gen: gen, kind: answerWorkspaceSymbols, syms: syms})
	})
}

// maxWorkspaceSymbols bounds the picker rows built from one answer. gopls
// answers an empty query with every symbol it has, which in a large repository
// is tens of thousands of rows. The cap is here, in the conversion, so the
// driver path (`raj ctl lsp symbols`) still gets the server's whole answer and
// only the overlay is bounded.
const maxWorkspaceSymbols = 1000

// applyWorkspaceSymbols lists the results in the picker, so Enter opens the
// chosen declaration. It reuses the same picker row ShowLocations opens for a
// reference or a jump — the shape it needs (a label, an absolute path and a
// 1-based place) is exactly that row — so there is no second listing UI and no
// new picker mode. The heading names what the list is; the footer still counts
// "references", the one piece of wording the shared mode owns, which a later
// wave can parameterise.
//
// A symbol the server named by file alone gets Line 0, so openFromPicker opens
// the file and leaves the cursor where it was rather than jumping to the top.
func (a *App) applyWorkspaceSymbols(r lspAnswer) {
	if len(r.syms) == 0 {
		a.status = "no symbols found"
		return
	}
	capHint := len(r.syms)
	if capHint > maxWorkspaceSymbols {
		capHint = maxWorkspaceSymbols
	}
	rows := make([]picker.Reference, 0, capHint)
	for _, s := range r.syms {
		if len(rows) >= maxWorkspaceSymbols {
			break
		}
		symRoot := a.rootFor(s.Location.Path)
		if symRoot == "" {
			symRoot = a.primaryRoot()
		}
		row := picker.Reference{Label: symbolRowLabel(symRoot, s), Path: s.Location.Path}
		if s.HasRange {
			row.Line = s.Location.Range.Start.Line + 1
			row.Col = s.Location.Range.Start.Character + 1
		}
		rows = append(rows, row)
	}
	a.status = ""
	if len(r.syms) > maxWorkspaceSymbols {
		a.status = "showing the first " + itoa(maxWorkspaceSymbols) + " of " +
			itoa(len(r.syms)) + " symbols"
	}
	a.Picker.ShowLocations(rows, "Symbols")
	a.focus = FocusPicker
}

// symbolRowLabel is what one picker row shows: the declaration's name, its
// kind, and where it is. The name leads because the picker truncates the tail,
// and the name is what the query is matched against.
func symbolRowLabel(root string, s lsp.WorkspaceSymbol) string {
	rel := s.Location.Path
	if r, err := filepath.Rel(root, s.Location.Path); err == nil {
		rel = r
	}
	label := s.Name
	if k := s.Kind.String(); k != "" {
		label += "  " + k
	}
	label += "  " + rel
	if s.HasRange {
		label += ":" + itoa(s.Location.Range.Start.Line+1) + ":" + itoa(s.Location.Range.Start.Character+1)
	}
	return label
}

// documentSymbols asks a live language server for the active buffer's
// declarations, so the server's parse replaces the keyword scanner when there
// is one to ask. It returns false when there is no live server for this
// language or the running one never advertised documentSymbolProvider, which is
// the signal to fall back to the instant scanner.
//
// It is deliberately server-live, not server-startable: the chord must stay
// instant, and starting gopls from a symbol jump would pay a handshake for a
// list the scanner can draw now. A server warmed by a hover, a diagnostic or
// WarmServers is asked; one that has never run leaves the scanner in charge.
func (a *App) documentSymbols(p *editor.Pane) bool {
	path := a.docPath(p)
	ls := a.servers.live(path)
	if !canAnswerDocumentSymbols(ls) {
		return false
	}
	if !a.syncDoc(ls, p) {
		return false
	}
	a.lspGen++
	gen := a.lspGen
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		syms, _ := lsp.RequestDocumentSymbols(ctx, conn, path)
		a.park(lspAnswer{gen: gen, kind: answerDocumentSymbols, file: path, docSyms: syms})
	})
	return true
}

// canAnswerDocumentSymbols reports whether a live server advertised
// textDocument/documentSymbol. It is the one reading rule applied to the same
// raw capability every other feature gates on, so a server that sent `false`
// is refused exactly like one that stayed silent.
func canAnswerDocumentSymbols(ls *langServer) bool {
	return ls != nil &&
		capabilityGap(ls.caps.Capabilities.DocumentSymbolProvider, "document symbols") == ""
}

// applyDocumentSymbols lists the server's declarations in the picker. The tree
// is flattened depth-first with two spaces of indentation per level, so a
// nested outline reads as one and a child is visibly under its parent rather
// than beside it. It reuses the picker's location rows — a label, an absolute
// path and a 1-based place — because a chosen symbol is a jump like any other.
//
// The row's place is the selection range, the identifier itself, rather than
// the whole declaration: that is what the scanner's line number was standing in
// for, and it is what "go to this symbol" lands on.
func (a *App) applyDocumentSymbols(r lspAnswer) {
	if len(r.docSyms) == 0 {
		a.status = "no symbols found"
		return
	}
	var rows []picker.Reference
	var walk func([]lsp.DocumentSymbol, int)
	walk = func(syms []lsp.DocumentSymbol, depth int) {
		for _, s := range syms {
			row := picker.Reference{Label: documentSymbolRowLabel(s, depth)}
			row.Path = s.Path
			if row.Path == "" {
				row.Path = r.file
			}
			if s.HasRange {
				row.Line = s.SelectionRange.Start.Line + 1
				row.Col = s.SelectionRange.Start.Character + 1
			}
			rows = append(rows, row)
			walk(s.Children, depth+1)
		}
	}
	walk(r.docSyms, 0)
	a.status = ""
	a.Picker.ShowLocations(rows, "Symbols")
	a.focus = FocusPicker
}

// documentSymbolRowLabel is one row's text: the name, its kind, and the
// server's container when it sent one. Indentation carries the nesting a
// hierarchical reply gives; a flat SymbolInformation has no children, so its
// containerName is the only parent it can name and is shown in full.
func documentSymbolRowLabel(s lsp.DocumentSymbol, depth int) string {
	label := strings.Repeat("  ", depth) + s.Name
	if k := s.Kind.String(); k != "" {
		label += "  " + k
	}
	if s.Container != "" {
		label += "  " + s.Container
	}
	return label
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
	a.showItems(p, prefix, ans.items, line, col)
}

// resolveSelectedCompletion asks the server to finish the highlighted item when
// the server deferred the item's documentation.
//
// Servers omit documentation from the completion list to keep it small and fill
// it in only when an item is resolved; the item's data is the handle that lets
// them. Only the selected item is resolved, and the answer is memoised by that
// handle, so arrowing through the list asks once per item rather than once per
// keystroke. A request already in flight is not repeated.
func (a *App) resolveSelectedCompletion() {
	c, ok := a.Complete.Selected()
	if !ok || c.ResolveKey == "" {
		return
	}
	if c.Documentation != "" {
		return
	}
	if doc, ok := a.completionDocs[c.ResolveKey]; ok {
		a.Complete.SetResolved(c.ResolveKey, doc, "")
		return
	}
	if a.resolvePending[c.ResolveKey] {
		return
	}
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	// live, not for_: a completion came from a server, so one is already
	// running, and resolving must not start one.
	ls := a.servers.live(a.docPath(p))
	if ls == nil {
		return
	}
	if a.resolvePending == nil {
		a.resolvePending = map[string]bool{}
	}
	a.resolvePending[c.ResolveKey] = true
	key := c.ResolveKey
	conn := ls.srv.Conn()
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// A failed or timed-out resolve is recorded as empty rather than
		// retried: the item is asked once, and the memo keeps the next arrow
		// key from sending the same request again.
		item, _ := lsp.ResolveCompletion(ctx, conn, json.RawMessage(key))
		a.lspMu.Lock()
		a.lspAnswer = &lspAnswer{
			kind: answerCompletionDoc, resolveKey: key,
			doc: item.Documentation, detail: item.Detail,
		}
		a.lspMu.Unlock()
		a.host.Post(ui.Wake{})
	})
}

// highlightReq is the last document-highlight request for the active pane:
// which file, at which document version, and with the caret at which byte.
// Unlike the whole-document overlays, a caret move makes a new answer worth
// asking for even when the text has not changed.
type highlightReq struct {
	path    string
	version int
	caret   int
}

// highlightWanted reports whether asking for this path, version and caret is
// worth a request given the last one. It is pure so the memo rule is testable
// without a language server.
func highlightWanted(last highlightReq, path string, version, caret int) bool {
	return last.path != path || last.version != version || last.caret != caret
}

// highlightGap names why a live server cannot serve document highlights, or ""
// when it can. It is the one reading rule capabilityGap applies to the raw
// provider, named here so a test can assert the feature reads
// DocumentHighlightProvider without a live server.
func highlightGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.DocumentHighlightProvider, "document highlights")
}

// maybeRequestHighlight asks the language server for the occurrences of the
// symbol at the active pane's caret, when the caret or the text has moved since
// the last request.
//
// It is driven from the idle tick, through syncDirtyDocs, so a still caret asks
// once rather than once per frame. The lookup is live, not for_: a document
// highlight decorates a file the user is already editing, and starting a server
// for it would be the cost this editor deliberately declines to pay for a
// feature nothing asked for. A server whose provider is absent or false is
// gated with the shared voice, and the guard is recorded so the gate is not
// re-evaluated every tick.
//
// The renderer holds the other half of the safety: the installed set carries
// the caret and version it was measured at, so between a move and the next
// answer the old overlay simply does not paint.
func (a *App) maybeRequestHighlight(p *editor.Pane) {
	if a == nil || a.servers == nil {
		return
	}
	if p == nil {
		a.servers.highlightReq = highlightReq{}
		return
	}
	if a.mode != ModeEdit {
		// Not the editing surface: there is no caret for the overlay to
		// describe, so it must not survive into this mode.
		if p.File.Highlight != nil {
			p.File.ClearHighlight()
		}
		a.servers.highlightReq = highlightReq{}
		return
	}
	path := a.docPath(p)
	if path == "" {
		a.servers.highlightReq = highlightReq{}
		return
	}
	version := int(p.File.Session().Version())
	caret := p.Cursors.Primary().Head
	if !highlightWanted(a.servers.highlightReq, path, version, caret) {
		return
	}
	ls := a.servers.live(path)
	if ls == nil {
		// No live server. The guard is left as it was — it only records a
		// request that actually happened — so the next tick tries again and
		// the highlights appear once a server is running.
		return
	}
	if highlightGap(ls.caps.Capabilities) != "" {
		a.servers.highlightReq = highlightReq{path: path, version: version, caret: caret}
		a.servers.highlightGen++
		p.File.ClearHighlight()
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.servers.highlightReq = highlightReq{path: path, version: version, caret: caret}
	a.servers.highlightGen++
	gen := a.servers.highlightGen
	conn := ls.srv.Conn()
	pos := lsp.NewDocument(p.File.Text()).Position(caret)
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs, err := lsp.RequestDocumentHighlights(ctx, conn, path, pos)
		if err != nil {
			return
		}
		a.park(lspAnswer{
			gen:              gen,
			kind:             answerHighlight,
			path:             path,
			highlightVersion: version,
			highlightCaret:   caret,
			highlight:        hs,
		})
	})
}

// applyHighlight installs a document-highlight answer on the active pane.
//
// The answer is pinned to the caret and document version it was requested at,
// and both must still match: a highlight measured at another caret is an
// emphasis on the wrong word, and one measured on older text has offsets that
// have moved. A "no occurrences" answer installs a nil set, which clears the
// overlay rather than leaving the previous one up.
func (a *App) applyHighlight(ans lspAnswer) {
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
	if int(p.File.Session().Version()) != ans.highlightVersion {
		return
	}
	if p.Cursors.Primary().Head != ans.highlightCaret {
		return
	}
	doc := lsp.NewDocument(p.File.Text())
	set := editor.NewHighlightSet(highlightRuns(doc, ans.highlight), ans.highlightVersion, ans.highlightCaret)
	p.File.SetHighlight(set)
}

// highlightRuns converts decoded highlights into the line-relative byte runs
// the renderer overlays, using the same position mapping every other LSP answer
// goes through. A range that crosses a line is split at the line boundaries:
// the protocol allows it, and dropping it because it wrapped would look like
// the server never reported that occurrence.
//
// An empty range is skipped — there is no byte to emphasise — and runs on a
// line are kept in start order so the set is inspectable and the tests exact.
func highlightRuns(doc *lsp.Document, hs []lsp.DocumentHighlight) map[int][]editor.HighlightRun {
	if len(hs) == 0 {
		return nil
	}
	byLine := map[int][]editor.HighlightRun{}
	for _, h := range hs {
		lo, hi := doc.Span(h.Range)
		if hi <= lo {
			continue
		}
		write := h.Kind == lsp.HighlightWrite
		first := doc.Position(lo).Line
		last := doc.Position(hi - 1).Line
		for line := first; line <= last; line++ {
			lineStart := doc.Offset(lsp.Position{Line: line})
			lineEnd := doc.Offset(lsp.Position{Line: line + 1})
			start, end := lo, hi
			if start < lineStart {
				start = lineStart
			}
			if end > lineEnd {
				end = lineEnd
			}
			if end <= start {
				continue
			}
			byLine[line] = append(byLine[line], editor.HighlightRun{
				Start: start - lineStart,
				End:   end - lineStart,
				Write: write,
			})
		}
	}
	for line, runs := range byLine {
		sort.Slice(runs, func(i, j int) bool { return runs[i].Start < runs[j].Start })
		byLine[line] = runs
	}
	return byLine
}
