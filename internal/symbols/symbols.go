// Package symbols finds the declarations in a source file, for the go-to-symbol
// overlay.
//
// It is a line scanner over leading keywords, not a parser. That is a real
// ceiling — it cannot see a function assigned to a variable across three lines,
// and it will name something inside a string literal that begins a line with
// `func`. It is also the reason this exists at all: a parser per language is a
// dependency per language, and what go-to-symbol is for is jumping to a
// declaration you already know is there. A list that is 95% right and instant
// beats a correct one that needs a language server.
//
// The honest boundary is: every symbol reported has a keyword at the start of
// its line, and every symbol declared that way is reported. Anything else is
// out of scope rather than a bug.
package symbols

import (
	"path/filepath"
	"strings"
)

// Kind is the keyword a symbol was declared with, shown beside the name so a
// list of same-named things stays readable.
type Kind string

// Symbol is one declaration.
type Symbol struct {
	Name string
	Kind Kind
	Line int // 1-based
}

// rule matches one declaration form.
type rule struct {
	keyword string
	// topLevel restricts the rule to column zero. Go declares nested
	// functions with the same keyword as top-level ones, and a symbol list
	// that includes every closure is noise; Python and Ruby indent their
	// methods, so the same restriction there would hide the useful half.
	topLevel bool
	// receiver skips a parenthesised group before the name, which is how Go
	// writes a method.
	receiver bool
}

// langs maps an extension to the declaration forms it uses. Extensions rather
// than detected languages: the file is on disk with a name, and guessing from
// content is a bigger machine than the one this list justifies.
var langs = map[string][]rule{
	".go": {
		{keyword: "func", topLevel: true, receiver: true},
		{keyword: "type", topLevel: true},
		{keyword: "const", topLevel: true},
		{keyword: "var", topLevel: true},
	},
	".py": {
		{keyword: "def"},
		{keyword: "class"},
		{keyword: "async def"},
	},
	".rb": {
		{keyword: "def"},
		{keyword: "class"},
		{keyword: "module"},
	},
	".rs": {
		{keyword: "fn"},
		{keyword: "struct"},
		{keyword: "enum"},
		{keyword: "trait"},
		{keyword: "impl"},
		{keyword: "type"},
		{keyword: "mod"},
	},
	".js":   jsRules,
	".jsx":  jsRules,
	".ts":   jsRules,
	".tsx":  jsRules,
	".mjs":  jsRules,
	".sh":   {{keyword: "function"}},
	".bash": {{keyword: "function"}},
}

var jsRules = []rule{
	{keyword: "function"},
	{keyword: "class"},
	{keyword: "async function"},
	{keyword: "export function"},
	{keyword: "export class"},
	{keyword: "export default function"},
	{keyword: "export async function"},
	{keyword: "const"},
	{keyword: "let"},
	{keyword: "export const"},
	{keyword: "interface"},
	{keyword: "type"},
	{keyword: "enum"},
}

// Supported reports whether a path has symbol rules, so a caller can say "no
// symbols for .txt" rather than "no symbols found".
func Supported(path string) bool {
	_, ok := langs[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Find scans text for declarations, in file order.
//
// File order rather than alphabetical: the list is read alongside the file it
// came from, and a fuzzy query reorders it anyway. Sorting here would only
// destroy the one ordering the scan knows for free.
func Find(path, text string) []Symbol {
	rules, ok := langs[strings.ToLower(filepath.Ext(path))]
	if !ok {
		if isMarkdown(path) {
			return headings(text)
		}
		return nil
	}
	var out []Symbol
	line := 0
	for len(text) > 0 {
		line++
		var raw string
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			raw, text = text[:i], text[i+1:]
		} else {
			raw, text = text, ""
		}
		if s, ok := match(raw, line, rules); ok {
			out = append(out, s)
		}
	}
	return out
}

// match applies the first rule that fits. Rules are tried longest-keyword
// first, so "export default function f" is a function rather than an export.
func match(raw string, line int, rules []rule) (Symbol, bool) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" {
		return Symbol{}, false
	}
	indented := len(trimmed) != len(raw)

	best := -1
	for i, r := range rules {
		if r.topLevel && indented {
			continue
		}
		if !hasKeyword(trimmed, r.keyword) {
			continue
		}
		if best < 0 || len(r.keyword) > len(rules[best].keyword) {
			best = i
		}
	}
	if best < 0 {
		return Symbol{}, false
	}
	r := rules[best]
	rest := strings.TrimLeft(trimmed[len(r.keyword):], " \t")
	if r.receiver {
		rest = skipReceiver(rest)
	}
	name := identifier(rest)
	if name == "" {
		return Symbol{}, false
	}
	return Symbol{Name: name, Kind: Kind(r.keyword), Line: line}, true
}

// hasKeyword requires the keyword to be a whole word at the start, so `constant`
// is not a `const` and `classify` is not a `class`.
func hasKeyword(s, keyword string) bool {
	if !strings.HasPrefix(s, keyword) {
		return false
	}
	rest := s[len(keyword):]
	if rest == "" {
		return false
	}
	return rest[0] == ' ' || rest[0] == '\t' || rest[0] == '('
}

// skipReceiver drops a leading parenthesised group, which is how Go writes a
// method receiver. Nesting is counted rather than scanning to the first close,
// because a receiver type can itself hold parentheses in a func type.
func skipReceiver(s string) string {
	if s == "" || s[0] != '(' {
		return s
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimLeft(s[i+1:], " \t")
			}
		}
	}
	return "" // unbalanced: not a declaration this scanner can name
}

// identifier reads the name at the head of s and stops at the first byte that
// cannot be part of one.
func identifier(s string) string {
	i := 0
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	if i == 0 {
		return ""
	}
	// A name cannot start with a digit; a line beginning "const 3" is not a
	// declaration, and treating it as one puts noise at the top of the list.
	if s[0] >= '0' && s[0] <= '9' {
		return ""
	}
	return s[:i]
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// headings treats a markdown document's ATX headings as its symbols, indented
// by level so the outline reads as one. Documents are the files where jumping
// to a named place is most useful and where no declaration keyword exists.
func headings(text string) []Symbol {
	var out []Symbol
	line := 0
	fenced := false
	for len(text) > 0 {
		line++
		var raw string
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			raw, text = text[:i], text[i+1:]
		} else {
			raw, text = text, ""
		}
		if strings.HasPrefix(raw, "```") || strings.HasPrefix(raw, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.HasPrefix(raw, "#") {
			continue
		}
		level := 0
		for level < len(raw) && raw[level] == '#' {
			level++
		}
		if level > 6 || level >= len(raw) || raw[level] != ' ' {
			continue // "#nothashtag" and "####### too deep" are not headings
		}
		name := strings.TrimSpace(raw[level:])
		if name == "" {
			continue
		}
		out = append(out, Symbol{
			Name: strings.Repeat("  ", level-1) + name,
			Kind: Kind(strings.Repeat("#", level)),
			Line: line,
		})
	}
	return out
}
