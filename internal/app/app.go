// Package app is the event loop: it owns focus, routes keys to whichever pane
// has it, and decides when to repaint. Everything it drives is host-agnostic,
// so the whole application runs headlessly under ui.FakeHost.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"raj/internal/complete"
	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/explorer"
	"raj/internal/hidden"
	"raj/internal/hover"
	"raj/internal/keys"
	"raj/internal/lsp"
	"raj/internal/picker"
	"raj/internal/piecetable"
	"raj/internal/problems"
	"raj/internal/prompt"
	"raj/internal/search"
	"raj/internal/session"
	"raj/internal/store"
	"raj/internal/symbols"
	"raj/internal/tabs"
	"raj/internal/timing"
	"raj/internal/ui"
	"raj/internal/widget"
)

// App is one raj instance.
type App struct {
	host   ui.Host
	keymap *keys.Keymap
	screen *ui.Screen

	Tabs     *tabs.Tabs
	Explorer *explorer.Pane
	Search   *search.Pane
	Picker   *picker.Picker
	Prompt   *prompt.Prompt
	Complete complete.Popup
	// snippet is the active snippet-expansion session, if any. It owns tab and
	// shift+tab while it runs, so the editor's indent is suspended only across
	// the stops of a completion the user just accepted; any other key ends it.
	// It lives on the app rather than in the popup because accept closes the
	// popup and the session outlives it, and it moves the caret in buffer
	// coordinates the completion package never sees.
	snippet snippetSession
	// Hover is the floating panel for a language server's answer. Separate
	// from Complete because the two can be open at once and mean different
	// things: one is what you are typing, the other what you are reading.
	Hover hover.Panel
	// Problems is the workspace-wide diagnostics list. A view over a.diags,
	// refreshed when that changes rather than owning any state of its own.
	Problems *problems.Pane
	// settingsPane is the model behind the settings sidebar: the selected row,
	// the scope a change is written to, and a half-typed width. Every displayed
	// value is re-read from Settings, so it holds no value of its own to drift.
	settingsPane settingsPane
	// Menu is the right-click context menu. It floats above the panes and the
	// picker, and the pointer gives it first refusal while it is open. Its
	// Show/Handle/Render state lives in the widget; menuTarget is the app's
	// record of what it is about, so a chosen item dispatches to the same
	// entry point the keyboard already uses.
	Menu widget.Menu
	// menuTarget is what the open menu acts on, captured when it opens and
	// cleared when it closes.
	menuTarget menuTarget
	// menuAnchorCol and menuAnchorRow are where the gesture asked for the
	// menu; menuCol and menuRow are where the last frame drew it, which is
	// what the pointer hit-tests against.
	menuAnchorCol, menuAnchorRow int
	menuCol, menuRow             int

	// completeCache holds each buffer's words, keyed by its version, so typing
	// rescans only the buffer being typed into. Without it, completion cost
	// 5.3 ms per keystroke against 2 MB of open buffers.
	completeCache *complete.Cache

	// servers is the language servers for this workspace: started on demand by
	// a request that needs one, or eagerly by WarmServers for the languages of
	// the files already open at launch.
	// lspGen is the cancellation generation: an answer whose generation has
	// moved on describes a position the cursor has left, and is dropped.
	servers *servers
	lspGen  int
	// lspForced caches the per-language command overrides resolved from
	// settings, keyed by lsp.LanguageID. A present key with a nil command
	// disables the language's server. lspForcedMu guards the map because
	// SetSetting replaces it on the event thread while a request goroutine may
	// read it through lspCommand.
	lspForced   map[string][]string
	lspForcedMu sync.RWMutex
	// completeGen is a separate generation from lspGen: a completion answer is
	// superseded by later typing, not by a cursor move for a hover, and
	// sharing one counter would make each cancel the other.
	completeGen int

	// inlayGen is the cancellation generation for hint requests, separate for
	// the same reason completeGen is separate from lspGen: cursor moves and
	// typing each cancel their own kind of answer, and sharing one counter
	// would make them cancel each other.
	inlayGen int

	// codeActionList is the last code-action answer, held for the picker rows to
	// index into: the picker reports which row was chosen, and the action that
	// row names lives here. codeActionPath is the document the request was made
	// for, so a chosen command can find the same server that offered it, and
	// codeActionVersions pins the open documents so a stale edit is refused.
	codeActionList     []lsp.CodeAction
	codeActionPath     string
	codeActionVersions map[string]int

	// cached is the last completion list a server returned, kept only when the
	// server said the list was complete. A complete list is everything that
	// could go at that point, so a longer prefix can only be a subset of it and
	// filtering locally gives the same answer as asking again — instantly, and
	// without a request per keystroke.
	//
	// An INCOMPLETE list is never cached. That is the server saying it
	// truncated the answer and wants to be asked again as the prefix narrows;
	// filtering a truncated list would keep the first answer's arbitrary cut
	// forever, so a large package would show a handful of results that never
	// improve however much more is typed.
	cached completionCache

	// completionDocs memoises completionItem/resolve answers by the item's
	// opaque resolve key, so arrowing back through a list does not ask again
	// and a later list carrying the same item shows its documentation at once.
	completionDocs map[string]string
	// resolvePending marks the resolve keys with a request in flight, so
	// arrowing across an item and back does not send a second one before the
	// first answers. It is cleared when the popup closes.
	resolvePending map[string]bool

	// diags is the current problems per file, published by servers rather than
	// requested.
	diags *diagnostics

	// inlays is the last hint answer per file, keyed by path and carrying the
	// document version it describes. A hint from a version the buffer has left
	// must never reach the renderer: it would move every column after it
	// rather than merely look wrong.
	inlays *inlayStore
	// inlayReq records the last hint request — path, document version and
	// range — so the idle tick asks again only when one of those has moved.
	inlayReq inlayRequest

	// lenses is the last code-lens answer per file, keyed by path and carrying
	// the document version it describes. Like inlays it is filled from a
	// goroutine and installed on the event thread.
	lenses *lensStore
	// lensGen is the cancellation generation for lens answers, separate from
	// inlayGen because the two are requested and superseded independently.
	lensGen int
	// lensReq records the last lens request — path and document version — so
	// the idle tick asks again only when the text has moved.
	lensReq lensReq
	// onTypeGen is the cancellation generation for on-type formatting answers,
	// separate for the same reason lensGen is separate from inlayGen: it is a
	// keystroke's answer and later typing supersedes it, not a cursor move.
	onTypeGen int

	// semantics is the last semantic-token overlay per file, keyed by path and
	// carrying the document version it describes. Like lenses it is filled
	// from a goroutine and installed on the event thread.
	semantics *semanticStore
	// semanticGen is the cancellation generation for semantic-token answers,
	// separate from lensGen because the two are requested and superseded
	// independently.
	semanticGen int
	// semanticReq records the last semantic-token request — path and document
	// version — so the idle tick asks again only when the text has moved.
	semanticReq semanticReq

	// drag tracks a press-and-hold in the editor, and click counts a rapid
	// sequence in one place so double and triple clicks can mean something.
	drag bool
	// autoscroll is how far outside the text area a drag is being held, in
	// rows: negative above, positive below, zero inside. It is a held state
	// rather than an event because the pointer sits still while the text
	// moves — there is no second event to react to, so the scroll has to come
	// from the tick.
	autoscroll int
	// dragCol and dragRow are where the pointer was last seen, so each tick
	// can re-extend the selection to it after scrolling.
	dragCol, dragRow int
	click            clickTracker
	// hintHover is the pointer tooltip state. The panel it shows is the same
	// hover.Panel the caret-driven document hover writes, so it carries the
	// text and anchor it showed to tell its own box from a replacement.
	hintHover hintHover
	lspMu     sync.Mutex
	lspAnswer *lspAnswer
	// saveAnswer is the parked willSaveWaitUntil answer, on its own slot so a
	// save cannot be lost to another answer overwriting the shared slot.
	saveAnswer *lspAnswer
	// saveGen correlates a willSaveWaitUntil request with its answer; a later
	// save gesture bumps it to supersede the earlier request.
	saveGen int
	// pendingWrite is the save waiting on that answer. Event-thread only, like
	// the buffers it names.
	pendingWrite *pendingWrite

	root string
	// state is the workspace SQLite store, nil when it could not be opened or
	// there is no workspace root. Every read and write is nil-safe: without it
	// persistence degrades to the session file, or to nothing.
	state   *store.Store
	sidebar Sidebar
	focus   Focus
	theme   editor.Theme
	wth     widget.Theme

	// previewPath is the path last shown in the reusable preview tab, so
	// arrowing only reloads when the selection actually changed. It is the
	// app's memo; Tabs holds the authoritative preview pane.
	previewPath string

	Debug     debugLog
	clipboard editor.Clip
	// WrapDefault is applied to every pane as it opens, so the flag and the
	// toggle do not disagree about newly opened files. On by default: a line
	// running off the right edge is worse than one that continues below, and
	// horizontal scrolling is the fallback rather than the norm.
	WrapDefault bool

	// AutoPairs is applied to every pane as it opens, for the same reason as
	// WrapDefault: the setting lives on the application and panes inherit it,
	// rather than each pane deciding for itself and drifting.
	AutoPairs bool
	// InlayHints is the application default for language-server hints, applied
	// to panes as they open the way WrapDefault and AutoPairs are.
	InlayHints bool
	// settings is the effective configuration resolved at startup and kept
	// current by SetSetting, so Settings reports what is in force.
	settings   ResolvedSettings
	dark       bool
	lastLayout Layout
	// lastCols and lastRows are the frame size lastLayout was drawn at, so a
	// size change can be told from a same-size layout change: the former is
	// the terminal's doing and must clear, the latter is raj's own and may
	// repaint without erasing. They are zero on the first frame, which is what
	// makes that frame clear too.
	lastCols, lastRows int
	// promptReturn is where focus goes when a dialog closes. Captured when the
	// first prompt in a chain opens, so an overwrite check answered three
	// dialogs deep still lands back where the user was.
	promptReturn Focus
	mode         Mode
	status       string
	// phone is the launch profile: taller scrollable tab chips, no status
	// strip, and a review action bar. It is fixed at construction, so no
	// stored setting can turn it on or off.
	phone bool
	// ctrlAliases is on when the keymap carries a ctrl+<key> alias for every
	// super+<key> binding it could. main turns it on for --phone unless
	// --ctrl-aliases says otherwise. ctrlAliasAdded and ctrlAliasCollisions are
	// what the builder did, kept for the startup notice.
	ctrlAliases         bool
	ctrlAliasAdded      int
	ctrlAliasCollisions []string // statusShown and statusAt date the phone profile's transient status
	// overlay: Draw timestamps a message when it first appears and the idle
	// tick expires it. The ordinary profile ignores both.
	statusShown string
	statusAt    time.Time
	// drawerOpen is the phone action drawer's expanded state. drawerHandle and
	// drawerPanel are where the last frame drew its touch targets, so the
	// pointer resolves a tap against what was painted rather than against a
	// second copy of the arithmetic.
	drawerOpen bool
	drawerSel  int
	// drawerWant is the action whose button the drawer should select on the
	// next frame. It is resolved against the drawn, mode/pending-filtered cells
	// then, so a jump survives the panel being rebuilt when a decision empties
	// the review controls. Event-thread only.
	drawerWant      keys.Action
	drawerHandle    drawerItem
	drawerPanel     []drawerItem
	drawerPanelTop  int
	drawerPanelRows int
	// swipe follows a bare-motion run across the phone tab strip: swipeOn is
	// true while one is in progress, swipeCol the last column seen and
	// swipeAcc the horizontal distance not yet spent as scroll.
	swipeOn  bool
	swipeCol int
	swipeAcc int
	focused  bool
	quit     bool
	// quitAsked is true while the quit confirmation is on screen, so a second
	// Quit forces the exit instead of reopening the same question.
	quitAsked bool

	// saveDoneAt is when the last successful save finished, for the timing
	// instrument: the next frame reports the distance from it to the first
	// frame that paints the buffer clean. Zero whenever timing is off.
	saveDoneAt time.Time

	// treeScanAt is when the explorer tree was last compared to the
	// filesystem, so the idle tick rescans it at a bounded rate instead of
	// every frame.
	treeScanAt time.Time

	// NoRestore disables reading and writing the session file, for --no-restore
	// and for tests that must not touch a workspace they did not create.
	NoRestore bool

	// standalone is --standalone: a single-file throwaway editor. The workspace
	// store is never opened and the session is inert; the sidebar cannot be
	// shown or opened, so the editor is one file and its tab strip.
	standalone bool

	// sessionDirty and sessionSaved debounce writing the session file, so a
	// crash loses seconds rather than the whole session. sessionTabs is the
	// last-written tab-set fingerprint, so opening or closing a tab is flushed
	// on the next tick instead of waiting out the interval.
	sessionDirty bool
	sessionSaved time.Time
	sessionTabs  string

	// compactAt debounces the idle compaction pass so the origin-index walk does
	// not run on every tick, and compacted records the (session version,
	// decision generation) each pane was last attempted at. Compact leaves the
	// version where it found it and a decision moves only the generation, so a
	// pane whose pair has not moved has nothing new to fold and is skipped
	// before Compact builds its origin index. The map is keyed on the pane, so
	// compactTick prunes it to the live tabs first: a closed pane would
	// otherwise outlive its buffer, and the allocator can hand its pointer to a
	// new pane that then inherits the stale pair.
	compactAt time.Time
	compacted map[*editor.Pane]compactSeen

	// journals is the op-log tap per buffer path, nil until the first dirty
	// tick opens one. journalSaved debounces the append the same way
	// sessionSaved debounces the session file, but the tick does not fsync:
	// durability is the save, close and quit flush.
	journals     map[string]*logTap
	journalSaved time.Time
	// journalPersisted debounces the store-backed journal write and
	// journalWritten is the change token last persisted per path; journalNote
	// carries a one-shot message from the loader to the open that follows it.
	journalPersisted time.Time
	journalWritten   map[string]journalStamp
	journalNote      string
	// restoredAuthors is the author table read from the op logs at startup,
	// waiting for the control registry StartControl builds later in startup.
	// Ids are explicit, so a restored op's author resolves to the identity,
	// name and kind it was written under rather than a fresh join order.
	restoredAuthors []control.Participant

	// tabWidth is the indent width the tab set was built with, kept so a log
	// restore can build a File with the same geometry as one read from disk.
	tabWidth int

	// headless holds buffers that are loaded and addressable over the socket
	// but have no tab: document state without presentation state, so an agent
	// inspecting twenty files does not put twenty tabs on screen. Most recently
	// used first, bounded by headlessMax; see headless.go.
	headless []*editor.Pane

	// control is the control server, nil until StartControl runs. Its requests
	// are executed in drainControl, on this thread.
	control *control.Server
	// guard is the validation chokepoint in front of the buffer host. One per
	// app, so read-before-write is remembered across requests.
	guard *control.Guard

	// attach records that this app is a client of a running editor: documents
	// arrive as snapshots and the idle tick must not touch the local disk or
	// write a session.
	attach bool
	// attachAddr is the daemon address the attach requested; empty means
	// discovery. It is settled by StartClient once a dial succeeds.
	attachAddr string
	// attachKey names the saved client view in the store, so two clients of one
	// workspace keep separate tab sets. It is the --name value when given, else
	// the profile (phone, else attach).
	attachKey string
	// clientStop is closed by CloseClient to tell the watch goroutine that the
	// transport error it is about to see is an orderly shutdown, not news.
	clientStop chan struct{}
	// clientTabMu guards clientMirrored, the set of paths the client shows as
	// tabs. The value is true for a path mirrored from a daemon real tab and
	// false for one the client loaded itself; that distinction lets a reconcile
	// drop a mirrored tab the daemon closed while it keeps a client-loaded
	// headless buffer.
	clientTabMu    sync.Mutex
	clientMirrored map[string]bool
	// clientClosed records the daemon facts for a path the user closed on this
	// client. It is a snooze, not a mute: the path is skipped while the daemon
	// buffer still matches the mark, and re-added once the version or review
	// state moves. That is what lets a close survive a reconcile yet a later
	// proposal or edit bring the tab back.
	clientClosed map[string]bufferMark
	// client is the daemon connection, nil in a local editor. StartClient
	// sets it once before the watch goroutine starts; CloseClient closes it on
	// the way out, which unblocks a parked watch. It is owned by the watch
	// goroutine after that, so it cannot also carry decisions: a parked watch
	// holds the client lock for the length of the park. A reconnect swaps it
	// under clientConnMu.
	client clientConn
	// clientDecide is the second daemon connection, owned by the event thread.
	// Decisions use it so a decision never waits on the parked watch, and clear
	// claims the path on this connection, the same author the clear verb must
	// satisfy. A reconnect swaps it under clientConnMu.
	clientDecide clientConn
	// clientMu guards the handoff from the watch goroutine to the event
	// thread: files the goroutine rebuilt, and the one-line lost-connection
	// status it wants shown.
	clientMu    sync.Mutex
	clientFiles []clientFile
	clientLost  string
	// clientDown is the connection dot state: true once the watch transport
	// fails, false while it is healthy. Guarded by clientMu because the watch
	// goroutine sets it and the frame reads it.
	clientDown bool
	// clientConnMu guards the watch and decision connections, which the watch
	// goroutine swaps on a reconnect while the event thread reads the decision
	// one. It is separate from clientMu, which guards the file handoff.
	clientConnMu sync.Mutex
	// clientDone is closed when the watch goroutine returns, so shutdown and a
	// test can tell that the retry loop has actually stopped.
	clientDone chan struct{}
	// controlGen is the review generation handed to control.Server and watched
	// by clients; controlHash is the hash of that workspace review surface — the
	// open buffers with their decisions, plus the pending removals — so the idle
	// tick only bumps Gen when something a client watches actually moved.
	controlGen  uint64
	controlHash uint64

	// pendingDeletions is the workspace-level set of paths an agent has
	// proposed to delete, keyed by the canonical path, each carrying the
	// proposing author. It is not a change set: a deletion is a path-level
	// fact that outlives any buffer, so it lives on the app rather than in a
	// session. In-memory, like the claim set; a restart forgets it. A
	// proposal records here and changes nothing on disk — the unlink happens
	// only when the user approves, in the deletion gate (W4b-2).
	pendingDeletions map[string]control.Deletion

	// pendingDirRemovals is the workspace-level set of directories an agent
	// has proposed to remove, keyed by the canonical directory path, each
	// carrying the proposing author. It is the rmdir analogue of
	// pendingDeletions: a subtree-level fact rather than a change set, and
	// in-memory, so a restart forgets it. rmdir only records here; nothing is
	// removed until the user approves in the review tab.
	pendingDirRemovals map[string]control.DirRemoval

	// pendingRemovals is the arrival order of pendingDeletions and
	// pendingDirRemovals, so the re-raise key can present the oldest proposal
	// first. The maps hold the proposals; this is only the queue. See
	// removals.go.
	pendingRemovals []pendingRemoval

	// deletionPromptPane is the active pane the deletion gate last evaluated.
	// The tracked pane is what makes the gate once per focus: a pane that has
	// not changed leaves it alone, and focusing the path again re-raises the
	// question. Nil until the gate has looked at a pane.
	deletionPromptPane *editor.Pane

	// snapshots holds dump results keyed by id, and snapSeq mints the ids. They
	// are per-author (Patch checks the writer owns the id), and the map lives
	// only for the process, so a restart evicts them — a patch by id never
	// crosses a restart.
	snapshots map[uint64]snapshot
	snapSeq   uint64
}

