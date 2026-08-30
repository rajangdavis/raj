// Package problems is the sidebar pane listing diagnostics across the
// workspace.
//
// It exists because the gutter and the status line only answer "what is wrong
// with the line I am standing on". That is enough to fix a problem you have
// already found and useless for finding one three files away — which is the
// question you actually have after a build fails.
//
// The shape is the search pane's: grouped by file, collapsible, enter to jump.
// Deliberately so. A list of results across a workspace is a list of results
// across a workspace, and two panes that answer the same question in two
// different shapes is one shape too many to learn.
//
// Unlike the search pane there is no query and no worker. Diagnostics arrive
// unasked and are already in a store, so this pane is a view over that store
// and nothing more.
package problems

import (
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/keys"
	"raj/internal/lsp"
	"raj/internal/ui"
	"raj/internal/widget"
)

// File is one file's problems, as the pane is given them.
type File struct {
	Path  string
	Items []lsp.Diagnostic
}

// Row is one visible line: a file heading or one problem under it.
type Row struct {
	IsHdr bool
	Path  string
	Open  bool
	Item  lsp.Diagnostic
	// Errors and Warnings are the counts on a heading, so a folded group still
	// says how much it is hiding.
	Errors, Warnings int
}

// Pane is the list.
type Pane struct {
	files  []File
	folded map[string]bool
	rows   []Row
	list   widget.List

	// dirty records that rows need rebuilding. Rebuilding on every Render
	// would sort the whole workspace once a frame; rebuilding on Set, on fold
	// and on a filter change is enough, because nothing else changes what is
	// displayed.
	dirty bool

	// The filters. On a large workspace mid-refactor the unfiltered list is
	// hundreds of rows, and the two questions people actually ask of it are
	// "what is broken" and "what is broken in what I am looking at".
	errorsOnly bool
	openOnly   bool
	// open is which paths have a tab. Supplied by the app rather than worked
	// out here: this package is given diagnostics and knows nothing about tabs,
	// and inverting that to let it ask would make the pane depend on the editor.
	open map[string]bool

	// spot is the focus stop, in the order things are drawn.
	spot int
}

// The focus stops. The toggles come before the list, so tab order and reading
// order are the same thing — the rule the explorer settled on after having them
// the other way round.
const (
	spotErrors = iota
	spotOpen
	spotList
	spotCount
)

func New() *Pane {
	return &Pane{folded: map[string]bool{}, open: map[string]bool{}, spot: spotList}
}

// SetOpenPaths tells the pane which files have a tab, for the "open files"
// filter.
func (p *Pane) SetOpenPaths(paths []string) {
	next := make(map[string]bool, len(paths))
	for _, path := range paths {
		next[path] = true
	}
	if len(next) == len(p.open) {
		same := true
		for path := range next {
			if !p.open[path] {
				same = false
				break
			}
		}
		if same {
			return // unchanged: do not dirty the rows on every frame
		}
	}
	p.open = next
	if p.openOnly {
		p.dirty = true
	}
}

// Filters is what the pane is currently showing, for tests and for the app.
func (p *Pane) Filters() (errorsOnly, openOnly bool) { return p.errorsOnly, p.openOnly }

// keep reports whether a diagnostic passes the severity filter.
func (p *Pane) keep(it lsp.Diagnostic) bool {
	return !p.errorsOnly || rank(it.Severity) == 0
}

// Set replaces the pane's contents.
//
// The selection is kept by identity rather than by index where it can be:
// diagnostics are republished on every keystroke that reaches the server, so an
// index-based selection would walk under the cursor while you read the list.
func (p *Pane) Set(files []File) {
	var selPath string
	var selLine = -1
	if r, ok := p.selected(); ok {
		selPath = r.Path
		if !r.IsHdr {
			selLine = r.Item.Range.Start.Line
		}
	}

	p.files = append([]File(nil), files...)
	// Sorted by path so the list is stable between publishes. A list that
	// reorders itself as servers report is one you cannot read.
	sort.SliceStable(p.files, func(i, j int) bool { return p.files[i].Path < p.files[j].Path })
	p.dirty = true
	p.build()
	p.restore(selPath, selLine)
}

// restore puts the selection back on the same problem, or as near as it can.
func (p *Pane) restore(path string, line int) {
	if path == "" {
		return
	}
	for i, r := range p.rows {
		if r.Path != path {
			continue
		}
		if line < 0 && r.IsHdr {
			p.list.Sel = i
			return
		}
		if line >= 0 && !r.IsHdr && r.Item.Range.Start.Line == line {
			p.list.Sel = i
			return
		}
	}
	// The problem is gone — fixed, most likely, which is the good case. Land
	// on the file's heading if it still has any, rather than jumping to
	// whatever happens to be at the old index.
	for i, r := range p.rows {
		if r.IsHdr && r.Path == path {
			p.list.Sel = i
			return
		}
	}
	p.list.Sel = 0
}

