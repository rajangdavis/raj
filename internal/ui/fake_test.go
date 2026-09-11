package ui

import (
	"strings"
	"testing"
)

// A typo'd chord — "escape" where the canonical name is "esc" — used to be
// dropped silently, so the only signal was an assertion failing three steps
// later on a frame that never came. Press now refuses at the line that made
// the mistake.
func TestFakeHostPressPanicsOnUnknownChord(t *testing.T) {
	h := NewFakeHost(80, 24)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Press dropped an unknown chord silently")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, `"escape"`) {
			t.Errorf("panic = %v, want it to name the bogus chord", r)
		}
	}()
	h.Press("escape")
}

// The control case: a canonical chord still queues its event.
func TestFakeHostPressQueuesKnownChord(t *testing.T) {
	h := NewFakeHost(80, 24)
	h.Press("esc")
	select {
	case <-h.Events():
	default:
		t.Error("no event queued for a known chord")
	}
}

// scratch: review-popup probe — reject this
