// Package git is the editor's read-only view of the repository its workspace
// lives in, plus the object plumbing that can materialise a projection of the
// journal as a scratch tree.
//
// The boundary is deliberate (docs/GIT-COMPATIBILITY.md section 1): raj owns
// the in-progress change graph, provenance and review state; git owns durable
// history. This package reads git's world and writes objects into it, but it
// never moves a branch, HEAD, a remote or the index. commit-tree is the
// ceiling for history, except Note, which updates the refs/notes/commits ref,
// and UpdateRefCAS, the one allowlisted local ref move (raj/baseline only);
// every other ref move and push is outward and belongs to a human hook.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The named refusals. A caller matches with errors.Is, so a refusal reads as
// itself and not as a message to parse.
var (
	// ErrGitUnavailable means no git could be started at all: the binary is
	// not on PATH.
	ErrGitUnavailable = errors.New("git: git is not available")
	// ErrNoRepository means git ran but the workspace is not inside a
	// repository with a working tree.
	ErrNoRepository = errors.New("git: not a repository")
	// ErrBareRepository means the workspace is a bare repository, which the
	// worktree-facing reads and materialisation cannot serve.
	ErrBareRepository = errors.New("git: bare repository")
)

// Runner runs git. It is injectable so the package is testable against a fake
// and still exercised against real git. Every call names the directory git
// runs in; stdin carries the bytes for the commands that read it
// (hash-object, mktree).
type Runner interface {
	Run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error)
}

// Service runs git against one workspace root.
type Service struct {
	root   string
	runner Runner
}

// New returns a Service over root using the real git binary.
func New(root string) *Service { return &Service{root: root, runner: execRunner{}} }

// NewWithRunner returns a Service using an injected runner. It is how a test
// drives the command shapes and the refusals without git installed.
func NewWithRunner(root string, runner Runner) *Service {
	return &Service{root: root, runner: runner}
}

// Root is the workspace root the service was built over. It is not necessarily
// the repository root, which View discovers with git.
func (s *Service) Root() string { return s.root }

// execRunner is the real git subprocess. One call is one process.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	return execRunner{}.RunEnv(ctx, dir, nil, stdin, args...)
}

