package syntax

import (
	"strings"
	"testing"
	"time"

	"raj/internal/ui"
)

// wait drives the highlighter to completion, the way the app's idle tick does.
func wait(t *testing.T, h *Highlighter, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Ensure(text)
		if h.Ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("highlighter never produced tokens")
}

func TestGoHighlighting(t *testing.T) {
	src := "package main\n\n// comment\nfunc main() {\n\ts := \"hi\"\n}\n"
	h := New("main.go", true)
	if !h.Enabled() {
		t.Fatal("go files should be highlighted")
	}
	h.Invalidate()
	wait(t, h, src)

	spans := h.Line(0)
	if len(spans) == 0 {
		t.Fatal("no spans on the package line")
	}
	kw, _ := StyleAt(spans, 0) // "package"
	id, _ := StyleAt(spans, 8) // "main"
	if kw == id {
		t.Errorf("keyword and identifier share a style %+v", kw)
	}
}

func TestUnknownLanguageDisabled(t *testing.T) {
	if New("notes.zzz", true).Enabled() {
		t.Error("unknown extensions should not be highlighted")
	}
}

// Tokens spanning newlines must be split, or a block comment swallows every
// line after it.
func TestMultilineTokenSplits(t *testing.T) {
	h := New("x.go", true)
	src := "/* one\ntwo\nthree */\nvar x = 1\n"
	h.Invalidate()
	wait(t, h, src)
	if s := h.Line(3); len(s) == 0 {
		t.Fatal("no spans on the line after a block comment")
	}
	if got := len(h.lines); got != 5 {
		t.Errorf("cached %d lines, want 5", got)
	}
}

// Line must never block or tokenise: it is called once per visible line, every
// frame, and chroma costs tens of milliseconds.
func TestLineIsNonBlockingBeforeReady(t *testing.T) {
	h := New("x.go", true)
	h.Invalidate()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.Line(i)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("1000 Line calls took %v; it must not tokenise", d)
	}
}

// An edit arriving mid-pass must not be lost: the cache has to converge on the
// latest text rather than stopping one edit behind.
func TestConvergesAfterEditDuringPass(t *testing.T) {
	h := New("x.go", true)
	big := strings.Repeat("func f() { x := 1; _ = x }\n", 400)
	h.Invalidate()
	h.Ensure(big)
	h.Invalidate()
	h.Ensure(big + "var final = 1\n")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.Ensure(big + "var final = 1\n")
		if len(h.linesLen()) > 0 && h.linesLen()[0] == 401 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("never converged on the latest text")
}

// linesLen reports the cached line count under the lock, for tests.
func (h *Highlighter) linesLen() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lines == nil {
		return nil
	}
	return []int{len(h.lines)}
}

// Colours come from the terminal's 16-colour palette, never from named RGB, so
// the user's Ghostty theme decides what the code looks like.
func TestUsesTerminalPalette(t *testing.T) {
	src := "package main\n\n// c\nfunc f() { s := \"x\" }\n"
	h := New("main.go", true)
	wait(t, h, src)
	for n := range h.lines {
		for _, sp := range h.Line(n) {
			for _, c := range []ui.Color{sp.Style.Fg, sp.Style.Bg} {
				if _, _, _, isRGB := c.RGB(); isRGB {
					t.Fatalf("line %d uses a direct colour; it must use the palette", n)
				}
			}
			if sp.Style.Bg != ui.Default {
				t.Errorf("line %d sets a background; that belongs to the terminal", n)
			}
		}
	}
}

// A new highlighter is stale, so the first idle tick tokenises even if the file
// is never edited.
func TestNewStartsStale(t *testing.T) {
	h := New("x.go", true)
	h.mu.Lock()
	stale := h.stale
	h.mu.Unlock()
	if !stale {
		t.Error("a fresh highlighter must be stale so it tokenises on open")
	}
}
