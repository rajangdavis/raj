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

// A merely Proposed span is advisory: an edit inside it lands in the editor's
// own group, and the proposal's member fragments into one mark per surviving
// run. The runs share the group id, so the review surface still reads them as
// the one change set they are.
func TestPendingMarksFragmentAroundAnEditInsideAnAdvisoryProposal(t *testing.T) {
	p := newTestPane("hello world\n")
	id := propose(t, p, piecetable.Hunk{Start: 6, End: 11, Text: "socket"})

	// A second writer applies inside the proposed text, at offset 8 — the
	// advisory ApplyDiff path, not the human typing path (which still refuses).
	conflicts, _ := p.File.ApplyDiff(piecetable.User, p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 8, End: 8, Text: "XYZ"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the advisory proposal to allow the edit", conflicts)
	}
	if got := p.File.Text(); got != "hello soXYZcket\n" {
		t.Fatalf("text = %q, want the edit applied between the proposal's halves", got)
	}
	marks := p.PendingMarks()
	if len(marks) != 2 {
		t.Fatalf("marks = %+v, want the fragmented run as two marks", marks)
	}
	want := [][2]int{{6, 8}, {11, 15}}
	for i, m := range marks {
		if m.Group != id {
			t.Errorf("mark %d group = %d, want %d", i, m.Group, id)
		}
		if m.Start != want[i][0] || m.End != want[i][1] {
			t.Errorf("mark %d span = %d..%d, want %d..%d", i, m.Start, m.End, want[i][0], want[i][1])
		}
		if !m.CoversLine(p.File, 0) {
			t.Errorf("mark %d must still cover its line for accept-at-caret", i)
		}
	}
}

// Overwriting every run of an advisory proposal lands the edit and moves the
// set: it drops out of Pending (there is nothing left to decide or to block a
// save on) while DiffPending still reports it as moved, so a reviewer can see
// what the buffer moved past.
func TestPendingDropsAWhollyOverwrittenProposal(t *testing.T) {
	p := newTestPane("hello world\n")
	id := propose(t, p, piecetable.Hunk{Start: 6, End: 11, Text: "socket"})

	conflicts, _ := p.File.ApplyDiff(piecetable.User, p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 6, End: 12, Text: "port"}})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want the advisory proposal to allow the overwrite", conflicts)
	}
	if got := p.File.Text(); got != "hello port\n" {
		t.Fatalf("text = %q, want the overwrite applied", got)
	}
	if marks := p.PendingMarks(); len(marks) != 0 {
		t.Errorf("marks = %+v, want no surviving run", marks)
	}
	if got := p.File.Session().Pending(); len(got) != 0 {
		t.Errorf("pending = %+v, want the overwritten set out of the pending list", got)
	}
	diffs := p.File.Session().DiffPending()
	if len(diffs) != 1 || diffs[0].Group.ID != id || diffs[0].Moved != 1 {
		t.Errorf("diffs = %+v, want set %d reported as moved", diffs, id)
	}
}
