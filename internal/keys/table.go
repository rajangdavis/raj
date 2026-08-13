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

	{"file", NewFile, "super+n", "110;9u", "cmd+n", "ctrl+n", "ghostty default: new window"},
	{"file", Save, "super+s", "115;9u", "cmd+s", "ctrl+s", ""},

	{"edit", Undo, "super+z", "122;9u", "cmd+z", "ctrl+z", ""},
	{"edit", Redo, "shift+super+z", "122;10u", "cmd+shift+z", "ctrl+shift+z", ""},
	{"edit", Cut, "super+x", "120;9u", "cmd+x", "ctrl+x", ""},
	{"edit", Copy, "super+c", "99;9u", "cmd+c", "ctrl+shift+c", "ghostty default: copy. raj writes clipboard via OSC 52"},
	{"edit", SelectAll, "super+a", "97;9u", "cmd+a", "ctrl+a", ""},
	{"edit", SelectLine, "super+l", "108;9u", "cmd+l", "ctrl+l", "linux default: clear screen"},
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
	{"cursor", SplitIntoLines, "shift+super+l", "108;10u", "cmd+shift+l", "ctrl+shift+l", "sublime: split selection into lines"},
	{"cursor", AllOccurrences, "ctrl+super+g", "103;13u", "cmd+ctrl+g", "ctrl+alt+g", "sublime: find all. alt+f3 on linux, but f3 encodes inconsistently"},
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
	{"nav", FindNext, "super+g", "103;9u", "cmd+g", "ctrl+alt+n", "macOS default: find next"},
	{"nav", FindPrev, "shift+super+g", "103;10u", "cmd+shift+g", "ctrl+alt+p", "macOS default: find previous"},
	{"nav", GotoSymbol, "shift+super+o", "111;10u", "cmd+shift+o", "ctrl+shift+o", ""},
	// cmd+i for hover, cmd+j to jump to a definition. F12 is what most editors
	// use, but the function keys are not in the chord vocabulary here and
	// adding them for two bindings would be a larger change than the bindings.
	//
	// Two earlier choices were wrong for reasons raj cannot detect at runtime,
	// which is why the reserved tables below exist. cmd+alt+d never arrived at
	// all: macOS hides the Dock with it. cmd+shift+d arrives, but iTerm2 and
	// Ghostty both split a pane with it, and taking a chord someone uses for
	// their terminal is a worse trade than picking a duller letter.
	{"nav", Hover, "super+i", "105;9u", "cmd+i", "ctrl+i", ""},
	{"nav", GotoDef, "super+j", "106;9u", "cmd+j", "ctrl+alt+j", ""},
}

// Reclaim holds chords a terminal keeps for itself, which raj therefore has to
// take back explicitly. They are Bindings in every mechanical sense — same
// struct, same emitters — and separate only because the reason they are listed
// is different: a Bindings entry exists because Ghostty already binds the chord,
// a Reclaim entry because the chord never reaches raj at all until the terminal
// is told to hand it over.
//
// shift+pgup and shift+pgdown are iTerm2's scrollback keys. Under Ghostty they
// arrive on their own; under iTerm2 they scroll the terminal and raj sees
// nothing, which is why the keymap binding them was not enough.
var Reclaim = []Binding{
	{"nav", SelPageUp, "shift+pgup", "5;2~", "shift+page_up", "shift+page_up", "iTerm2 default: scroll back"},
	{"nav", SelPageDown, "shift+pgdown", "6;2~", "shift+page_down", "shift+page_down", "iTerm2 default: scroll forward"},
}

// Native holds the chords the keymap binds that need no configuration at all:
// no terminal claims them, so report_all delivers them untouched. They are
// listed rather than left implicit so that every chord raj resolves is
// accounted for in exactly one table — TestEveryKeymapChordIsAccountedFor is
// what makes that a rule rather than an intention.
type Native struct {
	Chord  string
	Action Action
	Note   string
}

var Natives = []Native{
	{"tab", CycleFocus, "editor scope overrides this to Indent"},
	{"shift+tab", CycleFocusBack, "editor scope overrides this to Outdent"},
	{"esc", Cancel, "one byte without KKP; see the escape timeout"},
	{"enter", Confirm, "editor scope inserts a newline instead"},
	{"left", CharLeft, ""},
	{"right", CharRight, ""},
	{"up", LineUp, ""},
	{"down", LineDown, ""},
	{"shift+left", SelCharLeft, ""},
	{"shift+right", SelCharRight, ""},
	{"shift+up", SelLineUp, ""},
	{"shift+down", SelLineDown, ""},
	{"ctrl+alt+w", ToggleWrap, "ctrl suppresses macOS option-composition"},
	{"pgup", PageUp, ""},
	{"pgdown", PageDown, ""},
	{"backspace", Backspace, ""},
	{"delete", Delete, ""},
}

// Emitted is every chord that needs a line in a terminal config: the ones the
// terminal binds, plus the ones it keeps.
func Emitted() []Binding { return append(append([]Binding{}, Bindings...), Reclaim...) }

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
		for _, bd := range Emitted() {
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
