package editor

import (
	"strings"

	"raj/internal/syntax"
)

// Auto-indent that reads the line rather than copying it.
//
// Carrying the previous line's whitespace gets most of the way and stops
// exactly where it starts to matter: `if x {` with no closer yet is the moment
// you want a level added, and `}` on a fresh line is the moment you want one
// taken away. Both need to know what the line MEANS, not what it starts with.
//
// What makes this tractable rather than a parser is the token class the
// highlighter already computes. A brace in a string or a comment must not count
// — `s := "{"` would otherwise add a level to everything after it — and that is
// the same question bracket matching asks, answered the same way. Where the
// lexer has nothing to say the brackets all count, which is the behaviour this
// would have had anyway.

// openDepth is the net number of brackets opened on a line before off, ignoring
// any inside a string or a comment.
//
// Net, so `func f() {` counts one rather than two: the parentheses open and
// close on the line and leave nothing outstanding. Anything above zero adds a
// single level, because a line ending `{{` is still one block to the eye and
// two would put the next line somewhere nobody expects.
func (p *Pane) openDepth(off int) int {
	line := p.File.LineOf(off)
	start := p.File.LineStart(line)
	text := p.File.Line(line)
	end := off - start
	if end > len(text) {
		end = len(text)
	}
	var spans []syntax.Span
	if p.File.Syntax.Ready() {
		spans = p.File.Syntax.Line(line)
	}

	depth := 0
	for i := 0; i < end; i++ {
		c := text[i]
		_, opens := pairs[c]
		_, shuts := closers[c]
		if !opens && !shuts {
			continue
		}
		if syntax.ClassAt(spans, i) != syntax.ClassCode {
			continue
		}
		if opens {
			depth++
		} else if depth > 0 {
			// Floored at zero. A line that closes more than it opens — the `}`
			// ending a block — is not asking for the next line to be outdented
			// twice; it has already been outdented by the closer rule below.
			depth--
		}
	}
	return depth
}

// reindentCloser lines a just-typed closing bracket up with whatever opened it.
//
// Only when the closer is the first thing on its line. Typing `}` at the end of
// `if x { }` is part of an expression and moving the line would be rearranging
// code that was not asked about; typing it on a line of its own is unambiguously
// "close this block", and lining it up is the whole point.
//
// The partner's own indent is copied rather than a level being subtracted from
// the current one. Subtracting assumes the file is indented consistently and in
// the units raj thinks it is; copying is right whatever the file does, including
// files that mix tabs and spaces or indent by amounts nobody would choose.
func (p *Pane) reindentCloser() {
	if len(p.Cursors.All()) != 1 {
		// With several cursors the closers land on several lines and each would
		// want a different indent. Doing it for one and not the others is worse
		// than doing it for none.
		return
	}
	head := p.Cursors.Primary().Head
	at := head - 1 // the byte just typed
	if at < 0 {
		return
	}
	if _, ok := closers[p.byteAt(at)]; !ok {
		return
	}
	line := p.File.LineOf(at)
	start := p.File.LineStart(line)
	text := p.File.Line(line)
	before := text[:at-start]
	if strings.TrimSpace(before) != "" {
		return // not the first thing on the line
	}

	partner, ok := p.scanFrom(at, p.byteAt(at))
	if !ok {
		return // nothing to line up with
	}
	want := p.lineIndent(partner)
	if want == before {
		return
	}
	// Replace the leading whitespace only. The cursor is put back at the end of
	// what it was on, so the caret stays after the bracket it just typed rather
	// than jumping to wherever the byte count landed.
	p.File.Begin()
	p.applyEdit(start, len(before), want)
	p.File.End()
	moved := start + len(want) + (head - start - len(before))
	p.Cursors.Set(moved, moved)
	p.FollowCursor()
}
