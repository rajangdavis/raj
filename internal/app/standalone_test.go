package app

import "testing"

// A standalone editor opens no workspace store: there is no state directory
// for the file's directory and no session to save or restore.
func TestStandaloneOpensNoStore(t *testing.T) {
	h := newOptionsHarness(t, "x\n", Options{Standalone: true})
	if h.state != nil {
		t.Errorf("standalone opened a workspace store: %p", h.state)
	}
	if !h.NoRestore {
		t.Error("standalone did not imply NoRestore; the session is not inert")
	}
	if !h.standalone {
		t.Error("Options.Standalone did not reach the app")
	}
}

// A standalone editor serves no control listener, even when an address is
// passed to the app: it is a private single-file session.
func TestStandaloneServesNoControl(t *testing.T) {
	h := newOptionsHarness(t, "x\n", Options{Standalone: true})
	if err := h.StartControlAddrs([]string{"tcp://127.0.0.1:0"}); err != nil {
		t.Fatalf("StartControlAddrs(standalone) = %v", err)
	}
	if got := h.ControlPaths(); got != nil {
		t.Errorf("standalone control paths = %v, want none", got)
	}
	if tok := h.ControlToken(); tok != "" {
		t.Errorf("standalone minted control token %q, want none", tok)
	}
}

// The sidebar is hidden for the whole standalone run: it starts closed, the
// FocusExplorer chord cannot open it, and the frame keeps the full width for
// the editor.
func TestStandaloneSidebarStaysHidden(t *testing.T) {
	h := newOptionsHarness(t, "x\n", Options{Standalone: true})
	if got := h.SidebarMode(); got != SidebarNone {
		t.Fatalf("standalone sidebar = %v, want closed", got)
	}
	if got := h.Focused(); got != FocusEditor {
		t.Fatalf("standalone focus = %v, want the editor", got)
	}
	h.press("shift+super+e")
	if got := h.SidebarMode(); got != SidebarNone {
		t.Errorf("after FocusExplorer, sidebar = %v, want closed", got)
	}
	if got := h.Focused(); got != FocusEditor {
		t.Errorf("after FocusExplorer, focus = %v, want the editor", got)
	}
	l := h.layout(120, 12)
	if l.ShowSidebar {
		t.Error("standalone layout shows the sidebar")
	}
	if l.EditorW != 120 {
		t.Errorf("standalone editor width = %d, want the full 120", l.EditorW)
	}
}

// The other sidebar chords are no-ops too, so no route conjures the explorer
// or the problems pane in a one-file session.
func TestStandaloneSidebarChordsAreNoOps(t *testing.T) {
	h := newOptionsHarness(t, "x\n", Options{Standalone: true})
	for _, chord := range []string{"shift+super+e", "shift+super+f", "shift+super+m", "super+b"} {
		h.press(chord)
		if got := h.SidebarMode(); got != SidebarNone {
			t.Errorf("after %s, sidebar = %v, want closed", chord, got)
		}
		if got := h.Focused(); got != FocusEditor {
			t.Errorf("after %s, focus = %v, want the editor", chord, got)
		}
	}
}
