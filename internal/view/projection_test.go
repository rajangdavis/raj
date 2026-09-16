package view

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

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

// randomProjection builds a consistent projection over a random session by
// partitioning it into kept runs and hidden runs the way piecetable.Project
// feeds them to Build: a kept run carries its bytes into the composition and a
// hidden run contributes one fold row and no composition bytes. Chunk
// boundaries land on rune boundaries so a fold never splits a character, and
// adjacent hidden runs merge so folds stay maximal.
func randomProjection(rng *rand.Rand) (session, comp string, p *Projection) {
	samples := []string{"a", "b", "c", "d", " ", "\n", "\t", "é", "界", "🙂"}
	var text strings.Builder
	for n := rng.Intn(30) + 1; n > 0; n-- {
		text.WriteString(samples[rng.Intn(len(samples))])
	}
	session = text.String()

	var cbuf strings.Builder
	var segs []Seg
	for i := 0; i < len(session); {
		runes := rng.Intn(5) + 1
		end := i
		for k := 0; k < runes && end < len(session); k++ {
			_, size := utf8.DecodeRuneInString(session[end:])
			end += size
		}
		chunk := session[i:end]
		if rng.Intn(2) == 0 {
			if n := len(segs); n > 0 && segs[n-1].Fold {
				segs[n-1].Len += len(chunk)
				segs[n-1].HiddenLines += strings.Count(chunk, "\n")
			} else {
				segs = append(segs, Seg{
					Doc:         i,
					Disp:        cbuf.Len(),
					Len:         len(chunk),
					Fold:        true,
					Group:       uint64(len(segs) + 1),
					HiddenLines: strings.Count(chunk, "\n"),
				})
			}
		} else {
			segs = append(segs, Seg{Doc: i, Disp: cbuf.Len(), Len: len(chunk), DLen: len(chunk)})
			cbuf.WriteString(chunk)
		}
		i = end
	}
	comp = cbuf.String()
	return session, comp, Build(comp, segs)
}

// checkProjectionInvariants asserts the map properties the D1 oracle names on
// whatever projection it is handed: comp is the composition it lays out and
// session the raw session bytes behind it.
func checkProjectionInvariants(t *testing.T, session, comp string, p *Projection) {
	t.Helper()
	if p == nil {
		t.Fatal("projection is nil for a non-empty segmentation")
	}
	if got := p.Text(); got != comp {
		t.Fatalf("Text = %q, want %q", got, comp)
	}
	if !utf8.ValidString(comp) {
		t.Fatalf("composition is not valid UTF-8: %q", comp)
	}

	// Inverse: every byte a session-backed row draws round-trips.
	checkSessionRows(t, p)

	// Monotonicity: DispOfDoc never moves backwards as the session offset
	// grows, and DocAt never moves backwards as the display row grows. The
	// pair is compared lexicographically because a line break resets the
	// column on a strictly later row.
	prevLine, prevCol := -1, -1
	for off := 0; off <= len(session); off++ {
		line, col := p.DispOfDoc(off)
		if line < prevLine || (line == prevLine && col < prevCol) {
			t.Fatalf("DispOfDoc(%d) = (%d,%d) after (%d,%d)", off, line, col, prevLine, prevCol)
		}
		prevLine, prevCol = line, col
	}
	prevOff := -1
	for row := 0; row < p.Lines(); row++ {
		if at := p.DocAt(row, 0); at < prevOff {
			t.Fatalf("DocAt(%d,0) = %d after %d", row, at, prevOff)
		}
		prevOff = p.DocAt(row, 0)
	}

	// Atomicity: every hidden byte maps to its fold row at column zero, and
	// DocAt on that row is the run's session cursor, never a byte inside it.
	for row := 0; row < p.Lines(); row++ {
		group, hidden, ok := p.Fold(row)
		if !ok {
			continue
		}
		if group == 0 || hidden <= 0 {
			t.Fatalf("fold row %d = (group %d, hidden %d)", row, group, hidden)
		}
		lo := p.DocAt(row, 0)
		hi := lo + hidden
		for off := lo; off < hi; off++ {
			line, col := p.DispOfDoc(off)
			if line != row || col != 0 {
				t.Fatalf("hidden offset %d maps to (%d,%d), want fold row (%d,0)", off, line, col, row)
			}
		}
		for _, col := range []int{-1, 0, 1, 1 << 20} {
			if got := p.DocAt(row, col); got != lo {
				t.Fatalf("DocAt(fold row %d, col %d) = %d, want run cursor %d", row, col, got, lo)
			}
		}
	}

	// Text: the non-fold rows, joined with the composition's own line breaks,
	// reproduce the composition; fold rows contribute nothing. A line whose
	// only row is a fold still contributes its break (rowTextComposition
	// checks the break before skipping the fold).
	if got := rowTextComposition(p); got != comp {
		t.Fatalf("RowText composition = %q, want %q", got, comp)
	}

	// Line agreement: a session-backed row reports a real session line, and
	// DispOfDocLine names the first row that does, or -1 when none does.
	first := map[int]int{}
	for row := 0; row < p.Lines(); row++ {
		sl := p.At(row).SessionLine
		if sl < 0 {
			continue
		}
		if sl >= len(p.first) {
			t.Fatalf("row %d reports session line %d, beyond %d", row, sl, len(p.first))
		}
		if _, seen := first[sl]; !seen {
			first[sl] = row
		}
	}
	for sl := 0; sl < len(p.first); sl++ {
		want, ok := first[sl]
		if !ok {
			want = -1
		}
		if got := p.DispOfDocLine(sl); got != want {
			t.Fatalf("DispOfDocLine(%d) = %d, want %d", sl, got, want)
		}
	}

	// UTF-8: every drawn range and every fold edge starts on a rune boundary,
	// so no conversion can hand back a position mid-character.
	for row := 0; row < p.Lines(); row++ {
		for _, off := range []int{p.docLo[row], p.docHi[row]} {
			if off < len(session) && !utf8.RuneStart(session[off]) {
				t.Fatalf("row %d boundary %d is mid-rune in %q", row, off, session)
			}
		}
	}
}

