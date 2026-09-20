package app

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"raj/internal/editor"
	"raj/internal/session"
)

// Session capture and restore.
//
// Both directions run on the event thread, because both read or write the
// model. Saving happens on the way out rather than on every change: the state
// is worth a file write once per session, not once per keystroke, and a crash
// losing a scroll position is not worth the disk traffic to prevent.

// SessionSaveInterval bounds how often the session file is rewritten while the
// editor is running.
//
// Saving only on a clean exit lost everything to a crash or a kill, and looked
// like the editor had silently forgotten the last hour. Saving on every change
// is the other extreme: this is view state, and a scroll position is not worth
// a write per keystroke. A few seconds behind is close enough that nobody
// notices the gap, and rare enough that the disk traffic is nothing.
const SessionSaveInterval = 3 * time.Second

// TouchSession marks the session worth rewriting. Cheap enough to call from
// anything that changes where the user is.
func (a *App) TouchSession() { a.sessionDirty = true }

// sessionTick writes the session if something changed and enough time has
// passed. Called from the idle tick, where the cost cannot land on a keystroke.
func (a *App) sessionTick(now time.Time) {
	// A client keeps no local session: the daemon owns the document, and
	// writing positions for buffers this process does not own would make the
	// next local run restore them.
	if a.attach {
		return
	}
	if !a.sessionDirty || a.root == "" || a.NoRestore {
		return
	}
	// A change to the tab set is not view churn: quitting inside the window
	// would reopen a tab the user just closed, so it is written on the next
	// tick. The interval is left for cursor and scroll movement.
	tabs := a.sessionFingerprint()
	structural := tabs != a.sessionTabs
	if !structural && !a.sessionSaved.IsZero() && now.Sub(a.sessionSaved) < SessionSaveInterval {
		return
	}
	a.sessionDirty = false
	a.sessionSaved = now
	// A failure here is not worth interrupting anyone for: the next tick tries
	// again, and the worst case is the position it had before.
	_ = a.SaveSession()
	a.sessionTabs = tabs
}

// sessionFingerprint names the tab set a write would save: the ordered paths
// and the active index. Cursor and scroll are deliberately left out, so moving
// around is not mistaken for a structural change.
func (a *App) sessionFingerprint() string {
	st := a.SessionState()
	parts := make([]string, 0, len(st.Tabs)+1)
	for _, t := range st.Tabs {
		parts = append(parts, t.Path)
	}
	// Paths never contain a NUL, so the join cannot make two different tab
	// sets look the same.
	parts = append(parts, strconv.Itoa(st.Active))
	return strings.Join(parts, "\x00")
}

// SessionState captures where the workspace is now.
func (a *App) SessionState() session.State {
	st := session.State{Active: a.Tabs.Index(), Focus: focusName(a.focus)}
	for _, p := range a.Tabs.All() {
		// A preview is transient view state, not a committed tab. Leaving it
		// out keeps arrowing through files from rewriting the session, and
		// keeps a restart from reopening a file nobody opened.
		if p == a.Tabs.Preview() {
			continue
		}
		// An unnamed buffer is keyed on nothing, so there is nowhere to put it.
		// It needs the dirty-buffer journal, not a path. A git transient is
		// skipped too: git invoked the editor, not the user, and a commit
		// message that reappears next launch is noise.
		if p.File.Path == "" || isGitPath(p.File.Path) {
			continue
		}
		hints := p.Hints
		st.Tabs = append(st.Tabs, session.Tab{
			// Top is a display row: Viewport.Top's own coordinate. Ratio is
			// Top/DisplayLines, and restore prefers it because a fold that moved
			// under the saved position cannot shift a proportion. See RestoreSession.
			Path:   p.File.Path,
			Cursor: p.Cursors.Primary().Head,
			Top:    p.Viewport.Top,
			Ratio:  scrollRatio(p),
			Wrap:   p.Wrap,
			Hints:  &hints,
		})
	}
	// Active is an index into the tabs that were kept, so skipping unnamed ones
	// would otherwise point at the wrong tab — or past the end.
	st.Active = a.activeAmong(st.Tabs)
	if a.Explorer != nil && a.Explorer.Tree != nil {
		st.Expanded = a.Explorer.Tree.ExpandedDirs()
	}
	sb := sidebarName(a.sidebar)
	st.Sidebar = &sb
	return st
}

