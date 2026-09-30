package intent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"raj/internal/git"
)

// Publish is one publish action proposal: the exact outward step a wave would
// take, pinned at propose time so a human's accept can only run what they
// reviewed. It is the `single` strategy (one commit, one branch, one MR; no
// stacks) and the same family as a pending deletion: proposed, shown in
// `proposals`, carried out only by a human accept.
//
// BaseSHA, ExportTree, ExportID, RemoteURL, Commit, HookPath, HookHash and
// Argv are pinned by value or content hash, so accept re-checks every one and
// refuses on any drift rather than adapting.
type Publish struct {
	Name       string   `json:"name"`
	Owner      string   `json:"owner,omitempty"`
	Author     uint8    `json:"author"`
	Commit     string   `json:"commit"`
	Pushed     string   `json:"pushed,omitempty"`
	BaseRef    string   `json:"base_ref"`
	BaseSHA    string   `json:"base_sha"`
	ExportTree string   `json:"export_tree"`
	ExportID   int64    `json:"export_id"`
	Branch     string   `json:"branch"`
	Remote     string   `json:"remote"`
	RemoteURL  string   `json:"remote_url"`
	HookPath   string   `json:"hook_path"`
	HookHash   string   `json:"hook_hash"`
	Argv       []string `json:"argv"`
	DryRun     string   `json:"dry_run,omitempty"`
	ExitCode   int      `json:"exit_code,omitempty"`
	URL        string   `json:"url,omitempty"`
	RemoteRef  string   `json:"remote_ref,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	Decided    string   `json:"decided,omitempty"`
}

// BranchForWave names the one branch a wave publishes to: raj/wave-<slug>.
func BranchForWave(name string) string { return "raj/wave-" + slug(name) }

// CheckBranch refuses a branch that is unsafe to publish. The base branch and
// the protected trunk names are the hard rules; the caller adds the
// checked-out-branch check, which needs git. The base may be given as
// origin/main or main; both refuse the name main.
func CheckBranch(branch, baseRef string) error {
	if branch == "" {
		return errors.New("publish: needs a branch name")
	}
	base := baseRef
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if branch == base || branch == "main" || branch == "master" {
		return fmt.Errorf("publish: refusing branch %q: it is the base or a protected trunk name", branch)
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.ContainsAny(branch, " \t\n~^:?*[\\") {
		return fmt.Errorf("publish: refusing branch %q: unsafe name", branch)
	}
	return nil
}

// HashFile returns the hex sha256 of path. It is the pin that catches a hook
// edited between propose and accept.
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CheckPins re-checks every local pin against the workspace and git and returns
// the first drift as an error. Accept refuses on any error; it never adapts a
// pin to what it finds.
//
// seam is the projection the current workspace would export for the pinned
// wave, built by the caller (the app owns the buffers); lastExportID is the id
// of the wave's most recent export row, or 0 when there is none. Both are pins
// the same way the commit and the hook are: the artifact is immutable, so
// accept refuses when the seam no longer re-materialises ExportTree, or a newer
// export has replaced the pinned row, rather than re-exporting or re-landing
// behind the human's back.
//
// Every pin here is LOCAL by construction: a commit, a ref, a hook file and a
// remote URL, all readable without touching the network. Remote-branch drift -
// whether the remote branch already holds a commit the push would overwrite -
// is the one pin the host cannot re-check without a fetch. This function
// deliberately does not check it (a fetch at accept would itself be an
// un-reviewed outward step) and never forces. The pinned hook owns that pin:
// it fast-forwards only and refuses a non-fast-forward with its exit 12. Do
// not add a host-side remote-branch check here.
func (p Publish) CheckPins(ctx context.Context, svc *git.Service, root string, seam Projection, lastExportID int64) error {
	if p.Name == "" {
		return errors.New("publish: the proposal has no wave name")
	}
	if err := CheckBranch(p.Branch, p.BaseRef); err != nil {
		return err
	}
	if p.Commit == "" {
		return errors.New("publish: the proposal has no commit")
	}
	got, err := svc.RevParse(ctx, p.Commit)
	if err != nil || got != p.Commit {
		return fmt.Errorf("publish: the exported commit %s is gone; re-propose", p.Commit)
	}
	if p.BaseSHA == "" {
		return errors.New("publish: the proposal has no pinned base; re-propose")
	}
	if _, err := svc.RevParse(ctx, p.BaseSHA); err != nil {
		return fmt.Errorf("publish: base %s: %w", p.BaseSHA, err)
	}
	if p.ExportTree == "" {
		return errors.New("publish: the proposal has no pinned export tree; re-propose")
	}
	// The artifact's tree is the export's tree. A commit whose tree has moved is
	// drift, not an update.
	patch, _, err := svc.DiffTrees(ctx, p.Commit, p.ExportTree)
	if err != nil {
		return fmt.Errorf("publish: cannot read the artifact tree %s: %w", p.ExportTree, err)
	}
	if patch != "" {
		return fmt.Errorf("publish: the exported commit %s no longer holds the pinned tree %s; re-propose", p.Commit, p.ExportTree)
	}
	// The export row is a pin: a later export of the same wave replaces it, so
	// the proposal no longer describes what the human reviewed.
	if lastExportID != p.ExportID {
		return fmt.Errorf("publish: %s has a newer export than the pinned row %d; re-propose", p.Name, p.ExportID)
	}
	// Re-materialise the seam from the live buffers: if it no longer gives the
	// exported tree, the workspace moved since the export. Refuse rather than
	// re-export; the artifact is immutable and only a fresh export replaces it.
	tree, err := Materialise(ctx, svc, p.BaseSHA, seam)
	if err != nil {
		return fmt.Errorf("publish: re-materialising the seam: %w", err)
	}
	if tree != p.ExportTree {
		return errors.New("publish: the seam changed since export; re-export and re-propose")
	}
	url, err := svc.RemoteURL(ctx, p.Remote)
	if err != nil {
		return fmt.Errorf("publish: remote %s: %w", p.Remote, err)
	}
	if url != p.RemoteURL {
		return fmt.Errorf("publish: remote %s URL changed from pinned %s to %s; re-propose", p.Remote, p.RemoteURL, url)
	}
	if p.HookPath != "" {
		hash, err := HashFile(filepath.Join(root, filepath.FromSlash(p.HookPath)))
		if err != nil {
			return fmt.Errorf("publish: hook %s: %w", p.HookPath, err)
		}
		if hash != p.HookHash {
			return fmt.Errorf("publish: hook %s changed since the proposal was pinned; re-propose", p.HookPath)
		}
	}
	return nil
}

// slug reduces a wave name to the characters one branch segment allows, so
// "task-1" and "H5: publish" name stable branches.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "wave"
	}
	return s
}
