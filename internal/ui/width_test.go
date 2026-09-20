package ui

import "testing"

// The ambiguous class is narrow by default, but the terminal draws the three
// horizontal arrows two cells wide. Counting them one column put the caret one
// cell left of the text for every arrow on the line.
func TestHorizontalArrowsAreWide(t *testing.T) {
	for _, r := range []rune{'←', '→', '↔'} {
		if got := RuneWidth(r); got != 2 {
			t.Errorf("RuneWidth(%q U+%04X) = %d, want 2", r, r, got)
		}
	}
	// An em-dash is ambiguous too and stays narrow, so this is a targeted
	// exception rather than a blanket ambiguous-wide switch.
	if got := RuneWidth('—'); got != 1 {
		t.Errorf("RuneWidth(em dash U+2014) = %d, want 1", got)
	}
}

// The fast paths and the pre-existing wide classes must not move.
func TestWidthClassesUnchanged(t *testing.T) {
	if got := RuneWidth('a'); got != 1 {
		t.Errorf("ASCII width = %d, want 1", got)
	}
	if got := RuneWidth('日'); got != 2 {
		t.Errorf("CJK width = %d, want 2", got)
	}
	if got := RuneWidth('\u0301'); got != 0 {
		t.Errorf("combining mark width = %d, want 0", got)
	}
}
