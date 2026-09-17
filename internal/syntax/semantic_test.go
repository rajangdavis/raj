package syntax

import (
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// The semantic mapping is the chroma palette under another vocabulary: a
// semantic keyword and a chroma keyword must look the same, or a file would
// flicker between two colours as the two highlighters disagreed about the same
// token. Pinning a few representative types is enough — the point is that the
// table is keyed off styleFor's roles, not a second palette.
func TestSemanticStyleMatchesChromaRoles(t *testing.T) {
	h := New("x.go", true)
	cases := []struct {
		semantic string
		chroma   chroma.TokenType
	}{
		{"comment", chroma.Comment},
		{"string", chroma.LiteralString},
		{"number", chroma.LiteralNumber},
		{"keyword", chroma.Keyword},
		{"operator", chroma.Operator},
		{"type", chroma.NameClass},
		{"function", chroma.NameFunction},
		{"namespace", chroma.NameNamespace},
		{"decorator", chroma.NameDecorator},
	}
	for _, c := range cases {
		got, ok := SemanticStyle(c.semantic)
		if !ok {
			t.Errorf("SemanticStyle(%q) = no role, want the chroma role", c.semantic)
			continue
		}
		if want := h.styleFor(c.chroma); got != want {
			t.Errorf("SemanticStyle(%q) = %+v, chroma styleFor(%v) = %+v", c.semantic, got, c.chroma, want)
		}
	}
}

// Every standard token type has a role, so a server that uses one is not
// silently left with the chroma colour under a type the client advertised. A
// type outside the standard set is refused rather than guessed at; the empty
// name is what an out-of-range legend index resolves to and must be refused the
// same way.
func TestSemanticStyleCoversStandardTypes(t *testing.T) {
	names := []string{
		"namespace", "type", "class", "enum", "interface", "struct",
		"typeParameter", "parameter", "variable", "property", "enumMember",
		"event", "function", "method", "macro", "keyword", "modifier",
		"comment", "string", "number", "regexp", "operator", "decorator",
	}
	for _, name := range names {
		if _, ok := SemanticStyle(name); !ok {
			t.Errorf("standard type %q has no palette role", name)
		}
	}
	for _, name := range []string{"", "not-a-real-token-type"} {
		if _, ok := SemanticStyle(name); ok {
			t.Errorf("type %q was mapped; a guessed colour is worse than none", name)
		}
	}
}