// rowTextComposition rebuilds the composition from RowText: a newline is
// emitted exactly where a non-fold row's composition range begins after one, so
// rows a fold split on the same line concatenate without a separator.
func rowTextComposition(p *Projection) string {
	var sb strings.Builder
	lastBreak := -1
	for row := 0; row < p.Lines(); row++ {
		lo := p.compLo[row]
		// A line break precedes a row whose composition range starts just after
		// it. This is checked for fold rows too: a line whose only row is a
		// fold still has its break, and skipping the fold before emitting it
		// would lose that newline. Each break is emitted once, so a fold row
		// and a content row following it on the same line do not double it.
		if lo > 0 && p.text[lo-1] == '\n' && lo-1 != lastBreak {
			sb.WriteByte('\n')
			lastBreak = lo - 1
		}
		if _, _, ok := p.Fold(row); ok {
			continue
		}
		sb.WriteString(p.RowText(row))
	}
	return sb.String()
}

// TestProjectionInvariantsAcrossRandomSessions drives the map over random
// sessions and segmentations, covering cases no hand-written fixture thinks to
// include.
func TestProjectionInvariantsAcrossRandomSessions(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for range 500 {
		session, comp, p := randomProjection(rng)
		checkProjectionInvariants(t, session, comp, p)
	}
}

// FuzzProjectionInvariants lets the fuzzer drive the same oracle from a seed,
// so an interval the generator's distribution misses is one mutation away.
func FuzzProjectionInvariants(f *testing.F) {
	f.Add(uint64(1))
	f.Add(uint64(2))
	f.Add(uint64(99))
	f.Fuzz(func(t *testing.T, seed uint64) {
		rng := rand.New(rand.NewSource(int64(seed)))
		session, comp, p := randomProjection(rng)
		checkProjectionInvariants(t, session, comp, p)
	})
}

// TestRowTextReconstructsComposition pins the text invariant on the two shapes
// the random generator does not produce: a restored deletion among kept runs
// and a fold that splits its session line.
func TestRowTextReconstructsComposition(t *testing.T) {
	cases := []struct {
		name string
		comp string
		segs []Seg
	}{
		{"mid-line fold", "hello\n", []Seg{
			{Doc: 0, Disp: 0, Len: 5, DLen: 5},
			{Doc: 5, Disp: 5, Len: 6, DLen: 0, Fold: true, Group: 9},
			{Doc: 11, Disp: 5, Len: 1, DLen: 1},
		}},
		{"restore among keeps", "ABC", []Seg{
			{Doc: 0, Disp: 0, Len: 1, DLen: 1},
			{Doc: 1, Disp: 1, Len: 2, DLen: 0, Fold: true, Group: 7},
			{Doc: 3, Disp: 1, Len: 0, DLen: 1},
			{Doc: 3, Disp: 2, Len: 1, DLen: 1},
		}},
	}
	for _, tc := range cases {
		p := Build(tc.comp, tc.segs)
		if p == nil {
			t.Fatalf("%s: nil projection", tc.name)
		}
		if got := rowTextComposition(p); got != tc.comp {
			t.Errorf("%s: RowText composition = %q, want %q", tc.name, got, tc.comp)
		}
	}
}