// RunEnv is Run with env appended to the process environment, for the object
// plumbing that pins a commit's author and committer date.
func (execRunner) RunEnv(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrGitUnavailable, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// run runs git with no stdin.
func (s *Service) run(ctx context.Context, args ...string) ([]byte, error) {
	return s.runner.Run(ctx, s.root, nil, args...)
}

// StatusEntry is one worktree change from `git status --porcelain`: the path,
// the index and worktree codes (X and Y), and the original path for a rename or
// copy.
type StatusEntry struct {
	Path       string `json:"path"`
	Index      string `json:"index,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	RenameFrom string `json:"rename_from,omitempty"`
}

// View is the structured read of section 2: everything a caller would
// otherwise parse out of git's text output.
type View struct {
	Root      string        `json:"root"`
	Head      string        `json:"head,omitempty"`
	Branch    string        `json:"branch,omitempty"`
	Detached  bool          `json:"detached,omitempty"`
	Unborn    bool          `json:"unborn,omitempty"`
	Entries   []StatusEntry `json:"entries,omitempty"`
	Untracked []string      `json:"untracked,omitempty"`
	Ignored   []string      `json:"ignored,omitempty"`
	Operation string        `json:"operation,omitempty"`
}

// View reads the repository and the worktree state in one structured reply. It
// refuses by name when there is no repository, it is bare, or git is missing.
func (s *Service) View(ctx context.Context) (*View, error) {
	root, err := s.repoRoot(ctx)
	if err != nil {
		return nil, err
	}
	v := &View{Root: root}
	if head, err := s.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		v.Head = strings.TrimSpace(string(head))
	} else {
		v.Unborn = true
	}
	if branch, err := s.run(ctx, "symbolic-ref", "--short", "--quiet", "HEAD"); err == nil {
		v.Branch = strings.TrimSpace(string(branch))
	} else if !v.Unborn {
		v.Detached = true
	}
	out, err := s.run(ctx, "status", "--porcelain", "-z",
		"--untracked-files=all", "--ignored=matching")
	if err != nil {
		return nil, err
	}
	parseStatus(v, out)
	if gd, err := s.run(ctx, "rev-parse", "--git-dir"); err == nil {
		v.Operation = operationInProgress(root, strings.TrimSpace(string(gd)))
	}
	return v, nil
}

// StatusDigest is the hex sha256 of `git status --porcelain --untracked-files=all`. It is stable
// while the worktree is unchanged and moves the moment a tracked or untracked
// path does, which is what a materialisation records as its provenance.
func (s *Service) StatusDigest(ctx context.Context) (string, error) {
	out, err := s.run(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

// repoRoot resolves the repository's working-tree root, refusing by name when
// there is none or it is bare.
func (s *Service) repoRoot(ctx context.Context) (string, error) {
	out, err := s.run(ctx, "rev-parse", "--show-toplevel")
	if err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return root, nil
		}
	} else if errors.Is(err, ErrGitUnavailable) {
		return "", err
	}
	if _, derr := s.run(ctx, "rev-parse", "--git-dir"); derr != nil {
		if errors.Is(derr, ErrGitUnavailable) {
			return "", derr
		}
		return "", fmt.Errorf("%w: %s", ErrNoRepository, s.root)
	}
	if bare, berr := s.run(ctx, "rev-parse", "--is-bare-repository"); berr == nil &&
		strings.TrimSpace(string(bare)) == "true" {
		return "", fmt.Errorf("%w: %s", ErrBareRepository, s.root)
	}
	return "", fmt.Errorf("%w: %s", ErrNoRepository, s.root)
}

// parseStatus splits `git status --porcelain -z` into the view's three lists.
// A rename or copy consumes the next NUL field for its original path.
func parseStatus(v *View, out []byte) {
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 3 {
			continue
		}
		xy := string(f[:2])
		entry := StatusEntry{Index: xy[:1], Worktree: xy[1:], Path: string(f[3:])}
		if strings.ContainsAny(xy, "RC") && i+1 < len(fields) {
			entry.RenameFrom = string(fields[i+1])
			i++
		}
		switch xy {
		case "??":
			v.Untracked = append(v.Untracked, entry.Path)
		case "!!":
			v.Ignored = append(v.Ignored, entry.Path)
		default:
			v.Entries = append(v.Entries, entry)
		}
	}
}

// operationInProgress names the merge, rebase or other sequencer state a
// marker file in the git directory records.
func operationInProgress(root, gitDir string) string {
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	for _, op := range []struct{ marker, name string }{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, op.marker)); err == nil {
			return op.name
		}
	}
	return ""
}

// TreeEntry is one record of an mktree input: a mode, a type ("blob" or
// "tree"), the object's id, and its name within the tree.
type TreeEntry struct {
	Mode string
	Type string
	Hash string
	Name string
}

// HashObject writes bytes as a blob and returns its id. It is inert: the object
// dangles until something references it, and no ref moves.
func (s *Service) HashObject(ctx context.Context, data []byte) (string, error) {
	out, err := s.runner.Run(ctx, s.root, data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Mktree builds a tree object from entries. The caller supplies the entries'
// ids; mktree normalizes the order, so an unsorted slice is fine.
func (s *Service) Mktree(ctx context.Context, entries []TreeEntry) (string, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s %s %s\t%s", e.Mode, e.Type, e.Hash, e.Name)
		buf.WriteByte(0)
	}
	out, err := s.runner.Run(ctx, s.root, buf.Bytes(), "mktree", "-z")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitTree writes a commit object for tree with the given parents. It is the
// ceiling: the commit dangles until a human hook points a ref at it. The
// identity is pinned per call, so a host with no git user configured still
// produces a deterministic commit.
func (s *Service) CommitTree(ctx context.Context, tree string, parents []string, message string) (string, error) {
	args := []string{"-c", "user.name=raj", "-c", "user.email=raj@localhost",
		"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)
	out, err := s.runner.Run(ctx, s.root, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Note attaches provenance to an object with `git notes add`. It updates a ref
// -- git's own refs/notes/commits -- while HEAD and every branch stay
// untouched; the only other ref move this package offers is the allowlisted
// UpdateRefCAS.
func (s *Service) Note(ctx context.Context, object, message string) error {
	args := []string{"-c", "user.name=raj", "-c", "user.email=raj@localhost",
		"notes", "add", "-f", "-m", message, object}
	_, err := s.runner.Run(ctx, s.root, nil, args...)
	return err
}

// rel turns an absolute path under the workspace into a pathspec git accepts
// relative to the service root; a relative path is passed through.
func (s *Service) rel(path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		if r, err := filepath.Rel(s.root, path); err == nil {
			return r
		}
	}
	return path
}

// NumStatEntry is one file's churn from `git diff --numstat`.
type NumStatEntry struct {
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Path      string `json:"path"`
	Binary    bool   `json:"binary,omitempty"`
}

// LogEntry is one commit from `git log`.
type LogEntry struct {
	SHA     string `json:"sha"`
	Author  string `json:"author,omitempty"`
	Email   string `json:"email,omitempty"`
	When    string `json:"when,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// Query names one read-only git operation.
type Query struct {
	Mode  string
	Path  string
	Rev   string
	Count int
}

// Result is the structured answer one Query produces. Exactly one of the
// fields is populated, selected by Mode.
type Result struct {
	Mode    string         `json:"mode"`
	View    *View          `json:"view,omitempty"`
	Text    string         `json:"text,omitempty"`
	Numstat []NumStatEntry `json:"numstat,omitempty"`
	Log     []LogEntry     `json:"log,omitempty"`
}

// Call dispatches the read-only modes the `git` control verb exposes. None of
// them writes anything.
func (s *Service) Call(ctx context.Context, q Query) (*Result, error) {
	switch q.Mode {
	case "status":
		v, err := s.View(ctx)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: "status", View: v}, nil
	case "diff":
		text, err := s.Diff(ctx, q.Rev, q.Path)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: "diff", Text: text}, nil
	case "show":
		text, err := s.Show(ctx, q.Rev, q.Path)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: "show", Text: text}, nil
	case "numstat":
		ns, err := s.NumStat(ctx, q.Rev, q.Path)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: "numstat", Numstat: ns}, nil
	case "log":
		entries, err := s.Log(ctx, q.Count)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: "log", Log: entries}, nil
	case "":
		return nil, errors.New("git: needs a mode: status, diff, show, numstat or log")
	default:
		return nil, fmt.Errorf("git: unknown mode %q; want status, diff, show, numstat or log", q.Mode)
	}
}

