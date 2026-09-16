package editor

import (
	"strings"

	"raj/internal/piecetable"
	"raj/internal/view"
)

// Pane is one editing surface: a file, its cursors, and the window onto it.
//
// Edits are applied cursor-by-cursor from the highest offset down, so earlier
// cursors' offsets stay valid while later ones are being edited. Doing it the
// other way requires shifting every remaining cursor after each edit, which is
// where multi-cursor implementations usually go wrong.
type Pane struct {
	File     *File
	Cursors  *Cursors
	Viewport view.Viewport
	Author   piecetable.Author
	Find     Find

	// leaseHit is the change set that refused the most recent edit attempt and
	// leaseBlocked records that a refusal happened, so the application can turn
	// it into a status note. The key path returns only "consumed", so the pane
	// carries the refusal itself and TakeLeaseRefusal drains it.
	leaseHit     uint64
	leaseBlocked bool

	// Wrap makes long lines occupy several visual rows instead of scrolling
	// horizontally. Off by default: turning it on changes what a "row" means
	// for every viewport calculation, so it is opt-in per pane.
	Wrap bool

	// AutoPairs completes brackets and quotes as they are typed. Auto-indent
	// on newline is not gated by it: a newline that drops the indentation is
	// not a convenience anyone declines, whereas an auto-closed bracket is a
	// preference people genuinely differ on.
	AutoPairs bool

	// Hints shows language-server inlay hints inline. It inherits the
	// application default as a pane opens, like Wrap and AutoPairs, so the
	// setting and the toggle cannot disagree about a newly opened file.
	Hints bool

	wrapBuf []int // reused across lines and frames by the renderer
	focused bool

	// disp is the display projection: the edit composition with hidden runs
	// collapsed to fold rows. It is derived, not a second document, and nil
	// means identity — no decisions, so display coordinates are the raw
	// session coordinates. SetDisplay rebuilds it; edits never touch it.
	disp *view.Projection
	// dispKey is the memo key for disp: the session version, the decision
	// generation and the composition policy SetDisplay built it from. They sit
	// beside disp because the projection and the key that qualifies it are one
	// piece of derived state, and a caller must not refresh one without the
	// other. UpdateDisplay is the only reader.
	dispVersion  piecetable.Version
	dispGen      uint64
	dispPolicy   piecetable.Policy
	dispKeyValid bool
	// displayBuilds counts SetDisplay calls. It exists so a test can assert the
	// memo skipped a rebuild honestly, including on the nil identity projection
	// where a pointer comparison cannot tell a rebuild from a no-op.
	displayBuilds int
	// diskStale records the file changed on disk since raj read or wrote it,
	// set by the app idle tick and cleared by save or reload.
	diskStale bool

	// cursorHistory records where the cursors were before each movement, so
	// cmd+u can put them back. A snapshot of a place rather than of an action:
	// cursor undo returns to a position, which is what undo means for a cursor.
	cursorHistory []cursorSnapshot

	// pending is the proposed change sets in current coordinates, filled once
	// per frame by RenderFocused so every drawn row tints from one journal
	// walk rather than walking it per line.
	pending []PendingMark
}

// cursorSnapshot is one recorded cursor position: the whole set and which of
// them was primary. Primary matters because it is what the viewport follows
// and the status line reports.
type cursorSnapshot struct {
	cursor  []Cursor
	primary int
}

// cursorHistoryLimit bounds the undo ring. Fifty positions is enough to move
// around a long file without losing the earlier places.
const cursorHistoryLimit = 50

// pushCursorHistory records the current position unless it is the same as the
// last one recorded, so holding a movement key does not fill the ring with
// duplicates.
func (p *Pane) pushCursorHistory() {
	list, primary := p.Cursors.State()
	if n := len(p.cursorHistory); n > 0 {
		top := p.cursorHistory[n-1]
		if top.primary == primary && sameCursors(top.cursor, list) {
			return
		}
	}
	p.cursorHistory = append(p.cursorHistory, cursorSnapshot{cursor: list, primary: primary})
	if len(p.cursorHistory) > cursorHistoryLimit {
		p.cursorHistory = p.cursorHistory[len(p.cursorHistory)-cursorHistoryLimit:]
	}
}

