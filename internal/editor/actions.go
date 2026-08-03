package editor

import (
	"raj/internal/keys"
	"raj/internal/piecetable"
)

// Handle applies an action to the pane, reporting whether it was consumed.
// Unconsumed actions fall through to the application — a tab switch or a pane
// focus change is not the editor's business.
//
// The switch is deliberately flat and declarative. Movement actions pair with
// their selecting variants on the same line so the two can never drift apart,
// which is the usual way editors end up with shift+left behaving differently
// from left.
func (p *Pane) Handle(a keys.Action) bool {
	// One action, one undo step. Without this, typing with three cursors takes
	// three presses of cmd+z to reverse, and the intermediate states are ones
	// the user never created.
	p.File.Begin()
	defer p.File.End()

	switch a {
	// movement
	case keys.CharLeft:
		p.CharLeft(false)
	case keys.SelCharLeft:
		p.CharLeft(true)
	case keys.CharRight:
		p.CharRight(false)
	case keys.SelCharRight:
		p.CharRight(true)
	case keys.LineUp:
		p.MoveVertical(-1, false)
	case keys.SelLineUp:
		p.MoveVertical(-1, true)
	case keys.LineDown:
		p.MoveVertical(+1, false)
	case keys.SelLineDown:
		p.MoveVertical(+1, true)
	case keys.LineStart:
		p.LineStart(false)
	case keys.SelLineStart:
		p.LineStart(true)
	case keys.LineEnd:
		p.LineEnd(false)
	case keys.SelLineEnd:
		p.LineEnd(true)
	case keys.DocStart:
		p.DocStart(false)
	case keys.SelDocStart:
		p.DocStart(true)
	case keys.DocEnd:
		p.DocEnd(false)
	case keys.SelDocEnd:
		p.DocEnd(true)
	case keys.WordLeft:
		p.WordLeft(false)
	case keys.SelWordLeft:
		p.WordLeft(true)
	case keys.WordRight:
		p.WordRight(false)
	case keys.PageUp:
		p.MovePage(-1, false)
	case keys.SelPageUp:
		p.MovePage(-1, true)
	case keys.PageDown:
		p.MovePage(+1, false)
	case keys.SelPageDown:
		p.MovePage(+1, true)
	case keys.SelWordRight:
		p.WordRight(true)

	// editing
	case keys.Backspace:
		p.DeleteBackward()
	case keys.Delete:
		p.DeleteForward()
	case keys.DeleteLine:
		p.DeleteLine()
	case keys.LineBelow:
		p.OpenLineBelow()
	case keys.LineAbove:
		p.OpenLineAbove()
	case keys.Indent:
		p.Indent()
	case keys.Outdent:
		p.Outdent()
	case keys.SelectAll:
		p.SelectAll()
	case keys.MoveLineUp:
		p.MoveLines(-1)
	case keys.MoveLineDown:
		p.MoveLines(+1)
	case keys.CopyLineUp:
		p.CopyLines(-1)
	case keys.CopyLineDown:
		p.CopyLines(+1)
	case keys.ToggleComment:
		p.ToggleComment()

	// history
	case keys.Undo:
		p.history(p.File.Undo(p.Author))
	case keys.Redo:
		p.history(p.File.Redo(p.Author))

	// multi-cursor
	case keys.CursorAbove:
		p.AddCursorVertical(-1)
	case keys.CursorBelow:
		p.AddCursorVertical(+1)
	case keys.AddNextOccurrence:
		p.AddNextOccurrence()
	case keys.AllOccurrences:
		p.SelectAllOccurrences()
	case keys.Cancel:
		p.Cursors.Clear()

	default:
		return false
	}
	p.FollowCursor()
	return true
}

// HandleText inserts literal text — a keypress with no action bound, or a
// paste. Pastes arrive whole rather than as individual keys, so a large paste
// is one buffer edit rather than thousands.
func (p *Pane) HandleText(text string) {
	if text == "" {
		return
	}
	p.File.Begin()
	p.InsertText(text)
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
