package editor

import (
	"testing"

	"raj/internal/syntax"
	"raj/internal/ui"
)

// A nil set is the no-tokens fast path: reading it must not panic and must
// report nothing, because the renderer reads every line's overlay without a nil
// check of its own.
func TestSemanticSetNilIsSafe(t *testing.T) {
	var s *SemanticSet
	if got := s.At(0); got != nil {
		t.Errorf("nil set At(0) = %+v, want nil", got)
	}
	if got := NewSemanticSet(nil); got != nil {
		t.Errorf("NewSemanticSet(nil) = %+v, want nil", got)
	}
	if got := NewSemanticSet(map[int][]syntax.Span{}); got != nil {
		t.Errorf("NewSemanticSet(empty) = %+v, want nil", got)
	}
}

// The overlay wins where it has a span and leaves every other byte to the base
// highlighter. Without the composition the semantic answer would decode and
// never reach a cell, which is the whole feature: the file would keep only the
// chroma colour.
func TestSemanticOverlayPaintsOverChroma(t *testing.T) {
	p := newTestPane("hello world\n")
	style := ui.DefaultStyle.With(ui.Ansi(13)).Plus(ui.Bold)
	p.File.SetSemantic(NewSemanticSet(map[int][]syntax.Span{
		0: {{Start: 6, End: 11, Style: style}},
	}))

	gut := p.GutterWidth()
	s := screenOf(p, 40, 10)
	if c := s.At(gut+6, 0); c.Rune != 'w' || c.Style != style {
		t.Errorf("byte 6 = %q %+v, want the overlay style %+v", c.Rune, c.Style, style)
	}
	if c := s.At(gut+0, 0); c.Style == style {
		t.Error("the overlay painted past its start")
	}
	if c := s.At(gut+11, 0); c.Style == style {
		t.Error("the overlay painted past its end")
	}
}

// Clearing the overlay is what an edit does through the application, and the
// file must forget it rather than paint stale colours on moved bytes.
func TestSemanticOverlayClears(t *testing.T) {
	p := newTestPane("hello world\n")
	style := ui.DefaultStyle.With(ui.Ansi(13)).Plus(ui.Bold)
	p.File.SetSemantic(NewSemanticSet(map[int][]syntax.Span{
		0: {{Start: 0, End: 5, Style: style}},
	}))
	if p.File.Semantic == nil {
		t.Fatal("setup: the overlay was not set")
	}
	p.File.ClearSemantic()
	if p.File.Semantic != nil {
		t.Error("ClearSemantic left the set installed")
	}
	s := screenOf(p, 40, 10)
	if c := s.At(p.GutterWidth(), 0); c.Style == style {
		t.Error("a cleared overlay still painted")
	}
}
