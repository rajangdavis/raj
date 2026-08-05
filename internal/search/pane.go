package search

import (
	"fmt"
	"sync"
	"time"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Pane is the search sidebar: a query field with a visible boundary, include
// and exclude glob fields, the regex and case toggles, and the results.
//
// Tab walks that ring and, past the last stop, hands focus to the editor.
// Shift+tab walks back but stops at the query field — once focus is in the
// editor, tab indents, and returning is a chord.
type Pane struct {
	Root   string
	Result Result

	query     widget.Input
	include   widget.Input
	exclude   widget.Input
	q         Query
	rows      []Row
	collapsed map[string]bool
	visited   bool
	list      widget.List
	spot      int

	// Searching happens off the event thread: Run walks the whole tree, and a
	// keystroke must never wait for it. The worker touches nothing the rest of
	// this file touches — it parks its result in pending, and apply installs it
	// on the event thread — so every field above stays single-threaded and only
	// the four below need the lock.
	//
	// gen is what makes cancellation unnecessary. Each search carries the
	// generation it was started for, and a result whose generation has moved on
	// is dropped. Without it a slow search for "f" can land after a fast one for
	// "func" and replace it.
	mu          sync.Mutex
	gen         int
	inflight    int
	timer       *time.Timer
	pending     Result
	havePending bool

	// search is Run unless a test substitutes something slower or ordered.
	// Debounce is the pause after the last keystroke before searching; zero
	// means the default.
	search   func(root string, q Query) Result
	Debounce time.Duration
}

// DefaultDebounce is how long the pane waits after the last keystroke before
// walking the tree. Typing is bursty and each character invalidates the last
// query, so the pause is what turns a word into one search instead of four.
const DefaultDebounce = 120 * time.Millisecond

// Row is one line of the results list: either a file header or a match under
// it. Grouping matters at sidebar width — repeating a truncated path on every
// hit spends the columns that should be showing the matching text.
type Row struct {
	Path  string
	Count int   // header only: matches in this file
	Match Match // zero for a header
	IsHdr bool
	Open  bool // header only: whether its matches are showing
}

// Rows exposes the flattened result tree, for tests.
func (p *Pane) Rows() []Row { return p.rows }

// List exposes the selection state, for tests.
func (p *Pane) List() *widget.List { return &p.list }

// group flattens matches into headers followed by their matches, skipping the
// matches of collapsed files. Run returns matches in walk order, so hits for
// one file are already adjacent.
//
// Collapse state is keyed by path and survives re-running the search, so
// refining a query does not re-expand everything you just folded away.
func (p *Pane) group() {
	p.rows = p.rows[:0]
	for i := 0; i < len(p.Result.Matches); {
		path := p.Result.Matches[i].Path
		j := i
		for j < len(p.Result.Matches) && p.Result.Matches[j].Path == path {
			j++
		}
		open := !p.collapsed[path]
		p.rows = append(p.rows, Row{Path: path, Count: j - i, IsHdr: true, Open: open})
		if open {
			for _, m := range p.Result.Matches[i:j] {
				p.rows = append(p.rows, Row{Path: path, Match: m})
			}
		}
		i = j
	}
}

// CollapseAll folds every file, which is the state a fresh search starts in
// when there are enough files that the flat list is unreadable.
func (p *Pane) CollapseAll() {
	for _, m := range p.Result.Matches {
		p.collapsed[m.Path] = true
	}
	p.group()
	p.list.Reset()
}

// toggle folds or unfolds the file under the cursor, keeping that header
// selected so repeated presses open and close the same group.
func (p *Pane) toggle() {
	if p.list.Sel >= len(p.rows) {
		return
	}
	path := p.rows[p.list.Sel].Path
	p.collapsed[path] = !p.collapsed[path]
	p.group()
	for i, r := range p.rows {
		if r.IsHdr && r.Path == path {
			p.list.Sel = i
			break
		}
	}
	p.list.Follow(len(p.rows))
}

// CollapseThreshold is how many files a result set may span before groups start
// folded. Below it the flat list is readable and folding is friction; above it
// the file names are what you are scanning for.
const CollapseThreshold = 5

// Stops in the focus ring, in tab order.
const (
	spotQuery = iota
	spotInclude
	spotExclude
	spotRegex
	spotCase
	spotWord
	spotResults
	spotCount
)

// NewPane returns a search pane rooted at a directory.
func NewPane(root string) *Pane {
	p := &Pane{Root: root, collapsed: map[string]bool{}}
	p.query = widget.Input{Label: "Search"}
	p.include = widget.Input{Label: "files to include"}
	p.exclude = widget.Input{Label: "files to exclude"}
	return p
}

// Focus restores the pane to wherever focus was when it was last left.
//
// Resetting to the query field means that opening a result and coming back
// costs six tabs to reach the list again, having already typed the query. The
// first time the pane opens there is nothing to restore, so it lands on the
// query field as you would expect.
func (p *Pane) Focus() {
	if !p.visited {
		p.spot = spotQuery
		p.visited = true
	}
}

// Handle applies an action. It returns a file to open with the line to jump to,
// and whether focus should leave for the editor.
func (p *Pane) Handle(a keys.Action, text string) (path string, line int, exit bool) {
	p.apply()
	// The jump actions are claimed before the focused input sees them: a text
	// field treats cmd+up as "go to the start of the line", which would make
	// the shortcut do nothing in the very place it is most useful.
	switch a {
	case keys.DocStart:
		p.spot = spotQuery
		return "", 0, false
	case keys.DocEnd:
		p.spot = spotResults
		return "", 0, false
	}
	if in := p.activeInput(); in != nil && in.Handle(a, text) {
		p.run()
		return "", 0, false
	}
	switch a {
	case keys.CycleFocus:
		if p.spot+1 >= spotCount {
			return "", 0, true
		}
		p.spot++
	case keys.CycleFocusBack:
		if p.spot > 0 {
			p.spot--
		}
	case keys.LineUp:
		p.list.Move(-1, len(p.rows))
	case keys.LineDown:
		p.list.Move(+1, len(p.rows))
	case keys.Confirm:
		if p.toggleAt(p.spot) {
			return "", 0, false
		}
		// On a header, enter folds; on a match, it opens the file. Folding is
		// the more common intent once results are grouped, and the file is
		// still one keystroke away through its first match.
		if p.spot == spotResults && p.list.Sel < len(p.rows) && p.rows[p.list.Sel].IsHdr {
			p.toggle()
			return "", 0, false
		}
		if p.spot == spotResults || p.spot == spotQuery {
			if m, ok := p.selected(); ok {
				return m.Path, m.Line, false
			}
		}
	case keys.CharLeft, keys.CharRight:
		if p.spot == spotResults {
			p.toggle()
		}
	case keys.None:
		// Space flips the focused toggle; the letters work from any toggle, so
		// r/c/w remain a shortcut once you know them.
		switch {
		case text == " ":
			p.toggleAt(p.spot)
		case p.spot >= spotRegex && p.spot <= spotWord:
			switch text {
			case "r":
				p.toggleAt(spotRegex)
			case "c":
				p.toggleAt(spotCase)
			case "w":
				p.toggleAt(spotWord)
			}
		}
	}
	return "", 0, false
}

// toggleAt flips the search option at a focus stop, reporting whether that stop
// was a toggle at all.
func (p *Pane) toggleAt(spot int) bool {
	switch spot {
	case spotRegex:
		p.q.Regex = !p.q.Regex
	case spotCase:
		p.q.Case = !p.q.Case
	case spotWord:
		p.q.Word = !p.q.Word
	default:
		return false
	}
	p.run()
	return true
}

// Options exposes the current toggle state, for tests.
func (p *Pane) Options() Query { return p.q }

func (p *Pane) activeInput() *widget.Input {
	switch p.spot {
	case spotQuery:
		return &p.query
	case spotInclude:
		return &p.include
	case spotExclude:
		return &p.exclude
	}
	return nil
}

// selected resolves the highlighted row to a match. Choosing a file header
// opens that file at its first hit rather than doing nothing.
func (p *Pane) selected() (Match, bool) {
	if p.list.Sel >= len(p.rows) {
		return Match{}, false
	}
	r := p.rows[p.list.Sel]
	if !r.IsHdr {
		return r.Match, true
	}
	if p.list.Sel+1 < len(p.rows) {
		return p.rows[p.list.Sel+1].Match, true
	}
	return Match{}, false
}

// run starts a search for the current field contents and returns immediately.
// The walk happens on a worker after the debounce; apply installs the result.
//
// An empty query is answered here rather than deferred: clearing the box should
// clear the results now, and there is nothing to walk for.
func (p *Pane) run() {
	p.q.Text = p.query.Text
	p.q.Include = p.include.Text
	p.q.Exclude = p.exclude.Text

	p.mu.Lock()
	p.gen++
	gen := p.gen
	// Stop reports whether it beat the timer. When it did, that search never
	// started and must not be counted as in flight.
	if p.timer != nil && p.timer.Stop() {
		p.inflight--
	}
	if p.q.Text == "" {
		p.pending, p.havePending = Result{}, true
		p.mu.Unlock()
		p.apply()
		return
	}
	q, root, fn := p.q, p.Root, p.searcher()
	p.inflight++
	p.timer = time.AfterFunc(p.debounce(), func() { p.work(gen, root, q, fn) })
	p.mu.Unlock()
}

// work runs one search off the event thread and parks the result if it is still
// the one being waited for.
func (p *Pane) work(gen int, root string, q Query, fn func(string, Query) Result) {
	res := fn(root, q)
	p.mu.Lock()
	if gen == p.gen {
		p.pending, p.havePending = res, true
	}
	p.inflight--
	p.mu.Unlock()
}

// apply installs a finished search. Event thread only — Handle and Render both
// call it, so a result is picked up on the next keystroke or the next tick.
func (p *Pane) apply() {
	p.mu.Lock()
	res, ok := p.pending, p.havePending
	p.pending, p.havePending = Result{}, false
	p.mu.Unlock()
	if !ok {
		return
	}
	p.Result = res
	p.group()
	if p.Result.Files > CollapseThreshold {
		p.CollapseAll()
	}
	p.list.Reset()
}

// Settle waits for any pending search to finish and installs it. Exported so
// tests can drive the pane a step at a time; the application never needs it,
// because the result arrives on the next tick on its own.
func (p *Pane) Settle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		p.apply()
		p.mu.Lock()
		busy := p.inflight > 0 || p.havePending
		p.mu.Unlock()
		if !busy {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

func (p *Pane) searcher() func(string, Query) Result {
	if p.search != nil {
		return p.search
	}
	return Run
}

func (p *Pane) debounce() time.Duration {
	if p.Debounce > 0 {
		return p.Debounce
	}
	return DefaultDebounce
}

// Render draws the pane.
func (p *Pane) Render(s *ui.Screen, x, y, w, h int, th widget.Theme, focused bool) {
	p.apply()
	if w < 6 || h < 12 {
		return
	}
	p.query.Focused = focused && p.spot == spotQuery
	p.include.Focused = focused && p.spot == spotInclude
	p.exclude.Focused = focused && p.spot == spotExclude

	p.query.Render(s, x, y, w, th)
	p.include.Render(s, x, y+3, w, th)
	p.exclude.Render(s, x, y+6, w, th)
	p.renderToggles(s, x+1, y+9, w-2, th, focused)
	p.renderResults(s, x, y+10, w, h-10, th, focused)
}

// renderToggles draws the three options as separate focus stops, each
// highlighted individually. Labels are compact so the row survives a narrow
// sidebar: .* regex, Aa case-sensitive, ab whole word.
func (p *Pane) renderToggles(s *ui.Screen, x, y, w int, th widget.Theme, focused bool) {
	items := []struct {
		spot  int
		on    bool
		label string
	}{
		{spotRegex, p.q.Regex, ".*"},
		{spotCase, p.q.Case, "Aa"},
		{spotWord, p.q.Word, "ab"},
	}
	col := 0
	for _, it := range items {
		style := th.Dim
		if it.on {
			style = th.Text
		}
		if focused && p.spot == it.spot {
			style = th.Selected
		}
		text := check(it.on) + it.label
		if col+len(text) > w {
			break
		}
		col += s.SetString(x+col, y, text, style, w-col) + 1
	}
}

func check(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

// renderResults draws the grouped tree: a relative path per file, then one
// indented row per hit showing the line number and the matching text.
func (p *Pane) renderResults(s *ui.Screen, x, y, w, h int, th widget.Theme, focused bool) {
	if h < 2 {
		return
	}
	header := fmt.Sprintf("%d results in %d files", len(p.Result.Matches), p.Result.Files)
	if p.Result.Err != nil {
		header = "bad pattern: " + p.Result.Err.Error()
	} else if p.Result.Capped {
		header += " (capped)"
	}
	s.Fill(x, y, w, 1, ui.DefaultStyle)
	s.SetString(x+1, y, widget.Truncate(header, w-2), th.Heading(focused && p.spot == spotResults), w-2)

	rows := h - 1
	p.list.Rows = rows
	p.list.Follow(len(p.rows))

	for row := 0; row < rows; row++ {
		i := p.list.Top + row
		if i >= len(p.rows) {
			break
		}
		r := p.rows[i]
		style := th.Focus(i == p.list.Sel, focused && p.spot == spotResults)
		if r.IsHdr && i != p.list.Sel {
			style = th.Text.Plus(ui.Bold)
		}
		label, indent := "", 1
		if r.IsHdr {
			marker := "▸ "
			if r.Open {
				marker = "▾ "
			}
			label = marker + fmt.Sprintf("%s (%d)",
				widget.TruncateLeft(relative(p.Root, r.Path), w-10), r.Count)
		} else {
			indent = 4
			label = fmt.Sprintf("%d: %s", r.Match.Line, trimIndent(r.Match.Text))
		}
		s.SetString(x+indent, y+1+row, widget.Truncate(label, w-indent-1), style, w-indent-1)
	}
}
