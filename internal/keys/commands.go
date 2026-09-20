package keys

import "strings"

// Command is one action the keymap can run, named for the command palette.
//
// Name is the action's identifier with its underscores opened into words —
// toggle_sidebar becomes "toggle sidebar" — so the palette shows a human label
// without a second hand-maintained table to drift from the first. Chord is the
// chord the action resolves to, taken from the same tables NewKeymap builds
// from; a chordless command — one listed in Unbound — carries an empty Chord.
type Command struct {
	Action Action
	Name   string
	Chord  string
}

// Commands lists every action a palette can run: the ones bound in Bindings,
// Reclaim or Natives in binding order, then the chordless ones in Unbound.
//
// For the bound actions it reads the tables rather than carrying a list of its
// own: the palette can neither offer a chord the keymap does not have nor miss
// one it does, which is what keeps the two from drifting. Actions that appear
// in both tables are listed once, with their chords joined.
//
// Unbound adds only commands that have no chord by design — an action worth
// having in the palette but not worth spending a chord on. It is the one
// hand-maintained list this function reads, and it may hold nothing else: not
// a bound action (its chord would be hidden) and not an unimplemented one (the
// palette is a list of things to run). A chordless entry's Chord is "".
//
// Actions bound only as a scope override — Indent and Outdent on tab and
// shift+tab — are absent: their chord depends on the focused pane, so there is
// no single chord to show. Their own chords still reach them.
func Commands() []Command {
	var out []Command
	index := map[Action]int{}
	add := func(a Action, chord string) {
		if a == None {
			return
		}
		if i, ok := index[a]; ok {
			out[i].Chord += ", " + chord
			return
		}
		index[a] = len(out)
		out = append(out, Command{Action: a, Name: nameOf(a), Chord: chord})
	}
	for _, b := range Bindings {
		add(b.Action, b.Chord)
	}
	// Reclaimed chords are bound too: the keymap resolves them exactly as it
	// resolves a Bindings entry, so leaving them out would be the drift this
	// list exists to prevent.
	for _, b := range Reclaim {
		add(b.Action, b.Chord)
	}
	for _, n := range Natives {
		add(n.Action, n.Chord)
	}
	// Chordless commands come last rather than interleaved: the bound entries
	// then stay in keymap order, which is the order the reference reads in, and
	// the handful without a chord do not interrupt it. An action already listed
	// as bound keeps its bound entry, so a misplaced Unbound entry cannot hide
	// a chord.
	for _, a := range Unbound {
		if a == None {
			continue
		}
		if _, ok := index[a]; ok {
			continue
		}
		index[a] = len(out)
		out = append(out, Command{Action: a, Name: nameOf(a), Chord: ""})
	}
	return out
}

// Unbound lists implemented actions that appear in the palette with no chord.
//
// A chord is a scarce resource the terminal also wants, and a command can be
// worth having in the palette without being worth one: copy_relative_path is
// the first. Commands derives a label for each entry from its action and
// leaves Chord empty, so the palette shows the name alone.
//
// Every action here must be implemented — internal/app's
// TestEveryUnboundActionIsHandled holds that end — and must not also be bound;
// TestUnboundActionsAreListedChordless holds that end.
var Unbound = []Action{
	CopyRelPath,
	Settings,
}

// nameOf opens an action's underscores into the words the palette shows. The
// identifier is the only name an action has, so deriving the label from it is
// the one spelling that cannot fall out of step with the dispatch.
func nameOf(a Action) string {
	return strings.ReplaceAll(string(a), "_", " ")
}
