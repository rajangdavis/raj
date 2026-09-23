package ui

import (
	"fmt"
	"math"
)

// Color is packed into one comparable scalar so a Cell stays small and frame
// diffing is a plain equality test.
//
//	-1        inherit the terminal's default
//	0..255    ANSI palette index
//	1<<24|RGB direct colour
//
// Defaulting matters more than it looks: a pane that leaves foreground and
// background as Default inherits the user's Ghostty theme for free, including
// any changes made while raj is running. raj only names explicit colours where
// it must — syntax highlighting and author tints.
type Color int32

const (
	// Default inherits the terminal's own foreground or background.
	Default Color = -1

	rgbFlag = 1 << 24
)

// Ansi returns palette index n (0-255).
func Ansi(n uint8) Color { return Color(n) }

// RGBColor returns a direct-colour value.
func RGBColor(r, g, b uint8) Color {
	return Color(rgbFlag | int32(r)<<16 | int32(g)<<8 | int32(b))
}

// RGB unpacks a direct colour. ok is false for Default and palette colours,
// whose actual values only the terminal knows.
func (c Color) RGB() (r, g, b uint8, ok bool) {
	// Default is -1, which has every bit set including the flag, so it must be
	// excluded explicitly or it reports as a direct colour with garbage
	// components.
	if c < 0 || c&rgbFlag == 0 {
		return 0, 0, 0, false
	}
	return uint8(c >> 16), uint8(c >> 8), uint8(c), true
}

// The xterm 256-colour palette's upper entries are defined by a formula every
// terminal implements, so the colours raj names itself can be resolved without
// asking the terminal. Indices 0-15 are the exception: those are the user's
// configured colours, so a resolver reports them unknown rather than guessing
// a value the terminal may have redefined.
//
// PaletteRGB returns the components of a palette index. ok is false for 0-15.
func PaletteRGB(n uint8) (r, g, b uint8, ok bool) {
	if n < 16 {
		return 0, 0, 0, false
	}
	if n < 232 {
		c := int(n) - 16
		return cubeLevel(c / 36), cubeLevel(c / 6 % 6), cubeLevel(c % 6), true
	}
	v := uint8(8 + 10*(int(n)-232))
	return v, v, v, true
}

// cubeLevel maps one axis of the 6x6x6 colour cube to its component: 0, then
// 95 upward in steps of 40, the levels the xterm palette specifies.
func cubeLevel(v int) uint8 {
	if v == 0 {
		return 0
	}
	return uint8(55 + 40*v)
}

// resolveRGB turns a Color into components whichever representation it uses: a
// direct colour through RGB, a palette index through PaletteRGB. ok is false
// when the actual value is only known to the terminal.
func resolveRGB(c Color) (r, g, b uint8, ok bool) {
	if r, g, b, ok := c.RGB(); ok {
		return r, g, b, true
	}
	if c < 0 || c > 255 {
		return 0, 0, 0, false
	}
	return PaletteRGB(uint8(c))
}

// Luminance is the WCAG relative luminance of a colour, from 0 for black to 1
// for white. ok is false when the RGB is unknown: a palette index 0-15 or
// Default, whose actual colour only the terminal knows.
func Luminance(c Color) (float64, bool) {
	r, g, b, ok := resolveRGB(c)
	if !ok {
		return 0, false
	}
	return 0.2126*linearChannel(r) + 0.7152*linearChannel(g) + 0.0722*linearChannel(b), true
}

func linearChannel(v uint8) float64 {
	c := float64(v) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// ContrastRatio is the WCAG contrast ratio between two colours: 1 for two
// identical colours and 21 for black against white. ok is false when either
// colour's RGB is unknown.
func ContrastRatio(a, b Color) (float64, bool) {
	la, oka := Luminance(a)
	lb, okb := Luminance(b)
	if !oka || !okb {
		return 0, false
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

// LegibleOn picks a foreground that reads on bg: black or near-white, whichever
// has the higher contrast. Default is returned when bg's RGB is unknown, so the
// terminal chooses rather than a guess being made for it.
func LegibleOn(bg Color) Color {
	if _, _, _, ok := resolveRGB(bg); !ok {
		return Default
	}
	black, white := Ansi(16), Ansi(231)
	cb, _ := ContrastRatio(black, bg)
	cw, _ := ContrastRatio(white, bg)
	if cb >= cw {
		return black
	}
	return white
}

// sgrParams renders the colour as SGR parameters. fg selects 3x/9x vs 4x/10x.
func (c Color) sgrParams(fg bool) string {
	base := 40
	if fg {
		base = 30
	}
	switch {
	case c == Default:
		return fmt.Sprint(base + 9)
	case c&rgbFlag != 0:
		r, g, b, _ := c.RGB()
		return fmt.Sprintf("%d;2;%d;%d;%d", base+8, r, g, b)
	case c < 8:
		return fmt.Sprint(base + int(c))
	case c < 16:
		return fmt.Sprint(base + 60 + int(c) - 8)
	default:
		return fmt.Sprintf("%d;5;%d", base+8, int(c))
	}
}

// Attr is a set of text attributes.
type Attr uint8

const (
	Bold Attr = 1 << iota
	Dim
	Italic
	Underline
	Reverse
	Strike
)

// Style is a cell's full appearance. It is a comparable value type, so frame
// diffing compares styles directly rather than walking fields.
type Style struct {
	Fg, Bg Color
	Attr   Attr
}

// DefaultStyle inherits both colours from the terminal.
var DefaultStyle = Style{Fg: Default, Bg: Default}

// With returns a copy with the given foreground.
func (s Style) With(fg Color) Style { s.Fg = fg; return s }

// On returns a copy with the given background. Author tints are backgrounds, so
// this is the one the renderer reaches for most.
func (s Style) On(bg Color) Style { s.Bg = bg; return s }

// Plus returns a copy with attributes added.
func (s Style) Plus(a Attr) Style { s.Attr |= a; return s }

// sgr renders the full escape sequence to move from any style to this one. It
// always resets first, which costs a few bytes per style change but removes a
// whole class of stuck-attribute bugs.
func (s Style) sgr() string {
	out := "\x1b[0"
	for _, m := range []struct {
		a Attr
		n int
	}{{Bold, 1}, {Dim, 2}, {Italic, 3}, {Underline, 4}, {Reverse, 7}, {Strike, 9}} {
		if s.Attr&m.a != 0 {
			out += fmt.Sprintf(";%d", m.n)
		}
	}
	return out + ";" + s.Fg.sgrParams(true) + ";" + s.Bg.sgrParams(false) + "m"
}