// New builds an application rooted at a directory. It applies no explicit flag
// overrides, so a stored setting still beats its tabWidth argument; main uses
// NewWithOptions to say which command-line flags the user actually typed.
func New(host ui.Host, root string, tabWidth int) *App {
	return NewWithOptions(host, root, Options{TabWidth: tabWidth})
}

// NewWithOptions builds an application, resolving the effective settings
// before anything is constructed. A workspace value beats a user value, both
// beat built-in defaults, and an explicit flag — a *Set field on Options —
// beats them all. The tab width is decided here because tabs.New bakes it into
// the tab set, so a later override could not reach it.
func NewWithOptions(host ui.Host, root string, o Options) *App {
	cols, rows := host.Size()
	// The store opens before the tab set because settings decide the tab
	// width; it is also where the session and positions live. State lives in
	// the XDG state dir, outside the workspace, so a failed migration or an
	// unwritable state dir is not fatal: the editor runs on the built-in
	// defaults with persistence off, and the failure is left for the status
	// line below.
	var state *store.Store
	var stateErr error
	var migErr error
	if root != "" && !o.Standalone {
		if dir := session.StateDir(root); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				stateErr = err
			} else {
				migErr = migrateState(root)
				if s, err := store.Open(filepath.Join(dir, "state.db")); err != nil {
					stateErr = err
				} else {
					state = s
				}
			}
		}
	}
	user, workspace := settingScopes(state)
	res, bad := resolveSettings(defaultSettings(o.TabWidth), user, workspace)
	// An explicit flag is a decision made now, so it wins over anything stored.
	// A non-positive width is not a width: it is ignored so the default stands,
	// rather than reaching tabs.New(0) and a pin SetTabWidth refuses.
	if o.TabWidthSet && o.TabWidth > 0 {
		res.TabWidth = o.TabWidth
	}
	if o.TabsSet {
		res.Tabs = o.Tabs
	}
	if o.WrapSet {
		res.Wrap = o.Wrap
	}
	// The profile implication: --phone turns on the ctrl aliases unless
	// --ctrl-aliases was passed explicitly. Resolved here so main can pass the
	// raw flags and the rule lives in one place.
	phoneOn, aliasesOn := ProfileFlags(o.Phone, o.CtrlAliases, o.CtrlAliasesSet)
	// A standalone editor is one file: the sidebar starts closed and the editor
	// starts focused, whatever the ordinary default is. The store is never
	// opened, so there is nothing to restore a session from either.
	side, focus := SidebarExplorer, FocusSidebar
	if o.Standalone {
		side, focus = SidebarNone, FocusEditor
	}
	a := &App{
		host:           host,
		keymap:         keys.NewKeymap(),
		screen:         ui.NewScreen(cols, rows),
		Tabs:           tabs.New(res.TabWidth),
		tabWidth:       res.TabWidth,
		Explorer:       explorer.NewPane(root),
		Search:         search.NewPane(root),
		Problems:       problems.New(),
		settingsPane:   newSettingsPane(),
		completeCache:  complete.NewCache(),
		servers:        newServers(root),
		diags:          newDiagnostics(),
		inlays:         newInlayStore(),
		lenses:         newLensStore(),
		semantics:      newSemanticStore(),
		Picker:         picker.New(root),
		Prompt:         prompt.New(),
		root:           root,
		state:          state,
		journalWritten: make(map[string]journalStamp),
		settings:       res,
		sidebar:        side,
		focus:          focus,
		theme:          editor.DefaultTheme(),
		// The effective settings, not the raw defaults: a stored value or an
		// explicit flag has already been layered over them. On by default, so
		// an App built in a test or by a future entry point wraps rather than
		// depending on who remembered to set the field.
		WrapDefault: res.Wrap,
		AutoPairs:   res.AutoPairs,
		InlayHints:  res.InlayHints,
		NoRestore:   o.NoRestore || o.Standalone,
		standalone:  o.Standalone,
		attach:      o.Attach,
		attachAddr:  o.AttachAddr,
		attachKey:   clientViewKey(o, phoneOn),
		phone:       phoneOn,
		ctrlAliases: aliasesOn,
		dark:        host.Theme().Dark(),
		wth:         widget.DefaultTheme(),
		focused:     true,
	}
	a.Tabs.IndentTabs = res.Tabs
	a.Tabs.SetPhone(phoneOn)
	a.Tabs.Load = a.loadFile
	a.Picker.Tall = phoneOn
	a.Explorer.Tall = phoneOn
	if phoneOn {
		// The drawer is a phone surface. esc is the editor's Cancel in the
		// ordinary keymap, so it is bound only here; a tap is the primary
		// route and this is for an attached keyboard.
		a.keymap.Bind(keys.Editor, "esc", keys.ToggleDrawer)
	}
	if a.ctrlAliases {
		a.ctrlAliasAdded, a.ctrlAliasCollisions = a.keymap.CtrlAliases()
	}
	// The LSP command overrides are settings the typed struct cannot carry.
	// Resolve them before anything can ask for a server, and point the server
	// table at the App method that reads the cache.
	a.servers.resolveCommand = a.lspCommand
	a.refreshLSPOverrides()
	// An explicit tab width is pinned onto buffers as they open, so it survives a
	// file detecting its own indentation; the default is only a fallback.
	if tabWidthExplicit(o, user, workspace) {
		a.Tabs.SetTabWidth(res.TabWidth)
	}
	// A bad line in .raj/hidden is skipped rather than fatal, but silently
	// skipped is how a typo becomes "raj ignores my config". The status line is
	// the only place it can be said at startup.
	if badHidden := a.Explorer.Tree.Hidden.Bad; len(badHidden) > 0 {
		a.status = fmt.Sprintf("%s: ignoring %d bad pattern(s): %s",
			hidden.File, len(badHidden), strings.Join(badHidden, ", "))
	}
	// A store that failed to open, and a setting whose value will not parse,
	// are the same kind of problem: the editor runs, but a typo that would
	// otherwise look like "raj ignores my setting" is said once. A bad value
	// is ignored rather than zeroed, so it cannot silently turn a setting off.
	if stateErr != nil {
		if a.status != "" {
			a.status += "; "
		}
		a.status += "state: " + stateErr.Error()
	}
	// A legacy state directory that would not move is not fatal either: the
	// editor runs against the new location and the old files are left behind.
	if migErr != nil {
		if a.status != "" {
			a.status += "; "
		}
		a.status += "state migration: " + migErr.Error()
	}
	if len(bad) > 0 {
		if a.status != "" {
			a.status += "; "
		}
		a.status += fmt.Sprintf("settings: ignoring %d bad value(s): %s", len(bad), strings.Join(bad, ", "))
	}
	// A finished search used to wait for the 150 ms tick, because the result is
	// installed on the event thread and nothing woke that thread. Posting a
	// Wake closes the gap for typing pauses, and it is the seam the agent pane
	// needs for exactly the same reason.
	a.Search.Notify = func() { host.Post(ui.Wake{}) }
	// Search the buffers, not only the disk. Only dirty ones: a saved buffer
	// and its file are the same bytes, so snapshotting it would copy a
	// document to search it exactly as reading it would have. An unnamed
	// buffer has no path to key on and is skipped — it is the one tab a search
	// cannot reach, for the same reason session restore cannot bring it back.
	a.Search.Buffers = func() search.Docs {
		var open search.Docs
		for _, p := range a.Tabs.All() {
			if p.File.Path == "" || !p.File.ViewDirty() {
				continue
			}
			if open == nil {
				open = search.Docs{}
			}
			open[p.File.Path] = p.File.Text()
		}
		return open
	}
	return a
}

// syncTheme adopts the terminal's measured background once the OSC query has
// answered. Highlighting a dark theme with light colours is unreadable, and the
// answer arrives asynchronously, so this runs whenever it might have changed.
func (a *App) syncTheme() {
	dark := a.host.Theme().Dark()
	if dark == a.dark {
		return
	}
	a.dark = dark
	for _, p := range a.Tabs.All() {
		p.File.SetDark(dark)
	}
}

// OpenFile opens a path in a tab and focuses the editor.
//
// A refusal is a dialog rather than a status line when the reason is the file
// itself. Clicking a name in the tree is a direct request for that file, and
// answering it with a line of text at the bottom of the screen looks like
// nothing happened — the tree still shows the name, the editor still shows the
// last file, and the only evidence is somewhere the eye is not. A permissions
// error stays in the status line: that is a transient condition rather than a
// statement about what the file is.
//
// openFileQuiet is the same open without the focus move, for the control
// surface: an agent asking to open a file gets it a tab, but the user's active
// tab and focus stay put.
func (a *App) OpenFile(path string) {
	a.openFile(path, true)
}

// openFileQuiet opens a path in a tab without moving the active tab or focus.
// It shares openFile's body, so the two cannot drift.
func (a *App) openFileQuiet(path string) {
	a.openFile(path, false)
}

