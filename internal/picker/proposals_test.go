package picker

import (
	"testing"

	"raj/internal/keys"
)

// Proposals mode is the waiting list in the quick-open overlay: the rows are
// labelled by the caller and each carries its own file and line, so choosing
// one answers with the place that row named.
func TestProposalsModeChoosesALine(t *testing.T) {
	p := New("/root")
	p.ShowProposals([]Proposal{
		{Label: "group 3 · AgentD · +40 bytes · 2 op(s)", Path: "/root/main.go", Line: 12, Index: 0},
		{Label: "group 4 · AgentD · -7 bytes · 1 op(s)", Path: "/root/main.go", Line: 30, Index: 1},
	})
	if !p.Open || p.Mode() != Proposals {
		t.Fatalf("mode = %v open = %v, want an open proposals list", p.Mode(), p.Open)
	}
	if p.Results() != 2 {
		t.Fatalf("results = %d, want 2", p.Results())
	}
	if idx, ok := p.SelectedProposal(); !ok || idx != 0 {
		t.Fatalf("selected index = %d ok = %v, want row 0", idx, ok)
	}

	path := p.Handle(keys.Confirm, "")
	if path != "/root/main.go" {
		t.Fatalf("chose %q, want the file the row names", path)
	}
	pos, ok := p.PositionFor("/root/main.go")
	if !ok || pos.Line != 12 {
		t.Errorf("position = %+v ok = %v, want line 12", pos, ok)
	}
	if p.Open {
		t.Error("choosing should close the overlay")
	}
}

// A workspace list spans files: choosing a later row lands in the file that
// row names rather than the first, because each row carries its own path.
func TestProposalsModeChoosesTheRowFile(t *testing.T) {
	p := New("/root")
	p.ShowProposals([]Proposal{
		{Label: "A · change set 1 · a.go · 10 bytes", Path: "/root/a.go", Line: 3, Index: 0},
		{Label: "B · change set 2 · b.go · 20 bytes", Path: "/root/b.go", Line: 9, Index: 1},
	})
	if idx, ok := p.SelectedProposal(); !ok || idx != 0 {
		t.Fatalf("selected index = %d ok = %v, want row 0", idx, ok)
	}
	p.Handle(keys.LineDown, "")
	if idx, ok := p.SelectedProposal(); !ok || idx != 1 {
		t.Fatalf("selected index after down = %d ok = %v, want row 1", idx, ok)
	}
	path := p.Handle(keys.Confirm, "")
	if path != "/root/b.go" {
		t.Fatalf("chose %q, want /root/b.go", path)
	}
	pos, ok := p.PositionFor("/root/b.go")
	if !ok || pos.Line != 9 {
		t.Errorf("position = %+v ok = %v, want line 9", pos, ok)
	}
}

// A row with no place to jump names no path and leaves no position for the next
// file the picker opens.
func TestProposalsModeNonTextRowHasNoPosition(t *testing.T) {
	p := New("/root")
	p.ShowProposals([]Proposal{
		{Label: "A · delete · gone.go", Path: "", Index: 0},
	})
	if path := p.Handle(keys.Confirm, ""); path != "" {
		t.Fatalf("chose %q, want no path for a row with nowhere to go", path)
	}
	if _, ok := p.PositionFor("/root/anything.go"); ok {
		t.Error("a non-text row left a stale position behind")
	}
}

// The query narrows the review list by whole tokens, not the file matcher's
// subsequence: typing a group id leaves the matching change set, and the
// digits in the byte deltas stay inert.
func TestProposalsModeFilters(t *testing.T) {
	p := New("/root")
	p.ShowProposals([]Proposal{
		{Label: "group 3 · AgentD · +40 bytes", Path: "/root/main.go", Line: 12, Index: 0},
		{Label: "group 4 · AgentD · -7 bytes", Path: "/root/main.go", Line: 30, Index: 1},
	})
	p.Handle(keys.None, "4")
	if p.Results() != 1 {
		t.Fatalf("results after query = %d, want 1", p.Results())
	}
	if got := p.Top(); got != "group 4 · AgentD · -7 bytes" {
		t.Errorf("top = %q, want the group 4 row", got)
	}
}
