package keys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docPath is the checked-in reference, relative to this package.
const docPath = "../../docs/KEYBINDINGS.md"

// The reference is generated, so the only thing that can go wrong is the
// checked-in copy falling behind the table. This is the test that makes the
// TODO's "so it cannot drift" true rather than aspirational: change a chord
// without regenerating and the build fails, with the command to fix it.
func TestCheckedInDocMatchesTheTable(t *testing.T) {
	want := Doc()
	got, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("%s is missing: %v\nrun: go run ./cmd/raj --keys > docs/KEYBINDINGS.md", docPath, err)
	}
	if string(got) != want {
		t.Errorf("%s is stale.\nrun: go run ./cmd/raj --keys > docs/KEYBINDINGS.md",
			filepath.Base(docPath))
	}
}

// Every bound chord appears in the reference. A binding documented nowhere is
// one nobody can discover, and the chord is taken from the terminal either way.
func TestEveryBindingIsDocumented(t *testing.T) {
	doc := Doc()
	for _, bd := range Bindings {
		if !strings.Contains(doc, "`"+bd.Chord+"`") {
			t.Errorf("%s (%s) is bound but not in the reference", bd.Action, bd.Chord)
		}
		if !strings.Contains(doc, string(bd.Action)) {
			t.Errorf("action %s is bound but not named in the reference", bd.Action)
		}
	}
	for _, bd := range Reclaim {
		if !strings.Contains(doc, "`"+bd.Chord+"`") {
			t.Errorf("%s is reclaimed but not in the reference", bd.Chord)
		}
	}
}

// An action listed as unimplemented must actually be bound. A stale entry here
// would print "(unimplemented)" against nothing, or silently do nothing at all.
func TestUnimplementedActionsAreBound(t *testing.T) {
	bound := map[Action]bool{}
	for _, bd := range Bindings {
		bound[bd.Action] = true
	}
	for a := range Unimplemented {
		if !bound[a] {
			t.Errorf("%s is listed as unimplemented but is not bound; drop it", a)
		}
	}
}

// The marking has to show up where somebody reading the table would see it.
func TestUnimplementedActionsAreMarked(t *testing.T) {
	doc := Doc()
	for _, line := range strings.Split(doc, "\n") {
		for a := range Unimplemented {
			if strings.HasPrefix(line, "| "+string(a)+" ") &&
				!strings.Contains(line, "(unimplemented)") {
				t.Errorf("%s is listed without its marking: %s", a, line)
			}
		}
	}
}

// Empty cells render as something rather than as nothing, so a blank column
// reads as "none" rather than as a table that failed to render.
func TestEmptyCellsAreVisible(t *testing.T) {
	for _, line := range strings.Split(Doc(), "\n") {
		if strings.Contains(line, "|  |") {
			t.Errorf("an empty cell reached the reference: %s", line)
		}
	}
}
