package app

import (
	"fmt"
	"strings"

	"raj/internal/editor"
	"raj/internal/keys"
)

// Mode is the application-level editing mode. Edit is the editor as it has
// always been. Review makes the document read-only so a review pass over
// proposed change sets cannot become an accidental edit — the edit that
// produces the moved-hunk case in docs/INVESTIGATIONS.md.
type Mode uint8

const (
	// ModeEdit is ordinary editing.
	ModeEdit Mode = iota
	// ModeReview is read-only review of proposed change sets. Movement,
	// selection, search, hover and goto stay live; text mutations are refused
	// with a status note; the review decisions themselves — accept, reject and
	// the next/prev cycle — stay live, because they are decisions, not edits.
	ModeReview
)

// toggleReview switches modes. Entering Review with pending change sets jumps
// to the first one, so a review pass starts at chunk 1; entering with none is
// allowed — a read-only browse — and says so.
func (a *App) toggleReview() {
	if a.mode == ModeReview {
		a.mode = ModeEdit
		a.status = "edit mode"
		return
	}
	a.mode = ModeReview
	// A completion popup would accept into the document on the next tab, so it
	// is closed on the way in.
	a.hideCompletion()
	p := a.Tabs.Active()
	if p == nil {
		a.status = "no proposed changes"
		return
	}
	groups := proposalGroups(p)
	if len(groups) > 0 {
		a.jumpTo(groups[0].Line + 1)
		a.status = fmt.Sprintf("review: proposal 1 of %d", len(groups))
		return
	}
	// A set with no surviving projection is auto-rejected, so an empty cycle
	// really does mean there is nothing left to review.
	a.status = "no proposed changes"
}

// mutatesText reports whether an editor action changes the document text.
// This is the list Review mode refuses. Selection and movement are deliberately
// absent, and so are the multi-cursor tricks, because they only move cursors.
func mutatesText(action keys.Action) bool {
	switch action {
	case keys.Backspace, keys.Delete,
		keys.DeleteLine, keys.DeleteToLineEnd,
		keys.LineBelow, keys.LineAbove,
		keys.Indent, keys.Outdent,
		keys.MoveLineUp, keys.MoveLineDown,
		keys.CopyLineUp, keys.CopyLineDown,
		keys.ToggleComment,
		keys.Undo, keys.Redo,
		keys.Complete, keys.Cut:
		return true
	}
	return false
}

// reviewRefuses reports whether an editor keystroke would change the document
// while Review mode holds it read-only, setting the status note when it would.
// A keystroke carrying text and no action is typing; anything else must be in
// mutatesText to be refused.
func (a *App) reviewRefuses(action keys.Action, text string) bool {
	if action == keys.None {
		if text == "" {
			return false
		}
	} else if !mutatesText(action) {
		return false
	}
	a.status = reviewReadOnlyNote()
	return true
}

// reviewReadOnlyNote is the status line refusal. It names the chord that
// leaves Review mode, read from the binding table rather than assuming cmd+r.
func reviewReadOnlyNote() string {
	return "read-only in review mode: " + chordFor(keys.ToggleReview) + " to edit"
}

// chordFor is the canonical chord an action is bound to, read from the key
// table so the keybar cannot advertise a chord the editor does not actually
// resolve. It falls back to the action name rather than to a guess.
func chordFor(action keys.Action) string {
	for _, b := range keys.Bindings {
		if b.Action == action {
			return b.Chord
		}
	}
	for _, n := range keys.Natives {
		if n.Action == action {
			return n.Chord
		}
	}
	return string(action)
}

// reviewProgress reports how many pending change sets have at least one
// projected run — the sets next/prev can reach — and how many members across
// those sets a later edit overwrote entirely. Under option A a set with no
// surviving run is auto-rejected: it is not pending and does not block a save,
// so it contributes to neither count. A pending set can still lose a member to
// a later edit that erased it whole; that member is the "not shown" signal.
func reviewProgress(p *editor.Pane) (reachable, unplaced int) {
	if p == nil {
		return 0, 0
	}
	for _, d := range p.File.Session().DiffPending() {
		if len(d.Hunks) == 0 {
			continue // auto-rejected: nothing left to review or to block a save
		}
		reachable++
		unplaced += d.Moved
	}
	return reachable, unplaced
}

// reviewBar is Review mode status line: the mode badge, progress through the
// sets the review cycle can reach, a note for any member that could not be
// placed, and the real shortcuts. The chords come from the binding table, not
// from the shorthand in the design, so rebinding one moves the keybar with it.
func (a *App) reviewBar() string {
	total, unplaced := 0, 0
	current := 0
	if p := a.Tabs.Active(); p != nil {
		total, unplaced = reviewProgress(p)
		if m, ok := a.proposalAtCaret(p); ok {
			for i, g := range proposalGroups(p) {
				if g.Group == m.Group {
					current = i + 1
					break
				}
			}
		}
	}
	parts := []string{"Review"}
	switch {
	case total == 0:
		parts = append(parts, "no proposed changes")
	case current > 0:
		parts = append(parts, fmt.Sprintf("%d/%d", current, total))
	default:
		parts = append(parts, fmt.Sprintf("%d pending", total))
	}
	if unplaced > 0 {
		parts = append(parts, fmt.Sprintf("%d member(s) not shown", unplaced))
	}
	parts = append(parts,
		chordFor(keys.PrevProposed)+"/"+chordFor(keys.NextProposed)+" move",
		chordFor(keys.AcceptProposed)+" accept",
		chordFor(keys.RejectProposed)+" reject",
		chordFor(keys.ToggleReview)+" edit")
	return strings.Join(parts, " · ")
}