// popCursorHistory restores the most recently recorded position, reporting
// whether there was one. An empty history is a no-op rather than an error:
// stepping back before anything has moved should just do nothing.
func (p *Pane) popCursorHistory() bool {
	if len(p.cursorHistory) == 0 {
		return false
	}
	s := p.cursorHistory[len(p.cursorHistory)-1]
	p.cursorHistory = p.cursorHistory[:len(p.cursorHistory)-1]
	p.Cursors.Restore(s.cursor, s.primary)
	p.FollowCursor()
	return true
}

func sameCursors(a, b []Cursor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MarkDiskStale records the file changed on disk behind this tab.
func (p *Pane) MarkDiskStale() { p.diskStale = true }

// ClearDiskStale resets the disk-changed mark after a save or reload.
func (p *Pane) ClearDiskStale() { p.diskStale = false }

// DiskStale reports whether the file changed on disk since raj read or wrote it.
func (p *Pane) DiskStale() bool { return p.diskStale }

// NewPane wraps a file for editing.
func NewPane(f *File) *Pane {
	return &Pane{
		File:     f,
		Cursors:  NewCursors(),
		Viewport: view.Viewport{ScrollOff: 2},
		Author:   piecetable.User,
	}
}

// SetDisplay derives the pane display projection from a composition policy.
//
// It is the one place the pane reaches for Session.Project: Segments describes
// how the raw session view and the policy composition line up, including the
// runs the composition hides and the runs it restores. view.Build turns that
// into the display map the renderer and every coordinate conversion read. No
// decisions yields no segments and Build returns nil, so the projection is the
// identity of the session — disp is derived, never a second document. The
// application calls UpdateDisplay once per frame so the journal walk is
// memoised; tests call this directly to force a rebuild.
func (p *Pane) SetDisplay(policy piecetable.Policy) {
	comp := p.File.Session().Project(policy)
	segs := comp.Segments()
	var out []view.Seg
	if len(segs) > 0 {
		out = make([]view.Seg, len(segs))
		for i, s := range segs {
			out[i] = view.Seg{
				Doc:         s.Doc,
				Disp:        s.Disp,
				Len:         s.Len,
				DLen:        s.DLen,
				Fold:        s.Hide,
				Group:       s.Group,
				HiddenLines: s.HiddenLines,
			}
		}
	}
	p.disp = view.Build(comp.Text(), out)
	p.dispVersion = p.File.Session().Version()
	p.dispGen = p.File.DecisionGeneration()
	p.dispPolicy = policy
	p.dispKeyValid = true
	p.displayBuilds++
}

// UpdateDisplay derives the projection for policy, rebuilding only when its
// inputs moved.
//
// SetDisplay walks the journal through Session.Project, so it must not run on
// every frame. The composition is a function of three things: the session
// version, the decision generation, and the policy. A decision (propose,
// accept, reject, clear, revert) moves the composition without moving the
// version, so the generation is half the key; a mode switch moves neither, so
// the policy is the other half. The key lives on the pane rather than in the
// application because the projection and the key that qualifies it are one
// derived value: keeping them together means no caller can refresh one without
// the other, and every Pane — test-built ones included — gets the memo without
// app-side bookkeeping.
func (p *Pane) UpdateDisplay(policy piecetable.Policy) {
	if p.dispKeyValid {
		v, g := p.File.Session().Version(), p.File.DecisionGeneration()
		if p.dispPolicy == policy && p.dispVersion == v && p.dispGen == g {
			return
		}
	}
	p.SetDisplay(policy)
}

// invalidateDisplay drops the derived projection and its memo, so the next
// UpdateDisplay rebuilds against the session as it now stands. It is for a
// wholesale document replacement — Pane.Reload, and a restored journal — where
// the old projection no longer describes the text and the session version need
// not move far enough for the key to notice on its own. Until the rebuild disp
// is nil, which is the identity: the fresh session's own coordinates.
func (p *Pane) invalidateDisplay() {
	p.disp = nil
	p.dispKeyValid = false
}

// DisplayLines is the number of display rows the projection yields, or the
// document line count when there is no projection.
func (p *Pane) DisplayLines() int {
	if p.disp != nil {
		return p.disp.Lines()
	}
	return p.File.Lines()
}

// line describes display row i, the unit the renderer and every coordinate
// conversion work in. It reports the session line the row draws, the byte
// range [lo,hi) of that line shown on the row, and whether the row is a fold
// standing in for a hidden run.
//
// Three kinds of row exist:
//
//   - session-backed: sessionLine >= 0, the row draws that session line bytes
//     [lo,hi);
//   - fold: sessionLine < 0 and fold true, a marker for a hidden session run;
//   - composition-only: sessionLine < 0 and fold false, bytes that exist only
//     in the composition (a restored deletion), with no session line behind
//     them.
//
// With no projection every row is session line i in full.
func (p *Pane) line(i int) (sessionLine, lo, hi int, fold bool) {
	if p.disp == nil {
		if i < 0 || i >= p.File.Lines() {
			return i, 0, 0, false
		}
		return i, 0, len(p.File.Line(i)), false
	}
	if i < 0 || i >= p.disp.Lines() {
		return i, 0, 0, false
	}
	d := p.disp.At(i)
	if d.SessionLine < 0 {
		return -1, 0, 0, d.Fold != 0
	}
	return d.SessionLine, d.Lo, d.Hi, false
}

// Fold reports the hidden run a fold display row stands for: its change set and
// the number of session bytes it hides. ok is false on any row that is not a
// fold, and always false with no projection.
func (p *Pane) Fold(i int) (group uint64, hiddenBytes int, ok bool) {
	if p.disp == nil {
		return 0, 0, false
	}
	return p.disp.Fold(i)
}

// DispOfDocLine maps a session line to the first display row that shows it, or
// -1 when a fold hides the line entirely. The gutter and the review list keep
// their marks in session coordinates and use this to place them on the
// projected screen.
func (p *Pane) DispOfDocLine(sessionLine int) int {
	if sessionLine < 0 || sessionLine >= p.File.Lines() {
		return -1
	}
	if p.disp == nil {
		return sessionLine
	}
	return p.disp.DispOfDocLine(sessionLine)
}

// dispLineOf maps a byte offset to the display row that draws it. With no
// projection it is the document line; with one, DispOfDoc resolves a mid-line
// fold to the exact sub-row rather than the session line first row.
func (p *Pane) dispLineOf(off int) int {
	if p.disp == nil {
		return p.File.LineOf(off)
	}
	if line, _ := p.disp.DispOfDoc(off); line >= 0 && line < p.disp.Lines() {
		return line
	}
	if line := p.disp.DispOfDocLine(p.File.LineOf(off)); line >= 0 && line < p.disp.Lines() {
		return line
	}
	return 0
}

// DispPos maps a document offset to a display row and the display column
// within that row, for the unwrapped caret and vertical motion. A fold or
// composition-only row answers column zero: neither draws a session byte the
// caret could sit on.
func (p *Pane) DispPos(off int) (line, col int) {
	if p.disp == nil {
		return p.File.LineCol(off)
	}
	line = p.dispLineOf(off)
	sl, lo, _, _ := p.line(line)
	if sl < 0 {
		return line, 0
	}
	full := p.File.Line(sl)
	within := clamp(off-p.File.LineStart(sl), 0, len(full))
	hints := p.File.HintCols(sl)
	col = p.File.Cols.ColOfHints(full, within, hints) - p.File.Cols.ColOfHints(full, lo, hints)
	return line, col
}

// DocAt maps a display row and column back to a document offset. A
// session-backed row converts through the column machinery, so it stays the
// inverse of DispPos; a fold or composition-only row defers to the map, which
// yields the run session cursor so a click can never land inside text the row
// does not draw. With no projection it is File.OffsetAt exactly.
func (p *Pane) DocAt(line, col int) int {
	if p.disp == nil {
		return p.File.OffsetAt(line, col)
	}
	if line < 0 {
		return 0
	}
	if line >= p.disp.Lines() {
		return p.File.Len()
	}
	sl, lo, hi, _ := p.line(line)
	if sl < 0 {
		return p.disp.DocAt(line, col)
	}
	text := p.File.Line(sl)[lo:hi]
	return p.File.LineStart(sl) + lo + p.File.Cols.OffsetOf(text, col)
}

// snapOut moves off out of a hidden run to the nearest edge, so a motion that
// would land inside text the display does not draw stops beside it instead.
// dir < 0 takes the leading edge, dir > 0 the trailing one, and 0 the nearer.
// A composition-only row has no session bytes to be inside; it snaps to the
// row session cursor.
func (p *Pane) snapOut(off, dir int) int {
	if p.disp == nil {
		return off
	}
	line := p.dispLineOf(off)
	sl, _, _, fold := p.line(line)
	if sl >= 0 {
		return off
	}
	start := p.DocAt(line, 0)
	if !fold {
		return start // composition-only: its session cursor
	}
	_, hidden, ok := p.Fold(line)
	if !ok || hidden <= 0 {
		return start
	}
	end := start + hidden
	if off <= start {
		return start
	}
	if off >= end {
		return end
	}
	switch {
	case dir < 0:
		return start
	case dir > 0:
		return end
	case off-start <= end-off:
		return start
	default:
		return end
	}
}

// Resize sets the visible area in cells.
func (p *Pane) Resize(cols, rows int) {
	p.Viewport.Resize(cols, rows, p.DisplayLines())
	if p.Wrap {
		// A width change alters how many rows the top line occupies. Clamping
		// it is the whole cost of a resize under this design: there is no
		// global line-to-row table to rebuild.
		p.clampWrapTop()
	}
}

// FollowCursor scrolls so the primary cursor is visible.
func (p *Pane) FollowCursor() {
	if p.Wrap {
		p.followCursorWrapped()
		return
	}
	line, col := p.DispPos(p.Cursors.Primary().Head)
	p.Viewport.ScrollTo(line, col, p.DisplayLines())
}

// InsertText types at every cursor, replacing selections.
func (p *Pane) InsertText(text string) {
	p.editEachCursor(func(c Cursor) (pos, remove int, insert string) {
		lo, hi := c.Range()
		return lo, hi - lo, text
	})
}

// ReplaceRange replaces the buffer range [start,end) with text, leaving the
// cursor at the end of the inserted text.
//
// This is the single-range form of an accept: a language server names a span to
// overwrite rather than a prefix to extend, so its edit cannot go through
// InsertText. The range is clamped to the buffer, because it was computed
// against a version the server saw and the user may have edited since.
func (p *Pane) ReplaceRange(start, end int, text string) {
	n := p.File.Len()
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	if end < start {
		end = start
	}
	if end > n {
		end = n
	}
	p.Cursors.Set(start, start)
	p.editEachCursor(func(Cursor) (pos, remove int, insert string) {
		return start, end - start, text
	})
}

// Paste inserts a block of text as a single edit.
//
// A paste has no per-cursor semantics: it is one chunk of text arriving at one
// place. Routing it through the per-cursor machinery makes a large paste into
// as many ops as there are cursors, each appending its own copy to the author
// store, and turns one undo step into several. This appends once and commits
// once, which is also why a 100-line paste costs three pieces rather than
// hundreds.
func (p *Pane) Paste(text string) {
	if text == "" {
		return
	}
	c := p.Cursors.Primary()
	lo, hi := c.Range()
	if g, ok := p.File.EditLeased(lo, hi-lo); ok {
		p.noteLease(g)
		return
	}
	p.File.Begin()
	defer p.File.End()

	p.Cursors.Set(lo, lo)
	if hi > lo {
		p.File.Delete(p.Author, lo, hi-lo)
	}
	p.File.Insert(p.Author, lo, text)
	at := lo + len(text)
	p.Cursors.Set(at, at)
	p.FollowCursor()
}

// PasteDistributed gives each cursor one line of the clipboard, which is the
// other half of a multi-cursor copy: select three names, copy, make three
// cursors elsewhere, paste, and each name lands at its own cursor.
//
// Cursors are edited highest-offset-first so earlier ones stay valid, and the
// whole thing is one undo step. Total bytes stored is the clipboard once —
// each cursor takes a different slice — unlike inserting the whole clipboard at
// every cursor, which stores it N times.
func (p *Pane) PasteDistributed(lines []string) {
	cursors := p.Cursors.All()
	if len(lines) != len(cursors) {
		p.Paste(strings.Join(lines, "\n"))
		return
	}
	edits := make([]cursorEdit, 0, len(cursors))
	for i, c := range cursors {
		lo, hi := c.Range()
		edits = append(edits, cursorEdit{pos: lo, remove: hi - lo, insert: lines[i]})
	}
	if p.leaseBlocks(edits) {
		return
	}
	p.File.Begin()
	defer p.File.End()

	for i := len(cursors) - 1; i >= 0; i-- {
		lo, hi := cursors[i].Range()
		if hi > lo {
			p.File.Delete(p.Author, lo, hi-lo)
			p.Cursors.Shift(lo, hi-lo, 0)
		}
		p.File.Insert(p.Author, lo, lines[i])
		p.Cursors.Shift(lo, 0, len(lines[i]))
		p.bumpCursorsAt(lo, len(lines[i]))
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
	p.FollowCursor()
}

// PasteAtEachCursor inserts the whole clipboard at every cursor, replacing each
// selection. It is the fallback for a multi-cursor paste whose text is not one
// line per cursor, where distributing would keep only the first line.
//
// The text is stored once: the highest cursor performs the real insert and the
// pieces it produced are then spliced at every cursor below it, appending
// nothing. Cursors are edited highest-offset-first so earlier ones stay valid,
// and the whole thing is one undo step.
func (p *Pane) PasteAtEachCursor(text string) {
	if text == "" {
		return
	}
	cursors := p.Cursors.All()
	if len(cursors) == 0 {
		return
	}
	edits := make([]cursorEdit, 0, len(cursors))
	for _, c := range cursors {
		lo, hi := c.Range()
		edits = append(edits, cursorEdit{pos: lo, remove: hi - lo, insert: text})
	}
	if p.leaseBlocks(edits) {
		return
	}
	p.File.Begin()
	defer p.File.End()

	last := len(cursors) - 1
	lo, hi := cursors[last].Range()
	if hi > lo {
		p.File.Delete(p.Author, lo, hi-lo)
		p.Cursors.Shift(lo, hi-lo, 0)
	}
	p.File.Insert(p.Author, lo, text)
	recs := p.File.Snapshot(lo, len(text))
	p.Cursors.Shift(lo, 0, len(text))
	p.bumpCursorsAt(lo, len(text))

	for i := last - 1; i >= 0; i-- {
		lo, hi := cursors[i].Range()
		if hi > lo {
			p.File.Delete(p.Author, lo, hi-lo)
			p.Cursors.Shift(lo, hi-lo, 0)
		}
		p.File.InsertPieces(p.Author, lo, recs)
		p.Cursors.Shift(lo, 0, len(text))
		p.bumpCursorsAt(lo, len(text))
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
	p.FollowCursor()
}

// DeleteBackward is backspace: remove the selection, or the character before
// the cursor.
func (p *Pane) DeleteBackward() {
	// An empty pair the editor completed is removed whole, so auto-pairing
	// does not quietly cost a keystroke every time a bracket is typed and
	// then thought better of.
	if p.deletePair() {
		return
	}
	p.editEachCursor(func(c Cursor) (int, int, string) {
		if c.HasSelection() {
			lo, hi := c.Range()
			return lo, hi - lo, ""
		}
		if c.Head == 0 {
			return c.Head, 0, ""
		}
		prev := p.prevBoundary(c.Head)
		return prev, c.Head - prev, ""
	})
}

// DeleteForward is the delete key.
func (p *Pane) DeleteForward() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		if c.HasSelection() {
			lo, hi := c.Range()
			return lo, hi - lo, ""
		}
		if c.Head >= p.File.Len() {
			return c.Head, 0, ""
		}
		return c.Head, p.nextBoundary(c.Head) - c.Head, ""
	})
}

