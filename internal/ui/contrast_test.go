package ui

import (
	"math"
	"testing"
)

// ContrastRatio(black, white) is the WCAG maximum, 21.
func TestContrastRatioBlackOnWhite(t *testing.T) {
	got, ok := ContrastRatio(Ansi(16), Ansi(231))
	if !ok {
		t.Fatal("black and white are both resolvable")
	}
	if math.Abs(got-21) > 1e-9 {
		t.Errorf("contrast(black, white) = %v, want 21", got)
	}
}

// Luminance lands on the WCAG reference values for the extremes and the
// primaries' coefficients.
func TestLuminanceKnownValues(t *testing.T) {
	for _, c := range []struct {
		name string
		col  Color
		want float64
	}{
		{"black", Ansi(16), 0},
		{"white", Ansi(231), 1},
		{"red", RGBColor(255, 0, 0), 0.2126},
		{"green", RGBColor(0, 255, 0), 0.7152},
		{"blue", RGBColor(0, 0, 255), 0.0722},
	} {
		got, ok := Luminance(c.col)
		if !ok {
			t.Errorf("%s: not resolvable", c.name)
			continue
		}
		if math.Abs(got-c.want) > 1e-4 {
			t.Errorf("luminance(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// The upper palette is defined by a formula, so it resolves without the
// terminal; the first sixteen entries are the user's own colours and must not
// be guessed at.
func TestPaletteRGB(t *testing.T) {
	for _, c := range []struct {
		n       uint8
		r, g, b uint8
	}{
		{16, 0, 0, 0},
		{21, 0, 0, 255},
		{196, 255, 0, 0},
		{231, 255, 255, 255},
		{232, 8, 8, 8},
		{255, 238, 238, 238},
	} {
		r, g, b, ok := PaletteRGB(c.n)
		if !ok {
			t.Errorf("PaletteRGB(%d) not resolvable, want a colour", c.n)
			continue
		}
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("PaletteRGB(%d) = %d,%d,%d, want %d,%d,%d", c.n, r, g, b, c.r, c.g, c.b)
		}
	}
	for n := range 16 {
		if _, _, _, ok := PaletteRGB(uint8(n)); ok {
			t.Errorf("PaletteRGB(%d) resolved a terminal-configurable colour", n)
		}
	}
}

// LegibleOn picks the side of the background that reads: for any resolvable
// background the chosen black or white clears the 4.5 WCAG threshold, and it is
// always at least the better of the two.
func TestLegibleOnPicksTheHigherContrast(t *testing.T) {
	for n := 16; n <= 255; n++ {
		bg := Ansi(uint8(n))
		fg := LegibleOn(bg)
		if fg != Ansi(16) && fg != Ansi(231) {
			t.Fatalf("LegibleOn(%d) = %v, want black or near-white", n, fg)
		}
		got, ok := ContrastRatio(fg, bg)
		if !ok {
			t.Fatalf("contrast(%v, %v) not resolvable", fg, bg)
		}
		if got < 4.5 {
			t.Errorf("LegibleOn(%d) = %v with contrast %.2f, want >= 4.5", n, fg, got)
		}
		cb, _ := ContrastRatio(Ansi(16), bg)
		cw, _ := ContrastRatio(Ansi(231), bg)
		best := cb
		if cw > best {
			best = cw
		}
		if got < best-1e-9 {
			t.Errorf("LegibleOn(%d) chose %.3f, below the better %.3f", n, got, best)
		}
	}
}

// A colour only the terminal knows gets no guess: the ratio is not reported and
// the foreground stays Default.
func TestUnknownColoursAreNotResolved(t *testing.T) {
	if _, ok := ContrastRatio(Default, Ansi(16)); ok {
		t.Error("Default should not resolve to a contrast ratio")
	}
	if _, ok := ContrastRatio(Ansi(8), Ansi(16)); ok {
		t.Error("a 0-15 palette index should not resolve to a contrast ratio")
	}
	if _, ok := Luminance(Default); ok {
		t.Error("Default should not have a luminance")
	}
	if got := LegibleOn(Default); got != Default {
		t.Errorf("LegibleOn(Default) = %v, want Default", got)
	}
	if got := LegibleOn(Ansi(8)); got != Default {
		t.Errorf("LegibleOn(Ansi(8)) = %v, want Default for an unknown background", got)
	}
}

// A direct colour resolves through RGB, the same way a palette entry does.
func TestDirectColourResolves(t *testing.T) {
	got, ok := ContrastRatio(RGBColor(0, 0, 0), RGBColor(255, 255, 255))
	if !ok || math.Abs(got-21) > 1e-9 {
		t.Errorf("contrast of direct black/white = %v (ok=%v), want 21", got, ok)
	}
}
