package syntax

import "raj/internal/ui"

// Semantic tokens are the language server's own tokenisation of the document.
// Where the chroma highlighter guesses from a grammar, the server knows: a name
// here is a parameter, a type there is deprecated. This file is the bridge from
// the protocol's token type names to the same palette roles the chroma
// highlighter uses, so the two agree on what "keyword" or "string" looks like.
//
// It lives beside styleFor rather than in the language-server client because
// the palette is this package's job. A second colour table in the app would
// drift from this one the first time a role changed, and the file would flicker
// between two colours as the two highlighters disagreed.

// SemanticStyle maps a semantic token type name — as named by a language
// server's legend — to one of the terminal's palette roles.
//
// The names are the protocol's standard token types; a server that sends one
// this table does not name gets ok false, and the caller leaves the chroma
// colour in place. That is deliberate: a type raj has no role for cannot be
// coloured honestly, and a guessed colour is indistinguishable from a correct
// one, so not colouring it is the only safe answer. The modifiers a token
// carries are handled by the caller; only deprecated has an obvious role, added
// as a strike-through so it composes with the type's colour.
func SemanticStyle(name string) (ui.Style, bool) {
	switch name {
	case "comment":
		return ui.DefaultStyle.With(ui.Ansi(8)).Plus(ui.Italic), true // dim, recedes
	case "string", "regexp":
		return ui.DefaultStyle.With(ui.Ansi(10)), true // bright green
	case "number":
		// The LiteralString case spans every Literal* token, so a number
		// takes the same green as a string, not the cyan here.
		return ui.DefaultStyle.With(ui.Ansi(10)), true
	case "keyword", "modifier":
		return ui.DefaultStyle.With(ui.Ansi(13)).Plus(ui.Bold), true // bright magenta, the anchor
	case "type", "class", "enum", "interface", "struct", "typeParameter",
		"enumMember", "namespace", "macro", "decorator":
		return ui.DefaultStyle.With(ui.Ansi(11)), true // bright yellow
	case "function", "method", "event":
		// Every Name* token shares one sub-category, so a function takes
		// the same yellow as a type, not the blue here.
		return ui.DefaultStyle.With(ui.Ansi(11)), true
	case "operator", "punctuation":
		return ui.DefaultStyle.With(ui.Ansi(7)), true // slightly muted against identifiers
	case "parameter", "variable", "property":
		// Identifiers are Name* tokens too, so chroma gives them the same
		// yellow; matching it keeps the overlay from repainting a name.
		return ui.DefaultStyle.With(ui.Ansi(11)), true
	}
	return ui.DefaultStyle, false
}
