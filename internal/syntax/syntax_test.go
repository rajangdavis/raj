package syntax

import (
	"strings"
	"testing"
	"time"

	"raj/internal/ui"
)

// wait drives the highlighter to completion the way the app's tick does, and
// returns once the lexer has seen exactly this version.
func wait(t *testing.T, h *Highlighter, text string, ver uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.Ensure(text, ver)
		h.mu.Lock()
		done := h.have && h.lexVer == ver && !h.running
		h.mu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("highlighter never caught up with the current version")
}

// covered is the last byte a span reaches on a line, which is a cheap way to
// ask which text the cache is describing.
func covered(h *Highlighter, n int) int {
	end := 0
	for _, s := range h.Line(n) {
		if s.End > end {
			end = s.End
		}
	}
	return end
}

func TestGoHighlighting(t *testing.T) {
	src := "package main\n\n// comment\nfunc main() {\n\ts := \"hi\"\n}\n"
	h := New("main.go", true)
	if !h.Enabled() {
		t.Fatal("go files should be highlighted")
	}
	wait(t, h, src, 1)

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
	wait(t, h, src, 1)
	if s := h.Line(3); len(s) == 0 {
		t.Fatal("no spans on the line after a block comment")
	}
	if got := len(h.lines); got != 5 {
		t.Errorf("cached %d lines, want 5", got)
	}
}

// The cache must hold exactly as many lines as the text has, whatever the lexer
// emitted. Chroma appends a newline for some languages, and a cache one line
// long would put every later splice on the wrong line.
func TestLineCountMatchesText(t *testing.T) {
	for _, src := range []string{"a := 1", "a := 1\n", "a := 1\n\n", ""} {
		h := New("x.go", true)
		wait(t, h, src, 1)
		want := strings.Count(src, "\n") + 1
		if got := len(h.lines); got != want {
			t.Errorf("%q: cached %d lines, want %d", src, got, want)
		}
		if got := len(h.starts); got != want {
			t.Errorf("%q: cached %d starts, want %d", src, got, want)
		}
	}
}

// Line must never block or tokenise: it is called once per visible line, every
// frame, and chroma costs tens of milliseconds.
func TestLineIsNonBlockingBeforeReady(t *testing.T) {
	h := New("x.go", true)
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
	h.Ensure(big, 1)
	h.Ensure(big+"var final = 1\n", 2)
	wait(t, h, big+"var final = 1\n", 2)
	if got := len(h.lines); got != 402 {
		t.Errorf("cached %d lines, want 402", got)
	}
}

// Regression: an Ensure with nothing to do used to park its text in the pending
// slot, and the next real edit's pass would then re-tokenise that older text on
// top of its own fresh result — leaving the colours describing pre-edit text
// with nothing scheduled to correct them. It only came right when the user
// happened to press another key.
func TestQuiescentEnsureDoesNotResurrectOldText(t *testing.T) {
	h := New("x.go", true)
	a, b := "var x = 1\n", "var xyzzy = 1\n"
	wait(t, h, a, 1)

	h.Ensure(a, 1) // an idle tick with no edit behind it
	h.Ensure(b, 2) // the edit, and then the user stops typing

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		idle := !h.running
		h.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got, want := covered(h, 0), len(b)-1; got != want {
		t.Errorf("cache describes stale text: spans reach %d, want %d", got, want)
	}
}

// A pass that finishes after a newer one has landed must not overwrite it.
func TestLateePassDoesNotOverwriteNewer(t *testing.T) {
	h := New("x.go", true)
	big := strings.Repeat("func f() { x := 1 }\n", 300)
	h.Ensure(big, 1)
	small := "var x = 1\n"
	h.Ensure(small, 2)
	wait(t, h, small, 2)
	if got := len(h.lines); got != 2 {
		t.Errorf("cached %d lines, want 2: an older pass overwrote a newer one", got)
	}
}

// Colours come from the terminal's 16-colour palette, never from named RGB, so
// the user's Ghostty theme decides what the code looks like.
func TestUsesTerminalPalette(t *testing.T) {
	src := "package main\n\n// c\nfunc f() { s := \"x\" }\n"
	h := New("main.go", true)
	wait(t, h, src, 1)
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

// A highlighter with no cache tokenises on the first Ensure, so a file that is
// opened and never edited still gets colour.
func TestFirstEnsureTokenisesUntouchedFile(t *testing.T) {
	h := New("x.go", true)
	if h.Ready() {
		t.Fatal("a fresh highlighter should have no tokens")
	}
	wait(t, h, "package main\n", 1)
	if !h.Ready() {
		t.Error("the first Ensure must tokenise an unedited file")
	}
}

// Re-Ensuring the same version must not start another pass; the lexer is the
// expensive thing here and the tick calls this several times a second.
func TestEnsureIsIdempotentPerVersion(t *testing.T) {
	h := New("x.go", true)
	src := "var x = 1\n"
	wait(t, h, src, 1)
	for i := 0; i < 20; i++ {
		h.Ensure(src, 1)
	}
	h.mu.Lock()
	running := h.running
	h.mu.Unlock()
	if running {
		t.Error("Ensure re-tokenised text the lexer had already seen")
	}
}
