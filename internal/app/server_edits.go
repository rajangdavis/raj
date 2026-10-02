package app

import (
	"sort"

	"raj/internal/complete"
	"raj/internal/editor"
	"raj/internal/lsp"
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
	if group, leased := editsLeased(p, edits); leased {
		return group, false
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

// editsLeased reports the first change set that leases a byte the edit batch
// touches, or 0/false when the whole batch is clear. It is the all-or-nothing
// gate every server-edit applier shares: checking each span before any is
// applied is what keeps a pending, rejected or invalidated change set from
// splitting a batch, and naming the set is what lets the caller point at the
// decision that has to happen first.
func editsLeased(p *editor.Pane, edits []complete.Edit) (group uint64, leased bool) {
	if p == nil || p.File == nil {
		return 0, false
	}
	for _, e := range edits {
		// A pure insertion has End-Start == 0, which probes the insertion
		// point itself; a replacement is caught by the bytes it takes away.
		if g, ok := p.File.EditLeased(e.Start, e.End-e.Start); ok {
			return g, true
		}
	}
	return 0, false
}

// editTarget is one document a server edit will touch: the pane holding it and
// the complete.Edit batch to apply.
type editTarget struct {
	pane  *editor.Pane
	edits []complete.Edit
}

// resolveEditTargets resolves every document before any edit lands: an open
// buffer is used as-is; an unopened file is loaded headlessly and recorded in
// the returned loaded slice so the caller can announce it on success or drop it
// again on a later refusal. A document that cannot be loaded refuses the whole
// batch: every buffer loaded for the attempt is dropped again, status is set to
// refuse(path), and ok is false. The conversion from the server's LSP ranges to
// byte spans lives here, once, so both server-edit callers share it.
func (a *App) resolveEditTargets(docs []lsp.DocumentEdits, refuse func(path string) string) (targets []editTarget, loaded []*editor.Pane, ok bool) {
	targets = make([]editTarget, 0, len(docs))
	for _, d := range docs {
		p, found := a.paneByPath(d.Path)
		if !found {
			q, err := a.loadHeadless(d.Path)
			if err != nil {
				for _, l := range loaded {
					a.dropHeadless(l)
				}
				a.status = refuse(d.Path)
				return nil, nil, false
			}
			p = q
			loaded = append(loaded, q)
		}
		doc := lsp.NewDocument(p.File.Text())
		edits := make([]complete.Edit, 0, len(d.Edits))
		for _, te := range d.Edits {
			start, end := doc.Span(te.Range)
			edits = append(edits, complete.Edit{Start: start, End: end, Text: te.NewText})
		}
		targets = append(targets, editTarget{pane: p, edits: edits})
	}
	return targets, loaded, true
}
