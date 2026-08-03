package term

import (
	"fmt"
	"strconv"
	"strings"
)

// Query sequences. Responses arrive on the tty like any other input and are
// parsed by ParseColorReply.
const (
	QueryKKPFlags   = "\x1b[?u"
	QueryBackground = "\x1b]11;?\x07"
	QueryForeground = "\x1b]10;?\x07"
	queryPaletteFmt = "\x1b]4;%d;?\x07"
)

// Color is an 8-bit-per-channel RGB triple.
type Color struct{ R, G, B uint8 }

// Theme is what raj reads from the host terminal so it inherits the user's
// Ghostty configuration rather than shipping its own palette.
type Theme struct {
	Background Color
	Foreground Color
	Palette    map[int]Color // ANSI indices actually queried
}

// Dark reports whether the background is dark, using Rec. 601 luma. This picks
// the syntax-highlighting style; getting it backwards makes everything
// unreadable, so it is worth querying rather than assuming.
func (t Theme) Dark() bool { return t.Background.Luma() < 0.5 }

// Luma is perceived brightness in [0,1].
func (c Color) Luma() float64 {
	return (0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)) / 255
}

// QueryTheme writes the colour queries. Call ParseColorReply on the OSC replies
// that come back, then Theme.Set to assemble them.
func (t *Terminal) QueryTheme(palette ...int) {
	fmt.Fprint(t.out, QueryBackground)
	fmt.Fprint(t.out, QueryForeground)
	for _, n := range palette {
		fmt.Fprintf(t.out, queryPaletteFmt, n)
	}
}

// ParseColorReply decodes an OSC colour response.
//
// Terminals answer with 16 bits per channel — rgb:1e1e/1e1e/1e1e — but some
// use 4, 8 or 12, so each component is scaled by its own width rather than
// assuming four hex digits. kind is 10 (foreground), 11 (background), or 4 for
// a palette entry, in which case index is the ANSI slot.
func ParseColorReply(raw []byte) (kind, index int, c Color, ok bool) {
	s := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\x07"), "\x1b\\")
	if !strings.HasPrefix(s, "\x1b]") {
		return 0, 0, c, false
	}
	parts := strings.Split(s[2:], ";")
	if len(parts) < 2 {
		return 0, 0, c, false
	}
	kind, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, c, false
	}
	spec := parts[len(parts)-1]
	if kind == 4 {
		if len(parts) < 3 {
			return 0, 0, c, false
		}
		if index, err = strconv.Atoi(parts[1]); err != nil {
			return 0, 0, c, false
		}
	}
	c, ok = parseRGBSpec(spec)
	return kind, index, c, ok
}

// parseRGBSpec handles the X11 "rgb:RRRR/GGGG/BBBB" form at any component width.
func parseRGBSpec(spec string) (Color, bool) {
	spec = strings.TrimPrefix(spec, "rgb:")
	comps := strings.Split(spec, "/")
	if len(comps) != 3 {
		return Color{}, false
	}
	var out [3]uint8
	for i, comp := range comps {
		if comp == "" || len(comp) > 4 {
			return Color{}, false
		}
		v, err := strconv.ParseUint(comp, 16, 32)
		if err != nil {
			return Color{}, false
		}
		max := float64(uint64(1)<<(4*len(comp))) - 1
		out[i] = uint8(float64(v)/max*255 + 0.5)
	}
	return Color{out[0], out[1], out[2]}, true
}

// Set files a parsed reply into the theme. It takes ParseColorReply's full
// result so callers can write th.Set(ParseColorReply(raw)); a failed parse is
// ignored rather than clobbering a good value with zeroes.
func (t *Theme) Set(kind, index int, c Color, ok bool) {
	if !ok {
		return
	}
	switch kind {
	case 10:
		t.Foreground = c
	case 11:
		t.Background = c
	case 4:
		if t.Palette == nil {
			t.Palette = map[int]Color{}
		}
		t.Palette[index] = c
	}
}
