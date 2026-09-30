package editor

import (
	"strings"
	"testing"

	"raj/internal/keys"
)

// selActions is every selecting action the key package can bind, gathered from
// all three of its tables so one added there is covered here without an edit.
func selActions() []keys.Action {
	var out []keys.Action
	add := func(a keys.Action) {
		if !strings.HasPrefix(string(a), "sel_") {
			return
		}
		for _, have := range out {
			if have == a {
				return
			}
		}
		out = append(out, a)
	}
	for _, b := range keys.Bindings {
		add(b.Action)
	}
	for _, b := range keys.Reclaim {
		add(b.Action)
	}
	for _, n := range keys.Natives {
		add(n.Action)
	}
	return out
}

// Handle's comment promises that a movement and its selecting variant cannot
// drift. The pair table is what makes that structural; this is what makes it
// checked: every selecting action is either a pair row's sel or one of the two
// page-select actions.
//
// PageUp and PageDown stay in paneActions rather than motionPairs because their
// plain form scrolls the view without moving the cursors, while their selecting
// form moves them, so the two cannot share a handler. The exception is named
// here so a third selecting action cannot join it silently.
func TestEverySelActionHasAPair(t *testing.T) {
	sel := selActions()
	if len(sel) < 12 {
		t.Fatalf("found %d selecting actions, want at least 12: the key tables changed", len(sel))
	}
	for _, a := range sel {
		if _, ok := motionPairFor(a); ok {
			continue
		}
		if a == keys.SelPageUp || a == keys.SelPageDown {
			if _, _, ok := lookupAction(a); !ok {
				t.Errorf("selecting action %q has neither a pair row nor a paneAction row", a)
			}
			continue
		}
		t.Errorf("selecting action %q has no motion pair row", a)
	}
}

// Every action is served by exactly one row across the two tables. A duplicate
// would leave the later row dead and hide a lost edit.
func TestActionTablesAreDisjoint(t *testing.T) {
	served := map[keys.Action]string{}
	for _, m := range motionPairs {
		for _, a := range []keys.Action{m.plain, m.sel} {
			if prev, dup := served[a]; dup {
				t.Errorf("action %q served by both %s and a motion pair", a, prev)
				continue
			}
			served[a] = "a motion pair"
		}
	}
	for _, ac := range paneActions {
		if prev, dup := served[ac.action]; dup {
			t.Errorf("action %q served by both %s and paneActions", ac.action, prev)
			continue
		}
		served[ac.action] = "paneActions"
	}
}
