package ui

import (
	"strings"
	"testing"
)

func TestSetStringAndRow(t *testing.T) {
	s := NewScreen(20, 3)
	s.SetString(2, 1, "hello", DefaultStyle, 20)
	if got := s.Row(1); got != "  hello" {
		t.Errorf("row = %q", got)
	}
	if got := s.Row(0); got != "" {
		t.Errorf("blank row = %q, want empty", got)
	}
}

// SetString must clip on display columns, not byte or rune count, or a pane
// with wide characters overruns its neighbour.
func TestSetStringClipsOnDisplayWidth(t *testing.T) {
	s := NewScreen(20, 1)
	used := s.SetString(0, 0, "日本語abc", DefaultStyle, 5)
	if used != 4 {
		t.Errorf("used %d columns, want 4 (two wide runes fit, the third does not)", used)
	}
	if got := s.Row(0); got != "日本" {
		t.Errorf("row = %q, want 日本", got)
	}
}

// A wide rune claims a continuation cell so column arithmetic stays honest.
func TestWideRuneOccupiesTwoCells(t *testing.T) {
	s := NewScreen(6, 1)
	s.Set(0, 0, '日', DefaultStyle)
	if c := s.At(0, 0); c.Width != 2 {
		t.Errorf("width = %d, want 2", c.Width)
	}
	if c := s.At(1, 0); c.Width != 0 {
		t.Errorf("continuation width = %d, want 0", c.Width)
	}
}

// A first frame repaints everything; an unchanged frame emits nothing.
func TestDiffFullAndEmpty(t *testing.T) {
	s := NewScreen(10, 2)
	s.SetString(0, 0, "hi", DefaultStyle, 10)
	if out := s.Diff(nil); !strings.Contains(out, "hi") {
		t.Errorf("full repaint missing content: %q", out)
	}
	if out := s.Diff(s.Clone()); out != "" {
		t.Errorf("identical frames emitted %q, want nothing", out)
	}
}

// The point of diffing: one changed cell must not repaint the screen.
func TestDiffIsMinimal(t *testing.T) {
	prev := NewScreen(80, 24)
	prev.SetString(0, 0, strings.Repeat("x", 80), DefaultStyle, 80)
	next := prev.Clone()
	next.Set(40, 0, 'Y', DefaultStyle)

	out := next.Diff(prev)
	if !strings.Contains(out, "Y") {
		t.Fatalf("change missing from diff: %q", out)
	}
	if len(out) > 40 {
		t.Errorf("one-cell change emitted %d bytes: %q", len(out), out)
	}
}

// A resize forces a full repaint: cells do not correspond across sizes.
func TestDiffResizeForcesFullRepaint(t *testing.T) {
	prev := NewScreen(10, 2)
	next := NewScreen(20, 2)
	next.SetString(0, 0, "wide", DefaultStyle, 20)
	if out := next.Diff(prev); !strings.Contains(out, "wide") {
		t.Errorf("resize did not repaint: %q", out)
	}
}

func TestStyleSGR(t *testing.T) {
	cases := []struct {
		style Style
		want  string
	}{
		{DefaultStyle, "\x1b[0;39;49m"},
		{DefaultStyle.With(Ansi(1)), "\x1b[0;31;49m"},
		{DefaultStyle.With(Ansi(9)), "\x1b[0;91;49m"},
		{DefaultStyle.On(Ansi(240)), "\x1b[0;39;48;5;240m"},
		{DefaultStyle.With(RGBColor(1, 2, 3)), "\x1b[0;38;2;1;2;3;49m"},
		{DefaultStyle.Plus(Bold | Underline), "\x1b[0;1;4;39;49m"},
	}
	for _, c := range cases {
		if got := c.style.sgr(); got != c.want {
			t.Errorf("%+v sgr = %q, want %q", c.style, got, c.want)
		}
	}
}

// Panes leaving colours as Default inherit the user's Ghostty theme, so the
// default style must encode as 39/49 rather than naming a colour.
func TestDefaultColorsInheritTerminal(t *testing.T) {
	if got := DefaultStyle.sgr(); !strings.Contains(got, "39") || !strings.Contains(got, "49") {
		t.Errorf("default style %q should use SGR 39/49", got)
	}
}

func TestThemeDarkFallsBackToDark(t *testing.T) {
	if !(Theme{}).Dark() {
		t.Error("unknown theme should assume dark")
	}
	if !(Theme{Background: RGBColor(0, 0, 0), Known: true}).Dark() {
		t.Error("black background should be dark")
	}
	if (Theme{Background: RGBColor(255, 255, 255), Known: true}).Dark() {
		t.Error("white background should be light")
	}
}

// The headless host is the reason the seam exists: scripted input, captured
// frames, no terminal.
func TestFakeHostRecordsFrames(t *testing.T) {
	h := NewFakeHost(20, 3)
	defer h.Close()

	s := NewScreen(20, 3)
	s.SetString(0, 0, "frame one", DefaultStyle, 20)
	h.Present(s)
	s.SetString(0, 0, "frame two", DefaultStyle, 20)
	h.Present(s)

	if n := len(h.Frames()); n != 2 {
		t.Fatalf("recorded %d frames, want 2", n)
	}
	if got := h.Text(); !strings.HasPrefix(got, "frame two") {
		t.Errorf("last frame = %q", got)
	}
}

