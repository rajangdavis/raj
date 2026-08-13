package complete

import (
	"reflect"
	"strings"
	"testing"
)

// counting wraps a text source so a test can see whether it was read.
type counting struct {
	text  string
	reads int
}

func (c *counting) read() string { c.reads++; return c.text }

// The whole point: a buffer that has not changed is not rescanned. Typing
// modifies one buffer and leaves the others exactly as they were, which is
// nearly all of them nearly always.
func TestUnchangedBuffersAreNotRescanned(t *testing.T) {
	c := NewCache()
	src := &counting{text: "func handleRequest() {}\n"}

	for i := 0; i < 10; i++ {
		c.Words("/w/a.go", 1, src.read)
	}
	if src.reads != 1 {
		t.Errorf("scanned %d times, want once", src.reads)
	}
}

// A version bump invalidates. Nothing else does, because nothing else means the
// bytes changed.
func TestVersionBumpRescans(t *testing.T) {
	c := NewCache()
	src := &counting{text: "alpha\n"}
	c.Words("/w/a.go", 1, src.read)

	src.text = "alpha beta\n"
	got := c.Words("/w/a.go", 2, src.read)
	if src.reads != 2 {
		t.Fatalf("scanned %d times, want twice", src.reads)
	}
	if !contains(got, "beta") {
		t.Errorf("got %v, want the new word", got)
	}
	// And going back to a version already seen still rescans rather than
	// resurrecting stale words: the cache holds one entry per path, and an
	// older version is not the one it holds.
	before := src.reads
	c.Words("/w/a.go", 1, src.read)
	if src.reads == before {
		t.Error("an older version was served from the cache")
	}
}

// Version zero is a real version — an untouched buffer — so presence in the map
// has to be what marks an entry valid, not a zero check.
func TestVersionZeroIsCached(t *testing.T) {
	c := NewCache()
	src := &counting{text: "alpha\n"}
	c.Words("/w/a.go", 0, src.read)
	c.Words("/w/a.go", 0, src.read)
	if src.reads != 1 {
		t.Errorf("scanned %d times at version zero, want once", src.reads)
	}
}

// Each buffer is cached separately, so editing one does not throw away the
// others.
func TestBuffersAreIndependent(t *testing.T) {
	c := NewCache()
	a := &counting{text: "alpha\n"}
	b := &counting{text: "bravo\n"}
	c.Words("/w/a.go", 1, a.read)
	c.Words("/w/b.go", 1, b.read)

	c.Words("/w/a.go", 2, a.read) // a changed
	c.Words("/w/b.go", 1, b.read) // b did not
	if b.reads != 1 {
		t.Errorf("editing one buffer rescanned another (%d reads)", b.reads)
	}
}

// Closed buffers are dropped, or a long session holds the words of every file
// ever visited.
func TestRetainDropsClosedBuffers(t *testing.T) {
	c := NewCache()
	for _, p := range []string{"/w/a.go", "/w/b.go", "/w/c.go"} {
		c.Words(p, 1, func() string { return "word\n" })
	}
	if c.Len() != 3 {
		t.Fatalf("cached %d, want 3", c.Len())
	}
	c.Retain(map[string]bool{"/w/a.go": true})
	if c.Len() != 1 {
		t.Errorf("after Retain %d entries remain, want 1", c.Len())
	}
	// The kept one is still cached rather than merely surviving.
	src := &counting{text: "word\n"}
	c.Words("/w/a.go", 1, src.read)
	if src.reads != 0 {
		t.Error("Retain invalidated the buffer it kept")
	}
}

func snapshots(cur string, texts map[string]string, syms map[string][]string) []Snapshot {
	var out []Snapshot
	for path, text := range texts {
		text := text
		out = append(out, Snapshot{
			Path:    path,
			Version: 1,
			Text:    func() string { return text },
			Symbols: syms[path],
			Current: path == cur,
		})
	}
	// Sorted so the input order is fixed; ranking must not depend on it, and a
	// test that varies it is testing map iteration rather than ranking.
	sortSnapshots(out)
	return out
}