// DeleteLine removes the whole line each cursor sits on.
func (p *Pane) DeleteLine() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		line := p.File.LineOf(c.Head)
		start := p.File.LineStart(line)
		end := p.File.LineEnd(line)
		if end < p.File.Len() {
			end++ // take the newline too
		} else if start > 0 {
			start-- // last line: take the preceding newline instead
		}
		return start, end - start, ""
	})
}

// DeleteToLineEnd removes everything from the cursor to the end of its line.
//
// On an already-empty tail it takes the newline instead, so repeating the chord
// pulls the next line up rather than doing nothing — which is what readline's
// ctrl+k does and the only behaviour that makes pressing it twice sensible.
// A selection is deleted as a selection, since that is what every other editing
// action here does with one.
func (p *Pane) DeleteToLineEnd() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		if lo, hi := c.Range(); lo != hi {
			return lo, hi - lo, ""
		}
		end := p.File.LineEnd(p.File.LineOf(c.Head))
		if end == c.Head && end < p.File.Len() {
			end++ // nothing left on this line: join the next one
		}
		return c.Head, end - c.Head, ""
	})
}

// OpenLineBelow inserts a newline after the current line and moves there,
// regardless of where in the line the cursor sits.
func (p *Pane) OpenLineBelow() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		return p.File.LineEnd(p.File.LineOf(c.Head)), 0, "\n"
	})
	p.MoveTo(func(c Cursor, f *File) int { return c.Head }, false)
}

