package editor

import (
	"testing"

	"raj/internal/piecetable"
	"raj/internal/ui"
)

// One scenario, one set of constants: the hunk span, the replacement text and
// the expected marks below are all derived from these, so a change to the
// fixture changes the expectation with it.
const (
	proposalFixture = "user text here"
	proposalAt      = 5
	proposalOld     = "text"
	proposalNew     = "AGNT"
)

// propose writes hunks as an agent and marks the group proposed, mirroring
// what the socket host does with an agent's apply: the text lands, and the
// decision about it is still outstanding.
func propose(t *testing.T, p *Pane, hunks ...piecetable.Hunk) uint64 {
	t.Helper()
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Agent, p.File.Session().Version(), hunks)
	p.File.End()
	id := p.File.Session().LastGroup()
	p.File.Session().MarkGroup(id, piecetable.Proposed)
	return id
}

// A proposed hunk shows up as one mark in current coordinates, with the group
// and author it came from and the line the gutter will mark.
func TestPendingMarksProjectTheHunk(t *testing.T) {
	p := newTestPane(proposalFixture)
	id := propose(t, p, piecetable.Hunk{Start: proposalAt, End: proposalAt + len(proposalOld), Text: proposalNew})

	marks := p.PendingMarks()
	if len(marks) != 1 {
		t.Fatalf("marks = %+v, want one", marks)
	}
	m := marks[0]
	if m.Group != id {
		t.Errorf("group = %d, want %d", m.Group, id)
	}
	if m.Author != piecetable.Agent {
		t.Errorf("author = %v, want the agent", m.Author)
	}
	if m.Start != proposalAt || m.End != proposalAt+len(proposalNew) {
		t.Errorf("span = %d..%d, want %d..%d", m.Start, m.End, proposalAt, proposalAt+len(proposalNew))
	}
	if m.Line != 0 || m.Removed {
		t.Errorf("line = %d removed = %v, want line 0, not removed", m.Line, m.Removed)
	}
}

// A pure deletion is a zero-width mark where the text came out: nothing left
// to tint inline, so the gutter carries it, in red.
func TestPendingMarksPureDeletion(t *testing.T) {
	p := newTestPane("aaa bbb\n")
	propose(t, p, piecetable.Hunk{Start: 3, End: 3 + len(" bbb"), Text: ""})

	marks := p.PendingMarks()
	if len(marks) != 1 {
		t.Fatalf("marks = %+v, want one", marks)
	}
	m := marks[0]
	if !m.Removed {
		t.Error("a hunk with no new text must read as a removal")
	}
	if m.Start != m.End {
		t.Errorf("removal span = %d..%d, want zero width", m.Start, m.End)
	}
	if m.Line != 0 {
		t.Errorf("line = %d, want 0", m.Line)
	}
}

// CoversLine is what accept-at-caret matches on: the hunk's own lines,
// including the line a deletion's gap sits on.
func TestPendingMarkCoversLine(t *testing.T) {
	p := newTestPane("one\ntwo\nthree\n")
	// Replace "two\n" with "T\nT\n": the hunk now spans lines 1 and 2.
	propose(t, p, piecetable.Hunk{Start: 4, End: 4 + len("two\n"), Text: "T\nT\n"})
	marks := p.PendingMarks()
	if len(marks) != 1 {
		t.Fatalf("marks = %+v, want one", marks)
	}
	m := marks[0]
	for _, c := range []struct {
		line int
		want bool
	}{{0, false}, {1, true}, {2, true}, {3, false}} {
		if got := m.CoversLine(p.File, c.line); got != c.want {
			t.Errorf("covers line %d = %v, want %v", c.line, got, c.want)
		}
	}
}

// Pending added text renders green — the added half of a diff — on top of the
// attribution tint, and the surrounding user text keeps neither.
func TestRenderTintsProposedText(t *testing.T) {
	p := newTestPane(proposalFixture)
	propose(t, p, piecetable.Hunk{Start: proposalAt, End: proposalAt + len(proposalOld), Text: proposalNew})

	s := ui.NewScreen(40, 10)
	th := DefaultTheme()
	p.Render(s, 0, 0, 40, 10, th)

	gut := p.GutterWidth()
	if bg := s.At(gut+proposalAt, 0).Style.Bg; bg != th.ProposedAdd {
		t.Errorf("proposed text bg = %v, want %v", bg, th.ProposedAdd)
	}
	if bg := s.At(gut+0, 0).Style.Bg; bg == th.AgentTint || bg == th.ProposedAdd {
		t.Errorf("user text bg = %v, want untinted", bg)
	}
}

// Accepting a change set ends the review: the same text renders with the plain
// attribution tint from then on.
func TestAcceptedTextFallsBackToAuthorTint(t *testing.T) {
	p := newTestPane(proposalFixture)
	id := propose(t, p, piecetable.Hunk{Start: proposalAt, End: proposalAt + len(proposalOld), Text: proposalNew})
	p.File.Session().AcceptGroup(id)

	s := ui.NewScreen(40, 10)
	th := DefaultTheme()
	p.Render(s, 0, 0, 40, 10, th)

	gut := p.GutterWidth()
	if bg := s.At(gut+proposalAt, 0).Style.Bg; bg != th.AgentTint {
		t.Errorf("accepted text bg = %v, want the plain author tint %v", bg, th.AgentTint)
	}
	if len(p.PendingMarks()) != 0 {
		t.Errorf("marks after accept = %+v, want none", p.PendingMarks())
	}
}
