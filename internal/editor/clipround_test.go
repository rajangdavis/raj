package editor

import (
	"os"
	"path/filepath"
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
			src := build()
			clip := src.Copy()

			// Paste internally into the pane that copied, so the spans splice;
			// a separate pane's store differs and would exercise Text only.
			src.PasteClip(clip)
			spliced := src.File.Slice(0, src.File.Len())

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

// The linewise paste must yield the same document whether it goes through the
// piece path (internal) or appends the text (external), and land the cursor in
// the same place. TestClipRoundTripAllShapes narrowed to the new path,
// covering the captured-newline and NewlinePiece cases.
func TestLinewisePasteInternalMatchesExternal(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		from int // caret when copying the whole line
		to   int // caret when pasting
	}{
		{"after indent", "\tfoo\nbar\n", 1, 1},
		{"end of line", "\tfoo\nbar\n", 4, 4},
		{"column zero", "\tfoo\nbar\n", 0, 0},
		{"filled empty line", "one\n\nthree\n", 0, 4},
		{"final empty line", "one\n", 0, 4},
		{"final line without newline", "one\nlast", 0, 8},
		{"final source into empty line", "x\n\nlast", 6, 2},
		{"final source pasted below", "one\nlast", 4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func() *Pane {
				p := NewPane(NewFile("t.go", tc.body, 2))
				p.Resize(80, 40)
				p.Cursors.Set(tc.from, tc.from)
				return p
			}
			src := build()
			clip := src.Copy()
			src.Cursors.Set(tc.to, tc.to)
			src.PasteClip(clip)
			spliced := src.File.Text()

			ext := build()
			ext.Cursors.Set(tc.to, tc.to)
			ext.PasteClip(Clip{Text: clip.Text})
			external := ext.File.Text()

			if spliced != external {
				t.Errorf("internal %q\nexternal %q", spliced, external)
			}
			if spliced == tc.body {
				t.Errorf("paste changed nothing: %q", spliced)
			}
			if a, b := src.Cursors.Primary().Head, ext.Cursors.Primary().Head; a != b {
				t.Errorf("cursor internal %d, external %d", a, b)
			}
		})
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
		// Paste again below line 0: both the filled and the pasted-below
		// branch must reuse the captured newline, appending nothing.
		p.Cursors.Set(0, 0)
		p.PasteClip(clip)
		if got := p.File.Session().Store().Bytes() - before; got != 0 {
			t.Errorf("size %d: stored %d bytes, want 0", size, got)
		}
	}
}

// A clip's spans are offsets into the store of the file that copied it. Pasted
// into a different file they name unrelated bytes, or none at all, so PasteClip
// must fall back to the text. This is the cross-buffer path: copy in one file,
// switch tabs, paste in another.
func TestClipDoesNotCrossFiles(t *testing.T) {
	src := NewPane(NewFile("a.go", "alpha beta", 2))
	src.Resize(80, 40)
	src.Cursors.Set(0, 5) // select "alpha"
	clip := src.Copy()
	if clip.Text != "alpha" {
		t.Fatalf("copy = %q, want alpha", clip.Text)
	}

	dst := NewPane(NewFile("b.go", "XY", 2))
	dst.Resize(80, 40)
	dst.PasteClip(clip)

	ext := NewPane(NewFile("b.go", "XY", 2))
	ext.Resize(80, 40)
	ext.PasteClip(Clip{Text: clip.Text})

	if got, want := dst.File.Text(), ext.File.Text(); got != want {
		t.Errorf("cross-file paste = %q, want %q (the text paste)", got, want)
	}
	if got := dst.File.Text(); got != "alphaXY" {
		t.Errorf("cross-file paste = %q, want alphaXY", got)
	}
}

// A reload replaces the file's session and store in place, so a clip captured
// before it names bytes that no longer exist even though the File is the same.
// It too must go through Text.
func TestClipDoesNotCrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPane(f)
	p.Resize(80, 40)
	p.Cursors.Set(0, 5) // select "alpha"
	clip := p.Copy()

	if err := os.WriteFile(path, []byte("replaced entirely"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	p.PasteClip(clip)
	if got := p.File.Text(); !strings.Contains(got, "alpha") {
		t.Errorf("paste after reload = %q, want the copied text inserted", got)
	}
}
