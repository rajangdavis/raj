package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// BaselineRef is the one local ref a landed wave may move. The export path
// writes objects and hands the commit here; every other ref, HEAD and every
// remote stay out of reach, so commit-on-land is local and allowlisted.
const BaselineRef = "raj/baseline"

// ErrRefNotAllowed refuses a ref write that is not the allowlisted baseline.
// A caller matches it with errors.Is, so a refusal reads as itself.
var ErrRefNotAllowed = errors.New("git: ref is not allowlisted")

// UpdateRefCAS moves ref from oldSHA to newSHA with a compare-and-swap: if the
// ref is not at oldSHA the write is refused rather than clobbering a concurrent
// writer. Only BaselineRef may move; any other ref is refused by name before
// git runs, so this can never become a general ref-write verb. An empty oldSHA
// means the ref must not exist yet (a create). It moves no HEAD, no worktree
// and no remote; the command travels through update-ref --stdin so it is
// object-format agnostic (SHA-1 and SHA-256 both work).
func (s *Service) UpdateRefCAS(ctx context.Context, ref, newSHA, oldSHA string) error {
	if ref != BaselineRef {
		return fmt.Errorf("%w: %q; only %s may move", ErrRefNotAllowed, ref, BaselineRef)
	}
	if newSHA == "" {
		return errors.New("git: update-ref needs a commit")
	}
	var cmd string
	if oldSHA == "" {
		cmd = fmt.Sprintf("create %s %s\n", ref, newSHA)
	} else {
		cmd = fmt.Sprintf("update %s %s %s\n", ref, newSHA, oldSHA)
	}
	_, err := s.runner.Run(ctx, s.root, []byte(cmd), "update-ref", "--stdin")
	return err
}

// Upstream returns the current branch's upstream ref name (for a local branch,
// its remote-tracking ref; for a remote branch, the remote and branch). It
// returns "" when HEAD is detached or the branch has no upstream, which is not
// an error: a workspace may have no remote. The ref is read, never moved.
func (s *Service) Upstream(ctx context.Context) (string, error) {
	out, err := s.run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		if errors.Is(err, ErrGitUnavailable) {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}
