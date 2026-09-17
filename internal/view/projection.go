package view

import (
	"sort"
	"strings"
)

// Seg is one run of the alignment between the raw session text and the
// composition the projection lays out, as piecetable.Segments reports it. This
// package does not import piecetable: the caller copies the fields across, so
// the view layer stays free of the document model.
//
// Doc and Len index the raw session, Disp and DLen index comp. A Fold run
// (DLen == 0, Len > 0) is an excluded insertion: comp omits it and the display
// draws one fold row in its place. A Restore run (Len == 0, DLen > 0) is a
// composition-only run — bytes an excluded deletion put back — and draws as an
// ordinary row.
//
// HiddenLines counts the '\n' bytes inside a Fold run's session bytes. They are
// not in comp, so it is the only way to number the session lines that follow the
// fold. It is zero for every other run.
type Seg struct {
	Doc, Disp   int
	Len, DLen   int
	Fold        bool
	Group       uint64
	HiddenLines int
}

// DispLine is one row of the projection.
//
//   - session-backed: SessionLine >= 0, the row draws that session line's bytes
//     [Lo, Hi) (Lo and Hi are offsets within the line, newline excluded).
//   - fold: SessionLine < 0 and Fold != 0, a marker for the hidden run of that
//     change set.
//   - composition-only: SessionLine < 0 and Fold == 0, bytes that exist only in
//     the composition (a Restore run), with no session line behind them.
type DispLine struct {
	SessionLine int
	Lo, Hi      int
	Fold        uint64
}

// Projection is a display-only layout of a composition. It is a snapshot: build
// it once and read it; it mutates nothing.
type Projection struct {
	text   string
	starts []int // session line starts the composition pins down
	n      int   // raw session length in bytes

	lines          []DispLine
	compLo, compHi []int // comp byte range of each row's text
	docLo, docHi   []int // session byte range (fold: hidden run; restore: a point)
	first          []int // first row of each session line, -1 if none

	sessRows []int // session-backed rows, ordered by docLo
	foldRows []int // fold rows, ordered by docLo
}

// FoldRange is one reader-placed hidden run: the session bytes [Lo, Hi) the
// display collapses to one fold row. It is a display-only range and adds
// nothing to the document — the same atomic-fold representation a rejected run
// uses, distinguished only by the sentinel group on its row.
type FoldRange struct{ Lo, Hi int }

// UserFold is the DispLine.Fold value on a row a reader folded rather than a
// change set. Change-set ids start at 1, so the top of the id space is free to
// mark the other kind; Pane.Fold and drawFold read it to tell them apart
// without a second field on every row.
const UserFold uint64 = ^uint64(0)

// Build lays comp into display rows from the composition segments alone. It is
// BuildFolds with no reader-placed folds, kept because every existing caller
// and test wants exactly that.
func Build(comp string, segs []Seg) *Projection {
	return BuildFolds(comp, segs, nil)
}

// BuildFolds lays comp — the text of a piecetable projection — into display
// rows, inserting one fold row wherever a Fold seg sits and one more wherever a
// caller-placed FoldRange hides a run the composition still carries. It returns
// nil iff there are no segments and no reader folds: with neither, the
// composition is the identity and the caller renders the session directly.
//
// A reader fold (a language server's folding range, toggled by the user) hides
// bytes the composition keeps, so its whole display rows are dropped and
// replaced by one fold row. The map stays the single bidirectional projection:
// DispOfDoc maps any hidden offset to that row, DocAt returns the run's start,
// and DispOfDocLine answers -1 for the hidden lines, so the caret, scroll,
// mouse and gutter invariants are the rejected-run ones unchanged.
func BuildFolds(comp string, segs []Seg, user []FoldRange) *Projection {
	if len(segs) == 0 {
		if len(user) == 0 {
			return nil
		}
		// A clean document has no decisions, so the composition is the session
		// text itself; synthesise the identity run so a fold still has rows to
		// hide. Every field is the one session byte mapping to itself.
		segs = []Seg{{Doc: 0, Disp: 0, Len: len(comp), DLen: len(comp)}}
	}
	p := buildProjection(comp, segs)
	if len(user) > 0 {
		p.hideUserFolds(user)
	}
	p.reindex()
	return p
}

