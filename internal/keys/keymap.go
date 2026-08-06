package keys

// Scope is which pane has focus. The same chord can mean different things per
// pane — tab indents a selection in the editor but cycles focus everywhere
// else — so resolution is scoped first, global second.
type Scope int

const (
	Global Scope = iota
	Editor
	Explorer
	Search
	Agent
	Picker
	Prompt
)

// Keymap resolves a chord to an Action. Zero value is unusable; use NewKeymap.
type Keymap struct {
	global map[string]Action
	scoped map[Scope]map[string]Action
}

// NewKeymap builds the default map: every Binding as a global, plus the
// scope-specific overrides that cannot live in the Ghostty config because they
// depend on focus rather than on the chord.
func NewKeymap() *Keymap {
	k := &Keymap{
		global: make(map[string]Action, len(Bindings)+16),
		scoped: make(map[Scope]map[string]Action, 4),
	}
	for _, b := range Bindings {
		k.global[b.Chord] = b.Action
	}
	// Chords that need no Ghostty line, and chords a terminal keeps and must be
	// told to hand over. Both live in table.go so that every chord raj resolves
	// is in exactly one table and a test can say so.
	for _, n := range Natives {
		k.global[n.Chord] = n.Action
	}
	for _, b := range Reclaim {
		k.global[b.Chord] = b.Action
	}
	// Tab indents in the editor and cycles focus everywhere else. This is what
	// makes the sidebar focus ring one-way: tab can walk out of a sidebar into
	// the editor, but once there it is indentation, so shift+tab cannot walk
	// back. Returning to a sidebar is always a chord.
	k.Bind(Editor, "tab", Indent)
	k.Bind(Editor, "shift+tab", Outdent)
	k.Bind(Editor, "enter", None) // the editor inserts a newline, not "confirm"

	// The picker is a modal overlay: enter chooses, escape dismisses, and tab
	// has nowhere to go.
	k.Bind(Picker, "tab", None)
	k.Bind(Picker, "shift+tab", None)

	// A prompt is modal in the strongest sense: it is the only thing that can
	// answer itself, so tab has nowhere to go and enter must mean "confirm"
	// even though the editor underneath would insert a newline.
	k.Bind(Prompt, "tab", None)
	k.Bind(Prompt, "shift+tab", None)
	return k
}

// Bind sets a scope-specific override. Binding to None masks the global.
func (k *Keymap) Bind(s Scope, chord string, a Action) {
	if k.scoped[s] == nil {
		k.scoped[s] = map[string]Action{}
	}
	k.scoped[s][chord] = a
}

// Lookup resolves a chord within a scope. Returns None when nothing is bound.
func (k *Keymap) Lookup(s Scope, chord string) Action {
	if m, ok := k.scoped[s]; ok {
		if a, ok := m[chord]; ok {
			return a
		}
	}
	return k.global[chord]
}

// Resolve turns a decoded Event into an Action.
//
// It drops the two event classes an editor must never act on twice: key
// releases (KKP flag 2 reports press and release for every chord) and bare
// modifier presses. text is the literal text to insert when the event is a
// printable key with no Action bound — the caller inserts it verbatim.
func (k *Keymap) Resolve(s Scope, e Event) (a Action, text string, ok bool) {
	if e.Kind != KeyEvent || e.Type == Release || e.IsModifierKey() {
		return None, "", false
	}
	if a := k.Lookup(s, e.Chord()); a != None {
		return a, "", true
	}
	if t := e.Insertable(); t != "" {
		return None, t, true
	}
	return None, "", false
}

// Insertable returns the literal text a key event should type, or "" if the
// event is not a text-producing keypress. Any modifier beyond shift means the
// chord was meant as a command, not as input.
func (e Event) Insertable() string {
	if e.Mods&^ModShift != 0 {
		return ""
	}
	if e.Text != "" {
		return e.Text // KKP flag 16, authoritative when present
	}
	if e.Final != 0 && e.Final != 'u' {
		return ""
	}
	// KKP flag 4: the shifted codepoint arrives as an alternate. Without this,
	// shift+a types "a" because Code always carries the base key.
	if e.Mods&ModShift != 0 && e.Shifted > 32 && e.Shifted < 0xE000 {
		return string(rune(e.Shifted))
	}
	switch {
	case e.Code == 13:
		return "\n"
	case e.Code == 32:
		return " "
	case e.Code > 32 && e.Code != 127 && e.Code < 0xE000:
		return string(rune(e.Code))
	}
	return ""
}