// build flattens the files into rows, honouring which groups are folded.
func (p *Pane) build() {
	if !p.dirty {
		return
	}
	p.dirty = false
	p.rows = p.rows[:0]
	for _, f := range p.files {
		if len(f.Items) == 0 || (p.openOnly && !p.open[f.Path]) {
			continue
		}
		errs, warns, kept := 0, 0, 0
		for _, it := range f.Items {
			if !p.keep(it) {
				continue
			}
			kept++
			switch rank(it.Severity) {
			case 0:
				errs++
			case 1:
				warns++
			}
		}
		// A file whose problems are all filtered out loses its heading too. A
		// heading reading "0E 0W" is a row that says nothing and still costs a
		// line, which on a filtered list is most of what you were trying to
		// remove.
		if kept == 0 {
			continue
		}
		open := !p.folded[f.Path]
		p.rows = append(p.rows, Row{
			IsHdr: true, Path: f.Path, Open: open,
			Errors: errs, Warnings: warns,
		})
		if !open {
			continue
		}
		for _, it := range f.Items {
			if p.keep(it) {
				p.rows = append(p.rows, Row{Path: f.Path, Item: it})
			}
		}
	}
	if p.list.Sel >= len(p.rows) {
		p.list.Sel = len(p.rows) - 1
	}
	if p.list.Sel < 0 {
		p.list.Sel = 0
	}
}

// Rows is what the pane is showing, for tests and for the pointer.
func (p *Pane) Rows() []Row { p.build(); return p.rows }

// Count is how many problems the pane holds, headings excluded.
func (p *Pane) Count() int {
	n := 0
	for _, f := range p.files {
		n += len(f.Items)
	}
	return n
}

// Focus resets to the top of the list.
func (p *Pane) Focus() { p.build() }

func (p *Pane) selected() (Row, bool) {
	if p.list.Sel < 0 || p.list.Sel >= len(p.rows) {
		return Row{}, false
	}
	return p.rows[p.list.Sel], true
}

// Handle consumes a key. It returns a file and line to open, and whether focus
// should leave the pane.
//
// There is no field to tab between, so tab leaves in one step rather than
// cycling through components the way the search pane does.
func (p *Pane) Handle(action keys.Action) (path string, line int, exit bool) {
	p.build()
	// The toggles come first: on a toggle, up and down are how you get back to
	// the list, and a list that read them as "previous row" would trap focus.
	if p.spot != spotList {
		switch action {
		case keys.Confirm:
			p.flip(p.spot)
			return "", 0, false
		case keys.LineDown, keys.CycleFocus:
			p.spot++
			return "", 0, false
		case keys.LineUp, keys.CycleFocusBack:
			if p.spot == 0 {
				return "", 0, true // off the top of the pane
			}
			p.spot--
			return "", 0, false
		case keys.Cancel, keys.Indent, keys.Outdent:
			return "", 0, true
		}
		return "", 0, false
	}
	switch action {
	case keys.CycleFocusBack:
		p.spot = spotOpen
		return "", 0, false
	case keys.LineUp:
		p.list.Move(-1, len(p.rows))
	case keys.LineDown:
		p.list.Move(+1, len(p.rows))
	case keys.PageUp:
		p.list.Move(-p.list.Rows, len(p.rows))
	case keys.PageDown:
		p.list.Move(+p.list.Rows, len(p.rows))
	case keys.Confirm:
		return p.activate()
	case keys.Indent, keys.Outdent, keys.Cancel:
		return "", 0, true
	}
	return "", 0, false
}

// flip toggles one filter and rebuilds.
//
// The selection is not preserved across a filter change. Set goes to some
// trouble to keep it by identity, because diagnostics are republished under the
// cursor while you read; a filter change is the opposite case — you asked for a
// different list, and landing at the top of it is what you meant.
func (p *Pane) flip(spot int) {
	switch spot {
	case spotErrors:
		p.errorsOnly = !p.errorsOnly
	case spotOpen:
		p.openOnly = !p.openOnly
	default:
		return
	}
	p.dirty = true
	p.list.Sel = 0
	p.build()
}

// toggle is one drawn filter: which stop it is, its state, and its label.
type toggle struct {
	spot int
	on   bool
	text string
	col  int
}

// toggles lays the filters out across w columns, dropping any that do not fit.
// The renderer and the pointer both go through it, so a filter cannot be drawn
// in one place and clicked in another.
func (p *Pane) toggles(w int) []toggle {
	all := []toggle{
		{spot: spotErrors, on: p.errorsOnly, text: "errors"},
		{spot: spotOpen, on: p.openOnly, text: "open files"},
	}
	out := make([]toggle, 0, len(all))
	col := 0
	for _, it := range all {
		it.text = check(it.on) + it.text
		if col+len(it.text) > w {
			break
		}
		it.col = col
		out = append(out, it)
		col += len(it.text) + 1
	}
	return out
}

func check(on bool) string {
	if on {
		return "[x] "
	}
	return "[ ] "
}

// activate folds a heading or opens a problem, which is the same split enter
// makes in the search pane and in the tree.
func (p *Pane) activate() (string, int, bool) {
	r, ok := p.selected()
	if !ok {
		return "", 0, false
	}
	if r.IsHdr {
		p.folded[r.Path] = !p.folded[r.Path]
		p.dirty = true
		p.build()
		return "", 0, false
	}
	// One-based, because that is what every other jump in raj takes and what
	// the row itself displays. LSP counts from zero.
	return r.Path, r.Item.Range.Start.Line + 1, false
}

