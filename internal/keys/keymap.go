package keys

import "strings"

// Scope is which pane has focus. The same chord can mean different things per
// pane — tab indents a selection in the editor but cycles focus everywhere
// else — so resolution is scoped first, global second.
type Scope int

const (
	Global Scope = iota
	Editor
	Explorer
	Search
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
	// Scope overrides are focus-dependent rather than platform-dependent, so
	// they cannot live in the terminal config. The table is shared with
	// CtrlAliases, which derives their ctrl aliases from the same list, so an
	// alias cannot drift from the binding it mirrors.
	for _, o := range scopeOverrides {
		k.Bind(o.Scope, o.Chord, o.Action)
	}
	return k
}

// scopeOverride is one chord whose meaning depends on which pane has focus.
// NewKeymap binds them and CtrlAliases mirrors them, so there is exactly one
// source for what a scope override is.
type scopeOverride struct {
	Scope  Scope
	Chord  string
	Action Action
}

var scopeOverrides = []scopeOverride{
	// Tab indents in the editor and cycles focus everywhere else. This is what
	// makes the sidebar focus ring one-way: tab can walk out of a sidebar into
	// the editor, but once there it is indentation, so shift+tab cannot walk
	// back. Returning to a sidebar is always a chord.
	{Editor, "tab", Indent},
	{Editor, "shift+tab", Outdent},
	{Editor, "enter", None}, // the editor inserts a newline, not "confirm"

	// The picker is a modal overlay: enter chooses, escape dismisses, and tab
	// has nowhere to go.
	{Picker, "tab", None},
	{Picker, "shift+tab", None},

	// A prompt is modal in the strongest sense: it is the only thing that can
	// answer itself, so tab cannot cycle focus and enter must mean "confirm"
	// even though the editor underneath would insert a newline.
	//
	// Tab means Indent rather than None, because a one-line field has nothing
	// to indent and the prompt uses it to complete instead — which is what
	// turns save-as from a text box into a file dialog. Bound to None it
	// resolved to nothing at all and the keystroke was simply lost.
	{Prompt, "tab", Indent},
	{Prompt, "shift+tab", None},

	// cmd+return expands every folder under the explorer selection, and every
	// file group in the search results. A scope override shadows the global
	// LineBelow only while one of those panes has focus, so the editor keeps
	// its line-below.
	{Explorer, "super+enter", ToggleExpandAll},
	{Search, "super+enter", ToggleExpandAll},
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

// ctrlAlias is the ctrl replacement for a chord that uses super and no ctrl.
// It reports false when the chord has no simple ctrl form: a bare key, a chord
// already carrying ctrl (ctrl+super+m would collapse to ctrl+m, which is
// enter), or an unknown modifier. Modifiers are rebuilt in the canonical
// shift, ctrl, alt order Chord uses, so the alias is the string the decoder
// produces for a real ctrl keypress rather than a hand-spelled approximation.
func ctrlAlias(chord string) (string, bool) {
	parts := strings.Split(chord, "+")
	if len(parts) < 2 {
		return "", false
	}
	key := parts[len(parts)-1]
	var shift, ctrl, alt, super, other bool
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "shift":
			shift = true
		case "ctrl":
			ctrl = true
		case "alt":
			alt = true
		case "super":
			super = true
		default:
			other = true
		}
	}
	if !super || ctrl || other {
		return "", false
	}
	var mods []string
	if shift {
		mods = append(mods, "shift")
	}
	mods = append(mods, "ctrl")
	if alt {
		mods = append(mods, "alt")
	}
	return strings.Join(append(mods, key), "+"), true
}

// CtrlAliases adds a ctrl+<key> alias for every super+<key> binding, so a
// terminal that cannot send super can still reach the action. An alias is
// added only where its chord is free in the same scope; an existing ctrl
// binding is never overwritten and its super chord is returned as a collision
// for the caller to report. Chords that already carry ctrl are not candidates:
// their ctrl form would not be a new chord.
//
// The sources are walked in table order, not map order, so which of two
// colliding chords wins is deterministic. It returns how many aliases were
// added and the super chords that had no free ctrl alias.
func (k *Keymap) CtrlAliases() (added int, collisions []string) {
	add := func(s Scope, chord string, action Action) {
		alias, ok := ctrlAlias(chord)
		if !ok || action == None {
			return
		}
		if s == Global {
			if _, taken := k.global[alias]; taken {
				collisions = append(collisions, chord)
				return
			}
			k.global[alias] = action
			added++
			return
		}
		m := k.scoped[s]
		if m == nil {
			m = map[string]Action{}
			k.scoped[s] = m
		}
		if _, taken := m[alias]; taken {
			collisions = append(collisions, chord)
			return
		}
		m[alias] = action
		added++
	}
	for _, b := range Bindings {
		add(Global, b.Chord, b.Action)
	}
	for _, b := range Reclaim {
		add(Global, b.Chord, b.Action)
	}
	for _, o := range scopeOverrides {
		add(o.Scope, o.Chord, o.Action)
	}
	return added, collisions
}
