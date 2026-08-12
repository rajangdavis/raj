package keys

import "testing"

func mouseOf(t *testing.T, seq string) Mouse {
	t.Helper()
	e, n := Parse([]byte(seq))
	if n != len(seq) {
		t.Fatalf("%q consumed %d of %d bytes", seq, n, len(seq))
	}
	if e.Kind != MouseEvent {
		t.Fatalf("%q decoded as kind %v, want a mouse event", seq, e.Kind)
	}
	return e.Mouse
}

// The wheel is the reason this exists: raj never enabled mouse reporting, so
// in the alt screen a wheel notch went to the terminal's own scrollback, which
// the alt screen is not part of, and nothing happened.
func TestWheelDecoding(t *testing.T) {
	cases := []struct {
		seq    string
		button MouseButton
	}{
		{"\x1b[<64;1;1M", WheelUp},
		{"\x1b[<65;1;1M", WheelDown},
		{"\x1b[<66;1;1M", WheelLeft},
		{"\x1b[<67;1;1M", WheelRight},
	}
	for _, c := range cases {
		m := mouseOf(t, c.seq)
		if m.Button != c.button {
			t.Errorf("%q: button %v, want %v", c.seq, m.Button, c.button)
		}
		if !m.IsWheel {
			t.Errorf("%q: not reported as a wheel event", c.seq)
		}
		if !m.Press {
			t.Errorf("%q: a wheel notch has no release; it must read as a press", c.seq)
		}
	}
}

// Coordinates are converted to zero-based at the boundary, because every other
// coordinate in raj is zero-based.
func TestMouseCoordinates(t *testing.T) {
	m := mouseOf(t, "\x1b[<0;12;34M")
	if m.Col != 11 || m.Row != 33 {
		t.Errorf("got (%d,%d), want (11,33)", m.Col, m.Row)
	}
	// Three digits and beyond: the whole reason for preferring SGR over the
	// original encoding, which cannot express a column past 223.
	m = mouseOf(t, "\x1b[<0;300;500M")
	if m.Col != 299 || m.Row != 499 {
		t.Errorf("got (%d,%d), want (299,499)", m.Col, m.Row)
	}
}

// Press and release are distinguished by the final byte alone.
func TestMousePressAndRelease(t *testing.T) {
	if m := mouseOf(t, "\x1b[<0;5;5M"); !m.Press {
		t.Error("M should be a press")
	}
	if m := mouseOf(t, "\x1b[<0;5;5m"); m.Press {
		t.Error("m should be a release")
	}
}

func TestMouseButtons(t *testing.T) {
	cases := map[string]MouseButton{
		"\x1b[<0;1;1M": MouseLeft,
		"\x1b[<1;1;1M": MouseMiddle,
		"\x1b[<2;1;1M": MouseRight,
		"\x1b[<3;1;1m": MouseNone,
	}
	for seq, want := range cases {
		if got := mouseOf(t, seq).Button; got != want {
			t.Errorf("%q: button %v, want %v", seq, got, want)
		}
	}
}

// Modifiers sit at different offsets from the KKP ones and lack super, so they
// are translated rather than passed through.
func TestMouseModifiers(t *testing.T) {
	cases := map[string]int{
		"\x1b[<0;1;1M":  0,
		"\x1b[<4;1;1M":  ModShift,
		"\x1b[<8;1;1M":  ModAlt,
		"\x1b[<16;1;1M": ModCtrl,
		"\x1b[<28;1;1M": ModShift | ModAlt | ModCtrl,
		"\x1b[<80;1;1M": ModCtrl, // ctrl + wheel up
	}
	for seq, want := range cases {
		if got := mouseOf(t, seq).Mods; got != want {
			t.Errorf("%q: mods %d, want %d", seq, got, want)
		}
	}
}

