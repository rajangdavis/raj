// Package search finds text across the workspace.
package search

import (
	"bufio"
	"bytes"
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
const (
	MaxMatches  = 500
	MaxFileSize = 2 << 20
)

// Result is a completed search.
type Result struct {
	Matches []Match
	Files   int
	Capped  bool
	Err     error
}

// Run searches root. It walks the tree itself rather than shelling out to
// ripgrep so raj has no runtime dependency, at the cost of being slower on very
// large repositories — acceptable while matches are capped.
func Run(root string, q Query) Result {
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
	buf := make([]byte, 64*1024)

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
		if len(res.Matches) >= MaxMatches {
			res.Capped = true
			return filepath.SkipAll
		}
		if n := scan(path, m, buf, &res); n > 0 {
			res.Files++
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

// scan searches one file, skipping anything that looks binary.
func scan(path string, m matcher, buf []byte, res *Result) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	found := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(buf, 1<<20)
	// sc.Bytes avoids a string allocation for every line in the repository;
	// only a line that actually matches is turned into one.
	for line := 1; sc.Scan(); line++ {
		raw := sc.Bytes()
		if bytes.IndexByte(raw, 0) >= 0 {
			return found // NUL byte: binary, stop reading
		}
		start, end, ok := m.find(raw)
		if !ok {
			continue
		}
		if len(res.Matches) >= MaxMatches {
			res.Capped = true
			return found
		}
		res.Matches = append(res.Matches, Match{
			Path: path, Line: line,
			Text: strings.TrimRight(string(raw), " \t"),
			Col:  start, Len: end - start,
		})
		found++
	}
	return found
}

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
func matches(name string, patterns []string, emptyResult bool) bool {
	if len(patterns) == 0 {
		return emptyResult
	}
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}
