// Package workspace is the editor's set of workspace roots: the directories a
// single raj instance was opened on, in caller-supplied order, with the first
// serving as the primary. This wave routes the editor's one root through it so
// the machinery that will eventually hold several does not have to be bolted on
// beside every reader; for one root the behaviour is exactly the old single
// string.
//
// A Roots is a value holding canonical absolute paths and nothing else. It
// performs no filesystem access, so constructing one cannot depend on whether a
// path exists or is a repository.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Roots is a set of workspace roots in caller-supplied order. The zero value is
// a workspace with no root: Primary is empty and Len is zero, the same state a
// "", no-workspace editor has always had.
type Roots struct {
	paths []string
}

// New canonicalizes each non-empty path to an absolute, cleaned path, drops
// exact duplicates keeping the first occurrence's position, and rejects a set
// in which one root is equal to or nested inside another. Empty inputs are
// ignored; a call with no non-empty path yields the zero Roots and no error.
//
// It is pure: there is no existence check and no walk-up to find a repository.
// A relative path is resolved against the process working directory, which is
// the same resolution StateDir has always done.
func New(paths ...string) (Roots, error) {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			// Abs only fails when the working directory cannot be read;
			// fall back to the cleaned path rather than dropping the root,
			// the way StateDir already does.
			abs = filepath.Clean(p)
		}
		if slices.Contains(out, abs) {
			continue
		}
		out = append(out, abs)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if inside(out[i], out[j]) {
				return Roots{}, fmt.Errorf("workspace: root %q is inside root %q", out[j], out[i])
			}
			if inside(out[j], out[i]) {
				return Roots{}, fmt.Errorf("workspace: root %q is inside root %q", out[i], out[j])
			}
		}
	}
	return Roots{paths: out}, nil
}

// Primary is the first root in supplied order, or "" when there is none. It is
// the root the editor was actually opened on.
func (r Roots) Primary() string {
	if len(r.paths) == 0 {
		return ""
	}
	return r.paths[0]
}

// All returns a copy of every root in supplied order, so a caller cannot mutate
// the Roots through the slice.
func (r Roots) All() []string {
	if len(r.paths) == 0 {
		return nil
	}
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

// Len is the number of roots.
func (r Roots) Len() int { return len(r.paths) }

// Names returns the base name of each root in supplied order.
func (r Roots) Names() []string {
	if len(r.paths) == 0 {
		return nil
	}
	out := make([]string, len(r.paths))
	for i, p := range r.paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// RootFor returns the one root equal to path or containing it. Roots are not
// nested, so at most one can match, and the comparison is component-wise
// through inside: /a/b is not read as inside /a/bc. An empty or relative path,
// or the zero Roots, has no containing root and yields "".
func (r Roots) RootFor(path string) string {
	if len(r.paths) == 0 || path == "" || !filepath.IsAbs(path) {
		return ""
	}
	abs := filepath.Clean(path)
	for _, root := range r.paths {
		if inside(root, abs) {
			return root
		}
	}
	return ""
}

// Contains reports whether path is equal to or inside any root. It is RootFor
// asked as a yes-or-no question, so the containment rule has one home: a
// relative or unresolvable path is not contained, and the zero Roots contains
// nothing.
func (r Roots) Contains(path string) bool {
	return r.RootFor(path) != ""
}

// StateKey is the workspace's state-directory name: "raj-", a slug of the
// lowest-sorted root's base name for readability, and eight hex digits of the
// SHA-256 of the sorted cleaned roots joined with "\n" for uniqueness.
//
// For one root this is byte-for-byte the key session.StateDir has always
// produced: the same slug of the same cleaned absolute path and the same digest
// of that path. For several it is deterministic and independent of the order
// the caller supplied, because the roots are sorted first.
func StateKey(roots []string) string {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		clean = append(clean, filepath.Clean(r))
	}
	sorted := append([]string(nil), clean...)
	sort.Strings(sorted)
	base := ""
	if len(sorted) > 0 {
		base = sorted[0]
	}
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return "raj-" + slug(filepath.Base(base)) + "-" + hex.EncodeToString(sum[:4])
}

// inside reports whether path is dir itself or a descendant of dir, using
// filepath.Rel so the answer is component-wise rather than a string prefix:
// /a/b is inside /a but /a/bc is not.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// slug keeps the base name's letters and digits, lowercased, and folds every
// other run to a single dash. An empty or all-punctuation name becomes
// "workspace" so the key is never just the digest.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "workspace"
	}
	return out
}