// openFile opens a path in a tab, moving the active tab and focus only when
// focus is set. A quiet open still adds the tab -- the caller has a reason the
// user should be able to find it -- and leaves the active tab alone when there
// was one; with nothing open the new tab takes the slot so something is
// visible.
func (a *App) openFile(path string, focus bool) {
	if path == "" {
		return
	}
	// A headless buffer is already loaded. Showing it is announcing, not
	// reading it a second time: without this, a file an agent inspected and the
	// user then clicked would exist twice, the tab and the hidden pane free to
	// drift.
	if p, ok := a.findHeadless(path); ok {
		if focus {
			a.announce(p)
		} else {
			a.announceQuiet(p)
		}
		a.maybePromptDeletion()
		return
	}
	// A pane for this path may already exist, as a tab or the preview. Only a
	// genuinely new pane gets the remembered position: re-opening a file that
	// is already on screen must not yank the caret back.
	alreadyOpen := false
	for _, q := range a.Tabs.All() {
		if q.File.Path == path {
			alreadyOpen = true
			break
		}
	}
	// An attached client opens through the daemon: the file is loaded there
	// and comes back as a snapshot, so this process never reads the disk and
	// never owns a writable copy. A path already among the client tabs falls
	// through to the focus path below and is not re-opened.
	if a.attach && !alreadyOpen {
		a.openRemote(path, focus)
		return
	}
	prev := a.Tabs.Active()
	p, err := a.Tabs.Open(path)
	if err != nil {
		switch {
		case errors.Is(err, editor.ErrBinary):
			a.refuse(path, "is not a text file, so raj will not open it.")
		case errors.Is(err, editor.ErrTooLarge):
			a.refuse(path, "is larger than raj will open.")
		case errors.Is(err, editor.ErrUnsupportedEncoding):
			a.refuse(path, "uses a text encoding raj cannot open.")
		default:
			a.status = "cannot open: " + err.Error()
		}
		return
	}
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
	if !alreadyOpen {
		a.applyStoredPosition(p)
	}
	if focus {
		a.focus = FocusEditor
	} else if prev != nil && a.Tabs.Contains(prev) {
		// The quiet open left the new tab active; put the user's tab back in
		// front. With no previous tab the new one keeps the slot.
		a.Tabs.Focus(prev)
	}
	// Opening a tab is the change most worth not losing to a crash: a cursor
	// position is a scroll, a missing tab is a file you have to find again.
	a.TouchSession()
	// A property of the file just opened is the one thing worth saying here;
	// this assignment is the clear, so an empty warning leaves the status line
	// quiet.
	a.status = a.journalStatus(fileWarning(p.File))
	a.maybePromptDeletion()
}

// previewFile shows path in the one reusable preview tab without moving focus
// to the editor, so arrowing through the explorer loads a file without
// committing to it. Enter goes through OpenFile instead, and that is what
// focuses the editor and places the cursor.
func (a *App) previewFile(path string) {
	if path == "" {
		return
	}
	old := a.Tabs.Preview()
	var p *editor.Pane
	if h, ok := a.findHeadless(path); ok {
		// The file is already loaded — possibly carrying an agent's proposal.
		// Reveal that same pane rather than reading a second copy from disk,
		// which would leave one path with two tabs when enter announces the
		// headless copy.
		a.unregisterHeadless(h)
		a.Tabs.PreviewPane(h)
		p = h
	} else {
		var err error
		p, err = a.Tabs.OpenPreview(path)
		if err != nil {
			// A preview is a glance, not a request: a binary or unreadable file
			// is left unshown rather than answered with a dialog nobody asked
			// for. Enter still reports it through OpenFile.
			return
		}
	}
	if old != nil && old != p && !a.Tabs.Contains(old) {
		// The preview slot was reused, so the pane it held is gone. Remember
		// where it was, then forget what was keyed on its path the way closing
		// its tab would.
		a.rememberPosition(old)
		a.closeDoc(old)
	}
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
	a.previewPath = path
}

// fileWarning is the sentence, if any, a freshly opened buffer owes the user:
// mixed line endings that a save will normalise, or indentation the file's own
// format rejects. Both are said when both hold; an ordinary file says nothing,
// so the status line stays quiet.
func fileWarning(f *editor.File) string {
	var w []string
	if enc := f.EncodingWarning(); enc != "" {
		w = append(w, enc)
	}
	if indent := f.IndentWarning(); indent != "" {
		w = append(w, indent)
	}
	return strings.Join(w, "; ")
}

// refuse says why a file was not opened, and puts focus back where it came from
// once the dialog is dismissed.
func (a *App) refuse(path, because string) {
	a.confirm("Cannot open", filepath.Base(path)+" "+because,
		[]string{"OK"}, func(string, bool) {})
}

// openFromPicker opens a chosen file and honours any position the query was
// pasted with, so pasting a compiler line lands where the compiler pointed
// rather than at the top of the file.
//
// It is separate from OpenFile because a position only makes sense for a choice
// the picker made: nothing else in the app has a pasted `:line:col` to apply.
func (a *App) openFromPicker(path string) {
	if path == "" {
		return
	}
	pos, ok := a.Picker.PositionFor(path)
	a.OpenFile(path)
	if !ok || a.Tabs.Active() == nil {
		return
	}
	if pos.Line > 0 {
		a.jumpTo(pos.Line)
	}
	if pos.Col > 0 {
		a.jumpToColumn(pos.Line, pos.Col)
	}
}

// wheelRows is how far one notch scrolls. Three is the near-universal default
// and the number every terminal that translates the wheel into arrow keys
// sends, so matching it means the feel does not change with the terminal.
const wheelRows = 3

// wheelCols is how far one horizontal notch scrolls the phone tab strip.
// Wider than wheelRows because the chips are wide and a notch should cross
// most of one.
const wheelCols = 6

// mouse handles a pointer event. Only the wheel does anything today.
//
// It scrolls whatever is under the pointer rather than whatever has focus,
// because that is what a pointer is for: reaching something without first
// going there. An overlay takes the wheel wherever the pointer is, since it is
// drawn over everything and scrolling the pane behind it would be scrolling
// something the user cannot see.
func (a *App) mouse(ev ui.Mouse) {
	if !ev.IsWheel {
		// A bare-motion run over the phone tab strip is a swipe; it consumes
		// the event before the ordinary hover path can see it.
		if a.swipeTabs(ev) {
			return
		}
		if !ev.Motion || ev.Button != keys.MouseNone {
			// A click (or a drag) moves the caret, which leaves the snippet's
			// stops behind. A bare pointer move must not: the hand resting on
			// the mouse would otherwise end every snippet.
			a.endSnippet()
		}
		a.pointer(ev)
		return
	}
	// A scroll moves the rows under a resting pointer, so a tooltip it opened
	// no longer describes what is there.
	a.hideHint()
	// A wheel notch ends any tab-strip motion run, so a later motion does not
	// accumulate a delta across the gap.
	a.swipeOn = false
	// The phone tab strip scrolls sideways on a horizontal wheel notch. The
	// ordinary profile has nowhere sideways to go, so the notch is dropped.
	if ev.Button == keys.WheelLeft || ev.Button == keys.WheelRight {
		if a.phone {
			step := wheelCols
			if ev.Button == keys.WheelLeft {
				step = -step
			}
			a.Tabs.ScrollTabs(step)
		}
		return
	}
	delta := wheelRows
	if ev.Button == keys.WheelUp {
		delta = -wheelRows
	}
	if ev.Button != keys.WheelUp && ev.Button != keys.WheelDown {
		return // no other wheel direction scrolls the panes
	}

	if a.Picker.Open {
		a.Picker.Scroll(delta)
		return
	}
	cols, rows := a.screen.Size()
	l := a.layout(cols, rows)
	if l.ShowSidebar && ev.Col >= l.SidebarX && ev.Col < l.SidebarX+l.SidebarW {
		switch a.sidebar {
		case SidebarExplorer:
			a.Explorer.Scroll(delta)
		case SidebarSearch:
			a.Search.Scroll(delta)
		}
		return
	}
	if l.ShowEditor {
		if p := a.Tabs.Active(); p != nil {
			p.ScrollRows(delta)
		}
	}
}

// swipeStep is how many columns a horizontal bare-motion run must cover before
// it counts as a swipe rather than jitter. A phone's motion reports arrive
// more than a column apart, and a threshold keeps a resting finger from
// scrolling the strip.
const swipeStep = 3

// swipeTabs turns a bare-motion run over the phone tab strip into a horizontal
// scroll. It reports whether it consumed the event: a motion run on the strip
// is not a hover, so the caller must not pass it on. A press, a release, a
// wheel, or motion anywhere else ends the run and leaves the event for its
// usual path.
func (a *App) swipeTabs(ev ui.Mouse) bool {
	if !a.phone || !ev.Motion || ev.Button != keys.MouseNone {
		a.swipeOn = false
		return false
	}
	if ev.Row < 0 || ev.Row >= a.Tabs.StripRows() {
		a.swipeOn = false
		return false
	}
	if !a.swipeOn {
		a.swipeOn = true
		a.swipeCol = ev.Col
		a.swipeAcc = 0
		return true
	}
	a.swipeAcc += ev.Col - a.swipeCol
	a.swipeCol = ev.Col
	// Dragging left reveals later tabs, which is a positive scroll offset.
	if a.swipeAcc >= swipeStep || a.swipeAcc <= -swipeStep {
		a.Tabs.ScrollTabs(-a.swipeAcc)
		a.swipeAcc = 0
	}
	return true
}

// phoneStatusTTL is how long the phone profile's transient status stays up.
// Long enough to read a one-line note, short enough that it does not become
// the status strip the profile removed.
const phoneStatusTTL = 3 * time.Second

// expirePhoneStatus drops a phone status once it has had its time. The idle
// tick is the clock because a message needs no event to expire it, and the
// ordinary profile never expires its status strip.
func (a *App) expirePhoneStatus() {
	if !a.phone || a.status == "" || a.statusAt.IsZero() {
		return
	}
	if time.Since(a.statusAt) >= phoneStatusTTL {
		a.status = ""
		a.statusShown = ""
		a.statusAt = time.Time{}
	}
}

// completionCache is a server's answer plus where it was asked for, so a later
// keystroke can tell whether it still applies.
type completionCache struct {
	items  []lsp.CompletionItem
	prefix string
	// line and col anchor the word the list describes. A prefix that merely
	// looks like an extension is not enough: typing "fmt" on one line and then
	// "fmt" on another produces the same string at a different place, and the
	// answers are not interchangeable.
	line, col int
}

// covers reports whether a cached list still answers for a prefix at a place.
func (c completionCache) covers(prefix string, line, col int) bool {
	return c.items != nil && c.line == line && c.col == col &&
		strings.HasPrefix(prefix, c.prefix)
}

// offerCompletion refreshes the popup for whatever word the cursor is now in.
//
// It is called after the edit rather than before, so the prefix is what is
// actually on screen. Only typing and backspace offer anything; every other
// action closes the popup, because a cursor that jumped somewhere is no longer
// finishing the word it was on.
func (a *App) offerCompletion(p *editor.Pane, typing bool) {
	if !typing {
		a.hideCompletion()
		return
	}
	a.showCompletion(p, complete.MinPrefix)
}

// summonCompletion is the deliberate ask, on ctrl+space.
//
// Two differences from the popup appearing on its own. There is no minimum
// prefix, because asking explicitly is a statement that you want it here even
// with one character or none; and the cache is dropped first, so pressing the
// chord again re-asks rather than redisplaying the answer that is already on
// screen. "Ask again" is the only thing a second press could reasonably mean.
func (a *App) summonCompletion(p *editor.Pane) {
	a.cached = completionCache{}
	if !a.showCompletion(p, 0) {
		a.status = "no completions here"
	}
}

// hideCompletion closes the popup and forgets the server's answer, since the
// answer described a word the cursor is no longer in.
func (a *App) hideCompletion() {
	a.Complete.Hide()
	a.cached = completionCache{}
	// A request in flight for a closed popup is still worth completing — its
	// answer fills the memo — but its pending marker must not survive to block
	// a fresh request for the same item later.
	a.resolvePending = nil
}

// showCompletion offers candidates for the word the cursor is in, reporting
// whether anything was shown.
//
// Multiple cursors show nothing. A completion is one word at one place, and
// applying it at four cursors that are mid-word in four different identifiers
// would replace text nobody looked at.
func (a *App) showCompletion(p *editor.Pane, minPrefix int) bool {
	if a.Tabs.Active() != p || len(p.Cursors.All()) > 1 ||
		p.Cursors.Primary().HasSelection() {
		a.hideCompletion()
		return false
	}
	head := p.Cursors.Primary().Head
	// The popup is placed in display rows and columns: a fold above the caret
	// shifts every row after it, so anchoring on the session line would put the
	// list a row off. With no decisions DispPos is File.LineCol exactly, so a
	// clean buffer is unchanged.
	line, col := p.DispPos(head)
	// The prefix comes from the row's own text, not the session line: a fold
	// can cut a rejected run out from under the caret, and completing from the
	// session bytes would then offer to replace text that is not on screen.
	// The offset within the row is the caret minus the row's first session
	// byte, so a clean row indexes its session line exactly as before.
	prefix := complete.PrefixAt(p.RowText(line), head-p.DocAt(line, 0))
	if len(prefix) < minPrefix {
		a.hideCompletion()
		return false
	}
	anchor := col - len(prefix)

	// A complete list already covering this word is the whole answer, so it is
	// filtered rather than re-fetched. This is the difference the isIncomplete
	// flag buys: one request per word instead of one per keystroke.
	if a.cached.covers(prefix, line, anchor) {
		return a.showItems(p, prefix, a.cached.items, line, anchor)
	}

	cands := a.completeCache.Rank(a.completionSnapshots(p), prefix)
	a.Complete.Show(prefix, cands, line, anchor)
	// Buffer words are on screen now; the server's answer replaces them when
	// it arrives. Asking after showing rather than before is what keeps the
	// popup instant.
	if a.Complete.Open {
		a.requestCompletion(p, prefix, line, anchor)
	}
	return a.Complete.Open
}

