package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/intent"
	"raj/internal/keys"
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
// the screen rather than the file: what you see is what you decided. While the
// waiting list is open the chord decides the selected row instead, so accept
// and reject mean the row under the highlight.
func (a *App) reviewProposed(accept bool) {
	if a.decideSelectedProposal(accept) {
		return
	}
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if m, ok := a.proposalAtCaret(p); ok {
		decided := a.decideProposed(p, m.Group, accept)
		if !decided {
			// A refusal is the status decideRemote or the local flip left; it
			// never advances, so the user stays on the set that was refused.
			if !a.attach {
				a.status = fmt.Sprintf("change set %d is no longer awaiting a decision", m.Group)
			}
			return
		}
		if !accept {
			// A rejected set stays in view: only accept advances, so the user
			// can read the rejection, and clear is still one chord away.
			a.status = decidedStatus(false, m.Group)
			return
		}
		a.advanceAfterDecision(decidedStatus(true, m.Group))
		return
	}
	// Nothing under the caret: decide every proposed change on screen, so a
	// review pass is read-then-one-chord rather than one chord per change.
	// The visible set is captured once — that is what the user saw, and the
	// gesture may only decide those sets — but a reject re-reads the pending
	// projection after each reversal and unwinds newest-first, mirroring the
	// socket's `reject -all`. List order alone goes stale under the walk: a
	// set can only come out once every later edit that overlaps it is gone.
	visible := a.proposalsVisible(p)
	if len(visible) == 0 {
		a.status = "no proposed changes here"
		return
	}
	wanted := make(map[uint64]bool, len(visible))
	for _, m := range visible {
		wanted[m.Group] = true
	}
	n := 0
	refused := ""
	record := func(ok bool) {
		if ok {
			n++
			return
		}
		if a.attach {
			refused = a.status
		}
	}
	if accept {
		// Accepting is order-independent: the text is already in the
		// document, so the decision only clears a mark.
		for _, m := range visible {
			record(a.decideProposed(p, m.Group, true))
		}
	} else {
		attempted := map[uint64]bool{}
		for {
			id, ok := newestWanted(p.File, wanted, attempted)
			if !ok {
				break
			}
			attempted[id] = true
			record(a.decideProposed(p, id, false))
		}
	}
	if refused != "" {
		// A host refusal is what the user needs to see; a count of what landed
		// before it would hide why the pass stopped.
		a.status = refused
		return
	}
	verb := "accepted"
	if !accept {
		verb = "rejected"
	}
	a.status = fmt.Sprintf("%s %d of %d proposed change set(s) on screen", verb, n, len(visible))
}

// newestWanted picks the newest still-pending change set this bulk walk wants
// and has not attempted yet. It re-reads Session.DiffPending on every call, so
// a reversal that takes a set out of the pending projection is seen rather than
// worked from a list captured before it. DiffPending lists oldest-first, so the
// last match is the newest; newest-first is the order a reversal needs, because
// an earlier set can be blocked by a later overlapping one and the later has to
// come out first. attempted keeps the loop finite when a set cannot come out at
// all.
func newestWanted(f *editor.File, wanted, attempted map[uint64]bool) (uint64, bool) {
	var id uint64
	found := false
	for _, d := range f.Session().DiffPending() {
		if len(d.Hunks) == 0 {
			continue // no surviving text: nothing left to reverse
		}
		if wanted[d.Group.ID] && !attempted[d.Group.ID] {
			id, found = d.Group.ID, true
		}
	}
	return id, found
}

// decidedStatus is the one-line confirmation for a single decision.
func decidedStatus(accept bool, group uint64) string {
	if accept {
		return fmt.Sprintf("accepted change set %d", group)
	}
	return fmt.Sprintf("rejected change set %d", group)
}

// decideProposed applies one decision through the File, so its decision
// generation moves with it and Dirty/ViewDirty see the change. Accepting
// cannot fail — the text is already in the document and the mark clears.
// Rejecting returns false only when the set names nothing the journal holds or
// is already Rejected; it never removes text, so a later edit can no longer
// wedge it.
func (a *App) decideProposed(p *editor.Pane, group uint64, accept bool) bool {
	// An attached client does not own the document: the decision goes to the
	// daemon, and the local tab is replaced from the daemon answer rather than
	// mutated, so the next wake cannot overwrite it.
	if a.attach {
		return a.decideRemote(p, group, accept)
	}
	defer a.flushJournal(p)
	if accept {
		p.File.AcceptGroup(group)
		return true
	}
	if !p.File.RejectGroup(group) {
		return false
	}
	p.Cursors.Normalize()
	a.Explorer.Tree.MarkChanged(p.File.Path)
	return true
}

