// Package picker is the quick-open overlay: a floating list with a fuzzy query,
// not a sidebar.
//
// It serves both cmd+p and cmd+shift+o because they are the same widget with
// different rows. Splitting them would mean a second overlay, a second focus
// state, a second keymap scope and a second renderer, all to display a list of
// strings that is filtered as you type — and the two would drift. What differs
// is where the rows come from and what choosing one means, which is two fields.
package picker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/keys"
	"raj/internal/symbols"
	"raj/internal/ui"
	"raj/internal/widget"
)

// MaxFiles bounds the index. A picker that takes seconds to open in a large
// repository is worse than one that quietly stops indexing.
const MaxFiles = 20000

// Mode is what the overlay is listing.
type Mode int

const (
	// Files lists the workspace, and choosing one opens it.
	Files Mode = iota
	// Symbols lists the declarations in one file, and choosing one jumps.
	Symbols
)

// Picker is the floating quick-open overlay.
type Picker struct {
	Root  string
	Open  bool
	mode  Mode
	items []entry
	shown []scored
	input widget.Input
	list  widget.List

	// pos is the line:col a paste carried, and path is the form of the query
	// it was attached to. Held rather than discarded so that pasting a
	// compiler line opens the file where the compiler was pointing. Cleared
	// by anything that changes the query, since a position that outlives the
	// path it came from would land in whatever file is chosen next.
	pos  Position
	path string

	// file is the document Symbols mode is listing, held so a chosen symbol
	// can name it.
	file string
}

// Position is a place in a file, 1-based, as a compiler or grep prints it. A
// zero Line means the paste carried no position.
type Position struct{ Line, Col int }

// entry is one row. label is what is shown and what the query is matched
// against; line is where choosing it goes, and is zero for a file.
type entry struct {
	label string
	line  int
}

type scored struct {
	entry
	score int
	hits  []int // byte offsets in the label that matched, for highlighting
}

// New builds a picker rooted at a directory.
func New(root string) *Picker {
	p := &Picker{Root: root}
	p.input = widget.Input{Label: "Go to file"}
	return p
}

// Show opens the file finder, reindexing so files created since last time
// appear.
func (p *Picker) Show() {
	p.reset(Files, "Go to file")
	p.index()
	p.filter()
}

// ShowSymbols opens the same overlay over one file's declarations. The path is
// held so that choosing a symbol names the file it came from, which is what
// lets a caller open and jump with the machinery it already has for a file.
func (p *Picker) ShowSymbols(path string, syms []symbols.Symbol) {
	p.reset(Symbols, "Go to symbol")
	p.file = path
	p.items = p.items[:0]
	for _, s := range syms {
		label := s.Name
		if s.Kind != "" {
			label += "  " + string(s.Kind)
		}
		p.items = append(p.items, entry{label: label, line: s.Line})
	}
	p.filter()
}

func (p *Picker) reset(m Mode, label string) {
	p.Open = true
	p.mode = m
	p.file = ""
	p.pos, p.path = Position{}, ""
	p.input.Label = label
	p.input.SetText("")
	p.input.Focused = true
}

// Mode is what the overlay is currently listing.
func (p *Picker) Mode() Mode { return p.mode }

// ActiveInput is the query field while the overlay is open.
func (p *Picker) ActiveInput() *widget.Input {
	if !p.Open {
		return nil
	}
	return &p.input
}

// Hide closes it.
func (p *Picker) Hide() { p.Open = false }

// Resolve turns an indexed path into one a caller can open.
//
// The index holds paths relative to Root because that is what the list shows
// and what the fuzzy score ranks: an absolute prefix is the same bytes on every
// entry, so it only dilutes the score and eats the width. But a relative path
// handed to a caller is resolved against the process working directory, not
// against the workspace — so `raj ~/code/thing` run from anywhere else opened a
// blank buffer named after the file that was picked, silently, and saving it
// would have written a new file next to wherever the shell happened to be. The
// two representations both have to exist; the seam between them is here.
func (p *Picker) Resolve(rel string) string {
	if rel == "" || p.Root == "" || filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(p.Root, rel)
}

