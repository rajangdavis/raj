package syntax

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// spanText is the text each cached span claims to be describing. It is the
// thing that has to stay put across an edit: a span whose colour is a version
// out of date is invisible, and a span covering the wrong characters is the
// bug this file exists to prevent.
func spanTexts(h *Highlighter, text string) [][]string {
	lines := strings.Split(text, "\n")
	out := make([][]string, 0, len(lines))
	for n, line := range lines {
		var row []string
		for _, s := range h.Line(n) {
			if s.Start < 0 || s.End > len(line) || s.Start > s.End {
				row = append(row, "<out of range>")
				continue
			}
			row = append(row, line[s.Start:s.End])
		}
		out = append(out, row)
	}
	return out
}

// check asserts the cache is structurally sound against the given text.
func check(t *testing.T, h *Highlighter, text string) {
	t.Helper()
	want := lineStarts(text)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !reflect.DeepEqual(h.starts, want) {
		t.Fatalf("line starts %v, want %v", h.starts, want)
	}
	if len(h.lines) != len(h.starts) {
		t.Fatalf("%d cached lines for %d line starts", len(h.lines), len(h.starts))
	}
	lines := strings.Split(text, "\n")
	for n, spans := range h.lines {
		prev := 0
		for _, s := range spans {
			if s.Start < prev || s.End < s.Start || s.End > len(lines[n]) {
				t.Fatalf("line %d span %v is not inside %q", n, s, lines[n])
			}
			prev = s.End
		}
	}
}

// insert applies an insertion to both the text and the cache, the way File does
// from an op.
func insert(h *Highlighter, text string, pos int, s string, ver uint64) string {
	var nl []int
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			nl = append(nl, pos+i+1)
		}
	}
	h.Edit(pos, 0, len(s), nl, ver)
	return text[:pos] + s + text[pos:]
}

func remove(h *Highlighter, text string, pos, n int, ver uint64) string {
	h.Edit(pos, n, 0, nil, ver)
	return text[:pos] + text[pos+n:]
}

// The reported symptom: indent a line and the colours stay a tab to the left
// until something else forces a retokenise, then out-dent and they are a tab to
// the right. Spliced spans move with the text instead.
func TestIndentKeepsSpansOnTheirText(t *testing.T) {
	src := "func f() {\nx := \"hi\"\n}\n"
	h := New("x.go", true)
	wait(t, h, src, 1)
	before := spanTexts(h, src)

	pos := lineStarts(src)[1]
	text := insert(h, src, pos, "\t", 2)
	check(t, h, text)
	if got := spanTexts(h, text); !reflect.DeepEqual(got[1], before[1]) {
		t.Errorf("after indent, line 1 spans cover %q, want %q", got[1], before[1])
	}

	text = remove(h, text, pos, 1, 3)
	check(t, h, text)
	if got := spanTexts(h, text); !reflect.DeepEqual(got, before) {
		t.Errorf("after out-dent, spans cover %q, want %q", got, before)
	}
	if text != src {
		t.Fatalf("model text drifted: %q", text)
	}
}

// Commenting a line out and back in is the same shape of edit at the start of a
// line, and used to leave the rest of the line coloured off by two.
func TestCommentToggleKeepsSpansOnTheirText(t *testing.T) {
	src := "package p\n\nvar x = 1\n"
	h := New("x.go", true)
	wait(t, h, src, 1)
	before := spanTexts(h, src)

	pos := lineStarts(src)[2]
	text := insert(h, src, pos, "// ", 2)
	check(t, h, text)
	if got := spanTexts(h, text); !reflect.DeepEqual(got[2], before[2]) {
		t.Errorf("after commenting, line 2 spans cover %q, want %q", got[2], before[2])
	}

	text = remove(h, text, pos, 3, 3)
	check(t, h, text)
	if got := spanTexts(h, text); !reflect.DeepEqual(got, before) {
		t.Errorf("after uncommenting, spans cover %q, want %q", got, before)
	}
}

// Splitting a line moves the text after the caret to a new line, and its spans
// have to go with it.
func TestNewlineSplitsSpansOntoTheNewLine(t *testing.T) {
	src := "var x = 1; var y = 2\n"
	h := New("x.go", true)
	wait(t, h, src, 1)

	pos := strings.Index(src, "var y")
	text := insert(h, src, pos, "\n", 2)
	check(t, h, text)
	got := spanTexts(h, text)
	if len(got) < 2 || len(got[1]) == 0 || got[1][0] != "var" {
		t.Errorf("line 1 spans %q; the split half should keep its tokens", got[1])
	}
}

// Joining lines with backspace at column zero is the same edit in reverse.
func TestJoinLinesKeepsSpans(t *testing.T) {
	src := "var x = 1\nvar y = 2\n"
	h := New("x.go", true)
	wait(t, h, src, 1)

	pos := lineStarts(src)[1] - 1 // the newline itself
	text := remove(h, src, pos, 1, 2)
	check(t, h, text)
	got := spanTexts(h, text)
	if len(got[0]) == 0 || !strings.Contains(strings.Join(got[0], ""), "var y") {
		t.Errorf("line 0 spans %q; the joined half's tokens should have come along", got[0])
	}
}

// An edit that lands mid-pass must be replayed onto the result, or the pass
// installs spans describing text that is already gone.
func TestEditDuringPassIsReplayed(t *testing.T) {
	h := New("x.go", true)
	big := strings.Repeat("func f() { x := 1 }\n", 500)
	h.Ensure(big, 1) // slow enough to still be running below

	text := insert(h, big, 0, "\t", 2)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		done := h.have && !h.running
		h.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	check(t, h, text)
}

// Random edit sequences must leave the cache structurally consistent with the
// text, whatever order insertions, deletions and line splits arrive in.
func FuzzSplice(f *testing.F) {
	f.Add(int64(1), 40)
	f.Add(int64(99), 7)
	f.Fuzz(func(t *testing.T, seed int64, n int) {
		if n < 0 || n > 200 {
			t.Skip()
		}
		rng := rand.New(rand.NewSource(seed))
		text := "package p\n\nfunc f() {\n\ts := \"hi\"\n\t// note\n}\n"
		h := New("x.go", true)
		wait(t, h, text, 1)

		for i := 0; i < n; i++ {
			ver := uint64(i + 2)
			if len(text) > 0 && rng.Intn(2) == 0 {
				pos := rng.Intn(len(text))
				text = remove(h, text, pos, rng.Intn(len(text)-pos)+1, ver)
			} else {
				pos := rng.Intn(len(text) + 1)
				frag := []string{"\t", "x", "// ", "\n", "\n\n", "a\nb\n", "\"s\""}[rng.Intn(7)]
				text = insert(h, text, pos, frag, ver)
			}
			check(t, h, text)
		}
	})
}