// showItems renders a server list, filtered to a prefix. Shared by the fresh
// answer and the cached one so the two cannot present the same items
// differently.
//
// The pane is needed because a candidate can carry a server textEdit, whose
// ranges are LSP coordinates. They are converted to byte offsets against the
// current pane text here, once, so acceptance is a plain range replacement.
func (a *App) showItems(p *editor.Pane, prefix string, items []lsp.CompletionItem, line, col int) bool {
	items = lsp.FilterItems(items, prefix)
	if len(items) == 0 {
		return false
	}
	lsp.SortItems(items)

	var doc *lsp.Document
	if p != nil {
		doc = lsp.NewDocument(p.File.Text())
	}
	cands := make([]complete.Candidate, 0, len(items))
	for i, it := range items {
		if i >= complete.MaxResults {
			break
		}
		c := complete.Candidate{
			Word:          it.Insert,
			Detail:        it.Detail,
			Documentation: it.Documentation,
			Snippet:       it.Snippet,
		}
		// Only an item the server left undocumented needs its handle carried:
		// an item with documentation is already complete, and the handle is the
		// item verbatim, which is large enough not to carry for nothing.
		if c.Documentation == "" {
			c.ResolveKey = it.ResolveKey()
		}
		if c.ResolveKey != "" {
			// A memoised resolve answer is what makes backtracking to a
			// previously-resolved item instant rather than a second request.
			if text, ok := a.completionDocs[c.ResolveKey]; ok {
				c.Documentation = text
			}
		}
		if doc != nil {
			if it.Edit != nil {
				start, end := doc.Span(it.Edit.Range)
				c.Edit = &complete.Edit{Start: start, End: end, Text: it.Edit.NewText}
			}
			if len(it.Additional) > 0 {
				c.Additional = make([]complete.Edit, 0, len(it.Additional))
				for _, te := range it.Additional {
					start, end := doc.Span(te.Range)
					c.Additional = append(c.Additional, complete.Edit{Start: start, End: end, Text: te.NewText})
				}
			}
		}
		cands = append(cands, c)
	}
	a.Complete.Show(prefix, cands, line, col)
	if a.Complete.Open {
		// The first item is selected; if its documentation was deferred, ask
		// for it now so the panel fills without the user having to move.
		a.resolveSelectedCompletion()
	}
	return a.Complete.Open
}

// completionSource is every open buffer plus the declarations raj can already
// find in them. A language server becomes another Source here rather than a
// change to the popup.
func (a *App) completionSnapshots(cur *editor.Pane) []complete.Snapshot {
	open := make(map[string]bool, len(a.Tabs.All()))
	snaps := make([]complete.Snapshot, 0, len(a.Tabs.All()))
	for _, p := range a.Tabs.All() {
		path := p.File.Path
		if path == "" {
			path = p.File.Name()
		}
		open[path] = true
		snaps = append(snaps, complete.Snapshot{
			Path:    path,
			Version: complete.Version(p.File.Session().Version()),
			// A closure rather than the text: an unchanged buffer is never
			// read, so materialising every piece table here would put back
			// most of what the cache saves.
			Text:    p.File.Text,
			Symbols: a.completionSymbols(p, path),
			Current: p == cur,
		})
	}
	a.completeCache.Retain(open)
	return snaps
}

// completionSymbols is the declarations in a buffer, which rank above plain
// words. Scanned every time rather than cached: the symbol scan is 0.42 ms on
// a file larger than anything in this repository, and a second cache keyed the
// same way would be more bookkeeping than it saves.
func (a *App) completionSymbols(p *editor.Pane, path string) []string {
	if !symbols.Supported(path) {
		return nil
	}
	found := symbols.Find(path, p.File.Text())
	names := make([]string, 0, len(found))
	for _, s := range found {
		names = append(names, s.Name)
	}
	return names
}

// snippetSession is an accepted snippet awaiting its tab stops. The stops are
// buffer offsets, so the session is app state: the completion package knows
// only the template's own coordinates, and translating them to the buffer means
// knowing where the insert landed.
type snippetSession struct {
	active bool
	pane   *editor.Pane
	stops  []snippetStop
	at     int
}

// snippetStop is one stop in buffer coordinates: the caret selects
// [start, start+length) when the session arrives at it.
type snippetStop struct {
	start  int
	length int
}

// startSnippetSession selects the first stop of a freshly inserted snippet.
// base is where the literal text landed, end is just past it, and stops are the
// parser's offsets into that text. No stops means no session: the caret is left
// at end, which is where an ordinary insert leaves it.
func (a *App) startSnippetSession(p *editor.Pane, base, end int, stops []complete.TabStop) {
	if p == nil {
		return
	}
	if len(stops) == 0 {
		a.endSnippet()
		if end < 0 {
			end = 0
		}
		if n := p.File.Len(); end > n {
			end = n
		}
		p.Cursors.Set(end, end)
		p.FollowCursor()
		return
	}
	ss := snippetSession{active: true, pane: p}
	for _, s := range stops {
		ss.stops = append(ss.stops, snippetStop{start: base + s.Offset, length: s.Length})
	}
	a.snippet = ss
	a.selectSnippetStop()
}

// selectSnippetStop puts the caret on the session's current stop, selecting its
// placeholder text. The offsets are clamped to the buffer: the session ends on
// any edit, but a socket write or a tab switch landing on the event thread
// between two keys must not be able to point a selection past the end.
func (a *App) selectSnippetStop() {
	p := a.snippet.pane
	if p == nil || a.snippet.at < 0 || a.snippet.at >= len(a.snippet.stops) {
		return
	}
	n := p.File.Len()
	start := a.snippet.stops[a.snippet.at].start
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end := start + a.snippet.stops[a.snippet.at].length
	if end > n {
		end = n
	}
	if end < start {
		end = start
	}
	p.Cursors.Set(end, start)
	p.FollowCursor()
}

// snippetNext advances to the next stop, ending the session past the last one.
func (a *App) snippetNext() {
	if !a.snippet.active {
		return
	}
	if a.snippet.at+1 >= len(a.snippet.stops) {
		a.endSnippet()
		return
	}
	a.snippet.at++
	a.selectSnippetStop()
}

// snippetPrev steps back to the previous stop. The first stop is a floor: shift
// tab at the beginning of a snippet neither wraps nor cancels, so a mistaken key
// cannot drop the caret into text the session was not showing.
func (a *App) snippetPrev() {
	if !a.snippet.active || a.snippet.at == 0 {
		return
	}
	a.snippet.at--
	a.selectSnippetStop()
}

// endSnippet forgets the session without touching the caret or the selection:
// the selection it left is the editor's to clear or replace as usual.
func (a *App) endSnippet() { a.snippet = snippetSession{} }

// handleSnippetKey arbitrates a key against an active session, reporting
// whether the session consumed the action.
//
// While a session runs, tab and shift+tab mean next and previous stop rather
// than indent and outdent — that is the only thing the session claims. Escape
// ends it and falls through, so the editor's cancel still drops the selection;
// every other key, including a typed character, ends the session and is then
// handled normally.
func (a *App) handleSnippetKey(action keys.Action) bool {
	switch action {
	case keys.Indent:
		a.snippetNext()
		return true
	case keys.Outdent:
		a.snippetPrev()
		return true
	case keys.Cancel:
		a.endSnippet()
		return false
	default:
		a.endSnippet()
		return false
	}
}

