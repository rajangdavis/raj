package editor

import (
	"path/filepath"
	"strings"
)

// MoveLines shifts every touched line up or down by one, carrying the cursors
// with it.
//
// Cursors are recorded as (line, column) before the text moves and restored
// afterwards. Adjusting byte offsets instead looks simpler and is wrong: once
// the lines have swapped, an old offset points into whatever text now occupies
// that position, so the cursor ends up on the line that moved the other way.
//
// Both ends are recorded, not just the head. Restoring a collapsed cursor loses
// the selection, and losing the selection is not cosmetic here: the next press
// reads touchedLines() again and sees one line where the block was, so holding
// the chord tears a highlighted block apart a line at a time.
//
// The touched set can leave a gap at the far end: touchedLines() drops a line
// the selection only meets at column 0, so it stays where it is while the block
// moves past it. Moving DOWN would push that line into its own block, which
// displaces the very text the exclusion promised to spare, so such a move is
// refused rather than made. Moving UP restores an end that sat on that line to
// the block's new far edge, so the selection keeps covering the same text.
//
// Restoring a mark is a line permutation, not a uniform shift: the swap
// sequence is simulated over the only lines it can touch, and each mark is
// mapped through the result. A uniform line+delta shift collapses the cursor on
// the block's far boundary — both ends land on the same line and the selection
// is gone — where the permutation parks each end on the text it was watching.
func (p *Pane) MoveLines(delta int) {
	lines := p.touchedLines()
	if len(lines) == 0 {
		return
	}
	if delta < 0 && lines[0] == 0 {
		return
	}
	if delta > 0 && (lines[len(lines)-1] >= p.File.Lines()-1 || p.boundaryBlocksDown(lines)) {
		return
	}

	type where struct{ headLine, headCol, ancLine, ancCol int }
	marks := make([]where, 0, p.Cursors.Count())
	for _, c := range p.Cursors.All() {
		hl, hc := p.File.LineCol(c.Head)
		al, ac := p.File.LineCol(c.Anchor)
		marks = append(marks, where{hl, hc, al, ac})
	}

	if delta > 0 {
		for i := len(lines) - 1; i >= 0; i-- {
			p.swapLines(lines[i], lines[i]+1)
		}
	} else {
		for _, l := range lines {
			p.swapLines(l-1, l)
		}
	}

	// The permutation the swap sequence just performed, over the only lines it
	// can have moved: the block, the line below it, and — for an upward move —
	// the line above it that falls into the vacated slot. The array says which
	// old line now sits at each position, so a mark's new line is found where
	// the mark's old line sits.
	lo := lines[0]
	hi := lines[len(lines)-1] + 1
	if delta < 0 {
		lo = lines[0] - 1
		hi = lines[len(lines)-1]
	}
	at := make([]int, hi-lo+1)
	for i := range at {
		at[i] = lo + i
	}
	atSwap := func(a, b int) { at[a-lo], at[b-lo] = at[b-lo], at[a-lo] }
	if delta > 0 {
		for i := len(lines) - 1; i >= 0; i-- {
			atSwap(lines[i], lines[i]+1)
		}
	} else {
		for _, l := range lines {
			atSwap(l-1, l)
		}
	}
	moved := make([]int, len(at))
	for k, old := range at {
		moved[old-lo] = lo + k
	}

	restored := make([]Cursor, 0, len(marks))
	for _, m := range marks {
		head := p.followLine(moved, lo, hi, delta, lines, m.headLine, m.headCol)
		anchor := p.followLine(moved, lo, hi, delta, lines, m.ancLine, m.ancCol)
		restored = append(restored, Cursor{Head: head, Anchor: anchor, Goal: m.headCol})
	}
	p.Cursors.Replace(restored)
	p.FollowCursor()
}

// boundaryBlocksDown reports whether a selection ends at column 0 of the line
// a downward move would push into its own block. touchedLines() left that line
// out so it stays in place, and a downward move cannot both happen and keep
// that promise.
func (p *Pane) boundaryBlocksDown(lines []int) bool {
	below := lines[len(lines)-1] + 1
	for _, c := range p.Cursors.All() {
		lo, hi := c.Range()
		if p.File.LineOf(lo) < below && hi == p.File.LineStart(below) {
			return true
		}
	}
	return false
}

// followLine maps one recorded end through the move's permutation to the offset
// it now points at.
//
// The only exception is the far end of an upward-moving block: an end that sat
// at column 0 of the line below the block lands on the block's new far edge
// rather than on that line itself, or the selection reaches past the block into
// the line the move deliberately left behind.
func (p *Pane) followLine(moved []int, lo, hi, delta int, lines []int, line, col int) int {
	if delta < 0 && line == lines[len(lines)-1]+1 && col == 0 {
		return p.File.LineStart(lines[len(lines)-1] + delta + 1)
	}
	if line < lo || line > hi {
		return p.File.OffsetAt(line, col)
	}
	return p.File.OffsetAt(moved[line-lo], col)
}

// swapLines exchanges two adjacent lines, a before b.
func (p *Pane) swapLines(a, b int) {
	if a < 0 || b >= p.File.Lines() || a >= b {
		return
	}
	textA, textB := p.File.Line(a), p.File.Line(b)
	start := p.File.LineStart(a)
	end := p.File.LineEnd(b)
	p.applyEdit(start, end-start, textB+"\n"+textA)
}

