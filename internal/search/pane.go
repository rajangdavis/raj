package search

import (
	"context"
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
	// compact records the layout the last Render chose, so the focus ring can
	// agree with it. Height is not known at Handle time and the alternative —
	// threading it through every caller — would put a rendering concern into
	// the key path.
	compact bool
	list    widget.List
	spot    int

	// Searching happens off the event thread: Run walks the whole tree, and a
	// keystroke must never wait for it. The worker touches nothing the rest of
	// this file touches — it parks its result in pending, and apply installs it
	// on the event thread — so every field above stays single-threaded and only
	// the four below need the lock.
	//
	// gen keeps results ORDERED: each search carries the generation it was
	// started for, and a result whose generation has moved on is dropped.
	// Without it a slow search for "f" can land after a fast one for "func"
	// and replace it.
	//
	// gen does not make the abandoned search stop, which is a separate and
	// more expensive problem — see cancel.
	mu          sync.Mutex
	gen         int
	inflight    int
	timer       *time.Timer
	pending     Result
	havePending bool

	// cancel stops the walk that is currently running. Dropping a stale result
	// is not the same as not computing it: on a large repository a search
	// outlives several keystrokes, so without this the pane accumulates a full
	// concurrent walk per character typed, each one reading every file for an
	// answer that is already obsolete. The cost lands on the disk and the
	// garbage collector, so the symptom is a laggy editor rather than a slow
	// search.
	cancel context.CancelFunc

	// Notify is called when a finished result has been parked, so the event
	// loop can come back for it instead of waiting out the tick. Optional: a
	// nil Notify simply means the result is picked up on the next pass, which
	// is what tests that drive the pane directly want.
	//
	// It is a plain func rather than a ui.Host so that this package keeps
	// knowing nothing about who is driving it, and it is called OFF the event
	// thread — whatever it does must be safe from a worker goroutine.
	Notify func()

	// lastDur is how long the last completed search took, and abandoned counts
	// the ones cancelled before finishing. Both are read by the debug pane:
	// "why is this slow" is unanswerable without them.
	lastDur   time.Duration
	abandoned int

	// search is RunContext unless a test substitutes something slower or
	// ordered. Debounce pins the pause after the last keystroke; zero lets the
	// pane choose one from how long searches here actually take.
	search   func(ctx context.Context, root string, q Query) Result
	Debounce time.Duration

	// Buffers supplies the contents of open documents, so a search sees what
	// is on screen rather than what was last written to disk. Called on the
	// event thread when a search is scheduled — never from the worker, where
	// reading a buffer the user is typing into would be a race.
	//
	// nil means "search the disk", which is what the pane did before and what
	// a test driving it directly wants.
	Buffers func() Docs
}

// The debounce window adapts to the repository, because one constant cannot
// serve both. At 120 ms on a tree that answers in 15 ms the pane feels sluggish
// for no reason; on a tree that takes two seconds, 120 ms is indistinguishable
// from no debounce at all, since the next keystroke lands long before the walk
// returns.
//
// So the pause tracks the measured cost of searching this tree: half the last
// search's duration, clamped. Half rather than all, because cancellation makes
// an early start cheap to undo — the aim is to stop starting walks faster than
// they can be thrown away, not to guarantee only one is ever in flight.
const (
	DefaultDebounce = 120 * time.Millisecond // before anything has been measured
	MinDebounce     = 60 * time.Millisecond
	MaxDebounce     = 500 * time.Millisecond
)

// Row is one line of the results list: either a file header or a match under
// it. Grouping matters at sidebar width — repeating a truncated path on every
// hit spends the columns that should be showing the matching text.
type Row struct {
	Path  string
	Count int // header only: matches shown for this file
	// Total is how many the file actually holds. It exceeds Count once the
	// file passes MaxPerFile, and the header says so rather than letting the
	// shown number pass for the real one.
	Total int
	Match Match // zero for a header
	IsHdr bool
	Open  bool // header only: whether its matches are showing
}

// Rows exposes the flattened result tree, for tests.
func (p *Pane) Rows() []Row { return p.rows }

