// Package search finds text across the workspace.
package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Match is one hit.
type Match struct {
	Path string
	Line int    // 1-based
	Text string // the whole line, trimmed of trailing space
	Col  int    // byte offset of the match within Text
	Len  int
}

// Query describes a search.
type Query struct {
	Text    string
	Include string // comma-separated globs, e.g. "*.go,*.md"
	Exclude string
	Regex   bool
	Case    bool
	Word    bool
}

// Limits keep an interactive search bounded. A search box that hangs the editor
// on a large repository is worse than one that stops early and says so.
// The two caps answer different questions. MaxPerFile stops one file eating the
// budget: the walk is lexical, so a single early file with hundreds of hits
// used to spend the whole allowance before the rest of the tree was seen —
// measured, `te` reported 500 results in 6 files, all at the repository root
// and 200 of them from LICENSE alone, while the rarer `testing` reported 361 in
// 34 files simply because it never hit the cap. The file count therefore said
// more about walk order than about the query, and it was least informative
// exactly when the term was most common.
//
// MaxMatches is the hard stop that keeps the pane bounded, and is set high
// enough that with a per-file cap in front of it the walk usually finishes.
// Matching is around a thirtieth of a search's cost, so counting past the
// per-file cap to report a true total is close to free; it is the opening and
// reading that is expensive, and that happens either way.
const (
	MaxMatches  = 2000
	MaxPerFile  = 20
	MaxFileSize = 2 << 20
)

// Result is a completed search.
type Result struct {
	Matches []Match
	Files   int
	// Considered counts the files opened and scanned. It is the number to look
	// at when a search feels slow: a file skipped during enumeration costs
	// nothing, a file opened costs everything.
	Considered int
	Capped     bool

	// Counts is the true number of matches per file, which is not the number
	// reported: a file past MaxPerFile contributes twenty rows and its real
	// total here, so the header can say "20 of 214" rather than quietly
	// implying the file holds twenty.
	Counts map[string]int
	// Stopped is set when the search was abandoned before finishing, which
	// happens whenever the query changes while a walk is in progress.
	Stopped bool
	Err     error
}

// Total is how many matches the search actually found, which is at least
// len(Matches): files past MaxPerFile contribute more here than they do rows.
func (r Result) Total() int {
	if r.Counts == nil {
		return len(r.Matches)
	}
	n := 0
	for _, c := range r.Counts {
		n += c
	}
	return n
}

// Run searches root with no way to stop it. Prefer RunContext: an interactive
// search is abandoned far more often than it is read.
func Run(root string, q Query) Result { return RunContext(context.Background(), root, q) }

// RunContext searches root, stopping when ctx is cancelled.
//
// Cancellation is the difference between one walk and a pile of them. Every
// keystroke starts a new search, and without a way to stop the old one a slow
// repository accumulates a full concurrent walk per keystroke — each opening
// and reading every file, none of whose results anyone will look at. The cost
// lands on the disk and the garbage collector, which is why the symptom is a
// laggy editor rather than a slow search.
//
// It walks the tree itself rather than shelling out to ripgrep so raj has no
// runtime dependency.
func RunContext(ctx context.Context, root string, q Query) Result {
	var res Result
	if q.Text == "" {
		return res
	}
	re, err := compile(q)
	if err != nil {
		res.Err = err
		return res
	}
	m := matcher(regexMatcher{re})
	if !forceRegexp {
		m = newMatcher(q, re)
	}
	inc, exc := globs(q.Include), globs(q.Exclude)
	// One scan buffer for the whole walk. Allocating 64 KB per file made the
	// buffer, not the matching, the dominant cost of a search.
	buf := make([]byte, 0, 64*1024)

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !matches(name, inc, true) || matches(name, exc, false) {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > MaxFileSize {
			return nil
		}
		// One non-blocking check per file. At tens of nanoseconds against an
		// open and a read, this is free, and it bounds abandoned work at one
		// file rather than one repository.
		select {
		case <-ctx.Done():
			res.Stopped = true
			return filepath.SkipAll
		default:
		}
		if len(res.Matches) >= MaxMatches {
			res.Capped = true
			return filepath.SkipAll
		}
		res.Considered++
		if _, total := scan(path, m, &buf, &res); total > 0 {
			res.Files++
			if res.Counts == nil {
				res.Counts = map[string]int{}
			}
			res.Counts[path] = total
		}
		return nil
	})
	return res
}

// forceRegexp disables the literal fast path. It exists so the benchmarks can
// measure the fast path against the regexp path it replaced, in the same
// process and over the same corpus.
var forceRegexp = false