// A proposal that cuts a line in half (replacing "world" inside "hello world")
// composes as base "hello " | proposed "socket" | base "\n". Adjacent kept runs
// owned by different change sets must draw as one display row, while a fold or a
// restore still forces its own. The merge case follows TestBuildMidLineFold with
// the fold swapped for a second kept run; the boundary cases follow
// TestBuildMidLineFold and TestBuildRestoreOnlyNoFold.
func TestBuildMidLineReplacementMergesKeptRuns(t *testing.T) {
	t.Run("kept runs merge", func(t *testing.T) {
		// The session is the view after the proposal: "hello socket\n".
		p := Build("hello socket\n", []Seg{
			{Doc: 0, Disp: 0, Len: 6, DLen: 6},           // base "hello "
			{Doc: 6, Disp: 6, Len: 6, DLen: 6, Group: 4}, // proposed "socket"
			{Doc: 12, Disp: 12, Len: 1, DLen: 1},         // base "\n"
		})
		if p == nil {
			t.Fatal("Build = nil")
		}
		if got := p.Lines(); got != 2 {
			t.Fatalf("Lines = %d, want 2: one content row and the line's break", got)
		}
		if got := p.RowText(0); got != "hello socket" {
			t.Errorf("RowText(0) = %q, want the whole line", got)
		}
		if got := p.At(0); got != (DispLine{SessionLine: 0, Lo: 0, Hi: 12}) {
			t.Errorf("At(0) = %+v, want one row over the whole line", got)
		}
		if _, _, ok := p.Fold(0); ok {
			t.Error("row 0 reported a fold; a replacement is kept text")
		}
		if got := p.DispOfDocLine(0); got != 0 {
			t.Errorf("DispOfDocLine(0) = %d, want 0", got)
		}
		checkProjectionInvariants(t, "hello socket\n", "hello socket\n", p)
	})

	t.Run("fold still splits", func(t *testing.T) {
		// session "aXb\n", composition "ab\n": hide "X".
		p := Build("ab\n", []Seg{
			{Doc: 0, Disp: 0, Len: 1, DLen: 1},                       // a
			{Doc: 1, Disp: 1, Len: 1, DLen: 0, Fold: true, Group: 2}, // hidden X
			{Doc: 2, Disp: 1, Len: 2, DLen: 2},                       // b\n
		})
		if p == nil {
			t.Fatal("Build = nil")
		}
		if got := p.Lines(); got != 4 {
			t.Fatalf("Lines = %d, want 4: a, the fold, b, and the empty line", got)
		}
		if group, hidden, ok := p.Fold(1); !ok || group != 2 || hidden != 1 {
			t.Errorf("Fold(1) = (%d,%d,%v), want (2,1,true)", group, hidden, ok)
		}
		if got := p.RowText(0); got != "a" {
			t.Errorf("RowText(0) = %q, want a", got)
		}
		if got := p.RowText(2); got != "b" {
			t.Errorf("RowText(2) = %q, want b; the fold must not merge the runs", got)
		}
		checkProjectionInvariants(t, "aXb\n", "ab\n", p)
	})

	t.Run("restore still splits", func(t *testing.T) {
		// session "AC", composition "ABC": B is restored between A and C.
		p := Build("ABC", []Seg{
			{Doc: 0, Disp: 0, Len: 1, DLen: 1}, // A
			{Doc: 1, Disp: 1, Len: 0, DLen: 1}, // restored B
			{Doc: 1, Disp: 2, Len: 1, DLen: 1}, // C
		})
		if p == nil {
			t.Fatal("Build = nil")
		}
		if got := p.Lines(); got != 3 {
			t.Fatalf("Lines = %d, want 3: a kept run on either side of the restore", got)
		}
		for i, want := range []string{"A", "B", "C"} {
			if got := p.RowText(i); got != want {
				t.Errorf("RowText(%d) = %q, want %q", i, got, want)
			}
		}
		if r := p.At(1); r.SessionLine >= 0 || r.Fold != 0 {
			t.Errorf("restore row = %+v, want session-less with Fold 0", r)
		}
		checkProjectionInvariants(t, "AC", "ABC", p)
	})
}