// scrollRatio is the scroll position as a proportion of the display, so a
// restored session lands at the same place in a file that has grown. Zero for
// an empty file: there is nothing to be proportional to, and the saved Top is
// then exactly right. It is coordinate-free, which is why restore prefers it
// to Top: a fold that moved between sessions cannot shift a ratio.
func scrollRatio(p *editor.Pane) float64 {
	lines := p.DisplayLines()
	if lines <= 1 {
		return 0
	}
	return float64(p.Viewport.Top) / float64(lines)
}

// activeAmong maps the live tab index onto the saved list.
func (a *App) activeAmong(saved []session.Tab) int {
	active := a.Tabs.Active()
	if active == nil || active.File.Path == "" {
		return 0
	}
	for i, t := range saved {
		if t.Path == active.File.Path {
			return i
		}
	}
	return 0
}

// SaveSession writes the current position. Errors are returned rather than
// surfaced: failing to record a scroll position is not worth interrupting a
// quit for, and the caller decides whether anyone should hear about it.
func (a *App) SaveSession() error {
	if a.root == "" || a.NoRestore {
		return nil
	}
	if a.state == nil {
		// No store: nowhere to persist. The legacy session.json is read by the
		// one-shot migration but nothing writes it any more, so this is a
		// no-op rather than a resurrected file fallback.
		return nil
	}
	st := a.SessionState()
	// Record each saved tab position as part of the save, so a file whose tab
	// is closed later is still remembered. The blob is the session; the rows
	// are per-file hints that outlive the tab set.
	for _, t := range st.Tabs {
		_ = a.state.SetPosition(t.Path, t.Cursor, t.Top)
	}
	blob, err := session.Encode(st)
	if err != nil {
		return err
	}
	return a.state.PutSession(blob)
}

// CloseState releases the workspace state database. It is nil-safe and
// idempotent, so it can be deferred whether or not a store was ever opened.
func (a *App) CloseState() {
	if a.state == nil {
		return
	}
	_ = a.state.Close()
}

// migrateState moves a workspace's legacy .raj state into the XDG state
// directory the first time a build that keeps state there runs. The store
// database, the op logs and the trash all move; .raj/hidden is workspace
// config, not state, and is never touched.
//
// It is best-effort and runs before the store opens: the destination is created
// first, a move whose destination already exists is skipped (which also absorbs
// a race with another instance migrating at once), and a move that fails leaves
// the legacy file in place and returns the first error. The caller continues
// against the new location rather than failing to start.
//
// The database's WAL and SHM sidecars move after state.db, and only if the
// database itself moved: a sidecar without its database is worse than leaving
// it behind. A crashed writer's sidecars therefore travel with it, and the
// migrated database keeps its un-checkpointed transactions.
func migrateState(root string) error {
	dir := session.StateDir(root)
	legacy := session.Dir(root)
	if dir == "" || legacy == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// moveOne relocates one entry and reports whether it moved anything, so
	// the database's sidecars can be gated on the database itself: a WAL or
	// SHM that arrived without its database would be worse than leaving it.
	moveOne := func(name string) bool {
		src := filepath.Join(legacy, name)
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(src); err != nil {
			return false // nothing legacy of this kind
		}
		if _, err := os.Stat(dst); err == nil {
			return false // already migrated, or another instance got there first
		}
		if err := os.Rename(src, dst); err != nil {
			if os.IsExist(err) || os.IsNotExist(err) {
				return false // another instance won the race
			}
			note(err)
			return false
		}
		return true
	}
	if moveOne("state.db") {
		// The WAL and SHM are part of the database. They follow it only after
		// the database itself has moved, and each is best-effort: a missing or
		// already-moved sidecar is not an error, and one that will not move is
		// reported once and left for inspection.
		moveOne("state.db-wal")
		moveOne("state.db-shm")
	}
	moveOne("logs")
	moveOne("trash")
	return firstErr
}

