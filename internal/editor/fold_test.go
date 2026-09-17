package editor

import (
	"strings"
	"testing"

	"raj/internal/piecetable"
	"raj/internal/ui"
	"raj/internal/view"
)

// foldOf builds a Fold the way the app does: the header line is StartLine and
// the hidden run covers StartLine+1 through EndLine.
func foldOf(t *testing.T, p *Pane, startLine, endLine int) Fold {
	t.Helper()
	if startLine < 0 || endLine >= p.File.Lines() || startLine >= endLine {
		t.Fatalf("bad fold lines %d..%d over %d lines", startLine, endLine, p.File.Lines())
	}
	hi := p.File.Len()
	if endLine+1 < p.File.Lines() {
		hi = p.File.LineStart(endLine + 1)
	}
	return Fold{StartLine: startLine, EndLine: endLine, Lo: p.File.LineStart(startLine + 1), Hi: hi}
}

// A closed reader fold hides the whole display rows of the lines it covers and
// stands in with one fold row, so the composition is shorter and the map
// answers -1 for a hidden line. Without the fold reaching the projection the
// file renders every line and DispOfDocLine is the identity.
func TestUserFoldHidesInnerLines(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	f := foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.SetDisplay(piecetable.AcceptedAndProposed)

	if got := p.DisplayLines(); got != 4 {
		t.Fatalf("DisplayLines = %d, want 4 (a, the fold, e, the empty line after the newline)", got)
	}
	if got := p.RowText(0); got != "a" {
		t.Errorf("RowText(0) = %q, want a", got)
	}
	if got := p.RowText(2); got != "e" {
		t.Errorf("RowText(2) = %q, want e", got)
	}
	for _, ln := range []int{1, 2, 3} {
		if got := p.DispOfDocLine(ln); got != -1 {
			t.Errorf("DispOfDocLine(%d) = %d, want -1 for a folded line", ln, got)
		}
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	if got := p.DispOfDocLine(4); got != 2 {
		t.Errorf("DispOfDocLine(4) = %d, want 2 after the fold", got)
	}
	group, hidden, ok := p.Fold(1)
	if !ok || group != view.UserFold || hidden != 6 {
		t.Fatalf("Fold(1) = (%d,%d,%v), want (UserFold,6,true)", group, hidden, ok)
	}
}

// A click or caret on the fold row follows the hidden-run convention: DocAt
// yields the run's session start and every hidden offset maps to the fold row
// at column zero. A motion into the hidden run snaps to an edge rather than
// parking inside text the display does not draw.
func TestUserFoldRowFollowsHiddenRunConvention(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	f := foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.SetDisplay(piecetable.AcceptedAndProposed)

	lo, hi := f.Lo, f.Hi
	if got := p.DocAt(1, 0); got != lo {
		t.Errorf("DocAt(fold row) = %d, want the hidden run start %d", got, lo)
	}
	if got := p.dispLineOf(lo); got != 1 {
		t.Errorf("dispLineOf(hidden start) = %d, want the fold row 1", got)
	}
	if got := p.snapOut(lo+1, -1); got != lo {
		t.Errorf("snapOut into the fold backwards = %d, want %d", got, lo)
	}
	if got := p.snapOut(hi-1, 1); got != hi {
		t.Errorf("snapOut into the fold forwards = %d, want %d", got, hi)
	}
	// The round trip the renderer relies on: the cell the caret is drawn on
	// maps back to the same offset.
	if got := p.DocAt(p.dispLineOf(lo), 0); got != lo {
		t.Errorf("round trip on the fold = %d, want %d", got, lo)
	}
}

// The toggle chord flips the range containing the caret: the header line
// closes, a line inside the hidden body opens. Without the toggle the list is
// inert and both calls leave the display unchanged.
func TestToggleFoldClosesAndOpens(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	p.SetFolds([]Fold{foldOf(t, p, 0, 3)}, int(p.File.Session().Version()))
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != 6 {
		t.Fatalf("setup: DisplayLines = %d, want 6 with the fold open", got)
	}

	got, ok := p.ToggleFoldAt(0)
	if !ok || !got.Closed {
		t.Fatalf("ToggleFoldAt(header) = %+v,%v, want a closed fold", got, ok)
	}
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != 4 {
		t.Errorf("DisplayLines after closing = %d, want 4", got)
	}

	// The caret on the fold row is inside the body, so the same chord opens it.
	got, ok = p.ToggleFoldAt(2)
	if !ok || got.Closed {
		t.Fatalf("ToggleFoldAt(body) = %+v,%v, want it open", got, ok)
	}
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != 6 {
		t.Errorf("DisplayLines after opening = %d, want 6", got)
	}
}

// The innermost range wins when folds nest, so the chord toggles the smaller
// fold around the caret rather than the outer one.
func TestToggleFoldPicksInnermost(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\nf\ng\n")
	outer := foldOf(t, p, 1, 5)
	inner := foldOf(t, p, 2, 3)
	p.SetFolds([]Fold{outer, inner}, int(p.File.Session().Version()))
	p.UpdateDisplay(piecetable.AcceptedAndProposed)

	got, ok := p.ToggleFoldAt(3)
	if !ok {
		t.Fatal("no fold at line 3")
	}
	if got.StartLine != 2 || got.EndLine != 3 {
		t.Errorf("toggled fold = %d..%d, want the inner 2..3", got.StartLine, got.EndLine)
	}
	if got := p.FoldCount(); got != 2 {
		t.Errorf("FoldCount = %d, want both ranges kept", got)
	}
}

// An answer for another version is ignored by the projection, so an edit
// unfolds rather than hiding the wrong lines, and the fold comes back when a
// fresh answer for the new version is installed. Without the version guard the
// stale byte range would hide lines the edit moved.
func TestStaleFoldListIsIgnoredAfterAnEdit(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	f := foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != 4 {
		t.Fatalf("setup: DisplayLines = %d, want 4", got)
	}

	p.Cursors.Set(0, 0)
	p.InsertText("x") // bumps the session version
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != p.File.Lines() {
		t.Errorf("DisplayLines after the edit = %d, want the unfolded %d", got, p.File.Lines())
	}
	// A fresh answer for the new version restores the fold's effect.
	f = foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if got := p.DisplayLines(); got != 4 {
		t.Errorf("DisplayLines after a fresh answer = %d, want 4", got)
	}
}

// SetFolds keeps a range closed across an answer for the same lines, so an idle
// tick re-requesting the ranges does not spring the user's fold open.
func TestSetFoldsPreservesClosed(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	f := foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	// A second answer for the same version, as a fresh idle request produces.
	p.SetFolds([]Fold{foldOf(t, p, 0, 3)}, int(p.File.Session().Version()))
	if got, ok := p.ToggleFoldAt(0); !ok || got.Closed {
		t.Errorf("fold = %+v after a re-answer, want it still closed", got)
	}
	if got := p.FoldCount(); got != 1 {
		t.Errorf("FoldCount = %d, want 1", got)
	}
}

// A closed fold above the caret does not strand it: the caret keeps a display
// row inside the shorter display and the renderer still places it.
func TestCaretStaysSaneWhenAFoldShrinksTheDisplay(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	f := foldOf(t, p, 1, 5)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.UpdateDisplay(piecetable.AcceptedAndProposed)

	// Put the caret on the last line and follow it: the fold above moves it up
	// by the hidden rows, and it must still be on screen.
	last := p.File.LineStart(p.File.Lines() - 1)
	p.Cursors.Set(last, last)
	p.FollowCursor()
	row, _ := p.DispPos(p.Cursors.Primary().Head)
	if row < 0 || row >= p.DisplayLines() {
		t.Fatalf("caret row %d outside the display of %d rows", row, p.DisplayLines())
	}
	s := ui.NewScreen(40, 6)
	p.RenderFocused(s, 0, 0, 40, 6, Theme{}, true)
	if s.CursorY < 0 || s.CursorY >= 6 || s.CursorX < 0 || s.CursorX >= 40 {
		t.Errorf("caret drawn at (%d,%d), outside the 40x6 pane", s.CursorX, s.CursorY)
	}
}

// The fold row draws a marker that names the reader fold, distinct from a
// rejected run's change-set state. Without the UserFold branch the row would
// print "rejected" for a fold no decision put there.
func TestUserFoldRowDrawsFoldedMarker(t *testing.T) {
	p := savedPane(t, "a\nb\nc\nd\ne\n")
	f := foldOf(t, p, 0, 3)
	f.Closed = true
	p.SetFolds([]Fold{f}, int(p.File.Session().Version()))
	p.SetDisplay(piecetable.AcceptedAndProposed)
	rows := render(p, 40, 8)
	if !strings.Contains(strings.Join(rows, "\n"), "folded") {
		t.Errorf("no folded marker drawn:\n%q", rows)
	}
}
