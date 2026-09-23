package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path translation across a container boundary.
//
// The protocol addresses files by absolute path, which is unambiguous right up
// until the two ends have different filesystems. A repository bind-mounted into
// a container at /workspace is /Users/rajan/src/raj to the editor holding it
// open, and an agent that asks for the path it can see is asking for a file the
// editor has never heard of. The refusal is correct and completely unhelpful:
// "no open buffer for /workspace/main.go" is true of an editor that has
// /Users/rajan/src/raj/main.go open in front of the user.
//
// So `raj ctl` rewrites paths at the transport edge — outbound into the
// editor's coordinates, inbound back into the caller's — and everything above
// it goes on speaking in the paths it can see. `search` prints paths the agent
// can open with its own tools; `read` takes paths the agent got from its own
// tools; neither has to know a boundary was crossed.
//
// # Why the map is inferred and not required
//
// Making it a required flag would mean every invocation carries it, which means
// a wrapper script, which means one place for it to be wrong. Inferring it is a
// single question with an objective answer: does the editor's root exist on
// this filesystem? If it does, we share a filesystem and no rewriting is right
// — rewriting there is how `raj ctl read /Users/x/proj/a.go` run from a parent
// directory turns into a path with the project name in it twice. If it does
// not, the editor is somewhere this process cannot see, and the local
// workspace root is what stands in for it.
//
// RAJ_ROOT_MAP overrides both, for the mount layout that inference gets wrong.

// RootMapEnv is one or more `local=editor` pairs separated by commas: the path
// this process sees, then the path the editor sees. Explicit, and it wins over
// anything inferred.
const RootMapEnv = "RAJ_ROOT_MAP"

// Pair is one translation between two views of the same tree. Local is the path
// this process sees, Editor the path the editor sees; both are absolute roots.
type Pair struct {
	Local  string
	Editor string
}

// Mapper rewrites absolute paths between one or more pairs of views of a tree.
// The zero value rewrites nothing, which is the shared-filesystem case and the
// common one. A container with two bind mounts is two pairs: each path is
// rewritten by the pair whose from-side contains it, and a path outside every
// pair is left alone.
type Mapper struct {
	pairs []Pair
}

// NewMapper builds a mapper from pairs. A pair with an empty side, or with both
// sides equal, rewrites nothing and is dropped, so the zero value and a no-op
// mapping are the same thing. Roots are cleaned so a caller's spelling cannot
// make two spellings of one root look like two pairs.
func NewMapper(pairs ...Pair) Mapper {
	m := Mapper{}
	for _, p := range pairs {
		if p.Local == "" || p.Editor == "" || filepath.Clean(p.Local) == filepath.Clean(p.Editor) {
			continue
		}
		m.pairs = append(m.pairs, Pair{Local: filepath.Clean(p.Local), Editor: filepath.Clean(p.Editor)})
	}
	return m
}

// Active reports whether this mapper does anything.
func (m Mapper) Active() bool {
	for _, p := range m.pairs {
		if p.Local != "" && p.Editor != "" && p.Local != p.Editor {
			return true
		}
	}
	return false
}

// String renders the pairs the way RAJ_ROOT_MAP spells them, joined by commas.
func (m Mapper) String() string {
	parts := make([]string, 0, len(m.pairs))
	for _, p := range m.pairs {
		parts = append(parts, p.Local+"="+p.Editor)
	}
	return strings.Join(parts, ",")
}

// ToEditor rewrites a path this process can see into the editor's coordinates.
// The pair whose local side contains the path component-wise wins. A relative
// or empty path is left alone: the editor resolves those itself, and ""
// specifically means "the buffer the user is looking at".
func (m Mapper) ToEditor(p string) string { return m.rebase(p, true) }

// FromEditor rewrites a path the editor reported back into this process's.
func (m Mapper) FromEditor(p string) string { return m.rebase(p, false) }

