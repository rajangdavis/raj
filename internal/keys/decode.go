package keys

import (
	"bytes"
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
	PasteEvent
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
		if e, ok := legacyControl(b[0]); ok {
			return e, 1
		}
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
		// Bracketed paste is claimed before parseCSI, because the payload
		// between the markers is arbitrary bytes rather than parameters: a
		// pasted "5;3R" would otherwise be read as a cursor-position reply,
		// and every newline in the paste as a separate enter keypress.
		if bytes.HasPrefix(b, pasteStart) {
			return parsePaste(b)
		}
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

// legacyControl decodes a C0 byte as the chord that produced it.
//
// Without the Kitty protocol a terminal sends ctrl+c as 0x03, not as
// CSI 99;5u. Every terminal does this, and any that does not speak KKP does it
// exclusively — so a decoder that only understands the modern encoding cannot
// see ctrl+c at all. That is not a hypothetical: iTerm2 answers the colour and
// device-attribute queries but not the KKP one, and raj was left unable to
// quit.
//
// Tab, enter and escape keep their own identities rather than becoming ctrl+i,
// ctrl+m and ctrl+[, because that is what the keys are called and what every
// keymap binds.
func legacyControl(b byte) (Event, bool) {
	e := Event{Kind: KeyEvent, Type: Press, Raw: []byte{b}}
	switch {
	case b == 9 || b == 13 || b == 27 || b == 32:
		return Event{}, false // named keys, handled as themselves
	case b == 8 || b == 127:
		e.Code = 127 // backspace
	case b == 0:
		e.Code, e.Mods = 32, ModCtrl // ctrl+space
	case b >= 1 && b <= 26:
		e.Code, e.Mods = int('a'+b-1), ModCtrl
	case b >= 28 && b <= 31:
		e.Code, e.Mods = int(`\]^_`[b-28]), ModCtrl
	default:
		return Event{}, false
	}
	return e, true
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

// Bracketed paste markers (DEC mode 2004). The terminal wraps pasted text in
// these so an application can tell it apart from typing.
var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// MaxPaste bounds how much is buffered while waiting for the end marker. A
// paste whose terminator never arrives would otherwise grow the read buffer
// without limit and raj would look hung, having consumed nothing. Past the cap
// the start marker is surrendered as an ordinary reply, which drains the buffer
// and lets the following bytes decode as keys — degraded, but not wedged.
const MaxPaste = 16 << 20

// parsePaste consumes a whole bracketed paste and returns its payload as Text.
//
// The payload is delivered in one event on purpose. Pasting a thousand lines as
// a thousand key events is a thousand buffer edits, a thousand undo entries and
// a thousand retokenises; as one event it is a single op the piece table stores
// once.
func parsePaste(b []byte) (Event, int) {
	end := bytes.Index(b, pasteEnd)
	if end < 0 {
		if len(b) > MaxPaste {
			return Event{Kind: OtherReply, Raw: b[:len(pasteStart)]}, len(pasteStart)
		}
		return Event{Kind: Partial}, 0
	}
	n := end + len(pasteEnd)
	return Event{
		Kind: PasteEvent,
		Raw:  b[:n],
		Text: normalizeNewlines(string(b[len(pasteStart):end])),
	}, n
}

// normalizeNewlines folds CR and CRLF to LF.
//
// A terminal reports Enter inside a paste as CR, so a multi-line paste taken
// literally is one buffer line containing control bytes the renderer draws as
// placeholders. Line endings are a wire detail here, not content. Nothing else
// is stripped: tabs are meaningful, and a paste containing ESC is the caller's
// problem to sanitise, not the decoder's to silently alter.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
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
