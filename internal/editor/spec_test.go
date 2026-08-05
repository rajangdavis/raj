package editor

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"raj/internal/keys"
)

// The invariants in CURSOR-VIEWPORT-SPEC.md, asserted as properties rather than
// as examples. Every cursor bug so far has been a disagreement between two
// representations of the same position — byte offset, line/column, display
// column, visual row — and an example test only catches the disagreement it
// happens to look at.
//
// The driver below walks a random document with a random action sequence and
// checks every invariant after every action, so a violation is reported at the
// step that caused it rather than at the end.

// specActions are the actions the driver picks from: everything that moves a
// cursor, scrolls, or edits, since the invariants must hold across all three.
var specActions = []keys.Action{
	keys.CharLeft, keys.CharRight, keys.LineUp, keys.LineDown,
	keys.WordLeft, keys.WordRight, keys.LineStart, keys.LineEnd,
	keys.DocStart, keys.DocEnd, keys.PageUp, keys.PageDown,
	keys.SelCharLeft, keys.SelCharRight, keys.SelLineUp, keys.SelLineDown,
	keys.SelPageUp, keys.SelPageDown, keys.SelectLine, keys.SplitIntoLines,
	keys.CursorAbove, keys.CursorBelow, keys.SelectAll, keys.Cancel,
	keys.Backspace, keys.Delete, keys.DeleteLine, keys.LineBelow, keys.LineAbove,
	keys.Undo, keys.Redo, keys.Indent, keys.Outdent,
}

