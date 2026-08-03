package editor

import (
	"fmt"
	"strings"
	"testing"
)

// pasteText is a realistic paste: a screenful of source.
var pasteText = strings.Repeat("\tif err != nil {\n\t\treturn fmt.Errorf(\"x: %w\", err)\n\t}\n", 20)

func withCursors(n int) *Pane {
	p := NewPane(NewFile("bench.go", strings.Repeat("target\n", 200), 2))
	p.Resize(80, 40)
	p.Cursors.Set(0, 0)
	for i := 1; i < n; i++ {
		p.AddCursorVertical(1)
	}
	return p
}

// BenchmarkPaste measures the dedicated path against what it replaced: running
// the same text through the per-cursor edit machinery.
func BenchmarkPaste(b *testing.B) {
	// Both variants exclude setup, so the numbers compare the edit itself
	// rather than the cost of building a pane.
	run := func(b *testing.B, cursors int, paste func(*Pane)) {
		b.ReportAllocs()
		b.StopTimer()
		for i := 0; i < b.N; i++ {
			p := withCursors(cursors)
			b.StartTimer()
			paste(p)
			b.StopTimer()
		}
	}
	for _, cursors := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("block/cursors=%d", cursors), func(b *testing.B) {
			run(b, cursors, func(p *Pane) { p.Paste(pasteText) })
		})
		b.Run(fmt.Sprintf("percursor/cursors=%d", cursors), func(b *testing.B) {
			run(b, cursors, func(p *Pane) { p.InsertText(pasteText) })
		})
	}
}

// TestPasteCost pins the properties the dedicated path exists for: a paste is
// one op and a bounded number of pieces however many cursors are active, and it
// stores the text exactly once.
func TestPasteCost(t *testing.T) {
	for _, cursors := range []int{1, 4, 16} {
		block := withCursors(cursors)
		beforePieces, beforeOps := block.File.Pieces(), len(block.File.Session().Journal())
		beforeStore := block.File.Session().Store().Bytes()
		block.Paste(pasteText)

		pieces := block.File.Pieces() - beforePieces
		ops := len(block.File.Session().Journal()) - beforeOps
		stored := block.File.Session().Store().Bytes() - beforeStore

		if pieces > 3 {
			t.Errorf("%d cursors: paste added %d pieces, want at most 3", cursors, pieces)
		}
		if ops != 1 {
			t.Errorf("%d cursors: paste committed %d ops, want 1", cursors, ops)
		}
		if stored != len(pasteText) {
			t.Errorf("%d cursors: stored %d bytes for a %d-byte paste; it must be copied once",
				cursors, stored, len(pasteText))
		}

		// The path it replaced scales with cursor count on every axis.
		perCursor := withCursors(cursors)
		bp, bo := perCursor.File.Pieces(), len(perCursor.File.Session().Journal())
		bs := perCursor.File.Session().Store().Bytes()
		perCursor.InsertText(pasteText)
		t.Logf("cursors=%2d  block: %d pieces %d ops %dB   per-cursor: %d pieces %d ops %dB",
			cursors, pieces, ops, stored,
			perCursor.File.Pieces()-bp, len(perCursor.File.Session().Journal())-bo,
			perCursor.File.Session().Store().Bytes()-bs)
	}
}

// A paste must not degrade reads: piece count is what Spans and rendering walk.
func BenchmarkSpansAfterPastes(b *testing.B) {
	p := withCursors(1)
	for i := 0; i < 200; i++ {
		p.Paste(pasteText)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.File.Spans(0, 4000)
	}
}
