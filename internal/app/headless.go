package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"raj/internal/editor"
)

// Headless buffers: a document without a tab.
//
// open used to be the only way to make a path addressable over the socket, and
// open both loads the file and announces it as a tab. An agent surveying twenty
// files therefore put twenty tabs in front of the user. The split here is spec
// 10's: a buffer can be loaded, tracked and served -- its journal, stores, line
// index and version, the document state -- with no tab and no presentation
// state at all. A tab appears only when the buffer has to be seen: a proposal
// is created, the user opens it, or open is asked to show it.
//
// The registry is a slice ordered most recently used first, and it is bounded.
// A clean headless buffer is always droppable and reloadable, so the cap is a
// memory bound rather than a correctness one. A headless buffer is never dirty
// and never holds a pending proposal: the write and caret paths announce first
// (host.Apply, host.Patch, host.Goto), so dropping one cannot lose work.
const headlessMax = 8

// loadHeadless loads path into the registry, or returns the pane already there.
//
// Only a path inside the workspace is loaded. The Guard now pre-checks the
// resolved root in canonical before it calls Resolve, so this lexical check is
// the backstop for loadHeadless's one caller: a path outside the tree is
// refused before any bytes are read. A missing, unreadable, binary or
// oversized file is an error; the caller turns it into the same "no open
// buffer" a closed path already answered with.
func (a *App) loadHeadless(path string) (*editor.Pane, error) {
	if path == "" || !a.pathInRoot(path) {
		return nil, fmt.Errorf("not a buffer path")
	}
	if p, ok := a.findHeadless(path); ok {
		return p, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	f, err := editor.Open(path, a.tabWidth)
	if err != nil {
		return nil, err
	}
	f.SetIndentDefault(editor.Indent{Tabs: a.Tabs.IndentTabs, Width: a.tabWidth})
	if a.Tabs.TabWidthPinned() {
		f.SetTabWidth(a.tabWidth)
	}
	p := editor.NewPane(f)
	a.headless = append([]*editor.Pane{p}, a.headless...)
	a.evictHeadless()
	return p, nil
}

// findHeadless looks a path up in the registry and marks it most recently used.
// The comparison is sameFile, the same one tabs use, so a symlink spelling and
// its target are one buffer rather than two.
func (a *App) findHeadless(path string) (*editor.Pane, bool) {
	for i, p := range a.headless {
		if !sameFile(path, p.File.Path) {
			continue
		}
		a.refreshHeadless(p)
		if i > 0 {
			a.headless = append(a.headless[:i], a.headless[i+1:]...)
			a.headless = append([]*editor.Pane{p}, a.headless...)
		}
		return p, true
	}
	return nil, false
}

// refreshHeadless re-reads p from disk when the file moved under it, so a
// cached headless buffer is not the snapshot it was first loaded from.
//
// A headless buffer is clean by definition: the write and caret paths announce
// it into a tab before they touch it, so there is no unsaved content and no
// pending proposal a reload could discard. The check is DiskChanged, the same
// one stat the tab diskCheck pays, so an unchanged file -- the common case --
// costs one stat and no read. Any stat, read or refusal error keeps the cached
// copy: with the file gone, unreadable or no longer openable, what raj already
// holds is the only text there is. Reload replaces the document in place and
// clamps the caret, so the pane and its File survive for callers holding them.
func (a *App) refreshHeadless(p *editor.Pane) {
	if p == nil || p.File == nil || !p.File.DiskChanged() {
		return
	}
	_ = p.Reload()
}

// isHeadless reports whether p is in the registry. It marks a buffers listing
// entry and is what the write and caret paths check before announcing.
func (a *App) isHeadless(p *editor.Pane) bool {
	for _, q := range a.headless {
		if q == p {
			return true
		}
	}
	return false
}

// announceIfHeadless gives p a tab if it is headless, and does nothing
// otherwise. A buffer about to hold a proposal, or about to take the caret,
// must be visible: work the user has to decide about is never hidden.
func (a *App) announceIfHeadless(p *editor.Pane) {
	if p != nil && a.isHeadless(p) {
		a.announce(p)
	}
}

// announceIfHeadlessQuiet is announceIfHeadless without the focus move. A
// socket request that reveals a buffer gets it a tab to sit in, but the user
// stays where they were: the tab is there to be found, not to interrupt.
func (a *App) announceIfHeadlessQuiet(p *editor.Pane) {
	if p != nil && a.isHeadless(p) {
		a.announceQuiet(p)
	}
}

// announce reveals a loaded pane as a tab and focuses it.
func (a *App) announce(p *editor.Pane) {
	if p == nil {
		return
	}
	a.announceQuiet(p)
	a.Tabs.Focus(p)
	if !a.Prompt.Open {
		a.focus = FocusEditor
	}
}

// announceQuiet reveals a loaded pane as a tab without moving the user's active
// tab or focus.
//
// The pane keeps its document state -- the piece table and journal are the same
// objects -- and gains the presentation state OpenFile gives every tab. Adding
// it rather than reopening it is what preserves the version an agent read
// before it proposed, so the offsets still land on the bytes they were measured
// against.
//
// A tab the user has to find is still a tab: a proposal is never hidden. What
// is left alone is which tab is in front. An already-open pane is simply left
// as it is; a newly added one is appended and the previous active tab is
// restored, or, when the editor had nothing open, keeps the slot so something
// is visible.
func (a *App) announceQuiet(p *editor.Pane) {
	if p == nil {
		return
	}
	for _, q := range a.Tabs.All() {
		if q == p {
			// Already a tab: it has its presentation, and moving the user is
			// not this call's business.
			return
		}
	}
	prev := a.Tabs.Active()
	a.unregisterHeadless(p)
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
	a.Tabs.Add(p)
	if prev != nil && a.Tabs.Contains(prev) {
		a.Tabs.Focus(prev)
	}
	// A revealed buffer owes the same warning a freshly opened one does: mixed
	// line endings a save will normalise, or indentation the file's format
	// rejects. It lands here, before the caller does the work that revealed the
	// buffer (a proposal, a goto), so a note the caller sets afterwards still
	// wins the status line.
	a.status = fileWarning(p.File)
	// A tab appeared, and the session is the set of tabs; losing one to a crash
	// is a file to find again.
	a.TouchSession()
}

// dropHeadless removes p from the registry and forgets what was keyed on its
// path. It reports whether p was there.
func (a *App) dropHeadless(p *editor.Pane) bool {
	for i, q := range a.headless {
		if q == p {
			a.headless = append(a.headless[:i], a.headless[i+1:]...)
			a.rememberPosition(p)
			a.closeDoc(p)
			return true
		}
	}
	return false
}

// unregisterHeadless removes p without the close bookkeeping, for announce,
// which is moving the pane rather than dropping it.
func (a *App) unregisterHeadless(p *editor.Pane) {
	for i, q := range a.headless {
		if q == p {
			a.headless = append(a.headless[:i], a.headless[i+1:]...)
			return
		}
	}
}

// evictHeadless keeps the registry bounded, dropping the oldest clean buffers
// first.
//
// A headless buffer is always clean and never holds a decision -- the write
// paths announce before a proposal lands, so a set with anything to decide has
// a tab -- and a clean buffer is exactly the disk bytes, so dropping it costs a
// re-read rather than work. The dirty and pending checks are belt and braces:
// if the invariant ever breaks, the buffer is announced instead of dropped, so
// the failure mode is a tab and never lost text.
func (a *App) evictHeadless() {
	for len(a.headless) > headlessMax {
		i := len(a.headless) - 1
		p := a.headless[i]
		a.headless = a.headless[:i]
		if p.File.ViewDirty() || len(p.File.Session().Pending()) > 0 {
			a.announce(p)
			continue
		}
		a.rememberPosition(p)
		a.closeDoc(p)
	}
}

// pathInRoot reports whether path is inside the workspace root, the same
// boundary the Guard enforces. loadHeadless checks it before reading bytes as
// the lexical backstop; the Guard's resolved check in canonical is the gate and
// runs before Resolve.
func (a *App) pathInRoot(path string) bool {
	root := filepath.Clean(a.root)
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(root, clean)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