// loadSession reads the remembered state, preferring the store and adopting a
// session.json left by an older build. A store that is absent or unreadable
// degrades to the file: persistence is a convenience, never a reason not to
// start.
func (a *App) loadSession() session.State {
	if a.state == nil {
		return session.Load(a.root)
	}
	blob, ok, err := a.state.Session()
	if err != nil {
		// A store read that failed still leaves the file as a source, so the
		// session is not lost to a database problem.
		return session.Load(a.root)
	}
	if ok {
		// The store is the source of truth now, so a session.json left behind
		// by an earlier migration (or one whose os.Remove failed) would
		// otherwise linger forever. Best-effort and harmless if it will not go.
		if path := session.File(a.root); path != "" {
			_ = os.Remove(path)
		}
		return session.Decode(blob, a.root)
	}
	// No stored session: adopt a session.json from before the store. The
	// store is populated before the file is removed, so a crash between the
	// two migrates again rather than losing the state.
	path := session.File(a.root)
	if path == "" {
		return session.State{}
	}
	if _, err := os.Stat(path); err != nil {
		return session.State{}
	}
	st := session.Load(a.root)
	if blob, err := session.Encode(st); err == nil {
		if a.state.PutSession(blob) == nil {
			// Best-effort: a file that will not go is re-migrated next time,
			// which is harmless.
			_ = os.Remove(path)
		}
	}
	return st
}

// rememberPosition records where a pane was before it is dropped. An unnamed
// buffer has no path and a nil store has nowhere to put it, so both are
// no-ops.
func (a *App) rememberPosition(p *editor.Pane) {
	if a.state == nil || p == nil || p.File.Path == "" {
		return
	}
	_ = a.state.SetPosition(p.File.Path, p.Cursors.Primary().Head, p.Viewport.Top)
}

// applyStoredPosition moves a newly opened pane to the cursor and scroll row
// recorded for its path, if any. Both are clamped to the buffer as it is now:
// a remembered position is a hint, and the file may have changed underneath
// it.
func (a *App) applyStoredPosition(p *editor.Pane) {
	if a.state == nil || p == nil || p.File.Path == "" {
		return
	}
	cursor, top, ok, err := a.state.Position(p.File.Path)
	if err != nil || !ok {
		return
	}
	if cursor > p.File.Len() {
		cursor = p.File.Len()
	}
	if cursor < 0 {
		cursor = 0
	}
	p.Cursors.Set(cursor, cursor)
	if max := p.DisplayLines() - 1; top > max {
		top = max
	}
	if top < 0 {
		top = 0
	}
	p.Viewport.Top = top
}

