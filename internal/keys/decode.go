package keys

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type Kind int

const (
	KeyEvent Kind = iota
	FocusIn
	FocusOut
	KKPFlags
	OSCReply
	OtherReply
	Partial
)

// KKP modifier bits. caps_lock (64) and num_lock (128) are reported too and
// MUST be masked out before matching a chord, or capslock breaks every binding.
const (
	ModShift = 1
	ModAlt   = 2
	ModCtrl  = 4
	ModSuper = 8
	modMask  = ModShift | ModAlt | ModCtrl | ModSuper | 16 | 32
)

// Event types under KKP flag 2. Release events fire for every key; an editor
// that does not filter them will double-apply every action.
const (
	Press   = 1
	Repeat  = 2
	Release = 3
)

type Event struct {
	Kind    Kind
	Raw     []byte
	Code    int // unicode-key-code (or the '~' number for legacy keys)
	Shifted int // alternate-key: shifted codepoint (KKP flag 4)
	Base    int // alternate-key: base-layout codepoint
	Mods    int // masked bitmask, already decremented from the wire value
	Type    int // Press / Repeat / Release
	Text    string
	Final   byte
	Params  []string
}

// parse decodes one event from the head of b. n == 0 means "need more bytes".
func Parse(b []byte) (Event, int) {
	if len(b) == 0 {
		return Event{Kind: Partial}, 0
	}
	if b[0] != 0x1b {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 && len(b) < 4 {
			return Event{Kind: Partial}, 0
		}
		return Event{Kind: KeyEvent, Raw: b[:n], Code: int(r), Type: Press}, n
	}
	if len(b) == 1 {
		return Event{Kind: Partial}, 0
	}
	switch b[1] {
	case '[':
		return parseCSI(b)
	case ']':
		return parseTerminated(b, OSCReply)
	case 'P', 'X', '^', '_':
		return parseTerminated(b, OtherReply)
	}
	// Legacy alt+key: ESC followed by the key.
	r, n := utf8.DecodeRune(b[1:])
	if r == utf8.RuneError && n == 1 && len(b) < 5 {
		return Event{Kind: Partial}, 0
	}
	return Event{Kind: KeyEvent, Raw: b[:1+n], Code: int(r), Mods: ModAlt, Type: Press}, 1 + n
}

func parseCSI(b []byte) (Event, int) {
	i := 2
	for i < len(b) && b[i] >= 0x30 && b[i] <= 0x3f {
		i++
	}
	for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
		i++
	}
	if i >= len(b) || b[i] < 0x40 || b[i] > 0x7e {
		return Event{Kind: Partial}, 0
	}
	e := Event{Raw: b[:i+1], Final: b[i]}
	body := string(b[2:i])
	if body != "" {
		e.Params = strings.Split(body, ";")
	}
	n := i + 1

	switch {
	case e.Final == 'I':
		e.Kind = FocusIn
		return e, n
	case e.Final == 'O':
		e.Kind = FocusOut
		return e, n
	case e.Final == 'u' && strings.HasPrefix(body, "?"):
		e.Kind = KKPFlags
		return e, n
	case e.Final == 'u' || e.Final == '~' || strings.IndexByte("ABCDEFHPQS", e.Final) >= 0:
		e.Kind = KeyEvent
	default:
		e.Kind = OtherReply
		return e, n
	}

	key := sub(e.Params, 0)
	e.Code, e.Shifted, e.Base = num(key, 0), num(key, 1), num(key, 2)
	if e.Final != 'u' && e.Final != '~' {
		e.Code = 0 // functional key: identity is the final byte, not the number
	}
	mod := sub(e.Params, 1)
	if m := num(mod, 0); m > 0 {
		e.Mods = (m - 1) & modMask
	}
	e.Type = Press
	if t := num(mod, 1); t > 0 {
		e.Type = t
	}
	for _, cp := range strings.Split(sub(e.Params, 2), ":") {
		if v, err := strconv.Atoi(cp); err == nil && v > 0 {
			e.Text += string(rune(v))
		}
	}
	return e, n
}

// parseTerminated consumes an OSC/DCS/APC string up to BEL or ST.
func parseTerminated(b []byte, k Kind) (Event, int) {
	for i := 2; i < len(b); i++ {
		if b[i] == 0x07 {
			return Event{Kind: k, Raw: b[:i+1]}, i + 1
		}
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
			return Event{Kind: k, Raw: b[:i+2]}, i + 2
		}
	}
	return Event{Kind: Partial}, 0
}

func sub(params []string, i int) string {
	if i < len(params) {
		return params[i]
	}
	return ""
}

func num(field string, i int) int {
	parts := strings.Split(field, ":")
	if i >= len(parts) {
		return 0
	}
	v, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}
	return v
}