// Scroll moves the visible window without moving the selection, for the wheel.
func (p *Picker) Scroll(delta int) { p.list.Scroll(delta, len(p.shown)) }

// Query is the current filter text.
func (p *Picker) Query() string { return p.input.Text }

// Results is how many files match the query.
func (p *Picker) Results() int { return len(p.shown) }

// Top is the highest-ranked match, or "" when nothing matches.
func (p *Picker) Top() string {
	if len(p.shown) == 0 {
		return ""
	}
	return p.shown[0].label
}

// Handle applies an action, returning a chosen path.
func (p *Picker) Handle(a keys.Action, text string) (path string) {
	switch a {
	case keys.Cancel:
		p.Hide()
		return ""
	case keys.LineUp:
		p.list.Move(-1, len(p.shown))
		return ""
	case keys.LineDown:
		p.list.Move(+1, len(p.shown))
		return ""
	case keys.Confirm:
		if p.list.Sel < len(p.shown) {
			return p.choose(p.shown[p.list.Sel])
		}
		return ""
	}
	if p.input.Handle(a, text) {
		p.filter()
	}
	return ""
}

// Paste sets the query from a pasted payload rather than inserting it verbatim.
//
// A paste into the picker is nearly always a path from somewhere else — a
// shell, a stack trace, a grep line — and those carry decoration the index does
// not: an absolute prefix, a `:line:col` suffix, a leading `./`. The fuzzy match
// is a subsequence test, so a single stray byte that appears nowhere in the
// path drops the result count to zero. The field held what was pasted and the
// list showed nothing, which reads as the paste having been ignored.
//
// So a paste is treated as a hint and narrowed until it matches: the payload as
// pasted, then with its position suffix removed, then relative to the root, then
// the base name alone. The first form with any match wins. When none match the
// original is restored, because a picker that shows nothing for what you pasted
// is at least honest about what it searched.
func (p *Picker) Paste(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	p.input.SetText(strings.TrimSpace(text))
	p.filter()
}

// PositionFor reports the place the query asked for, if the chosen path is the
// one it named.
//
// The check matters because narrowing can widen the query: "app.go:464" pasted
// in a tree with several app.go files searches for "app.go", and arrowing to a
// different one of them must not inherit 464. A suffix match is the rule —
// exact when the paste was already relative, and base-name when narrowing went
// that far.
func (p *Picker) PositionFor(chosen string) (Position, bool) {
	if p.pos.Line == 0 && p.pos.Col == 0 {
		return Position{}, false
	}
	if chosen != p.path && !strings.HasSuffix(chosen, string(filepath.Separator)+p.path) {
		return Position{}, false
	}
	return p.pos, true
}

// pasteCandidates lists the forms of a pasted payload to try, most specific
// first, without duplicates.
func pasteCandidates(text, root string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	out := []string{text}
	add := func(s string) {
		if s == "" || s == "." {
			return
		}
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}

	bare, _, _ := splitPosition(text)
	add(bare)
	bare = strings.TrimPrefix(bare, "./")
	add(bare)
	if root != "" && filepath.IsAbs(bare) {
		if rel, err := filepath.Rel(root, bare); err == nil && !strings.HasPrefix(rel, "..") {
			add(rel)
		}
	}
	add(filepath.Base(bare))
	return out
}

// splitPosition separates a trailing :line or :line:col from a path, which is
// how a compiler, a linter and `grep -n` all name a place in a file. A colon is
// legal in a file name, so only an all-digit tail is taken, and at most two
// segments of one.
//
// It reads right to left, so the column is found before the line. A single
// trailing number is a line, not a column: "app.go:464" is what every tool
// prints when it has only one to give.
func splitPosition(s string) (path string, line, col int) {
	for i := 0; i < 2; i++ {
		j := strings.LastIndexByte(s, ':')
		if j <= 0 || j == len(s)-1 || !allDigits(s[j+1:]) {
			break
		}
		n, ok := atoi(s[j+1:])
		if !ok {
			break
		}
		line, col = n, line
		s = s[:j]
	}
	return s, line, col
}

// atoi accepts a non-negative decimal and bounds it, so a pasted line number of
// forty digits cannot overflow into a negative offset.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30, true
		}
	}
	return n, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// choose closes the overlay and returns the path the choice names. A symbol