// OpenLineAbove inserts a newline before the current line.
func (p *Pane) OpenLineAbove() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		return p.File.LineStart(p.File.LineOf(c.Head)), 0, "\n"
	})
}

// Indent adds one indent unit to every line touched by a cursor or selection.
func (p *Pane) Indent() { p.reindent(true) }

// Outdent removes one indent unit from every touched line.
func (p *Pane) Outdent() { p.reindent(false) }

func (p *Pane) reindent(add bool) {
	unit := p.File.Indent.Unit()
	lines := p.touchedLines()
	// Bottom-up: editing a later line cannot invalidate an earlier line's start.
	edits := make([]cursorEdit, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		start := p.File.LineStart(lines[i])
		text := p.File.Line(lines[i])
		if add {
			edits = append(edits, cursorEdit{pos: start, insert: unit})
			continue
		}
		if n := leadingSpaces(text, p.File.Cols.Tab); n > 0 {
			edits = append(edits, cursorEdit{pos: start, remove: n})
		}
	}
	if p.leaseBlocks(edits) {
		return
	}
	for _, e := range edits {
		p.applyEdit(e.pos, e.remove, e.insert)
	}
	p.Cursors.Normalize()
}

// touchedLines is every line any cursor or selection covers, ascending.
func (p *Pane) touchedLines() []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range p.Cursors.All() {
		lo, hi := c.Range()
		last := p.File.LineOf(hi)
		if last > p.File.LineOf(lo) && hi == p.File.LineStart(last) {
			last--
		}
		for line := p.File.LineOf(lo); line <= last; line++ {
			if !seen[line] {
				seen[line] = true
				out = append(out, line)
			}
		}
	}
	sortInts(out)
	return out
}