// advanceAfterDecision moves the caret to the next pending change set after a
// single decision, so repeated accept/reject walks the proposals one per press.
// It reuses the review walk (document order, wrapping at the ends) and keeps
// the decision confirmation in the status beside the new position. With no set
// left to land on it stays where it is and the confirmation stands alone.
func (a *App) advanceAfterDecision(confirm string) {
	if pos, total, ok := a.cycleProposedTo(true); ok {
		a.status = fmt.Sprintf("%s · proposal %d of %d", confirm, pos, total)
		return
	}
	a.status = confirm
}

// clearRejected hard-purges the rejected change set at the caret. Clearing is
// an edit — ClearRejected reverses the set's ops out of the document — so it
// goes through File, which mirrors the reversal into the line index and moves
// the decision generation with it. A line carrying more than one rejected set
// cannot name one, and says so rather than purging the wrong text; a
// rejected-set picker would be the surface for that case.
func (a *App) clearRejected() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	groups := rejectedAtCaret(p.File, p.Cursors.Primary().Head)
	if len(groups) > 1 {
		a.status = fmt.Sprintf("%d rejected change sets here; the caret cannot pick one", len(groups))
		return
	}
	if len(groups) == 1 {
		a.clearChangeSet(p, groups[0])
		return
	}
	// No rejected set here. An invalid (superseded) set has no surviving hunk
	// of its own, but a rejected collider can restore its run to the review
	// view; a caret on that run reaches the invalid set it holds.
	invalids := invalidAtCaret(p.File, p.Cursors.Primary().Head)
	if len(invalids) == 0 {
		a.status = "no rejected or invalid changes here"
		return
	}
	if len(invalids) > 1 {
		a.status = fmt.Sprintf("%d invalid change sets here; the caret cannot pick one", len(invalids))
		return
	}
	a.clearChangeSet(p, invalids[0])
}

// clearChangeSet disposes one change set through File.ClearGroup, which
// reverses a rejected set and drops an invalid Proposed set with the same
// gesture. It is the shared body of the clear chord, so the caret addresses a
// set by id and the wording says which kind was disposed. An attached client
// forwards the clear to the daemon rather than mutating the local tab.
func (a *App) clearChangeSet(p *editor.Pane, id uint64) {
	if a.attach {
		a.clearRemote(p, id)
		return
	}
	wasInvalid := invalidGroup(p.File, id)
	defer a.flushJournal(p)
	if ok, _ := p.File.ClearGroup(id); !ok {
		a.status = fmt.Sprintf("could not clear change set %d: later edits overlap it", id)
		return
	}
	p.Cursors.Normalize()
	a.Explorer.Tree.MarkChanged(p.File.Path)
	if wasInvalid {
		a.status = fmt.Sprintf("discarded invalid change set %d", id)
		return
	}
	a.status = fmt.Sprintf("cleared rejected change set %d", id)
}

// invalidGroup reports whether id names a still-Proposed invalid set. It is
// read before the disposal, which flips the set to Rejected and so clears the
// derived Invalid flag with it.
func invalidGroup(f *editor.File, id uint64) bool {
	for _, g := range f.Session().Groups() {
		if g.ID == id {
			return g.State == piecetable.Proposed && g.Invalid
		}
	}
	return false
}

