package picker

import (
	"testing"

	"raj/internal/keys"
)

// Proposals mode is the review pass in the quick-open overlay: the rows are
// labelled by the caller, and choosing one answers with the file and the line
// the change sits on, the same shape a symbol choice has.
func TestProposalsModeChoosesALine(t *testing.T) {
	p := New("/root")
	p.ShowProposals("/root/main.go", []Proposal{
		{Label: "group 3 · AgentD · +40 bytes · 2 op(s)", Line: 12},
		{Label: "group 4 · AgentD · -7 bytes · 1 op(s)", Line: 30},
	})
	if !p.Open || p.Mode() != Proposals {
		t.Fatalf("mode = %v open = %v, want an open proposals list", p.Mode(), p.Open)
	}
	if p.Results() != 2 {
		t.Fatalf("results = %d, want 2", p.Results())
	}

	path := p.Handle(keys.Confirm, "")
	if path != "/root/main.go" {
		t.Fatalf("chose %q, want the file the proposals are in", path)
	}
	pos, ok := p.PositionFor("/root/main.go")
	if !ok || pos.Line != 12 {
		t.Errorf("position = %+v ok = %v, want line 12", pos, ok)
	}
	if p.Open {
		t.Error("choosing should close the overlay")
	}
}

// The query narrows the review list by whole tokens, not the file matcher's
// subsequence: typing a group id leaves the matching change set, and the
// digits in the byte deltas stay inert.
func TestProposalsModeFilters(t *testing.T) {
	p := New("/root")
	p.ShowProposals("/root/main.go", []Proposal{
		{Label: "group 3 · AgentD · +40 bytes", Line: 12},
		{Label: "group 4 · AgentD · -7 bytes", Line: 30},
	})
	p.Handle(keys.None, "4")
	if p.Results() != 1 {
		t.Fatalf("results after query = %d, want 1", p.Results())
	}
	if got := p.Top(); got != "group 4 · AgentD · -7 bytes" {
		t.Errorf("top = %q, want the group 4 row", got)
	}
}