// buildProjection is the composition-only half of Build. It does not index the
// rows; BuildFolds hides reader folds first and reindexes once.
func buildProjection(comp string, segs []Seg) *Projection {
	if len(segs) == 0 {
		return nil
	}
	p := &Projection{text: comp}
	for _, s := range segs {
		p.n += s.Len
	}
	starts := []int{0}
	for _, s := range segs {
		if s.Fold {
			// The hidden run's newline positions are not in comp; place its
			// line starts at the end of the run so the total count, and the
			// lines after the fold, stay right. A run that ends at a line
			// boundary lands on the exact start.
			for k := 1; k <= s.HiddenLines && k <= s.Len; k++ {
				starts = append(starts, s.Doc+s.Len-s.HiddenLines+k)
			}
			continue
		}
		if s.Len <= 0 || s.DLen <= 0 {
			continue
		}
		text := comp[s.Disp:min(s.Disp+s.DLen, len(comp))]
		for off := 0; ; {
			k := strings.IndexByte(text[off:], '\n')
			if k < 0 {
				break
			}
			i := off + k
			starts = append(starts, min(s.Doc+i+1, p.n))
			off = i + 1
		}
	}
	p.starts = starts
	p.first = make([]int, len(starts))
	for i := range p.first {
		p.first[i] = -1
	}

	// Pieces cover comp: every DLen > 0 seg. Folds are zero-width markers.
	type piece struct{ disp, dlen, doc, length int }
	var pieces []piece
	type foldRun struct {
		disp        int
		group       uint64
		doc, length int
	}
	var folds []foldRun
	for _, s := range segs {
		if s.Fold {
			folds = append(folds, foldRun{disp: s.Disp, group: s.Group, doc: s.Doc, length: s.Len})
			continue
		}
		if s.DLen > 0 {
			pieces = append(pieces, piece{disp: s.Disp, dlen: s.DLen, doc: s.Doc, length: s.Len})
		}
	}
	sort.SliceStable(pieces, func(i, j int) bool { return pieces[i].disp < pieces[j].disp })
	sort.SliceStable(folds, func(i, j int) bool { return folds[i].disp < folds[j].disp })

	segAt := func(off int) (piece, bool) {
		i := sort.Search(len(pieces), func(k int) bool { return pieces[k].disp+pieces[k].dlen > off })
		if i < len(pieces) && pieces[i].disp <= off && off < pieces[i].disp+pieces[i].dlen {
			return pieces[i], true
		}
		if off == len(comp) && len(pieces) > 0 {
			return pieces[len(pieces)-1], true
		}
		return piece{}, false
	}

	emit := func(sessionLine, lo, hi int, fold uint64, docA, docB, cA, cB int) {
		p.lines = append(p.lines, DispLine{SessionLine: sessionLine, Lo: lo, Hi: hi, Fold: fold})
		p.docLo = append(p.docLo, docA)
		p.docHi = append(p.docHi, docB)
		p.compLo = append(p.compLo, cA)
		p.compHi = append(p.compHi, cB)
		if sessionLine >= 0 && p.first[sessionLine] < 0 {
			p.first[sessionLine] = len(p.lines) - 1
		}
	}

	// emitContent lays [a,b) of comp into rows. Adjacent kept pieces merge into
	// one row: a composition line restored from several change sets is still one
	// line. A restore piece starts its own row, drawing bytes the session does
	// not hold, and a fold splits the call in two. [a,b) never spans a
	// composition line or a fold: the caller emits each line's fold-free ranges.
	emitContent := func(a, b int) {
		// foldAt reports a fold marker at off. A kept run must not merge across
		// one: the fold owns a display row of its own there.
		foldAt := func(off int) bool {
			i := sort.Search(len(folds), func(k int) bool { return folds[k].disp >= off })
			return i < len(folds) && folds[i].disp == off
		}
		for a < b {
			pc, ok := segAt(a)
			if !ok {
				return
			}
			end := b
			if pc.disp+pc.dlen < end {
				end = pc.disp + pc.dlen
			}
			if end <= a {
				return
			}
			if pc.length <= 0 {
				// A restore run is composition-only: its own row, so it never
				// shares a session-backed row with kept bytes.
				emit(-1, a-pc.disp, end-pc.disp, 0, pc.doc, pc.doc, a, end)
				a = end
				continue
			}
			docA := pc.doc + (a - pc.disp)
			docB := pc.doc + (end - pc.disp)
			merged := end
			// Absorb every following kept piece that continues this run: same
			// composition line (the caller's range is one line), contiguous in
			// both composition and session, and not behind a fold. Different
			// change sets may own the pieces; the row is still one line.
			for merged < b && !foldAt(merged) {
				np, ok := segAt(merged)
				if !ok || np.length <= 0 || np.disp != merged || np.doc != docB {
					break
				}
				nend := b
				if np.disp+np.dlen < nend {
					nend = np.disp + np.dlen
				}
				if nend <= merged {
					break
				}
				docB = np.doc + (nend - np.disp)
				merged = nend
			}
			sl := projLineOf(starts, docA)
			ls := projLineStart(starts, sl)
			emit(sl, docA-ls, docB-ls, 0, docA, docB, a, merged)
			a = merged
		}
	}

	// emitEmpty draws a composition line with no visible bytes. Its kind follows
	// the run it sits in: session-backed inside a kept run, composition-only
	// inside a restored one.
	emitEmpty := func(cs int) {
		pc, ok := segAt(cs)
		if !ok && cs > 0 {
			pc, ok = segAt(cs - 1)
		}
		if !ok {
			emit(-1, 0, 0, 0, 0, 0, cs, cs)
			return
		}
		if pc.length > 0 {
			doc := max(min(pc.doc+(cs-pc.disp), pc.doc+pc.length), pc.doc)
			sl := projLineOf(starts, doc)
			ls := projLineStart(starts, sl)
			emit(sl, doc-ls, doc-ls, 0, doc, doc, cs, cs)
		} else {
			emit(-1, 0, 0, 0, pc.doc, pc.doc, cs, cs)
		}
	}

	// Composition line starts: one per newline plus the first.
	compStarts := []int{0}
	for off := 0; ; {
		k := strings.IndexByte(comp[off:], '\n')
		if k < 0 {
			break
		}
		i := off + k
		compStarts = append(compStarts, i+1)
		off = i + 1
	}

	fi := 0
	for li := 0; li < len(compStarts); li++ {
		cs := compStarts[li]
		ce := len(comp)
		if li+1 < len(compStarts) {
			ce = compStarts[li+1] - 1 // the newline position
		}
		pos := cs
		emitted := false
		for fi < len(folds) && folds[fi].disp < cs {
			fi++
		}
		for fi < len(folds) && folds[fi].disp <= ce {
			fr := folds[fi]
			if fr.disp > pos {
				emitContent(pos, fr.disp)
			}
			emit(-1, 0, 0, fr.group, fr.doc, fr.doc+fr.length, fr.disp, fr.disp)
			emitted = true
			pos = fr.disp
			fi++
		}
		if ce > pos {
			emitContent(pos, ce)
			emitted = true
		}
		if !emitted {
			emitEmpty(cs)
		}
	}

	return p
}

