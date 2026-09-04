// Package hidden decides which files and directories the tree, the search and
// the picker refuse to look at.
//
// It exists because that policy used to be three copies of
// `strings.HasPrefix(name, ".")`, one per package, which meant every dotfile in
// a repository was invisible to all three — `.gitlab-ci.yml`, `.github/`,
// `.eslintrc` — with no way to say otherwise. Hiding all dotfiles is a
// reasonable default only for `.git`; for everything else it hides files people
// edit.
//
// The rules are gitignore-shaped because that is the syntax a user of a code
// editor already knows, but deliberately smaller: no `**`, no per-directory
// files, no re-inclusion of a path under a hidden directory. A walk that skips
// a directory never reads it, so a rule that un-hides something inside a hidden
// directory cannot be honoured and is not pretended to be.
//
// A Rules value is immutable once built, so the search worker can read it off
// the event thread without a lock.
package hidden

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// File is the per-workspace configuration, relative to the workspace root.
const File = ".raj/hidden"

// defaults are the patterns applied before any configuration is read.
//
// Only two kinds of thing are here: version-control metadata, which is not
// source and is enormous, and dependency or cache directories that are
// machine-generated. Notably absent is the blanket dotfile rule: a dotfile at
// the root of a repository is usually configuration someone maintains by hand.
var defaults = []string{
	".git/",
	".hg/",
	".svn/",
	".DS_Store",
	"node_modules/",
	"vendor/",
	"__pycache__/",
	".venv/",
	".mypy_cache/",
	".pytest_cache/",
	".ruff_cache/",
	".tox/",
}

type rule struct {
	pat     string
	negate  bool // "!pat": un-hide, if no later rule re-hides
	dirOnly bool // "pat/": directories only
	rooted  bool // pattern contains a slash: match the whole relative path
	// plain is true when the pattern has no glob metacharacters, which is the
	// overwhelming majority of them — ".git", "node_modules", "vendor". Hidden
	// runs once per directory entry of every walk, so the multiplier is the
	// size of the repository, and a string comparison instead of a path.Match
	// is most of the difference. Measured: 279 ns/op down to 40 ns/op over the
	// default rules, zero allocations either way.
	plain bool
}

// Rules is an ordered list of patterns. The LAST one that matches decides, so a
// configuration file can un-hide something the defaults hide.
type Rules struct {
	rules []rule
	// Bad holds lines that are not valid patterns, in file order. They are
	// skipped rather than fatal — a typo in a config file should not stop the
	// editor opening — but they are kept so a caller can say so.
	Bad []string
	// Sources names the files that were read, for the same reason.
	Sources []string
}

// Default returns the built-in rules.
func Default() *Rules { return parse(strings.Join(defaults, "\n"), nil) }

// Parse reads patterns from text, one per line. Blank lines and lines starting
// with `#` are ignored. The defaults are applied first, so a file only has to
// state its differences from them.
func Parse(text string) *Rules { return parse(strings.Join(defaults, "\n")+"\n"+text, nil) }

// Load returns the defaults, then any user-level configuration, then the
// workspace's own — each appended, so the more specific file wins.
func Load(root string) *Rules {
	r := parse(strings.Join(defaults, "\n"), nil)
	for _, p := range configPaths(root) {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r = parse(string(data), r)
		r.Sources = append(r.Sources, p)
	}
	return r
}

// configPaths lists the files Load reads, least specific first.
func configPaths(root string) []string {
	var out []string
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		}
	}
	if dir != "" {
		out = append(out, filepath.Join(dir, "raj", "hidden"))
	}
	if root != "" {
		out = append(out, filepath.Join(root, filepath.FromSlash(File)))
	}
	return out
}

func parse(text string, onto *Rules) *Rules {
	r := &Rules{}
	if onto != nil {
		r.rules = append(r.rules, onto.rules...)
		r.Bad = append(r.Bad, onto.Bad...)
		r.Sources = append(r.Sources, onto.Sources...)
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ru := rule{pat: line}
		if strings.HasPrefix(ru.pat, "!") {
			ru.negate, ru.pat = true, ru.pat[1:]
		}
		ru.pat = strings.TrimPrefix(filepath.ToSlash(ru.pat), "./")
		if strings.HasSuffix(ru.pat, "/") {
			ru.dirOnly, ru.pat = true, strings.TrimSuffix(ru.pat, "/")
		}
		// A leading slash anchors the pattern at the workspace root, the same
		// as gitignore: "/target/" is the Rust build directory, not every
		// directory called target anywhere in the tree.
		if strings.HasPrefix(ru.pat, "/") {
			ru.rooted, ru.pat = true, strings.TrimPrefix(ru.pat, "/")
		}
		ru.rooted = ru.rooted || strings.Contains(ru.pat, "/")
		// Validate now rather than on every entry of every walk: a bad glob is
		// a property of the line, not of the path it is tested against.
		if ru.pat == "" {
			r.Bad = append(r.Bad, line)
			continue
		}
		if _, err := path.Match(ru.pat, "x"); err != nil {
			r.Bad = append(r.Bad, line)
			continue
		}
		ru.plain = !strings.ContainsAny(ru.pat, `*?[\`)
		r.rules = append(r.rules, ru)
	}
	return r
}

// Hidden reports whether ONE entry should be skipped. rel is that entry's path
// relative to the workspace root.
//
// It judges the entry, not its ancestors. A walk that skips a hidden directory
// never descends into it, so asking about a path underneath one does not arise
// there; a caller that has a path rather than a walk — an open buffer, say —
// must ask about each component, which is what search's eligible does. The
// alternative, re-testing every ancestor on every entry of every walk, pays for
// that case on the hot path that does not need it.
//
// The root is never hidden: hiding it would empty the editor.
func (r *Rules) Hidden(rel string, isDir bool) bool {
	if r == nil {
		return false
	}
	rel = strings.Trim(strings.TrimPrefix(filepath.ToSlash(rel), "./"), "/")
	if rel == "" || rel == "." {
		return false
	}
	base := path.Base(rel)
	out := false
	for _, ru := range r.rules {
		if ru.dirOnly && !isDir {
			continue
		}
		target := base
		if ru.rooted {
			target = rel
		}
		if ru.plain {
			if target == ru.pat {
				out = !ru.negate
			}
			continue
		}
		if ok, _ := path.Match(ru.pat, target); ok {
			out = !ru.negate
		}
	}
	return out
}

// HiddenPath is Hidden for a path that is not already relative to root.
func (r *Rules) HiddenPath(root, full string, isDir bool) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		rel = filepath.Base(full)
	}
	return r.Hidden(rel, isDir)
}

// Patterns returns the effective list, in order, for display.
func (r *Rules) Patterns() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.rules))
	for _, ru := range r.rules {
		s := ru.pat
		if ru.dirOnly {
			s += "/"
		}
		if ru.negate {
			s = "!" + s
		}
		out = append(out, s)
	}
	return out
}
