package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"raj/internal/git"
	"raj/internal/intent"
)

// defaultPublishHook is the single strategy's script, pinned by path and
// content hash at propose time (H5, WAVE-PLAN Track S).
const defaultPublishHook = "examples/hooks/publish-single.sh"

// publishURL finds the pull-request URL in a hook's output; publishSHA finds
// the commit the hook reports it pushed ("committed <sha> on branch ...").
var publishURL = regexp.MustCompile(`https?://[^\s"']+`)
var publishSHA = regexp.MustCompile(`committed ([0-9a-f]{7,64}) on branch`)

// publishRunner runs a publish proposal's argv in dir with env appended,
// returning stdout, stderr and the exit code. A start failure is the error; a
// non-zero exit is data.
type publishRunner func(ctx context.Context, dir string, argv, env []string) (stdout, stderr string, code int, err error)

// intentPublish dispatches the publish subcommand: propose by default, run a
// pending proposal on --approve, retract it on --withdraw. It is control's
// `intent` op, so the proxy may read intent.Proposal over the existing wire.
func (a *App) intentPublish(ctx context.Context, svc *git.Service, cmd intent.Command) (intent.Result, error) {
	if a.state == nil {
		return intent.Result{}, errors.New("intent publish: the workspace store is not open")
	}
	switch {
	case cmd.Approve:
		return a.approvePublish(ctx, svc, cmd)
	case cmd.Withdraw:
		return a.withdrawPublish(cmd)
	default:
		return a.proposePublish(ctx, svc, cmd)
	}
}

// proposePublish pins the exact outward step for a wave and records it as a
// pending proposal. It runs the pinned hook once with --dry-run and carries
// that output inline; nothing is pushed and no ref moves.
//
// The commit is the wave's S3 export (intent/land.go), and it is immutable: the
// artifact is pushed, never a commit rebuilt from the working tree. The base is
// the export's own parent, rec.BaseSHA, never a re-resolved branch name: with
// raj/baseline as the base a push "off main" would otherwise carry earlier
// waves. The hook takes --commit and --base by value and reads no disk state.
func (a *App) proposePublish(ctx context.Context, svc *git.Service, cmd intent.Command) (intent.Result, error) {
	name := cmd.Name
	if name == "" {
		return intent.Result{}, errors.New("intent publish: needs a wave name")
	}
	// The commit is the wave's S3 export: land writes it, and publish only
	// pushes a commit that already exists.
	rec, found, err := a.state.LastExport(name)
	if err != nil {
		return intent.Result{}, err
	}
	if !found {
		return intent.Result{}, fmt.Errorf("intent publish: %s has no export to publish; land the wave first", name)
	}
	if rec.CommitSHA == "" || rec.TreeSHA == "" || rec.BaseSHA == "" {
		return intent.Result{}, fmt.Errorf("intent publish: the export of %s is incomplete (commit %q tree %q base %q); re-export", name, rec.CommitSHA, rec.TreeSHA, rec.BaseSHA)
	}
	const remote = "origin"
	url, err := svc.RemoteURL(ctx, remote)
	if err != nil {
		return intent.Result{}, err
	}
	if url == "" {
		return intent.Result{}, fmt.Errorf("intent publish: remote %q has no URL", remote)
	}
	branch := intent.BranchForWave(name)
	if err := intent.CheckBranch(branch, rec.BaseSHA); err != nil {
		return intent.Result{}, err
	}
	root := a.visible.Primary()
	hash, err := intent.HashFile(filepath.Join(root, filepath.FromSlash(defaultPublishHook)))
	if err != nil {
		return intent.Result{}, fmt.Errorf("intent publish: hook %s: %w", defaultPublishHook, err)
	}
	pub := intent.Publish{
		Name: name, Owner: cmd.Owner, Author: authorID(cmd.Owner),
		Commit: rec.CommitSHA, BaseRef: rec.BaseSHA, BaseSHA: rec.BaseSHA,
		ExportTree: rec.TreeSHA, ExportID: rec.ID,
		Branch: branch, Remote: remote, RemoteURL: url,
		HookPath: defaultPublishHook, HookHash: hash,
		Argv: []string{"sh", defaultPublishHook,
			"--commit", rec.CommitSHA,
			"--base", rec.BaseSHA,
			"--branch", branch, "--remote", remote},
	}
	dryArgv := append(append([]string(nil), pub.Argv...), "--dry-run")
	stdout, stderr, code, err := a.runPublish(ctx, root, dryArgv, nil)
	if err != nil {
		return intent.Result{}, fmt.Errorf("intent publish: dry run: %w", err)
	}
	pub.DryRun = strings.TrimRight(stdout+stderr, "\n")
	if code != 0 {
		return intent.Result{}, fmt.Errorf("intent publish: dry run exited %d: %s", code, pub.DryRun)
	}
	if a.pendingPublishes == nil {
		a.pendingPublishes = make(map[string]intent.Publish)
	}
	a.pendingPublishes[name] = pub
	return intent.Result{Publish: &pub}, nil
}

