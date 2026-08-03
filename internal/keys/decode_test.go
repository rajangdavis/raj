package keys

import (
	"strings"
	"testing"
)

func TestParseChords(t *testing.T) {
	cases := []struct {
		name, in, chord string
		typ             int
	}{
		{"super+w", "\x1b[119;9u", "super+w", Press},
		{"shift+super+f", "\x1b[102;10u", "shift+super+f", Press},
		{"alt+super+left", "\x1b[1;11D", "alt+super+left", Press},
		{"super+up", "\x1b[1;9A", "super+up", Press},
		{"alt+left", "\x1b[1;3D", "alt+left", Press},
		{"ctrl+z", "\x1b[122;5u", "ctrl+z", Press},
		{"super+enter", "\x1b[13;9u", "super+enter", Press},
		{"super+1", "\x1b[49;9u", "super+1", Press},
		{"bare key", "\x1b[97u", "a", Press},
		{"release", "\x1b[97;1:3u", "a", Release},
		{"repeat", "\x1b[97;1:2u", "a", Repeat},
		{"alternates", "\x1b[97:65;2u", "shift+a", Press},
		{"capslock masked", "\x1b[119;73u", "super+w", Press},
		{"delete", "\x1b[3;9~", "super+delete", Press},
		{"plain rune", "a", "a", Press},
		{"legacy alt", "\x1bb", "alt+b", Press},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, n := Parse([]byte(c.in))
			if n != len(c.in) {
				t.Fatalf("consumed %d of %d bytes", n, len(c.in))
			}
			if e.Kind != KeyEvent {
				t.Fatalf("kind = %v, want KeyEvent", e.Kind)
			}
			if got := e.Chord(); got != c.chord {
				t.Errorf("chord = %q, want %q", got, c.chord)
			}
			if e.Type != c.typ {
				t.Errorf("type = %d, want %d", e.Type, c.typ)
			}
		})
	}
}

func TestParseNonKeyEvents(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
	}{
		{"\x1b[I", FocusIn},
		{"\x1b[O", FocusOut},
		{"\x1b[?15u", KKPFlags},
		{"\x1b]11;rgb:1e1e/1e1e/1e1e\x07", OSCReply},
		{"\x1b[?62;c", OtherReply},
	}
	for _, c := range cases {
		e, n := Parse([]byte(c.in))
		if n != len(c.in) {
			t.Fatalf("%q: consumed %d of %d", c.in, n, len(c.in))
		}
		if e.Kind != c.kind {
			t.Errorf("%q: kind = %v, want %v", c.in, e.Kind, c.kind)
		}
	}
}

// Partial sequences must be buffered, never mis-decoded.
func TestParsePartial(t *testing.T) {
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[119", "\x1b[119;9", "\x1b]11;rgb:"} {
		if e, n := Parse([]byte(in)); n != 0 || e.Kind != Partial {
			t.Errorf("%q: got n=%d kind=%v, want incomplete", in, n, e.Kind)
		}
	}
}

// Every binding's declared Seq must decode back to its declared Chord. This is
// what keeps the Ghostty config and raj's keymap from drifting apart.
func TestBindingsRoundTrip(t *testing.T) {
	seen := map[string]Action{}
	for _, b := range Bindings {
		e, n := Parse([]byte("\x1b[" + b.Seq))
		if n == 0 {
			t.Errorf("%s: sequence %q does not parse", b.Action, b.Seq)
			continue
		}
		if got := e.Chord(); got != b.Chord {
			t.Errorf("%s: seq %q decodes to %q, table says %q", b.Action, b.Seq, got, b.Chord)
		}
		if prev, dup := seen[b.Chord]; dup {
			t.Errorf("%s: chord %q already claimed by %s", b.Action, b.Chord, prev)
		}
		seen[b.Chord] = b.Action
	}
}

// Triggers must be unique per platform, or the last keybind line silently wins.
func TestTriggersUnique(t *testing.T) {
	for _, plat := range []string{"macos", "linux"} {
		seen := map[string]Action{}
		for _, b := range Bindings {
			trig := b.Mac
			if plat == "linux" {
				trig = b.Linux
			}
			if prev, dup := seen[trig]; dup {
				t.Errorf("%s: trigger %q used by both %s and %s", plat, trig, prev, b.Action)
			}
			seen[trig] = b.Action
		}
	}
}

// Ghostty has no trailing-comment syntax: everything after '=' is the action
// value. A '#' on a keybind line is sent to the app as literal text.
func TestConfigHasNoTrailingComments(t *testing.T) {
	for _, plat := range []string{"macos", "linux"} {
		for _, line := range strings.Split(GhosttyConfig(plat), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.HasPrefix(line, "keybind = kkp_on:") {
				t.Errorf("%s: unexpected line %q", plat, line)
			}
			if strings.ContainsAny(line, "# ") != strings.Contains(line, " = ") {
				t.Errorf("%s: stray content in %q", plat, line)
			}
			if i := strings.Index(line, "csi:"); i < 0 {
				t.Errorf("%s: no csi payload in %q", plat, line)
			} else if strings.ContainsAny(line[i:], "# \t") {
				t.Errorf("%s: payload has trailing junk: %q", plat, line)
			}
		}
	}
}