// leadingSpaces is how much indentation to strip: up to one tab width of
// spaces, or a single literal tab.
func leadingSpaces(line string, tab int) int {
	if strings.HasPrefix(line, "\t") {
		return 1
	}
	n := 0
	for n < len(line) && n < tab && line[n] == ' ' {
		n++
	}
	return n
}

// noteLease records that change set group owns text an edit just tried to
// touch. The application drains it with TakeLeaseRefusal.
func (p *Pane) noteLease(group uint64) {
	p.leaseHit, p.leaseBlocked = group, true
}

// TakeLeaseRefusal returns the change set that refused the most recent edit
// and clears the mark, so the application surfaces each refusal once. The
// second result is false when nothing was refused.
func (p *Pane) TakeLeaseRefusal() (uint64, bool) {
	if !p.leaseBlocked {
		return 0, false
	}
	g := p.leaseHit
	p.leaseHit, p.leaseBlocked = 0, false
	return g, true
}

// cursorEdit is one replacement a multi-cursor action will make.
type cursorEdit struct {
	pos, remove int
	insert      string
}

// leaseBlocks reports whether any of edits would intersect a lease, recording
// the refusing set. Every site is checked before any is applied, so a
// multi-cursor action is refused whole rather than mutating some cursors and
// not others.
func (p *Pane) leaseBlocks(edits []cursorEdit) bool {
	for _, e := range edits {
		if g, ok := p.File.EditLeased(e.pos, e.remove); ok {
			p.noteLease(g)
			return true
		}
	}
	return false
}