// answers with the file it lives in and leaves its line where PositionFor will
// find it, so both modes come out of Handle as "a path, possibly with a place
// in it" and the caller needs one path rather than two.
func (p *Picker) choose(s scored) string {
	p.Hide()
	if p.mode == Symbols {
		if p.file == "" {
			return ""
		}
		p.pos, p.path = Position{Line: s.line}, p.file
		return p.file
	}
	return p.Resolve(s.label)
}

func (p *Picker) index() {
	p.items = p.items[:0]
	filepath.WalkDir(p.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != p.Root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if len(p.items) >= MaxFiles {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(p.Root, path)
		if err == nil {
			p.items = append(p.items, entry{label: rel})
		}
		return nil
	})
}

// filter rescores every row against the query. An empty query lists everything,
// so cmd+p with no typing is a plain file list and cmd+shift+o with no typing is
// the file's outline in file order.
//
// The sort is stable and only runs for a non-empty query, which is what keeps
// that outline in file order: ties would otherwise be reordered by whatever the
// sort felt like, and a symbol list shuffled against the file it describes is
// worse than no list.
func (p *Picker) filter() {
	q := p.input.Text
	p.pos, p.path = Position{}, ""

	// The query as typed always wins if it matches anything at all.
	if p.score(q); len(p.shown) > 0 || q == "" {
		p.list.Reset()
		return
	}

	// Nothing matched, so treat the query as a path from somewhere else and
	// narrow it: without its position suffix, then relative to the root, then
	// the base name alone. The first form with a match wins.
	//
	// This runs on every query rather than only on a paste, because the editor
	// cannot tell the two apart. A terminal that does not honour bracketed
	// paste delivers a pasted path as individual keystrokes, and then the
	// query is a path with a `:464:12` on the end that no file contains — the
	// field is full and the list is empty, which reads as the paste having
	// been ignored. Narrowing here works whichever way the bytes arrived.
	_, line, col := splitPosition(q)
	for _, cand := range pasteCandidates(q, p.Root) {
		if cand == q {
			continue // already tried, and it is what we fall back to
		}
		if p.score(cand); len(p.shown) > 0 {
			p.pos, p.path = Position{line, col}, cand
			p.list.Reset()
			return
		}
	}

	// Nothing matched in any form. Show the query as given: a list that is
	// empty for what was actually typed is at least honest about what it
	// searched for.
	p.score(q)
	p.list.Reset()
}

// score fills shown with everything matching q, ranked. An empty query lists
// everything, so cmd+p with no typing is a plain file list and cmd+shift+o with
// no typing is the file's outline in file order.
//
// The sort is stable and only runs for a non-empty query, which is what keeps
// that outline in file order: ties would otherwise be reordered arbitrarily,
// and a symbol list shuffled against the file it describes is worse than none.
func (p *Picker) score(query string) {
	p.shown = p.shown[:0]
	q := strings.ToLower(query)
	for _, it := range p.items {
		if q == "" {
			p.shown = append(p.shown, scored{entry: it})
			continue
		}
		if s, hits, ok := fuzzy(it.label, q); ok {
			p.shown = append(p.shown, scored{it, s, hits})
		}
	}
	if q != "" {
		sort.SliceStable(p.shown, func(i, j int) bool { return p.shown[i].score > p.shown[j].score })
	}
}