// specDoc builds a document with the shapes that break position arithmetic:
// empty lines, tabs, wide runes, a line with no trailing newline.
func specDoc(rng *rand.Rand) string {
	parts := []string{
		"package main", "", "\tif err != nil {", "\t\treturn err", "\t}",
		"日本語のテキスト", "x", "", "    indented four", "ends without newline",
		"mixed\ttabs\tand spaces", "→ an arrow ←",
	}
	var b strings.Builder
	for i, n := 0, 3+rng.Intn(30); i < n; i++ {
		b.WriteString(parts[rng.Intn(len(parts))])
		if i < n-1 || rng.Intn(2) == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// specSeeds is how many random documents the driver walks. 40 runs in a
// fraction of a second and is the gate; raising it to 400 currently reproduces
// the redo/UTF-8 bug filed in TODO.md at wrap=false seed=245 step=51, which is
// how that bug was found in the first place.
const specSeeds = 40

func TestCursorViewportSpec(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		for seed := int64(0); seed < specSeeds; seed++ {
			rng := rand.New(rand.NewSource(seed))
			p := newTestPane(specDoc(rng))
			p.Wrap = wrap
			p.Resize(24+rng.Intn(40), 4+rng.Intn(12))

			for step := 0; step < 60; step++ {
				before := snapshot(p)
				a := specActions[rng.Intn(len(specActions))]
				if rng.Intn(6) == 0 {
					// Whole runes: typing half of one would make the document
					// invalid UTF-8, which is a property of the driver rather
					// than of the editor.
					typed := []string{"a", "b", "\t", "λ", "\n", "日"}
					p.HandleText(typed[rng.Intn(len(typed))])
					a = keys.None
				} else {
					p.Handle(a)
				}
				if err := checkInvariants(p, a, before); err != "" {
					t.Fatalf("wrap=%v seed=%d step=%d action=%s: %s\nbefore: %s",
						wrap, seed, step, a, err, before)
				}
			}
		}
	}
}

// snapshot is the state the scrolling invariant compares against.
type paneState struct {
	cursors []Cursor
	top     int
	len     int
}

func (s paneState) String() string {
	return "cursors=" + strconv.Itoa(len(s.cursors)) + " top=" + strconv.Itoa(s.top) + " len=" + strconv.Itoa(s.len)
}

func snapshot(p *Pane) paneState {
	cs := make([]Cursor, len(p.Cursors.All()))
	copy(cs, p.Cursors.All())
	return paneState{cursors: cs, top: p.Viewport.Top, len: p.File.Len()}
}

func checkInvariants(p *Pane, a keys.Action, before paneState) string {
	n := p.File.Len()

	// I1. Every cursor offset is a valid position in the document, on a rune
	// boundary. An offset inside a multi-byte rune indexes text that does not
	// exist as far as the renderer is concerned.
	for i, c := range p.Cursors.All() {
		for _, off := range []int{c.Head, c.Anchor} {
			if off < 0 || off > n {
				return "cursor " + strconv.Itoa(i) + " at " + strconv.Itoa(off) + " outside [0," + strconv.Itoa(n) + "]"
			}
			if off < n && !utf8.RuneStart(p.File.Text()[off]) {
				return "cursor " + strconv.Itoa(i) + " at " + strconv.Itoa(off) + " is mid-rune"
			}
		}
	}

	// I2. Cursors are sorted by head and do not overlap. Two cursors covering
	// the same text apply every subsequent edit twice at one place.
	all := p.Cursors.All()
	for i := 1; i < len(all); i++ {
		if all[i].Head < all[i-1].Head {
			return "cursors out of order at " + strconv.Itoa(i)
		}
		_, prevHi := all[i-1].Range()
		lo, _ := all[i].Range()
		if lo < prevHi {
			return "cursors " + strconv.Itoa(i-1) + " and " + strconv.Itoa(i) + " overlap"
		}
	}

	// I3. Offset and line/column agree in both directions. This is the
	// disagreement every cursor bug has turned out to be.
	for _, c := range p.Cursors.All() {
		line, col := p.File.LineCol(c.Head)
		if line < 0 || line >= p.File.Lines() {
			return "head " + strconv.Itoa(c.Head) + " reports line " + strconv.Itoa(line) + " of " + strconv.Itoa(p.File.Lines())
		}
		if start := p.File.LineStart(line); c.Head < start {
			return "head " + strconv.Itoa(c.Head) + " is before the start of its own line"
		}
		if end := p.File.LineEnd(line); c.Head > end+1 {
			return "head " + strconv.Itoa(c.Head) + " is past the end of its own line"
		}
		if back := p.File.OffsetAt(line, col); back != c.Head {
			return "offset " + strconv.Itoa(c.Head) + " -> (" + strconv.Itoa(line) + "," + strconv.Itoa(col) + ") -> " + strconv.Itoa(back)
		}
	}

	// I4. The line index agrees with a rescan of the text. A stale index turns
	// every position question into a wrong answer at once.
	text := p.File.Text()
	if got, want := p.File.Lines(), strings.Count(text, "\n")+1; got != want {
		return "index has " + strconv.Itoa(got) + " lines, text has " + strconv.Itoa(want)
	}
	for line := 0; line < p.File.Lines(); line++ {
		if l := p.File.LineOf(p.File.LineStart(line)); l != line {
			return "line " + strconv.Itoa(line) + " starts at an offset that reports line " + strconv.Itoa(l)
		}
	}

	// I5. The viewport is within the document.
	if p.Viewport.Top < 0 || (p.File.Lines() > 0 && p.Viewport.Top >= p.File.Lines()) {
		return "viewport top " + strconv.Itoa(p.Viewport.Top) + " outside the document"
	}

	// I6. Scrolling moves the view and nothing else; everything else that
	// consumed a keystroke leaves the primary cursor visible.
	switch a {
	case keys.PageUp, keys.PageDown:
		if len(p.Cursors.All()) != len(before.cursors) {
			return "paging changed the cursor count"
		}
		for i, c := range p.Cursors.All() {
			if c.Head != before.cursors[i].Head || c.Anchor != before.cursors[i].Anchor {
				return "paging moved cursor " + strconv.Itoa(i)
			}
		}
	default:
		line := p.File.LineOf(p.Cursors.Primary().Head)
		if !p.Viewport.Visible(line) {
			return "primary cursor on line " + strconv.Itoa(line) + " is off screen at top " + strconv.Itoa(p.Viewport.Top)
		}
	}
	return ""
}

// Vertical motion must be reversible: down then up returns to the column you
// started in, even across short lines. This is the goal column, and it is the
// single most noticeable cursor bug an editor can have.
func TestGoalColumnSurvivesShortLines(t *testing.T) {
	p := newTestPane("a long line of text here\nx\n\nanother long line of text\n")
	p.Resize(40, 10)
	p.Cursors.Set(20, 20)
	_, want := p.File.LineCol(20)

	for _, down := range []int{1, 2, 3} {
		p.Cursors.Set(20, 20)
		for i := 0; i < down; i++ {
			p.Handle(keys.LineDown)
		}
		for i := 0; i < down; i++ {
			p.Handle(keys.LineUp)
		}
		if _, got := p.File.LineCol(p.Cursors.Primary().Head); got != want {
			t.Errorf("down %d and back: column %d, want %d", down, got, want)
		}
	}
}
