package widget

import (
	"testing"
	"unicode/utf8"

	"raj/internal/ui"
)

// The column-to-offset mapping is the one place a field's pointer handling can
// be wrong without looking wrong. Byte offsets, display columns and rune
// boundaries are three different coordinate systems, and a mapping that mixes
// them up produces a caret that is subtly misplaced only on lines containing a
// wide or multi-byte rune — which is exactly the input a hand-written test is
// least likely to have.
//
// The invariants below are stated against the same width function the renderer
// draws with, so a caret can only land somewhere the renderer would not have
// drawn that character if the two genuinely disagree.

// cols is the display width of a string, as the renderer measures it.
func cols(s string) (n int) {
	for _, r := range s {
		n += ui.RuneWidth(r)
	}
	return
}

func FuzzPlaceCaret(f *testing.F) {
	f.Add("hello world", 3)
	f.Add("", 0)
	f.Add("héllo", 2)
	f.Add("日本語のテキスト", 5)
	f.Add("a\u00e9\u65e5b", 4)
	f.Add("mixed 日本 text", 9)
	f.Add("emoji 🙂 here", 7)

	f.Fuzz(func(t *testing.T, text string, col int) {
		if !utf8.ValidString(text) {
			t.Skip() // a field never holds invalid UTF-8; the buffer rejects it
		}
		in := &Input{Text: text}
		in.PlaceCaret(col)

		// 1. The caret is always a valid position in the text.
		if in.Cursor < 0 || in.Cursor > len(text) {
			t.Fatalf("caret %d is outside %q", in.Cursor, text)
		}
		// 2. It is always on a rune boundary. A caret inside a multi-byte rune
		//    would split it on the next insert.
		if in.Cursor < len(text) && !utf8.RuneStart(text[in.Cursor]) {
			t.Fatalf("caret %d splits a rune in %q", in.Cursor, text)
		}
		// 3. Placing collapses the selection: a click is not a drag.
		if in.HasSelection() {
			t.Fatalf("placing the caret left a selection in %q", text)
		}
		// 4. The character before the caret ends at or before the column asked
		//    for, and the character after it ends beyond — which is what "the
		//    caret goes before the character under the pointer" means, said in
		//    the renderer's own units.
		if col < 0 {
			return
		}
		if before := cols(text[:in.Cursor]); before > col {
			t.Fatalf("caret %d of %q sits at column %d, past the requested %d",
				in.Cursor, text, before, col)
		}
		if in.Cursor < len(text) {
			_, size := utf8.DecodeRuneInString(text[in.Cursor:])
			if end := cols(text[:in.Cursor+size]); end <= col {
				t.Fatalf("caret %d of %q stops short: the rune ends at column %d, "+
					"still at or before the requested %d", in.Cursor, text, end, col)
			}
		}
	})
}

// A column past the end of the text puts the caret after the last character,
// so clicking the empty space to the right of a short query appends rather
// than doing nothing.
func TestPlaceCaretPastTheEndAppends(t *testing.T) {
	in := &Input{Text: "abc"}
	in.PlaceCaret(99)
	if in.Cursor != 3 {
		t.Errorf("caret at %d, want 3", in.Cursor)
	}
}

// Every column of a rendered field maps back to a caret position, and stepping
// left to right never moves the caret backwards. A mapping that is not monotonic
// is one where dragging across a field would jump about.
func TestPlaceCaretIsMonotonic(t *testing.T) {
	for _, text := range []string{"hello", "日本語", "a日b語c", "🙂ok🙂"} {
		in := &Input{Text: text}
		last := -1
		for col := 0; col <= cols(text)+2; col++ {
			in.PlaceCaret(col)
			if in.Cursor < last {
				t.Errorf("%q: column %d moved the caret back from %d to %d",
					text, col, last, in.Cursor)
			}
			last = in.Cursor
		}
	}
}
