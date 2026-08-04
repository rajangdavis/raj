package keys

import (
	"strings"
	"testing"
)

// mustParse decodes a full sequence or fails the test.
func mustParse(t *testing.T, seq string) Event {
	t.Helper()
	e, n := Parse([]byte(seq))
	if n != len(seq) {
		t.Fatalf("%q: consumed %d of %d bytes", seq, n, len(seq))
	}
	return e
}

func TestResolveScoped(t *testing.T) {
	k := NewKeymap()
	cases := []struct {
		name  string
		scope Scope
		seq   string
		want  Action
	}{
		{"global chord in editor", Editor, "\x1b[119;9u", CloseTab},
		{"global chord in agent", Agent, "\x1b[119;9u", CloseTab},
		{"tab indents in editor", Editor, "\x1b[9u", Indent},
		{"tab cycles elsewhere", Explorer, "\x1b[9u", CycleFocus},
		{"shift+tab outdents", Editor, "\x1b[9;2u", Outdent},
		{"shift+tab cycles back", Search, "\x1b[9;2u", CycleFocusBack},
		{"esc cancels", Search, "\x1b[27u", Cancel},
		{"arrow is a nav action", Editor, "\x1b[1;1D", CharLeft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, ok := k.Resolve(c.scope, mustParse(t, c.seq))
			if !ok || a != c.want {
				t.Errorf("got (%q, %v), want %q", a, ok, c.want)
			}
		})
	}
}

// Releases must never resolve: KKP flag 2 reports press AND release for every
// chord, so acting on both applies every edit twice.
func TestResolveDropsReleasesAndModifiers(t *testing.T) {
	k := NewKeymap()
	for _, seq := range []string{
		"\x1b[119;9:3u", // super+w release
		"\x1b[97;1:3u",  // 'a' release
		"\x1b[57444;9u", // bare left-super press
		"\x1b[57441;2u", // bare left-shift press
		"\x1b[?15u",     // KKP flags reply, not a key
		"\x1b[I",        // focus in
	} {
		if _, text, ok := k.Resolve(Editor, mustParse(t, seq)); ok || text != "" {
			t.Errorf("%q resolved but should have been dropped", seq)
		}
	}
}

func TestInsertable(t *testing.T) {
	cases := []struct{ seq, want string }{
		{"\x1b[97u", "a"},      // plain letter
		{"\x1b[97:65;2u", "A"}, // shift+a -> alternates carry the shifted form
		{"\x1b[32u", " "},      // space
		{"\x1b[13u", "\n"},     // enter inserts a newline
		{"\x1b[97;9u", ""},     // super+a is a command, not input
		{"\x1b[97;3u", ""},     // alt+a likewise
		{"\x1b[1;1D", ""},      // arrows are never text
		{"\x1b[127u", ""},      // backspace is never text
		{"\x1b[57444;9u", ""},  // bare modifier
	}
	for _, c := range cases {
		if got := mustParse(t, c.seq).Insertable(); got != c.want {
			t.Errorf("%q: Insertable = %q, want %q", c.seq, got, c.want)
		}
	}
}

// A printable key with no binding resolves as text, not as an action.
func TestResolveFallsThroughToText(t *testing.T) {
	k := NewKeymap()
	a, text, ok := k.Resolve(Editor, mustParse(t, "\x1b[113u"))
	if !ok || a != None || text != "q" {
		t.Errorf("got (%q, %q, %v), want (None, \"q\", true)", a, text, ok)
	}
}

// Binding to None masks a global without deleting it for other scopes.
func TestBindNoneMasksGlobal(t *testing.T) {
	k := NewKeymap()
	if got := k.Lookup(Editor, "enter"); got != None {
		t.Errorf("editor enter = %q, want None (editor inserts a newline)", got)
	}
	if got := k.Lookup(Search, "enter"); got != Confirm {
		t.Errorf("search enter = %q, want Confirm", got)
	}
}

