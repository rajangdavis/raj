package editor

import (
	"path/filepath"
	"strings"
)

// What one press of Tab inserts.
//
// This used to be `strings.Repeat(" ", Cols.Tab)` in the two places that indent,
// which meant a tab-indented file grew space indentation the moment it was
// edited — the bytes went to disk exactly as typed, so nothing downstream could
// have fixed it.
//
// The style is DETECTED from the file rather than configured, for the same
// reason reindentCloser copies its partner's indentation rather than computing
// one: the file already knows the answer, and a global setting is wrong on every
// file that disagrees with it. A Makefile needs tabs, a Go file uses them, and a
// JSON file two directories over does not — no single flag is right for all
// three, and being wrong means writing whitespace someone's linter rejects.
//
// The configured default applies only where there is nothing to detect: a new
// file, or one with no indentation yet.
//
// Detection alone is not enough, though, and a Makefile is why. Its tab is not
// a style, it is syntax — a recipe line indented with spaces is "missing
// separator. Stop." — so a Makefile that already has the bug reads as
// space-indented and detection faithfully reproduces it, and a Makefile being
// typed for the first time has nothing to detect at all and falls back to
// whatever the flag says. Both produce a broken file. Where a format MANDATES
// its whitespace, that outranks what the content happens to show.
//
// So the precedence is: what the format requires, then what the file does, then
// what the language conventionally does, then the configured default.

// Indent is an indentation style: tabs, or a number of spaces.
type Indent struct {
	Tabs  bool
	Width int // spaces per level; also the display width of a tab
}

// DefaultIndentWidth is used when a style names no width.
const DefaultIndentWidth = 4

// Unit is the text one level of indentation inserts.
func (i Indent) Unit() string {
	if i.Tabs {
		return "\t"
	}
	w := i.Width
	if w <= 0 {
		w = DefaultIndentWidth
	}
	return strings.Repeat(" ", w)
}

// String describes the style for a status line.
func (i Indent) String() string {
	if i.Tabs {
		return "tabs"
	}
	w := i.Width
	if w <= 0 {
		w = DefaultIndentWidth
	}
	return "spaces:" + itoa(w)
}

// IndentSource records which of those four decided, so a later default cannot
// contradict a stronger answer.
type IndentSource uint8

const (
	IndentFromDefault  IndentSource = iota // nothing said otherwise
	IndentFromLanguage                     // a convention for this file type
	IndentFromContent                      // the file's own indentation
	IndentFromFormat                       // the format requires it
)

// mandated reports the indentation a file format REQUIRES, whatever its current
// content says. Only formats where the wrong whitespace is an error belong
// here, not ones where it is merely unusual.
func mandated(path string) (Indent, bool) {
	if isMakefile(path) {
		// A tab begins every recipe line. GNU make has .RECIPEPREFIX to change
		// it, but a file that sets it is a file that has said so explicitly and
		// is rarer than the mistake this prevents.
		return Indent{Tabs: true}, true
	}
	return Indent{}, false
}

// conventional is what a file type is normally indented with, used only when
// the file itself has nothing to show. Weaker than mandated: a Go file indented
// with spaces is unusual, not broken, so its own content still wins.
func conventional(path string) (Indent, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return Indent{Tabs: true}, true // gofmt writes tabs
	}
	return Indent{}, false
}

func isMakefile(path string) bool {
	base := filepath.Base(path)
	switch strings.ToLower(filepath.Ext(base)) {
	case ".mk", ".mak", ".make":
		return true
	}
	lower := strings.ToLower(base)
	// Makefile.am, Makefile.in and friends are still make recipes.
	if i := strings.IndexByte(lower, '.'); i > 0 {
		lower = lower[:i]
	}
	switch lower {
	case "makefile", "gnumakefile", "bsdmakefile", "justfile":
		return true
	}
	return false
}

// IndentFor resolves the whole precedence for one file.
func IndentFor(path, content string, fallback Indent) (Indent, IndentSource) {
	if i, ok := mandated(path); ok {
		i.Width = fallback.Width
		return i, IndentFromFormat
	}
	if i, ok := DetectIndent(content); ok {
		if i.Tabs {
			i.Width = fallback.Width
		}
		return i, IndentFromContent
	}
	if i, ok := conventional(path); ok {
		i.Width = fallback.Width
		return i, IndentFromLanguage
	}
	return fallback, IndentFromDefault
}

// SpaceIndentedLines counts lines that begin with a space where the format
// requires a tab. It is how a file that is already broken says so, since raj
// fixes the next line typed and leaves the existing ones alone.
func SpaceIndentedLines(path, content string) int {
	if _, ok := mandated(path); !ok {
		return 0
	}
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// maxDetectLines bounds the scan. Indentation is decided in the first screens
// of a file in every real case, and reading a 200 MB minified bundle to answer
// a question about whitespace is not worth the open.
const maxDetectLines = 5000

// DetectIndent infers a file's indentation style from its content. ok is false
// when the file offers no evidence — it is empty, or nothing in it is indented —
// and the caller should keep its own default rather than treat the zero value as
// an answer.
//
// Tabs win on a simple majority of indented lines rather than on any presence,
// because a file indented with spaces very often contains one tab-indented line
// (a pasted snippet, a Makefile-ish block in a README) and one line should not
// redefine the file.
func DetectIndent(text string) (Indent, bool) {
	var tabs, spaces int
	var widths []int
	lines := 0
	for len(text) > 0 && lines < maxDetectLines {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		lines++
		n := 0
		for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
			n++
		}
		if n == len(line) {
			continue // blank or whitespace-only: indents nothing
		}
		switch line[0] {
		case '\t':
			tabs++
		case ' ':
			spaces++
			// Count only the leading spaces. A line that starts with spaces
			// and continues with a tab is mixed and says nothing useful about
			// a width, so it is counted as space-indented and no more.
			w := 0
			for w < len(line) && line[w] == ' ' {
				w++
			}
			widths = append(widths, w)
		}
	}
	if tabs == 0 && spaces == 0 {
		return Indent{}, false
	}
	if tabs >= spaces {
		return Indent{Tabs: true}, true
	}
	return Indent{Width: detectWidth(widths)}, true
}

// detectWidth is the most common step between adjacent indentation levels.
//
// The step, not the smallest indent: continuation lines are aligned to whatever
// they are continuing — an argument list wrapped under an open paren sits at 22
// columns — and those alignments are not indentation levels. Differences between
// neighbouring lines cancel that out, since a continuation and the line it
// continues differ by an alignment, while the ordinary lines around them differ
// by one level.
func detectWidth(widths []int) int {
	const maxStep = 8
	var freq [maxStep + 1]int
	prev := 0
	best, bestN := 0, 0
	for _, w := range widths {
		d := w - prev
		prev = w
		if d < 0 {
			d = -d
		}
		if d == 0 || d > maxStep {
			continue
		}
		freq[d]++
		// Ties go to the smaller step: a file indented by 2 produces plenty of
		// 4s wherever a level is skipped, and 2 is the one that generates 4
		// rather than the other way round.
		if freq[d] > bestN {
			best, bestN = d, freq[d]
		}
	}
	if best == 0 {
		return DefaultIndentWidth
	}
	return best
}