// acceptSnippet applies a completion whose insert is a snippet template.
//
// It expands the template first and then uses the path the plain accept uses: a
// candidate with a server edit goes through applyServerEdits, and one without
// replaces the typed prefix through ReplaceRange. Either way the edit is one
// change set and one undo, and the literal text — never the template — is what
// reaches the buffer. The parser's stops are translated to buffer coordinates
// and handed to startSnippetSession.
func (a *App) acceptSnippet(p *editor.Pane, prefix string, c complete.Candidate) {
	text, stops := complete.ParseSnippet(c.Snippet)
	if c.Edit == nil {
		head := p.Cursors.Primary().Head
		start := head - len(prefix)
		if start < 0 {
			start = 0
		}
		// One Begin/End brackets the replacement: ReplaceRange deletes the
		// prefix and inserts the expansion, and without the group that is two
		// undo steps rather than the one the plain accept path costs.
		p.File.Begin()
		p.ReplaceRange(start, head, text)
		p.File.End()
		if g, ok := p.TakeLeaseRefusal(); ok {
			// A pending or rejected set owns the span: nothing landed and there
			// is no text to put a session on.
			a.status = leaseNote(g)
			return
		}
		a.startSnippetSession(p, start, start+len(text), stops)
		a.Explorer.Tree.MarkChanged(p.File.Path)
		return
	}

	primary := *c.Edit
	// The server range end is where the cursor was when it answered, but more
	// may have been typed since. Extend the replaced span to the cursor on the
	// same line, exactly as the plain accept path does.
	if head := p.Cursors.Primary().Head; head >= primary.End &&
		p.File.LineOf(head) == p.File.LineOf(primary.End) {
		primary.End = head
	}
	primary.Text = text
	edits := make([]complete.Edit, 0, len(c.Additional)+1)
	edits = append(edits, primary)
	edits = append(edits, c.Additional...)
	if group, ok := applyServerEdits(p, edits); !ok {
		a.status = leaseNote(group)
		return
	}
	// An additional edit before the primary — an import line — moves the
	// primary text right, and the stops move with it. This is the same shift
	// the plain path applies to the caret.
	shift := 0
	for _, e := range c.Additional {
		if e.Start <= primary.Start {
			shift += len(e.Text) - (e.End - e.Start)
		}
	}
	a.startSnippetSession(p, primary.Start+shift, primary.Start+shift+len(text), stops)
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// acceptCompletion applies the chosen candidate.
//
// A candidate with no server edit replaces the typed prefix with the word, as
// it always has: replacing rather than appending the remainder, because the two
// differ when the candidate and the prefix disagree in a way a prefix match
// still allows, and one edit is one undo step.
//
// A candidate from a language server can instead carry a textEdit — a range to
// overwrite — and additional edits such as an import line that must land with
// it. Those go through applyServerEdits, the same one-undo-step path formatting
// uses, so the word and its import land as one change set and one undo.
func (a *App) acceptCompletion(p *editor.Pane, prefix string, c complete.Candidate) {
	// Accepting writes into the buffer, so the same read-only gate every other
	// edit takes applies here too: a popup left open across a mode change must
	// not become the one way text lands.
	if a.readOnly() {
		a.status = a.readOnlyNote()
		return
	}
	// A new accept supersedes any session the previous one left running, so a
	// snippet's stops cannot outlive the word that put them there.
	a.endSnippet()
	if c.Snippet != "" {
		a.acceptSnippet(p, prefix, c)
		return
	}
	if c.Edit == nil {
		if c.Word == "" || !strings.HasPrefix(c.Word, prefix) {
			return
		}
		p.InsertText(c.Word[len(prefix):])
		a.Explorer.Tree.MarkChanged(p.File.Path)
		return
	}

	primary := *c.Edit
	// The server range end is where the cursor was when it answered, but more
	// may have been typed since. When the cursor is still on that line at or
	// after the end, extend the replaced span to it, so the extra characters
	// are overwritten rather than left dangling after the insertion. A cursor
	// that moved to another line is left alone: extending there would delete
	// text the user did not type as part of the word.
	if head := p.Cursors.Primary().Head; head >= primary.End &&
		p.File.LineOf(head) == p.File.LineOf(primary.End) {
		primary.End = head
	}

	edits := make([]complete.Edit, 0, len(c.Additional)+1)
	edits = append(edits, primary)
	edits = append(edits, c.Additional...)
	if group, ok := applyServerEdits(p, edits); !ok {
		// A pending or rejected change set owns part of the span. Nothing
		// landed; name the set rather than completing into text the user has
		// not decided on.
		a.status = leaseNote(group)
		return
	}
	// The cursor belongs after the word the user chose, not in an import line
	// the server added at the top of the file.
	at := primary.Start + len(primary.Text)
	for _, e := range c.Additional {
		if e.Start <= primary.Start {
			at += len(e.Text) - (e.End - e.Start)
		}
	}
	if at < 0 {
		at = 0
	}
	if n := p.File.Len(); at > n {
		at = n
	}
	p.Cursors.Set(at, at)
	p.FollowCursor()
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// Run processes events until the application quits or the host closes.
func (a *App) Run() error {
	// Language servers are subprocesses, so they outlive the editor unless
	// something stops them. An editor that leaves them running is a bug people
	// find in their process list rather than in the editor.
	defer a.servers.stopAll()
	// The socket is a file in the filesystem; leaving it behind means the next
	// process finds a path that answers nothing.
	defer a.StopControl()
	defer a.closeJournals()
	// A quit inside the debounce window would leave the last tab set on disk.
	// Flush once more while the model is whole, before the socket and the
	// journals go away.
	defer func() { _ = a.SaveSession() }()

	a.Draw()
	for e := range a.host.Events() {
		a.Handle(e)
		if a.quit {
			return nil
		}
		a.Draw()
	}
	return nil
}

// CompactInterval bounds how often the idle tick attempts piece compaction.
//
// Compaction is maintenance, not work the user asked for: a merge is
// behaviour-neutral and a flatten copies only bytes a save already wrote, so it
// can wait seconds rather than frames and never sits on the keystroke path.
const CompactInterval = 2 * time.Second

// compactSeen is the session state the last compaction pass saw for a pane.
type compactSeen struct {
	version piecetable.Version
	gen     uint64
}

// pruneCompacted forgets the state of panes that are no longer open. The map is
// keyed on the pane, so a closed pane's entry outlives it, and Go can hand its
// pointer to a new buffer whose (version, generation) happens to match the
// stale pair -- skipping the compaction the new buffer needs. Tabs.All is the
// set compactTick is about to walk, so anything outside it is stale by
// definition.
func (a *App) pruneCompacted() {
	for p := range a.compacted {
		live := false
		for _, q := range a.Tabs.All() {
			if q == p {
				live = true
				break
			}
		}
		if !live {
			delete(a.compacted, p)
		}
	}
}

// compactTick folds fragmented pieces on idle buffers.
//
// It calls Compact within the guarantees Compact documents rather than around
// them. The saved baseline is SavedVersion -- the version the last write put on
// disk -- so a flatten can only copy a span whose every op is already
// committed; an unsaved or undecided span is left in place. Compact refuses a
// Proposed or Rejected span on its own, so a buffer holding unsaved proposals
// keeps the pieces its review hunks are rendered from. The composed text, the
// decisions and the projection are unchanged either way.
//
// Two guards keep it off the frame budget. The interval debounce makes most
// ticks a clock comparison, and a pane whose session version and decision
// generation have not moved since the last attempt is skipped before Compact
// builds its origin index: Compact leaves the version where it found it, so an
// idle buffer is never rescanned for one edit.
func (a *App) compactTick(now time.Time) {
	if !a.compactAt.IsZero() && now.Sub(a.compactAt) < CompactInterval {
		return
	}
	a.compactAt = now
	// Drop the state of panes that are no longer open before reading any of it,
	// so a reused pane pointer cannot match a closed buffer's pair.
	a.pruneCompacted()
	for _, p := range a.Tabs.All() {
		if p == nil || p.File == nil || p.File.Pieces() < 2 {
			continue
		}
		seen := compactSeen{version: p.File.Session().Version(), gen: p.File.DecisionGeneration()}
		if last, ok := a.compacted[p]; ok && last == seen {
			continue
		}
		if a.compacted == nil {
			a.compacted = map[*editor.Pane]compactSeen{}
		}
		p.File.Session().Compact(p.File.SavedVersion())
		a.compacted[p] = seen
	}
}

// Handle applies one event. Exported so tests can drive the app a step at a
// time and assert between steps rather than only at the end.
func (a *App) Handle(e ui.Event) {
	switch ev := e.(type) {
	case ui.Key:
		a.handleKey(ev)
	case ui.Paste:
		a.paste(ev.Text)
	case ui.Mouse:
		a.mouse(ev)
	case ui.Resize:
		// Invalidate here rather than only in the host: after a resize the
		// terminal's contents outside the old geometry are undefined, and that
		// is true whichever host delivered the event.
		a.screen.Resize(ev.Cols, ev.Rows)
		a.host.Invalidate()
	case ui.Focus:
		a.focused = ev.In
		a.syncTheme()
	case ui.Suspended:
		a.screen.Clear()
		a.host.Invalidate()
	case ui.Wake:
		// Background work parks its result where the event thread already
		// looks, and Run draws after every event — so the value of a Wake is
		// mostly in having ended the wait for the next tick. A language server
		// answer is parked the same way and collected here.
		a.applyAnswer()
		a.drainDiagnostics()
		a.drainServerMessages()
		a.drainControl()
		a.drainClient()
	case ui.Tick:
		// A drag held outside the pane scrolls from here, because the pointer
		// is not moving and so there is no event to hang it on.
		a.autoScrollStep()
		// A pointer resting on a hint has sent its last motion event, so the
		// dwell that opens the tooltip is completed here.
		a.dwellHint()
		// A burst of inspection can leave the headless registry over its cap;
		// dropping the clean ones here keeps the bound on the idle path as
		// well as on load.
		a.evictHeadless()
		// Idle work only: retokenising costs tens of milliseconds and must
		// never sit on the keystroke path.
		a.refreshSyntax()
		a.diskCheck()
		// A file created outside raj appears in the tree within a second
		// rather than waiting for raj's own next write.
		a.syncFileTree(time.Now())
		// Every open document is pushed to its server here, not just the
		// visible one, so diagnostics for a buffer the user is not looking at
		// are a real reading. It runs before the hint request, so that request
		// is made against a synced document.
		a.syncDirtyDocs()
		// Inlay hints are asked for here and only here, and only when the
		// visible range or the document version has moved, so this is a
		// debounce rather than a request per keystroke.
		a.maybeRequestHints(a.Tabs.Active())
		// Code lenses are whole-document and change only when the text does,
		// so the same idle tick asks for them once the version has moved.
		a.maybeRequestLenses(a.Tabs.Active())
		// Semantic tokens are whole-document and change only when the text
		// does, so the same idle tick asks for them once the version has
		// moved. They refine the chroma colours rather than replace them.
		a.maybeRequestSemantic(a.Tabs.Active())
		a.sessionTick(time.Now())
		a.compactTick(time.Now())
		a.journalTick(time.Now())
		a.persistTick(time.Now())
		a.Debug.sample()
		// The phone status overlay is transient; the idle tick is its clock.
		a.expirePhoneStatus()
		// Clients parked on watch are woken here, once per change, rather
		// than at every mutation site.
		a.controlTick()
	case ui.Quit:
		a.quit = true
	}
	// The deletion gate is evaluated after every event, not only on a tab
	// switch: a socket request can land a proposal with no keystroke to hang
	// the question on, and a focus moved by the pointer has no chord either.
	a.maybePromptDeletion()
}

// handleKey resolves a chord in the focused scope, then gives the global
// actions first refusal before the focused pane sees it.
func (a *App) handleKey(k ui.Key) {
	if a.Menu.Open() {
		// A context menu is modal for the keys it draws: up/down/home/end,
		// enter and esc belong to it, and a key it does not claim is swallowed
		// rather than leaking to the pane underneath. Quit is the one
		// exception, so a stray menu can never wedge the session.
		//
		// Resolution uses the global scope, not the focused pane's: the
		// editor rebinds enter to a newline, and a menu opened over the
		// editor must still treat enter as "choose".
		action, _, ok := a.keymap.Resolve(keys.Global, k.Event)
		if !ok {
			return
		}
		if action == keys.Quit {
			a.dispatch(action, "")
			return
		}
		if key, chosen := a.Menu.Handle(action); chosen {
			a.chooseMenu(key)
		}
		return
	}
	scope := a.scope()
	action, text, ok := a.keymap.Resolve(scope, k.Event)
	if !ok {
		return
	}
	a.Debug.record(k, scope, action, text)
	// A modal dialog gets first refusal, ahead of the globals: cmd+w while
	// "save changes?" is on screen must not close the tab the question is
	// about.
	if a.Prompt.Open && !passesModal(action) {
		a.Prompt.Handle(action, text)
		a.settlePrompt()
		return
	}
	// The phone drawer is modal for the keys it draws; it routes before the
	// focused pane so Left/Right/Tab cannot also move, indent or complete. The
	// profile guard is repeated at the call site so the ordinary editor never
	// enters the drawer path at all.
	if a.phone && a.drawerKey(action) {
		return
	}
	a.dispatch(action, text)
}

// dispatch routes an action the way a key press does: the globals get first
// refusal, then the focused pane handles it. The command palette runs a chosen
// action through here rather than calling a handler directly, so a palette
// entry does exactly what its chord does — the same review-mode refusals, the
// same per-pane meaning, the same status notes.
func (a *App) dispatch(action keys.Action, text string) {
	if a.handleGlobal(action) {
		return
	}
	defer a.refreshSyntax()

	switch a.focus {
	case FocusPicker:
		path := a.Picker.Handle(action, text)
		if cmd := a.Picker.Action(); cmd != keys.None {
			// A palette choice names an action and closes the overlay; run it
			// from the editor the palette leaves behind, rather than routing
			// the action back into the picker that named it.
			a.focus = FocusEditor
			a.dispatch(cmd, "")
			return
		}
		if i, chosen := a.Picker.ChosenCodeAction(); chosen {
			// A code-action choice names an index into the answer the app is
			// holding; run it from the editor the picker leaves behind, for the
			// same reason a palette choice dispatches from there.
			a.focus = FocusEditor
			a.runCodeAction(i)
			return
		}
		a.openFromPicker(path)
		if !a.Picker.Open && a.focus == FocusPicker {
			a.focus = FocusEditor
		}
	case FocusSidebar:
		a.handleSidebar(action, text)
	default:
		a.handleEditor(action, text)
	}
}

// passesModal names the few actions a dialog must not swallow.
//
// Quit is here so a dialog can never wedge the session. Cut and copy are here
// because the global handler is what knows how to reach a text field at all —
// focusedInput asks the dialog first, so letting them through is what makes
// cmd+c inside a save-as box copy the path rather than do nothing.
func passesModal(a keys.Action) bool {
	return a == keys.Quit || a == keys.Cut || a == keys.Copy
}

// scope is the keymap scope for the focused pane. It is what makes tab indent
// in the editor and cycle focus everywhere else.
func (a *App) scope() keys.Scope {
	switch {
	case a.focus == FocusPrompt:
		return keys.Prompt
	case a.focus == FocusPicker:
		return keys.Picker
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		return keys.Search
	case a.focus == FocusSidebar:
		return keys.Explorer
	default:
		return keys.Editor
	}
}

// handleGlobal deals with actions that work regardless of focus. Returns true
// when the action was consumed.
func (a *App) handleGlobal(action keys.Action) bool {
	switch action {
	case keys.OpenMenu:
		a.openContextMenu()
	case keys.CommandPalette:
		a.commandPalette()
	case keys.ToggleDebug:
		a.Debug.Open = !a.Debug.Open
		a.Debug.sample()
	case keys.ToggleInlayHints:
		a.toggleInlayHints()
	case keys.ApplyInlayEdit:
		a.applyInlayEdit()
	case keys.Quit:
		a.tryQuit()
	case keys.Suspend:
		a.host.Suspend()
	case keys.Save:
		a.saveActive(nil)
	case keys.ToggleReview:
		a.toggleReview()
	case keys.ToggleDrawer:
		// The phone drawer's key fallback. Only the phone profile binds it,
		// so the ordinary editor never reaches this.
		a.drawerOpen = !a.drawerOpen
		if a.drawerOpen {
			a.drawerSel = 0
			a.drawerWant = a.drawerOpenWant()
		}
		return true
	case keys.Reload:
		// Reload replaces the buffer from disk. Refused in Review, and in a
		// client where the disk is not the daemon: it is not an edit, but it
		// would discard the proposals being reviewed.
		if a.readOnly() {
			a.status = a.readOnlyNote()
			return true
		}
		a.reloadActive()
	case keys.AcceptProposed:
		a.reviewProposed(true)
	case keys.RejectProposed:
		a.reviewProposed(false)
	case keys.ClearRejected:
		a.clearRejected()
	case keys.ReviewProposed:
		a.reviewPicker()
	case keys.NextProposed:
		a.cycleProposed(true)
	case keys.PrevProposed:
		a.cycleProposed(false)
	case keys.PendingRemovals:
		a.reopenPendingRemoval()
	case keys.Cut:
		// Cut is an edit. Copy stays live; and cut in a dialog's own field is
		// not the document, so only the editor is refused.
		if a.readOnly() && a.focus == FocusEditor {
			a.status = a.readOnlyNote()
			return true
		}
		a.clip(true)
	case keys.Copy:
		a.clip(false)
	case keys.CopyRelPath:
		// Palette-only: the command has no chord, and the palette dispatches
		// it through this same switch. See keys.Unbound.
		a.copyRelativePath()
	case keys.FocusExplorer:
		a.openSidebar(SidebarExplorer)
	case keys.FocusSearch:
		a.openSidebar(SidebarSearch)
	case keys.FocusProblems:
		a.openSidebar(SidebarProblems)
	case keys.Settings:
		a.openSidebar(SidebarSettings)
	case keys.ToggleWrap:
		if p := a.Tabs.Active(); p != nil {
			p.Wrap = !p.Wrap
			a.WrapDefault = p.Wrap
			p.Viewport.Left, p.Viewport.TopRow = 0, 0
			p.FollowCursor()
			a.status = "wrap off"
			if p.Wrap {
				a.status = "wrap on"
			}
		}
		return true
	case keys.ToggleSidebar:
		a.toggleSidebar()
	case keys.FilePicker:
		a.Picker.Show()
		a.focus = FocusPicker
	case keys.FindInFile:
		if p := a.Tabs.Active(); p != nil {
			if p.Find.Open {
				p.Find.Handle(p, keys.FindInFile, "")
			} else {
				p.Find.Show(p)
			}
			a.focus = FocusEditor
			return true
		}
		return false
	case keys.GotoLine:
		a.gotoLine()
	case keys.GotoSymbol:
		a.gotoSymbol()
	case keys.Hover:
		a.hover()
	case keys.GotoDef:
		a.gotoDefinition()
	case keys.References:
		a.findReferences()
	case keys.GotoDecl:
		a.gotoDeclaration()
	case keys.GotoTypeDef:
		a.gotoTypeDefinition()
	case keys.GotoImpl:
		a.gotoImplementation()
	case keys.SignatureHelp:
		a.signatureHelp()
	case keys.CodeAction:
		a.codeActions()
	case keys.RunCodeLens:
		a.runCodeLens()
	case keys.ToggleFold:
		a.toggleFold()
	case keys.WorkspaceSymbols:
		a.workspaceSymbols()
	case keys.Rename:
		a.renameSymbol()
	case keys.Format:
		a.formatDocument()
	case keys.FormatRange:
		a.formatSelection()
	case keys.FollowLink:
		a.followLink()
	case keys.NewFile:
		a.newFile()
	case keys.CloseTab:
		a.closeTab()
	case keys.ReopenTab:
		// Route through OpenFile so a reopened tab gets the same treatment as
		// any other: theme, highlighting, focus.
		if path, ok := a.Tabs.PopClosed(); ok {
			a.OpenFile(path)
		}
	case keys.NextTab:
		a.Tabs.Next()
		a.focusEditor()
	case keys.PrevTab:
		a.Tabs.Prev()
		a.focusEditor()
	default:
		if n, isTab := tabNumber(action); isTab {
			a.Tabs.Goto(n)
			a.focusEditor()
			return true
		}
		return false
	}
	return true
}

// openSidebar shows a sidebar pane and focuses it. Pressing the chord for the
// pane that already has focus closes it, which makes the binding a toggle
// without needing a second key.
func (a *App) openSidebar(s Sidebar) {
	if a.standalone {
		// No sidebar exists in a standalone editor, so every chord that would
		// open one is a no-op rather than a way to conjure it.
		return
	}
	if a.sidebar == s && a.focus == FocusSidebar {
		a.sidebar = SidebarNone
		a.focus = FocusEditor
		a.TouchSession()
		return
	}
	a.sidebar = s
	a.focus = FocusSidebar
	a.TouchSession()
	switch s {
	case SidebarExplorer:
		a.Explorer.Focus()
	case SidebarSearch:
		a.Search.Focus()
	case SidebarProblems:
		a.refreshProblems()
		a.Problems.Focus()
	case SidebarSettings:
		a.settingsPane.Focus()
	}
}

func (a *App) toggleSidebar() {
	if a.standalone {
		return
	}
	if a.sidebar == SidebarNone {
		a.sidebar = SidebarExplorer
		a.focus = FocusSidebar
		a.Explorer.Focus()
		a.TouchSession()
		return
	}
	a.sidebar = SidebarNone
	a.focus = FocusEditor
	a.TouchSession()
}

// handleSidebar routes to the open sidebar pane. The explorer reports an open
// file (open) or a walk out of the pane (exit); either way focus crosses to the
// editor. Coming back is a chord, deliberately: tab indents in the document, so
// a one-key route in would make editing interruptible.
func (a *App) handleSidebar(action keys.Action, text string) {
	switch a.sidebar {
	case SidebarExplorer:
		path, exit := a.Explorer.Handle(action, text)
		if path != "" {
			// Enter opens for real: OpenFile promotes the preview and hands
			// focus to the editor.
			a.previewPath = ""
			a.OpenFile(path)
		} else if !exit {
			// Arrowing only previews. The selection is compared against what
			// is already shown so a key that did not move the cursor does not
			// reload anything, and a directory (or nothing) clears the memo so
			// coming back to a file previews it afresh.
			if sel, ok := a.Explorer.SelectedPath(); ok {
				if sel != a.previewPath || a.Tabs.Preview() == nil {
					a.previewFile(sel)
				}
			} else {
				a.previewPath = ""
			}
		}
		if exit {
			a.focus = FocusEditor
		}
	case SidebarSearch:
		path, line, exit := a.Search.Handle(action, text)
		if path != "" {
			a.OpenFile(path)
			a.jumpTo(line)
		}
		if exit {
			a.focus = FocusEditor
		}
	case SidebarProblems:
		path, line, exit := a.Problems.Handle(action)
		if path != "" {
			a.OpenFile(path)
			a.jumpTo(line)
		}
		if exit {
			a.focus = FocusEditor
		}
	case SidebarSettings:
		// Escape closes the pane. While the width field is open the pane
		// consumes escape itself to cancel the edit, so that gate comes first.
		if action == keys.Cancel && !a.settingsPane.editing {
			a.sidebar = SidebarNone
			a.focus = FocusEditor
			return
		}
		if a.settingsPane.Handle(a, action, text) {
			a.focus = FocusEditor
		}
	}
}

func (a *App) handleEditor(action keys.Action, text string) {
	p := a.Tabs.Active()
	if p != nil && p.Find.Open {
		// Enter arrives as the literal newline in the editor scope: the keymap
		// masks Confirm there so the pane can insert a line. The bar wants the
		// Confirm meaning — next match on the query row, replace on the
		// replacement row — so normalize it before the review gate, or a
		// refused replace would not be recognised as one.
		if action == keys.None && text == "\n" {
			action, text = keys.Confirm, ""
		}
		// The bar is delegated to before the ordinary read-only check, so a
		// replace would otherwise bypass it. Ask the bar whether the key would
		// change the document and refuse it here if Review mode holds the
		// buffer. A refused replace must still drain the lease the pane
		// recorded, which is what the note below the handle call is for.
		if a.readOnly() && p.Find.WouldEdit(action) {
			a.status = a.readOnlyNote()
			return
		}
		p.Find.Handle(p, action, text)
		a.noteLeaseRefusal(p)
		return
	}
	if p == nil {
		if action == keys.FilePicker {
			a.Picker.Show()
			a.focus = FocusPicker
		}
		return
	}
	// Review mode is read-only for the document. Movement, selection, search,
	// hover and goto fall through; any keystroke that would change the text is
	// refused with a note rather than silently dropped.
	if a.readOnly() && a.reviewRefuses(action, text) {
		return
	}
	// An active snippet session claims tab and shift+tab — the stop-navigation
	// keys — before the editor sees them as indent and outdent. Every other key
	// ends the session and is handled normally, so indent comes back the moment
	// the user leaves the snippet. A session whose pane is no longer the active
	// one is stale and is dropped.
	if a.snippet.active {
		if a.snippet.pane != p {
			a.endSnippet()
		} else if a.handleSnippetKey(action) {
			return
		}
	}
	// Summoning the popup is claimed before the popup itself sees keys, so
	// pressing the chord while it is already open re-asks rather than being
	// swallowed as a navigation key it does not use.
	if action == keys.Complete {
		a.summonCompletion(p)
		return
	}
	// Escape closes the hover panel before anything else sees it, so the first
	// escape dismisses the box rather than a selection underneath it.
	if a.Hover.Handle(action) {
		return
	}
	// Any other action dismisses it. A panel describes the thing the cursor
	// was on, so the moment the cursor moves or the text changes it is
	// describing something that is no longer there — and a stale box floating
	// over the code is worse than the status line it replaced, because it is
	// bigger and looks more authoritative. Asking again is one chord.
	if action != keys.Hover && action != keys.SignatureHelp {
		a.Hover.Hide()
	}
	// The completion popup sees keys before the editor, but claims only the
	// handful it navigates with. Everything else falls through and types,
	// which is what keeps it from being modal: it can be ignored entirely.
	// The prefix has to be read before Handle, which clears it on accept.
	prefix := a.Complete.Prefix()
	if c, accepted, consumed := a.Complete.Handle(action); consumed {
		if accepted {
			a.acceptCompletion(p, prefix, c)
			a.noteLeaseRefusal(p)
		} else {
			// Moving the highlight may have landed on an item whose
			// documentation the server deferred.
			a.resolveSelectedCompletion()
		}
		return
	}
	if action != keys.None {
		if !p.Handle(action) {
			a.status = "unhandled: " + string(action)
		}
		a.noteLeaseRefusal(p)
		a.offerCompletion(p, action == keys.Backspace)
		return
	}
	before := int(p.File.Session().Version())
	p.HandleText(text)
	changed := int(p.File.Session().Version()) != before
	a.noteLeaseRefusal(p)
	a.Explorer.Tree.MarkChanged(p.File.Path)
	a.offerCompletion(p, true)
	a.maybeOnTypeFormat(p, text, changed)
}

// noteLeaseRefusal drains a lease refusal the pane recorded during the last
// edit attempt and turns it into the status note. A keystroke that touches a
// pending or rejected run changes nothing, so the note is the only feedback;
// draining it here keeps the mark from outliving the attempt.
func (a *App) noteLeaseRefusal(p *editor.Pane) {
	if g, ok := p.TakeLeaseRefusal(); ok {
		a.status = leaseNote(g)
	}
}

// paste routes a bracketed-paste payload to whatever has focus.
//
// Gating this on the editor meant a paste into the search box or the picker
// vanished with no feedback — the payload arrives as one event rather than as
// keystrokes, so the text fields never saw it at all and there was nothing for
// them to fall back to.
func (a *App) paste(text string) {
	if text == "" {
		return
	}
	if a.focus == FocusEditor {
		// Paste is an edit, refused in Review mode and in a client with the
		// same note a typed character gets. Fields and dialogs are not the
		// document.
		if a.readOnly() {
			a.status = a.readOnlyNote()
			return
		}
		a.endSnippet()
		a.pasteIntoBuffer(text)
		return
	}
	// A single-line field takes the first line only. It has nowhere to put the
	// rest, and inserting literal newlines gives a query box that renders as
	// control-byte placeholders and searches for something no file contains.
	line := firstLine(text)
	if line == "" {
		return
	}
	switch {
	case a.focus == FocusPrompt:
		a.Prompt.Handle(keys.None, line)
		a.settlePrompt()
	case a.focus == FocusPicker:
		// Not Handle: the picker narrows a pasted path against its index
		// rather than taking it verbatim, and a paste never chooses a file.
		a.Picker.Paste(line)
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		a.handleSidebar(keys.None, line)
	}
	// The explorer has no text field, so a paste there is deliberately ignored
	// rather than routed: its only keys.None handler treats a space as the
	// changed-only toggle, and a pasted space should not flip a filter.
}

// pasteIntoBuffer inserts into the active document. Text from outside cannot
// reuse pieces — unless it is byte-identical to what raj last copied, which is
// the common case of cmd+c then cmd+v and is worth catching.
func (a *App) pasteIntoBuffer(text string) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	clip := a.clipboard
	if clip.Text != text || !clip.Internal() {
		clip = editor.Clip{Text: text}
	}
	p.PasteClip(clip)
	a.noteLeaseRefusal(p)
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// firstLine is the payload up to its first newline, trimmed. A path pasted from
// a shell arrives with a trailing newline, which would otherwise be searched for
// literally.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// gotoLine asks for a line number and jumps there.
//
// It accepts "12", "12:5" and ":5" — the line:column form because that is what
// a compiler, a linter and a stack trace all print, and pasting one in should
// work without editing it down. Out-of-range values clamp rather than refuse:
// "go to line 9999" in a 300-line file means the end, and an error message
// there would be pedantry.
// gotoSymbol lists the active file's declarations in the quick-open overlay.
//
// The source is the language server when one is live and advertises
// documentSymbolProvider, and the leading-keyword scanner otherwise. The server
// parses, so it sees a function assigned to a variable and does not name a
// keyword inside a string; the scanner needs no server and answers instantly,
// which is why it remains the fallback rather than being deleted.
//
// It reuses the picker rather than adding an overlay: a symbol answers with the
// file it lives in and a place, which is the same shape a pasted path already
// had, so opening and jumping is the path openFromPicker was already on.
func (a *App) gotoSymbol() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if a.documentSymbols(p) {
		return
	}
	if !symbols.Supported(p.File.Path) {
		a.status = "no symbols for this file type"
		return
	}
	syms := symbols.Find(p.File.Path, p.File.Text())
	if len(syms) == 0 {
		a.status = "no symbols found"
		return
	}
	a.Picker.ShowSymbols(p.File.Path, syms)
	a.focus = FocusPicker
	a.status = ""
}

// commandPalette lists the keymap actions in the quick-open overlay, so a
// chord can be found by name and run without remembering it. It is the same
// picker as files and symbols; the rows come from keys.Commands and choosing
// one dispatches the action a chord would have resolved to.
//
// The palette does not list itself: running "command palette" from inside the
// palette would only reopen it, and an entry that does nothing is the silent
// no-op this list exists to avoid.
func (a *App) commandPalette() {
	all := keys.Commands()
	rows := make([]picker.Command, 0, len(all))
	for _, c := range all {
		if c.Action == keys.CommandPalette {
			continue
		}
		// A chordless command renders as its name alone: appending an empty
		// chord would leave a trailing separator with nothing after it.
		label := c.Name
		if c.Chord != "" {
			label += "  " + c.Chord
		}
		rows = append(rows, picker.Command{
			Label:  label,
			Action: c.Action,
		})
	}
	a.Picker.ShowCommands(rows)
	a.focus = FocusPicker
	a.status = ""
}

func (a *App) gotoLine() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	here, _ := p.File.LineCol(p.Cursors.Primary().Head)
	a.askSuggestion("Go to line", itoa(here+1), func(answer string, ok bool) {
		if !ok {
			return
		}
		line, col, valid := parsePosition(answer)
		if !valid {
			a.status = "not a line number: " + answer
			return
		}
		if line == 0 {
			line = here + 1 // ":40" is a column on the line already showing
		}
		a.jumpTo(line)
		if col > 0 {
			a.jumpToColumn(line, col)
		}
	})
}

