package view

import "testing"

// Build is nil exactly when there are no segments: with no decisions the
// composition is the identity and the caller renders the session directly.
func TestBuildNilIffNoSegs(t *testing.T) {
	if p := Build("abc", nil); p != nil {
		t.Fatalf("Build(nil segs) = %v, want nil", p)
	}
	if p := Build("abc", []Seg{}); p != nil {
		t.Fatalf("Build(empty segs) = %v, want nil", p)
	}
	// One kept run is a real projection, not the identity.
	p := Build("abc", []Seg{{Doc: 0, Disp: 0, Len: 3, DLen: 3}})
	if p == nil {
		t.Fatal("Build with a kept run = nil, want a projection")
	}
	if got := p.Text(); got != "abc" {
		t.Fatalf("Text = %q, want abc", got)
	}
	if got := p.Lines(); got != 1 {
		t.Fatalf("Lines = %d, want 1", got)
	}
	if got := p.RowText(0); got != "abc" {
		t.Fatalf("RowText(0) = %q, want abc", got)
	}
	if _, _, ok := p.Fold(0); ok {
		t.Error("a kept row reported a fold")
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
}

// A Restore-only projection is a rejected deletion: the restored text is in the
// composition, so it draws as ordinary rows and there is no fold row.
func TestBuildRestoreOnlyNoFold(t *testing.T) {
	// session "AC", composition "ABC": B is restored between A and C.
	p := Build("ABC", []Seg{
		{Doc: 0, Disp: 0, Len: 1, DLen: 1}, // A
		{Doc: 1, Disp: 1, Len: 0, DLen: 1}, // restored B
		{Doc: 1, Disp: 2, Len: 1, DLen: 1}, // C
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Text(); got != "ABC" {
		t.Fatalf("Text = %q, want ABC", got)
	}
	if got := p.Lines(); got != 3 {
		t.Fatalf("Lines = %d, want 3", got)
	}
	for i, want := range []string{"A", "B", "C"} {
		if got := p.RowText(i); got != want {
			t.Errorf("RowText(%d) = %q, want %q", i, got, want)
		}
		if _, _, ok := p.Fold(i); ok {
			t.Errorf("row %d reported a fold; a restore is not a fold", i)
		}
	}
	if r := p.At(1); r.SessionLine >= 0 || r.Fold != 0 {
		t.Fatalf("restore row = %+v, want session-less with Fold 0", r)
	}
	if got := p.DocAt(1, 7); got != 1 {
		t.Errorf("DocAt(restore row) = %d, want the session cursor 1", got)
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	checkSessionRows(t, p)
}

// A hidden insertion and a restored deletion in one composition: one fold row
// for the hide, one ordinary row for the restore.
func TestBuildMixedHideAndRestore(t *testing.T) {
	// session "AxyC", composition "ABC": hidden "xy", restored "B".
	p := Build("ABC", []Seg{
		{Doc: 0, Disp: 0, Len: 1, DLen: 1},                       // A
		{Doc: 1, Disp: 1, Len: 2, DLen: 0, Fold: true, Group: 7}, // hidden xy
		{Doc: 3, Disp: 1, Len: 0, DLen: 1},                       // restored B
		{Doc: 3, Disp: 2, Len: 1, DLen: 1},                       // C
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Text(); got != "ABC" {
		t.Fatalf("Text = %q, want ABC", got)
	}
	if got := p.Lines(); got != 4 {
		t.Fatalf("Lines = %d, want 4", got)
	}
	if group, hidden, ok := p.Fold(1); !ok || group != 7 || hidden != 2 {
		t.Fatalf("Fold(1) = (%d,%d,%v), want (7,2,true)", group, hidden, ok)
	}
	if got := p.RowText(1); got != "" {
		t.Errorf("RowText(fold) = %q, want empty", got)
	}
	if got := p.RowText(2); got != "B" {
		t.Errorf("RowText(restore) = %q, want B", got)
	}
	if r := p.At(2); r.SessionLine >= 0 || r.Fold != 0 {
		t.Fatalf("restore row = %+v, want session-less with Fold 0", r)
	}
	if got := p.DocAt(1, 0); got != 1 {
		t.Errorf("DocAt(fold) = %d, want the hidden start 1", got)
	}
	// A session offset inside the hidden run answers the fold row.
	if line, _ := p.DispOfDoc(2); line != 1 {
		t.Errorf("DispOfDoc(2) row = %d, want the fold row 1", line)
	}
	checkSessionRows(t, p)
}

// A hidden run in the middle of a line splits it into a before row, the fold
// row, and the rest of the composition.
func TestBuildMidLineFold(t *testing.T) {
	// session "hello world\n", composition "hello\n": hide " world".
	p := Build("hello\n", []Seg{
		{Doc: 0, Disp: 0, Len: 5, DLen: 5},                       // hello
		{Doc: 5, Disp: 5, Len: 6, DLen: 0, Fold: true, Group: 9}, // " world"
		{Doc: 11, Disp: 5, Len: 1, DLen: 1},                      // newline
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Text(); got != "hello\n" {
		t.Fatalf("Text = %q", got)
	}
	if got := p.Lines(); got != 3 {
		t.Fatalf("Lines = %d, want 3", got)
	}
	want := []DispLine{
		{SessionLine: 0, Lo: 0, Hi: 5},
		{SessionLine: -1, Fold: 9},
		{SessionLine: 1, Lo: 0, Hi: 0},
	}
	for i, w := range want {
		if got := p.At(i); got != w {
			t.Errorf("At(%d) = %+v, want %+v", i, got, w)
		}
	}
	if group, hidden, ok := p.Fold(1); !ok || group != 9 || hidden != 6 {
		t.Fatalf("Fold(1) = (%d,%d,%v), want (9,6,true)", group, hidden, ok)
	}
	if got := p.RowText(0); got != "hello" {
		t.Errorf("RowText(0) = %q, want hello", got)
	}
	if got := p.DocAt(1, 0); got != 5 {
		t.Errorf("DocAt(fold) = %d, want 5", got)
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	if got := p.DispOfDocLine(1); got != 2 {
		t.Errorf("DispOfDocLine(1) = %d, want 2", got)
	}
	checkSessionRows(t, p)
}

// A hidden run that swallows a whole line and its newline at the end of the
// buffer folds to one row, and the hidden line reports no display row.
func TestBuildWholeLineFoldAtEnd(t *testing.T) {
	// session "a\nb\nHIDDEN\n", composition "a\nb\n".
	p := Build("a\nb\n", []Seg{
		{Doc: 0, Disp: 0, Len: 4, DLen: 4},                                       // "a\nb\n"
		{Doc: 4, Disp: 4, Len: 7, DLen: 0, Fold: true, Group: 3, HiddenLines: 1}, // "HIDDEN\n"
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Text(); got != "a\nb\n" {
		t.Fatalf("Text = %q", got)
	}
	if got := p.Lines(); got != 3 {
		t.Fatalf("Lines = %d, want 3", got)
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	if got := p.DispOfDocLine(1); got != 1 {
		t.Errorf("DispOfDocLine(1) = %d, want 1", got)
	}
	if got := p.DispOfDocLine(2); got != -1 {
		t.Errorf("DispOfDocLine(2) = %d, want -1 for the hidden line", got)
	}
	if group, hidden, ok := p.Fold(2); !ok || group != 3 || hidden != 7 {
		t.Fatalf("Fold(2) = (%d,%d,%v), want (3,7,true)", group, hidden, ok)
	}
	checkSessionRows(t, p)
}

// A folded run that spans several session lines carries its newline count, so
// the rows after it land on the true session lines and DispOfDocLine resolves
// them instead of returning -1.
func TestBuildMultiLineFoldCountsHiddenLines(t *testing.T) {
	// session "L0\nL1\nL2\nL3\n", composition "L0\nL3\n": hide "L1\nL2\n".
	p := Build("L0\nL3\n", []Seg{
		{Doc: 0, Disp: 0, Len: 3, DLen: 3},                                       // "L0\n"
		{Doc: 3, Disp: 3, Len: 6, DLen: 0, Fold: true, Group: 5, HiddenLines: 2}, // "L1\nL2\n"
		{Doc: 9, Disp: 3, Len: 3, DLen: 3},                                       // "L3\n"
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Lines(); got != 4 {
		t.Fatalf("Lines = %d, want 4", got)
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	// The two session lines the fold covers have no row.
	if got := p.DispOfDocLine(1); got != -1 {
		t.Errorf("DispOfDocLine(1) = %d, want -1", got)
	}
	if got := p.DispOfDocLine(2); got != -1 {
		t.Errorf("DispOfDocLine(2) = %d, want -1", got)
	}
	// L3 follows the fold: without the newline count it would be row -1.
	if got := p.DispOfDocLine(3); got != 2 {
		t.Errorf("DispOfDocLine(3) = %d, want 2", got)
	}
	if got := p.DispOfDocLine(4); got != 3 {
		t.Errorf("DispOfDocLine(4) = %d, want 3", got)
	}
	if group, hidden, ok := p.Fold(1); !ok || group != 5 || hidden != 6 {
		t.Fatalf("Fold(1) = (%d,%d,%v), want (5,6,true)", group, hidden, ok)
	}
	if line, _ := p.DispOfDoc(9); line != 2 {
		t.Errorf("DispOfDoc(9) row = %d, want 2", line)
	}
	if got := p.DocAt(2, 0); got != 9 {
		t.Errorf("DocAt(L3 row) = %d, want 9", got)
	}
	checkSessionRows(t, p)
}

// A hidden run with a newline can start and end mid-line. The rows after it
// report the line the hidden newline put them on, not the before-slice's.
func TestBuildMidLineFoldWithNewline(t *testing.T) {
	// session "helloMID\nDLEworld\n", composition "helloworld\n": hide "MID\nDLE".
	p := Build("helloworld\n", []Seg{
		{Doc: 0, Disp: 0, Len: 5, DLen: 5},                                       // "hello"
		{Doc: 5, Disp: 5, Len: 7, DLen: 0, Fold: true, Group: 6, HiddenLines: 1}, // "MID\nDLE"
		{Doc: 12, Disp: 5, Len: 7, DLen: 7},                                      // "world\n"
	})
	if p == nil {
		t.Fatal("Build = nil")
	}
	if got := p.Lines(); got != 4 {
		t.Fatalf("Lines = %d, want 4", got)
	}
	if got := p.DispOfDocLine(0); got != 0 {
		t.Errorf("DispOfDocLine(0) = %d, want 0", got)
	}
	if got := p.DispOfDocLine(1); got != 2 {
		t.Errorf("DispOfDocLine(1) = %d, want 2", got)
	}
	if got := p.DispOfDocLine(2); got != 3 {
		t.Errorf("DispOfDocLine(2) = %d, want 3", got)
	}
	if got := p.RowText(2); got != "world" {
		t.Errorf("RowText(after fold) = %q, want world", got)
	}
	if group, hidden, ok := p.Fold(1); !ok || group != 6 || hidden != 7 {
		t.Fatalf("Fold(1) = (%d,%d,%v), want (6,7,true)", group, hidden, ok)
	}
	if line, _ := p.DispOfDoc(12); line != 2 {
		t.Errorf("DispOfDoc(12) row = %d, want 2", line)
	}
	checkSessionRows(t, p)
}

// The map is monotone: as the session offset grows the display row never moves
// backwards, even across folds and restores.
func TestProjectionMonotone(t *testing.T) {
	cases := []struct {
		comp  string
		segs  []Seg
		limit int
	}{
		{"ABC", []Seg{
			{Doc: 0, Disp: 0, Len: 1, DLen: 1},
			{Doc: 1, Disp: 1, Len: 0, DLen: 1},
			{Doc: 1, Disp: 2, Len: 1, DLen: 1},
		}, 2},
		{"ABC", []Seg{
			{Doc: 0, Disp: 0, Len: 1, DLen: 1},
			{Doc: 1, Disp: 1, Len: 2, DLen: 0, Fold: true, Group: 7},
			{Doc: 3, Disp: 1, Len: 0, DLen: 1},
			{Doc: 3, Disp: 2, Len: 1, DLen: 1},
		}, 4},
		{"hello\n", []Seg{
			{Doc: 0, Disp: 0, Len: 5, DLen: 5},
			{Doc: 5, Disp: 5, Len: 6, DLen: 0, Fold: true, Group: 9},
			{Doc: 11, Disp: 5, Len: 1, DLen: 1},
		}, 12},
		{"L0\nL3\n", []Seg{
			{Doc: 0, Disp: 0, Len: 3, DLen: 3},
			{Doc: 3, Disp: 3, Len: 6, DLen: 0, Fold: true, Group: 5, HiddenLines: 2},
			{Doc: 9, Disp: 3, Len: 3, DLen: 3},
		}, 12},
	}
	for _, tc := range cases {
		p := Build(tc.comp, tc.segs)
		if p == nil {
			t.Fatalf("%q: nil projection", tc.comp)
		}
		prev := -1
		for off := 0; off <= tc.limit; off++ {
			line, _ := p.DispOfDoc(off)
			if line < prev {
				t.Fatalf("%q: DispOfDoc(%d) row %d after row %d", tc.comp, off, line, prev)
			}
			prev = line
		}
	}
}

// checkSessionRows asserts DocAt inverts DispOfDoc on every session-backed row:
// each byte the row draws maps back to the row and the same offset.
func checkSessionRows(t *testing.T, p *Projection) {
	t.Helper()
	for i := 0; i < p.Lines(); i++ {
		d := p.At(i)
		if d.SessionLine < 0 {
			continue
		}
		if d.Lo > d.Hi {
			t.Fatalf("row %d has Lo %d > Hi %d", i, d.Lo, d.Hi)
		}
		for off := p.docLo[i]; off < p.docHi[i]; off++ {
			line, col := p.DispOfDoc(off)
			if line != i {
				t.Fatalf("offset %d maps to row %d, want %d", off, line, i)
			}
			if got := p.DocAt(line, col); got != off {
				t.Fatalf("offset %d -> row %d col %d -> %d", off, line, col, got)
			}
		}
		if p.docLo[i] == p.docHi[i] {
			if got := p.DocAt(i, d.Lo); got != p.docLo[i] {
				t.Fatalf("empty row %d -> %d, want %d", i, got, p.docLo[i])
			}
		}
	}
}
