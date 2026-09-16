package app

import (
	"strings"
	"testing"

	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/ui"
)

// These tests pin the D2b rule for the app's gutter and floating consumers: a
// mark stays in session coordinates and is placed on the display map, so a fold
// above it shifts it down by the hidden rows and a mark inside a fold is not
// drawn at all. The fold is built the way the editor builds one — reject a
// multi-line insertion in Edit mode — so the tests exercise the real projection
// rather than a hand-built one.

// foldedFixture is the one scenario every test below derives from: four lines,
// a rejected insertion of two lines at the top, and a later session line that
// must be placed one display row higher than its session line.
const foldedFixture = "aaa\nbbb\nccc\nddd\n"

// foldedMarkHarness builds the fixture, rejects a two-line "HIDDEN1\nHIDDEN2\n"
// insertion after the first line, and returns the pane and the session line of
// the last document line.
//
// The insertion is proposed and rejected so that Edit mode's
// AcceptedAndProposed policy folds it; a rejected insertion is excluded from
// the composition, so its two session lines collapse to one fold row and every
// line after it is pulled one display row up. A one-line fold would not shift
// anything — one fold row stands where one session line was — which is why the
// fixture hides two.
func foldedMarkHarness(t *testing.T) (*harness, int) {
	t.Helper()
	h := newHarness(t, foldedFixture)
	at := strings.Index(foldedFixture, "bbb")
	id := propose(t, h, piecetable.Hunk{Start: at, End: at, Text: "HIDDEN1\nHIDDEN2\n"})
	if !h.Pane().File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	h.Draw()

	p := h.Pane()
	// The inserted lines are session 1 and 2: both have no display row.
	if got := p.DispOfDocLine(1); got != -1 {
		t.Fatalf("DispOfDocLine(1) = %d, want -1: the fixture did not fold a line", got)
	}
	if got := p.DispOfDocLine(2); got != -1 {
		t.Fatalf("DispOfDocLine(2) = %d, want -1: the fixture did not fold two lines", got)
	}
	// The last line is session 5 after the insertion ("aaa", the two hidden,
	// then bbb/ccc/ddd) and sits on display row 4: the shift the tests follow.
	if got := p.DispOfDocLine(5); got != 4 {
		t.Fatalf("DispOfDocLine(5) = %d, want 4: the fixture did not shift the tail", got)
	}
	return h, 5
}

// editorLayout is the layout the frame was drawn at, so a test can address the
// gutter cell a mark should occupy.
func editorLayout(h *harness) Layout {
	return computeLayout(120, 12, h.App.sidebar, h.App.focus)
}

// gutterCell is the screen cell a display row draws its gutter mark in.
func gutterCell(h *harness, l Layout, row int) ui.Cell {
	y := l.TopY + (row - h.Pane().Viewport.Top)
	return h.host.Last().At(l.EditorX, y)
}

// A diagnostic published on a line below a fold is drawn on that line's display
// row, not its session line: session line 5 lands on display row 4, so the
// naive placement would paint one row low.
func TestDiagnosticMarkFollowsFoldShift(t *testing.T) {
	h, hiddenTail := foldedMarkHarness(t)
	path := h.docPath(h.Pane())
	h.diags.set(path, []lsp.Diagnostic{{Range: lsp.Range{Start: lsp.Position{Line: hiddenTail}}, Severity: sevError}})
	h.Draw()

	l := editorLayout(h)
	if cell := gutterCell(h, l, 4); cell.Rune != 'E' {
		t.Errorf("display row 4 rune = %q, want E: the mark did not follow the fold", cell.Rune)
	}
	// The session line's own row (5) must not hold the mark: placing by session
	// number would put it there.
	if cell := gutterCell(h, l, 5); cell.Rune == 'E' {
		t.Error("display row 5 holds an E: the mark was placed by session line, not display row")
	}
}

// A diagnostic on a line the fold hides is skipped, not clamped onto the fold
// row: the hidden line has no display row of its own.
func TestDiagnosticMarkInsideFoldIsSkipped(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	path := h.docPath(h.Pane())
	h.diags.set(path, []lsp.Diagnostic{{Range: lsp.Range{Start: lsp.Position{Line: 1}}, Severity: sevError}})
	h.Draw()

	l := editorLayout(h)
	// Find the fold row and assert the gutter there is clean. Also assert no
	// E anywhere in the gutter, since a skip must not be a clamp.
	foldRow := -1
	for row := 0; row < h.Pane().DisplayLines(); row++ {
		if _, _, ok := h.Pane().Fold(row); ok {
			foldRow = row
		}
		if cell := gutterCell(h, l, row); cell.Rune == 'E' {
			t.Errorf("display row %d holds an E, want no mark for a hidden line", row)
		}
	}
	if foldRow < 0 {
		t.Fatal("fixture has no fold row")
	}
}

