package keys

import "strings"

// Binding is one chord raj owns. Chord is what Parse+Chord produce after the
// key reaches us; Seq is the CSI payload Ghostty emits (bytes are ESC '[' +
// Seq); Mac/Linux are the Ghostty triggers that produce it.
//
// The Ghostty config is the platform-adaptation layer: macOS cmd-chords and
// Linux ctrl-chords normalise to the SAME Seq, so raj sees one canonical chord
// set and the keymap is platform-independent.
//
// Every Chord/Seq pair here was measured against a patched Ghostty via
// cmd/keyprobe, not inferred. TestBindingsRoundTrip keeps them consistent.
type Binding struct {
	Group  string
	Action Action
	Chord  string
	Seq    string
	Mac    string
	Linux  string
	Note   string
}

// Bindings needs an explicit kkp_on line only for chords Ghostty already binds;
// under report_all everything else is auto-encoded. The rest are listed so the
// encoding is pinned rather than inferred, and so both platforms agree.
//
// cmd+1-9 are deliberately absent. They are the terminal's own tab-select
// chords, and raj claiming them meant there was no way to reach another
// terminal tab without suspending. Leaving them unbound hands them back:
// Ghostty binds them by default, so dropping the kkp_on line restores its
// action rather than sending the chord to nobody. raj's own tabs are reachable
// with cmd+alt+left/right. GotoTab1-9 and app.tabNumber stay live but unbound,
// so rebinding later is a one-line change.
var Bindings = []Binding{
	{"panes", ToggleSidebar, "super+b", "98;9u", "cmd+b", "ctrl+b", ""},
	{"panes", FocusExplorer, "shift+super+e", "101;10u", "cmd+shift+e", "ctrl+shift+e", "linux default: new split down"},
	{"panes", FocusSearch, "shift+super+f", "102;10u", "cmd+shift+f", "ctrl+shift+f", ""},
	{"panes", ToggleAgent, "alt+super+b", "98;11u", "cmd+alt+b", "ctrl+alt+b", ""},
	{"panes", FilePicker, "super+p", "112;9u", "cmd+p", "ctrl+p", ""},
	{"panes", CommandPalette, "shift+super+p", "112;10u", "cmd+shift+p", "ctrl+shift+p", ""},
	{"panes", FindInFile, "super+f", "102;9u", "cmd+f", "ctrl+f", "macOS default: find"},
	{"panes", Suspend, "ctrl+z", "122;5u", "ctrl+z", "ctrl+alt+z", "linux ctrl+z is undo, so suspend moves"},
	{"panes", Quit, "ctrl+c", "99;5u", "ctrl+c", "ctrl+c", ""},
	{"panes", ToggleDebug, "shift+ctrl+d", "100;6u", "ctrl+shift+d", "ctrl+shift+d", ""},

	{"tabs", CloseTab, "super+w", "119;9u", "cmd+w", "ctrl+shift+w", "ghostty default: close surface"},
	{"tabs", ReopenTab, "shift+super+t", "116;10u", "cmd+shift+t", "ctrl+shift+t", ""},
	{"tabs", NextTab, "alt+super+right", "1;11C", "cmd+alt+right", "ctrl+alt+right", "ghostty default: focus split right"},
	{"tabs", PrevTab, "alt+super+left", "1;11D", "cmd+alt+left", "ctrl+alt+left", "ghostty default: focus split left"},

	{"file", Save, "super+s", "115;9u", "cmd+s", "ctrl+s", ""},

	{"edit", Undo, "super+z", "122;9u", "cmd+z", "ctrl+z", ""},
	{"edit", Redo, "shift+super+z", "122;10u", "cmd+shift+z", "ctrl+shift+z", ""},
	{"edit", Cut, "super+x", "120;9u", "cmd+x", "ctrl+x", ""},
	{"edit", Copy, "super+c", "99;9u", "cmd+c", "ctrl+shift+c", "ghostty default: copy. raj writes clipboard via OSC 52"},
	{"edit", SelectAll, "super+a", "97;9u", "cmd+a", "ctrl+a", ""},
	{"edit", ToggleComment, "super+/", "47;9u", "cmd+slash", "ctrl+slash", ""},
	{"edit", DeleteLine, "shift+super+k", "107;10u", "cmd+shift+k", "ctrl+shift+k", ""},
	{"edit", LineBelow, "super+enter", "13;9u", "cmd+enter", "ctrl+enter", "ghostty default: toggle fullscreen"},
	{"edit", LineAbove, "shift+super+enter", "13;10u", "cmd+shift+enter", "ctrl+shift+enter", "ghostty default: split zoom"},
	{"edit", MoveLineUp, "alt+up", "1;3A", "alt+up", "alt+up", ""},
	{"edit", MoveLineDown, "alt+down", "1;3B", "alt+down", "alt+down", ""},
	{"edit", CopyLineUp, "shift+alt+up", "1;4A", "shift+alt+up", "shift+alt+up", ""},
	{"edit", CopyLineDown, "shift+alt+down", "1;4B", "shift+alt+down", "shift+alt+down", ""},

	{"cursor", CursorAbove, "alt+super+up", "1;11A", "cmd+alt+up", "ctrl+alt+up", "ghostty default: focus split up"},
	{"cursor", CursorBelow, "alt+super+down", "1;11B", "cmd+alt+down", "ctrl+alt+down", "ghostty default: focus split down"},
	{"cursor", AddNextOccurrence, "super+d", "100;9u", "cmd+d", "ctrl+d", "ghostty default: split right"},
	{"cursor", AllOccurrences, "shift+super+l", "108;10u", "cmd+shift+l", "ctrl+shift+l", ""},
	{"cursor", CursorUndo, "super+u", "117;9u", "cmd+u", "ctrl+u", ""},

	{"nav", LineStart, "super+left", "1;9D", "cmd+left", "home", ""},
	{"nav", LineEnd, "super+right", "1;9C", "cmd+right", "end", ""},
	{"nav", DocStart, "super+up", "1;9A", "cmd+up", "ctrl+home", "ghostty default: jump to prev prompt"},
	{"nav", DocEnd, "super+down", "1;9B", "cmd+down", "ctrl+end", "ghostty default: jump to next prompt"},
	{"nav", WordLeft, "alt+left", "1;3D", "alt+left", "ctrl+left", "ghostty sends ESC b by default"},
	{"nav", WordRight, "alt+right", "1;3C", "alt+right", "ctrl+right", "ghostty sends ESC f by default"},
	{"nav", SelLineStart, "shift+super+left", "1;10D", "cmd+shift+left", "shift+home", ""},
	{"nav", SelLineEnd, "shift+super+right", "1;10C", "cmd+shift+right", "shift+end", ""},
	{"nav", SelDocStart, "shift+super+up", "1;10A", "cmd+shift+up", "ctrl+shift+home", ""},
	{"nav", SelDocEnd, "shift+super+down", "1;10B", "cmd+shift+down", "ctrl+shift+end", ""},
	{"nav", SelWordLeft, "shift+alt+left", "1;4D", "shift+alt+left", "ctrl+shift+left", ""},
	{"nav", SelWordRight, "shift+alt+right", "1;4C", "shift+alt+right", "ctrl+shift+right", ""},
	{"nav", GotoLine, "ctrl+g", "103;5u", "ctrl+g", "ctrl+g", ""},
	{"nav", GotoSymbol, "shift+super+o", "111;10u", "cmd+shift+o", "ctrl+shift+o", ""},
}

// GhosttyConfig renders the kkp_on keybind lines for a platform.
//
// Comments MUST be on their own line. Ghostty has no trailing-comment syntax:
// everything after '=' is the action value, so a trailing "# note" is delivered
// to the app as literal text after the CSI sequence.
func GhosttyConfig(platform string) string {
	var b strings.Builder
	b.WriteString("# raj keybindings — generated, do not edit by hand.\n")
	b.WriteString("# Applies only while the focused app has KKP report_all set;\n")
	b.WriteString("# otherwise Ghostty's own bindings are untouched.\n")
	for _, g := range []string{"panes", "tabs", "file", "edit", "cursor", "nav"} {
		b.WriteString("\n# ===== " + g + " =====\n")
		for _, bd := range Bindings {
			if bd.Group != g {
				continue
			}
			trigger := bd.Mac
			if platform == "linux" {
				trigger = bd.Linux
			}
			if trigger == "" {
				continue
			}
			b.WriteString("\n# " + string(bd.Action))
			if bd.Note != "" {
				b.WriteString(" — " + bd.Note)
			}
			b.WriteString("\nkeybind = kkp_on:" + trigger + "=csi:" + bd.Seq + "\n")
		}
	}
	return b.String()
}