// Rows above the list: the heading and the filter row. Render and ClickAt both
// measure from these, so the list cannot be drawn at one offset and clicked at
// another.
const headRows = 2

// ClickAt handles a press at (dx, dy) relative to the pane's origin.
func (p *Pane) ClickAt(dx, dy, w, h int) (path string, line int, ok bool) {
	p.build()
	if dy < 0 || dy >= h {
		return "", 0, false
	}
	if dy == 1 {
		// The filters are drawn inset by one column, so the press is measured
		// from the same origin they were laid out against.
		for _, it := range p.toggles(w - 2) {
			if col := dx - 1; col >= it.col && col < it.col+len(it.text) {
				p.spot = it.spot
				p.flip(it.spot)
				return "", 0, true
			}
		}
		return "", 0, true // the rest of the row is still the pane's
	}
	if dy < headRows {
		return "", 0, false // the heading
	}
	i := p.list.Top + dy - headRows
	if i >= len(p.rows) {
		return "", 0, false
	}
	p.spot = spotList
	p.list.Sel = i
	path, line, _ = p.activate()
	return path, line, true
}

// Render draws the pane.
func (p *Pane) Render(s *ui.Screen, x, y, w, h int, th widget.Theme, focused bool) {
	p.build()
	if w < 8 || h < 2 {
		return
	}
	s.SetString(x+1, y, widget.Truncate(p.heading(), w-2),
		th.Heading(focused && p.spot == spotList), w-2)
	p.renderFilters(s, x+1, y+1, w-2, th, focused)

	rows := h - headRows
	p.list.Rows = rows
	p.list.Settle(rows, len(p.rows))
	if len(p.rows) == 0 {
		s.SetString(x+1, y+headRows, p.empty(), th.Dim, w-2)
		return
	}
	for row := 0; row < rows; row++ {
		i := p.list.Top + row
		if i >= len(p.rows) {
			break
		}
		r := p.rows[i]
		style := th.Focus(i == p.list.Sel, focused && p.spot == spotList)
		s.Fill(x, y+headRows+row, w, 1, style)
		s.SetString(x, y+headRows+row, widget.Truncate(p.label(r), w), style, w)
	}
}

func (p *Pane) renderFilters(s *ui.Screen, x, y, w int, th widget.Theme, focused bool) {
	for _, it := range p.toggles(w) {
		style := th.Dim
		if it.on {
			style = th.Text
		}
		if focused && p.spot == it.spot {
			style = th.Selected
		}
		s.SetString(x+it.col, y, it.text, style, w-it.col)
	}
}

// empty says why the list is empty, because "no problems" under an active
// filter is a lie — the workspace may be full of them.
func (p *Pane) empty() string {
	if p.Count() > 0 {
		return "no problems match the filter"
	}
	return "no problems"
}

// heading counts the whole workspace, so a folded pane still says how much
// there is.
func (p *Pane) heading() string {
	errs, warns := 0, 0
	for _, f := range p.files {
		for _, it := range f.Items {
			switch rank(it.Severity) {
			case 0:
				errs++
			case 1:
				warns++
			}
		}
	}
	if errs == 0 && warns == 0 {
		return "PROBLEMS"
	}
	return "PROBLEMS  " + counts(errs, warns)
}

// label is one row's text.
func (p *Pane) label(r Row) string {
	if r.IsHdr {
		mark := "▾ "
		if !r.Open {
			mark = "▸ "
		}
		return " " + mark + filepath.Base(r.Path) + "  " + counts(r.Errors, r.Warnings)
	}
	// Line number first, then severity, then the message. The number is what
	// the eye scans for when comparing against a compiler's output, and the
	// message is the part that gets truncated in a narrow pane — so it goes
	// last, where losing its tail costs least.
	msg := strings.ReplaceAll(r.Item.Message, "\n", " ")
	return "    " + itoa(r.Item.Range.Start.Line+1) + ": " + mark(r.Item.Severity) + " " + msg
}

func counts(errs, warns int) string {
	switch {
	case errs > 0 && warns > 0:
		return itoa(errs) + "E " + itoa(warns) + "W"
	case errs > 0:
		return itoa(errs) + "E"
	case warns > 0:
		return itoa(warns) + "W"
	}
	return ""
}

// rank and mark mirror the app's severity handling. They are duplicated rather
// than imported because the app imports this package, not the other way round —
// and the alternative, a shared package for two switch statements, is more
// indirection than the duplication costs. The tests assert they agree.
func rank(sev int) int {
	switch sev {
	case 1, 0:
		return 0
	case 2:
		return 1
	case 3:
		return 2
	case 4:
		return 3
	}
	return 4
}

func mark(sev int) string {
	switch rank(sev) {
	case 0:
		return "E"
	case 1:
		return "W"
	case 2:
		return "i"
	}
	return "·"
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
