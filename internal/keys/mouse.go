package keys

import "strings"

// Mouse reporting, in the SGR encoding (DECSET 1006).
//
// Only SGR is decoded. The original X10 encoding packs coordinates into single
// bytes offset by 32, which caps them at 223 columns and makes the parameters
// indistinguishable from arbitrary text — a decoder cannot tell a click at
// column 200 from a UTF-8 continuation byte. SGR is a normal CSI sequence with
// decimal parameters and no ceiling, every terminal worth supporting has had it
// for a decade, and raj asks for it explicitly. A terminal that refuses simply
// sends nothing, which is what happens today.
//
// The wire format is `CSI < Cb ; Cx ; Cy M` for a press and `... m` for a
// release, where Cb carries the button in its low bits and the modifiers above
// them.

// MouseButton is which button, or which wheel direction, an event carries.
type MouseButton int

const (
	MouseLeft MouseButton = iota
	MouseMiddle
	MouseRight
	MouseNone // motion with nothing held, and the release of an unknown button
	WheelUp
	WheelDown
	WheelLeft
	WheelRight
)

// Mouse is a decoded mouse report. Col and Row are zero-based, unlike the wire
// format, because every other coordinate in raj is zero-based and converting at
// the boundary is cheaper than remembering which convention applies where.
type Mouse struct {
	Button  MouseButton
	Col     int
	Row     int
	Mods    int
	Press   bool // false for a release
	Motion  bool // the button-motion bit: a drag, or bare movement
	IsWheel bool
}

// mouse bits in Cb.
const (
	mouseMotionBit = 32
	mouseWheelBit  = 64
	mouseExtraBit  = 128 // buttons 8-11, which raj does not use
)

// parseMouse decodes a `CSI < ... M/m` report. The caller has already split the
// parameters and checked the private marker.
func parseMouse(e *Event) bool {
	if len(e.Params) != 3 {
		return false
	}
	cb, ok1 := atoiStrict(strings.TrimPrefix(e.Params[0], "<"))
	cx, ok2 := atoiStrict(e.Params[1])
	cy, ok3 := atoiStrict(e.Params[2])
	if !ok1 || !ok2 || !ok3 {
		return false
	}

	m := Mouse{
		// A terminal reports 1-based coordinates. Clamp rather than allow a
		// negative: a zero here would mean a report from outside the window,
		// which is not a thing raj can act on but is also not worth dropping
		// the event over.
		Col:    max0(cx - 1),
		Row:    max0(cy - 1),
		Press:  e.Final == 'M',
		Motion: cb&mouseMotionBit != 0,
	}
	m.Mods = mouseMods(cb)

	switch {
	case cb&mouseExtraBit != 0:
		return false // buttons 8-11: not bound to anything, so not decoded
	case cb&mouseWheelBit != 0:
		m.IsWheel = true
		m.Button = WheelUp + MouseButton(cb&3)
		// A wheel report is always a press on the wire; there is no release
		// of a wheel notch, and treating one as a release would swallow half
		// the scrolling.
		m.Press = true
	default:
		m.Button = MouseLeft + MouseButton(cb&3)
	}

	e.Kind = MouseEvent
	e.Mouse = m
	return true
}

// mouseMods pulls the modifier bits out of Cb. They sit in the same order as
// the KKP modifiers but at different offsets and without super, so they are
// translated rather than reused.
func mouseMods(cb int) int {
	mods := 0
	if cb&4 != 0 {
		mods |= ModShift
	}
	if cb&8 != 0 {
		mods |= ModAlt
	}
	if cb&16 != 0 {
		mods |= ModCtrl
	}
	return mods
}

// atoiStrict parses a decimal with no sign and no slack, so a malformed report
// is rejected rather than silently read as zero. A mouse report arrives in the
// same stream as text; misreading one as a click at (0,0) would move the cursor
// on garbage.
func atoiStrict(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