// parsePosition reads "line", "line:col" or ":col". A missing line means the
// one the cursor is already on, which is what ":40" from a column-only
// reference should mean.
func parsePosition(text string) (line, col int, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, false
	}
	lineText, colText := text, ""
	if i := strings.IndexByte(text, ':'); i >= 0 {
		lineText, colText = text[:i], text[i+1:]
	}
	if lineText != "" {
		if line, ok = atoi(lineText); !ok {
			return 0, 0, false
		}
	}
	if colText != "" {
		if col, ok = atoi(colText); !ok {
			return 0, 0, false
		}
	}
	return line, col, line > 0 || col > 0
}

// atoi accepts a non-negative decimal and nothing else. strconv would accept a
// leading sign, and "go to line -3" is a typo rather than a request.
func atoi(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30, true // clamped below anyway
		}
	}
	return n, len(s) > 0
}

// jumpToColumn places the cursor within a line already scrolled to.
//
// Out-of-range values need no guard here: Index.LineStart, Viewport.Center and
// File.OffsetAt all clamp, which is where that invariant belongs. "Go to line
// 9999" in a 300-line file lands on the end because the index says so, not
// because this function checked.
func (a *App) jumpToColumn(line, col int) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	off := p.File.OffsetAt(line-1, col-1)
	p.Cursors.Set(off, off)
}

// jumpToSessionLine moves p's caret to a 1-based session line and centres the
// viewport on the display row that draws it. The line stays file-true and the
// caret lands at that session line's start; only the viewport arithmetic moves
// onto the display map, because a fold above the target shifts its row away
// from its session line. A line a fold hides has no row to land on, so the
// jump is skipped rather than clamped onto the fold marker. With no decisions
// the projection is the identity, so this is exactly the pre-map jump, and it
// is the one path the chord, the sidebar, the review walk and EnterReview share.
func jumpToSessionLine(p *editor.Pane, line int) {
	if p == nil || line <= 0 {
		return
	}
	// Out of range clamps to the end, as the old LineStart/Center path did:
	// only a line that exists but a fold hides is skipped.
	if last := p.File.Lines() - 1; last >= 0 && line-1 > last {
		line = last + 1
	}
	row := p.DispOfDocLine(line - 1)
	if row < 0 {
		return
	}
	off := p.File.LineStart(line - 1)
	p.Cursors.Set(off, off)
	p.Viewport.Center(row, p.DisplayLines())
}

// jumpTo moves the active pane's cursor to a 1-based session line and centres
// it. The mapping lives entirely in jumpToSessionLine, so the chord and every
// review surface cannot drift.
func (a *App) jumpTo(line int) { jumpToSessionLine(a.Tabs.Active(), line) }

// ---------- file lifecycle ----------
//
// New, save and close are one story rather than three features, because the
// interesting cases are where they meet: an unnamed buffer has nowhere to save
// to, and a dirty buffer must not be closed without asking. Both answers arrive
// from a dialog, so each step below takes a continuation and runs it only once
// the previous question has actually been answered.

// newFile opens an empty unnamed buffer.
func (a *App) newFile() {
	p := a.Tabs.NewFile()
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
	a.focus = FocusEditor
	a.status = a.journalStatus(fileWarning(p.File))
}

