package git

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// EnvRunner is a Runner that can run git with extra environment entries. The
// base Runner interface stays environment-free so every existing fake keeps
// compiling; the object plumbing that pins a commit date discovers this
// interface at run time and falls back to the plain Run when a runner does not
// implement it.
type EnvRunner interface {
	RunEnv(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error)
}

// runEnv runs git with env appended to the process environment. A runner that
// does not know RunEnv runs without the extra entries.
func (s *Service) runEnv(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
	if er, ok := s.runner.(EnvRunner); ok {
		return er.RunEnv(ctx, s.root, env, stdin, args...)
	}
	return s.runner.Run(ctx, s.root, stdin, args...)
}

// RevParse resolves ref to the full commit id it names. It is a read: the ref
// is looked up, never moved.
func (s *Service) RevParse(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("git: rev-parse needs a ref")
	}
	out, err := s.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitTreeAt writes a commit object for tree with the given parents and the
// author and committer date pinned to when. The identity is fixed, so a host
// with no git user configured still produces a deterministic commit: the same
// tree, parents, message and time give the same commit id. It is inert and
// moves no ref.
func (s *Service) CommitTreeAt(ctx context.Context, tree string, parents []string, message string, when time.Time) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)
	env := []string{
		"GIT_AUTHOR_NAME=raj", "GIT_AUTHOR_EMAIL=raj@localhost",
		"GIT_COMMITTER_NAME=raj", "GIT_COMMITTER_EMAIL=raj@localhost",
	}
	if !when.IsZero() {
		date := when.UTC().Format(time.RFC3339)
		env = append(env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	out, err := s.runEnv(ctx, env, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TreeFromProjection writes a tree object for base's tree with proj overlaid
// and returns its id. A nil value for a path removes it. It uses a temporary
// index (GIT_INDEX_FILE) and never touches the worktree or the user's index, so
// it is safe to call while the user is editing. The same base and proj always
// give the same tree id.
func (s *Service) TreeFromProjection(ctx context.Context, base string, proj Projection) (string, error) {
	if _, err := s.repoRoot(ctx); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "raj-index-")
	if err != nil {
		return "", fmt.Errorf("git: temp index: %w", err)
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name) // git creates the index itself
	defer os.Remove(name)
	env := []string{"GIT_INDEX_FILE=" + name}

	if base == "" {
		if _, err := s.runEnv(ctx, env, nil, "read-tree", "--empty"); err != nil {
			return "", err
		}
	} else {
		if _, err := s.runEnv(ctx, env, nil, "read-tree", base); err != nil {
			return "", err
		}
	}
	paths := make([]string, 0, len(proj))
	for p := range proj {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data := proj[p]
		if data == nil {
			if _, err := s.runEnv(ctx, env, nil, "update-index", "--force-remove", "--", p); err != nil {
				return "", err
			}
			continue
		}
		blob, err := s.HashObject(ctx, data)
		if err != nil {
			return "", err
		}
		if _, err := s.runEnv(ctx, env, nil, "update-index", "--add", "--cacheinfo",
			cacheInfo(s, ctx, env, p, blob)); err != nil {
			return "", err
		}
	}
	out, err := s.runEnv(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// cacheInfo renders the --cacheinfo argument for p, keeping the mode base's
// index already gave the path (an executable or symlink entry) and defaulting
// to a regular file for a path base did not hold.
func cacheInfo(s *Service, ctx context.Context, env []string, p, blob string) string {
	mode := "100644"
	if out, err := s.runEnv(ctx, env, nil, "ls-files", "-s", "--", p); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) > 0 {
			switch fields[0] {
			case "100644", "100755", "120000":
				mode = fields[0]
			}
		}
	}
	return mode + "," + blob + "," + p
}