// CopyLines duplicates every touched line above or below itself.
func (p *Pane) CopyLines(delta int) {
	lines := p.touchedLines()
	for i := len(lines) - 1; i >= 0; i-- {
		text := p.File.Line(lines[i])
		at := p.File.LineStart(lines[i])
		if delta > 0 {
			at = p.File.LineEnd(lines[i])
			p.applyEdit(at, 0, "\n"+text)
			continue
		}
		p.applyEdit(at, 0, text+"\n")
	}
	p.Cursors.Normalize()
}

// wordAt returns the word surrounding an offset, and its bounds.
func (p *Pane) wordAt(off int) (string, int, int) {
	line := p.File.LineOf(off)
	start := p.File.LineStart(line)
	text := p.File.Line(line)
	i := off - start
	if i > len(text) {
		i = len(text)
	}
	lo := i
	for lo > 0 && isWord(rune(text[lo-1])) {
		lo--
	}
	hi := i
	for hi < len(text) && isWord(rune(text[hi])) {
		hi++
	}
	return text[lo:hi], start + lo, start + hi
}

// searchTerm is what cmd+d and cmd+shift+L look for: the primary selection, or
// the word under the cursor when nothing is selected.
//
// selecting reports that this call created the selection rather than reusing
// one. cmd+d on a bare cursor selects the word and stops there; only the second
// press adds a cursor, which is what VSCode does and what makes the key safe to
// lean on.
func (p *Pane) searchTerm() (term string, selecting, ok bool) {
	c := p.Cursors.Primary()
	if c.HasSelection() {
		lo, hi := c.Range()
		return p.File.Slice(lo, hi-lo), false, true
	}
	word, lo, hi := p.wordAt(c.Head)
	if word == "" {
		return "", false, false
	}
	p.Cursors.Set(hi, lo)
	return word, true, true
}

// AddNextOccurrence selects the next match of the current selection, adding a
// cursor. It wraps to the start of the document, so repeated presses eventually
// cover every occurrence.
func (p *Pane) AddNextOccurrence() {
	term, selecting, ok := p.searchTerm()
	if !ok || selecting {
		return
	}
	text := p.File.Text()
	taken := map[int]bool{}
	last := 0
	for _, c := range p.Cursors.All() {
		lo, _ := c.Range()
		taken[lo] = true
		if lo > last {
			last = lo
		}
	}
	for _, from := range []int{last + 1, 0} {
		for i := indexFrom(text, term, from); i >= 0; i = indexFrom(text, term, i+1) {
			if !taken[i] {
				p.Cursors.Add(i+len(term), i)
				p.FollowCursor()
				return
			}
		}
	}
}

// SelectAllOccurrences puts a cursor on every match of the current selection.
func (p *Pane) SelectAllOccurrences() {
	term, _, ok := p.searchTerm()
	if !ok {
		return
	}
	text := p.File.Text()
	first := true
	for i := indexFrom(text, term, 0); i >= 0; i = indexFrom(text, term, i+1) {
		if first {
			p.Cursors.Set(i+len(term), i)
			first = false
			continue
		}
		p.Cursors.Add(i+len(term), i)
	}
}

func indexFrom(text, term string, from int) int {
	if from >= len(text) || term == "" {
		return -1
	}
	if i := strings.Index(text[from:], term); i >= 0 {
		return from + i
	}
	return -1
}

// ToggleComment comments or uncomments every touched line. Following most
// editors, a block is uncommented only when every non-blank line in it is
// already commented; otherwise the whole block is commented.
func (p *Pane) ToggleComment() {
	token := commentToken(p.File.Path)
	if token == "" {
		return
	}
	lines := p.touchedLines()
	if len(lines) == 0 {
		return
	}
	allCommented, indent := true, -1
	for _, l := range lines {
		text := p.File.Line(l)
		trimmed := strings.TrimLeft(text, " \t")
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, token) {
			allCommented = false
		}
		if n := len(text) - len(trimmed); indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		indent = 0
	}

	for i := len(lines) - 1; i >= 0; i-- {
		text := p.File.Line(lines[i])
		start := p.File.LineStart(lines[i])
		trimmed := strings.TrimLeft(text, " \t")
		if trimmed == "" {
			continue
		}
		if allCommented {
			at := start + len(text) - len(trimmed)
			n := len(token)
			if strings.HasPrefix(trimmed[n:], " ") {
				n++ // remove the space the comment was inserted with
			}
			p.applyEdit(at, n, "")
			continue
		}
		p.applyEdit(start+indent, 0, token+" ")
	}
	p.Cursors.Normalize()
}

// commentToken is the line-comment marker for a file, by extension. Only line
// comments: block comments need balanced insertion and are a worse fit for a
// per-line toggle.
func commentToken(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".c", ".h", ".cpp", ".hpp", ".cc", ".java", ".js", ".jsx", ".ts",
		".tsx", ".rs", ".swift", ".kt", ".scala", ".zig", ".dart", ".php", ".cs":
		return "//"
	case ".py", ".rb", ".sh", ".bash", ".zsh", ".fish", ".yaml", ".yml", ".toml",
		".conf", ".cfg", ".ini", ".pl", ".r", ".jl", ".nix", ".tf", ".dockerfile":
		return "#"
	case ".lua", ".sql", ".hs", ".elm":
		return "--"
	case ".vim":
		return `"`
	case ".lisp", ".clj", ".el", ".scm":
		return ";"
	}
	if filepath.Base(path) == "Makefile" || filepath.Base(path) == "Dockerfile" {
		return "#"
	}
	return ""
}