// closeTab closes the active tab.
func (a *App) closeTab() { a.closeTabAt(a.Tabs.Index()) }

// closeTabAt closes the nth tab, stopping to ask when it holds unsaved work.
//
// The guard lives here rather than in Tabs because Tabs is a container: it has
// no way to ask a question, and no business deciding whether losing an edit is
// acceptable.
//
// The question is asked about the tab that was clicked but answered later, by
// which time tabs may have opened or closed and the index may name a different
// file. So the continuation re-finds the pane rather than trusting the number
// it was handed.
func (a *App) closeTabAt(i int) {
	a.TouchSession()
	panes := a.Tabs.All()
	if i < 0 || i >= len(panes) {
		return
	}
	p := panes[i]
	// A client tab is the daemon document, not unsaved local work: closing it
	// just closes the view, so it never prompts and never saves. The watch is
	// told the path was closed locally so a reconcile does not re-add it.
	if a.attach || !p.File.ViewDirty() {
		a.rememberPosition(p)
		a.closeDoc(p)
		a.Tabs.CloseIndex(i)
		if a.attach {
			a.markClientClosed(p.File.Path, p.File)
			a.saveClientView()
		}
		a.refreshProblems() // the open-files filter just lost a file
		return
	}
	closePane := func() {
		for j, q := range a.Tabs.All() {
			if q == p {
				a.rememberPosition(p)
				a.closeDoc(p)
				a.Tabs.CloseIndex(j)
				return
			}
		}
	}
	// Focus what is being asked about: a dialog naming a file that is not the
	// one on screen reads as a question about something else.
	a.Tabs.Focus(p)
	a.confirm("Unsaved changes", "Save changes to "+p.File.Name()+" before closing?",
		prompt.SaveOptions(), func(answer string, ok bool) {
			switch {
			case !ok || answer == prompt.Cancel:
				return
			case answer == prompt.Discard:
				closePane()
			default:
				// Close only once the bytes are on disk. A save-as can be
				// cancelled, and closing anyway would discard exactly the work
				// the answer asked to keep.
				a.saveActive(func(saved bool) {
					if saved {
						closePane()
					}
				})
			}
		})
}

// saveActive writes the active buffer, asking where to put it when it has no
// path yet. then reports whether the bytes reached the disk — "the user pressed
// save" and "the file was saved" are different events, and a caller about to
// close a tab needs the second.
func (a *App) saveActive(then func(saved bool)) {
	a.savePane(a.Tabs.Active(), then)
}

// savePane is saveActive for a specific pane. A resumed formatted save needs it
// because the buffer being completed need not be the one on screen: the user
// may have switched tabs while the server worked, and a change set that arrived
// in the meantime is re-routed through the same review.
func (a *App) savePane(p *editor.Pane, then func(saved bool)) {
	if p == nil {
		report(then, false)
		return
	}
	// An attached client does not own the bytes: save acts on the daemon, and a
	// refusal (pending proposals, disk conflict) is the status rather than a
	// local write the next wake would overwrite.
	if a.attach {
		a.saveRemote(p, then)
		return
	}
	// Proposed change sets get reviewed before they get saved: the gesture
	// opens a listing to step through rather than accepting sight-unseen.
	// Accept-all-and-save stays one chord, so the review is a glance, not a
	// gate.
	if pending := p.File.Session().Pending(); len(pending) > 0 {
		a.reviewSave(p, pending, then)
		return
	}
	if p.File.Path == "" {
		a.saveAs(p, then)
		return
	}
	a.saveNamed(p, then)
}

// saveNamed writes a buffer that already has a path, offering to create a
// missing parent directory exactly as save-as does.
//
// Without this, saving a buffer whose directory does not exist yet failed with
// the raw writeAtomic ENOENT against a path the buffer already carried — which
// reads as a no-op, leaves the buffer dirty, and loses the content when the tab
// is closed without saving. ensureParent already exists for save-as; a normal
// save reaching it is the whole fix.
func (a *App) saveNamed(p *editor.Pane, then func(saved bool)) {
	a.ensureParent(p.File.Path, then, func() { a.writeTo(p, p.File.Path, then) })
}

// saveAs asks where an unnamed buffer should go, starting at the workspace root.
func (a *App) saveAs(p *editor.Pane, then func(saved bool)) {
	a.saveAsIn(p, a.root, then)
}

// saveAsIn is saveAs with the folder the prompt starts in named by the caller.
//
// The field is seeded with dir and a separator so the common answer is a bare
// file name, and a relative answer is resolved against the root rather than the
// process's working directory — which is wherever raj happened to be launched
// from and is not what "notes.md" means to someone looking at this tree. The
// explorer's New File item passes the folder the menu was opened in; every other
// caller passes the workspace root through saveAs, so ordinary save-as is
// unchanged. It is a seed hook, not a second save path: the prompt, the
// overwrite question, the missing-parent offer and the write are all shared.
func (a *App) saveAsIn(p *editor.Pane, dir string, then func(saved bool)) {
	if dir == "" {
		dir = a.root
	}
	a.askPath("Save as", dir+string(filepath.Separator), func(answer string, ok bool) {
		if !ok || answer == "" {
			a.status = "save cancelled"
			report(then, false)
			return
		}
		path := answer
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.root, path)
		}
		// Stat rather than trusting the name: the picker and the tree both
		// show what is already there, but a typed path does not, and silently
		// replacing a file is the one outcome nobody recovers from.
		if _, err := os.Stat(path); err == nil {
			a.confirm("File exists", filepath.Base(path)+" already exists. Overwrite?",
				[]string{prompt.Overwrite, prompt.Cancel}, func(ans string, ok bool) {
					if !ok || ans != prompt.Overwrite {
						a.status = "save cancelled"
						report(then, false)
						return
					}
					a.writeTo(p, path, then)
				})
			return
		}
		a.ensureParent(path, then, func() { a.writeTo(p, path, then) })
	})
}

// copyRelativePath copies the active buffer's path relative to the workspace
// root. It is the palette-only sibling of the context menu's Copy Path: the
// same clipboard helper, but spelled the way a shell rooted at the workspace
// wants it.
//
// A buffer with no path has nothing to copy, so it is refused in words rather
// than putting an empty string on the clipboard. A path outside the root —
// save-as permits one — has no relative spelling, so the absolute path is
// copied and the status line says which fallback happened.
func (a *App) copyRelativePath() {
	p := a.Tabs.Active()
	if p == nil || p.File.Path == "" {
		a.status = "nothing to copy: this buffer has no path"
		return
	}
	path := p.File.Path
	if underDir(a.root, path) {
		if rel, err := filepath.Rel(a.root, path); err == nil {
			a.copyPath(rel)
			return
		}
	}
	a.copyPath(path)
	a.status = "path is outside the workspace; copied " + path
}

// reloadActive takes the version on disk deliberately, rather than as an answer
// to a save that failed.
//
// Without it the only route to a reload was to press save on a file you did not
// want to save and pick the third button, which is a strange thing to have to
// do to say "give me what is on disk". The common case is not a conflict at
// all: something rewrote the file, you have typed nothing, and you want to see
// it.
//
// A clean buffer reloads without a question — there is nothing to lose, and
// asking would train the answer out of people. A dirty one asks, because this
// is the gesture that throws away the only copy of something.
func (a *App) reloadActive() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if p.File.Path == "" {
		a.status = "nothing to reload: this buffer has never been saved"
		return
	}
	if !p.File.ViewDirty() {
		a.reload(p, nil)
		return
	}
	a.confirm("Discard your changes",
		"Reloading "+p.File.Name()+" will throw away your unsaved changes. Continue?",
		[]string{prompt.Discard, prompt.Cancel}, func(ans string, ok bool) {
			if !ok || ans != prompt.Discard {
				a.status = "reload cancelled"
				return
			}
			a.reload(p, nil)
		})
}

// conflict is the dialog for a file that changed underneath the buffer.
//
// Three answers, because two were not enough: Cancel left the user holding a
// buffer they still could not save and no way forward except closing the tab
// and losing the work anyway. Reload is the way forward that takes the other
// writer's version.
//
// Reload is offered first when the buffer is clean, because then it is
// lossless and almost always what was meant — the file moved and you had not
// touched it. On a dirty buffer it is destructive in the same way Overwrite is,
// just pointed the other way, so it sits in the middle and asks again.
func (a *App) conflict(p *editor.Pane, path string, then func(saved bool)) {
	dirty := p.File.ViewDirty()
	options := []string{prompt.Overwrite, prompt.Reload, prompt.Cancel}
	question := filepath.Base(path) + " was modified by another program. " +
		"Overwrite it with this buffer, or reload and lose your changes?"
	if !dirty {
		options = []string{prompt.Reload, prompt.Overwrite, prompt.Cancel}
		question = filepath.Base(path) + " was modified by another program. " +
			"This buffer has no unsaved changes, so reloading costs nothing."
	}
	a.confirm("Changed on disk", question, options, func(ans string, ok bool) {
		switch {
		case !ok || ans == prompt.Cancel:
			a.status = "save cancelled — the file on disk is newer"
			report(then, false)
		case ans == prompt.Overwrite:
			a.write(p, path, true, then)
		case !dirty:
			a.reload(p, then)
		default:
			// A second question, because this is the one answer that destroys
			// work that exists nowhere else. Overwrite discards bytes that are
			// still in git, or in whatever wrote them; this discards the only
			// copy.
			a.confirm("Discard your changes",
				"Reloading "+filepath.Base(path)+" will throw away your unsaved changes. Continue?",
				[]string{prompt.Discard, prompt.Cancel}, func(ans string, ok bool) {
					if !ok || ans != prompt.Discard {
						a.status = "save cancelled — the file on disk is newer"
						report(then, false)
						return
					}
					a.reload(p, then)
				})
		}
	})
}

// reload takes the version on disk. It reports the save as not having happened,
// which is true: the caller that asked to save — closing a tab, quitting — must
// not treat a reload as permission to carry on and drop the buffer.
func (a *App) reload(p *editor.Pane, then func(saved bool)) error {
	name := p.File.Name()
	if err := p.Reload(); err != nil {
		a.status = "cannot reload: " + err.Error()
		report(then, false)
		return err
	}
	p.ClearDiskStale()
	a.status = "reloaded " + name + " from disk"
	report(then, false)
	return nil
}

// ensureParent makes sure a path's directory exists, asking first.
//
// Without this, saving into a directory that is not there yet failed with the
// raw os.WriteFile error — "no such file or directory" against a path the user
// had just typed in full, which reads as though the save itself was rejected
// rather than as a missing folder they could make.
//
// It asks rather than creating silently. Everything else save-as does happens
// to a file the user named; creating directories is the one step that puts
// something on disk they did not, and a typo in a path would otherwise leave a
// stray tree behind with no indication it had been made.
func (a *App) ensureParent(path string, then func(saved bool), cont func()) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err == nil {
		cont()
		return
	}
	// The relative form, because the absolute one is usually too long for the
	// dialog and the part that matters is what is new.
	shown := dir
	if rel, err := filepath.Rel(a.root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		shown = rel
	}
	a.confirm("Create directory", shown+" does not exist. Create it?",
		[]string{prompt.Create, prompt.Cancel}, func(ans string, ok bool) {
			if !ok || ans != prompt.Create {
				a.status = "save cancelled"
				report(then, false)
				return
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				a.status = "cannot create directory: " + err.Error()
				report(then, false)
				return
			}
			a.Explorer.Tree.Refresh()
			cont()
		})
}

// writeTo names the buffer if needed and writes it.
func (a *App) writeTo(p *editor.Pane, path string, then func(saved bool)) {
	a.write(p, path, false, then)
}

// write is writeTo with the answer to the conflict question already given.
// force is what an "Overwrite" answer turns into.
func (a *App) write(p *editor.Pane, path string, force bool, then func(saved bool)) {
	was := p.File.Path
	renamed := was != path
	p.File.SetPath(path)
	a.appendJournal(p)
	// At most one formatted save waits on a server at a time. A save that
	// arrives while one is in flight supersedes it: the old request's answer is
	// dropped by the generation, and every continuation waiting on the old save
	// rides the new one, so a close or quit that was waiting still completes.
	thens := []func(bool){then}
	if ps := a.pendingWrite; ps != nil {
		if ps.pane != p {
			// Another buffer save owns the single wait slot. This save writes
			// now without waiting, and that save still resumes.
			a.finishWrite(p, path, force, renamed, was, thens)
			return
		}
		// The same buffer saves again: supersede the in-flight request and
		// carry its continuations onto this write.
		a.saveGen++
		a.pendingWrite = nil
		thens = append(ps.thens, then)
	}
	if a.beginWillSave(p, path, force, thens) {
		return // the write resumes when the server answer lands
	}
	a.finishWrite(p, path, force, renamed, was, thens)
}

