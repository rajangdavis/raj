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
func (p *Pane) MoveLines(delta int) {
	lines := p.touchedLines()
	if len(lines) == 0 {
		return
	}
	if delta < 0 && lines[0] == 0 {
		return
	}
	if delta > 0 && lines[len(lines)-1] >= p.File.Lines()-1 {
		return
	}

	type where struct{ line, col int }
	marks := make([]where, 0, p.Cursors.Count())
	for _, c := range p.Cursors.All() {
		l, col := p.File.LineCol(c.Head)
		marks = append(marks, where{l, col})
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

	restored := make([]Cursor, 0, len(marks))
	for _, m := range marks {
		off := p.File.OffsetAt(clamp(m.line+delta, 0, p.File.Lines()-1), m.col)
		restored = append(restored, Cursor{Head: off, Anchor: off, Goal: m.col})
	}
	p.Cursors.Replace(restored)
	p.FollowCursor()
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
