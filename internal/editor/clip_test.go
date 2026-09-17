package editor

import (
	"fmt"
	"strings"
	"testing"
)

// pasteFixture builds a pane with n selected regions to copy from.
func pasteFixture(n, lineLen int) *Pane {
	body := strings.Repeat(strings.Repeat("x", lineLen)+"\n", n*2)
	p := NewPane(NewFile("t.go", body, 2))
	p.Resize(80, 40)
	p.Cursors.Set(lineLen, 0)
	for i := 1; i < n; i++ {
		start := i * (lineLen + 1)
		p.Cursors.Add(start+lineLen, start)
	}
	return p
}

// TestInternalPasteStoresNothing is the property the piece clipboard exists
// for: text copied from the buffer is already in its stores, and those stores
// are append-only, so pasting it back appends nothing whatever its size.
func TestInternalPasteStoresNothing(t *testing.T) {
	for _, size := range []int{100, 10000, 1 << 20} {
		p := NewPane(NewFile("t.go", strings.Repeat("a", size)+"\n", 2))
		p.Resize(80, 40)
		p.Cursors.Set(size, 0)
		clip := p.Copy()

		before := p.File.Session().Store().Bytes()
		p.Cursors.Set(size+1, size+1)
		p.PasteClip(clip)
		stored := p.File.Session().Store().Bytes() - before

		if stored != 0 {
			t.Errorf("size %d: pasting stored %d bytes, want none", size, stored)
		}
		if p.File.Len() != 2*size+1 {
			t.Errorf("size %d: length %d, want %d", size, p.File.Len(), 2*size+1)
		}
	}
}

// An external clip has no pieces, so it must be appended.
func TestExternalPasteAppends(t *testing.T) {
	p := NewPane(NewFile("t.go", "body\n", 2))
	p.Resize(80, 40)
	before := p.File.Session().Store().Bytes()
	p.PasteClip(Clip{Text: "from another program"})
	if got := p.File.Session().Store().Bytes() - before; got != len("from another program") {
		t.Errorf("stored %d bytes, want the text appended once", got)
	}
}

// Copied text survives deletion of the original: stores never erase.
func TestClipSurvivesDeletingTheOriginal(t *testing.T) {
	p := NewPane(NewFile("t.go", "keep this text\n", 2))
	p.Resize(80, 40)
	p.Cursors.Set(9, 5)
	clip := p.Copy()
	if clip.Text != "this" {
		t.Fatalf("copy = %q", clip.Text)
	}
	p.File.Delete(p.Author, 0, p.File.Len())
	p.Cursors.Set(0, 0)
	p.PasteClip(clip)
	if got := p.File.Text(); got != "this" {
		t.Errorf("text = %q; a clip must outlive the region it came from", got)
	}
}

// Pasted text keeps its original author, so agent-written code stays tinted as
// agent code however many times it is moved around.
func TestPasteKeepsAttribution(t *testing.T) {
	p := NewPane(NewFile("t.go", "", 2))
	p.Resize(80, 40)
	p.File.Insert(2, 0, "agentcode") // author 2 is the first agent
	p.Cursors.Set(9, 0)
	clip := p.Copy()
	p.Cursors.Set(9, 9)
	p.PasteClip(clip)

	for _, sp := range p.File.Spans(9, 9) {
		if sp.Author != 2 {
			t.Errorf("pasted span attributed to %d, want the original author", sp.Author)
		}
	}
}

// A whole-line copy pasted with the caret next to the text goes in as a line
// below, rather than at the raw caret: the original line is untouched and the
// duplicate keeps its own indent. The reported case was a caret after the
// indentation; a caret anywhere on the line behaves the same.
func TestWholeLinePasteGoesBelowNotAtCaret(t *testing.T) {
	const body = "\tfoo\nbar\n"
	for _, caret := range []int{0, 1, 4} { // column 0, after indent, end of line
		p := NewPane(NewFile("t.go", body, 2))
		p.Resize(80, 40)
		p.Cursors.Set(caret, caret)
		clip := p.Copy()
		p.PasteClip(clip)
		if got, want := p.File.Text(), "\tfoo\n\tfoo\nbar\n"; got != want {
			t.Errorf("caret %d: got %q, want %q", caret, got, want)
		}
	}
}

