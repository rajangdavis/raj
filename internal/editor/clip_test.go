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

// A mismatched external clipboard goes in whole at the primary cursor rather
// than distributing part of it and dropping the rest.
func TestExternalWholeOnMismatch(t *testing.T) {
	p := pasteFixture(3, 4)
	p.PasteClip(Clip{Text: "one\ntwo"})
	if n := strings.Count(p.File.Text(), "one\ntwo"); n != 1 {
		t.Errorf("expected the clipboard inserted whole once, got %d", n)
	}
}
