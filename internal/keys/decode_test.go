package keys

import (
	"bytes"
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

// Terminals without the Kitty protocol send ctrl chords as C0 bytes. A decoder
// that only reads CSI u cannot see them at all — which left raj unable to quit
// in iTerm2, since ctrl+c arrived as 0x03.
func TestLegacyControlBytes(t *testing.T) {
	cases := map[byte]string{
		0x03: "ctrl+c",
		0x1a: "ctrl+z",
		0x01: "ctrl+a",
		0x11: "ctrl+q",
		0x07: "ctrl+g",
		0x00: "ctrl+space",
		0x1c: `ctrl+\`,
		0x1f: "ctrl+_",
	}
	for b, want := range cases {
		e, n := Parse([]byte{b})
		if n != 1 {
			t.Errorf("%#02x: consumed %d bytes", b, n)
			continue
		}
		if got := e.Chord(); got != want {
			t.Errorf("%#02x: chord = %q, want %q", b, got, want)
		}
		if txt := e.Insertable(); txt != "" {
			t.Errorf("%#02x: inserted %q; a ctrl chord is not text", b, txt)
		}
	}
}

// Tab, enter, escape and backspace keep their own names rather than becoming
// ctrl+i, ctrl+m, ctrl+[ and ctrl+h, because that is what keymaps bind.
func TestLegacyNamedKeys(t *testing.T) {
	for b, want := range map[byte]string{0x09: "tab", 0x0d: "enter", 0x08: "backspace", 0x7f: "backspace"} {
		e, n := Parse([]byte{b})
		if n != 1 {
			t.Fatalf("%#02x: consumed %d bytes", b, n)
		}
		if got := e.Chord(); got != want {
			t.Errorf("%#02x: chord = %q, want %q", b, got, want)
		}
	}
}

// The legacy and modern encodings of the same chord must resolve identically,
// or a binding works in one terminal and not another.
func TestLegacyAndKKPAgree(t *testing.T) {
	k := NewKeymap()
	for _, c := range []struct {
		legacy byte
		kkp    string
	}{{0x03, "\x1b[99;5u"}, {0x1a, "\x1b[122;5u"}, {0x07, "\x1b[103;5u"}} {
		le, _ := Parse([]byte{c.legacy})
		me, _ := Parse([]byte(c.kkp))
		if le.Chord() != me.Chord() {
			t.Errorf("%#02x decodes as %q, %q decodes as %q", c.legacy, le.Chord(), c.kkp, me.Chord())
		}
		la, _, _ := k.Resolve(Editor, le)
		ma, _, _ := k.Resolve(Editor, me)
		if la != ma {
			t.Errorf("%s resolves to %q legacy and %q modern", le.Chord(), la, ma)
		}
	}
}

// A paste arrives as one event carrying the whole payload, not as keystrokes.
func TestBracketedPaste(t *testing.T) {
	e, n := Parse([]byte("\x1b[200~hello\x1b[201~"))
	if n != len("\x1b[200~hello\x1b[201~") {
		t.Fatalf("consumed %d bytes", n)
	}
	if e.Kind != PasteEvent {
		t.Fatalf("kind = %v, want PasteEvent", e.Kind)
	}
	if e.Text != "hello" {
		t.Errorf("text = %q, want %q", e.Text, "hello")
	}
}

// The payload is arbitrary bytes, not CSI parameters. Text that looks like a
// terminal reply must survive intact rather than being decoded as one, and a
// newline inside a paste must not become an enter keypress.
func TestPasteBodyIsOpaque(t *testing.T) {
	body := "a\nb\tc\x1b[5;3R\x1b[201x"
	e, n := Parse([]byte("\x1b[200~" + body + "\x1b[201~"))
	if n == 0 {
		t.Fatal("returned Partial on a complete paste")
	}
	if e.Kind != PasteEvent || e.Text != body {
		t.Errorf("text = %q, want %q", e.Text, body)
	}
}

// CR and CRLF fold to LF: a terminal reports Enter inside a paste as CR, and
// taking that literally makes a multi-line paste one line of control bytes.
func TestPasteNormalizesNewlines(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a\r\nb", "a\nb"},
		{"a\rb", "a\nb"},
		{"a\nb", "a\nb"},
		{"a\r\n\r\nb", "a\n\nb"},
	} {
		e, _ := Parse([]byte("\x1b[200~" + c.in + "\x1b[201~"))
		if e.Text != c.want {
			t.Errorf("%q -> %q, want %q", c.in, e.Text, c.want)
		}
	}
}

// An incomplete paste consumes nothing, so the reader keeps buffering rather
// than emitting half a payload.
func TestPartialPasteConsumesNothing(t *testing.T) {
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[2", "\x1b[200", "\x1b[200~", "\x1b[200~partial", "\x1b[200~x\x1b[201"} {
		e, n := Parse([]byte(in))
		if n != 0 || e.Kind != Partial {
			t.Errorf("%q: got kind %v n=%d, want Partial/0", in, e.Kind, n)
		}
	}
}

// Bytes following a paste decode normally, so the reader resumes at the right
// offset rather than re-reading the payload.
func TestPasteThenKey(t *testing.T) {
	buf := []byte("\x1b[200~hi\x1b[201~\x1b[99;5u")
	e, n := Parse(buf)
	if e.Kind != PasteEvent || e.Text != "hi" {
		t.Fatalf("first event = %v %q", e.Kind, e.Text)
	}
	next, _ := Parse(buf[n:])
	if got := next.Chord(); got != "ctrl+c" {
		t.Errorf("chord after paste = %q, want ctrl+c", got)
	}
}

// An unterminated paste must not buffer without limit: past the cap the start
// marker is surrendered so the reader drains instead of appearing hung.
func TestUnterminatedPasteDoesNotWedge(t *testing.T) {
	big := append([]byte("\x1b[200~"), bytes.Repeat([]byte("x"), MaxPaste+1)...)
	e, n := Parse(big)
	if n != len("\x1b[200~") || e.Kind == PasteEvent {
		t.Errorf("got kind %v n=%d, want the start marker surrendered", e.Kind, n)
	}
}

// A terminal without KKP sends escape as a single byte, which is also the first
// byte of every sequence. Parse must hold it — splitting a CSI would be worse —
// and ParseFinal must resolve it once the reader has waited.
func TestLoneEscapeNeedsAFinalRead(t *testing.T) {
	if _, n := Parse([]byte{0x1b}); n != 0 {
		t.Errorf("Parse consumed %d bytes of a lone ESC, want 0 (wait for more)", n)
	}
	e, n := ParseFinal([]byte{0x1b})
	if n != 1 || e.Kind != KeyEvent || e.Chord() != "esc" {
		t.Fatalf("ParseFinal = (%v, %d), want the esc key in 1 byte", e.Chord(), n)
	}
}

// ParseFinal must not steal anything else: a real sequence, and the legacy
// alt+key form, decode identically either way.
func TestParseFinalDefersToParse(t *testing.T) {
	for _, seq := range []string{"\x1b[27u", "\x1b[97;9u", "\x1ba", "a"} {
		want, wantN := Parse([]byte(seq))
		got, gotN := ParseFinal([]byte(seq))
		if gotN != wantN || got.Chord() != want.Chord() {
			t.Errorf("%q: ParseFinal = (%q, %d), Parse = (%q, %d)", seq, got.Chord(), gotN, want.Chord(), wantN)
		}
	}
}
