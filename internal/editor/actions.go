package editor

import (
	"raj/internal/keys"
	"raj/internal/piecetable"
)

// Handle applies an action to the pane, reporting whether it was consumed.
// Unconsumed actions fall through to the application — a tab switch or a pane
// focus change is not the editor's business.
//
// The actions live in two tables. motionPairs pairs each cursor movement with
// its selecting variant, one row holding both actions and the single handler
// that takes the selection flag, so the two cannot drift apart — the usual way
// editors end up with shift+left behaving differently from left. paneActions
// holds everything else: the action, its handler, whether it moves a cursor,
// and whether the view follows the cursor afterwards.
//
// One action, one undo step. Without the begin/end below, typing with three
// cursors takes three presses of cmd+z to reverse, and the intermediate states
// are ones the user never created.
func (p *Pane) Handle(a keys.Action) bool {
	p.File.Begin()
	defer p.File.End()

	// Movements record where the cursors were before they moved, so cmd+u can
	// step back through them. Everything else — typing, text undo, selection
	// tricks — is answered by another mechanism or changes nothing to undo.
	if isMotion(a) {
		p.pushCursorHistory()
	}

	fn, follow, ok := lookupAction(a)
	if !ok {
		return false
	}
	fn(p)
	if follow {
		p.FollowCursor()
	}
	return true
}

// motionPair is one cursor movement and its selecting variant. The two share a
// single handler, told apart by the bool, so the pair cannot drift.
type motionPair struct {
	plain keys.Action
	sel   keys.Action
	fn    func(*Pane, bool)
}

// motionPairs is every cursor movement, paired with its selecting variant.
// PageUp and PageDown are deliberately absent: their plain form scrolls the
// view without moving the cursors, while their selecting form moves them, so
// the two do not share a handler and live in paneActions instead.
var motionPairs = []motionPair{
	{keys.CharLeft, keys.SelCharLeft, (*Pane).CharLeft},
	{keys.CharRight, keys.SelCharRight, (*Pane).CharRight},
	{keys.LineUp, keys.SelLineUp, func(p *Pane, sel bool) { p.MoveVertical(-1, sel) }},
	{keys.LineDown, keys.SelLineDown, func(p *Pane, sel bool) { p.MoveVertical(+1, sel) }},
	{keys.LineStart, keys.SelLineStart, (*Pane).LineStart},
	{keys.LineEnd, keys.SelLineEnd, (*Pane).LineEnd},
	{keys.DocStart, keys.SelDocStart, (*Pane).DocStart},
	{keys.DocEnd, keys.SelDocEnd, (*Pane).DocEnd},
	{keys.WordLeft, keys.SelWordLeft, (*Pane).WordLeft},
	{keys.WordRight, keys.SelWordRight, (*Pane).WordRight},
}

// paneAction is one action that is not half of a motion pair. motion says
// whether the handler moves a cursor; follow says whether the view should
// follow the cursor once the handler has run.
type paneAction struct {
	action keys.Action
	fn     func(*Pane)
	motion bool
	follow bool
}

// paneActions is every action that is not half of a motion pair.
var paneActions = []paneAction{
	// The page chords: the plain form scrolls the view and leaves the cursors
	// where they are, so it does not follow; the selecting form moves them.
	{keys.PageUp, func(p *Pane) { p.ScrollPage(-1) }, false, false},
	{keys.PageDown, func(p *Pane) { p.ScrollPage(+1) }, false, false},
	{keys.SelPageUp, func(p *Pane) { p.MovePage(-1, true) }, true, true},
	{keys.SelPageDown, func(p *Pane) { p.MovePage(+1, true) }, true, true},

	// editing
	{keys.Backspace, (*Pane).DeleteBackward, false, true},
	{keys.Delete, (*Pane).DeleteForward, false, true},
	{keys.DeleteLine, (*Pane).DeleteLine, false, true},
	{keys.DeleteToLineEnd, (*Pane).DeleteToLineEnd, false, true},
	{keys.LineBelow, (*Pane).OpenLineBelow, false, true},
	{keys.LineAbove, (*Pane).OpenLineAbove, false, true},
	{keys.Indent, (*Pane).Indent, false, true},
	{keys.Outdent, (*Pane).Outdent, false, true},
	{keys.SelectAll, (*Pane).SelectAll, false, true},
	{keys.FindNext, func(p *Pane) { p.Find.Step(p, +1) }, false, true},
	{keys.FindPrev, func(p *Pane) { p.Find.Step(p, -1) }, false, true},
	{keys.SelectLine, (*Pane).SelectLine, false, true},
	{keys.MoveLineUp, func(p *Pane) { p.MoveLines(-1) }, false, true},
	{keys.MoveLineDown, func(p *Pane) { p.MoveLines(+1) }, false, true},
	{keys.CopyLineUp, func(p *Pane) { p.CopyLines(-1) }, false, true},
	{keys.CopyLineDown, func(p *Pane) { p.CopyLines(+1) }, false, true},
	{keys.ToggleComment, (*Pane).ToggleComment, false, true},

	// history
	{keys.Undo, func(p *Pane) { p.history(p.File.Undo(p.Author)) }, false, true},
	{keys.Redo, func(p *Pane) { p.history(p.File.Redo(p.Author)) }, false, true},
	// Cursor undo returns to a recorded position; an empty history is a
	// no-op, not an error — see popCursorHistory.
	{keys.CursorUndo, func(p *Pane) { p.popCursorHistory() }, false, true},

	// multi-cursor
	{keys.CursorAbove, func(p *Pane) { p.AddCursorVertical(-1) }, false, true},
	{keys.CursorBelow, func(p *Pane) { p.AddCursorVertical(+1) }, false, true},
	{keys.AddNextOccurrence, (*Pane).AddNextOccurrence, false, true},
	{keys.AllOccurrences, (*Pane).SelectAllOccurrences, false, true},
	{keys.SplitIntoLines, (*Pane).SplitIntoLines, false, true},
	{keys.Cancel, func(p *Pane) { p.Cursors.Clear() }, false, true},
}

