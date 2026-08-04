package probe

import (
	"strings"
	"testing"

	"raj/internal/keys"
)

// The probe walks every binding, so a chord added to the table appears in the
// checklist without anyone remembering to add it.
func TestChecklistCoversEveryBinding(t *testing.T) {
	c := newChecklist()
	if len(c.items) != len(keys.Bindings) {
		t.Errorf("checklist has %d items for %d bindings", len(c.items), len(keys.Bindings))
	}
	seen := map[keys.Action]bool{}
	for _, it := range c.items {
		seen[it.Action] = true
	}
	for _, b := range keys.Bindings {
		if !seen[b.Action] {
			t.Errorf("%s missing from the checklist", b.Action)
		}
	}
}

// Only the expected chord advances the list. One stray key must not consume a
// prompt and shift every remaining item.
func TestChecklistAdvancesOnlyOnMatch(t *testing.T) {
	c := newChecklist()
	want := c.items[0]

	stray, _ := keys.Parse([]byte("\x1b[97;9u")) // super+a, almost certainly not first
	if want.Seq == "97;9u" {
		t.Skip("first binding is the stray we chose")
	}
	c.record(stray)
	if c.idx != 0 {
		t.Fatalf("a stray chord advanced the list to %d", c.idx)
	}

	match, _ := keys.Parse([]byte("\x1b[" + want.Seq))
	c.record(match)
	if c.idx != 1 {
		t.Errorf("the expected chord did not advance the list")
	}
}

// The report names what never arrived, with the last thing seen instead, since
// that is what identifies a terminal swallowing a chord.
func TestReportNamesMissingChords(t *testing.T) {
	c := newChecklist()
	c.lastStray = `\e[119;9u`
	c.skip()
	out := c.report()
	if !strings.Contains(out, "NOT RECEIVED") || !strings.Contains(out, "last stray") {
		t.Errorf("report does not explain the miss:\n%s", out)
	}
}

// Nothing in the probe should name a particular terminal: it measures whatever
// it is run under.
func TestProbeIsTerminalAgnostic(t *testing.T) {
	c := newChecklist()
	c.skip()
	for _, name := range []string{"Ghostty", "ghostty", "iTerm", "kitty"} {
		if strings.Contains(c.report(), name) {
			t.Errorf("the report names %s", name)
		}
	}
}