// RestoreSession reopens what was left. It is a hint: anything that fails to
// open is skipped, because a workspace that has moved on since last time should
// still start.
func (a *App) RestoreSession() {
	if a.root == "" || a.NoRestore {
		return
	}
	defer a.restoreJournals()
	st := a.loadSession()
	if len(st.Tabs) == 0 && len(st.Expanded) == 0 && st.Sidebar == nil {
		return
	}
	for _, d := range st.Expanded {
		a.Explorer.Tree.Expand(d)
	}

	restored := 0
	for _, t := range st.Tabs {
		p, err := a.Tabs.Open(t.Path)
		if err != nil {
			// Binary, too large, unreadable now: skipped silently. These are
			// reported when a person asks for the file; on a restore nobody
			// asked, and a column of refusals is not a welcome.
			continue
		}
		a.settle(p)
		p.Wrap = t.Wrap
		// Hints is tri-state: absent (nil) means the session predates the
		// field, and settle leaves the app default in place. An explicit value
		// is what the pane saved and overrides it.
		if t.Hints != nil {
			p.Hints = *t.Hints
		}
		// Clamped against the file as it is now, not as it was: Load validated
		// against its size, but the authority is the buffer that just opened.
		at := t.Cursor
		if at > p.File.Len() {
			at = p.File.Len()
		}
		p.Cursors.Set(at, at)
		// Top is a display row. A session written by an older build stored a
		// session line instead; while the projection is the identity here (the
		// journal has not been restored yet) the two are the same number, and the
		// clamp below keeps either in range. Ratio is coordinate-free and
		// supersedes Top whenever it was written, so an old session cannot jump.
		top := t.Top
		if t.Ratio > 0 {
			// Proportional restore: keep the same place in the display rather
			// than the same row, so a file that has grown opens where the work
			// was and a fold above the saved position cannot shift it.
			top = int(math.Round(t.Ratio * float64(p.DisplayLines())))
		}
		// The projection may hide rows under a fold, so the bound is display
		// rows, not session lines. Load validated the stored value against the
		// file on disk; the authority is the buffer that just opened.
		if max := p.DisplayLines() - 1; top > max {
			top = max
		}
		if top < 0 {
			top = 0
		}
		p.Viewport.Top = top
		restored++
	}
	if restored == 0 && st.Sidebar == nil {
		return
	}
	if restored > 0 && st.Active < restored {
		a.Tabs.Goto(st.Active + 1)
	}
	if f, ok := focusValue(st.Focus); ok {
		a.focus = f
	} else {
		a.focus = FocusEditor
	}
	a.restoreSidebar(st.Sidebar)
	// The viewport was restored before the pane knew its height, so the first
	// resize clamps it. Nothing else to do here: leaving Top as saved and
	// letting the frame reconcile is what keeps a restored scroll position
	// exact when the terminal is the same size, and sane when it is not.
}

// sidebarName is the session name for a sidebar pane: empty means it was
// closed.
func sidebarName(s Sidebar) string {
	switch s {
	case SidebarExplorer:
		return "explorer"
	case SidebarSearch:
		return "search"
	case SidebarProblems:
		return "problems"
	case SidebarSettings:
		return "settings"
	}
	return ""
}

// restoreSidebar applies a saved sidebar kind. A nil pointer means the session
// predates the field, so the default pane is kept; an empty name means the
// sidebar was closed. When the saved focus was the sidebar, the pane's own
// Focus is restored too, the same way openSidebar does it.
func (a *App) restoreSidebar(name *string) {
	if name == nil {
		return
	}
	switch *name {
	case "":
		a.sidebar = SidebarNone
	case "explorer":
		a.sidebar = SidebarExplorer
	case "search":
		a.sidebar = SidebarSearch
	case "problems":
		a.sidebar = SidebarProblems
	case "settings":
		a.sidebar = SidebarSettings
	default:
		return
	}
	if a.focus != FocusSidebar {
		return
	}
	switch a.sidebar {
	case SidebarExplorer:
		a.Explorer.Focus()
	case SidebarSearch:
		a.Search.Focus()
	case SidebarProblems:
		a.Problems.Focus()
	case SidebarSettings:
		a.settingsPane.Focus()
	}
}

// isGitPath reports whether a path lives under a .git directory. Git opens its
// own transient files (COMMIT_EDITMSG, MERGE_MSG, rebase todos) in the editor,
// and those must not persist as tabs or dirty-buffer journals: the user did not
// open them, and a commit message that reappears next launch is noise.
func isGitPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

// settle applies the per-pane defaults a freshly opened tab gets, so a restored
// tab is not subtly different from one the person opened themselves.
func (a *App) settle(p *editor.Pane) {
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
}

// Focus is stored by name rather than by number so that reordering the enum
// does not silently restore a different pane.
func focusName(f Focus) string {
	switch f {
	case FocusSidebar:
		return "sidebar"
	default:
		return "editor"
	}
}

func focusValue(s string) (Focus, bool) {
	switch s {
	case "sidebar":
		return FocusSidebar, true
	case "editor":
		return FocusEditor, true
	}
	return FocusEditor, false
}
