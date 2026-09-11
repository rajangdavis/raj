package editor

import (
	"raj/internal/piecetable"
)

// Reviewing what an agent proposed.
//
// A change set lands as a proposal: in the document, tinted, and blocked from
// reaching disk until the user decides it. What the review UI needs is the
// same thing the renderer needs — where each pending set sits NOW, not where
// it was written. Session.DiffPending already walks the journal and rebases
// every member forward; PendingMarks projects that result into the form the
// pane, the gutter and the keybindings all consume, so no consumer re-runs
// the walk and no two consumers can disagree about where a change is.

// PendingMark is one proposed hunk in current coordinates. Start and End are
// the byte span the hunk's inserted text covers now — zero width for a pure
// deletion, whose old text is gone from the document by definition. Line is
// the first line the hunk touches, where the gutter carries its mark. Removed
// is set when the hunk took text away: it is the difference between a green
// mark and a red one. Ops and Bytes describe the change set the hunk belongs
// to, for a review list that has to say how big it is.
type PendingMark struct {
	Group   uint64
	Author  piecetable.Author
	Start   int
	End     int
	Line    int
	Removed bool
	Ops     int
	Bytes   int
}

// PendingMarks lists the proposed changes in this buffer, oldest first, as
// hunk spans rebased onto the current document. It is a projection of
// Session.DiffPending, nothing more — the walk stays in the piece table, this
// stays a mapping.
//
// A member a later edit fragmented comes back as one mark per surviving run,
// and the runs share the group id, so the gutter, the caret and next/prev
// still read them as the one change set they are.
func (p *Pane) PendingMarks() []PendingMark {
	var out []PendingMark
	for _, g := range p.File.Session().DiffPending() {
		for _, h := range g.Hunks {
			out = append(out, PendingMark{
				Group:   g.Group.ID,
				Author:  g.Group.Author,
				Start:   h.Start,
				End:     h.End,
				Line:    p.File.LineOf(h.Start),
				Removed: h.New == "",
				Ops:     g.Group.Ops,
				Bytes:   g.Group.Bytes,
			})
		}
	}
	return out
}

// CoversLine reports whether the hunk touches the given line. A deletion's
// zero-width gap counts as covering the line it sits on, so a change that
// removes an entire line is still decided from the line that held it.
func (m PendingMark) CoversLine(f *File, line int) bool {
	first := m.Line
	last := first
	if m.End > m.Start {
		last = f.LineOf(m.End - 1)
	}
	return line >= first && line <= last
}
