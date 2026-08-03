package editor

import (
	"fmt"
	"strings"
	"testing"
)

// clipboardOf builds a clipboard of n lines, each about a line of source.
func clipboardOf(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("\tresult%d := compute(input%d, options)", i, i)
	}
	return lines
}

// cost is what one paste strategy costs on every axis that matters.
type cost struct {
	pieces, ops, stored int
}

func measure(cursors int, paste func(*Pane, []string)) cost {
	p := withCursors(cursors)
	lines := clipboardOf(cursors)
	before := cost{p.File.Pieces(), len(p.File.Session().Journal()),
		p.File.Session().Store().Bytes()}
	paste(p, lines)
	return cost{
		p.File.Pieces() - before.pieces,
		len(p.File.Session().Journal()) - before.ops,
		p.File.Session().Store().Bytes() - before.stored,
	}
}

// TestPasteStrategyCost compares the three candidates on pieces, ops and stored
// bytes. Time is measured separately; these are the ones that persist.
func TestPasteStrategyCost(t *testing.T) {
	for _, n := range []int{1, 4, 16, 64} {
		clip := strings.Join(clipboardOf(n), "\n")
		block := measure(n, func(p *Pane, l []string) { p.Paste(strings.Join(l, "\n")) })
		dist := measure(n, func(p *Pane, l []string) { p.PasteDistributed(l) })
		perCursor := measure(n, func(p *Pane, l []string) { p.InsertText(strings.Join(l, "\n")) })

		t.Logf("cursors=%2d clip=%dB  block{p:%d o:%d s:%d}  distributed{p:%d o:%d s:%d}  per-cursor{p:%d o:%d s:%d}",
			n, len(clip),
			block.pieces, block.ops, block.stored,
			dist.pieces, dist.ops, dist.stored,
			perCursor.pieces, perCursor.ops, perCursor.stored)

		if n > 1 && dist.stored > len(clip) {
			t.Errorf("cursors=%d: distributing stored %d bytes for a %d-byte clipboard",
				n, dist.stored, len(clip))
		}
		// Ops are not undo steps: a group of ops reverses together, which is
		// the property that matters. Check it directly.
		p := withCursors(n)
		text := p.File.Text()
		p.PasteDistributed(clipboardOf(n))
		if p.File.Text() == text {
			t.Fatalf("cursors=%d: paste did nothing", n)
		}
		p.history(p.File.Undo(p.Author))
		if got := p.File.Text(); got != text {
			t.Errorf("cursors=%d: one undo left %d bytes changed", n, len(got)-len(text))
		}
	}
}

func BenchmarkPasteStrategies(b *testing.B) {
	run := func(b *testing.B, cursors int, paste func(*Pane, []string)) {
		lines := clipboardOf(cursors)
		b.ReportAllocs()
		b.StopTimer()
		for i := 0; i < b.N; i++ {
			p := withCursors(cursors)
			b.StartTimer()
			paste(p, lines)
			b.StopTimer()
		}
	}
	for _, n := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("block/cursors=%d", n), func(b *testing.B) {
			run(b, n, func(p *Pane, l []string) { p.Paste(strings.Join(l, "\n")) })
		})
		b.Run(fmt.Sprintf("distributed/cursors=%d", n), func(b *testing.B) {
			run(b, n, func(p *Pane, l []string) { p.PasteDistributed(l) })
		})
	}
}

// The round-trip is the point: copy from N cursors, paste into N cursors, and
// each selection lands at its own cursor.
func TestCopyPasteRoundTrip(t *testing.T) {
	p := NewPane(NewFile("t.go", "alpha\nbeta\ngamma\n---\nx\nx\nx\n", 2))
	p.Resize(80, 20)
	p.Cursors.Set(5, 0)   // alpha
	p.Cursors.Add(10, 6)  // beta
	p.Cursors.Add(16, 11) // gamma

	clip := p.Copy()
	if clip.Text != "alpha\nbeta\ngamma" {
		t.Fatalf("copy = %q, want all three selections", clip.Text)
	}

	// Select each of the three x lines.
	p.Cursors.Set(22, 21)
	p.Cursors.Add(24, 23)
	p.Cursors.Add(26, 25)
	p.PasteClip(clip)

	if got := p.File.Text(); got != "alpha\nbeta\ngamma\n---\nalpha\nbeta\ngamma\n" {
		t.Errorf("text = %q", got)
	}
}

// A clipboard whose line count does not match falls back to inserting it whole,
// rather than distributing part of it and dropping the rest.
func TestDistributeFallsBackOnMismatch(t *testing.T) {
	p := withCursors(3)
	p.PasteDistributed([]string{"one", "two"})
	if n := strings.Count(p.File.Text(), "one\ntwo"); n != 1 {
		t.Errorf("expected the clipboard inserted whole once, got %d", n)
	}
}