// compile turns a query into a regexp. Literal searches are quoted rather than
// matched by hand so that case-insensitivity and word boundaries work the same
// way in both modes.
func compile(q Query) (*regexp.Regexp, error) {
	pattern := q.Text
	if !q.Regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if q.Word {
		pattern = `\b` + pattern + `\b`
	}
	if !q.Case {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

// scan searches one file.
//
// The file is read whole into a reused buffer and swept with the matcher,
// rather than split into lines and matched line by line. Almost every file in a
// repository contains no match at all, and those files never need lines: on
// ghostty, splitting 574,639 lines cost 17 ms of a 62 ms search to locate zero
// matches. Line numbers are counted only across the span leading to a hit, so
// the cost is paid per match rather than per line.
//
// MaxFileSize bounds the buffer, so "read it all" is bounded too.
// scan reports how many matches it recorded and how many the file actually
// holds. The two differ once a file passes MaxPerFile, and the difference is
// the whole point: the pane can then say how much it is not showing.
func scan(path string, m matcher, buf *[]byte, res *Result) (found, total int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	data, err := readAll(f, buf)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return 0, 0 // unreadable, or a NUL byte somewhere: binary
	}

	hay, lineFallback := m.prepare(data)
	if lineFallback != nil {
		return scanLines(path, lineFallback, data, res)
	}

	line, lineStart, from := 1, 0, 0
	for from <= len(data) {
		start, end, ok := m.find(hay[from:])
		if !ok {
			break
		}
		start, end = from+start, from+end

		// Advance the line counter over everything skipped since the last hit.
		// Across a whole file this visits each newline once, which is what the
		// line-splitting loop did anyway — the difference is that a file with
		// no matches never enters it.
		for {
			nl := bytes.IndexByte(data[lineStart:start], '\n')
			if nl < 0 {
				break
			}
			line++
			lineStart += nl + 1
		}
		lineEnd := len(data)
		if nl := bytes.IndexByte(data[start:], '\n'); nl >= 0 {
			lineEnd = start + nl
		}

		total++
		if len(res.Matches) >= MaxMatches {
			res.Capped = true
			return found, total
		}
		if found < MaxPerFile {
			text := data[lineStart:lineEnd]
			res.Matches = append(res.Matches, Match{
				Path: path, Line: line,
				Text: strings.TrimRight(string(text), " \t"),
				Col:  start - lineStart, Len: end - start,
			})
			found++
		}

		// At most one match is reported per line, as before.
		if lineEnd >= len(data) {
			break
		}
		line++
		lineStart = lineEnd + 1
		from = lineStart
	}
	return found, total
}

// readAll fills buf with the contents of f, growing it if needed and handing
// the new backing array back to the caller so the next file reuses it.
func readAll(f *os.File, buf *[]byte) ([]byte, error) {
	b := (*buf)[:0]
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		n, err := f.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			*buf = b
			if err == io.EOF {
				return b, nil
			}
			return b, err
		}
		if len(b) > MaxFileSize {
			*buf = b
			return b, errTooBig
		}
	}
}

// scanLines handles the rare file the sweep cannot: one containing non-ASCII
// bytes under a case-insensitive query, where folding in place would change
// byte offsets. Only such files pay the per-line regexp cost, and only for
// themselves.
func scanLines(path string, m matcher, data []byte, res *Result) (found, total int) {
	for line, off := 1, 0; off <= len(data); line++ {
		end := len(data)
		if nl := bytes.IndexByte(data[off:], '\n'); nl >= 0 {
			end = off + nl
		}
		raw := data[off:end]
		if start, stop, ok := m.find(raw); ok {
			total++
			if len(res.Matches) >= MaxMatches {
				res.Capped = true
				return found, total
			}
			if found < MaxPerFile {
				res.Matches = append(res.Matches, Match{
					Path: path, Line: line,
					Text: strings.TrimRight(string(raw), " \t"),
					Col:  start, Len: stop - start,
				})
				found++
			}
		}
		if end >= len(data) {
			break
		}
		off = end + 1
	}
	return found, total
}

var errTooBig = errors.New("file exceeds MaxFileSize")

func globs(spec string) []string {
	var out []string
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matches tests a filename against a glob list. An empty list means "no
// constraint", which is why the caller passes what an empty list should mean.
//
// Patterns of the form *.ext — which is almost all of them in practice — are
// answered by comparing the extension rather than by running the glob matcher.
// filepath.Match costs more than the read it is meant to avoid: on ghostty,
// eight exclude patterns turned a 62 ms search into a 107 ms one.
func matches(name string, patterns []string, emptyResult bool) bool {
	if len(patterns) == 0 {
		return emptyResult
	}
	for _, p := range patterns {
		if ext, ok := plainExt(p); ok {
			if strings.EqualFold(filepath.Ext(name), ext) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// plainExt reports whether a pattern is exactly "*.something" with no further
// metacharacters, and if so the extension it selects.
func plainExt(p string) (string, bool) {
	if len(p) < 3 || p[0] != '*' || p[1] != '.' {
		return "", false
	}
	if strings.ContainsAny(p[2:], "*?[\\") {
		return "", false
	}
	return p[1:], true
}
