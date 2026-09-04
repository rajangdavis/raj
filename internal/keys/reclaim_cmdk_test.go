package keys

import "testing"

// cmd+k is bound to take it AWAY from the terminal, not because the editor
// needed another chord.
//
// Unbound, Ghostty and iTerm2 both clear the scrollback with it: the shell
// history above raj disappears, which raj cannot redraw and the user cannot get
// back. The damage is invisible from inside the editor, so nothing in the
// application can notice this regressing — only the table can, and only if
// something asserts on it.
func TestCmdKIsClaimedFromTheTerminal(t *testing.T) {
	var found *Binding
	for i, b := range Emitted() {
		if b.Chord == "super+k" {
			found = &Emitted()[i]
		}
	}
	if found == nil {
		t.Fatal("super+k is not in the emitted table, so the terminal keeps cmd+k and clears the scrollback")
	}
	if found.Mac != "cmd+k" {
		t.Errorf("mac trigger = %q, want cmd+k", found.Mac)
	}
	if found.Action == "" || Unimplemented[found.Action] != "" {
		t.Errorf("cmd+k resolves to %q, which is unimplemented: a claimed dead chord "+
			"takes the key from the terminal and gives nothing back", found.Action)
	}
	// It must actually resolve in the editor, which is where the scrollback
	// mistake gets made.
	if a, _, ok := NewKeymap().Resolve(Editor, mustParse(t, "\x1b[107;9u")); !ok || a != DeleteToLineEnd {
		t.Errorf("editor resolves cmd+k to (%q, %v), want delete_to_line_end", a, ok)
	}
}

// The agent pane was dropped rather than left bound-and-unimplemented, so its
// chord goes back to the terminal instead of being taken for nothing.
func TestAgentChordIsNotClaimed(t *testing.T) {
	for _, b := range Emitted() {
		if b.Mac == "cmd+alt+b" {
			t.Errorf("cmd+alt+b is still claimed for %q", b.Action)
		}
	}
	for a := range Unimplemented {
		if a == "toggle_agent" {
			t.Error("toggle_agent is still listed as unimplemented; it was removed")
		}
	}
}
