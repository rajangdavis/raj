package keys

import (
	"strings"
	"testing"
)

// The palette's list is the keymap's list: every action bound in Bindings,
// Reclaim or Natives is runnable from it, so a new chord is discoverable the
// moment it is bound. This is the property that makes "cannot drift" true
// rather than aspirational.
func TestCommandsCoverTheKeymap(t *testing.T) {
	listed := map[Action]bool{}
	for _, c := range Commands() {
		listed[c.Action] = true
	}
	for _, b := range Bindings {
		if !listed[b.Action] {
			t.Errorf("%s is bound but missing from the palette", b.Action)
		}
	}
	// Reclaim entries are bound in the keymap too, so they are exactly the
	// actions this test is for: bound, implemented, and easy to lose from the
	// palette because the table they live in is not the first one read.
	for _, b := range Reclaim {
		if !listed[b.Action] {
			t.Errorf("%s is bound but missing from the palette", b.Action)
		}
	}
	for _, n := range Natives {
		if !listed[n.Action] {
			t.Errorf("%s is bound but missing from the palette", n.Action)
		}
	}
}

// A command can be palette-only: runnable without owning a chord, because a
// chord is a resource the terminal also wants. Commands must list it exactly
// once and with an empty Chord, and it has to be declared in Unbound —
// otherwise it is a bound action that lost its chord.
//
// Without the mechanism there is no copy_relative_path entry to find, so the
// count assertion fails; with Unbound declared but Commands not reading it, the
// same assertion fails; with Unbound read twice, the count is 2.
func TestCommandsListCopyRelPathChordlessOnce(t *testing.T) {
	unbound := map[Action]bool{}
	for _, a := range Unbound {
		unbound[a] = true
	}
	found := 0
	for _, c := range Commands() {
		if c.Action != CopyRelPath {
			continue
		}
		found++
		if c.Chord != "" {
			t.Errorf("copy_relative_path is chordless but listed with chord %q", c.Chord)
		}
		if want := strings.ReplaceAll(string(c.Action), "_", " "); c.Name != want {
			t.Errorf("copy_relative_path is named %q, want %q", c.Name, want)
		}
	}
	if found != 1 {
		t.Fatalf("copy_relative_path appears in Commands() %d times, want exactly once", found)
	}
	if !unbound[CopyRelPath] {
		t.Error("copy_relative_path has no chord, so it must be declared in Unbound")
	}
}

// Unbound is the one hand-maintained list Commands reads, so it needs the same
// honesty test ActionsByName gives the bound set. Every entry must be listed
// once, chordless, and must not also be bound: a bound action's chord would be
// hidden behind the chordless entry.
//
// Without Commands reading Unbound an entry appears zero times and the count
// fails; the overlap check fails if a bound action is copied into Unbound, a
// case the listing-once guard would otherwise paper over.
func TestUnboundActionsAreListedChordless(t *testing.T) {
	bound := map[Action]bool{}
	for _, b := range Bindings {
		bound[b.Action] = true
	}
	for _, b := range Reclaim {
		bound[b.Action] = true
	}
	for _, n := range Natives {
		bound[n.Action] = true
	}
	for _, a := range Unbound {
		if bound[a] {
			t.Errorf("%s is in Unbound and also bound; the binding's chord would be hidden", a)
		}
		count := 0
		for _, c := range Commands() {
			if c.Action != a {
				continue
			}
			count++
			if c.Chord != "" {
				t.Errorf("%s is in Unbound but listed with chord %q", a, c.Chord)
			}
		}
		if count != 1 {
			t.Errorf("%s is in Unbound but appears in Commands() %d times, want once", a, count)
		}
	}
}

// A command's label is derived from its action rather than a parallel table,
// and every bound command carries the chord a user would otherwise have to find
// in the reference. A command listed with no chord is allowed only when Unbound
// declares it: an empty chord nothing accounts for is a command that lost its
// binding rather than one that never had one.
func TestCommandLabelsAndChordsComeFromTheTables(t *testing.T) {
	unbound := map[Action]bool{}
	for _, a := range Unbound {
		unbound[a] = true
	}
	for _, c := range Commands() {
		if want := strings.ReplaceAll(string(c.Action), "_", " "); c.Name != want {
			t.Errorf("%s is named %q, want %q", c.Action, c.Name, want)
		}
		if c.Chord == "" && !unbound[c.Action] {
			t.Errorf("%s is listed with no chord but Unbound does not declare it", c.Action)
		}
	}
}

// The palette must not offer an action the keymap cannot run. An unimplemented
// action is bound so its chord is not lost, but running it does nothing, and
// the palette is a list of things to run.
func TestCommandsAreImplemented(t *testing.T) {
	for _, c := range Commands() {
		if why := Unimplemented[c.Action]; why != "" {
			t.Errorf("%s is runnable from the palette but listed unimplemented: %s", c.Action, why)
		}
	}
}

// cmd+shift+p is implemented now, so it must no longer be listed as a chord
// that does nothing. TestUnimplementedActionsReallyAreUnimplemented in
// internal/app catches the reverse: unlisting one that still does nothing.
func TestCommandPaletteIsImplemented(t *testing.T) {
	if why := Unimplemented[CommandPalette]; why != "" {
		t.Errorf("command_palette is implemented but still listed unimplemented: %s", why)
	}
}
