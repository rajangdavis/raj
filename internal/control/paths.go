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

// RootMapEnv is `local=editor`: the path this process sees, then the path the
// editor sees. Explicit, and it wins over anything inferred.
const RootMapEnv = "RAJ_ROOT_MAP"

// Mapper rewrites absolute paths between two views of one tree. The zero value
// rewrites nothing, which is the shared-filesystem case and the common one.
type Mapper struct {
	Local  string // this process's view, e.g. /workspace
	Editor string // the editor's view, e.g. /Users/rajan/src/raj
}

// Active reports whether this mapper does anything.
func (m Mapper) Active() bool {
	return m.Local != "" && m.Editor != "" && m.Local != m.Editor
}

func (m Mapper) String() string { return m.Local + "=" + m.Editor }

// ToEditor rewrites a path this process can see into the editor's coordinates.
// A relative or empty path is left alone: the editor resolves those itself, and
// "" specifically means "the buffer the user is looking at".
func (m Mapper) ToEditor(p string) string { return rebase(p, m.Local, m.Editor, m.Active()) }

// FromEditor rewrites a path the editor reported back into this process's.
func (m Mapper) FromEditor(p string) string { return rebase(p, m.Editor, m.Local, m.Active()) }

func rebase(p, from, to string, active bool) string {
	if !active || p == "" || !filepath.IsAbs(p) {
		return p
	}
	if p == from {
		return to
	}
	if rest, ok := strings.CutPrefix(p, ensureSlash(from)); ok {
		return filepath.Join(to, rest)
	}
	// Outside the mapped tree. Left alone rather than guessed at: the editor
	// refuses paths outside its root anyway, so a wrong rewrite would turn a
	// clear "not under the workspace" into a confusing one.
	return p
}

func ensureSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// MapperFromEnv reads RAJ_ROOT_MAP. A malformed value is an error rather than
// an ignored setting: silently not mapping is the failure that looks like the
// editor having the wrong files open.
func MapperFromEnv() (Mapper, error) {
	v := os.Getenv(RootMapEnv)
	if v == "" {
		return Mapper{}, nil
	}
	local, editor, ok := strings.Cut(v, "=")
	if !ok || local == "" || editor == "" {
		return Mapper{}, fmt.Errorf("%s must be local=editor, e.g. /workspace=/Users/you/src/proj; got %q",
			RootMapEnv, v)
	}
	if !filepath.IsAbs(local) || !filepath.IsAbs(editor) {
		return Mapper{}, fmt.Errorf("%s wants two absolute paths; got %q", RootMapEnv, v)
	}
	return Mapper{Local: filepath.Clean(local), Editor: filepath.Clean(editor)}, nil
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

// inferMapper decides whether paths need translating.
//
// editorRoot is what the editor reported. The test is whether it exists here:
// a path that resolves on this filesystem is one both ends mean the same thing
// by, and anything else is a boundary. Deliberately not "are the strings
// different" — an agent run from a parent directory sees a different root and
// shares a filesystem, and rewriting there would corrupt every path it sends.
func inferMapper(cwd, editorRoot string) Mapper {
	if editorRoot == "" || cwd == "" {
		return Mapper{}
	}
	if _, err := os.Stat(editorRoot); err == nil {
		return Mapper{}
	}
	local := WorkspaceRoot(cwd)
	if local == "" || local == editorRoot {
		return Mapper{}
	}
	return Mapper{Local: local, Editor: editorRoot}
}