// reindex rebuilds the derived row tables from p.lines: which rows are folds,
// which are session-backed, and the first row of each session line. BuildFolds
// calls it after hiding reader folds, so the map's search tables describe the
// rows that actually exist.
func (p *Projection) reindex() {
	p.first = make([]int, len(p.starts))
	for i := range p.first {
		p.first[i] = -1
	}
	p.foldRows = p.foldRows[:0]
	p.sessRows = p.sessRows[:0]
	for i, d := range p.lines {
		switch {
		case d.Fold != 0:
			p.foldRows = append(p.foldRows, i)
		case d.SessionLine >= 0:
			p.sessRows = append(p.sessRows, i)
			if d.SessionLine < len(p.first) && p.first[d.SessionLine] < 0 {
				p.first[d.SessionLine] = i
			}
		}
	}
}

// hideUserFolds drops the rows a set of reader folds hides and inserts one fold
// row for each. Ranges are sorted and overlapping ones merged, so the result is
// one row per hidden region and the row tables stay ordered by session offset.
//
// A row is hidden when its session start falls inside a range. With line-based
// ranges from a server that is always whole rows, because a range begins at a
// line start; a proposal fold row that straddles a range boundary is left
// visible rather than half-hidden, which is the one overlap this deliberately
// does not try to resolve.
func (p *Projection) hideUserFolds(folds []FoldRange) {
	fs := make([]FoldRange, 0, len(folds))
	for _, f := range folds {
		if f.Hi > f.Lo {
			fs = append(fs, f)
		}
	}
	if len(fs) == 0 {
		return
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Lo < fs[j].Lo })
	merged := fs[:1]
	for _, f := range fs[1:] {
		last := &merged[len(merged)-1]
		if f.Lo <= last.Hi {
			if f.Hi > last.Hi {
				last.Hi = f.Hi
			}
			continue
		}
		merged = append(merged, f)
	}

	n := len(p.lines)
	lines := make([]DispLine, 0, n)
	docLo := make([]int, 0, n)
	docHi := make([]int, 0, n)
	compLo := make([]int, 0, n)
	compHi := make([]int, 0, n)

	fi := 0
	for i := 0; i < n; {
		for fi < len(merged) && merged[fi].Hi <= p.docLo[i] {
			fi++
		}
		if fi < len(merged) && p.docLo[i] >= merged[fi].Lo && p.docLo[i] < merged[fi].Hi {
			fr := merged[fi]
			lines = append(lines, DispLine{SessionLine: -1, Fold: UserFold})
			docLo = append(docLo, fr.Lo)
			docHi = append(docHi, fr.Hi)
			compLo = append(compLo, 0)
			compHi = append(compHi, 0)
			for i < n && p.docLo[i] >= fr.Lo && p.docLo[i] < fr.Hi {
				i++
			}
			continue
		}
		lines = append(lines, p.lines[i])
		docLo = append(docLo, p.docLo[i])
		docHi = append(docHi, p.docHi[i])
		compLo = append(compLo, p.compLo[i])
		compHi = append(compHi, p.compHi[i])
		i++
	}
	p.lines = lines
	p.docLo = docLo
	p.docHi = docHi
	p.compLo = compLo
	p.compHi = compHi
}