// An empty line is filled rather than duplicated, so no stray blank line is
// left behind; the empty line's own newline terminates the pasted line.
func TestWholeLinePasteFillsEmptyLine(t *testing.T) {
	p := NewPane(NewFile("t.go", "one\n\nthree\n", 2))
	p.Resize(80, 40)
	p.Cursors.Set(0, 0)
	clip := p.Copy()
	p.Cursors.Set(4, 4) // the empty second line
	p.PasteClip(clip)
	if got, want := p.File.Text(), "one\none\nthree\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The final empty line has no newline of its own to inherit, so the pasted
// line brings one; the file still ends in a newline rather than gaining a
// blank final line.
func TestWholeLinePasteFillsFinalEmptyLine(t *testing.T) {
	p := NewPane(NewFile("t.go", "one\n", 2))
	p.Resize(80, 40)
	p.Cursors.Set(0, 0)
	clip := p.Copy()
	p.Cursors.Set(p.File.Len(), p.File.Len())
	p.PasteClip(clip)
	if got, want := p.File.Text(), "one\none\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// After a linewise paste the cursor sits on the pasted line's first non-blank
// byte, matching Vim's p, for both the pasted-below and the filled case.
func TestLinewisePasteCursorAtFirstNonBlank(t *testing.T) {
	below := NewPane(NewFile("t.go", "\tfoo\nbar\n", 2))
	below.Resize(80, 40)
	below.Cursors.Set(0, 0)
	below.PasteClip(below.Copy())
	if got, want := below.Cursors.Primary().Head, 6; got != want {
		t.Errorf("below: cursor at %d, want %d", got, want)
	}

	filled := NewPane(NewFile("t.go", "one\n\nthree\n", 2))
	filled.Resize(80, 40)
	filled.Cursors.Set(0, 0)
	clip := filled.Copy()
	filled.Cursors.Set(4, 4)
	filled.PasteClip(clip)
	if got, want := filled.Cursors.Primary().Head, 4; got != want {
		t.Errorf("filled: cursor at %d, want %d", got, want)
	}
}

// TestClipCost reports what each strategy costs, for the record.
func TestClipCost(t *testing.T) {
	for _, n := range []int{1, 4, 16, 64} {
		p := pasteFixture(n, 40)
		clip := p.Copy()
		before := p.File.Session().Store().Bytes()
		beforePieces := p.File.Pieces()
		p.PasteClip(clip)
		t.Logf("cursors=%2d clip=%4dB  spliced: %d pieces, %d bytes stored",
			n, len(clip.Text), p.File.Pieces()-beforePieces,
			p.File.Session().Store().Bytes()-before)
	}
}

func BenchmarkPasteInternalVsExternal(b *testing.B) {
	for _, size := range []int{1000, 100000} {
		body := strings.Repeat("x", size)
		b.Run(fmt.Sprintf("spliced/%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				p := NewPane(NewFile("t.go", body, 2))
				p.Resize(80, 40)
				p.Cursors.Set(size, 0)
				clip := p.Copy()
				p.Cursors.Set(size, size)
				b.StartTimer()
				p.PasteClip(clip)
				b.StopTimer()
			}
		})
		b.Run(fmt.Sprintf("appended/%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				p := NewPane(NewFile("t.go", body, 2))
				p.Resize(80, 40)
				p.Cursors.Set(size, size)
				b.StartTimer()
				p.PasteClip(Clip{Text: body})
				b.StopTimer()
			}
		})
	}
}

// An external clipboard whose line count matches the cursor count distributes,
// the way VSCode does: it is the only reading under which a multi-cursor paste
// from another program does something useful.
func TestExternalDistributesOnLineCountMatch(t *testing.T) {
	p := pasteFixture(3, 4)
	p.PasteClip(Clip{Text: "one\ntwo\nthree"})
	body := p.File.Text()
	for _, want := range []string{"one", "two", "three"} {
		if strings.Count(body, want) != 1 {
			t.Errorf("%q appears %d times, want once at its own cursor",
				want, strings.Count(body, want))
		}
	}
}

// A mismatched external clipboard goes in whole at every cursor: distributing
// it would keep only the first line and drop the rest, and the old primary-only
// fallback left the other cursors untouched.
func TestExternalWholeOnMismatch(t *testing.T) {
	p := pasteFixture(3, 4)
	p.PasteClip(Clip{Text: "one\ntwo"})
	if n := strings.Count(p.File.Text(), "one\ntwo"); n != 3 {
		t.Errorf("expected the clipboard inserted whole at each cursor, got %d", n)
	}
}

// A clipboard whose line count matches the cursor count still distributes one
// line per cursor: replication is only the mismatched case. The cursors sit on
// non-adjacent lines, so a distributed paste leaves the lines separated — if
// the clipboard were replicated at each cursor the whole block would appear
// three times instead.
func TestExternalDistributesNotReplicated(t *testing.T) {
	p := NewPane(NewFile("t.go", strings.Repeat("xxxx\n", 6), 2))
	p.Resize(80, 40)
	p.Cursors.Set(4, 0)   // line 0
	p.Cursors.Add(14, 10) // line 2
	p.Cursors.Add(24, 20) // line 4
	p.PasteClip(Clip{Text: "one\ntwo\nthree"})
	body := p.File.Text()
	if n := strings.Count(body, "one\ntwo\nthree"); n != 0 {
		t.Errorf("matching clipboard replicated %d times; it must distribute", n)
	}
	for _, want := range []string{"one", "two", "three"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q appears %d times, want once at its own cursor", want, n)
		}
	}
}

