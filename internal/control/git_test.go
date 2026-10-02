package control

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/git"
)

// controlGitRepo makes a real repository for the Dispatch tests, so the git
// verb drives git rather than a fake.
func controlGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@localhost",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "a.go")
	runGit("commit", "-q", "--no-gpg-sign", "-m", "first")
	return dir
}

// gitJSON marshals a real git.Result into the canned answer the fake editor
// returns, so the CLI fixture is the type it claims to carry rather than a
// hand-copied string that can drift from the struct.
func gitJSON(t *testing.T, r git.Result) string {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestDispatchGitStatusReadsTheWorkspace drives the real entry path: Dispatch
// with a guard over a temp repo, so the git verb runs git host-side.
func TestDispatchGitStatusReadsTheWorkspace(t *testing.T) {
	repo := controlGitRepo(t)
	g := NewGuard(newMemHost(repo, nil))
	res := Dispatch(g, Request{Op: "git", GitMode: "status"})
	if !res.OK {
		t.Fatalf("git status = %+v", res)
	}
	var got git.Result
	if err := json.Unmarshal([]byte(res.GitJSON), &got); err != nil {
		t.Fatalf("GitJSON %q: %v", res.GitJSON, err)
	}
	if got.Mode != "status" || got.View == nil || got.View.Root == "" {
		t.Errorf("git status = %+v, want a view with a root", got)
	}
}

// gitUnavailableRunner stands in for a git that cannot start, so the refusal
// is testable through the control verb.
type gitUnavailableRunner struct{}

func (gitUnavailableRunner) Run(context.Context, string, []byte, ...string) ([]byte, error) {
	return nil, git.ErrGitUnavailable
}

func TestDispatchGitRefusalNamesTheReason(t *testing.T) {
	repo := t.TempDir()
	g := NewGuard(newMemHost(repo, nil))
	g.Git = git.NewWithRunner(repo, gitUnavailableRunner{})
	res := Dispatch(g, Request{Op: "git", GitMode: "status"})
	if res.OK || !strings.Contains(res.Err, "git is not available") {
		t.Errorf("git status = %+v, want the unavailable refusal", res)
	}
}

func TestDispatchGitUnknownMode(t *testing.T) {
	repo := controlGitRepo(t)
	g := NewGuard(newMemHost(repo, nil))
	res := Dispatch(g, Request{Op: "git", GitMode: "bogus"})
	if res.OK || !strings.Contains(res.Err, "unknown mode") {
		t.Errorf("git bogus = %+v, want a mode refusal", res)
	}
}

func TestCLIGitStatusJSON(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.gitJSON = gitJSON(t, git.Result{Mode: "status", View: &git.View{
		Root: "/w", Head: "abc", Branch: "main",
		Entries: []git.StatusEntry{{Path: "a.go", Index: "M", Worktree: " "}},
	}})
	out, errs, code := run(t, "git", "status", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, `"mode":"status"`) {
		t.Errorf("git status -json = %q", out)
	}
	if ed.lastGit.GitMode != "status" {
		t.Errorf("the request mode = %q, want status", ed.lastGit.GitMode)
	}
}

func TestCLIGitPlainStatus(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.gitJSON = gitJSON(t, git.Result{Mode: "status", View: &git.View{
		Root: "/w", Head: "abc", Branch: "main",
		Entries: []git.StatusEntry{{Path: "a.go", Index: "M", Worktree: " "}},
	}})
	out, errs, code := run(t, "git", "status")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "root\t/w") || !strings.Contains(out, "a.go") {
		t.Errorf("git status = %q", out)
	}
}

// A git log -count reaches the git service through the CLI path: the flag
// parses into the request's GitCount and the service sees it, so the verb lists
// that many commits rather than git's default. Without the field on the wire
// the count is dropped and the service reads it as zero.
func TestCLIGitLogCountReachesTheService(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.gitJSON = gitJSON(t, git.Result{Mode: "log"})
	out, errs, code := run(t, "git", "log", "-count", "2", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if ed.lastGit.GitMode != "log" {
		t.Errorf("mode = %q, want log", ed.lastGit.GitMode)
	}
	if ed.lastGit.GitCount != 2 {
		t.Errorf("count = %d, want 2", ed.lastGit.GitCount)
	}
	if !strings.Contains(out, `"mode":"log"`) {
		t.Errorf("git log -json = %q", out)
	}
}

func TestCLIGitUnknownMode(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	_, errs, code := run(t, "git", "bogus")
	if code != 2 || !strings.Contains(errs, "want status") {
		t.Errorf("git bogus: code %d err %q", code, errs)
	}
}

// A git query runs on the connection goroutine, not the event thread. The fake
// event loop holds its git handler on gitHold; with the query off-thread the
// loop is free to answer a ping on another connection. On the old path the
// query was handled by the event thread, so the ping queued behind the held
// query and timed out.
func TestGitRunsOffTheEventThread(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.gitJSON = gitJSON(t, git.Result{Mode: "status"})
	ed.gitStarted = make(chan struct{})
	ed.gitHold = make(chan struct{})

	first, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	gitDone := make(chan Response, 1)
	go func() {
		res, err := first.Do(Request{Op: "git", GitMode: "status"})
		if err != nil {
			res.Err = "transport: " + err.Error()
		}
		gitDone <- res
	}()
	select {
	case <-ed.gitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the git query never started")
	}

	// The event thread is free: a ping on a second connection is answered
	// while the git query is still held.
	second, err := Dial(ed.srv.Path())
	if err != nil {
		close(ed.gitHold)
		t.Fatal(err)
	}
	defer second.Close()
	pong := make(chan error, 1)
	go func() {
		_, err := second.Do(Request{Op: "ping"})
		pong <- err
	}()
	select {
	case err := <-pong:
		if err != nil {
			close(ed.gitHold)
			t.Fatalf("ping while git held: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(ed.gitHold)
		t.Fatal("the event thread was blocked by the held git query")
	}
	close(ed.gitHold)
	if res := <-gitDone; !res.OK || res.Err != "" {
		t.Fatalf("held git query = %+v, want OK", res)
	}
}

// A git query that never returns is cut off by the connection deadline, so a
// hung git cannot hold the connection forever. The gitQueryTimeout test seam
// keeps the test fast.
func TestGitQueryTimesOut(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.gitJSON = gitJSON(t, git.Result{Mode: "status"})
	ed.srv.gitQueryTimeout = 50 * time.Millisecond
	ed.gitHold = make(chan struct{}) // never closed: the query hangs

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.readTimeout = 2 * time.Second

	res, err := c.Do(Request{Op: "git", GitMode: "status"})
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	if res.OK || !strings.Contains(res.Err, "deadline") {
		t.Fatalf("timed-out git = %+v, want a deadline refusal", res)
	}
}
