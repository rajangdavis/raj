package complete

import "testing"

// A bare $N is a tab stop at the point it appears, and the parse still appends
// a final resting stop because literal text follows it.
func TestParseSnippetPlainTabStop(t *testing.T) {
	text, stops := ParseSnippet("foo $1 bar")
	if text != "foo  bar" {
		t.Fatalf("text = %q, want %q", text, "foo  bar")
	}
	if len(stops) != 2 {
		t.Fatalf("stops = %+v, want the tab stop and the final stop", stops)
	}
	if stops[0] != (TabStop{Offset: 4}) {
		t.Errorf("stop = %+v, want offset 4", stops[0])
	}
	if stops[1] != (TabStop{Offset: len(text)}) {
		t.Errorf("final stop = %+v, want offset %d", stops[1], len(text))
	}
}

// The number, not the position, decides the visit order: $2 written before $1
// is visited second.
func TestParseSnippetOrdersByNumber(t *testing.T) {
	text, stops := ParseSnippet("$2 $1")
	if text != " " {
		t.Fatalf("text = %q, want one space", text)
	}
	if len(stops) < 2 {
		t.Fatalf("stops = %+v, want both stops", stops)
	}
	if stops[0].Offset != 1 {
		t.Errorf("first stop = %+v, want the $1 at offset 1", stops[0])
	}
	if stops[1].Offset != 0 {
		t.Errorf("second stop = %+v, want the $2 at offset 0", stops[1])
	}
}

// A placeholder's default is inserted and selected, so typing replaces it
// rather than landing beside it.
func TestParseSnippetPlaceholderDefault(t *testing.T) {
	text, stops := ParseSnippet("${1:name}")
	if text != "name" {
		t.Fatalf("text = %q, want name", text)
	}
	if len(stops) != 1 || stops[0] != (TabStop{Offset: 0, Length: 4}) {
		t.Errorf("stops = %+v, want one stop covering the default", stops)
	}
}

// A choice has no chooser here, so its first option becomes the default and is
// selected; the parser never types the bar syntax into the buffer.
func TestParseSnippetChoiceUsesFirstOption(t *testing.T) {
	text, stops := ParseSnippet("${1|alpha,beta|}")
	if text != "alpha" {
		t.Fatalf("text = %q, want the first option", text)
	}
	if len(stops) != 1 || stops[0] != (TabStop{Offset: 0, Length: 5}) {
		t.Errorf("stops = %+v, want one stop covering the first option", stops)
	}
}

// An escaped dollar is a literal dollar: no stop, and no template syntax in the
// buffer. A doubled backslash collapses to one.
func TestParseSnippetEscapesAreLiteral(t *testing.T) {
	text, stops := ParseSnippet(`cost \$1 and \\ done`)
	if text != `cost $1 and \ done` {
		t.Errorf("text = %q", text)
	}
	if len(stops) != 0 {
		t.Errorf("stops = %+v, want none", stops)
	}
}

// $0 is the final stop wherever it appears, and its presence suppresses the
// implicit final stop.
func TestParseSnippetZeroIsFinal(t *testing.T) {
	text, stops := ParseSnippet("x$0y")
	if text != "xy" {
		t.Fatalf("text = %q, want xy", text)
	}
	if len(stops) != 1 || stops[0] != (TabStop{Offset: 1}) {
		t.Errorf("stops = %+v, want only the final $0", stops)
	}
}

// A placeholder default is itself a template: a nested placeholder contributes
// its text and its own stop, and the outer stop still covers the whole default.
func TestParseSnippetNestedDefault(t *testing.T) {
	text, stops := ParseSnippet("${1:a ${2:b}}")
	if text != "a b" {
		t.Fatalf("text = %q, want a b", text)
	}
	if len(stops) != 2 {
		t.Fatalf("stops = %+v, want the outer and inner stops", stops)
	}
	if stops[0] != (TabStop{Offset: 0, Length: 3}) {
		t.Errorf("outer stop = %+v", stops[0])
	}
	if stops[1] != (TabStop{Offset: 2, Length: 1}) {
		t.Errorf("inner stop = %+v", stops[1])
	}
}

// An escaped closing brace inside a default does not close the construct early.
func TestParseSnippetEscapedBraceInDefault(t *testing.T) {
	text, stops := ParseSnippet(`${1:a\}b}`)
	if text != "a}b" {
		t.Fatalf("text = %q, want a}b", text)
	}
	if len(stops) != 1 || stops[0] != (TabStop{Offset: 0, Length: 3}) {
		t.Errorf("stops = %+v, want the whole default", stops)
	}
}

// A template that ends on a stop needs no extra final stop; one with literal
// text after the last stop gets a resting position at the end.
func TestParseSnippetFinalStopOnlyWhenNeeded(t *testing.T) {
	if _, stops := ParseSnippet("pre $1"); len(stops) != 1 {
		t.Errorf("stops = %+v, want only the $1", stops)
	}
	text, stops := ParseSnippet("pre ${1:x} post")
	if len(stops) != 2 || stops[1] != (TabStop{Offset: len(text)}) {
		t.Errorf("stops = %+v, want a final stop at %d", stops, len(text))
	}
}

// Malformed and unknown constructs are copied through verbatim: no panic, no
// stop, and — the property that matters most — no half-expanded template.
func TestParseSnippetMalformedStaysLiteral(t *testing.T) {
	cases := []string{
		"$",
		"${",
		"${1:name",
		"${1|a,b",
		"${1:${2:x}",
		"${TM_FILENAME}",
		"a}b",
	}
	for _, src := range cases {
		text, stops := ParseSnippet(src)
		if text != src {
			t.Errorf("ParseSnippet(%q) text = %q, want it copied through", src, text)
		}
		if len(stops) != 0 {
			t.Errorf("ParseSnippet(%q) stops = %+v, want none", src, stops)
		}
	}
}
