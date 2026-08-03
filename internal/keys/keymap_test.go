package keys

import "testing"

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
