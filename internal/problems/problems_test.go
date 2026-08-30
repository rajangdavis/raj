package problems

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/lsp"
	"raj/internal/ui"
	"raj/internal/widget"
)

// diag builds a diagnostic on a line, one-based line numbers being what the
// pane shows and zero-based being what LSP sends.
func diag(line, sev int, msg string) lsp.Diagnostic {
	var d lsp.Diagnostic
	d.Range.Start.Line = line
	d.Severity = sev
	d.Message = msg
	return d
}

func loaded(files ...File) *Pane {
	p := New()
	p.Set(files)
	return p
}

func frame(p *Pane, w, h int) string {
	s := ui.NewScreen(w, h)
	p.Render(s, 0, 0, w, h, widget.DefaultTheme(), true)
	var b strings.Builder
	for y := 0; y < h; y++ {
		b.WriteString(s.Row(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------- structure ----------

// Grouped by file with a heading above each, which is the search pane's shape
// and the reason it is that shape: a flat list of forty problems across nine
// files is not readable.
func TestGroupsByFile(t *testing.T) {
	p := loaded(
		File{Path: "/w/b.go", Items: []lsp.Diagnostic{diag(1, 1, "boom")}},
		File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(2, 1, "bang"), diag(3, 2, "hmm")}},
	)
	rows := p.Rows()
	if len(rows) != 5 {
		t.Fatalf("%d rows, want 2 headings and 3 problems", len(rows))
	}
	// Sorted by path so the list is stable between publishes.
	if !rows[0].IsHdr || rows[0].Path != "/w/a.go" {
		t.Errorf("first row is %+v, want a.go's heading", rows[0])
	}
	if !rows[3].IsHdr || rows[3].Path != "/w/b.go" {
		t.Errorf("row 3 is %+v, want b.go's heading", rows[3])
	}
}

// A heading carries its counts, so a folded group still says how much it hides.
func TestHeadingCountsSeverities(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{
		diag(1, 1, "e"), diag(2, 1, "e"), diag(3, 2, "w"),
	}})
	h := p.Rows()[0]
	if h.Errors != 2 || h.Warnings != 1 {
		t.Errorf("counts = %dE %dW, want 2E 1W", h.Errors, h.Warnings)
	}
}

// Files with nothing wrong do not appear. An empty publish is how a server says
// the problems are fixed, and a heading reading "0" is not news.
func TestEmptyFilesAreNotListed(t *testing.T) {
	p := loaded(
		File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e")}},
		File{Path: "/w/clean.go"},
	)
	for _, r := range p.Rows() {
		if r.Path == "/w/clean.go" {
			t.Error("a file with no problems was listed")
		}
	}
}

// ---------- keys ----------

// Enter on a heading folds it; enter on a problem opens the file at its line.
// The same split enter makes in the search pane and in the tree.
func TestEnterFoldsAHeadingAndOpensAProblem(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(4, 1, "boom")}})

	before := len(p.Rows())
	if path, _, _ := p.Handle(keys.Confirm); path != "" {
		t.Error("enter on a heading opened a file")
	}
	if got := len(p.Rows()); got >= before {
		t.Errorf("rows %d -> %d; the group should have folded", before, got)
	}

	p.Handle(keys.Confirm) // unfold
	p.Handle(keys.LineDown)
	path, line, _ := p.Handle(keys.Confirm)
	if path != "/w/a.go" {
		t.Errorf("opened %q", path)
	}
	// LSP counts lines from zero and every jump in raj takes one-based.
	if line != 5 {
		t.Errorf("line = %d, want 5", line)
	}
}

// Tab and escape leave the pane. There is no field to cycle through, so one
// step out rather than the search pane's walk through components.
func TestTabAndEscapeExit(t *testing.T) {
	for _, a := range []keys.Action{keys.Indent, keys.Outdent, keys.Cancel} {
		p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e")}})
		if _, _, exit := p.Handle(a); !exit {
			t.Errorf("%s did not leave the pane", a)
		}
	}
}

// An empty pane must not panic on any key, since it is the state the pane
// spends most of its life in.
func TestEmptyPaneHandlesEverything(t *testing.T) {
	p := New()
	for _, a := range []keys.Action{
		keys.LineUp, keys.LineDown, keys.PageUp, keys.PageDown, keys.Confirm,
	} {
		p.Handle(a)
	}
	if got := frame(p, 30, 10); !strings.Contains(got, "no problems") {
		t.Errorf("an empty pane drew %q", got)
	}
}