// editEachCursor applies one edit per cursor, highest offset first so that
// earlier cursors' positions remain valid throughout. The edits are computed
// first and checked together against the leases, so an action whose range
// touches a read-only run changes nothing at all.
func (p *Pane) editEachCursor(fn func(Cursor) (pos, remove int, insert string)) {
	cursors := p.Cursors.All()
	edits := make([]cursorEdit, 0, len(cursors))
	for i := len(cursors) - 1; i >= 0; i-- {
		pos, remove, insert := fn(cursors[i])
		if remove == 0 && insert == "" {
			continue
		}
		edits = append(edits, cursorEdit{pos: pos, remove: remove, insert: insert})
	}
	if p.leaseBlocks(edits) {
		return
	}
	for _, e := range edits {
		p.applyEdit(e.pos, e.remove, e.insert)
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
}

// applyEdit performs one replacement and shifts every cursor to match. It
// refuses and records the lease when the range touches a read-only run, so no
// text moves and no cursor shifts onto a position the edit did not create.
func (p *Pane) applyEdit(pos, remove int, insert string) {
	if g, ok := p.File.EditLeased(pos, remove); ok {
		p.noteLease(g)
		return
	}
	if remove > 0 {
		p.File.Delete(p.Author, pos, remove)
		p.Cursors.Shift(pos, remove, 0)
	}
	if insert != "" {
		p.File.Insert(p.Author, pos, insert)
		p.Cursors.Shift(pos, 0, len(insert))
		p.bumpCursorsAt(pos, len(insert))
	}
}

// bumpCursorsAt moves cursors sitting exactly at an insertion point to the end
// of the inserted text. Shift deliberately leaves them put — that is right for
// a cursor elsewhere in the document watching text appear before it, and wrong
// for the cursor that did the typing.
func (p *Pane) bumpCursorsAt(pos, n int) {
	for i, c := range p.Cursors.All() {
		if c.Head == pos {
			p.Cursors.list[i].Head = pos + n
		}
		if c.Anchor == pos {
			p.Cursors.list[i].Anchor = pos + n
		}
	}
}

func (p *Pane) prevBoundary(off int) int {
	for off > 0 {
		off--
		if p.File.Slice(off, 1)[0]&0xC0 != 0x80 {
			return off
		}
	}
	return 0
}

func (p *Pane) nextBoundary(off int) int {
	n := p.File.Len()
	for off < n {
		off++
		if off >= n || p.File.Slice(off, 1)[0]&0xC0 != 0x80 {
			return off
		}
	}
	return n
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
