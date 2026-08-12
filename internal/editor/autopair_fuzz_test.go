package editor

import (
	"strings"
	"testing"
)

// The invariant that makes these conveniences safe to leave on: typing a
// character never loses it and never shortens the document.
//
// Every rule here is a guess about intent, and a guess that eats a keystroke is
// far worse than no guess at all — the first is unpredictable, the second is
// merely unhelpful. Asserted against arbitrary buffers and arbitrary keystrokes
// rather than the cases I thought of.
func FuzzTypingPreservesInput(f *testing.F) {
	f.Add("", "(")
	f.Add("    indented", "\n")
	f.Add("if x {}", "\n")
	f.Add("don", "'")
	f.Add("()", ")")
	f.Add("\"\"", "\"")
	f.Add("a\nb\nc", "{")

	f.Fuzz(func(t *testing.T, text, keys string) {
		if len(text) > 2000 || len(keys) > 40 || !valid(text) || !valid(keys) {
			return
		}
		p := newTestPane(text)
		p.AutoPairs = true
		p.DocEnd(false)

		for _, r := range keys {
			ch := string(r)
			before := p.File.Text()
			p.HandleText(ch)
			after := p.File.Text()

			if len(after) < len(before) {
				t.Fatalf("typing %q into %q shrank it: %q -> %q",
					ch, text, before, after)
			}
			// A newline may be absorbed into an indent-carrying insert, but a
			// printable character must survive somewhere in the document.
			if ch != "\n" && !strings.Contains(after, ch) {
				t.Fatalf("typing %q into %q lost it: %q", ch, before, after)
			}
			// The cursor must stay addressable. An off-by-one here is a panic
			// on the next keystroke, not a cosmetic problem.
			if h := p.Cursors.Primary().Head; h < 0 || h > len(after) {
				t.Fatalf("cursor at %d, document is %d bytes", h, len(after))
			}
		}
	})
}

// valid rejects inputs the editor never sees: a buffer is text, and a
// keystroke is a printable rune or a newline.
func valid(s string) bool {
	for _, r := range s {
		if r == 0 || r == '\r' || (r < 32 && r != '\n' && r != '\t') {
			return false
		}
	}
	return true
}