// Shift+alternates: the shifted codepoint must not be mistaken for the base key
// when naming the chord, or shift+a would bind as "shift+A".
func TestShiftedAlternateNaming(t *testing.T) {
	if got := mustParse(t, "\x1b[97:65;2u").Chord(); got != "shift+a" {
		t.Errorf("chord = %q, want shift+a", got)
	}
}

// The iTerm2 profile must cover the same chords as the Ghostty config and
// encode the same sequences: one measured table, two emitters.
func TestITerm2ProfileMatchesTable(t *testing.T) {
	out := ITerm2Profile("raj")
	for _, b := range Bindings {
		key, ok := itermMappingKey(b.Chord)
		if !ok {
			t.Errorf("%s: chord %q has no iTerm2 mapping", b.Action, b.Chord)
			continue
		}
		if !strings.Contains(out, key) {
			t.Errorf("%s: mapping key %s missing from the profile", b.Action, key)
		}
		if !strings.Contains(out, `"[`+b.Seq+`"`) {
			t.Errorf("%s: sequence %s missing from the profile", b.Action, b.Seq)
		}
	}
}

func TestITerm2MappingKeys(t *testing.T) {
	cases := map[string]string{
		"super+w":        "0x77-0x100000",
		"shift+super+e":  "0x45-0x120000",
		"alt+super+left": "0xf702-0x180000",
		"ctrl+z":         "0x7a-0x40000",
		"super+1":        "0x31-0x100000",
		"shift+super+t":  "0x54-0x120000",
		"shift+alt+i":    "0x49-0xa0000",
	}
	for chord, want := range cases {
		got, ok := itermMappingKey(chord)
		if !ok || got != want {
			t.Errorf("%s -> %q (ok=%v), want %q", chord, got, ok, want)
		}
	}
	// An unmodified key needs no mapping: iTerm2 already sends it.
	if _, ok := itermMappingKey("tab"); ok {
		t.Error("unmodified keys should not be mapped")
	}
}

// Every shift+letter chord must map to the uppercase character code: AppKit
// applies shift when reporting charactersIgnoringModifiers, so a lowercase
// mapping never matches.
func TestITerm2ShiftedLettersAreUppercase(t *testing.T) {
	for _, b := range Bindings {
		if !strings.HasPrefix(b.Chord, "shift+") {
			continue
		}
		parts := strings.Split(b.Chord, "+")
		key := parts[len(parts)-1]
		if len(key) != 1 || key[0] < 'a' || key[0] > 'z' {
			continue
		}
		got, ok := itermMappingKey(b.Chord)
		if !ok {
			t.Errorf("%s: no mapping", b.Chord)
			continue
		}
		want := strings.ToUpper(key)[0]
		if !strings.HasPrefix(got, "0x"+strings.ToLower(string("0123456789abcdef"[want>>4]))+
			string("0123456789abcdef"[want&0xf])+"-") {
			t.Errorf("%s -> %s, want the uppercase code 0x%x", b.Chord, got, want)
		}
	}
}

// The wrap toggle must survive macOS. Option+letter composes into a character
// there, so a bare alt+letter chord never reaches the application — which is
// why every alt binding in the table is alt+super or alt+arrow.
func TestWrapToggleChordIsNotBareAlt(t *testing.T) {
	k := NewKeymap()
	if got := k.Lookup(Global, "alt+z"); got == ToggleWrap {
		t.Error("toggle_wrap is on alt+z, which macOS composes away")
	}
	if got := k.Lookup(Global, "ctrl+alt+w"); got != ToggleWrap {
		t.Errorf("ctrl+alt+w resolves to %q, want toggle_wrap", got)
	}
	// It must also decode from the wire, not just from a chord string.
	e := mustParse(t, "\x1b[119;7u")
	if got := e.Chord(); got != "ctrl+alt+w" {
		t.Errorf("119;7u decodes as %q, want ctrl+alt+w", got)
	}
	if a, _, _ := k.Resolve(Editor, e); a != ToggleWrap {
		t.Errorf("editor scope resolves it to %q, want toggle_wrap", a)
	}
}
