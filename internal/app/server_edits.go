package app

import (
	"sort"

	"raj/internal/complete"
	"raj/internal/editor"
)

// applyServerEdits applies a batch of server range replacements to a pane as one
// undo step. It is the one application path for every server edit the editor
// takes — completion's textEdit plus additionalTextEdits, and formatting's
// whole TextEdit list — so a server's edit list is never applied as N separate
// writes and one undo always reverses the whole answer.
//
// The edits are positions in the pane's text. They are applied highest offset
// first so earlier spans stay valid as later ones change the text beneath them;
// for equal starts the wider span goes first so an insertion at a point lands
// before a replacement there rather than inside it.
//
// Every span is checked against the buffer's leases before any is applied. A
// pending, rejected or invalidated change set owns its text until the user
// decides it, and a batch that overlaps one is refused whole — naming the set —
// rather than applying the edits that do not overlap and leaving the buffer
// half-formatted with no note of which part landed. The refusal is returned
// rather than recorded on the pane so a caller that wants a different status
// wording can have one; the set id is the same one leaseNote names.
func applyServerEdits(p *editor.Pane, edits []complete.Edit) (group uint64, ok bool) {
	if p == nil || len(edits) == 0 {
		return 0, true
	}
	for _, e := range edits {
		// A pure insertion has remove == 0, which probes the insertion point
		// itself; a replacement is caught by the bytes it takes away.
		if g, leased := p.File.EditLeased(e.Start, e.End-e.Start); leased {
			return g, false
		}
	}
	sorted := append([]complete.Edit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start > sorted[j].Start
		}
		return sorted[i].End > sorted[j].End
	})
	// One Begin/End brackets the whole batch: however many replacements the
	// server named, they are one user action and one undo step.
	p.File.Begin()
	for _, e := range sorted {
		p.ReplaceRange(e.Start, e.End, e.Text)
	}
	p.File.End()
	return 0, true
}
