package keys

import (
	"strings"
	"testing"
)

// macOS claims some chords system-wide and swallows them before any terminal
// sees them. A binding on one of these is not merely inconvenient — it never
// arrives at all, and there is nothing raj can do about it at runtime, so the
// only place to catch it is here.
//
// cmd+alt+d was bound to go-to-definition and never fired once: macOS uses it
// to hide the Dock.
func TestNoMacOSSystemShortcuts(t *testing.T) {
	// Reserved system-wide by macOS, from the Keyboard Shortcuts pane and the
	// window/application defaults every app inherits.
	reserved := map[string]string{
		"alt+super+d":     "hide/show the Dock",
		"super+space":     "Spotlight",
		"alt+super+space": "Finder search",
		"super+tab":       "application switcher",
		"shift+super+3":   "screenshot",
		"shift+super+4":   "screenshot selection",
		"shift+super+5":   "screenshot tools",
		"super+h":         "hide the application",
		"alt+super+h":     "hide others",
		"super+m":         "minimise the window",
		"alt+super+esc":   "force quit",
		"ctrl+super+f":    "full screen",
		"shift+super+q":   "log out",
	}
	for _, b := range Bindings {
		if why, bad := chordReason(reserved, b.Chord); bad {
			t.Errorf("%s is bound to %q, which macOS uses for %s — it will never arrive",
				b.Action, b.Chord, why)
		}
	}
}

// Chords the terminal itself uses. Unlike the macOS set these do arrive, so raj
// can bind them — but taking one costs the user a terminal feature they may
// well be using, and doing that silently is not a trade raj gets to make on
// their behalf.
//
// A binding here is not necessarily wrong: cmd+d is Ghostty's split-right and
// raj takes it deliberately, with the cost written in the table's own notes
// column. What this enforces is that the cost was noticed — a chord in this set
// needs a note saying so.
//
// cmd+shift+d was briefly bound to go-to-definition and is exactly the mistake
// this catches: it arrives fine, so nothing in raj would ever have complained,
// but it splits a pane in both iTerm2 and Ghostty.
func TestTerminalDefaultsAreAcknowledged(t *testing.T) {
	terminal := map[string]string{
		"super+d":           "split right (Ghostty)",
		"shift+super+d":     "split down (iTerm2, Ghostty)",
		"super+t":           "new tab",
		"super+n":           "new window",
		"super+w":           "close tab",
		"super+k":           "clear the buffer (iTerm2)",
		"super+enter":       "full screen",
		"shift+super+enter": "full screen",
		"super+f":           "find in terminal",
		"super+l":           "clear screen (Linux default)",
	}
	for _, b := range Bindings {
		why, claimed := chordReason(terminal, b.Chord)
		if !claimed {
			continue
		}
		if b.Note == "" {
			t.Errorf("%s takes %q, which the terminal uses for %s, without a note saying so",
				b.Action, b.Chord, why)
		}
	}
}

// chordReason reports a table's reason for a chord, comparing the normalized
// spelling so a table entry and a bound chord match whichever form each uses.
// Event.Chord renders the comma as ","; the tables here spell it "comma", so a
// raw map lookup was blind to every comma binding — the settings chord slipped
// both guards for exactly that reason.
func chordReason(set map[string]string, chord string) (string, bool) {
	want := normalizeChord(chord)
	for c, why := range set {
		if normalizeChord(c) == want {
			return why, true
		}
	}
	return "", false
}

// normalizeChord folds the two spellings of a key name into the one Event.Chord
// emits: a comma is "," in a chord and "comma" in prose, and the guards have to
// treat them as the same key.
func normalizeChord(chord string) string {
	return strings.ReplaceAll(chord, "comma", ",")
}

// A comma binding must be caught however the two sides spell the key. This one
// is synthetic: the table names the key "comma", the binding names it "," — the
// guard has to resolve them to the same chord.
func TestReservedCommaChordIsCaught(t *testing.T) {
	reserved := map[string]string{"super+comma": "application preferences"}
	if why, bad := chordReason(reserved, "super+,"); !bad {
		t.Error("super+, is not caught by a reserved super+comma entry; the guard is blind to comma chords")
	} else if why != "application preferences" {
		t.Errorf("reason = %q, want the reserved entry's", why)
	}
	terminal := map[string]string{"shift+super+comma": "reload config (Ghostty)"}
	if _, bad := chordReason(terminal, "shift+super+,"); !bad {
		t.Error("shift+super+, is not caught by a terminal shift+super+comma entry")
	}
}