// List exposes the selection state, for tests.
func (p *Pane) List() *widget.List { return &p.list }

// Scroll moves the results view without moving the selection, for the wheel.
func (p *Pane) Scroll(delta int) { p.list.Scroll(delta, len(p.rows)) }

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
		total := j - i
		if n, ok := p.Result.Counts[path]; ok && n > total {
			total = n
		}
		p.rows = append(p.rows, Row{Path: path, Count: j - i, Total: total, IsHdr: true, Open: open})
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
	if in := p.ActiveInput(); in != nil && in.Handle(a, text) {
		p.run()
		return "", 0, false
	}
	switch a {
	case keys.CycleFocus:
		n := p.spot
		for {
			if n++; n >= spotCount {
				return "", 0, true
			}
			if p.visible(n) {
				break
			}
		}
		p.spot = n
	case keys.CycleFocusBack:
		// Symmetric with tab walking off the last component: the pane is a
		// segment of the ring with an exit at each end, and both lead to the
		// editor. Wrapping round to the results instead would make backwards
		// mean something different from forwards, and land focus on the far
		// end of the pane rather than out of it.
		//
		// The original reason for stopping here does not apply to leaving. It
		// was that tab indents in the editor, so a one-key route back IN would
		// make editing interruptible — and that is untouched, since shift+tab
		// outdents once focus is in the document.
		n := p.spot
		for {
			if n == 0 {
				return "", 0, true
			}
			n--
			if p.visible(n) {
				break
			}
		}
		p.spot = n
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

// ActiveInput is the field the pane is typing into, or nil when focus is on a
// toggle or the results. The application asks so that cut and copy act on what
// the eye is on rather than on the document underneath.
func (p *Pane) ActiveInput() *widget.Input {
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
	// Stop the walk that is already running, not just its result.
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
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
	// The snapshot is taken here, on the event thread, and handed to the
	// worker by value. Taking it inside the worker would read a piece table
	// concurrently with the keystrokes that are still editing it.
	q, root, fn := p.q, p.Root, p.searcher(p.snapshot())
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.inflight++
	p.timer = time.AfterFunc(p.debounce(), func() { p.work(ctx, gen, root, q, fn) })
	p.mu.Unlock()
}

// work runs one search off the event thread and parks the result if it is still
// the one being waited for.
func (p *Pane) work(ctx context.Context, gen int, root string, q Query, fn func(context.Context, string, Query) Result) {
	start := time.Now()
	res := fn(ctx, root, q)
	elapsed := time.Since(start)

	p.mu.Lock()
	if res.Stopped {
		p.abandoned++
	} else {
		// Only a search that ran to completion says anything about how long
		// searching this tree costs; a cancelled one stopped early by
		// definition and would bias the window downwards.
		p.lastDur = elapsed
	}
	parked := gen == p.gen && !res.Stopped
	if parked {
		p.pending, p.havePending = res, true
	}
	p.inflight--
	notify := p.Notify
	p.mu.Unlock()

	// Outside the lock: Notify reaches the event loop, and holding the pane's
	// lock while doing that invites a deadlock the moment the loop calls back
	// into the pane.
	if parked && notify != nil {
		notify()
	}
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

// snapshot copies the open documents. Event thread only.
func (p *Pane) snapshot() Docs {
	if p.Buffers == nil {
		return nil
	}
	return p.Buffers()
}

// searcher binds the snapshot to the search function, which keeps the
// substitution seam a test uses at three arguments rather than four: a test
// that replaces the search entirely has no disk to disagree with.
func (p *Pane) searcher(open Docs) func(context.Context, string, Query) Result {
	if p.search != nil {
		return p.search
	}
	return func(ctx context.Context, root string, q Query) Result {
		return RunDocs(ctx, root, q, open)
	}
}

// debounce reports the pause to use before the next search. Callers hold mu.
func (p *Pane) debounce() time.Duration {
	if p.Debounce > 0 {
		return p.Debounce
	}
	if p.lastDur == 0 {
		return DefaultDebounce
	}
	d := p.lastDur / 2
	if d < MinDebounce {
		return MinDebounce
	}
	if d > MaxDebounce {
		return MaxDebounce
	}
	return d
}

// stopForTest cancels any running search so a test does not leak a goroutine
// blocked on a fake searcher.
func (p *Pane) stopForTest() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.mu.Unlock()
}

// InFlight, LastDuration and Abandoned report what the pane is doing, for the
// debug pane. A pile of in-flight searches is the signature of a repository
// that searches slower than it is typed in.
func (p *Pane) InFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight
}

