package complete

import (
	"reflect"
	"strings"
	"testing"
)

func words(cs []Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Word)
	}
	return out
}

// Words collects identifier runs. It over-collects on purpose: a word inside a
// comment or a string is still a word someone typed and may be the one they are
// typing again, and telling them apart needs a lexer that would be wrong about
// some language.
func TestWords(t *testing.T) {
	got := Words("func handleRequest(w http.ResponseWriter) { // note_this\n"+
		"  x := 42\n  s := \"quoted_word\"\n}", nil)
	for _, want := range []string{
		"func", "handleRequest", "http", "ResponseWriter", "note_this", "quoted_word",
	} {
		if !got[want] {
			t.Errorf("missing %q", want)
		}
	}
	// A number is not an identifier, and a single character is not worth
	// suggesting.
	for _, dont := range []string{"42", "x", "w", "s"} {
		if got[dont] {
			t.Errorf("collected %q", dont)
		}
	}
}

// A word starting with a digit is a number however it ends.
func TestWordsSkipsNumbers(t *testing.T) {
	got := Words("0x1f 12abc 3_000 name9", nil)
	if got["0x1f"] || got["12abc"] || got["3_000"] {
		t.Errorf("collected a number: %v", got)
	}
	if !got["name9"] {
		t.Error("a digit inside an identifier is fine")
	}
}

// The prefix is what has been typed up to the cursor, not the whole word under
// it: completing from the middle would replace letters typed deliberately.
func TestPrefixAt(t *testing.T) {
	cases := []struct {
		line string
		col  int
		want string
	}{
		{"handleReq", 9, "handleReq"},
		{"x := handleReq", 14, "handleReq"},
		{"handleRequest", 6, "handle"}, // cursor mid-word: only what is behind it
		{"a.b(han", 7, "han"},
		{"", 0, ""},
		{"   ", 3, ""},
		{"x := 42", 7, ""}, // a number is not an identifier
		{"foo(", 4, ""},    // nothing behind the cursor
		{"snake_case", 10, "snake_case"},
		{"héllo", 6, "héllo"}, // multi-byte identifier bytes count
		{"abc", 99, "abc"},    // a column past the end clamps
		{"abc", -1, ""},
	}
	for _, c := range cases {
		if got := PrefixAt(c.line, c.col); got != c.want {
			t.Errorf("PrefixAt(%q, %d) = %q, want %q", c.line, c.col, got, c.want)
		}
	}
}

var project = Buffers{
	Current: "/w/app.go",
	Contents: map[string]string{
		"/w/app.go":    "func handleRequest() {}\nvar handler = 1\nhandoff()\n",
		"/w/other.go":  "func handleTimeout() {}\nvar handshake = 2\n",
		"/w/README.md": "handwritten notes\n",
	},
	Symbols: map[string][]string{
		"/w/app.go":   {"handleRequest", "handoff"},
		"/w/other.go": {"handleTimeout"},
	},
}

// Nothing is offered until enough has been typed to discriminate: one character
// matches most of a file and ranks it by nothing useful.
func TestMinPrefix(t *testing.T) {
	if got := project.Rank("h"); got != nil {
		t.Errorf("one character offered %v", words(got))
	}
	if got := project.Rank(""); got != nil {
		t.Errorf("an empty prefix offered %v", words(got))
	}
	if got := project.Rank("ha"); len(got) == 0 {
		t.Error("two characters offered nothing")
	}
}

// Matching is a prefix test, not a fuzzy one. Completion finishes a word you
// have started, so a candidate that does not begin with what you typed is not a
// completion of it — inserting one would replace letters already on screen.
func TestPrefixMatchNotFuzzy(t *testing.T) {
	for _, w := range words(project.Rank("hand")) {
		if !strings.HasPrefix(w, "hand") {
			t.Errorf("%q does not start with the prefix", w)
		}
	}
	if got := project.Rank("hnd"); len(got) != 0 {
		t.Errorf("a subsequence matched: %v", words(got))
	}
}

