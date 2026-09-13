package editor

import (
	"strings"
	"testing"

	"raj/internal/piecetable"
	"raj/internal/ui"
)

// foldedPane builds a pane over a saved file, proposes a mid-line agent
// insertion, rejects it, and derives the edit-mode display. The rejected run is
// hidden from AcceptedAndProposed, so the projection collapses it to a fold.
func foldedPane(t *testing.T, body string) (*Pane, uint64) {
	t.Helper()
	p := savedPane(t, body)
	id := proposeAt(t, p.File, 6, 6, "a much longer rejected run")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	p.Resize(40, 8)
	return p, id
}

// restoredPane builds a pane over a saved file, proposes a deletion, rejects it,
// and derives the edit-mode display. A rejected deletion is excluded from the
// composition, so the deleted bytes come back as composition-only rows.
func restoredPane(t *testing.T, body string) (*Pane, uint64) {
	t.Helper()
	p := savedPane(t, body)
	id := proposeAt(t, p.File, 6, 11, "") // delete "world"
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	p.Resize(40, 8)
	return p, id
}

// hiddenByFold reports whether a session offset sits inside a hidden run the
// display folds away: its display row is a fold row.
func hiddenByFold(p *Pane, off int) bool {
	if p.disp == nil {
		return false
	}
	sl, _, _, fold := p.line(p.dispLineOf(off))
	return sl < 0 && fold
}

// A rejected insertion leaves the view as one fold row rather than
// disappearing or staying as live text.
func TestRejectedRunBecomesOneFoldRow(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	if p.disp == nil {
		t.Fatal("a rejected run must build a projection")
	}
	folds, keeps := 0, 0
	for i := 0; i < p.displayLines(); i++ {
		sl, lo, hi, fold := p.line(i)
		if sl < 0 {
			if !fold {
				t.Errorf("row %d is composition-only, want the insertion folded", i)
			}
			folds++
			if _, hidden, ok := p.Fold(i); !ok || hidden <= 0 {
				t.Errorf("fold row %d has no hidden byte count", i)
			}
			continue
		}
		keeps++
		if sl >= p.File.Lines() {
			t.Fatalf("kept row %d reports session line %d", i, sl)
		}
		full := p.File.Line(sl)
		if lo < 0 || hi < lo || hi > len(full) {
			t.Errorf("row %d slice [%d,%d) outside line %q", i, lo, hi, full)
		}
	}
	if folds != 1 {
		t.Errorf("got %d fold rows, want exactly 1", folds)
	}
	if keeps < 1 {
		t.Errorf("got %d kept rows, want the composition text drawn", keeps)
	}
}

// The fold row draws a marker instead of text, and the marker names the state.
func TestFoldRowDrawsRejectedMarker(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	rows := render(p, 40, 8)
	found := false
	for _, r := range rows {
		if strings.Contains(r, "rejected") {
			found = true
			if !strings.Contains(r, "bytes") {
				t.Errorf("fold marker %q does not name its size", r)
			}
		}
	}
	if !found {
		t.Errorf("no fold marker drawn:\n%q", rows)
	}
}

// A rejected deletion is restored in the composition, so it shows as a
// composition-only row with no fold, and the renderer draws that row rather
// than indexing a session line it does not have.
func TestRejectedDeletionRendersRestoredText(t *testing.T) {
	p, _ := restoredPane(t, "hello world\n")
	if p.disp == nil {
		t.Fatal("a rejected deletion must build a projection")
	}
	folds, compRows := 0, 0
	restored := ""
	for i := 0; i < p.displayLines(); i++ {
		sl, _, _, fold := p.line(i)
		if sl >= 0 {
			continue
		}
		if fold {
			folds++
			continue
		}
		compRows++
		if sl != -1 {
			t.Errorf("composition-only row %d reports session line %d", i, sl)
		}
		restored += p.disp.RowText(i)
	}
	if folds != 0 {
		t.Errorf("got %d fold rows, want none for a rejected deletion", folds)
	}
	if compRows == 0 {
		t.Fatal("no composition-only row for the restored deletion")
	}
	if !strings.Contains(restored, "world") {
		t.Errorf("restored rows = %q, want the deleted text back", restored)
	}
	rows := render(p, 40, 8)
	if !strings.Contains(strings.Join(rows, "\n"), "world") {
		t.Errorf("restored text not drawn:\n%q", rows)
	}
}

