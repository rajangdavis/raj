package search

import (
	"testing"

	"raj/internal/keys"
)

// expandAllFixture builds a pane showing three files with two matches each and
// groups them, the state the pane is in once a result has landed. The paths
// named in folded start collapsed and the rest start open, which is what a
// mix of auto-folded and manually opened groups looks like.
func expandAllFixture(t *testing.T, folded ...string) *Pane {
	t.Helper()
	p := NewPane(t.TempDir())
	p.spot = spotResults
	p.Result = Result{
		Files: 3,
		Matches: []Match{
			{Path: "/a.go", Line: 1, Text: "one"},
			{Path: "/a.go", Line: 2, Text: "two"},
			{Path: "/b.go", Line: 3, Text: "three"},
			{Path: "/b.go", Line: 4, Text: "four"},
			{Path: "/c.go", Line: 5, Text: "five"},
			{Path: "/c.go", Line: 6, Text: "six"},
		},
	}
	for _, path := range folded {
		p.collapsed[path] = true
	}
	p.group()
	return p
}

// headerAt returns the current header row for path, failing if the file has no
// visible header.
func headerAt(t *testing.T, p *Pane, path string) Row {
	t.Helper()
	for _, r := range p.Rows() {
		if r.IsHdr && r.Path == path {
			return r
		}
	}
	t.Fatalf("no header row for %s in %+v", path, p.Rows())
	return Row{}
}

// pressExpandAll drives the gesture through Handle, the same entry point the
// application uses, so the test covers the action being claimed by the pane
// and not just the method behind it.
func pressExpandAll(p *Pane) {
	p.Handle(keys.ToggleExpandAll, "")
}

// TestToggleExpandAllExpandsThenCollapses is the core rule: a fully folded
// result set expands on the first press and folds again on the second.
//
// Fails without the change because Handle has no case for the action, so the
// press falls through the switch and every header keeps whatever Open state the
// fixture gave it. The row-count assertions catch the same absence from the
// other side: a gesture that never reaches group cannot add the match rows.
func TestToggleExpandAllExpandsThenCollapses(t *testing.T) {
	p := expandAllFixture(t, "/a.go", "/b.go", "/c.go")
	for _, path := range []string{"/a.go", "/b.go", "/c.go"} {
		if headerAt(t, p, path).Open {
			t.Fatalf("%s starts open, want the fixture folded", path)
		}
	}
	if got, want := len(p.Rows()), 3; got != want {
		t.Fatalf("folded rows = %d, want %d headers only", got, want)
	}

	pressExpandAll(p)
	for _, path := range []string{"/a.go", "/b.go", "/c.go"} {
		if !headerAt(t, p, path).Open {
			t.Errorf("after the first press %s is folded, want every group expanded", path)
		}
	}
	if got, want := len(p.Rows()), 9; got != want {
		t.Fatalf("expanded rows = %d, want %d headers and matches", got, want)
	}

	pressExpandAll(p)
	for _, path := range []string{"/a.go", "/b.go", "/c.go"} {
		if headerAt(t, p, path).Open {
			t.Errorf("after the second press %s is open, want every group folded", path)
		}
	}
	if got, want := len(p.Rows()), 3; got != want {
		t.Fatalf("folded again rows = %d, want %d", got, want)
	}
}

// TestToggleExpandAllWithSomeGroupsOpenStillExpands covers the mixed state: one
// group is already open and two are folded, and the press fills the set in
// rather than flattening it. The rule is "expand unless nothing is folded",
// read off the visible groups.
//
// Fails without the change in two ways, whichever way the gesture is wired.
// With no case at all nothing moves and /b.go stays folded. Wired to the
// per-file toggle instead, the press flips the selected header to folded and
// leaves the rest alone, so /a.go flips to folded and /b.go stays folded.
func TestToggleExpandAllWithSomeGroupsOpenStillExpands(t *testing.T) {
	p := expandAllFixture(t, "/b.go", "/c.go")
	if !headerAt(t, p, "/a.go").Open {
		t.Fatal("/a.go starts folded, want open")
	}
	pressExpandAll(p)
	for _, path := range []string{"/a.go", "/b.go", "/c.go"} {
		if !headerAt(t, p, path).Open {
			t.Errorf("%s is folded after the press, want all expanded", path)
		}
	}
}

// TestToggleExpandAllIgnoresFilteredOutGroups pins "visible" to the current
// result set. A path the filter hid has no rows and no place in Result.Matches,
// but its fold state stays in the map; the gesture must fold and unfold the two
// visible files without touching it.
//
// Fails without the change because the press does nothing, leaving the visible
// files open. It also fails against a naive implementation that drives the
// gesture from p.collapsed rather than the result: /c.go would be counted and
// rewritten, or its hidden fold would make the whole set read as "partly
// folded" and expand the visible files when the test wants them folded.
func TestToggleExpandAllIgnoresFilteredOutGroups(t *testing.T) {
	p := expandAllFixture(t, "/c.go")
	// Drop /c.go as an include glob or a refined query would: it leaves the
	// rows and the visible set, but its fold state remains in the map.
	p.Result.Matches = p.Result.Matches[:4]
	p.Result.Files = 2
	p.group()
	if !p.collapsed["/c.go"] {
		t.Fatal("fixture lost the hidden group's folded state")
	}

	pressExpandAll(p)
	for _, path := range []string{"/a.go", "/b.go"} {
		if headerAt(t, p, path).Open {
			t.Errorf("%s is open after the press, want folded", path)
		}
	}
	if !p.collapsed["/c.go"] {
		t.Error("the hidden group was rewritten by a gesture that cannot see it")
	}

	pressExpandAll(p)
	for _, path := range []string{"/a.go", "/b.go"} {
		if !headerAt(t, p, path).Open {
			t.Errorf("%s is folded after the second press, want expanded", path)
		}
	}
	if !p.collapsed["/c.go"] {
		t.Error("the hidden group's state drifted across a second press")
	}
}

// TestToggleExpandAllKeepsTheSelectionOnItsFile checks that the cursor stays
// with its group. A match that a fold hides falls back to its file header;
// expanding again leaves the cursor on that header rather than jumping to
// another file.
//
// Fails without the reselect step because the fold shrinks the rows underneath
// a fixed Sel index: the cursor that was on a /b.go match is left at index 4,
// which is past the end of the three-header list, and the next key acts on a
// row the user never chose.
func TestToggleExpandAllKeepsTheSelectionOnItsFile(t *testing.T) {
	p := expandAllFixture(t) // everything open
	// Rows are a header, two matches, a header, two matches, ...; index 4 is
	// the first match under /b.go.
	p.list.Sel = 4
	if r := p.Rows()[p.list.Sel]; r.IsHdr || r.Path != "/b.go" {
		t.Fatalf("fixture selection = %+v, want a /b.go match", r)
	}

	pressExpandAll(p) // nothing is folded, so this folds everything
	r := p.Rows()[p.list.Sel]
	if !r.IsHdr || r.Path != "/b.go" {
		t.Errorf("after folding, selection = %+v, want the /b.go header", r)
	}

	pressExpandAll(p) // expanding leaves the header selected
	r = p.Rows()[p.list.Sel]
	if !r.IsHdr || r.Path != "/b.go" {
		t.Errorf("after expanding, selection = %+v, want the /b.go header", r)
	}
}