// Case matters, because in most languages Foo and foo are different things.
func TestCaseSensitive(t *testing.T) {
	b := Buffers{
		Current:  "/w/a.go",
		Contents: map[string]string{"/w/a.go": "Widget widget WIDGET\n"},
	}
	if got := words(b.Rank("Wid")); !reflect.DeepEqual(got, []string{"Widget"}) {
		t.Errorf("got %v, want just Widget", got)
	}
	if got := words(b.Rank("wid")); !reflect.DeepEqual(got, []string{"widget"}) {
		t.Errorf("got %v, want just widget", got)
	}
}

// A declaration outranks a word that merely appears, and a word from the file
// being edited outranks the same word from elsewhere. Locality is the best
// signal available without types.
func TestRanking(t *testing.T) {
	got := words(project.Rank("hand"))
	if len(got) < 4 {
		t.Fatalf("got %v, want at least four candidates", got)
	}
	// handleRequest and handoff are declarations in the current file, so both
	// outrank handleTimeout (a declaration elsewhere) and handwritten (a plain
	// word). Shortest-first breaks the tie between the two local declarations.
	if got[0] != "handoff" || got[1] != "handleRequest" {
		t.Errorf("got %v, want the current file's declarations first", got)
	}
	rank := map[string]int{}
	for i, w := range got {
		rank[w] = i
	}
	if rank["handleTimeout"] > rank["handwritten"] {
		t.Errorf("a declaration ranked below a comment word: %v", got)
	}
}

// The word already typed is not a completion of itself.
func TestExactPrefixIsNotSuggested(t *testing.T) {
	for _, w := range words(project.Rank("handoff")) {
		if w == "handoff" {
			t.Error("suggested the word already typed")
		}
	}
}

// The order is total, so identical keystrokes give an identical list. Map
// iteration is the obvious way for this to go wrong and it fails intermittently
// rather than reliably, which is the worst kind.
func TestOrderIsStable(t *testing.T) {
	first := words(project.Rank("hand"))
	for i := 0; i < 50; i++ {
		if got := words(project.Rank("hand")); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differed:\n got %v\nwant %v", i, got, first)
		}
	}
}

// A word in several buffers appears once, at its best rank.
func TestNoDuplicates(t *testing.T) {
	b := Buffers{
		Current: "/w/a.go",
		Contents: map[string]string{
			"/w/a.go": "shared\n", "/w/b.go": "shared\n", "/w/c.go": "shared\n",
		},
	}
	got := words(b.Rank("sha"))
	if len(got) != 1 {
		t.Errorf("got %v, want one entry", got)
	}
	if len(got) == 1 && b.Rank("sha")[0].Detail != "" {
		t.Error("the local copy should win, and local candidates show no file")
	}
}

// The list is bounded: past a screenful nobody is reading, and ranking is paid
// per keystroke.
func TestResultsAreBounded(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("prefixed")
		sb.WriteString(strings.Repeat("x", i%20+1))
		sb.WriteByte('\n')
	}
	b := Buffers{Current: "/w/a.go", Contents: map[string]string{"/w/a.go": sb.String()}}
	if got := b.Rank("prefixed"); len(got) > MaxResults {
		t.Errorf("got %d candidates, want at most %d", len(got), MaxResults)
	}
}

// Nothing here may panic on the shapes a buffer passes through while being
// edited.
func TestDegenerateInputs(t *testing.T) {
	empty := Buffers{}
	empty.Rank("abc")
	Buffers{Current: "/w/a.go"}.Rank("abc")
	Buffers{Contents: map[string]string{"": ""}}.Rank("abc")
	Buffers{Contents: map[string]string{"/w/a.go": "\x00\xff\n"}}.Rank("abc")
	Words("", nil)
	Words("\x00\xff\xfe", nil)
	PrefixAt("", 0)
}

// Candidates are what a language server will supply later, so the interface has
// to be satisfiable by something that is not a buffer scan.
func TestSourceInterface(t *testing.T) {
	var s Source = project
	if c := s.Candidates("hand"); len(c) == 0 {
		t.Error("Buffers does not satisfy Source usefully")
	}
	got := project.Rank("hand")
	for i := 1; i < len(got); i++ {
		if got[i-1].score < got[i].score {
			t.Errorf("candidate %d scores below %d: %v", i-1, i, words(got))
			break
		}
	}
}