// projLineOf reports which known session line an offset falls on, matching
// view.Index's LineOf for the lines the composition pins down.
func projLineOf(starts []int, off int) int {
	if off <= 0 || len(starts) == 0 {
		return 0
	}
	i := sort.SearchInts(starts, off+1)
	if i > len(starts) {
		i = len(starts)
	}
	return i - 1
}

// projLineStart is the byte offset a known session line begins at.
func projLineStart(starts []int, line int) int {
	if len(starts) == 0 {
		return 0
	}
	if line < 0 {
		return 0
	}
	if line >= len(starts) {
		return starts[len(starts)-1]
	}
	return starts[line]
}

// findRange returns the row in idx whose [docLo, docHi) contains off. idx must
// be ordered by docLo.
func (p *Projection) findRange(idx []int, off int) (int, bool) {
	i := sort.Search(len(idx), func(k int) bool { return p.docLo[idx[k]] > off })
	if i > 0 {
		r := idx[i-1]
		if off < p.docHi[r] {
			return r, true
		}
	}
	return 0, false
}

// Text is the composition the projection lays out.
func (p *Projection) Text() string {
	if p == nil {
		return ""
	}
	return p.text
}

// Lines is the number of display rows, fold rows included.
func (p *Projection) Lines() int {
	if p == nil {
		return 0
	}
	return len(p.lines)
}

// At returns the row at line, clamped to the projection's range.
func (p *Projection) At(line int) DispLine {
	if p == nil || len(p.lines) == 0 {
		return DispLine{SessionLine: -1}
	}
	line = max(min(line, len(p.lines)-1), 0)
	return p.lines[line]
}

