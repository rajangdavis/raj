package editor

import (
	"strings"
	"testing"
)

// The invariant that was broken: pasting an internal clip must produce the same
// document as pasting the system-clipboard text it was published with.
//
// Copy publishes two representations of one selection — Text for the world,
// Spans for splicing back into this buffer. If they describe different bytes,
// cmd+c then cmd+v depends on which path the paste happens to take, and a
// whole-line copy lost its trailing newline through the internal path only.
//
// Six shapes, because the bug lived in exactly one of them: byte counts
// legitimately differ once several cursors are joined with synthetic newlines,
// so equal lengths is the wrong test and round-trip equivalence is the right one.
func TestClipRoundTripAllShapes(t *testing.T) {
	const body = "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	for _, cursors := range []int{1, 2, 4} {
		for _, sel := range []bool{false, true} {
			build := func() *Pane {
				p := NewPane(NewFile("t.go", body, 2))
				p.Resize(80, 40)
				if sel {
					p.Cursors.Set(5, 0)
				} else {
					p.Cursors.Set(0, 0)
				}
				for i := 1; i < cursors; i++ {
					at := i * 6
					if sel {
						p.Cursors.Add(at+4, at)
					} else {
						p.Cursors.Add(at, at)
					}
				}
				return p
			}
			clip := build().Copy()

			dst := build()
			dst.PasteClip(clip)
			spliced := dst.File.Slice(0, dst.File.Len())

			ext := build()
			ext.PasteClip(Clip{Text: clip.Text})
			external := ext.File.Slice(0, ext.File.Len())

			if spliced != external {
				t.Errorf("cursors=%d sel=%v:\n  internal %q\n  external %q",
					cursors, sel, spliced, external)
			}
		}
	}
}

// A whole-line copy pastes the line back whole, newline included.
func TestWholeLineCopyKeepsItsNewline(t *testing.T) {
	p := NewPane(NewFile("t.go", "alpha\nbeta\ngamma\n", 2))
	p.Resize(80, 40)
	p.Cursors.Set(0, 0) // caret on line 1, nothing selected
	clip := p.Copy()

	p.Cursors.Set(p.File.Len(), p.File.Len())
	p.PasteClip(clip)
	if got := p.File.Slice(0, p.File.Len()); got != "alpha\nbeta\ngamma\nalpha\n" {
		t.Errorf("got %q, want the line pasted whole", got)
	}
}

// Splicing an internal clip appends nothing to the store, at any size. Widening
// the captured span to cover the newline must not cost that.
func TestWholeLineCopyStillStoresNothing(t *testing.T) {
	for _, size := range []int{100, 10000, 1 << 20} {
		p := NewPane(NewFile("t.go", strings.Repeat("a", size)+"\n", 2))
		p.Resize(80, 40)
		p.Cursors.Set(0, 0)
		clip := p.Copy()
		before := p.File.Session().Store().Bytes()
		p.Cursors.Set(p.File.Len(), p.File.Len())
		p.PasteClip(clip)
		if got := p.File.Session().Store().Bytes() - before; got != 0 {
			t.Errorf("size %d: stored %d bytes, want 0", size, got)
		}
	}
}
