package ui

import "fmt"

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
