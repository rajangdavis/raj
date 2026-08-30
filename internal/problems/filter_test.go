package problems

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/lsp"
)

// mixed is a workspace with problems of both severities in two files, one of
// which is open.
func mixed() *Pane {
	p := loaded(
		File{Path: "/w/open.go", Items: []lsp.Diagnostic{
			diag(1, 1, "undefined: x"),
			diag(4, 2, "unused variable"),
		}},
		File{Path: "/w/closed.go", Items: []lsp.Diagnostic{
			diag(2, 2, "shadowed import"),
		}},
	)
	p.SetOpenPaths([]string{"/w/open.go"})
	return p
}

// counts the problem rows, headings excluded.
func problemRows(p *Pane) int {
	n := 0
	for _, r := range p.Rows() {
		if !r.IsHdr {
			n++
		}
	}
	return n
}

// ---------- severity ----------

func TestErrorsOnlyHidesWarnings(t *testing.T) {
	p := mixed()
	p.flip(spotErrors)

	if got := problemRows(p); got != 1 {
		t.Errorf("%d problems shown, want only the error", got)
	}
	for _, r := range p.Rows() {
		if !r.IsHdr && rank(r.Item.Severity) != 0 {
			t.Errorf("a non-error survived: %q", r.Item.Message)
		}
	}
}

// A file whose problems are all filtered out loses its heading too. A heading
// reading "0E 0W" is a row that says nothing and still costs a line, which on a
// filtered list is most of what you were trying to remove.
func TestFilteredOutFileLosesItsHeading(t *testing.T) {
	p := mixed()
	p.flip(spotErrors)

	if strings.Contains(rowText(p), "closed.go") {
		t.Errorf("a file with nothing left still has a heading:\n%s", rowText(p))
	}
}

// A heading's counts describe what is shown, not what was published — a group
// claiming two warnings while showing none is worse than no counts at all.
func TestHeadingCountsFollowTheFilter(t *testing.T) {
	p := mixed()
	p.flip(spotErrors)

	for _, r := range p.Rows() {
		if r.IsHdr && r.Warnings != 0 {
			t.Errorf("heading for %s claims %d warnings under an errors-only filter",
				r.Path, r.Warnings)
		}
	}
}

// ---------- scope ----------

func TestOpenFilesOnlyHidesClosedOnes(t *testing.T) {
	p := mixed()
	p.flip(spotOpen)

	if strings.Contains(rowText(p), "closed.go") {
		t.Errorf("a closed file survived the filter:\n%s", rowText(p))
	}
	if !strings.Contains(rowText(p), "open.go") {
		t.Errorf("the open file was filtered out too:\n%s", rowText(p))
	}
}

// The two compose rather than overriding each other.
func TestFiltersCompose(t *testing.T) {
	p := mixed()
	p.flip(spotErrors)
	p.flip(spotOpen)

	if got := problemRows(p); got != 1 {
		t.Errorf("%d problems shown, want the one error in the one open file", got)
	}
}

// Opening a file changes what the scope filter shows, without anything having
// to re-publish diagnostics.
func TestOpeningAFileWidensTheScope(t *testing.T) {
	p := mixed()
	p.flip(spotOpen)
	before := problemRows(p)

	p.SetOpenPaths([]string{"/w/open.go", "/w/closed.go"})
	if got := problemRows(p); got <= before {
		t.Errorf("%d -> %d problems; opening a file should reveal its problems", before, got)
	}
}

// Handing over the same set again must not rebuild: it happens on every
// diagnostic refresh, and rebuilding sorts the workspace and resets nothing
// visible.
func TestSameOpenPathsDoesNotDirty(t *testing.T) {
	p := mixed()
	p.flip(spotOpen)
	p.Rows() // settle
	p.list.Sel = 1

	p.SetOpenPaths([]string{"/w/open.go"})
	if p.dirty {
		t.Error("an unchanged set of open paths marked the rows dirty")
	}
	if p.list.Sel != 1 {
		t.Errorf("the selection moved to %d", p.list.Sel)
	}
}

// ---------- what the pane says when empty ----------

// "no problems" under an active filter is a lie: the workspace may be full of
// them.
func TestEmptyUnderAFilterSaysWhy(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 2, "unused")}})
	p.flip(spotErrors)

	if got := frame(p, 40, 8); !strings.Contains(got, "match the filter") {
		t.Errorf("an empty filtered list did not say why:\n%s", got)
	}
}