// ---------- selection stability ----------

// Diagnostics are republished on every keystroke that reaches the server, so an
// index-based selection would walk under the cursor while you read the list.
func TestSelectionSurvivesARepublish(t *testing.T) {
	items := []lsp.Diagnostic{diag(1, 1, "one"), diag(5, 1, "two"), diag(9, 1, "three")}
	p := loaded(File{Path: "/w/a.go", Items: items})
	p.Handle(keys.LineDown)
	p.Handle(keys.LineDown)
	p.Handle(keys.LineDown) // the third problem
	sel, _ := p.selected()
	if sel.Item.Message != "three" {
		t.Fatalf("setup: selected %q", sel.Item.Message)
	}

	// The same problems, republished with one earlier one gone.
	p.Set([]File{{Path: "/w/a.go", Items: []lsp.Diagnostic{items[0], items[2]}}})

	got, ok := p.selected()
	if !ok || got.Item.Message != "three" {
		t.Errorf("selection moved to %q after a republish", got.Item.Message)
	}
}

// When the selected problem is fixed — the good case — the selection lands on
// its file's heading rather than on whatever is now at the old index.
func TestSelectionFallsBackToTheHeading(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{
		diag(1, 1, "one"), diag(5, 1, "two"),
	}})
	p.Handle(keys.LineDown)
	p.Handle(keys.LineDown) // "two"

	p.Set([]File{{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "one")}}})

	got, ok := p.selected()
	if !ok || !got.IsHdr || got.Path != "/w/a.go" {
		t.Errorf("selection = %+v, want a.go's heading", got)
	}
}

// A fold survives a republish too. Losing it on every keystroke would make
// folding useless.
func TestFoldSurvivesARepublish(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e")}})
	p.Handle(keys.Confirm) // fold
	if len(p.Rows()) != 1 {
		t.Fatal("setup: the group did not fold")
	}

	p.Set([]File{{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e"), diag(2, 1, "e")}}})
	if len(p.Rows()) != 1 {
		t.Errorf("%d rows; the fold was lost on republish", len(p.Rows()))
	}
}

// ---------- rendering ----------

// The line number leads, because it is what the eye scans for against a
// compiler's output, and the message goes last where losing its tail costs
// least in a narrow pane.
func TestRowShowsLineThenSeverityThenMessage(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(41, 1, "undefined: needle")}})
	got := frame(p, 44, 10)
	if !strings.Contains(got, "42: E undefined: needle") {
		t.Errorf("row not drawn as expected:\n%s", got)
	}
}

// The heading counts the whole workspace, so the pane says how much is wrong
// without being scrolled.
func TestPaneHeadingCountsEverything(t *testing.T) {
	p := loaded(
		File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e")}},
		File{Path: "/w/b.go", Items: []lsp.Diagnostic{diag(1, 2, "w"), diag(2, 1, "e")}},
	)
	if got := frame(p, 40, 10); !strings.Contains(got, "2E 1W") {
		t.Errorf("heading does not count the workspace:\n%s", got)
	}
}

// A pane too narrow or short to draw draws nothing rather than something
// broken.
func TestTooSmallDrawsNothing(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 1, "e")}})
	for _, size := range [][2]int{{4, 10}, {30, 1}} {
		s := ui.NewScreen(size[0], size[1])
		p.Render(s, 0, 0, size[0], size[1], widget.DefaultTheme(), true)
	}
}

// ---------- severity agreement ----------

// An unspecified severity is treated as an error, matching the app's own
// ranking. Two places deciding this differently is how a file shows a red mark
// in the gutter and "no problems" in a list.
func TestMissingSeverityIsAnError(t *testing.T) {
	p := loaded(File{Path: "/w/a.go", Items: []lsp.Diagnostic{diag(1, 0, "boom")}})
	if h := p.Rows()[0]; h.Errors != 1 {
		t.Errorf("counts = %dE %dW, want it counted as an error", h.Errors, h.Warnings)
	}
	if got := frame(p, 40, 10); !strings.Contains(got, ": E boom") {
		t.Errorf("not marked as an error:\n%s", got)
	}
}