// A pending proposal below a fold draws its green gutter mark on the display
// row, matching PendingMark.DispLine; the review gutter follows the same shift
// as the diagnostics gutter.
func TestProposalMarkFollowsFoldShift(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	off := strings.Index(h.text(), "ddd")
	propose(t, h, piecetable.Hunk{Start: off, End: off + len("ddd"), Text: "DDD"})
	h.Draw()

	marks := h.Pane().PendingMarks()
	if len(marks) != 1 {
		t.Fatalf("pending marks = %d, want 1", len(marks))
	}
	row := marks[0].DispLine(h.Pane())
	if row != 4 {
		t.Fatalf("mark display row = %d, want 4", row)
	}

	l := editorLayout(h)
	if cell := gutterCell(h, l, row); cell.Style.Fg != h.App.theme.ProposedAdd {
		t.Errorf("display row %d fg = %v, want the added colour %v", row, cell.Style.Fg, h.App.theme.ProposedAdd)
	}
	// The naive session row must be clean: a mark placed there is the bug.
	if cell := gutterCell(h, l, marks[0].Line); cell.Style.Fg == h.App.theme.ProposedAdd {
		t.Error("the session row holds the added-colour mark: placement ignored the fold")
	}
}

// With no decisions the map is the identity, so a diagnostic and a proposal
// land on their session lines exactly as they did before the projection.
func TestMarksIdentityWithoutFolds(t *testing.T) {
	h := newHarness(t, foldedFixture)
	off := strings.Index(h.text(), "ddd")
	propose(t, h, piecetable.Hunk{Start: off, End: off + len("ddd"), Text: "DDD"})
	h.diags.set(h.docPath(h.Pane()), []lsp.Diagnostic{
		{Range: lsp.Range{Start: lsp.Position{Line: 2}}, Severity: sevError},
	})
	h.Draw()

	p := h.Pane()
	if p.DisplayLines() != p.File.Lines() {
		t.Fatalf("DisplayLines = %d, want File.Lines = %d with no decisions", p.DisplayLines(), p.File.Lines())
	}
	if got := p.DispOfDocLine(2); got != 2 {
		t.Fatalf("DispOfDocLine(2) = %d, want the identity 2", got)
	}
	l := editorLayout(h)
	if cell := gutterCell(h, l, 2); cell.Rune != 'E' {
		t.Errorf("display row 2 rune = %q, want E on the session line", cell.Rune)
	}
	marks := p.PendingMarks()
	if len(marks) != 1 || marks[0].DispLine(p) != marks[0].Line {
		t.Errorf("marks = %+v, want one on its session line with no fold", marks)
	}
}

// sessionTopFor hands a session-anchored overlay the viewport top in the
// anchor's coordinate, so the overlay's (anchorLine - top) is the display
// delta. With no fold it is the plain viewport top, which is what keeps a
// clean buffer's popup exactly where it was.
func TestSessionTopForIdentityWithoutFolds(t *testing.T) {
	h := newHarness(t, foldedFixture)
	p := h.Pane()
	p.Viewport.Top = 2
	// No projection: the anchor rows and columns are the session ones.
	if got := sessionTopFor(p, 3, 0); got != p.Viewport.Top {
		t.Errorf("sessionTopFor = %d, want the viewport top %d with no fold", got, p.Viewport.Top)
	}
}

// Below a fold the overlay placement rows are display rows: sessionTopFor
// cancels the anchor's session-to-display offset, so (anchorLine - top) is the
// anchor display row minus the viewport top. Session line 5 sits on display row
// 4, and with the viewport top on display row 2 the top must come back as
// session line 3 so the difference is 1.
func TestSessionTopForAdjustsBelowFold(t *testing.T) {
	h, tail := foldedMarkHarness(t)
	p := h.Pane()
	p.Viewport.Top = 2
	got := sessionTopFor(p, tail, 0)
	// anchorRow(tail) = 4, so got = 5 - 4 + 2 = 3.
	if got != 3 {
		t.Fatalf("sessionTopFor = %d, want 3", got)
	}
	anchorRow, _ := p.DispPos(p.File.LineStart(tail) + 0)
	if delta := tail - got; delta != anchorRow-p.Viewport.Top {
		t.Errorf("anchor - top = %d, want the display delta %d", delta, anchorRow-p.Viewport.Top)
	}
}