// motionPairFor returns the pair that serves a, if a is either half of one.
func motionPairFor(a keys.Action) (motionPair, bool) {
	for _, m := range motionPairs {
		if a == m.plain || a == m.sel {
			return m, true
		}
	}
	return motionPair{}, false
}

// isMotion reports whether a is one of the cursor-moving actions. Only these
// push onto the cursor history: typing is undone by the text undo, and
// page-scrolling leaves the cursors where they were. The multi-cursor
// additions are their own thing rather than movements — sed is how they name
// themselves, and their undo is a collapse, not a return.
func isMotion(a keys.Action) bool {
	if _, ok := motionPairFor(a); ok {
		return true
	}
	for _, ac := range paneActions {
		if ac.action == a {
			return ac.motion
		}
	}
	return false
}

// lookupAction returns the handler for a and whether the view follows the
// cursor after it runs. Motion pairs are consulted first so the pair's handler
// is the one used; a pair always follows, since both halves move a cursor.
func lookupAction(a keys.Action) (fn func(*Pane), follow, ok bool) {
	if m, paired := motionPairFor(a); paired {
		selecting := a == m.sel
		return func(p *Pane) { m.fn(p, selecting) }, true, true
	}
	for _, ac := range paneActions {
		if ac.action == a {
			return ac.fn, ac.follow, true
		}
	}
	return nil, false, false
}

// HandleText inserts literal text — a keypress with no action bound, or a
// paste. Pastes arrive whole rather than as individual keys, so a large paste
// is one buffer edit rather than thousands.
// HandleText types text at every cursor.
//
// It routes through InsertRune, which is where auto-indent and bracket pairing
// live, because this is the keystroke path. A paste goes through Paste and an
// agent edit through InsertText, and neither should have its bytes second-
// guessed: typing a bracket is a keystroke, pasting one is data.
func (p *Pane) HandleText(text string) {
	if text == "" {
		return
	}
	p.File.Begin()
	p.InsertRune(text)
	p.File.End()
	p.FollowCursor()
}

// SetAuthor changes who subsequent edits are attributed to. The application
// sets this when applying an agent's work through the same pane the user is
// typing in, so the two are distinguishable in the tint and the undo history.
func (p *Pane) SetAuthor(a piecetable.Author) { p.Author = a }

// history repositions the cursors after undo or redo.
//
// The buffer has changed underneath them, so leaving them where they were means
// they address text that no longer exists — the visible symptom is the view
// scrolling sideways, because a cursor past the end of its line reports a
// column far to the right. Collapsing to a single cursor at the change is also
// what every editor does: undo should show you what it undid.
func (p *Pane) history(ops []piecetable.Op, ok bool) {
	if !ok || len(ops) == 0 {
		return
	}
	// One cursor per reversed op, not one at the last of them. A multi-cursor
	// edit reverses as several ops, and collapsing to the last committed one
	// puts the caret at whichever site happened to be edited last — the bottom
	// of the file, since edits are applied highest-offset-first. Restoring them
	// all also keeps the multi-cursor state that produced the edit.
	// Prefer the ops that put text back: reversing "type over a selection" is a
	// delete and an insert at the same place, and only the insert's end is
	// where the restored text actually finishes.
	sites := ops[:0:0]
	for _, op := range ops {
		if op.InsLen() > 0 {
			sites = append(sites, op)
		}
	}
	if len(sites) == 0 {
		sites = ops
	}
	restored := make([]Cursor, 0, len(sites))
	for _, op := range sites {
		at := clamp(op.Pos+op.InsLen(), 0, p.File.Len())
		restored = append(restored, Cursor{Head: at, Anchor: at})
	}
	p.Cursors.Replace(restored)
	p.FollowCursor()
}
