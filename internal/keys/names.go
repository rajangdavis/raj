package keys

import (
	"fmt"
	"strings"
)

// finalNames: CSI-with-letter-final functional keys.
var finalNames = map[byte]string{
	'A': "up", 'B': "down", 'C': "right", 'D': "left",
	'H': "home", 'F': "end", 'E': "kpbegin",
	'P': "f1", 'Q': "f2", 'S': "f4",
}

// tildeNames: CSI <n> ; mods ~ functional keys.
var tildeNames = map[int]string{
	2: "insert", 3: "delete", 5: "pgup", 6: "pgdown",
	15: "f5", 17: "f6", 18: "f7", 19: "f8", 20: "f9", 21: "f10", 23: "f11", 24: "f12",
}

// codeNames: CSI-u unicode-key-codes that are not literal characters.
var codeNames = map[int]string{
	9: "tab", 13: "enter", 27: "esc", 32: "space", 127: "backspace",
	57344: "esc", 57345: "enter", 57346: "tab", 57347: "backspace",
	57348: "insert", 57349: "delete", 57352: "home", 57353: "end",
	57354: "pgup", 57355: "pgdown", 57358: "capslock",
	57441: "lshift", 57442: "lctrl", 57443: "lalt", 57444: "lsuper",
	57447: "rshift", 57448: "rctrl", 57449: "ralt", 57450: "rsuper",
}

// modOrder matches the order Bubbletea v2 renders chords in, so the strings the
// probe prints line up with what raj's keymap will match on.
var modOrder = []struct {
	bit  int
	name string
}{{ModShift, "shift"}, {ModCtrl, "ctrl"}, {ModAlt, "alt"}, {ModSuper, "super"}, {16, "hyper"}, {32, "meta"}}

// chord renders the canonical name, e.g. "shift+super+f".
func (e Event) Chord() string {
	var parts []string
	for _, m := range modOrder {
		if e.Mods&m.bit != 0 {
			parts = append(parts, m.name)
		}
	}
	return strings.Join(append(parts, e.KeyName()), "+")
}

func (e Event) KeyName() string {
	if e.Final != 0 && e.Final != 'u' && e.Final != '~' {
		if n, ok := finalNames[e.Final]; ok {
			return n
		}
		return fmt.Sprintf("final(%c)", e.Final)
	}
	if e.Final == '~' {
		if n, ok := tildeNames[e.Code]; ok {
			return n
		}
		return fmt.Sprintf("tilde(%d)", e.Code)
	}
	if n, ok := codeNames[e.Code]; ok {
		return n
	}
	if e.Code > 32 && e.Code < 127 {
		return string(rune(e.Code))
	}
	if e.Code < 32 && e.Code > 0 {
		return fmt.Sprintf("ctrl-char(%d)", e.Code) // legacy, KKP not active
	}
	return fmt.Sprintf("code(%d)", e.Code)
}

// isModifierKey reports a bare shift/ctrl/alt/super press, which flag 2 reports
// as its own event. raj must ignore these.
func (e Event) IsModifierKey() bool { return e.Code >= 57441 && e.Code <= 57450 }

// escape renders raw bytes readably: ESC as \e, control bytes as \xNN.
func Escape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == 0x1b:
			sb.WriteString(`\e`)
		case c == 0x07:
			sb.WriteString(`\a`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func TypeName(t int) string {
	switch t {
	case Repeat:
		return "repeat"
	case Release:
		return "RELEASE"
	}
	return "press"
}