// Pasting a single-line clipboard with several cursors inserts it whole at each
// cursor rather than only at the primary. It is stored once however many
// cursors receive it, and the paste is one undo step.
func TestPasteClipReplicatesAtEachCursor(t *testing.T) {
	const text = "shared text"
	for _, n := range []int{2, 4} {
		p := withCursors(n)
		beforeText := p.File.Text()
		beforePieces := p.File.Pieces()
		beforeStore := p.File.Session().Store().Bytes()
		beforeOps := len(p.File.Session().Journal())

		p.PasteClip(Clip{Text: text})

		if got := strings.Count(p.File.Text(), text); got != n {
			t.Errorf("%d cursors: text appears %d times, want %d", n, got, n)
		}
		if stored := p.File.Session().Store().Bytes() - beforeStore; stored != len(text) {
			t.Errorf("%d cursors: stored %d bytes, want the text once (%d)", n, stored, len(text))
		}
		if ops := len(p.File.Session().Journal()) - beforeOps; ops != n {
			t.Errorf("%d cursors: committed %d ops, want one insert per cursor", n, ops)
		}
		if pieces := p.File.Pieces() - beforePieces; pieces > 2*n {
			t.Errorf("%d cursors: paste added %d pieces, want O(cursors)", n, pieces)
		}
		assertOneUndoStep(t, p, beforeOps, beforeText)
	}
}

// The same fallback handles a multi-line clipboard: the whole block lands at
// every cursor, and one undo takes it all back.
func TestPasteClipReplicatesMultilineAtEachCursor(t *testing.T) {
	const text = "one\ntwo\nthree"
	p := withCursors(4) // three lines and four cursors: the line count mismatches
	beforeText := p.File.Text()
	beforePieces := p.File.Pieces()
	beforeStore := p.File.Session().Store().Bytes()
	beforeOps := len(p.File.Session().Journal())

	p.PasteClip(Clip{Text: text})

	if got := strings.Count(p.File.Text(), text); got != 4 {
		t.Errorf("block appears %d times, want 4", got)
	}
	if stored := p.File.Session().Store().Bytes() - beforeStore; stored != len(text) {
		t.Errorf("stored %d bytes, want the text once (%d)", stored, len(text))
	}
	if ops := len(p.File.Session().Journal()) - beforeOps; ops != 4 {
		t.Errorf("committed %d ops, want one insert per cursor", ops)
	}
	if pieces := p.File.Pieces() - beforePieces; pieces > 2*4 {
		t.Errorf("paste added %d pieces, want O(cursors)", pieces)
	}
	assertOneUndoStep(t, p, beforeOps, beforeText)
}

// Every cursor's selection is replaced, not just the primary's.
func TestPasteClipReplacesEachSelection(t *testing.T) {
	const text = "REPLACED"
	p := pasteFixture(4, 4)
	beforeText := p.File.Text()
	beforeStore := p.File.Session().Store().Bytes()
	beforeOps := len(p.File.Session().Journal())

	p.PasteClip(Clip{Text: text})

	if got := strings.Count(p.File.Text(), text); got != 4 {
		t.Errorf("replacement appears %d times, want 4", got)
	}
	if got := strings.Count(p.File.Text(), "xxxx"); got != 4 {
		t.Errorf("%d unselected regions remain, want 4", got)
	}
	if stored := p.File.Session().Store().Bytes() - beforeStore; stored != len(text) {
		t.Errorf("stored %d bytes, want the text once (%d)", stored, len(text))
	}
	assertOneUndoStep(t, p, beforeOps, beforeText)
}

// assertOneUndoStep checks that the ops an action committed all share one undo
// group, and that a single undo restores the document. Ops are not undo steps:
// a group of ops reverses together, which is the property that matters.
func assertOneUndoStep(t *testing.T, p *Pane, beforeOps int, beforeText string) {
	t.Helper()
	ops := p.File.Session().Journal()
	if len(ops) <= beforeOps {
		t.Fatalf("action committed no ops")
	}
	group := ops[beforeOps].Group
	for _, o := range ops[beforeOps:] {
		if o.Group != group {
			t.Errorf("ops span groups %d and %d; want one undo step", group, o.Group)
			return
		}
	}
	p.history(p.File.Undo(p.Author))
	if got := p.File.Text(); got != beforeText {
		t.Errorf("one undo left the document changed")
	}
}
