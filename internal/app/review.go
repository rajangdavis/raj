package app

import (
	"fmt"
	"sort"

	"raj/internal/editor"
	"raj/internal/picker"
	"raj/internal/piecetable"
	"raj/internal/prompt"
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

// proposalGroups lists the distinct pending change sets in document order, one
// mark each — the first hunk of the set. PendingMarks comes out oldest first,
// which is not document order once a set is written after something below it,
// so the sort is what makes next/prev walk the file rather than the journal.
func proposalGroups(p *editor.Pane) []editor.PendingMark {
	seen := map[uint64]bool{}
	var out []editor.PendingMark
	for _, m := range p.PendingMarks() {
		if seen[m.Group] {
			continue
		}
		seen[m.Group] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// cycleProposed moves the caret to the next or previous pending change set
// without opening the picker, so a review pass can step change to change and
// decide each with accept or reject. The order is document order over distinct
// sets and the ends wrap, so the gesture never dead-ends. The caret picks the
// starting point only: inside a set it steps from that set, and between sets it
// steps to the nearest one in the direction of travel.
func (a *App) cycleProposed(forward bool) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	groups := proposalGroups(p)
	if len(groups) == 0 {
		a.status = "no proposed changes"
		return
	}
	on, hasOn := a.proposalAtCaret(p)
	caretLine := p.File.LineOf(p.Cursors.Primary().Head)
	next := cycleTarget(groups, caretLine, on.Group, hasOn, forward)
	a.jumpTo(groups[next].Line + 1)
	a.status = fmt.Sprintf("proposal %d of %d", next+1, len(groups))
}

// cycleTarget is the index cycleProposed lands on. A caret inside a set steps
// from that set; a caret between sets steps to the first set after it going
// forward and the last set before it going back. Either end wraps.
func cycleTarget(groups []editor.PendingMark, caretLine int, onGroup uint64, on, forward bool) int {
	n := len(groups)
	if on {
		for i, g := range groups {
			if g.Group == onGroup {
				if forward {
					return (i + 1) % n
				}
				return (i - 1 + n) % n
			}
		}
	}
	if forward {
		for i, g := range groups {
			if g.Line > caretLine {
				return i
			}
		}
		return 0
	}
	for i := n - 1; i >= 0; i-- {
		if groups[i].Line < caretLine {
			return i
		}
	}
	return n - 1
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

// reviewSave is the save gesture when the buffer holds proposals: a review
// dialog listing the pending sets, with the current one jumping the caret to
// where it sits, before the user answers accept-all-and-save or cancel. The
// sets the listing names come from Session.Pending, and the places they sit
// from Pane.PendingMarks — the same two projections the gutter and the
// proposals picker read, so the save review cannot disagree with either.
func (a *App) reviewSave(p *editor.Pane, pending []piecetable.Group, then func(saved bool)) {
	rows, lines := a.reviewRows(p, pending)
	a.beforePrompt()
	a.Prompt.Review(
		"Save with proposed changes",
		fmt.Sprintf("Accept all %d proposed change set(s)?", len(pending)),
		rows,
		[]string{prompt.Save, prompt.Cancel},
		func(row int) {
			if row < len(lines) && lines[row] > 0 {
				a.jumpTo(lines[row])
			}
		},
		func(answer string, ok bool) {
			if !ok || answer != prompt.Save {
				a.status = "save cancelled"
				report(then, false)
				return
			}
			if p.File.Path == "" {
				a.saveAs(p, then)
				return
			}
			a.writeTo(p, p.File.Path, then)
		})
}

// reviewRows describes one pending set per row for the save review, and the
// 1-based line its first pending mark sits on, so the dialog can name a set
// and the caret can visit it. A set whose marks have all been moved out from
// under it has no line; the caret stays put for it.
func (a *App) reviewRows(p *editor.Pane, pending []piecetable.Group) (rows []string, lines []int) {
	firstLine := map[uint64]int{}
	for _, m := range p.PendingMarks() {
		if _, seen := firstLine[m.Group]; !seen {
			firstLine[m.Group] = m.Line + 1 // jumpTo counts from 1
		}
	}
	for _, g := range pending {
		rows = append(rows, fmt.Sprintf("group %d · %s · %+d bytes · %d op(s)",
			g.ID, a.participantName(g.Author), g.Bytes, g.Ops))
		lines = append(lines, firstLine[g.ID])
	}
	return rows, lines
}