// invalidAtCaret names the invalid (superseded) change sets whose address
// touches the caret's line, in document order. An invalid set has no surviving
// hunk, so there are two ways to reach one: the live set that consumed it,
// which InvalidBy names and where that set sits, or a restored run the
// Annotated view keeps after a rejected collider put it back. A set with
// neither (wholly consumed, no collider) has no caret address; `clear --group`
// and `clear --all` reach it.
func invalidAtCaret(f *editor.File, off int) []uint64 {
	invalid := map[uint64]bool{}
	for _, g := range f.Session().Groups() {
		if g.State == piecetable.Proposed && g.Invalid {
			invalid[g.ID] = true
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	line := f.LineOf(off)
	type hit struct {
		id    uint64
		start int
	}
	var hits []hit
	seen := map[uint64]bool{}
	at := func(id uint64, start, end int) {
		first := f.LineOf(start)
		last := first
		if end > start {
			last = f.LineOf(end - 1)
		}
		if line < first || line > last || seen[id] {
			return
		}
		seen[id] = true
		hits = append(hits, hit{id, start})
	}
	for _, g := range f.Session().Groups() {
		if invalid[g.ID] && g.InvalidBy != nil {
			at(g.ID, g.InvalidBy.Start, g.InvalidBy.End)
		}
	}
	for _, r := range f.Session().Project(piecetable.Annotated).States() {
		if invalid[r.Group] && r.Len > 0 {
			at(r.Group, r.Off, r.Off+r.Len)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
	out := make([]uint64, len(hits))
	for i, h := range hits {
		out[i] = h.id
	}
	return out
}

// rejectedAtCaret names the rejected change sets whose live runs touch the
// caret's line, in document order. Rejected sets carry no PendingMarks — those
// are proposed only — so the lookup reads the Annotated projection, whose
// state runs cover every live edit in the same coordinates as the buffer the
// caret indexes. Line covering matches proposalAtCaret: a caret anywhere on a
// line a set owns reaches it. More than one set on the line comes back whole
// rather than guessed between.
func rejectedAtCaret(f *editor.File, off int) []uint64 {
	line := f.LineOf(off)
	var out []uint64
	seen := map[uint64]bool{}
	for _, r := range f.Session().Project(piecetable.Annotated).States() {
		if r.State != piecetable.Rejected || seen[r.Group] {
			continue
		}
		first, last := f.LineOf(r.Off), f.LineOf(r.Off)
		if r.Len > 0 {
			last = f.LineOf(r.Off + r.Len - 1)
		}
		if line < first || line > last {
			continue
		}
		seen[r.Group] = true
		out = append(out, r.Group)
	}
	return out
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
		if seen[m.Group] {
			continue
		}
		// The viewport is a window of display rows; a fold above the set shifts
		// its row away from its session line, so the visibility test projects
		// first. A mark a fold hides entirely has no row to be on screen.
		row := m.DispLine(p)
		if row < 0 || !p.Viewport.Visible(row) {
			continue
		}
		seen[m.Group] = true
		out = append(out, m)
	}
	return out
}

// proposalGroups lists the distinct pending change sets in document order, one
// mark each — the first hunk of the set that the display draws. PendingMarks
// comes out oldest first, which is not document order once a set is written
// after something below it, so the sort is what makes next/prev walk the file
// rather than the journal.
func proposalGroups(p *editor.Pane) []editor.PendingMark {
	seen := map[uint64]bool{}
	var out []editor.PendingMark
	for _, m := range p.PendingMarks() {
		if seen[m.Group] {
			continue
		}
		// A mark a fold hides has no row to jump to, so the set is represented
		// by the first mark that is drawn; a set with no drawn mark drops out
		// of the walk rather than landing the caret on a fold row.
		if m.DispLine(p) < 0 {
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
	pos, total, ok := a.cycleProposedTo(forward)
	if !ok {
		a.status = "no proposed changes"
		return
	}
	a.status = fmt.Sprintf("proposal %d of %d", pos, total)
}

// cycleProposedTo is the walk cycleProposed and the post-decision advance
// share: it moves the caret to the next or previous pending set in document
// order and returns its 1-based position and the total, or ok false when
// nothing is pending. The ends wrap, so the walk never dead-ends.
func (a *App) cycleProposedTo(forward bool) (pos, total int, ok bool) {
	p := a.Tabs.Active()
	if p == nil {
		return 0, 0, false
	}
	groups := proposalGroups(p)
	if len(groups) == 0 {
		return 0, 0, false
	}
	on, hasOn := a.proposalAtCaret(p)
	caretLine := p.File.LineOf(p.Cursors.Primary().Head)
	next := cycleTarget(groups, caretLine, on.Group, hasOn, forward)
	jumpToSessionLine(p, groups[next].Line+1)
	return next + 1, len(groups), true
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

// openProposals opens the waiting list: one row for every pending proposal in
// the workspace -- a change set, a deletion, a folder removal, a publish -- so
// ctrl+alt+v answers "what is an agent waiting on me for" in one place. The
// rows read App.Proposals, the same rollup `raj ctl proposals` prints, so the
// keyboard and the wire cannot disagree about what is waiting. The app keeps
// the rows it built so a decision chord can name the selected one back.
func (a *App) openProposals() {
	rows := a.Proposals()
	if len(rows) == 0 {
		a.pendingList = nil
		a.status = "nothing waiting for you"
		return
	}
	a.pendingList = rows
	list := make([]picker.Proposal, 0, len(rows))
	for i, pr := range rows {
		label, line := a.proposalRow(pr)
		list = append(list, picker.Proposal{Label: label, Path: pr.Path, Line: line, Index: i})
	}
	a.Picker.ShowProposals(list)
	a.focus = FocusPicker
	a.status = ""
}

// proposalRow labels one waiting proposal and reports the 1-based line a text
// change sits on, so choosing the row can jump there. A proposal that is not
// text in a buffer -- a deletion, a folder removal, a publish -- has no line.
func (a *App) proposalRow(pr control.Proposal) (label string, line int) {
	who := a.participantName(piecetable.Author(pr.Author))
	name := a.proposalName(pr.Path)
	switch pr.Kind {
	case "set", "invalid":
		verb := "change set"
		if pr.Kind == "invalid" {
			verb = "superseded set"
		}
		label = fmt.Sprintf("%s · %s %d · %s · %s", who, verb, pr.Group, name, humanBytes(pr.Size))
		if p := a.proposalSetPane(pr.Path); p != nil {
			line = firstMarkLine(p, pr.Group)
		}
	case "delete":
		label = fmt.Sprintf("%s · delete · %s · %s", who, name, humanBytes(pr.Size))
	case "rmdir":
		label = fmt.Sprintf("%s · remove folder · %s · %s", who, name, fileCount(pr.Size))
	case "publish":
		label = fmt.Sprintf("%s · publish · %s", who, name)
	default:
		label = fmt.Sprintf("%s · %s · %s", who, pr.Kind, name)
	}
	return label, line
}

// proposalName is the workspace-relative spelling of a proposal path when it
// lies under the primary root, and the path itself otherwise -- a publish names
// a wave, not a file, and stays as it is.
func (a *App) proposalName(path string) string {
	root := a.primaryRoot()
	if path == "" || root == "" {
		return path
	}
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// proposalSetPane is the pane holding a change set: an open tab or a headless
// buffer. Proposals includes both, so a decision must reach both.
func (a *App) proposalSetPane(path string) *editor.Pane {
	if p := a.paneFor(path); p != nil {
		return p
	}
	for _, p := range a.headless {
		if p.File.Path == path {
			return p
		}
	}
	return nil
}

// firstMarkLine is the 1-based line of the first drawn pending mark for a
// change set, or 0 when a fold hides every mark or the pane has none. It is the
// address the list enter jumps through.
func firstMarkLine(p *editor.Pane, group uint64) int {
	for _, m := range p.PendingMarks() {
		if m.Group == group && m.DispLine(p) >= 0 {
			return m.Line + 1
		}
	}
	return 0
}

// humanBytes formats a proposal byte size for the list. A negative value is a
// net removal, so the magnitude is what the row carries; the kind and the set
// already say which direction it went.
func humanBytes(n int) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n == 0:
		return "0 bytes"
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// fileCount names how many files a folder removal would take.
func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// decideSelectedProposal applies accept or reject to the row the waiting list
// has selected, and reports whether the list was open so the chord was consumed
// there. The caret-based review walk must never also run under the overlay.
func (a *App) decideSelectedProposal(accept bool) bool {
	if !a.Picker.Open || a.Picker.Mode() != picker.Proposals {
		return false
	}
	idx, ok := a.Picker.SelectedProposal()
	if !ok || idx < 0 || idx >= len(a.pendingList) {
		return false
	}
	pr := a.pendingList[idx]
	a.decideProposal(pr, accept)
	a.closeProposalList()
	if pr.Kind == "set" || pr.Kind == "invalid" {
		a.gotoProposal(pr)
	}
	return true
}

// closeProposalList hides the waiting list and hands the keyboard back to the
// editor, so after a decision the user sees the text the row named.
func (a *App) closeProposalList() {
	if a.Picker.Mode() == picker.Proposals {
		a.Picker.Hide()
	}
	if a.focus == FocusPicker {
		a.focus = FocusEditor
	}
}

// gotoProposal opens the file a text proposal names and lands the caret on the
// change, the same place enter takes. Opening and jumping both write the status
// line, so the decision status the caller set is restored over them: the user
// needs to read what was decided, not which file was opened.
func (a *App) gotoProposal(pr control.Proposal) {
	if pr.Path == "" {
		return
	}
	line := 0
	if p := a.proposalSetPane(pr.Path); p != nil {
		line = firstMarkLine(p, pr.Group)
	}
	status := a.status
	a.OpenFile(pr.Path)
	if line > 0 {
		a.jumpTo(line)
	}
	a.status = status
}

// decideProposal carries out one decision on one waiting proposal. It is the
// one place a list row kind is interpreted, so the local and attached paths
// cannot drift: a change set goes through decideProposed (reject only marks;
// clear is a separate chord), a deletion or folder removal through the same
// approve/withdraw the gate prompt uses, and a publish through the intention
// command the CLI runs. An attached client forwards every one to the daemon.
func (a *App) decideProposal(pr control.Proposal, accept bool) {
	switch pr.Kind {
	case "set":
		p := a.proposalSetPane(pr.Path)
		if p == nil {
			a.status = "no open buffer for " + filepath.Base(pr.Path)
			return
		}
		if !a.decideProposed(p, pr.Group, accept) {
			if !a.attach {
				a.status = fmt.Sprintf("change set %d is no longer awaiting a decision", pr.Group)
			}
			return
		}
		if accept {
			a.status = decidedStatus(true, pr.Group)
			return
		}
		// Reject only marks the set; the text stays and clear is the second
		// gesture. The status names it so the two-step stays discoverable.
		a.status = fmt.Sprintf("rejected change set %d; %s clears it", pr.Group, chordFor(keys.ClearRejected))
	case "invalid":
		p := a.proposalSetPane(pr.Path)
		if p == nil {
			a.status = "no open buffer for " + filepath.Base(pr.Path)
			return
		}
		if accept {
			a.status = fmt.Sprintf("change set %d is superseded; clear it instead", pr.Group)
			return
		}
		a.clearChangeSet(p, pr.Group)
	case "delete":
		d, ok := a.pendingDeletionFor(pr.Path)
		if !ok {
			a.status = "no pending deletion for " + filepath.Base(pr.Path)
			return
		}
		if accept {
			if a.attach {
				a.approveDeletionRemote(d)
				return
			}
			a.removeDeleted(a.openDeletionPane(d.Path), d.Path)
			return
		}
		if a.attach {
			a.withdrawDeletionRemote(d)
			return
		}
		a.withdrawDeletion(d)
	case "rmdir":
		d, ok := a.pendingDirRemovalFor(pr.Path)
		if !ok {
			a.status = "no pending dir-removal for " + filepath.Base(pr.Path)
			return
		}
		if accept {
			if a.attach {
				a.approveDirRemovalRemote(d)
				return
			}
			a.removeDirDeleted(d)
			return
		}
		if a.attach {
			a.withdrawDirRemovalRemote(d)
			return
		}
		a.withdrawDirRemoval(d)
	case "publish":
		a.decidePublish(pr.Path, accept)
	default:
		a.status = "unknown proposal kind " + pr.Kind
	}
}

// decidePublish accepts (publishes) or rejects (withdraws) a pending publish.
// Locally it runs the intention command the CLI runs, so a publish decided from
// the list is the same outward step; an attached client forwards the decision
// to the daemon, which owns the refs and the pinned hook.
func (a *App) decidePublish(name string, accept bool) {
	cmd := intent.Command{Mode: "publish", Name: name}
	if accept {
		cmd.Approve = true
	} else {
		cmd.Withdraw = true
	}
	if a.attach {
		res, err := a.sendIntent(cmd)
		if err != nil {
			a.status = "attach: " + err.Error()
			return
		}
		if !res.OK {
			a.status = res.Err
			return
		}
		delete(a.pendingPublishes, name)
	} else if _, err := a.runIntent(context.Background(), cmd); err != nil {
		a.status = err.Error()
		return
	}
	if accept {
		a.status = "publishing " + name
		return
	}
	a.status = "withdrew the publish of " + name
}

// decideProposal is deliberately the only interpreter of a list row kind:
// adding a proposal kind means teaching this switch, not every chord.
//
// waitingNote is the status-line surface for everything an agent is waiting on
// the user to decide: one count across every kind, and the chord that opens the
// list. It is empty with nothing waiting, so the no-pending case draws nothing.
func (a *App) waitingNote() string {
	n := a.waitingCount()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d waiting for you (%s)", n, chordFor(keys.ReviewProposed))
}

// waitingCount is the cheap count behind waitingNote: the workspace-level maps,
// plus each buffer pending change sets. It does not walk DiffPending, so it
// stays a fold the frame can afford.
func (a *App) waitingCount() int {
	n := len(a.pendingDeletions) + len(a.pendingDirRemovals) + len(a.pendingPublishes)
	for _, p := range a.Tabs.All() {
		n += len(p.File.Session().Pending())
	}
	for _, p := range a.headless {
		n += len(p.File.Session().Pending())
	}
	return n
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

// canShowReview reports whether the save review can actually be presented on
// the screen the app has. It is the fail-open half of the confirm: a dialog
// whose box cannot fit draws nothing yet still takes the keyboard, so a save
// must never be left waiting on one. A nil screen can show nothing.
func (a *App) canShowReview(message string, rows []string) bool {
	if a.screen == nil {
		return false
	}
	cols, screenRows := a.screen.Size()
	return prompt.ReviewFits(message, rows, cols, screenRows)
}

// reviewSave is the save gesture when the buffer holds another writer's
// proposals: a review dialog listing the pending sets, with the current one
// jumping the caret to where it sits, before the user answers save-anyway or
// steps into Review mode. The sets the listing names come from Session.Pending,
// and the places they sit from Pane.PendingMarks — the same two projections the
// gutter and the proposals picker read, so the save review cannot disagree with
// either. other is the subset the user did not author, whose count and names the
// message carries.
func (a *App) reviewSave(p *editor.Pane, pending []piecetable.Group, other []piecetable.Group, then func(saved bool)) {
	rows, lines := a.reviewRows(p, pending)
	message := a.saveConfirmMessage(other)
	// FAIL OPEN. The confirm warns but never guards: if it cannot be drawn
	// there is nothing to answer, so the save proceeds rather than waiting
	// forever on a dialog nobody can see.
	if !a.canShowReview(message, rows) {
		a.saveNow(p, then)
		return
	}
	a.beforePrompt()
	a.Prompt.Review(
		"Save with proposed changes",
		message,
		rows,
		[]string{prompt.SaveAnyway, prompt.ReviewOption},
		func(row int) {
			if row < len(lines) && lines[row] > 0 {
				jumpToSessionLine(p, lines[row])
			}
		},
		func(answer string, ok bool) {
			switch {
			case !ok || answer == prompt.Cancel:
				a.status = "save cancelled"
				report(then, false)
			case answer == prompt.SaveAnyway:
				a.saveNow(p, then)
			case answer == prompt.ReviewOption:
				a.EnterReview()
				report(then, false)
			}
		})
}

// reviewRows describes one pending set per row for the save review, and the
// 1-based line its first drawn pending mark sits on, so the dialog can name a
// set and the caret can visit it. A set whose marks have all been moved out
// from under it, or hidden behind a fold, has no line; the caret stays put.
func (a *App) reviewRows(p *editor.Pane, pending []piecetable.Group) (rows []string, lines []int) {
	firstLine := map[uint64]int{}
	for _, m := range p.PendingMarks() {
		if _, seen := firstLine[m.Group]; seen {
			continue
		}
		// A fold-hidden mark has no row to jump to; the set is represented by
		// the first mark that is drawn, and one with none keeps the caret put.
		if m.DispLine(p) < 0 {
			continue
		}
		firstLine[m.Group] = m.Line + 1 // jumpToSessionLine counts from 1
	}
	for _, g := range pending {
		rows = append(rows, fmt.Sprintf("group %d · %s · %+d bytes · %d op(s)",
			g.ID, a.participantName(g.Author), g.Bytes, g.Ops))
		lines = append(lines, firstLine[g.ID])
	}
	return rows, lines
}