// rebase applies the first pair whose from-side contains p; a path outside
// every pair is returned unchanged rather than guessed at.
func (m Mapper) rebase(p string, toEditor bool) string {
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	for _, pr := range m.pairs {
		from, to := pr.Local, pr.Editor
		if !toEditor {
			from, to = pr.Editor, pr.Local
		}
		if from == "" || to == "" || from == to {
			continue
		}
		if out, ok := rebaseOne(p, from, to); ok {
			return out
		}
	}
	// Outside every mapped tree. Left alone rather than guessed at: the editor
	// refuses paths outside its root anyway, so a wrong rewrite would turn a
	// clear "not under the workspace" into a confusing one.
	return p
}

// rebaseOne rewrites p from root from to root to, reporting whether p was under
// from. Containment is by path component, not string prefix, so /workspace/a
// does not swallow /workspace-other/a.
func rebaseOne(p, from, to string) (string, bool) {
	if p == from {
		return to, true
	}
	rest, ok := strings.CutPrefix(p, ensureSlash(from))
	if !ok {
		return p, false
	}
	return filepath.Join(to, rest), true
}

func ensureSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// MapperFromEnv reads RAJ_ROOT_MAP as one or more comma-separated `local=editor`
// pairs. Whitespace around a pair and around its `=` is tolerated. A malformed
// value is an error rather than an ignored setting: silently not mapping is the
// failure that looks like the editor having the wrong files open.
func MapperFromEnv() (Mapper, error) {
	v := os.Getenv(RootMapEnv)
	if v == "" {
		return Mapper{}, nil
	}
	var pairs []Pair
	for _, entry := range strings.Split(v, ",") {
		local, editor, ok := strings.Cut(entry, "=")
		local, editor = strings.TrimSpace(local), strings.TrimSpace(editor)
		if !ok || local == "" || editor == "" {
			return Mapper{}, fmt.Errorf("%s must be local=editor[,local=editor...], e.g. /workspace=/Users/you/src/proj; got %q",
				RootMapEnv, strings.TrimSpace(entry))
		}
		if !filepath.IsAbs(local) || !filepath.IsAbs(editor) {
			return Mapper{}, fmt.Errorf("%s wants absolute paths; got %q", RootMapEnv, strings.TrimSpace(entry))
		}
		pairs = append(pairs, Pair{Local: filepath.Clean(local), Editor: filepath.Clean(editor)})
	}
	return NewMapper(pairs...), nil
}

// WorkspaceRoot is the repository containing dir, or dir itself. It mirrors
// what raj itself does with its argument, so the root a container-side agent
// infers is the one the editor would have chosen for the same tree.
func WorkspaceRoot(dir string) string {
	if dir == "" {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return filepath.Clean(dir)
		}
		d = parent
	}
}

// inferMapper decides whether paths need translating for a daemon that named
// editorRoots as its workspace.
//
// The test is whether any editor root exists here: a path that resolves on this
// filesystem is one both ends mean the same thing by, and anything else is a
// boundary. Deliberately not "are the strings different" — an agent run from a
// parent directory sees a different root and shares a filesystem, and rewriting
// there would corrupt every path it sends.
//
// At a boundary only one mount can be inferred: the editor root that contains
// cwd, else the first, is paired with this process's workspace root, and every
// other editor root is left alone (an identity mapping). A second mount needs an
// explicit RAJ_ROOT_MAP entry naming both sides, because nothing on this side
// can tell which local directory stands for which editor root.
func inferMapper(cwd string, editorRoots []string) Mapper {
	if cwd == "" || len(editorRoots) == 0 {
		return Mapper{}
	}
	for _, r := range editorRoots {
		if r == "" {
			continue
		}
		if _, err := os.Stat(r); err == nil {
			return Mapper{}
		}
	}
	local := WorkspaceRoot(cwd)
	if local == "" {
		return Mapper{}
	}
	chosen := -1
	for i, r := range editorRoots {
		if r == "" {
			continue
		}
		if chosen < 0 {
			chosen = i
		}
		if pathWithin(r, cwd) {
			chosen = i
			break
		}
	}
	if chosen < 0 || editorRoots[chosen] == local {
		return Mapper{}
	}
	return NewMapper(Pair{Local: local, Editor: editorRoots[chosen]})
}

// pathWithin reports whether child is parent itself or sits under it, by path
// component rather than string prefix.
func pathWithin(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	parent, child = filepath.Clean(parent), filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
