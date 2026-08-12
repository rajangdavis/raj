package symbols

import (
	"strings"
	"testing"
)

// The invariants the overlay depends on, asserted against arbitrary bytes: a
// caller jumps to Line and displays Name, so a line out of range is a crash and
// an empty name is a blank row. Increasing lines are what makes "file order"
// meaningful rather than incidental.
func FuzzFind(f *testing.F) {
	f.Add("a.go", "func main() {}\n")
	f.Add("a.py", "class Cat:\n  def meow(self): pass\n")
	f.Add("README.md", "# Title\n## Section\n")
	f.Add("a.rs", "impl Cat {\n    fn meow(&self) {}\n}\n")
	f.Add("a.go", "func ((((\n")
	f.Add("x.txt", "func nope() {}\n")

	f.Fuzz(func(t *testing.T, path, text string) {
		lines := strings.Count(text, "\n") + 1
		prev := 0
		for _, s := range Find(path, text) {
			if s.Name == "" {
				t.Fatalf("empty name in %q", text)
			}
			if s.Line < 1 || s.Line > lines {
				t.Fatalf("line %d outside 1..%d for %q", s.Line, lines, text)
			}
			if s.Line <= prev {
				t.Fatalf("line %d after %d: not in file order", s.Line, prev)
			}
			prev = s.Line
			// The name has to be on the line it points at, or the jump lands
			// somewhere the list did not promise. Markdown indents its
			// headings for the outline, so compare on the trimmed name.
			row := strings.Split(text, "\n")[s.Line-1]
			if !strings.Contains(row, strings.TrimSpace(s.Name)) {
				t.Fatalf("%q not on line %d (%q)", s.Name, s.Line, row)
			}
		}
	})
}

func BenchmarkFind(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		sb.WriteString("// a comment line that is not a declaration at all\n")
		sb.WriteString("func handler(w int) error {\n\treturn nil\n}\n\n")
	}
	src := sb.String()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Find("bench.go", src)
	}
}