// RowText is the composition text a non-fold row draws. A fold row draws a
// marker instead and returns "".
func (p *Projection) RowText(line int) string {
	if p == nil || line < 0 || line >= len(p.lines) {
		return ""
	}
	return p.text[p.compLo[line]:p.compHi[line]]
}

// Fold reports the hidden run a fold row stands for, in session bytes. ok is
// false for a row that draws text.
func (p *Projection) Fold(line int) (group uint64, hiddenBytes int, ok bool) {
	if p == nil || line < 0 || line >= len(p.lines) {
		return 0, 0, false
	}
	d := p.lines[line]
	if d.Fold == 0 {
		return 0, 0, false
	}
	return d.Fold, p.docHi[line] - p.docLo[line], true
}

// DispOfDocLine is the first display row that shows any of sessionLine, or -1
// when the line shows nothing.
func (p *Projection) DispOfDocLine(sessionLine int) int {
	if p == nil || sessionLine < 0 || sessionLine >= len(p.first) {
		return -1
	}
	return p.first[sessionLine]
}

// DocAt maps a display cell to a session offset. A session-backed row yields the
// byte at column col (col is a column within that row's session line, clamped to
// [Lo, Hi]). A fold row yields the hidden run's session start; a
// composition-only row yields the seg's session cursor, so a caret can never
// land inside leased text.
func (p *Projection) DocAt(dispLine, col int) int {
	if p == nil || len(p.lines) == 0 {
		return -1
	}
	dispLine = max(min(dispLine, len(p.lines)-1), 0)
	d := p.lines[dispLine]
	if d.Fold != 0 || d.SessionLine < 0 {
		return p.docLo[dispLine]
	}
	c := max(min(col, d.Hi), d.Lo)
	return p.docLo[dispLine] + (c - d.Lo)
}

// DispOfDoc maps a session offset to a display cell. An offset inside a hidden
// run — including the run's first byte — maps to the fold row at column zero.
// off is clamped to [0, len(session)].
func (p *Projection) DispOfDoc(off int) (line, col int) {
	if p == nil || len(p.lines) == 0 {
		return -1, 0
	}
	off = max(min(off, p.n), 0)
	if r, ok := p.findRange(p.foldRows, off); ok {
		return r, 0
	}
	if r, ok := p.findRange(p.sessRows, off); ok {
		d := p.lines[r]
		return r, d.Lo + (off - p.docLo[r])
	}
	// A line-ending offset (a newline or end of file) maps to the end cell of
	// the row that shows the line's last visible byte.
	if r := p.lineEndRow(off); r >= 0 {
		return r, p.lines[r].Hi
	}
	return p.nearest(off)
}

// lineEndRow finds the session-backed row of off's line whose shown content ends
// at off, so a newline maps to the end cell of the line it terminates.
func (p *Projection) lineEndRow(off int) int {
	sl := projLineOf(p.starts, off)
	if sl < 0 || sl >= len(p.first) {
		return -1
	}
	ls := projLineStart(p.starts, sl)
	want := off - ls
	for r := p.first[sl]; r >= 0 && r < len(p.lines); r++ {
		d := p.lines[r]
		if d.SessionLine < 0 {
			continue
		}
		if d.SessionLine > sl {
			break
		}
		if d.Hi == want {
			return r
		}
	}
	return -1
}

// nearest is the fallback for an offset no row claims: the closest preceding
// row, or the first row.
func (p *Projection) nearest(off int) (int, int) {
	best := -1
	if i := sort.Search(len(p.sessRows), func(k int) bool { return p.docLo[p.sessRows[k]] > off }); i > 0 {
		best = p.sessRows[i-1]
	}
	if i := sort.Search(len(p.foldRows), func(k int) bool { return p.docLo[p.foldRows[k]] > off }); i > 0 {
		r := p.foldRows[i-1]
		if best < 0 || p.docLo[r] > p.docLo[best] {
			best = r
		}
	}
	if best < 0 {
		if len(p.lines) > 0 {
			return 0, 0
		}
		return -1, 0
	}
	if p.lines[best].SessionLine < 0 {
		return best, 0
	}
	return best, p.lines[best].Hi
}
