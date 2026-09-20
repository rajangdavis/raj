package app

import (
	"strings"
	"testing"
	"time"

	"raj/internal/keys"
	"raj/internal/store"
	"raj/internal/ui"
)

// The settings pane opens from its palette action and focuses the sidebar, and it shows
// the heading and all five settings plus the scope selector. Without the
// SidebarSettings case in openSidebar the action would move focus nowhere, and
// without the drawSidebar case the pane would be a blank column.
func TestSettingsPaneOpensAndShowsSettings(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.handleKeyAction(keys.Settings)

	if got := h.SidebarMode(); got != SidebarSettings {
		t.Fatalf("sidebar = %v, want settings", got)
	}
	if h.Focused() != FocusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.Focused())
	}
	text := h.host.Text()
	if !strings.Contains(text, "Settings") {
		t.Errorf("the pane heading is missing:\n%s", text)
	}
	for _, label := range []string{"Scope", "Tab width", "Indent with tabs", "Wrap lines", "Auto pairs", "Inlay hints"} {
		if !strings.Contains(text, label) {
			t.Errorf("the pane does not show %q:\n%s", label, text)
		}
	}
}

// Confirming a bool row writes through SetSetting and moves the effective
// value: the pane is not a private copy. Without the activate path calling
// write/SetSetting the row would toggle nothing.
func TestSettingsPaneTogglesWrapThroughSetSetting(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.handleKeyAction(keys.Settings)
	if got := h.Settings()["wrap"]; got != "true" {
		t.Fatalf("setup: wrap = %q, want true", got)
	}

	// Rows: scope, tab width, indent, wrap. Three downs lands on wrap.
	h.press("down", "down", "down")
	h.press("enter")

	if got := h.Settings()["wrap"]; got != "false" {
		t.Errorf("wrap after toggle = %q, want false", got)
	}
	if h.WrapDefault {
		t.Error("the running app still wraps; SetSetting did not apply")
	}
	rows, err := h.state.Settings(store.ScopeWorkspace)
	if err != nil {
		t.Fatalf("read workspace scope: %v", err)
	}
	if rows["wrap"] != "false" {
		t.Errorf("stored workspace wrap = %q, want false", rows["wrap"])
	}
}

// The width steps with left/right and is typed and committed with enter. The
// committed value is live on the app, so the next applied write starts from it.
func TestSettingsPaneStepsAndCommitsTabWidth(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.handleKeyAction(keys.Settings)
	h.press("down") // tab width
	h.press("right")

	if got := h.Settings()["tab_width"]; got != "3" {
		t.Fatalf("tab_width after one step = %q, want 3", got)
	}
	h.press("enter") // open the digit field
	h.typeText("8")
	h.press("enter") // commit

	if got := h.Settings()["tab_width"]; got != "8" {
		t.Errorf("tab_width after commit = %q, want 8", got)
	}
	if h.tabWidth != 8 {
		t.Errorf("live tabWidth = %d, want 8", h.tabWidth)
	}
}

// The scope row chooses where a change is written. Switching to user and then
// toggling wrap writes the user row and leaves the workspace scope untouched.
func TestSettingsPaneScopeSwitchWritesUserRow(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.handleKeyAction(keys.Settings)
	// The scope row is first and defaults to workspace, so one right turns it
	// into user.
	h.press("right")
	h.press("down", "down", "down") // wrap
	h.press("enter")

	if got := h.Settings()["wrap"]; got != "false" {
		t.Errorf("wrap = %q, want false", got)
	}
	user, err := h.state.Settings(store.ScopeUser)
	if err != nil {
		t.Fatalf("read user scope: %v", err)
	}
	if user["wrap"] != "false" {
		t.Errorf("user wrap = %q, want the write stored in the user scope", user["wrap"])
	}
	if ws, err := h.state.Settings(store.ScopeWorkspace); err != nil {
		t.Fatalf("read workspace scope: %v", err)
	} else if _, written := ws["wrap"]; written {
		t.Errorf("workspace wrap = %q, want no workspace row after a user-scope write", ws["wrap"])
	}
}

// Without a store the pane still opens and shows the effective values, but a
// change is refused out loud rather than silently lost, and nothing panics.
func TestSettingsPaneWithoutStoreRefusesWithStatus(t *testing.T) {
	host := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { host.Close() })
	a := New(host, "", 2)
	a.Search.Debounce = time.Nanosecond
	h := &harness{App: a, host: host}

	h.handleKeyAction(keys.Settings)
	if h.SidebarMode() != SidebarSettings || h.Focused() != FocusSidebar {
		t.Fatalf("sidebar = %v focus = %v, want the settings pane", h.SidebarMode(), h.Focused())
	}
	before := a.Settings()["wrap"]
	h.press("down", "down", "down")
	h.press("enter")

	if got := h.Status(); !strings.Contains(got, "no workspace store") {
		t.Errorf("status = %q, want a persistence refusal", got)
	}
	if got := a.Settings()["wrap"]; got != before {
		t.Errorf("wrap = %q, want it unchanged at %q without a store", got, before)
	}
}

// Tab is the existing one-way sidebar exit: it hands focus to the editor and
// leaves the pane open behind it. Escape is the pane's own close.
func TestSettingsPaneTabLeavesAndEscapeCloses(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.handleKeyAction(keys.Settings)
	h.press("tab")
	if h.Focused() != FocusEditor {
		t.Fatalf("focus = %v, want the editor after tab", h.Focused())
	}
	if h.SidebarMode() != SidebarSettings {
		t.Fatalf("sidebar = %v, want it left open behind the editor", h.SidebarMode())
	}

	h.handleKeyAction(keys.Settings) // reopen; the action toggles
	h.press("esc")
	if h.SidebarMode() != SidebarNone {
		t.Errorf("sidebar = %v, want closed after escape", h.SidebarMode())
	}
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor after closing", h.Focused())
	}
}
