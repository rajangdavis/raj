package keys

import (
	"sort"
	"strings"
)

// The keybinding reference, generated from Bindings so it cannot drift.
//
// A hand-written key table is wrong within a release: chords move when a
// terminal turns out to claim one, and the documentation is the thing nobody
// remembers to update. Generating it means the only way to change what is
// documented is to change what is bound.
//
// Written to KEYBINDINGS.md and checked in, because a reference you have to
// build the program to read is not a reference. A test asserts the file matches
// what Doc() produces, so the checked-in copy cannot fall behind either.

// Unimplemented names actions that have a chord but no behaviour behind them.
//
// They are documented as such rather than omitted, because the chord is taken
// from the terminal either way: an undocumented binding that does nothing is
// indistinguishable from a broken one, and someone will spend an evening
// working out which.
//
// This is the one hand-maintained part of the reference. There is a test in
// internal/app that presses each of these and asserts the application still
// reports it as unhandled, so implementing one without unlisting it fails the
// build.
var Unimplemented = map[Action]string{
	CommandPalette: "no palette yet; the file picker is cmd+p",
	// Found by the test rather than by anyone noticing. cmd+u has been taking
	// a chord from the terminal and doing nothing, and TODO.md listed only the
	// two above — which is the whole argument for generating this from the
	// table and checking it against the running application.
	CursorUndo: "cursor history is not recorded yet",
}

// Doc renders the keybinding reference as markdown.
func Doc() string {
	var b strings.Builder
	b.WriteString("# raj keybindings\n\n")
	b.WriteString("Generated from `internal/keys/table.go`. Do not edit by hand:\n")
	b.WriteString("run `raj --keys > KEYBINDINGS.md`, or just change the table and\n")
	b.WriteString("let the test tell you.\n\n")
	b.WriteString("Chords are written as raj resolves them. The macOS and Linux columns\n")
	b.WriteString("are what the terminal has to be configured to send — see\n")
	b.WriteString("`raj --config ghostty` and `raj --config iterm2`.\n\n")
	b.WriteString("Actions marked **(unimplemented)** are bound but do nothing yet. The\n")
	b.WriteString("chord is still claimed from the terminal, which is why they are listed\n")
	b.WriteString("rather than hidden.\n")

	for _, g := range groups() {
		b.WriteString("\n## " + g + "\n\n")
		b.WriteString("| action | chord | macOS | Linux | notes |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, bd := range Bindings {
			if bd.Group != g {
				continue
			}
			b.WriteString(row(bd))
		}
	}

	// Chords raj deliberately does not take are worth a section of their own:
	// "why does cmd+1 not switch raj tabs" is a real question with a real
	// answer, and the answer lives in the table already.
	if len(Reclaim) > 0 {
		b.WriteString("\n## Handed back to the terminal\n\n")
		b.WriteString("These are configured so the terminal keeps its own behaviour\n")
		b.WriteString("rather than sending the chord to raj.\n\n")
		b.WriteString("| chord | macOS | Linux | notes |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, bd := range Reclaim {
			b.WriteString("| `" + bd.Chord + "` | " + cell(bd.Mac) + " | " +
				cell(bd.Linux) + " | " + cell(bd.Note) + " |\n")
		}
	}
	return b.String()
}

func row(bd Binding) string {
	action := string(bd.Action)
	note := bd.Note
	if why, ok := Unimplemented[bd.Action]; ok {
		action += " **(unimplemented)**"
		if note != "" {
			note += "; "
		}
		note += why
	}
	return "| " + action + " | `" + bd.Chord + "` | " + cell(bd.Mac) + " | " +
		cell(bd.Linux) + " | " + cell(note) + " |\n"
}

// cell renders an empty value as an em dash, so a blank column reads as
// "nothing here" rather than as a table that failed to render.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// groups is every group in Bindings, in the order they first appear.
//
// Source order rather than alphabetical: the table is grouped the way somebody
// reading it would want it grouped, and sorting would scatter that.
func groups() []string {
	var out []string
	seen := map[string]bool{}
	for _, bd := range Bindings {
		if !seen[bd.Group] {
			seen[bd.Group] = true
			out = append(out, bd.Group)
		}
	}
	return out
}

// ActionsByName is every action that appears in Bindings, sorted. Exported for
// tests that need to walk the bound set.
func ActionsByName() []Action {
	seen := map[Action]bool{}
	var out []Action
	for _, bd := range Bindings {
		if !seen[bd.Action] {
			seen[bd.Action] = true
			out = append(out, bd.Action)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
