package complete

import (
	"testing"

	"raj/internal/keys"
)

func three() []Candidate {
	return []Candidate{
		{Word: "handoff"},
		{Word: "handleRequest"},
		{Word: "handshake", Detail: "other.go"},
	}
}

func open(t *testing.T) *Popup {
	t.Helper()
	p := &Popup{}
	p.Show("hand", three(), 10, 4)
	if !p.Open {
		t.Fatal("setup: popup did not open")
	}
	return p
}

// Nothing to offer means nothing on screen, so a caller can call Show after
// every keystroke without checking first.
func TestShowWithNoCandidatesCloses(t *testing.T) {
	p := open(t)
	p.Show("handz", nil, 10, 5)
	if p.Open {
		t.Error("the popup stayed open with no candidates")
	}
	if _, ok := p.Selected(); ok {
		t.Error("a closed popup still reports a selection")
	}
}

// The claimed keys are the whole contract: everything else must fall through
// and type, or the popup is modal in practice and swallows keystrokes.
func TestOnlyNavigationKeysAreConsumed(t *testing.T) {
	claimed := map[keys.Action]bool{
		keys.LineUp: true, keys.LineDown: true, keys.Cancel: true,
		keys.Indent: true, keys.Confirm: true,
	}
	falls := []keys.Action{
		keys.None, keys.CharLeft, keys.CharRight, keys.Backspace, keys.WordLeft,
		keys.LineStart, keys.LineEnd, keys.Save, keys.Undo, keys.SelLineDown,
		keys.DocStart, keys.PageDown, keys.DeleteLine, keys.Copy, keys.Cut,
	}
	for _, a := range falls {
		p := open(t)
		if _, _, consumed := p.Handle(a); consumed {
			t.Errorf("the popup consumed %v", a)
		}
		if claimed[a] {
			t.Errorf("test bug: %v is in both lists", a)
		}
	}
}

// A closed popup consumes nothing at all, so it cannot interfere when it is not
// showing.
func TestClosedPopupConsumesNothing(t *testing.T) {
	p := &Popup{}
	for _, a := range []keys.Action{keys.LineDown, keys.Confirm, keys.Cancel, keys.Indent} {
		if _, _, consumed := p.Handle(a); consumed {
			t.Errorf("a closed popup consumed %v", a)
		}
	}
}

func TestArrowsMoveTheSelection(t *testing.T) {
	p := open(t)
	if c, _ := p.Selected(); c.Word != "handoff" {
		t.Fatalf("starts on %q, want the first candidate", c.Word)
	}
	p.Handle(keys.LineDown)
	if c, _ := p.Selected(); c.Word != "handleRequest" {
		t.Errorf("after down: %q", c.Word)
	}
	p.Handle(keys.LineUp)
	if c, _ := p.Selected(); c.Word != "handoff" {
		t.Errorf("after up: %q", c.Word)
	}
	// Clamped at both ends rather than wrapping: wrapping past the end of a
	// short list makes the highlight jump the full height of the popup.
	p.Handle(keys.LineUp)
	if c, _ := p.Selected(); c.Word != "handoff" {
		t.Errorf("moved above the first candidate: %q", c.Word)
	}
	for i := 0; i < 10; i++ {
		p.Handle(keys.LineDown)
	}
	if c, _ := p.Selected(); c.Word != "handshake" {
		t.Errorf("moved past the last candidate: %q", c.Word)
	}
}

// Tab and enter both accept. Tab is what the fingers expect; enter because a
// list showing a highlighted row that enter does not take is a list nobody
// believes.
func TestTabAndEnterBothAccept(t *testing.T) {
	for _, a := range []keys.Action{keys.Indent, keys.Confirm} {
		p := open(t)
		p.Handle(keys.LineDown)
		got, ok, consumed := p.Handle(a)
		if !ok || !consumed {
			t.Fatalf("%v did not accept", a)
		}
		if got.Word != "handleRequest" {
			t.Errorf("%v accepted %q", a, got.Word)
		}
		if p.Open {
			t.Errorf("%v left the popup open", a)
		}
	}
}

// Escape closes without accepting, which is the escape hatch that makes the
// popup safe to leave open.
func TestEscapeClosesWithoutAccepting(t *testing.T) {
	p := open(t)
	got, ok, consumed := p.Handle(keys.Cancel)
	if ok {
		t.Errorf("escape accepted %q", got.Word)
	}
	if !consumed || p.Open {
		t.Error("escape did not close the popup")
	}
}

// The prefix is carried so a caller knows how much to replace. Losing it means
// accepting a candidate either duplicates the typed letters or eats the word
// in front of the cursor.
func TestPrefixIsCarried(t *testing.T) {
	p := open(t)
	if got := p.Prefix(); got != "hand" {
		t.Errorf("prefix = %q, want hand", got)
	}
	p.Handle(keys.Cancel)
	if got := p.Prefix(); got != "" {
		t.Errorf("a closed popup still reports the prefix %q", got)
	}
}

// Reopening resets the highlight: the candidates changed, so a selection index
// carried over from the previous list points at something unrelated.
func TestReopeningResetsTheSelection(t *testing.T) {
	p := open(t)
	p.Handle(keys.LineDown)
	p.Handle(keys.LineDown)
	p.Show("hands", []Candidate{{Word: "handshake"}, {Word: "handstand"}}, 10, 4)
	if c, _ := p.Selected(); c.Word != "handshake" {
		t.Errorf("selection carried over to %q", c.Word)
	}
}

// The popup must not panic on the shapes it meets while a buffer is edited.
func TestPopupDegenerateInputs(t *testing.T) {
	p := &Popup{}
	p.Show("", nil, 0, 0)
	p.Show("x", []Candidate{{Word: ""}}, -5, -5)
	p.Handle(keys.Confirm)
	p.Hide()
	p.Hide()
	if _, ok := p.Selected(); ok {
		t.Error("a hidden popup reports a selection")
	}
}