func TestEmptyWithNoProblemsSaysSo(t *testing.T) {
	p := New()
	got := frame(p, 40, 8)
	if !strings.Contains(got, "no problems") || strings.Contains(got, "filter") {
		t.Errorf("an empty pane should say plainly that there is nothing:\n%s", got)
	}
}

// ---------- focus and keys ----------

// The toggles come before the list, so tab order and reading order agree.
func TestTabReachesTheTogglesThenLeaves(t *testing.T) {
	p := mixed()
	p.spot = spotErrors

	if _, _, exit := p.Handle(keys.CycleFocus); exit {
		t.Fatal("tab left the pane from the first toggle")
	}
	if p.spot != spotOpen {
		t.Errorf("spot = %d, want the second toggle", p.spot)
	}
	p.Handle(keys.CycleFocus)
	if p.spot != spotList {
		t.Errorf("spot = %d, want the list", p.spot)
	}
}

// Shift-tab off the first toggle leaves the pane, rather than wrapping to the
// far end of it.
func TestShiftTabOffTheFirstToggleLeaves(t *testing.T) {
	p := mixed()
	p.spot = spotErrors
	if _, _, exit := p.Handle(keys.CycleFocusBack); !exit {
		t.Error("shift+tab did not leave the pane")
	}
}

// On a toggle, up and down are how you get back to the list. A list reading them
// as "previous row" would trap focus on the filters.
func TestArrowsMoveBetweenTogglesAndList(t *testing.T) {
	p := mixed()
	p.spot = spotErrors
	p.Handle(keys.LineDown)
	p.Handle(keys.LineDown)
	if p.spot != spotList {
		t.Errorf("spot = %d, want the list after two downs", p.spot)
	}
}

// Enter on a toggle flips it rather than opening whatever the list had
// selected.
func TestEnterOnAToggleFlipsIt(t *testing.T) {
	p := mixed()
	p.spot = spotErrors
	path, _, _ := p.Handle(keys.Confirm)
	if path != "" {
		t.Errorf("enter on a toggle opened %q", path)
	}
	if errs, _ := p.Filters(); !errs {
		t.Error("enter did not flip the filter")
	}
}

// A filter change lands at the top of the new list. Set goes to trouble to keep
// the selection by identity because diagnostics move under the cursor while you
// read; a filter change is the opposite case — you asked for a different list.
func TestFilterChangeResetsTheSelection(t *testing.T) {
	p := mixed()
	p.Rows()
	p.list.Sel = 2
	p.flip(spotErrors)
	if p.list.Sel != 0 {
		t.Errorf("selection = %d, want the top of the new list", p.list.Sel)
	}
}

// ---------- the pointer ----------

// Clicking a checkbox flips that filter and not its neighbour: they sit next to
// each other on one row.
func TestClickingAToggleFlipsOnlyIt(t *testing.T) {
	p := mixed()
	col := strings.Index(rowN(p, 40, 8, 1), "[ ] open files")
	if col < 0 {
		t.Fatalf("the toggles are not drawn:\n%s", frame(p, 40, 8))
	}
	p.ClickAt(col+1, 1, 40, 8)

	errs, open := p.Filters()
	if !open {
		t.Error("clicking the open-files box did not flip it")
	}
	if errs {
		t.Error("clicking one box flipped its neighbour")
	}
}

// The list is hit-tested from below the filter row, not from below the heading:
// adding a row must not have shifted every click by one.
func TestClickingARowStillOpensIt(t *testing.T) {
	p := mixed()
	want := p.Rows()[1] // the first problem, under the first heading
	path, line, ok := p.ClickAt(0, headRows+1, 40, 8)
	if !ok {
		t.Fatal("the click landed on nothing")
	}
	if path != want.Path || line != want.Item.Range.Start.Line+1 {
		t.Errorf("opened %s:%d, want %s:%d", path, line,
			want.Path, want.Item.Range.Start.Line+1)
	}
}

// rowText is every row of a rendered pane, joined.
func rowText(p *Pane) string { return frame(p, 60, 12) }

// rowN is one rendered row.
func rowN(p *Pane, w, h, y int) string {
	return strings.Split(strings.TrimRight(frame(p, w, h), "\n"), "\n")[y]
}
