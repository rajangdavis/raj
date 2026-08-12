package symbols

import (
	"strings"
	"testing"
)

// names renders a result compactly so a failure prints the whole list rather
// than the first disagreement.
func names(syms []Symbol) string {
	var b strings.Builder
	for i, s := range syms {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(s.Name)
		b.WriteString("@")
		b.WriteString(itoa(s.Line))
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func check(t *testing.T, path, text, want string) {
	t.Helper()
	if got := names(Find(path, text)); got != want {
		t.Errorf("%s:\n got %q\nwant %q", path, got, want)
	}
}

// The contract: a symbol is a declaration keyword at the start of a line. Go
// restricts that to column zero, because a symbol list containing every closure
// is a list nobody reads.
func TestGo(t *testing.T) {
	src := `package picker

import "strings"

type Picker struct {
	Root string
}

const MaxFiles = 20000

var langs = map[string]int{}

func New(root string) *Picker {
	handler := func(x int) int { return x }
	return nil
}

func (p *Picker) Handle(a Action) string {
	return ""
}

func (p *Picker) resolve(rel string) string { return rel }
`
	check(t, "picker.go", src,
		"Picker@5 MaxFiles@9 langs@11 New@13 Handle@18 resolve@22")
}

// A method's name is after the receiver, and a receiver can hold parentheses of
// its own.
func TestGoReceivers(t *testing.T) {
	src := "func (p *Picker) A() {}\n" +
		"func (h handler(int)) B() {}\n" +
		"func (unbalanced C() {}\n"
	check(t, "x.go", src, "A@1 B@2")
}

// Python and Ruby indent their methods, so restricting to column zero would
// hide the half of the list worth having.
func TestIndentedLanguages(t *testing.T) {
	check(t, "a.py", "class Cat:\n    def meow(self):\n        pass\n\nasync def fetch():\n    pass\n",
		"Cat@1 meow@2 fetch@5")
	check(t, "a.rb", "module Zoo\n  class Cat\n    def meow\n    end\n  end\nend\n",
		"Zoo@1 Cat@2 meow@3")
}

func TestRustAndJS(t *testing.T) {
	check(t, "a.rs", "struct Cat;\nimpl Cat {\n    fn meow(&self) {}\n}\ntrait Loud {}\n",
		"Cat@1 Cat@2 meow@3 Loud@5")
	check(t, "a.ts", "export default function main() {}\nexport const x = 1;\ninterface Shape {}\nclass Box {}\n",
		"main@1 x@2 Shape@3 Box@4")
}

// The longest matching keyword wins, so an exported function is a function
// rather than an export whose name is "function".
func TestLongestKeywordWins(t *testing.T) {
	check(t, "a.js", "export function alpha() {}\nasync function beta() {}\n", "alpha@1 beta@2")
}

// A keyword has to be a whole word. "constant" is not a const, and "classify"
// is not a class — this is the failure mode a prefix test walks straight into.
func TestKeywordMustBeAWholeWord(t *testing.T) {
	check(t, "a.js", "constant = 1;\nclassify();\nconst real = 2;\n", "real@3")
	check(t, "a.go", "typealias X = Y\ntype Real struct{}\n", "Real@2")
}

// A declaration with nothing nameable after it is not a symbol. These lines are
// what a half-typed file looks like, and putting them in the list means the
// list flickers while you type.
func TestNothingToName(t *testing.T) {
	check(t, "a.go", "func\nfunc ()\nconst 3\nvar\ntype  \nfunc Real() {}\n", "Real@6")
}

// Markdown has no declarations, and is where jumping to a named place is most
// useful. Headings are the outline; fenced code is not.
func TestMarkdownHeadings(t *testing.T) {
	src := "# Title\n\nsome text\n\n## Section\n\n```\n# not a heading\n```\n\n#nothashtag\n\n####### too deep\n\n### Deep\n"
	got := Find("README.md", src)
	if want := "Title@1   Section@5     Deep@15"; names(got) != want {
		t.Errorf("got %q, want %q", names(got), want)
	}
	if got[1].Kind != "##" {
		t.Errorf("kind = %q, want ##", got[1].Kind)
	}
}

// An unknown extension reports nothing, and says so distinguishably: "this file
// has no symbol rules" is different from "this file has no symbols".
func TestUnsupported(t *testing.T) {
	if Supported("notes.txt") {
		t.Error(".txt claims symbol rules")
	}
	if !Supported("a.go") || !Supported("A.GO") {
		t.Error("go should be supported, case-insensitively")
	}
	if got := Find("notes.txt", "func Whatever() {}\n"); got != nil {
		t.Errorf("got %v, want nothing", got)
	}
}

// File order, not alphabetical: the list is read alongside the file, and a
// query reorders it anyway.
func TestFileOrder(t *testing.T) {
	check(t, "a.go", "func zebra() {}\nfunc apple() {}\n", "zebra@1 apple@2")
}

// A file with no trailing newline still reports its last line.
func TestLastLineWithoutNewline(t *testing.T) {
	check(t, "a.go", "func a() {}\nfunc b() {}", "a@1 b@2")
}

// The scanner must not panic or hang on anything a buffer can hold, including
// the truncated and unbalanced states a file passes through while being typed.
func TestNeverPanics(t *testing.T) {
	inputs := []string{
		"", "\n", "\n\n\n", "func", "func (", "func ((((", "func )", "type",
		"#", "###", "```", "```\n#x\n", strings.Repeat("func (", 5000),
		"\x00\x01\x02", "func \xff\xfe() {}", strings.Repeat("a", 100000),
	}
	for _, ext := range []string{".go", ".py", ".rb", ".rs", ".ts", ".md", ".txt"} {
		for _, in := range inputs {
			Find("f"+ext, in) // a panic or a hang fails the test by itself
		}
	}
}
