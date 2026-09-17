package app

import (
	"math"
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
		// It needs the dirty-buffer journal, not a path.
		if p.File.Path == "" {
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
	return session.Save(a.root, a.SessionState())
}

// RestoreSession reopens what was left. It is a hint: anything that fails to
// open is skipped, because a workspace that has moved on since last time should
// still start.
func (a *App) RestoreSession() {
	if a.root == "" || a.NoRestore {
		return
	}
	defer a.restoreJournals()
	st := session.Load(a.root)
	if len(st.Tabs) == 0 && len(st.Expanded) == 0 {
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
	if restored == 0 {
		return
	}
	if st.Active < restored {
		a.Tabs.Goto(st.Active + 1)
	}
	if f, ok := focusValue(st.Focus); ok {
		a.focus = f
	} else {
		a.focus = FocusEditor
	}
	// The viewport was restored before the pane knew its height, so the first
	// resize clamps it. Nothing else to do here: leaving Top as saved and
	// letting the frame reconcile is what keeps a restored scroll position
	// exact when the terminal is the same size, and sane when it is not.
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
