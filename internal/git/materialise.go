package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Projection is the content to materialise: workspace-relative paths mapped to
// the bytes the tree should hold. It is plain data -- the app's Project(policy)
// or an intention selection -- never a live handle.
type Projection map[string][]byte

// MaterialiseOptions controls what a materialisation includes.
type MaterialiseOptions struct {
	// IncludeIgnored copies gitignored paths into the scratch tree. An ignored
	// dependency is a build input, so it must be present on purpose rather
	// than by accident; off by default means it is absent on purpose.
	IncludeIgnored bool
	// Tree additionally writes a git tree object for the materialised content
	// and records its id in Materialised.Tree. The object is inert.
	Tree bool
	// Dir is the parent directory for the scratch tree; empty uses the system
	// temporary directory.
	Dir string
}

// Materialised is the result: the scratch directory, the paths written, and,
// when asked for, the tree object id.
type Materialised struct {
	Dir   string
	Tree  string
	Paths []string
}

// Remove deletes the scratch tree. A tree object written alongside it is
// dangling and reclaimable by git gc.
func (m *Materialised) Remove() error {
	if m == nil || m.Dir == "" {
		return nil
	}
	return os.RemoveAll(m.Dir)
}

// Materialise copies the workspace and overlays a projection into a scratch
// tree. It never touches the worktree or the user's index: it reads files and
// writes only under the scratch directory, and the tree object it may write is
// dangling. A reflink is attempted where the filesystem supports it, with a
// full copy as the fallback. Hardlinks are never used -- shared inodes would
// let a build in the scratch tree corrupt the source.
func (s *Service) Materialise(ctx context.Context, projection Projection, opts MaterialiseOptions) (*Materialised, error) {
	if _, err := s.repoRoot(ctx); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(opts.Dir, "raj-materialise-")
	if err != nil {
		return nil, err
	}
	m := &Materialised{Dir: scratch}
	fail := func(err error) (*Materialised, error) {
		_ = m.Remove()
		return nil, err
	}

	seed, err := s.seedPaths(ctx, opts.IncludeIgnored)
	if err != nil {
		return fail(err)
	}
	for _, rel := range seed {
		if _, overlaid := projection[rel]; overlaid {
			continue
		}
		src := filepath.Join(s.root, filepath.FromSlash(rel))
		info, err := os.Lstat(src)
		if err != nil {
			continue // a gitlink, or a path removed since it was listed
		}
		if !info.Mode().IsRegular() {
			continue
		}
		dst := filepath.Join(scratch, filepath.FromSlash(rel))
		if err := copyFile(dst, src, info.Mode()); err != nil {
			return fail(fmt.Errorf("materialise %s: %w", rel, err))
		}
	}
	for p, data := range projection {
		rel, err := s.projectionPath(p)
		if err != nil {
			return fail(err)
		}
		dst := filepath.Join(scratch, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fail(err)
		}
	}

	if opts.Tree {
		tree, err := s.treeFromDir(ctx, scratch)
		if err != nil {
			return fail(err)
		}
		m.Tree = tree
	}

	var paths []string
	err = filepath.WalkDir(scratch, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(scratch, p)
		if rerr != nil {
			return rerr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fail(err)
	}
	sort.Strings(paths)
	m.Paths = paths
	return m, nil
}

// seedPaths lists the workspace files a materialisation starts from: tracked
// files and untracked-but-not-ignored files, plus ignored files when the
// policy asks for them. The index is read, never written.
func (s *Service) seedPaths(ctx context.Context, includeIgnored bool) ([]string, error) {
	lists := [][]string{
		{"ls-files", "-z"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	}
	if includeIgnored {
		lists = append(lists, []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"})
	}
	seen := map[string]bool{}
	var paths []string
	for _, args := range lists {
		out, err := s.run(ctx, args...)
		if err != nil {
			return nil, err
		}
		for _, p := range splitZ(out) {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// splitZ splits NUL-terminated git output into records.
func splitZ(b []byte) []string {
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out
}

// projectionPath validates a projection key and returns it as a path relative
// to the workspace root. An absolute path under the root is accepted and made
// relative; one that escapes the root is refused.
func (s *Service) projectionPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("git: projection has an empty path")
	}
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) {
		rel, err := filepath.Rel(s.root, clean)
		if err != nil {
			return "", fmt.Errorf("git: projection path %q is not under %s", p, s.root)
		}
		clean = rel
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("git: projection path %q escapes the workspace", p)
	}
	return clean, nil
}

// copyFile copies src to dst. It tries a reflink first and falls back to a
// full copy; it never creates a hardlink, so the two files keep separate
// inodes. The destination's parent is created.
func copyFile(dst, src string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if err := reflink(out, in); err == nil {
		return out.Close()
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(mode.Perm()); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// treeFromDir writes a tree object for the files under dir, bottom-up, and
// returns the root tree's id. It is the mktree path of section 4: blobs are
// hashed first, then each directory's entries are mktree'd.
func (s *Service) treeFromDir(ctx context.Context, dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var tree []TreeEntry
	for _, de := range entries {
		name := de.Name()
		if name == ".git" {
			continue
		}
		full := filepath.Join(dir, name)
		info, err := de.Info()
		if err != nil {
			return "", err
		}
		switch {
		case info.IsDir():
			sub, err := s.treeFromDir(ctx, full)
			if err != nil {
				return "", err
			}
			tree = append(tree, TreeEntry{Mode: "040000", Type: "tree", Hash: sub, Name: name})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return "", err
			}
			sha, err := s.HashObject(ctx, []byte(target))
			if err != nil {
				return "", err
			}
			tree = append(tree, TreeEntry{Mode: "120000", Type: "blob", Hash: sha, Name: name})
		case info.Mode().IsRegular():
			data, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			sha, err := s.HashObject(ctx, data)
			if err != nil {
				return "", err
			}
			mode := "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			tree = append(tree, TreeEntry{Mode: mode, Type: "blob", Hash: sha, Name: name})
		}
	}
	return s.Mktree(ctx, tree)
}