// placeCaret and OffsetAt stay inverses on a folded buffer: clicking the cell
// the caret is drawn on returns the caret offset. Hidden bytes are skipped,
// because the display addresses no position inside a folded run.
func TestOffsetAtRoundTripsPlaceCaretOnFoldedBuffer(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	roundTripOffsets(t, p)
}

// The same round trip through the wrapped path, which walks display rows rather
// than assuming one row per line.
func TestOffsetAtRoundTripsPlaceCaretOnWrappedFoldedBuffer(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	p.Wrap = true
	p.Resize(40, 8)
	roundTripOffsets(t, p)
}

// A click on the fold row lands on the hidden run session cursor, never inside
// the run.
func TestClickOnFoldRowSnapsToEdge(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	foldLine := -1
	for i := 0; i < p.displayLines(); i++ {
		if _, _, _, fold := p.line(i); fold {
			foldLine = i
			break
		}
	}
	if foldLine < 0 {
		t.Fatal("setup: no fold row")
	}
	want := p.docAt(foldLine, 0)
	if got := p.OffsetAt(3, foldLine-p.Viewport.Top); got != want {
		t.Errorf("click on fold row = %d, want the hidden run cursor %d", got, want)
	}
}

// roundTripOffsets walks every offset the projection draws, renders the caret
// there, and checks the cell it landed on maps back to that offset.
func roundTripOffsets(t *testing.T, p *Pane) {
	t.Helper()
	cols, rows := 40, 8
	for off := 0; off <= p.File.Len(); off++ {
		if hiddenByFold(p, off) {
			continue
		}
		p.Cursors.Set(off, off)
		p.FollowCursor()
		s := ui.NewScreen(cols, rows)
		p.RenderFocused(s, 0, 0, cols, rows, Theme{}, true)
		got := p.OffsetAt(s.CursorX-p.GutterWidth(), s.CursorY)
		if got != off {
			t.Fatalf("offset %d: caret drawn at (%d,%d) maps back to %d", off, s.CursorX, s.CursorY, got)
		}
	}
}

// With no decisions there is no projection, and the pane renders exactly as it
// did before folds existed.
func TestNoDecisionsRendersUnchanged(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\n")
	p.SetDisplay(piecetable.AcceptedOnly)
	if p.disp != nil {
		t.Fatal("no decisions must build no projection")
	}
	rows := render(p, 20, 4)
	if len(rows) < 2 || !strings.Contains(rows[0], "alpha") || !strings.Contains(rows[1], "beta") {
		t.Errorf("identity render changed:\n%q", rows)
	}
}

// A hidden run spanning several session lines must keep the session line
// numbers of the rows after it correct: the fold swallows the hidden newlines,
// so the next session-backed row is offset by the run's newline count.
func TestMultiLineFoldKeepsSessionLines(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\ngamma\ndelta\n")
	id := proposeAt(t, p.File, 6, 6, "hidden one\nhidden two\nhidden three\n")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	p.Resize(40, 12)
	if p.disp == nil {
		t.Fatal("a multi-line rejected run must build a projection")
	}
	sawFold := false
	got := map[string]int{}
	for i := 0; i < p.displayLines(); i++ {
		sl, _, _, fold := p.line(i)
		if sl < 0 {
			if fold {
				sawFold = true
			}
			continue
		}
		text := p.File.Line(sl)
		for _, want := range []string{"alpha", "beta", "gamma", "delta"} {
			if strings.Contains(text, want) {
				got[want] = sl
			}
		}
	}
	if !sawFold {
		t.Fatal("no fold row for the multi-line hidden run")
	}
	want := map[string]int{"alpha": 0, "beta": 4, "gamma": 5, "delta": 6}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("row text %q reports session line %d, want %d", k, got[k], v)
		}
	}
}
