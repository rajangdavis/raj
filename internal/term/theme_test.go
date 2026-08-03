package term

import "testing"

func TestParseColorReply(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		kind  int
		index int
		want  Color
	}{
		// Measured from a patched Ghostty via cmd/keyprobe.
		{"background black", "\x1b]11;rgb:0000/0000/0000\x07", 11, 0, Color{0, 0, 0}},
		{"foreground green", "\x1b]10;rgb:5c5c/f0f0/7070\x07", 10, 0, Color{0x5c, 0xf0, 0x70}},
		{"palette 4", "\x1b]4;4;rgb:2f2f/b5b5/6f6f\x07", 4, 4, Color{0x2f, 0xb5, 0x6f}},
		{"ST terminator", "\x1b]11;rgb:ffff/ffff/ffff\x1b\\", 11, 0, Color{255, 255, 255}},
		// Component width varies by terminal; each is scaled by its own width.
		{"8-bit components", "\x1b]11;rgb:1e/1e/1e\x07", 11, 0, Color{0x1e, 0x1e, 0x1e}},
		{"4-bit components", "\x1b]11;rgb:f/0/0\x07", 11, 0, Color{255, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, index, got, ok := ParseColorReply([]byte(c.raw))
			if !ok {
				t.Fatalf("failed to parse %q", c.raw)
			}
			if kind != c.kind || index != c.index || got != c.want {
				t.Errorf("got kind=%d index=%d %+v, want kind=%d index=%d %+v",
					kind, index, got, c.kind, c.index, c.want)
			}
		})
	}
}

func TestParseColorReplyRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"\x1b[11;rgb:0/0/0\x07",     // CSI, not OSC
		"\x1b]11;rgb:0000/0000\x07", // two components
		"\x1b]11;rgb:zzzz/0000/0000\x07",
		"\x1b]11\x07",          // no spec
		"\x1b]x;rgb:0/0/0\x07", // non-numeric kind
	} {
		if _, _, _, ok := ParseColorReply([]byte(raw)); ok {
			t.Errorf("%q parsed but should have been rejected", raw)
		}
	}
}

func TestThemeDark(t *testing.T) {
	var th Theme
	th.Set(ParseColorReply([]byte("\x1b]11;rgb:0000/0000/0000\x07")))
	if !th.Dark() {
		t.Error("black background should be dark")
	}
	th.Set(ParseColorReply([]byte("\x1b]11;rgb:ffff/ffff/ffff\x07")))
	if th.Dark() {
		t.Error("white background should be light")
	}
}

// Enter must refuse a flag set without report_all: Ghostty's kkp_on gate keys
// on flag 8, so without it every cmd chord goes back to the terminal and raj
// silently receives nothing.
func TestEnterRequiresReportAll(t *testing.T) {
	tm := &Terminal{}
	if err := tm.Enter(FlagDisambiguate | FlagEventTypes); err == nil {
		t.Fatal("expected an error for flags without FlagReportAll")
	}
	if DefaultFlags&FlagReportAll == 0 {
		t.Error("DefaultFlags must include FlagReportAll")
	}
}
