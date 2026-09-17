package editor

import (
	"strings"
	"testing"
	"unicode/utf8"

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
	for i := 0; i < p.DisplayLines(); i++ {
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
	for i := 0; i < p.DisplayLines(); i++ {
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
	for i := 0; i < p.DisplayLines(); i++ {
		if _, _, _, fold := p.line(i); fold {
			foldLine = i
			break
		}
	}
	if foldLine < 0 {
		t.Fatal("setup: no fold row")
	}
	want := p.DocAt(foldLine, 0)
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
		if off < p.File.Len() && !utf8.RuneStart(p.File.Slice(off, 1)[0]) {
			continue // inside a multi-byte rune: not an addressable position
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
	for i := 0; i < p.DisplayLines(); i++ {
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

// TestExportedMapIsIdentityWithoutDecisions pins the contract D2 relies on:
// with no decisions the projection is nil and the exported map is exactly the
// session map, so a caller can use it unconditionally.
func TestExportedMapIsIdentityWithoutDecisions(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\ngamma\n")
	p.SetDisplay(piecetable.AcceptedOnly)
	if p.disp != nil {
		t.Fatal("no decisions must leave the projection nil")
	}
	if got := p.DisplayLines(); got != p.File.Lines() {
		t.Fatalf("DisplayLines = %d, want File.Lines() = %d", got, p.File.Lines())
	}
	for off := 0; off <= p.File.Len(); off++ {
		wantLine, wantCol := p.File.LineCol(off)
		line, col := p.DispPos(off)
		if line != wantLine || col != wantCol {
			t.Fatalf("DispPos(%d) = (%d,%d), want (%d,%d)", off, line, col, wantLine, wantCol)
		}
	}
	for line := 0; line < p.File.Lines(); line++ {
		for col := -1; col <= len(p.File.Line(line))+1; col++ {
			if got, want := p.DocAt(line, col), p.File.OffsetAt(line, col); got != want {
				t.Fatalf("DocAt(%d,%d) = %d, want %d", line, col, got, want)
			}
		}
	}
}

// TestDecisionGenerationAdvancesOnDecision pins the second half of the memo key
// D2 caches SetDisplay under: a decision moves the composition without moving
// the session version, so the generation has to move for the cache to notice.
func TestDecisionGenerationAdvancesOnDecision(t *testing.T) {
	p := savedPane(t, "hello world\n")
	before := p.File.DecisionGeneration()
	id := proposeAt(t, p.File, 6, 11, "socket")
	proposed := p.File.DecisionGeneration()
	if proposed <= before {
		t.Fatalf("proposal generation = %d, want past %d", proposed, before)
	}
	version := p.File.Session().Version()
	p.File.AcceptGroup(id)
	if got := p.File.DecisionGeneration(); got <= proposed {
		t.Fatalf("accept generation = %d, want past %d", got, proposed)
	}
	if got := p.File.Session().Version(); got != version {
		t.Fatalf("a decision moved the session version %d -> %d", version, got)
	}
}

// TestOffsetAtRoundTripsPlaceCaretOnFoldedUTF8Buffer extends the caret round
// trip to multi-byte text: a fold and wide runes together must still be inverse
// at every character boundary.
func TestOffsetAtRoundTripsPlaceCaretOnFoldedUTF8Buffer(t *testing.T) {
	p, _ := foldedPane(t, "héllo wörld → 世界\n")
	roundTripOffsets(t, p)
}

// TestExportedLineMapOnMultiLineFold checks the exported line accessors agree
// with a multi-line fold: the hidden lines have no row, and the line after the
// fold reports its true session line.
func TestExportedLineMapOnMultiLineFold(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\ngamma\ndelta\n")
	id := proposeAt(t, p.File, 6, 6, "one\ntwo\nthree\n")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	if p.disp == nil {
		t.Fatal("a rejected multi-line insertion must build a projection")
	}
	if p.DisplayLines() >= p.File.Lines() {
		t.Fatalf("DisplayLines = %d, want fewer than the %d session lines", p.DisplayLines(), p.File.Lines())
	}
	if got := p.DispOfDocLine(1); got != -1 {
		t.Errorf("DispOfDocLine(1) = %d for a hidden line, want -1", got)
	}
	row := p.DispOfDocLine(6)
	if row < 0 {
		t.Fatal("DispOfDocLine(6) = -1 for the line after the fold")
	}
	if sl, _, _, fold := p.line(row); sl != 6 || fold {
		t.Errorf("row %d = (session line %d, fold %v), want session line 6", row, sl, fold)
	}
}

// RowText is the composition text a display row draws, which is what a
// consumer needs to read the screen rather than the session. With no decisions
// the projection is the identity, so every row reads back its session line
// exactly and D2 can call the accessor unconditionally.
func TestRowTextIsTheSessionLineWithoutDecisions(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\ngamma\n")
	p.SetDisplay(piecetable.AcceptedOnly)
	if p.disp != nil {
		t.Fatal("no decisions must leave the projection nil")
	}
	for i := 0; i < p.DisplayLines(); i++ {
		if got, want := p.RowText(i), p.File.Line(i); got != want {
			t.Errorf("RowText(%d) = %q, want the session line %q", i, got, want)
		}
	}
	if got := p.RowText(-1); got != "" {
		t.Errorf("RowText(-1) = %q, want empty", got)
	}
	if got := p.RowText(p.DisplayLines()); got != "" {
		t.Errorf("RowText(past the end) = %q, want empty", got)
	}
}

// A fold row draws a marker rather than text, so it reads back empty; the rows
// either side of a rejected mid-line run read back only the slice they draw.
func TestRowTextIsTheVisibleSliceAroundAFold(t *testing.T) {
	p, _ := foldedPane(t, "hello world\n")
	fold := firstFoldRow(p)
	if fold < 1 || fold+1 >= p.DisplayLines() {
		t.Fatalf("fixture built no usable fold: row %d of %d", fold, p.DisplayLines())
	}
	if got := p.RowText(fold); got != "" {
		t.Errorf("RowText(fold row %d) = %q, want empty", fold, got)
	}
	if got := p.RowText(fold - 1); got != "hello " {
		t.Errorf("RowText(before the fold) = %q, want %q", got, "hello ")
	}
	if got := p.RowText(fold + 1); got != "world" {
		t.Errorf("RowText(after the fold) = %q, want world", got)
	}
}

// firstFoldRow finds the first fold row in the pane's current projection, or
// -1 when nothing is folded. It reads the exported Fold accessor, so a test
// observes the projection's effect rather than reaching into view.
func firstFoldRow(p *Pane) int {
	for i := 0; i < p.DisplayLines(); i++ {
		if _, _, ok := p.Fold(i); ok {
			return i
		}
	}
	return -1
}

// TestUpdateDisplaySkipsARepeatedBuild pins the memo: a second UpdateDisplay
// with the session version, decision generation and policy all unchanged must
// not rebuild, which is what keeps Session.Project off the per-frame path. The
// build counter is the honest instrument here — p.disp is non-nil in this
// fixture, so the pointer check is a second witness, not the only one.
func TestUpdateDisplaySkipsARepeatedBuild(t *testing.T) {
	p := savedPane(t, "hello world\n")
	proposeAt(t, p.File, 6, 6, "XY")
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.disp == nil {
		t.Fatal("a proposed set must build a projection")
	}
	builds, first := p.displayBuilds, p.disp
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.displayBuilds != builds {
		t.Errorf("displayBuilds = %d, want %d: a no-op UpdateDisplay rebuilt", p.displayBuilds, builds)
	}
	if p.disp != first {
		t.Error("a no-op UpdateDisplay replaced the projection pointer")
	}
}

// TestUpdateDisplayRebuildsOnVersionBump: an ordinary edit moves the session
// version without moving the decision generation, so the version half of the
// key is what makes the next UpdateDisplay rebuild.
func TestUpdateDisplayRebuildsOnVersionBump(t *testing.T) {
	p := savedPane(t, "hello world\n")
	proposeAt(t, p.File, 6, 6, "XY")
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	builds, first := p.displayBuilds, p.disp
	p.File.Begin()
	if !p.File.Insert(p.Author, 0, "z") {
		t.Fatal("the setup edit was refused")
	}
	p.File.End()
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.displayBuilds != builds+1 {
		t.Fatalf("displayBuilds = %d, want %d after a version bump", p.displayBuilds, builds+1)
	}
	if p.disp == first {
		t.Error("the projection pointer did not move after a version bump")
	}
}

// TestUpdateDisplayRebuildsOnDecisionGeneration: a rejection moves the
// composition without moving the session version, so the generation half of
// the key is what makes the next UpdateDisplay rebuild. This is the case the
// version key alone would miss.
func TestUpdateDisplayRebuildsOnDecisionGeneration(t *testing.T) {
	p := savedPane(t, "hello world\n")
	id := proposeAt(t, p.File, 6, 6, "XY")
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	builds := p.displayBuilds
	version := p.File.Session().Version()
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if got := p.File.Session().Version(); got != version {
		t.Fatalf("rejecting moved the session version %d -> %d", version, got)
	}
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.displayBuilds != builds+1 {
		t.Fatalf("displayBuilds = %d, want %d after a decision", p.displayBuilds, builds+1)
	}
}

// TestUpdateDisplayRebuildsOnPolicyChange: switching modes moves neither the
// version nor the generation, so the policy is the third part of the key. The
// rebuilt projection must also change the composition: edit mode folds the
// rejected run away, review mode annotates it in place.
func TestUpdateDisplayRebuildsOnPolicyChange(t *testing.T) {
	p := savedPane(t, "hello world\n")
	id := proposeAt(t, p.File, 6, 6, "a much longer rejected run")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	builds := p.displayBuilds
	if firstFoldRow(p) < 0 {
		t.Fatal("edit mode must fold the rejected run away")
	}
	p.UpdateDisplay(piecetable.Annotated)
	if p.displayBuilds != builds+1 {
		t.Fatalf("displayBuilds = %d, want %d after a policy change", p.displayBuilds, builds+1)
	}
	if row := firstFoldRow(p); row >= 0 {
		t.Errorf("row %d is folded under Annotated: a rejected run is annotated, not hidden", row)
	}
}

// TestUpdateDisplaySkipsOnTheIdentityProjection covers the no-decisions fast
// path through the memo: Build returns nil, so the projection cannot witness a
// rebuild and the build counter is the only honest signal. A second call must
// still be a key hit, and the exported map must stay the session's own.
func TestUpdateDisplaySkipsOnTheIdentityProjection(t *testing.T) {
	p := savedPane(t, "alpha\nbeta\n")
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.disp != nil {
		t.Fatal("no decisions must leave the projection nil")
	}
	builds := p.displayBuilds
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.displayBuilds != builds {
		t.Errorf("displayBuilds = %d, want %d: the identity path rebuilt", p.displayBuilds, builds)
	}
	if got := p.DisplayLines(); got != p.File.Lines() {
		t.Errorf("DisplayLines = %d, want the identity %d", got, p.File.Lines())
	}
}

// TestPaneReloadInvalidatesTheDisplayProjection: a reload replaces the session,
// so the projection built from the old document no longer describes the new
// one. The pane must drop it before FollowCursor reads the map — otherwise the
// caret is scrolled through a file that is gone — and the next UpdateDisplay
// must rebuild rather than trust a key that did not move.
func TestPaneReloadInvalidatesTheDisplayProjection(t *testing.T) {
	f, path := openTemp(t, "hello world\n")
	p := NewPane(f)
	p.Resize(40, 8)
	id := proposeAt(t, p.File, 6, 6, "a much longer rejected run")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	if p.disp == nil {
		t.Fatal("setup: a rejected run must build a projection")
	}
	touch(t, path, "hello world\n")
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if p.disp != nil {
		t.Error("reload kept the projection built from the replaced document")
	}
	if got, want := p.DisplayLines(), p.File.Lines(); got != want {
		t.Errorf("DisplayLines = %d after reload, want the identity %d", got, want)
	}
	// A second Draw must rebuild the (identity) projection, not trust the key
	// the old document left behind.
	builds := p.displayBuilds
	p.UpdateDisplay(piecetable.AcceptedAndProposed)
	if p.displayBuilds != builds+1 {
		t.Errorf("displayBuilds = %d, want %d after a reload", p.displayBuilds, builds+1)
	}
}

// A display projection is a snapshot of the version it was built for, and the
// cursor can be read through it on the same event that shortened the text:
// undo appends its reversing ops and then Pane.history runs FollowCursor,
// before the next UpdateDisplay rebuilds the map. The stale row then reports
// the pre-undo line length, and reading the freshly shortened line through it
// used to slice out of range and take the process down.
func TestUndoOfAShorteningEditClampsTheStaleProjection(t *testing.T) {
	p := savedPane(t, "abc\ndef\n")
	p.Wrap = true // the wrapped cursor path is the one that slices the row
	// Any decision makes the edit-mode projection non-nil.
	proposeAt(t, p.File, 4, 7, "DDD")
	// The user insertion, which the undo will delete. The projection is
	// built with it in, so its row for line 0 ends past the line the undo
	// leaves behind.
	p.Cursors.Set(3, 3)
	if !p.File.Insert(p.Author, 3, "XY") {
		t.Fatal("the user insertion was refused")
	}
	p.SetDisplay(piecetable.AcceptedAndProposed)
	if p.disp == nil {
		t.Fatal("setup: a decision must build a projection")
	}
	if _, _, hi, _ := p.line(0); hi <= len("abc") {
		t.Fatalf("setup: the stale row ends at %d, want past the shortened line", hi)
	}
	// Undo runs FollowCursor through the stale map before the frame rebuilds
	// it; the fix clamps rather than slicing out of range.
	p.history(p.File.Undo(p.Author))
	if got := p.File.Text(); got != "abc\nDDD\n" {
		t.Fatalf("after undo = %q", got)
	}
	assertRowsInsideTheirLines(t, p)
	p.SetDisplay(piecetable.AcceptedAndProposed)
	assertRowsInsideTheirLines(t, p)
}

// assertRowsInsideTheirLines is the invariant Pane.line must hold whatever the
// projection vintage: a session-backed row [lo,hi) lies inside the session
// line it names.
func assertRowsInsideTheirLines(t *testing.T, p *Pane) {
	t.Helper()
	for i := 0; i < p.DisplayLines(); i++ {
		sl, lo, hi, _ := p.line(i)
		if sl < 0 {
			continue
		}
		full := p.File.Line(sl)
		if lo < 0 || hi < lo || hi > len(full) {
			t.Fatalf("row %d slice [%d,%d) outside line %q", i, lo, hi, full)
		}
	}
}
