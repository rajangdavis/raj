package app

import (
	"strings"
	"testing"

	"raj/internal/keys"
)

// keys.Unimplemented is the one hand-maintained part of the generated
// keybinding reference, so it needs something to keep it honest. This is that
// something: press each listed chord and assert the application still does not
// know what to do with it.
//
// It catches the drift that matters in both directions. Implement an action and
// forget to unlist it, and the reference goes on telling people a working chord
// does nothing — so this fails. Remove an action's handler without listing it,
// and TestEveryBoundActionIsHandled below fails instead.
//
// The signal is the "unhandled:" status the editor already sets, which exists
// for exactly this reason: an action reaching the pane with nobody to take it.
func TestUnimplementedActionsReallyAreUnimplemented(t *testing.T) {
	for action, why := range keys.Unimplemented {
		h := newHarness(t, "package main\n")
		h.handleKeyAction(action)
		if !strings.HasPrefix(h.Status(), "unhandled:") {
			t.Errorf("%s is listed as unimplemented (%q) but something handled it; "+
				"drop it from keys.Unimplemented and regenerate KEYBINDINGS.md",
				action, why)
		}
	}
}

// The other direction: every bound action that is NOT listed must be handled
// somewhere. A chord taken from the terminal that silently does nothing is the
// worst of both worlds — the terminal has lost it and raj is not using it.
//
// Actions that end the session or reach outside the process are skipped by
// name, since pressing them in a test would take the test with them.
func TestEveryBoundActionIsHandled(t *testing.T) {
	skip := map[keys.Action]bool{
		keys.Quit:    true, // ends the session
		keys.Suspend: true, // stops the process
	}
	for _, action := range keys.ActionsByName() {
		if skip[action] || keys.Unimplemented[action] != "" {
			continue
		}
		h := newHarness(t, "package main\nfunc F() {}\n")
		h.handleKeyAction(action)
		if strings.HasPrefix(h.Status(), "unhandled:") {
			t.Errorf("%s is bound but nothing handles it; either implement it or "+
				"add it to keys.Unimplemented and regenerate KEYBINDINGS.md", action)
		}
	}
}
