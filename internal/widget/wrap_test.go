package widget

import (
	"strings"
	"testing"
)

// Wrap must keep every byte of a long unbroken token: an absolute path has no
// space to break at, and losing its middle is the truncation bug in miniature.
func TestWrapKeepsWholeUnbrokenWord(t *testing.T) {
	long := "/Users/rajandavis/Desktop/projects/raj/internal/app/deletion.go"
	for _, w := range []int{8, 16, 24, 33, 49} {
		lines := Wrap(long, w)
		if got := strings.Join(lines, ""); got != long {
			t.Errorf("Wrap(%q, %d) = %q, lost text", long, w, got)
		}
		for _, line := range lines {
			if cols := runeCols(line); cols > w {
				t.Errorf("Wrap(%q, %d) line %q is %d cols wide", long, w, line, cols)
			}
		}
	}
}

// A message made of words breaks at spaces rather than mid-word.
func TestWrapBreaksAtSpaces(t *testing.T) {
	text := "agent 2 proposed removing pkg. a.go: Unsaved and 1 pending set."
	lines := Wrap(text, 20)
	if got := strings.Join(lines, " "); got != text {
		t.Errorf("Wrap = %q, want %q", got, text)
	}
	for _, line := range lines {
		if cols := runeCols(line); cols > 20 {
			t.Errorf("line %q is %d cols; want <= 20", line, cols)
		}
	}
	if strings.Contains(strings.Join(lines, ""), "…") {
		t.Errorf("Wrap introduced an ellipsis: %q", lines)
	}
}

func TestWrapHonoursNewlines(t *testing.T) {
	lines := Wrap("first\nsecond", 40)
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Errorf("Wrap = %q; want [first second]", lines)
	}
}

func TestWrapEmptyReservesARow(t *testing.T) {
	if got := Wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("Wrap(\"\", 10) = %q; want one empty line", got)
	}
}
