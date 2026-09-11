package app

import (
	"math"
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
	if !a.sessionSaved.IsZero() && now.Sub(a.sessionSaved) < SessionSaveInterval {
		return
	}
	a.sessionDirty = false
	a.sessionSaved = now
	// A failure here is not worth interrupting anyone for: the next tick tries
	// again, and the worst case is the position it had before.
	_ = a.SaveSession()
}

// SessionState captures where the workspace is now.
func (a *App) SessionState() session.State {
	st := session.State{Active: a.Tabs.Index(), Focus: focusName(a.focus)}
	for _, p := range a.Tabs.All() {
		// An unnamed buffer is keyed on nothing, so there is nowhere to put it.
		// It needs the dirty-buffer journal, not a path.
		if p.File.Path == "" {
			continue
		}
		st.Tabs = append(st.Tabs, session.Tab{
			Path:   p.File.Path,
			Cursor: p.Cursors.Primary().Head,
			Top:    p.Viewport.Top,
			Ratio:  scrollRatio(p),
			Wrap:   p.Wrap,
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

// scrollRatio is the scroll position as a proportion of the document, so a
// restored session lands at the same place in a file that has grown. Zero for
// an empty file: there is nothing to be proportional to, and the saved Top is
// then exactly right.
func scrollRatio(p *editor.Pane) float64 {
	lines := p.File.Lines()
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
		// Clamped against the file as it is now, not as it was: Load validated
		// against its size, but the authority is the buffer that just opened.
		at := t.Cursor
		if at > p.File.Len() {
			at = p.File.Len()
		}
		p.Cursors.Set(at, at)
		p.Viewport.Top = t.Top
		if t.Ratio > 0 {
			// Proportional restore: keep the same place in the document rather
			// than the same line number, so a file that has grown opens where
			// the work was. Clamped against the file as it is now, like the
			// cursor above; the first resize would clamp it too, but the
			// authority is the buffer that just opened.
			p.Viewport.Top = int(math.Round(t.Ratio * float64(p.File.Lines())))
			if max := p.File.Lines() - 1; p.Viewport.Top > max {
				p.Viewport.Top = max
			}
		}
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
