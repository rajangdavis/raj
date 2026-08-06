package search

import (
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// corpusRoot is the tree the benchmarks walk. It defaults to raj itself so the
// benchmarks run anywhere; set REPO_CORPUS to a large tree (the Go source
// distribution works well) for numbers that are not dominated by startup.
func corpusRoot(tb testing.TB) string {
	if r := os.Getenv("REPO_CORPUS"); r != "" {
		return r
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		tb.Skip("cannot resolve corpus root")
	}
	return root
}

// ---------------------------------------------------------------------------
// Equivalence. The literal matcher must agree with the regexp it replaces on
// every line it is asked about — that is the whole safety argument.

// findVia runs the matcher the way scan does: prepare the haystack for this
// content, then sweep it — or run the line fallback if prepare handed one back.
// Calling find directly would skip the fold and test nothing real.
func findVia(m matcher, line []byte) (int, int, bool) {
	hay, fallback := m.prepare(line)
	if fallback != nil {
		return fallback.find(line)
	}
	return m.find(hay)
}

func matcherPair(q Query) (matcher, matcher) {
	re, err := compile(q)
	if err != nil {
		panic(err)
	}
	return newMatcher(q, re), regexMatcher{re}
}

func TestLiteralMatcherAgreesWithRegexp(t *testing.T) {
	lines := corpusLines(t, corpusRoot(t), 20000)
	queries := []Query{
		{Text: "func", Case: true},
		{Text: "func"},
		{Text: "Func", Case: true},
		{Text: "FUNC"},
		{Text: "err", Case: true, Word: true},
		{Text: "ERR", Word: true},
		{Text: "return nil", Case: true},
		{Text: "RETURN NIL"},
		{Text: "zzq-no-such-token", Case: true},
		{Text: "_", Case: true},
		{Text: "//", Case: true},
		{Text: "(", Case: true},
		{Text: "(", Word: true}, // non-word edges: must fall back
		{Text: "é", Case: true}, // non-ASCII literal
		{Text: "É"},             // non-ASCII, case-insensitive: must fall back
	}
	for _, q := range queries {
		fast, slow := matcherPair(q)
		for _, ln := range lines {
			gs, ge, gok := findVia(fast, ln)
			ws, we, wok := findVia(slow, ln)
			if gok != wok || (gok && (gs != ws || ge != we)) {
				t.Fatalf("query %+v line %q: fast=(%d,%d,%v) regexp=(%d,%d,%v)",
					q, ln, gs, ge, gok, ws, we, wok)
			}
		}
	}
}

// Fuzz with random literals over random lines, including non-ASCII, to catch
// the cases a fixed query list does not think of.
func TestLiteralMatcherFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []rune("abcXYZ_09 \t(){}éßΩ/*")
	randStr := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		return sb.String()
	}
	for i := 0; i < 20000; i++ {
		q := Query{
			Text: randStr(1 + rng.Intn(4)),
			Case: rng.Intn(2) == 0,
			Word: rng.Intn(2) == 0,
		}
		re, err := compile(q)
		if err != nil {
			continue
		}
		fast, slow := newMatcher(q, re), regexMatcher{re}
		line := []byte(randStr(rng.Intn(40)))
		gs, ge, gok := findVia(fast, line)
		ws, we, wok := findVia(slow, line)
		if gok != wok || (gok && (gs != ws || ge != we)) {
			t.Fatalf("q=%+v line=%q: fast=(%d,%d,%v) regexp=(%d,%d,%v)",
				q, line, gs, ge, gok, ws, we, wok)
		}
	}
}

