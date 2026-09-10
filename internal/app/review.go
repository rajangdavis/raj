package app

import (
	"fmt"

	"raj/internal/editor"
	"raj/internal/picker"
	"raj/internal/piecetable"
)

// Reviewing proposals from the keyboard.
//
// The socket exposes accept and reject per change set; the keyboard gets the
// same two decisions at the caret. Only the address differs: a socket caller
// names a group id, the editor finds the group under the caret — and both end
// at AcceptGroup and RejectGroup, so a change decided on one surface is
// decided on the other. The ranges both surfaces read come from one place,
// Pane.PendingMarks, which is itself only a projection of Session.DiffPending.

// reviewProposed accepts or rejects the proposed change set at the caret, or
// every one visible when the caret is not on any of them. The bulk form is
// the screen rather than the file: what you see is what you decided.
func (a *App) reviewProposed(accept bool) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if m, ok := a.proposalAtCaret(p); ok {
		if a.decideProposed(p, m.Group, accept) {
			a.status = decidedStatus(accept, m.Group)
		} else {
			a.status = fmt.Sprintf("change set %d is wedged behind later edits; "+
				"it cannot be backed out", m.Group)
		}
		return
	}
	// Nothing under the caret: decide every proposed change on screen, so a
	// review pass is read-then-one-chord rather than one chord per change.
	visible := a.proposalsVisible(p)
	if len(visible) == 0 {
		a.status = "no proposed changes here"
		return
	}
	n := 0
	for _, m := range visible {
		if a.decideProposed(p, m.Group, accept) {
			n++
		}
	}
	verb := "accepted"
	if !accept {
		verb = "rejected"
	}
	a.status = fmt.Sprintf("%s %d of %d proposed change set(s) on screen", verb, n, len(visible))
}

// decidedStatus is the one-line confirmation for a single decision.
func decidedStatus(accept bool, group uint64) string {
	if accept {
		return fmt.Sprintf("accepted change set %d", group)
	}
	return fmt.Sprintf("rejected change set %d", group)
}

// decideProposed applies one decision. Accepting cannot fail — the text is
// already in the document — while rejecting can: a group wedged behind a
// later overlapping edit cannot be rebased out, and false says so.
func (a *App) decideProposed(p *editor.Pane, group uint64, accept bool) bool {
	sess := p.File.Session()
	if accept {
		sess.AcceptGroup(group)
		return true
	}
	if !sess.RejectGroup(group) {
		return false
	}
	p.Cursors.Normalize()
	a.Explorer.Tree.MarkChanged(p.File.Path)
	return true
}

// proposalAtCaret is the proposed change the caret is on. Line covering
// rather than byte covering: a caret at the start of a changed line should
// decide that change, and a deletion has no bytes to be inside of.
func (a *App) proposalAtCaret(p *editor.Pane) (editor.PendingMark, bool) {
	caretLine := p.File.LineOf(p.Cursors.Primary().Head)
	for _, m := range p.PendingMarks() {
		if m.CoversLine(p.File, caretLine) {
			return m, true
		}
	}
	return editor.PendingMark{}, false
}

// proposalsVisible is every proposed change set with a mark on screen, one
// entry per set, oldest first.
func (a *App) proposalsVisible(p *editor.Pane) []editor.PendingMark {
	seen := map[uint64]bool{}
	var out []editor.PendingMark
	for _, m := range p.PendingMarks() {
		if seen[m.Group] || !p.Viewport.Visible(m.Line) {
			continue
		}
		seen[m.Group] = true
		out = append(out, m)
	}
	return out
}

// reviewPicker lists the pending change sets in the quick-open overlay, so a
// review pass can jump from one to the next and decide each where it sits.
// It is a third picker mode beside files and symbols: same overlay, different
// rows, and choosing a row lands the caret on the change.
func (a *App) reviewPicker() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	var rows []picker.Proposal
	seen := map[uint64]bool{}
	for _, m := range p.PendingMarks() {
		if seen[m.Group] {
			continue
		}
		seen[m.Group] = true
		rows = append(rows, picker.Proposal{
			Label: fmt.Sprintf("group %d · %s · %+d bytes · %d op(s)",
				m.Group, a.participantName(m.Author), m.Bytes, m.Ops),
			Line: m.Line + 1, // the picker counts from 1
		})
	}
	if len(rows) == 0 {
		a.status = "no proposed changes here"
		return
	}
	a.Picker.ShowProposals(p.File.Path, rows)
	a.focus = FocusPicker
	a.status = ""
}

// participantName resolves an author to its display name, from the participant
// table when the control listener is up. Without it there is no registry to
// ask — a test harness, or a session nobody drove — and the numeric id is the
// honest fallback.
func (a *App) participantName(author piecetable.Author) string {
	if a.control != nil && a.control.Participants != nil {
		if p, ok := a.control.Participants.Get(uint8(author)); ok && p.Name != "" {
			return p.Name
		}
	}
	return fmt.Sprintf("author %d", author)
}

// participantInitial is the gutter one-cell who: the first letter of the
// participant name. Colour says what the change is; this says whose it is.
func (a *App) participantInitial(author piecetable.Author) string {
	name := a.participantName(author)
	if name == "" {
		return "?"
	}
	return string([]rune(name)[:1])
}
