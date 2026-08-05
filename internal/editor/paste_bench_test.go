package editor

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"raj/internal/piecetable"
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

// A paste must not cost a copy of itself just to find its newlines. The number
// that matters here is B/op: it was the size of the pasted text.
func BenchmarkPasteIndexUpdate(b *testing.B) {
	line := strings.Repeat("x", 79) + "\n"
	for _, lines := range []int{100, 10000} {
		text := strings.Repeat(line, lines)
		b.Run(strconv.Itoa(lines)+"-lines", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p := newTestPane("")
				b.StartTimer()
				p.Paste(text)
			}
		})
	}
}

// The index the fast path builds must match a rebuild from the text, or the
// saving is bought with wrong line numbers.
func TestIndexAfterPasteMatchesRebuild(t *testing.T) {
	for _, text := range []string{
		"no newlines",
		"a\nb\nc\n",
		"\n\n\n",
		"trailing\n",
		"\nleading",
		strings.Repeat("line\n", 500),
	} {
		p := newTestPane("head\ntail")
		p.Cursors.Set(5, 5) // between the two lines
		p.Paste(text)

		got := p.File.Lines()
		want := len(strings.Split(p.File.Text(), "\n"))
		if got != want {
			t.Errorf("%q: index has %d lines, text has %d", text, got, want)
		}
		for line := 0; line < got; line++ {
			start := p.File.LineStart(line)
			if l := p.File.LineOf(start); l != line {
				t.Errorf("%q: line %d starts at %d, which reports as line %d", text, line, start, l)
			}
		}
	}
}

// The two ways to find the newlines in an inserted span, side by side. The old
// path built a string of the whole insertion to scan it; the new one scans the
// pieces where they lie. Both must agree, so the test below runs them together.
func benchNewlines(b *testing.B, materialise bool) {
	text := strings.Repeat(strings.Repeat("x", 79)+"\n", 10000)
	p := newTestPane("")
	p.Paste(text)
	n := p.File.Len()
	nl := []byte("\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		if materialise {
			s := p.File.Slice(0, n)
			count = strings.Count(s, "\n")
		} else {
			op, _ := p.File.Session().LastOp()
			store := p.File.Session().Store()
			for _, rec := range op.Ins {
				count += bytes.Count(store.Slice(piecetable.Author(rec.Buf), rec.Start, rec.Length), nl)
			}
		}
		if count != 10000 {
			b.Fatalf("counted %d newlines", count)
		}
	}
}

func BenchmarkNewlinesMaterialise(b *testing.B) { benchNewlines(b, true) }
func BenchmarkNewlinesScan(b *testing.B)        { benchNewlines(b, false) }