// fuzzy scores a subsequence match.
//
// Scattered subsequence matches are the trap: "test" is a subsequence of
// in-t-ernal/ui/styl-e.go -> s -> t, so a naive per-character score ranks it
// alongside a file actually named test.go. The fix is to make contiguity
// dominate — a run of n adjacent characters scores quadratically, gaps cost,
// and an exact substring of the base name outweighs anything spread across
// directories.
// fuzzy scores a query against a path and reports which bytes matched.
//
// The match is anchored to the file name whenever the query fits inside it,
// and falls back to the whole path otherwise. Without that anchor the scan is
// greedy from the left and spends query characters on directories: typing
// buffer_test.go put internal/app/search_buffer_test.go above the file
// actually called buffer_test.go, because the b of the query was consumed by
// the b in "piecetable" and split the real name into a run of 1 and a run of
// 13, while the other path had no b before "buffer" and matched as one clean
// run of 14. Contiguity is quadratic and unbounded, so a longer accidental run
// outscored an exact name — and typing a file's own name is the single most
// common thing anyone does here.
//
// Anchoring makes that comparison fair rather than adding another bonus to
// outweigh it: both candidates are then scored over their names, both as one
// run, and the tie is broken by the prefix and equality bonuses that were
// always meant to decide it. A query containing a separator, or one that is
// not in the name at all, still matches across the path — that is what makes
// "app/app" or a fragment of a directory work.
func fuzzy(path, query string) (score int, hits []int, ok bool) {
	lower := strings.ToLower(path)
	baseAt := strings.LastIndexByte(path, filepath.Separator) + 1
	base := lower[baseAt:]

	// from is where scoring starts: the name when the query fits there.
	from := 0
	if subsequence(base, query) {
		from = baseAt
	}

	qi, run, gaps := 0, 0, 0
	for i := from; i < len(lower) && qi < len(query); i++ {
		if lower[i] != query[qi] {
			if qi > 0 {
				gaps++
			}
			run = 0
			continue
		}
		hits = append(hits, i)
		run++
		score += run * run * 3 // contiguity dominates
		if i >= baseAt {
			score += 8 // in the file name rather than a directory
		}
		if i == baseAt || i == 0 || lower[i-1] == filepath.Separator ||
			lower[i-1] == '_' || lower[i-1] == '-' || lower[i-1] == '.' {
			score += 12 // at a word boundary
		}
		qi++
	}
	if qi < len(query) {
		return 0, nil, false
	}

	switch {
	case base == query:
		// The name is exactly what was typed. Nothing else in the tree is a
		// better answer, and no accumulation of contiguity should be able to
		// claim otherwise.
		score += 600
	case strings.HasPrefix(base, query):
		score += 300
	case strings.Contains(base, query):
		score += 200
	case strings.Contains(lower, query):
		score += 60 // contiguous, but somewhere in the path
	}
	return score - gaps*4 - len(path)/4, hits, true
}

// subsequence reports whether every byte of query appears in s in order. It is
// the same test the scoring loop applies, run ahead of it to decide where to
// start.
func subsequence(s, query string) bool {
	if query == "" {
		return true
	}
	qi := 0
	for i := 0; i < len(s); i++ {
		if s[i] == query[qi] {
			qi++
			if qi == len(query) {
				return true
			}
		}
	}
	return false
}

// Render draws the overlay centred horizontally in the upper third, where
// VSCode puts it — close to the top so results have room, but not so high it
// looks like part of the tab bar.
func (p *Picker) Render(s *ui.Screen, cols, rows int, th widget.Theme) {
	if !p.Open {
		return
	}
	w := cols * 2 / 3
	if w < 30 {
		w = cols - 4
	}
	if w < 10 {
		return
	}
	h := 15
	if h > rows-4 {
		h = rows - 4
	}
	if h < 5 {
		return
	}
	x, y := (cols-w)/2, 2

	s.Fill(x, y, w, h, ui.DefaultStyle)
	widget.Box(s, x, y, w, h, th.BorderFocus)
	// Inset by one so the field's own border sits inside the overlay's rather
	// than doubling up on it.
	p.input.Render(s, x+2, y+1, w-4, th)

	top := y + 4
	p.list.Settle(h-5, len(p.shown))
	for row := 0; row < p.list.Rows; row++ {
		i := p.list.Top + row
		if i >= len(p.shown) {
			break
		}
		style := th.Focus(i == p.list.Sel, true)
		// Paths are recognisable by their tail and symbols by their head, so
		// they truncate from opposite ends.
		label := widget.Truncate(p.shown[i].label, w-4)
		if p.mode == Files {
			label = widget.TruncateLeft(p.shown[i].label, w-4)
		}
		s.Fill(x+1, top+row, w-2, 1, style)
		s.SetString(x+2, top+row, label, style, w-4)
	}
	noun := " files "
	if p.mode == Symbols {
		noun = " symbols "
	}
	s.SetString(x+2, y+h-1, " "+itoa(len(p.shown))+noun, th.Dim, w-4)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