func TestFakeHostSynthesisesChords(t *testing.T) {
	h := NewFakeHost(10, 2)
	h.Press("super+w")
	h.Press("shift+tab")
	h.Type("ab")
	h.Close()

	var chords []string
	for e := range h.Events() {
		if k, ok := e.(Key); ok {
			chords = append(chords, k.Chord())
		}
	}
	want := []string{"super+w", "shift+tab", "a", "b"}
	if len(chords) != len(want) {
		t.Fatalf("got %v, want %v", chords, want)
	}
	for i := range want {
		if chords[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, chords[i], want[i])
		}
	}
}

// Writing the bottom-right cell scrolls most terminals, which shifts every row
// and leaves the diff describing a screen that no longer exists. It must never
// be emitted.
func TestDiffSkipsBottomRightCell(t *testing.T) {
	s := NewScreen(10, 3)
	s.SetString(0, 2, strings.Repeat("Z", 10), DefaultStyle, 10)
	out := s.Diff(nil)
	if got := strings.Count(out, "Z"); got != 9 {
		t.Errorf("emitted %d of 10 cells on the last row; the last one must be skipped", got)
	}
	prev := s.Clone()
	s.Set(9, 2, 'Q', DefaultStyle)
	if out := s.Diff(prev); strings.Contains(out, "Q") {
		t.Error("a change to the bottom-right cell was emitted")
	}
}

// A clip is a hard boundary: a pane cannot paint over its neighbour even if it
// computes its own bounds wrongly.
func TestClipBlocksWritesOutside(t *testing.T) {
	s := NewScreen(20, 3)
	restore := s.Clip(5, 1, 5, 1)
	s.SetString(0, 1, strings.Repeat("x", 20), DefaultStyle, 20)
	s.Set(0, 0, 'y', DefaultStyle)
	restore()

	if got := s.Row(1); got != "     xxxxx" {
		t.Errorf("row 1 = %q, want writes confined to columns 5-9", got)
	}
	if got := s.Row(0); got != "" {
		t.Errorf("row 0 = %q, want nothing outside the clip", got)
	}
	s.Set(0, 0, 'y', DefaultStyle)
	if got := s.Row(0); got != "y" {
		t.Errorf("after restore row 0 = %q, want y", got)
	}
}

// A wide rune straddling the clip edge would paint over the neighbour, so it
// degrades to a placeholder rather than half a glyph.
func TestClipRejectsStraddlingWideRune(t *testing.T) {
	s := NewScreen(10, 1)
	restore := s.Clip(0, 0, 3, 1)
	s.Set(2, 0, '日', DefaultStyle)
	restore()
	if c := s.At(2, 0); c.Rune == '日' {
		t.Error("wide rune drawn across the clip edge")
	}
	if c := s.At(3, 0); c.Width == 0 {
		t.Error("continuation cell written outside the clip")
	}
}

// Control bytes must never reach the terminal. A raw ESC would begin a
// sequence the terminal executes rather than displays, which is how opening a
// binary file used to take the screen apart.
func TestSetSanitizesControlBytes(t *testing.T) {
	s := NewScreen(20, 1)
	for i, r := range []rune{0x1b, 0x00, 0x07, 0x7f, 0x9b, '\r', rune(0xFFFD)} {
		s.Set(i, 0, r, DefaultStyle)
	}
	row := s.Row(0)
	if strings.ContainsAny(row, "\x1b\x00\x07\x7f\r") {
		t.Fatalf("control byte survived into the cell grid: %q", row)
	}
	out := s.Diff(nil)
	for _, bad := range []string{"\x00", "\x07", "\x7f", "\r"} {
		if strings.Contains(out, bad) {
			t.Errorf("control byte %q emitted to the terminal", bad)
		}
	}
	// The only ESC in the output should be ours: cursor moves and SGR.
	for i := 0; i < len(out); i++ {
		if out[i] == 0x1b && (i+1 >= len(out) || out[i+1] != '[') {
			t.Errorf("bare ESC at %d in %q", i, out)
		}
	}
}

// Unprintable characters become a visible placeholder, so damage is legible
// rather than invisible.
func TestSanitizePlaceholders(t *testing.T) {
	s := NewScreen(4, 1)
	s.Set(0, 0, 0x1b, DefaultStyle)
	if got := s.At(0, 0).Rune; got != '·' {
		t.Errorf("ESC rendered as %q, want a placeholder", got)
	}
}

// Default must not report as a direct colour: it is -1, which has every bit
// set including the RGB flag.
func TestDefaultIsNotRGB(t *testing.T) {
	if _, _, _, ok := Default.RGB(); ok {
		t.Error("Default reported as a direct colour")
	}
	if _, _, _, ok := Ansi(8).RGB(); ok {
		t.Error("a palette colour reported as direct")
	}
	r, g, b, ok := RGBColor(1, 2, 3).RGB()
	if !ok || r != 1 || g != 2 || b != 3 {
		t.Errorf("round trip gave %d,%d,%d ok=%v", r, g, b, ok)
	}
}