// approvePublish is the human's accept. It re-checks every LOCAL pin, then
// re-runs the pinned hook: that pairing is the honest gate, because the pins
// describe what the human reviewed and the hook is the reviewed action. Any
// drift refuses and re-proposes; it never adapts.
//
// Two pins live outside git: the export row the proposal came from (ExportID)
// and the seam the current buffers would materialise (ExportTree). The app owns
// the store and the buffers, so it reads them here and hands them to CheckPins.
// A second export refuses, and so does an edit to one of the wave's own member
// groups. An unrelated buffer edit does not: the artifact is immutable, so only
// the pinned wave's own composition can invalidate it.
//
// The one pin it cannot re-check is remote-branch drift: whether the remote
// branch already holds a commit the push would overwrite is not visible without
// a fetch, and a host-side fetch at accept would itself be an un-reviewed
// outward step. The pinned hook owns that pin and refuses a non-fast-forward
// with its exit 12, so this side never force-pushes and never adds a
// remote-branch check of its own.
func (a *App) approvePublish(ctx context.Context, svc *git.Service, cmd intent.Command) (intent.Result, error) {
	name := cmd.Name
	pub, ok := a.pendingPublishes[name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent publish: no pending proposal for %q", name)
	}
	root := a.visible.Primary()
	// The export row is a pin: a later export replaces it, so the proposal no
	// longer describes what the human reviewed.
	rec, found, err := a.state.LastExport(name)
	if err != nil {
		return intent.Result{Publish: &pub}, err
	}
	var lastExportID int64
	if found {
		lastExportID = rec.ID
	}
	// The seam is re-materialised from the live buffers; waveProjection admits
	// only the wave's own member groups, so an unrelated edit leaves it alone.
	seam, err := a.waveProjection(a.waveMembers(name))
	if err != nil {
		delete(a.pendingPublishes, name)
		return intent.Result{Publish: &pub}, fmt.Errorf("intent publish: the seam changed since export; re-export and re-propose: %w", err)
	}
	if err := pub.CheckPins(ctx, svc, root, seam, lastExportID); err != nil {
		delete(a.pendingPublishes, name)
		return intent.Result{Publish: &pub}, err
	}
	// The hook is the pinned action. It points the branch at the exported commit
	// and pushes it; it no-ops when the branch already holds the commit, fast-
	// forwards only, and refuses everything else. The host adds no force and no
	// adaptation of its own.
	stdout, stderr, code, err := a.runPublish(ctx, root, pub.Argv, nil)
	if err != nil {
		return intent.Result{Publish: &pub}, fmt.Errorf("intent publish: %w", err)
	}
	pub.ExitCode = code
	pub.RemoteRef = "refs/heads/" + pub.Branch
	pub.Stderr = tailText(stderr, 20)
	if code == 0 {
		pub.Decided = "published"
		pub.Pushed = firstSHA(stdout)
		pub.URL = firstURL(stdout + "\n" + stderr)
		delete(a.pendingPublishes, name)
	} else {
		pub.Decided = "refused"
	}
	return intent.Result{Publish: &pub}, nil
}

// withdrawPublish retracts a pending publish proposal. Nothing outward ran, so
// there is nothing to undo.
func (a *App) withdrawPublish(cmd intent.Command) (intent.Result, error) {
	name := cmd.Name
	pub, ok := a.pendingPublishes[name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent publish: no pending proposal for %q", name)
	}
	delete(a.pendingPublishes, name)
	pub.Decided = "withdrawn"
	return intent.Result{Publish: &pub}, nil
}

// runPublish runs argv through the injected runner, or the real one.
func (a *App) runPublish(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
	run := a.publishRun
	if run == nil {
		run = execPublish
	}
	return run(ctx, dir, argv, env)
}

// execPublish is the production runner. A non-zero exit is returned as code
// with a nil error; only a failure to start is an error.
func execPublish(ctx context.Context, dir string, argv, env []string) (string, string, int, error) {
	if len(argv) == 0 {
		return "", "", -1, errors.New("publish: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), errb.String(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	return out.String(), errb.String(), -1, err
}

// authorID parses the server-stamped owner ("<author id>") back to an id.
func authorID(owner string) uint8 {
	n, err := strconv.ParseUint(owner, 10, 8)
	if err != nil {
		return 0
	}
	return uint8(n)
}

// firstURL returns the first http(s) URL in s, or "".
func firstURL(s string) string { return publishURL.FindString(s) }

// firstSHA returns the commit sha a publish hook reported it pushed, or "".
func firstSHA(s string) string {
	if m := publishSHA.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

// tailText keeps the last n lines of s, so a refusal carries the tail of the
// hook's stderr rather than its whole log.
func tailText(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