func (p *Pane) LastDuration() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastDur
}

func (p *Pane) Abandoned() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.abandoned
}

// Heights the two layouts need. A field is three rows with its border, so the
// full pane is three fields, the toggle row and one line of results; the
// compact one is the query and whatever is left.
const (
	fullRows    = 11
	compactRows = 4
)

// Render draws the pane.
//
// It used to return early below twelve rows, so a short terminal opened the
// sidebar onto nothing at all — not even a border, which reads as a redraw bug
// rather than a size one. Now it sheds the parts it cannot fit, and says so
// when it cannot fit any of them.
func (p *Pane) Render(s *ui.Screen, x, y, w, h int, th widget.Theme, focused bool) {
	p.apply()
	if w < 6 || h < 1 {
		return
	}
	p.compact = h < fullRows
	if !p.visible(p.spot) {
		// A resize can leave focus on a component that is no longer drawn,
		// which would mean typing into something invisible.
		p.spot = spotQuery
	}
	if h < compactRows {
		s.SetString(x, y, widget.Truncate("search: pane too short", w), th.Dim, w)
		return
	}

	p.query.Focused = focused && p.spot == spotQuery
	p.query.Render(s, x, y, w, th)
	if p.compact {
		// The globs and toggles go first: they are set once and then left
		// alone, while the query and its results are why the pane is open.
		p.renderResults(s, x, y+3, w, h-3, th, focused)
		return
	}
	p.include.Focused = focused && p.spot == spotInclude
	p.exclude.Focused = focused && p.spot == spotExclude
	p.include.Render(s, x, y+3, w, th)
	p.exclude.Render(s, x, y+6, w, th)
	p.renderToggles(s, x+1, y+9, w-2, th, focused)
	p.renderResults(s, x, y+10, w, h-10, th, focused)
}

// visible reports whether a focus stop is drawn at the size last rendered. The
// focus ring has to agree with the layout, or tab walks onto a component that
// is not on screen and keystrokes disappear into it.
func (p *Pane) visible(spot int) bool {
	if !p.compact {
		return true
	}
	return spot == spotQuery || spot == spotResults
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
	// The true total, not the number of rows. A per-file cap means the pane
	// deliberately shows less than it found, and reporting the row count would
	// turn that into an understatement of the search rather than of the display.
	total := p.Result.Total()
	header := fmt.Sprintf("%d results in %d files", total, p.Result.Files)
	if shown := len(p.Result.Matches); shown < total {
		header = fmt.Sprintf("%d of %d results in %d files", shown, total, p.Result.Files)
	}
	if p.Result.Err != nil {
		header = "bad pattern: " + p.Result.Err.Error()
	} else if p.Result.Capped {
		header += " (capped)"
	}
	s.Fill(x, y, w, 1, ui.DefaultStyle)
	s.SetString(x+1, y, widget.Truncate(header, w-2), th.Heading(focused && p.spot == spotResults), w-2)

	rows := h - 1
	p.list.Settle(rows, len(p.rows))

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
			count := fmt.Sprintf("(%d)", r.Count)
			if r.Total > r.Count {
				count = fmt.Sprintf("(%d of %d)", r.Count, r.Total)
			}
			label = marker + fmt.Sprintf("%s %s",
				widget.TruncateLeft(relative(p.Root, r.Path), w-10-len(count)), count)
		} else {
			indent = 4
			label = fmt.Sprintf("%d: %s", r.Match.Line, trimIndent(r.Match.Text))
		}
		s.SetString(x+indent, y+1+row, widget.Truncate(label, w-indent-1), style, w-indent-1)
	}
}