func sortSnapshots(s []Snapshot) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Path < s[j-1].Path; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// The cached ranking must equal the uncached one. They are two code paths over
// the same rules, and the ranking tests are written against the uncached one —
// so if they drift, the tests stop covering what actually runs.
func TestCachedRankingMatchesUncached(t *testing.T) {
	texts := map[string]string{
		"/w/app.go":    "func handleRequest() {}\nvar handler = 1\nhandoff()\n",
		"/w/other.go":  "func handleTimeout() {}\nvar handshake = 2\n",
		"/w/README.md": "handwritten notes\n",
	}
	syms := map[string][]string{
		"/w/app.go":   {"handleRequest", "handoff"},
		"/w/other.go": {"handleTimeout"},
	}
	uncached := Buffers{Current: "/w/app.go", Contents: texts, Symbols: syms}
	c := NewCache()
	snaps := snapshots("/w/app.go", texts, syms)

	for _, prefix := range []string{"hand", "handl", "h", "ha", "zzz", ""} {
		want := uncached.Rank(prefix)
		got := c.Rank(snaps, prefix)
		if !reflect.DeepEqual(words(got), words(want)) {
			t.Errorf("prefix %q: cached %v, uncached %v", prefix, words(got), words(want))
		}
	}
}

// And it stays equal across an edit, which is where a stale cache would show
// up as a suggestion for text that is no longer there.
func TestCachedRankingAfterAnEdit(t *testing.T) {
	c := NewCache()
	text := "handoff()\n"
	snaps := []Snapshot{{
		Path: "/w/a.go", Version: 1, Current: true,
		Text: func() string { return text },
	}}
	if got := words(c.Rank(snaps, "hand")); !contains(got, "handoff") {
		t.Fatalf("got %v, want handoff", got)
	}

	text = "handshake()\n"
	snaps[0].Version = 2
	got := words(c.Rank(snaps, "hand"))
	if contains(got, "handoff") {
		t.Errorf("got %v; handoff was deleted", got)
	}
	if !contains(got, "handshake") {
		t.Errorf("got %v, want handshake", got)
	}
}

// The order is total, so identical keystrokes give an identical list. Map
// iteration is how this goes wrong, and it fails intermittently rather than
// reliably.
func TestCachedOrderIsStable(t *testing.T) {
	c := NewCache()
	snaps := snapshots("/w/a.go", map[string]string{
		"/w/a.go": "handoff handle handler handshake\n",
		"/w/b.go": "handoff handle handler handshake\n",
	}, nil)
	first := words(c.Rank(snaps, "hand"))
	for i := 0; i < 50; i++ {
		if got := words(c.Rank(snaps, "hand")); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: %v, want %v", i, got, first)
		}
	}
}

// Nothing here may panic on the shapes a buffer passes through while open.
func TestCacheDegenerateInputs(t *testing.T) {
	c := NewCache()
	c.Words("", 0, func() string { return "" })
	c.Words("/w/a.go", 0, func() string { return "\x00\xff" })
	c.Rank(nil, "abc")
	c.Rank([]Snapshot{{Path: "/w/a.go", Text: func() string { return "" }}}, "abc")
	c.Retain(nil)
	var zero Cache // usable without NewCache
	zero.Words("/w/a.go", 1, func() string { return "word\n" })
}

func contains(ws []string, w string) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}

// The cost this exists to remove, measured the same way as the uncached
// benchmark so the two are comparable.
func BenchmarkCachedRank(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString("func handleSomething(writer http.ResponseWriter) error {\n")
		sb.WriteString("\treturn processRequest(writer, handlerConfig)\n}\n\n")
	}
	src := sb.String()

	var snaps []Snapshot
	for _, n := range []string{"/w/a.go", "/w/b.go", "/w/c.go", "/w/d.go", "/w/e.go"} {
		snaps = append(snaps, Snapshot{
			Path: n, Version: 1, Current: n == "/w/a.go",
			Text: func() string { return src },
		})
	}
	c := NewCache()
	c.Rank(snaps, "hand") // warm, as it would be after the first keystroke

	b.SetBytes(int64(len(src) * len(snaps)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Rank(snaps, "hand")
	}
}