// The motion bit marks a drag, which is what a future click-and-select needs to
// tell apart from a fresh press.
func TestMouseMotionBit(t *testing.T) {
	if m := mouseOf(t, "\x1b[<32;5;5M"); !m.Motion || m.Button != MouseLeft {
		t.Errorf("drag with the left button held: motion=%v button=%v", m.Motion, m.Button)
	}
	if m := mouseOf(t, "\x1b[<0;5;5M"); m.Motion {
		t.Error("a plain press should not be marked as motion")
	}
}

// A malformed report must not become a key, and must not be read as a click at
// the origin: mouse reports arrive in the same stream as text, and misreading
// one would move the cursor on garbage.
func TestMalformedReportsAreNotKeys(t *testing.T) {
	// Only sequences whose bytes are all legal CSI parameters belong here. A
	// letter or a sign is not one, so those terminate the sequence early and
	// are a framing question rather than a mouse one — the CSI tests cover it.
	for _, seq := range []string{
		"\x1b[<M",               // no parameters
		"\x1b[<0;1M",            // too few
		"\x1b[<0;1;1;1M",        // too many
		"\x1b[<0;;1M",           // empty parameter
		"\x1b[<9999999999;1;1M", // absurd
	} {
		e, n := Parse([]byte(seq))
		if n != len(seq) {
			t.Errorf("%q consumed %d of %d", seq, n, len(seq))
		}
		if e.Kind == MouseEvent {
			t.Errorf("%q decoded as a mouse event: %+v", seq, e.Mouse)
		}
		if e.Kind == KeyEvent {
			t.Errorf("%q decoded as a key, which would type into the buffer", seq)
		}
	}
}

// Buttons 8 and up are not bound to anything, so they are not decoded rather
// than decoded into a button raj would then have to ignore everywhere.
func TestHighButtonsAreIgnored(t *testing.T) {
	e, _ := Parse([]byte("\x1b[<128;1;1M"))
	if e.Kind == MouseEvent {
		t.Errorf("button 8 decoded as %+v", e.Mouse)
	}
}

// A bare CSI M without the private marker is the legacy X10 encoding, which
// raj does not enable and does not decode. It must not be mistaken for SGR.
func TestLegacyEncodingIsNotDecoded(t *testing.T) {
	e, _ := Parse([]byte("\x1b[M   "))
	if e.Kind == MouseEvent {
		t.Error("the X10 encoding was decoded; raj never asks for it")
	}
}

// A report split across reads must be requested again rather than half-parsed.
func TestPartialMouseReport(t *testing.T) {
	full := "\x1b[<64;10;20M"
	for i := 1; i < len(full); i++ {
		if e, n := Parse([]byte(full[:i])); n != 0 || e.Kind != Partial {
			t.Errorf("%q (%d bytes) parsed as %v/%d, want Partial/0", full[:i], i, e.Kind, n)
		}
	}
}

// PastePending is the distinction between "still coming" and "stalled". Every
// other partial sequence is ambiguous under silence — a lone ESC is the escape
// key — but a paste start marker is not, so waiting is always right.
func TestPastePending(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"start only", "\x1b[200~", true},
		{"start with a partial payload", "\x1b[200~some text", true},
		{"a complete paste", "\x1b[200~text\x1b[201~", false},
		{"a partial end marker", "\x1b[200~text\x1b[201", true},
		{"not a paste", "\x1b[1;5A", false},
		{"a lone escape", "\x1b", false},
		{"empty", "", false},
		{"payload containing the start marker", "\x1b[200~a\x1b[200~b", true},
	}
	for _, c := range cases {
		if got := PastePending([]byte(c.in)); got != c.want {
			t.Errorf("%s: PastePending(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// Past the ceiling, waiting stops: a terminal that sends a start marker and
// then dies must not wedge the reader forever.
func TestPastePendingRespectsTheCeiling(t *testing.T) {
	buf := append([]byte("\x1b[200~"), make([]byte, MaxPaste)...)
	if PastePending(buf) {
		t.Error("a paste past MaxPaste is still reported as pending")
	}
}
