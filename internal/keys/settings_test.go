package keys

import "testing"

// The settings pane is palette-only: the user rejected taking a terminal chord
// for it, so it is declared in Unbound and run from the command palette. The
// action is still implemented, so it must not be sitting in Unimplemented.
func TestSettingsIsPaletteOnly(t *testing.T) {
	listed := false
	for _, a := range Unbound {
		if a == Settings {
			listed = true
		}
	}
	if !listed {
		t.Error("settings has no chord, so it must be declared in Unbound")
	}
	if why := Unimplemented[Settings]; why != "" {
		t.Errorf("settings is listed as unimplemented: %s", why)
	}
}

// The palette is built from the keymap tables, so a chordless action appears
// there exactly once with an empty Chord. Without the Unbound entry the
// settings command is absent from the palette entirely, and the pane has no
// route in at all.
func TestSettingsCommandAppearsInPalette(t *testing.T) {
	found := 0
	for _, c := range Commands() {
		if c.Action != Settings {
			continue
		}
		found++
		if c.Chord != "" {
			t.Errorf("settings command chord = %q, want a chordless entry", c.Chord)
		}
		if c.Name != "settings" {
			t.Errorf("settings command name = %q, want settings", c.Name)
		}
	}
	if found != 1 {
		t.Fatalf("settings appears in Commands() %d times, want exactly once", found)
	}
}

// No chord reaches the settings pane any more. The rejected super+comma, the
// alt+super candidate whose Linux trigger collided with prev_proposed, and the
// shift+super candidate that is Ghostty's reload_config must all resolve to
// nothing, in both the chord and the CSI-u forms, or the palette-only claim is
// false.
func TestNoChordReachesSettings(t *testing.T) {
	k := NewKeymap()
	for _, chord := range []string{"super+,", "alt+super+,", "shift+super+,"} {
		if got := k.Lookup(Editor, chord); got != None {
			t.Errorf("%s = %q, want no binding", chord, got)
		}
	}
	for _, seq := range []string{"\x1b[44;9u", "\x1b[44;11u", "\x1b[44;10u"} {
		if a, _, ok := k.Resolve(Editor, mustParse(t, seq)); ok || a != None {
			t.Errorf("%s resolved to (%q, %v), want nothing", seq, a, ok)
		}
	}
}