// checkRev refuses a revision that begins with '-' before it can reach git's
// option parser. git treats any argument before `--` as an option, so a
// revision like `--output=/some/path` or `--version` is not a revision at all:
// `git diff --no-color --output=...` writes an arbitrary file and `git show
// --version` runs git's own flag handling. A revision is a name git resolves,
// never an option, so a leading dash is refused at the boundary.
func checkRev(rev string) error {
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("git: %q is not a revision: a revision may not begin with %q", rev, "-")
	}
	return nil
}

// Diff renders the change between rev (default HEAD) and the worktree as a
// unified patch. Reading it moves nothing.
func (s *Service) Diff(ctx context.Context, rev, path string) (string, error) {
	if err := checkRev(rev); err != nil {
		return "", err
	}
	if rev == "" {
		rev = "HEAD"
	}
	args := []string{"diff", "--no-color", rev}
	if p := s.rel(path); p != "" {
		args = append(args, "--", p)
	}
	out, err := s.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Show returns a committed blob or commit. With a path it is `git show
// REV:PATH` -- the committed text, no checkout; without one it is `git show
// REV`, HEAD by default.
func (s *Service) Show(ctx context.Context, rev, path string) (string, error) {
	if err := checkRev(rev); err != nil {
		return "", err
	}
	if rev == "" {
		rev = "HEAD"
	}
	args := []string{"show"}
	if p := s.rel(path); p != "" {
		args = append(args, rev+":"+p)
	} else {
		args = append(args, rev)
	}
	out, err := s.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// NumStat parses `git diff --numstat` into per-file churn.
func (s *Service) NumStat(ctx context.Context, rev, path string) ([]NumStatEntry, error) {
	if err := checkRev(rev); err != nil {
		return nil, err
	}
	if rev == "" {
		rev = "HEAD"
	}
	args := []string{"diff", "--numstat", rev}
	if p := s.rel(path); p != "" {
		args = append(args, "--", p)
	}
	out, err := s.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseNumStat(out), nil
}

// DiffTrees renders the change between two tree-ish objects -- a commit, a
// tree object or the empty tree -- as a unified patch and a per-file churn
// summary, read entirely from git's object database. It reads no worktree and
// touches no index, so the diff describes exactly the two trees and not
// whatever the workspace happens to hold. Both arguments are required: a
// missing tree-ish would silently diff HEAD, which is the wrong answer.
func (s *Service) DiffTrees(ctx context.Context, base, tree string) (string, []NumStatEntry, error) {
	if base == "" || tree == "" {
		return "", nil, errors.New("git: diff trees needs two tree-ish arguments")
	}
	patch, err := s.run(ctx, "diff", "--no-color", base, tree)
	if err != nil {
		return "", nil, err
	}
	stat, err := s.run(ctx, "diff", "--numstat", base, tree)
	if err != nil {
		return "", nil, err
	}
	return string(patch), parseNumStat(stat), nil
}

// DiffTreesPath is DiffTrees restricted to one path: the same object-only read
// with a pathspec, so a caller showing one file's slice of a change does not
// have to parse the whole patch to get it. It is the existing diff engine with
// a path filter, not a second one; base and tree are required, as in DiffTrees.
func (s *Service) DiffTreesPath(ctx context.Context, base, tree, path string) (string, error) {
	if base == "" || tree == "" {
		return "", errors.New("git: diff trees needs two tree-ish arguments")
	}
	args := []string{"diff", "--no-color", base, tree}
	if p := s.rel(path); p != "" {
		args = append(args, "--", p)
	}
	out, err := s.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseNumStat reads `git diff --numstat` output into per-file churn. A binary
// file reports "-" for both counts and is marked Binary; a malformed line is
// skipped rather than guessed at.
func parseNumStat(out []byte) []NumStatEntry {
	var entries []NumStatEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		e := NumStatEntry{Path: parts[2]}
		if parts[0] == "-" || parts[1] == "-" {
			e.Binary = true
		} else {
			e.Additions, _ = strconv.Atoi(parts[0])
			e.Deletions, _ = strconv.Atoi(parts[1])
		}
		entries = append(entries, e)
	}
	return entries
}

// Log lists commits newest-first. A zero count is git's own default; a
// positive count caps the list.
func (s *Service) Log(ctx context.Context, count int) ([]LogEntry, error) {
	const us = "\x1f"
	args := []string{"log", "--format=%H" + us + "%an" + us + "%ae" + us + "%aI" + us + "%s"}
	if count > 0 {
		args = append(args, "-n", strconv.Itoa(count))
	}
	out, err := s.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, us, 5)
		e := LogEntry{}
		if len(f) > 0 {
			e.SHA = f[0]
		}
		if len(f) > 1 {
			e.Author = f[1]
		}
		if len(f) > 2 {
			e.Email = f[2]
		}
		if len(f) > 3 {
			e.When = f[3]
		}
		if len(f) > 4 {
			e.Subject = f[4]
		}
		entries = append(entries, e)
	}
	return entries, nil
}