// finishWrite is the write proper: the half of a save that puts bytes on disk,
// after any willSaveWaitUntil answer has been applied. It is separate from
// write so the server hook can defer it without re-deriving the rename and
// conflict state, and so the synchronous and the resumed save run exactly the
// same code. thens are every save continuation this one write answers.
func (a *App) finishWrite(p *editor.Pane, path string, force, renamed bool, was string, thens []func(bool)) {
	var saveStart time.Time
	var tSaved time.Time
	if timing.On {
		saveStart = time.Now()
	}
	save := p.File.Save
	if force {
		save = p.File.SaveOver
	}
	if err := save(); err != nil {
		// Something else wrote the file since raj read it. Saving anyway is a
		// legitimate answer — it is often raj's own formatter or a git
		// checkout of the same content — but it is not one to assume, because
		// the bytes it discards are not recoverable from anywhere raj knows
		// about. Ask, and remember the name so the retry does not re-prompt
		// for a path.
		if errors.Is(err, editor.ErrDiskChanged) {
			p.File.SetPath(was)
			a.conflict(p, path, func(saved bool) { runThens(thens, saved) })
			return
		}
		// Put the name back. Leaving it set means the buffer claims a path it
		// is not at, so the next plain save writes there without asking —
		// which turns one visible failure into a silent one.
		p.File.SetPath(was)
		a.status = "save failed: " + err.Error()
		runThens(thens, false)
		return
	}
	if timing.On {
		tSaved = time.Now()
	}
	a.status = "saved " + p.File.Name()
	p.ClearDiskStale()
	// Every connected driver hears that the file reached disk, so a harness can
	// react without polling. Best-effort: a delivery failure is not a save
	// failure, and with no control listener this is nothing.
	a.notifySaved(p.File.Path)
	a.lspSaved(p)
	// The user pressing save is the approval. Nothing else in the editor can
	// write this file — an agent's own save is refused while its change sets
	// are proposed — so reaching here means a human chose to put these bytes on
	// disk, and leaving the marks set would make the next save refuse work that
	// is already committed.
	//
	// All-or-nothing, and the status line says how much, because that is the
	// only thing this gesture can honestly mean. Per-hunk review is a different
	// gesture and wants its own binding.
	if n := p.File.AcceptPending(); n > 0 {
		a.status += fmt.Sprintf(" (accepted %d proposed change set(s))", n)
	}
	a.recordWritten(p)
	a.flushJournal(p)
	a.dropJournal(path)

	if timing.On {
		// The save proper is tSaved-saveStart: text materialise, writeAtomic
		// and the saved-content digest. accept is the second Pending walk,
		// AcceptPending over the same journal. saveDoneAt starts the clock the
		// next frame reads to report save-to-clean.
		a.saveDoneAt = time.Now()
		timing.Log("save", a.saveDoneAt.Sub(saveStart),
			"write", tSaved.Sub(saveStart),
			"accept", a.saveDoneAt.Sub(tSaved))
	}
	if renamed {
		// A rename is the only save that puts a file in the tree that was not
		// there before. Refreshing on every save would walk the directory on
		// the keystroke path for nothing.
		a.Explorer.Tree.Refresh()
		a.warmSaved(p)
	}
	runThens(thens, true)
}

func report(then func(bool), ok bool) {
	if then != nil {
		then(ok)
	}
}

// tryQuit exits, stopping first if anything would be lost.
//
// cmd+w already guards one tab. Leaving quit unguarded made that a property of
// which chord you happened to press rather than of the buffer, and quit is the
// one with every unsaved tab behind it rather than one.
//
// A second Quit while the question is on screen forces the exit. ctrl+c is what
// people press when they want out now, and a dialog that answers it by asking
// again is the wedge the modal was written to avoid.
func (a *App) tryQuit() {
	if a.quitAsked {
		a.quit = true
		return
	}
	// A client owns no bytes: every tab is the daemon document, so there is
	// nothing unsaved to save and quit goes.
	if a.attach {
		a.quit = true
		return
	}
	dirty := a.Tabs.Dirty()
	if len(dirty) == 0 {
		a.quit = true
		return
	}
	a.quitAsked = true
	a.confirm("Unsaved changes", quitMessage(dirty), prompt.SaveOptions(),
		func(answer string, ok bool) {
			a.quitAsked = false
			switch {
			case !ok || answer == prompt.Cancel:
				return
			case answer == prompt.Discard:
				a.quit = true
			default:
				a.saveAllThenQuit(dirty)
			}
		})
}

// quitMessage names the file when there is one and counts them when there are
// several. A list of names would not fit the dialog, and a bare count when only
// one thing is at stake withholds the only detail that matters.
func quitMessage(dirty []*editor.Pane) string {
	if len(dirty) == 1 {
		return "Save changes to " + dirty[0].File.Name() + " before quitting?"
	}
	return "Save changes to " + itoa(len(dirty)) + " files before quitting?"
}

// saveAllThenQuit walks the dirty tabs, saving each and quitting only if they
// all land.
//
// It recurses through the continuation rather than looping, because any of them
// may be unnamed and stop for a path — and a loop would have run to the end
// before the first dialog was answered. Each tab is focused before it is saved,
// so a save-as dialog is asking about the buffer on screen.
func (a *App) saveAllThenQuit(dirty []*editor.Pane) {
	if len(dirty) == 0 {
		a.quit = true
		return
	}
	p := dirty[0]
	a.Tabs.Focus(p)
	a.saveActive(func(saved bool) {
		if !saved {
			// Cancelling a path is cancelling the quit. Exiting anyway would
			// discard exactly the work the answer asked to keep.
			a.status = "quit cancelled"
			return
		}
		a.saveAllThenQuit(dirty[1:])
	})
}

// ---------- dialogs ----------

// ask and confirm open a modal question and hand it focus. Both hide the file
// picker: it is the other full-screen overlay, and two of them at once means
// keys going somewhere invisible.
func (a *App) ask(title, initial string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.Ask(title, initial, done) // a plain question completes nothing
}

// askPath is ask for a question whose answer is a file path, so tab completes
// and the directory being typed into is listed.
func (a *App) askPath(title, initial string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.AskList(title, initial, a.completePath, a.pathCandidates, done)
}

// completePath extends a partially typed path, for tab in a save-as field.
//
// It completes to the longest common prefix of the matches rather than to the
// first one, which is what makes repeated tabs converge instead of cycling: a
// directory of similar names fills in as far as they agree and then stops,
// leaving the ambiguous part for you to resolve.
//
// A trailing separator is added when the single match is a directory, so tab
// walks down a tree one press per level instead of needing a slash typed
// between each.
func (a *App) completePath(text string) string {
	dir, base := filepath.Split(text)
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.root, dir)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "" // nothing to read: say nothing rather than guessing
	}
	var matches []os.DirEntry
	for _, e := range entries {
		// Hidden files are completed only when the prefix asks for them, the
		// same rule the tree and the search walk use. Tab is a convenience,
		// and offering a directory of dotfiles to somebody who typed nothing
		// is not one.
		if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if strings.HasPrefix(e.Name(), base) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	common := matches[0].Name()
	for _, e := range matches[1:] {
		common = sharedPrefix(common, e.Name())
	}
	if len(matches) == 1 && matches[0].IsDir() {
		common += string(filepath.Separator)
	}
	return dir + common
}

// pathCandidates lists the entries of the directory a save-as field names,
// filtered to those matching the partial name, so the field shows what is
// already there rather than only completing once enough of a name has been
// typed to be unambiguous.
//
// It mirrors completePath's rules — the same directory resolution, the same
// hidden-file rule — because a listing and a completion that disagreed about
// which entries exist would be worse than either alone.
func (a *App) pathCandidates(text string) []prompt.Candidate {
	dir, base := filepath.Split(text)
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.root, dir)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var out []prompt.Candidate
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(e.Name(), base) {
			continue
		}
		c := prompt.Candidate{Text: dir + e.Name(), Display: e.Name()}
		if e.IsDir() {
			// A trailing separator both shows it is a directory and makes
			// choosing it descend: the field then names the directory and the
			// next listing is its contents.
			c.Text += string(filepath.Separator)
			c.Display += string(filepath.Separator)
			c.Dir = true
		}
		out = append(out, c)
	}
	return out
}

// sharedPrefix is the longest prefix two names agree on, in bytes.
//
// Bytes rather than runes: a partial multi-byte rune cannot be produced here,
// because both inputs are whole names and any shared prefix that splits a rune
// would require them to differ inside it — which means they differ at the first
// byte of that rune too, and the prefix stops before it.
func sharedPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

// askSuggestion is ask with the seed selected: the field offers a default that
// typing replaces rather than a prefix that typing extends.
func (a *App) askSuggestion(title, suggestion string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.AskSuggestion(title, suggestion, done)
}

func (a *App) confirm(title, message string, options []string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.Confirm(title, message, options, done)
}

func (a *App) beforePrompt() {
	if a.focus != FocusPrompt {
		a.promptReturn = a.focus
	}
	a.Picker.Hide()
	a.Menu.Hide()

	if a.promptReturn == FocusPicker {
		a.promptReturn = FocusEditor
	}
	a.focus = FocusPrompt
}

// settlePrompt restores focus once a dialog has closed for good. A continuation
// is free to open the next question in a chain, so this checks whether one did
// rather than assuming an answered dialog is the end of the story.
func (a *App) settlePrompt() {
	if !a.Prompt.Open && a.focus == FocusPrompt {
		a.focus = a.promptReturn
	}
}

// tabNumber maps the goto-tab actions to their index.
func tabNumber(a keys.Action) (int, bool) {
	for i, want := range []keys.Action{keys.GotoTab1, keys.GotoTab2, keys.GotoTab3,
		keys.GotoTab4, keys.GotoTab5, keys.GotoTab6, keys.GotoTab7, keys.GotoTab8,
		keys.GotoTab9} {
		if a == want {
			return i + 1, true
		}
	}
	return 0, false
}

// Status returns the current status message, for tests.
func (a *App) Status() string { return a.status }

// Notice puts a startup message in the status line, for a caller that knows
// something the App does not — a stale terminal config, say. It yields to a
// status the App set itself: those are about the document in front of the
// user, and this is about their setup.
func (a *App) Notice(msg string) {
	if a.status == "" {
		a.status = msg
	}
}

// CtrlAliasReport is what CtrlAliases did, for the startup notice: empty when
// aliases are off or nothing collided, otherwise the count of super chords
// with no free ctrl alias and which they were. main writes it to stderr, not
// the status line, because it describes the keymap rather than the document.
func (a *App) CtrlAliasReport() string {
	if !a.ctrlAliases || len(a.ctrlAliasCollisions) == 0 {
		return ""
	}
	return fmt.Sprintf("%d super chord(s) have no free ctrl alias: %s",
		len(a.ctrlAliasCollisions), strings.Join(a.ctrlAliasCollisions, ", "))
}

// Focused reports which pane has focus, for tests.
func (a *App) Focused() Focus { return a.focus }

// SidebarMode reports which sidebar is open, for tests.
func (a *App) SidebarMode() Sidebar { return a.sidebar }

// Pane is the active editing pane, or nil.
func (a *App) Pane() *editor.Pane { return a.Tabs.Active() }

// focusEditor moves focus to the editor if a file is open. Choosing a tab means
// you intend to read or edit it, so leaving the keys in the sidebar makes every
// tab switch a two-step operation.
func (a *App) focusEditor() {
	if a.Tabs.Active() != nil {
		a.focus = FocusEditor
	}
}

// refreshSyntax starts a background retokenise if the text has changed. It runs
// after every key rather than only on the idle tick: tokenising is on its own
// goroutine, so starting it immediately costs nothing on the keystroke path and
// removes the visible lag between editing a line and it recolouring.
func (a *App) refreshSyntax() {
	if p := a.Tabs.Active(); p != nil {
		p.File.RefreshSyntax()
	}
}

// diskCheck marks a tab when its file changed on disk since raj read or wrote
// it, so the prompt on save stops being a surprise. One stat per open tab, on
// the idle tick; panes already marked are skipped until the user acts.
func (a *App) diskCheck() {
	// A snapshot buffer has no disk of its own to become stale: the daemon is
	// the file, and a local stat would only raise a false prompt.
	if a.attach {
		return
	}
	for _, p := range a.Tabs.All() {
		if p.DiskStale() {
			continue
		}
		if p.File.Path == "" || !p.File.DiskChanged() {
			continue
		}
		if p.File.ViewDirty() {
			// Unsaved work: keep the mark and let save raise the question.
			p.MarkDiskStale()
			continue
		}
		// Clean: take the disk version now, the same path the Reload gesture
		// uses, rather than marking a conflict the user would have to resolve.
		a.reload(p, nil)
	}
}

// treeScanInterval bounds how often the idle tick checks the explorer against
// the filesystem. A second is fast enough that a file created in another window
// shows up while the user is looking, and slow enough that the scan is a
// rounding error on the idle budget. The scan reads only the expanded
// directories' listings, not the repository.
const treeScanInterval = time.Second

// syncFileTree refreshes the explorer when the host filesystem moved under it:
// a file created, removed or renamed outside raj. The idle tick runs many times
// a second, so the check is throttled; the tree's own scan is listings-only.
func (a *App) syncFileTree(now time.Time) {
	if a.Explorer == nil || a.Explorer.Tree == nil {
		return
	}
	if now.Sub(a.treeScanAt) < treeScanInterval {
		return
	}
	a.treeScanAt = now
	if a.Explorer.Tree.ChangedOnDisk() {
		a.Explorer.Tree.Refresh()
	}
}

// focusedInput is the text field the keys are going into, or nil when they are
// going into the document, a list, or a toggle.
//
// cut and copy are global actions — they are claimed before any pane sees the
// chord — so without this they always acted on Tabs.Active(): cmd+c in the
// search box copied from the editor, and cmd+x edited the document being
// searched. Asking the focused thing whether it owns a selection is the fix,
// and the find bar needs it as much as the sidebar does, because it lives
// inside the editor pane rather than beside it.
func (a *App) focusedInput() *widget.Input {
	if in := a.Prompt.ActiveInput(); in != nil {
		return in // modal: it owns the keys whatever focus says
	}
	switch {
	case a.focus == FocusPicker:
		return a.Picker.ActiveInput()
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		return a.Search.ActiveInput()
	case a.focus == FocusEditor:
		if p := a.Tabs.Active(); p != nil {
			return p.Find.ActiveInput()
		}
	}
	return nil
}

// clip copies or cuts to the system clipboard via OSC 52, and keeps an internal
// copy so paste works even where the terminal refuses clipboard writes.
func (a *App) clip(cut bool) {
	if in := a.focusedInput(); in != nil {
		a.clipField(in, cut)
		return
	}
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	clip := p.Copy()
	if cut {
		clip = p.Cut()
	}
	if clip.Empty() {
		return
	}
	a.clipboard = clip
	a.host.SetClipboard(clip.Text)
	verb := "copied"
	if cut {
		verb = "cut"
	}
	a.status = verb + " " + itoa(len(clip.Text)) + " bytes"
}

// clipField copies or cuts inside a text field. A field carries no pieces, so
// this is plain text rather than an editor.Clip with a snapshot — pasting it
// back into a document costs a copy, which for a search query is nothing.
func (a *App) clipField(in *widget.Input, cut bool) {
	text := in.Copy()
	if cut {
		text = in.Cut()
	}
	if text == "" {
		// Nothing selected. Deliberately not falling back to the whole field:
		// cmd+x with no selection emptying the search box would be a
		// destructive surprise, and the document behaviour it would be
		// imitating (cut the current line) has no counterpart here.
		return
	}
	a.clipboard = editor.Clip{Text: text}
	a.host.SetClipboard(text)
	verb := "copied"
	if cut {
		verb = "cut"
	}
	a.status = verb + " " + itoa(len(text)) + " bytes"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
