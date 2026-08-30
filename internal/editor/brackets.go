package editor

import (
	"raj/internal/syntax"
)

// Matching brackets.
//
// Two things make this more than a depth counter. A bracket inside a string or
// a comment is not structural — `s := "{"` would otherwise leave every brace
// after it paired one level off — and the lexer already knows which is which,
// so the scan asks it rather than guessing. And an unbalanced file has no
// partner to find, so the scan is bounded: without a limit, one stray `{` at
// the top of a large file makes every keystroke walk to the end of it.
//
// When the lexer has nothing to say — a language it does not know, a file too
// large to highlight, or the first frames before the background pass finishes —
// every bracket counts. That degrades to plain depth counting, which is what
// the feature would have been anyway, rather than to no matching at all.

// How far the search walks in each direction: a line budget and a byte budget,
// because either one alone has a pathological case the other covers.
//
// The bound has to be tight, because the WORST case is the COMMON one. You
// type `{` and it has no partner until you type `}`, so between those two
// keystrokes every frame pays the full unmatched scan. A generous limit is
// therefore not a rare cost paid on malformed files; it is the cost of typing
// a brace.
//
// 500 lines is several times the tallest viewport, so any partner near enough
// for the highlight to be visible at all is inside it, and it holds the
// unmatched scan to around a tenth of a millisecond against roughly one
// microsecond for a partner nearby — see BenchmarkMatchBracketUnbalanced. The
// remaining cost is one piece-table read per line; chunking would cut it
// further and is not worth the line-boundary bookkeeping at this size. The byte budget catches
// the other shape: one minified line longer than the whole line budget, where
// counting lines bounds nothing.
const (
	bracketScanLines = 500
	bracketScanBytes = 256 << 10
)

// The two relations this needs, `pairs` and `closers`, are the ones autopair
// already declares. Shared rather than restated: a bracket raj closes for you
// and a bracket raj highlights for you must be the same set, or typing one
// would produce a pair that does not light up.

// MatchBracket returns the offsets of the bracket the cursor is on and its
// partner, and whether there is a pair to show.
//
// The bracket under the cursor is tried first, then the one just behind it.
// Both are conventional and the order matters: with the caret between `)` and
// `(`, as in `f()|(x)`, preferring the character under it means the pair that
// lights up is the one you are about to type into.
//
// Nothing is reported for a bracket inside a string or a comment. The cursor
// being on one is not a reason to start counting structure that is not there.
func (p *Pane) MatchBracket() (here, partner int, ok bool) {
	if len(p.Cursors.All()) > 1 {
		// Which of four cursors owns the highlight has no good answer, and
		// lighting up four pairs is noise rather than information.
		return 0, 0, false
	}
	c := p.Cursors.Primary()
	if c.HasSelection() {
		return 0, 0, false
	}
	for _, at := range []int{c.Head, c.Head - 1} {
		if at < 0 || at >= p.File.Len() {
			continue
		}
		ch := p.byteAt(at)
		if _, isOpen := pairs[ch]; !isOpen {
			if _, isClose := closers[ch]; !isClose {
				continue
			}
		}
		if p.classAt(at) != syntax.ClassCode {
			continue
		}
		if m, found := p.scanFrom(at, ch); found {
			return at, m, true
		}
		return 0, 0, false // a bracket with no partner: highlight nothing
	}
	return 0, 0, false
}

// scanFrom walks outward from a bracket and returns its partner's offset.
//
// A stack of all three kinds, not a depth count of one. Counting only the
// bracket you started on would pair `(` with `)` in `([)]` — a nesting that
// does not exist — because the unclosed `[` between them is invisible to a
// counter that only knows about parentheses. Reporting no match there is right:
// the text is malformed, and inventing a pair for it says the file nests in a
// way it does not.
//
// The walk is a line at a time rather than a byte at a time. Reading each byte
// through the piece table cost 10 ms for one unmatched brace — a scan that runs
// once per frame and so has a frame's budget, spent entirely on per-byte
// lookups. A line is one lookup, and its tokens are one more, so both costs
// are paid per line instead of per byte. See BenchmarkMatchBracketUnbalanced.
func (p *Pane) scanFrom(at int, ch byte) (int, bool) {
	dir := 1
	if _, isOpen := pairs[ch]; !isOpen {
		dir = -1
	}
	// Read the same way in both directions: going backwards, a closer is what
	// opens a region and an opener is what shuts it. The stack holds the
	// openers still waiting to be closed, innermost last.
	opens, shuts := pairs, closers
	if dir < 0 {
		opens, shuts = closers, pairs
	}
	stack := []byte{ch}

	budget := bracketScanBytes
	line := p.File.LineOf(at)
	last := line + dir*bracketScanLines
	from := at + dir
	for line >= 0 && line < p.File.Lines() && line != last && budget > 0 {
		text := p.File.Line(line)
		start := p.File.LineStart(line)
		// Fetched once per line. Line() on the highlighter is a mutex and a
		// slice index, which is cheap, but not per-bracket cheap.
		var spans []syntax.Span
		if p.File.Syntax.Ready() {
			spans = p.File.Syntax.Line(line)
		}

		lo, hi := 0, len(text)
		if line == p.File.LineOf(at) {
			// The first line starts beside the bracket rather than at its end.
			if dir > 0 {
				lo = from - start
			} else {
				hi = from - start + 1
			}
		}
		if lo < 0 {
			lo = 0
		}
		if hi > len(text) {
			hi = len(text)
		}
		// Clamped within the line, not just between lines: one line can be
		// longer than the whole budget, and deducting it afterwards would
		// scan it in full before noticing.
		if hi-lo > budget {
			if dir > 0 {
				hi = lo + budget
			} else {
				lo = hi - budget
			}
		}
		budget -= hi - lo

		i, stop := lo, hi
		if dir < 0 {
			i, stop = hi-1, lo-1
		}
		for ; i != stop; i += dir {
			c := text[i]
			_, isOpen := opens[c]
			_, isShut := shuts[c]
			if !isOpen && !isShut {
				continue
			}
			// Classification is checked only for characters that would count,
			// so its cost is paid per bracket rather than per byte.
			if syntax.ClassAt(spans, i) != syntax.ClassCode {
				continue
			}
			if isOpen {
				stack = append(stack, c)
				continue
			}
			if opens[stack[len(stack)-1]] != c {
				return 0, false // a crossed pair: the text is malformed
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return start + i, true
			}
		}
		line += dir
	}
	return 0, false
}

// Bytes rather than runes throughout: every bracket is ASCII, and a multi-byte
// rune cannot contain an ASCII byte in UTF-8, so there is nothing to
// mis-identify by scanning bytes and a great deal of decoding to avoid. byteAt
// is autopair's.

// classAt is what the lexer called the token covering an offset.
func (p *Pane) classAt(off int) syntax.Class {
	if !p.File.Syntax.Ready() {
		return syntax.ClassCode // nothing tokenised yet; count everything
	}
	line := p.File.LineOf(off)
	return syntax.ClassAt(p.File.Syntax.Line(line), off-p.File.LineStart(line))
}

// bracketMarks is the pair to underline this frame, as a set of offsets.
//
// A map of at most two entries rather than a pair of ints because drawLine
// already tests membership that way for cursors, and a lookup that reads the
// same as its neighbour is one less thing to get backwards.
func (p *Pane) bracketMarks() map[int]bool {
	here, partner, ok := p.MatchBracket()
	if !ok {
		return nil
	}
	return map[int]bool{here: true, partner: true}
}