// End to end: Run must return byte-identical results with and without the
// fast path, including Capped and Files.
func TestRunEquivalence(t *testing.T) {
	root := corpusRoot(t)
	for _, q := range []Query{
		{Text: "func", Case: true},
		{Text: "FUNC"},
		{Text: "Result", Case: true, Word: true},
		{Text: "zzq-no-such-token"},
		{Text: `func\s+\(`, Regex: true, Case: true},
	} {
		forceRegexp = true
		want := Run(root, q)
		forceRegexp = false
		got := Run(root, q)
		if want.Files != got.Files || want.Capped != got.Capped || len(want.Matches) != len(got.Matches) {
			t.Fatalf("q=%+v: files %d/%d capped %v/%v matches %d/%d",
				q, want.Files, got.Files, want.Capped, got.Capped, len(want.Matches), len(got.Matches))
		}
		for i := range want.Matches {
			if want.Matches[i] != got.Matches[i] {
				t.Fatalf("q=%+v match %d: %+v vs %+v", q, i, want.Matches[i], got.Matches[i])
			}
		}
	}
}

func TestFoldLower(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		sawHigh bool
	}{
		{"", "", false},
		{"Hello, World!", "hello, world!", false},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", false},
		{"\x00\x01\t\n [](){}", "\x00\x01\t\n [](){}", false},
		{"@AZ[`az{", "@az[`az{", false}, // boundaries either side of A-Z
		// The property that matters: high bytes survive untouched, so UTF-8
		// stays valid and offsets keep meaning what they meant.
		{"caf\u00e9 AU LAIT", "caf\u00e9 au lait", true},
		{"\u00c9CLAIR", "\u00c9clair", true}, // É is not folded; that is the regexp's job
		{"\u65e5\u672c\u8a9eABC", "\u65e5\u672c\u8a9eabc", true},
	} {
		b := []byte(tc.in)
		if got := foldLower(b); got != tc.sawHigh {
			t.Errorf("foldLower(%q) sawHigh=%v want %v", tc.in, got, tc.sawHigh)
		}
		if string(b) != tc.want {
			t.Errorf("foldLower(%q) = %q want %q", tc.in, b, tc.want)
		}
	}

	// Exhaustive over every byte value, at every alignment, against the
	// obvious loop. Alignment matters because the SWAR body handles eight
	// bytes at a time and the tail handles the rest.
	for off := 0; off < 16; off++ {
		buf := make([]byte, off+256)
		want := make([]byte, off+256)
		for i := 0; i < 256; i++ {
			c := byte(i)
			buf[off+i] = c
			if c >= 'A' && c <= 'Z' {
				c += 32
			}
			want[off+i] = c
		}
		region := buf[off:]
		if !foldLower(region) {
			t.Fatalf("off=%d: high bytes present but not reported", off)
		}
		for i := 0; i < 256; i++ {
			if region[i] != want[off+i] {
				t.Fatalf("off=%d byte %#x: got %#x want %#x", off, i, region[i], want[off+i])
			}
		}
	}

	// Folding must never invalidate UTF-8: every rune round-trips.
	var sb []byte
	for r := rune(1); r < 0x2FFFF; r++ {
		if utf8.ValidRune(r) {
			sb = utf8.AppendRune(sb, r)
		}
	}
	folded := append([]byte(nil), sb...)
	foldLower(folded)
	if !utf8.Valid(folded) {
		t.Fatal("folding produced invalid UTF-8")
	}
	if len(folded) != len(sb) {
		t.Fatal("folding changed the length")
	}
}

// corpusLines collects up to max lines of real source to match against.
func corpusLines(tb testing.TB, root string, max int) [][]byte {
	var out [][]byte
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= max {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > MaxFileSize {
			return nil
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if len(out) >= max {
				break
			}
			out = append(out, []byte(ln))
		}
		return nil
	})
	if len(out) == 0 {
		tb.Skip("empty corpus")
	}
	return out
}

// ---------------------------------------------------------------------------
// Benchmarks

func benchRun(b *testing.B, q Query, regexpPath bool) {
	root := corpusRoot(b)
	forceRegexp = regexpPath
	defer func() { forceRegexp = false }()
	res := Run(root, q) // warm the page cache
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res = Run(root, q)
	}
	b.StopTimer()
	b.ReportMetric(float64(len(res.Matches)), "matches")
}

