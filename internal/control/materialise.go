package control

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/git"
)

// ProjectionPolicy selects which composition a projection carries.
type ProjectionPolicy uint8

const (
	// ProjectionAccepted is the agreed composition: what save writes.
	ProjectionAccepted ProjectionPolicy = iota
	// ProjectionWithProposed is the edit view: accepted plus proposed runs.
	ProjectionWithProposed
	// ProjectionVerifying is the verification view: accepted plus proposed with
	// a proposed deletion-only set applied, so a gate sees exactly what
	// accepting every proposal would write. ProjectionWithProposed is the edit
	// view and defers such a set, keeping the bytes visible until the human
	// decides.
	ProjectionVerifying
)

// Provenance stamps what a materialisation was built from.
type Provenance struct {
	Head        string // HEAD sha; empty for an unborn branch
	DirtyDigest string // sha256 of `git status --porcelain`
}

// Projection snapshots the live composition under policy. Event thread only.
func (g *Guard) Projection(policy ProjectionPolicy) map[string][]byte {
	return g.Host.Projection(policy)
}

// materialiseWith builds a scratch tree from a projection and reports the git
// provenance it was built from. It is the shared body of Guard.Materialise and
// the projected exec path, so the two cannot drift: the worktree snapshot and
// the overlay are decided in one place. It touches no host state and may run
// off the event thread.
func materialiseWith(ctx context.Context, svc *git.Service, proj map[string][]byte, opts git.MaterialiseOptions) (*git.Materialised, Provenance, error) {
	view, err := svc.View(ctx)
	if err != nil {
		return nil, Provenance{}, err
	}
	dirty, err := svc.StatusDigest(ctx)
	if err != nil {
		return nil, Provenance{}, err
	}
	m, err := svc.Materialise(ctx, git.Projection(proj), opts)
	if err != nil {
		return nil, Provenance{}, err
	}
	return m, Provenance{Head: view.Head, DirtyDigest: dirty}, nil
}

// Materialise builds a scratch tree from a projection snapshot and reports the
// git provenance it was built from. It touches no host state, so it may run off
// the event thread; take the projection with Guard.Projection first.
func (g *Guard) Materialise(ctx context.Context, proj map[string][]byte, opts git.MaterialiseOptions) (*git.Materialised, Provenance, error) {
	svc := g.Git
	if svc == nil {
		svc = git.New(g.Root())
	}
	return materialiseWith(ctx, svc, proj, opts)
}

// firstOutsideRoot reports the lexically-first path in proj that is not
// under root, if any. The check is lexical (filepath.Rel plus a ".." prefix)
// and not a stat or symlink resolution: a projection path comes from an open
// buffer, and the point is to catch a path under a different workspace root
// before materialisation, not to validate that the path exists. Paths are
// sorted so the refusal names the same path on every run.
func firstOutsideRoot(proj map[string][]byte, root string) (string, bool) {
	paths := make([]string, 0, len(proj))
	for p := range proj {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, true
		}
	}
	return "", false
}

// projectionRevision returns a stable digest of a projection's content. A
// hook's per-revision cap keys on it, so the revision has to change exactly
// when the projected bytes do: the paths are sorted (map iteration is random),
// and each path and payload is length-prefixed, so no two distinct compositions
// can concatenate to the same byte stream. A hash collision is treated as the
// same revision, the approximation every content digest makes.
func projectionRevision(proj map[string][]byte) uint64 {
	paths := make([]string, 0, len(proj))
	for p := range proj {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var n [8]byte
	for _, p := range paths {
		binary.LittleEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
		binary.LittleEndian.PutUint64(n[:], uint64(len(proj[p])))
		h.Write(n[:])
		h.Write(proj[p])
	}
	return binary.LittleEndian.Uint64(h.Sum(nil))
}

// harnessNames are the build-definition files a gate build resolves, in GNU
// make's search order. They are the command that runs the sources, so a
// projected hook must take them from the accepted tree rather than from a
// proposal: a hook run exists to test a proposal, not to run one. See
// hookProjection.
var harnessNames = map[string]bool{
	"Makefile":    true,
	"makefile":    true,
	"GNUmakefile": true,
}

// harnessScripts are the workspace-relative paths the build harness keeps its
// executable parts in: the scripts/ directory whole (the dev harness; the
// brief named scripts/*.sh) and the one example hook a gate target runs by
// path -- make check's ignored-source target runs
// examples/hooks/no-ignored-source.sh. A path listed here is taken from the
// accepted tree, so a proposal cannot change what a gate executes.
var harnessScripts = []string{
	"scripts/",
	"examples/hooks/no-ignored-source.sh",
}

// isHarnessPath reports whether a projection path names part of the build
// harness. The comparison is workspace-relative and lexical, like
// firstOutsideRoot: an absolute path is made relative to root, and a path that
// escapes the root is not harness, so it is left proposed and the multi-root
// refusal can still see it.
func isHarnessPath(path, root string) bool {
	rel := path
	if filepath.IsAbs(rel) {
		if r, err := filepath.Rel(root, rel); err == nil {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	if harnessNames[rel] {
		return true
	}
	for _, prefix := range harnessScripts {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(rel, prefix) {
				return true
			}
			continue
		}
		if rel == prefix {
			return true
		}
	}
	return false
}

// pinHarness returns the overlay a projected hook runs against: the proposed
// bytes for the sources under test, and the accepted bytes for the build
// harness. A harness path whose accepted composition holds no bytes -- a file
// the proposal creates, or one no accepted change touches -- is dropped rather
// than pinned to the proposal, so the materialiser seeds the saved file from
// disk, which is the accepted answer when nothing has been accepted for it.
// Every non-harness path keeps its proposed bytes, including a proposed go.mod
// or go.sum, so a dependency change still verifies before save.
func pinHarness(proposed, accepted map[string][]byte, root string) map[string][]byte {
	out := make(map[string][]byte, len(proposed))
	for p, data := range proposed {
		if !isHarnessPath(p, root) {
			out[p] = data
			continue
		}
		if a, ok := accepted[p]; ok {
			out[p] = a
		}
	}
	return out
}

// hookProjection is the overlay a projected hook materialises: the
// verification composition for the sources under test, the accepted
// composition for the build harness. It is deliberately separate from
// Guard.Projection, which exec and the intentions use and which must keep
// seeing the proposal whole: only a hook run is a gate an agent can trigger
// against text the user has not accepted. Event thread only, like Projection.
func (g *Guard) hookProjection() map[string][]byte {
	return pinHarness(g.Projection(ProjectionVerifying), g.Projection(ProjectionAccepted), g.Root())
}