var (
	qLitSensitive   = Query{Text: "zzq-no-such-token", Case: true}
	qLitInsensitive = Query{Text: "zzq-no-such-token"}
	qLitRare        = Query{Text: "ErrNotSupported", Case: true}
	qLitRareInsens  = Query{Text: "errnotsupported"}
	qRegex          = Query{Text: `Err[A-Z][a-z]+Supported`, Regex: true, Case: true}
)

func BenchmarkRun_Literal_Sensitive_Regexp(b *testing.B) { benchRun(b, qLitSensitive, true) }
func BenchmarkRun_Literal_Sensitive_Fast(b *testing.B)   { benchRun(b, qLitSensitive, false) }

func BenchmarkRun_Literal_Insensitive_Regexp(b *testing.B) { benchRun(b, qLitInsensitive, true) }
func BenchmarkRun_Literal_Insensitive_Fast(b *testing.B)   { benchRun(b, qLitInsensitive, false) }

func BenchmarkRun_Rare_Sensitive_Regexp(b *testing.B) { benchRun(b, qLitRare, true) }
func BenchmarkRun_Rare_Sensitive_Fast(b *testing.B)   { benchRun(b, qLitRare, false) }

func BenchmarkRun_Rare_Insensitive_Regexp(b *testing.B) { benchRun(b, qLitRareInsens, true) }
func BenchmarkRun_Rare_Insensitive_Fast(b *testing.B)   { benchRun(b, qLitRareInsens, false) }

// Control: a real regexp query takes the same path either way, so these two
// should be indistinguishable. If they are not, the dispatch itself costs.
func BenchmarkRun_Regex_Regexp(b *testing.B) { benchRun(b, qRegex, true) }
func BenchmarkRun_Regex_Fast(b *testing.B)   { benchRun(b, qRegex, false) }

// Matcher-level, isolated from the walk and the I/O.
func benchMatcher(b *testing.B, q Query, regexpPath bool) {
	lines := corpusLines(b, corpusRoot(b), 20000)
	var n int64
	for _, l := range lines {
		n += int64(len(l))
	}
	re, err := compile(q)
	if err != nil {
		b.Fatal(err)
	}
	var m matcher = regexMatcher{re}
	if !regexpPath {
		m = newMatcher(q, re)
	}
	b.SetBytes(n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			findVia(m, l)
		}
	}
}

func BenchmarkMatch_Sensitive_Regexp(b *testing.B)   { benchMatcher(b, qLitSensitive, true) }
func BenchmarkMatch_Sensitive_Fast(b *testing.B)     { benchMatcher(b, qLitSensitive, false) }
func BenchmarkMatch_Insensitive_Regexp(b *testing.B) { benchMatcher(b, qLitInsensitive, true) }
func BenchmarkMatch_Insensitive_Fast(b *testing.B)   { benchMatcher(b, qLitInsensitive, false) }

// The fold in isolation, against the naive byte loop it replaces.
func foldNaive(b []byte) bool {
	var hi byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		hi |= c
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return hi&0x80 == 0
}

func benchFold(b *testing.B, f func([]byte) bool) {
	lines := corpusLines(b, corpusRoot(b), 20000)
	var blob []byte
	for _, l := range lines {
		blob = append(blob, l...)
	}
	buf := make([]byte, len(blob))
	b.SetBytes(int64(len(blob)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf, blob)
		f(buf)
	}
}

func BenchmarkFold_Naive(b *testing.B) { benchFold(b, foldNaive) }
func BenchmarkFold_SWAR(b *testing.B)  { benchFold(b, foldLower) }
func BenchmarkFold_Stdlib(b *testing.B) {
	lines := corpusLines(b, corpusRoot(b), 20000)
	var blob []byte
	for _, l := range lines {
		blob = append(blob, l...)
	}
	b.SetBytes(int64(len(blob)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.ToLower(string(blob))
	}
}

var _ = regexp.MustCompile
