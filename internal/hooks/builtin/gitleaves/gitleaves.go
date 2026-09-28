// Package gitleaves registers the git capability leaves: the builtin hook
// actions that wrap internal/git's readers and object writers.
//
// The read leaves -- git.status, git.diff, git.numstat, git.log, git.show --
// are AgentDefault and only read. The object leaves -- git.blob, git.tree,
// git.commit, git.note -- are HumanOnly: they write inert objects, and no
// agent gets one this wave.
//
// A builtin leaf runs in-process with its args and no working directory, so a
// git leaf resolves the workspace root from, in order, an explicit
// args["root"], a root carried on the context by builtin.WithRoot (how the
// dispatcher hands the run its workspace), and finally the process working
// directory.
// Registering this package at init is what makes its names resolvable by
// hooks.Parse, so a binary that parses hooks must import it.
package gitleaves

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"raj/internal/git"
	"raj/internal/hooks/builtin"
)

// rootOf resolves the workspace root a leaf acts on.
func rootOf(ctx context.Context, args map[string]any) (string, error) {
	if root := stringArg(args, "root"); root != "" {
		return root, nil
	}
	if root, ok := builtin.Root(ctx); ok {
		return root, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("git leaf: cannot find a workspace root: %w", err)
	}
	return wd, nil
}

// service builds the git reader/writer for the run's workspace.
func service(ctx context.Context, args map[string]any) (*git.Service, error) {
	root, err := rootOf(ctx, args)
	if err != nil {
		return nil, err
	}
	return git.New(root), nil
}

// fail turns a git refusal into the run's stderr and a nonzero exit status.
func fail(err error) (builtin.Result, error) {
	return builtin.Result{Output: err.Error() + "\n", Exit: 1}, err
}

// jsonOut renders a read leaf's structured answer.
func jsonOut(v any) (builtin.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: string(b) + "\n"}, nil
}

// stringArg reads a string arg, empty when absent or not a string.
func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// intArg reads a number arg; JSON decodes every number as float64.
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// statusLeaf is git.status: the structured View.
type statusLeaf struct{}

func (statusLeaf) Name() string { return "git.status" }

func (statusLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	svc, err := service(ctx, args)
	if err != nil {
		return fail(err)
	}
	v, err := svc.View(ctx)
	if err != nil {
		return fail(err)
	}
	return jsonOut(v)
}

// diffLeaf is git.diff: the worktree diff. An empty diff is success -- no
// output and exit zero, never an error, so a chain of steps can continue.
type diffLeaf struct{}

func (diffLeaf) Name() string { return "git.diff" }

func (diffLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	svc, err := service(ctx, args)
	if err != nil {
		return fail(err)
	}
	text, err := svc.Diff(ctx, stringArg(args, "rev"), stringArg(args, "path"))
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: text}, nil
}

// numstatLeaf is git.numstat: per-file churn as JSON. A binary file is an
// entry with Binary true, since git prints "-" for its counts.
type numstatLeaf struct{}

func (numstatLeaf) Name() string { return "git.numstat" }

func (numstatLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	svc, err := service(ctx, args)
	if err != nil {
		return fail(err)
	}
	ns, err := svc.NumStat(ctx, stringArg(args, "rev"), stringArg(args, "path"))
	if err != nil {
		return fail(err)
	}
	return jsonOut(ns)
}

// logLeaf is git.log: commits newest-first as JSON. args["count"] caps the
// list; zero asks for git's own default.
type logLeaf struct{}

func (logLeaf) Name() string { return "git.log" }

func (logLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	svc, err := service(ctx, args)
	if err != nil {
		return fail(err)
	}
	entries, err := svc.Log(ctx, intArg(args, "count"))
	if err != nil {
		return fail(err)
	}
	return jsonOut(entries)
}

// showLeaf is git.show: a committed blob or commit as text.
type showLeaf struct{}

func (showLeaf) Name() string { return "git.show" }

func (showLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	svc, err := service(ctx, args)
	if err != nil {
		return fail(err)
	}
	text, err := svc.Show(ctx, stringArg(args, "rev"), stringArg(args, "path"))
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: text}, nil
}

// blobLeaf is git.blob: bytes from args["data"], or the file at args["path"]
// relative to the workspace, written as an inert blob.
type blobLeaf struct{}

func (blobLeaf) Name() string { return "git.blob" }

func (blobLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	root, err := rootOf(ctx, args)
	if err != nil {
		return fail(err)
	}
	var data []byte
	if p := stringArg(args, "path"); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		data, err = os.ReadFile(p)
		if err != nil {
			return fail(err)
		}
	} else {
		data = []byte(stringArg(args, "data"))
	}
	blob, err := git.New(root).HashObject(ctx, data)
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: blob + "\n"}, nil
}

// treeLeaf is git.tree: a tree built from args["entries"], each an object with
// mode, type, hash and name.
type treeLeaf struct{}

func (treeLeaf) Name() string { return "git.tree" }

func (treeLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	root, err := rootOf(ctx, args)
	if err != nil {
		return fail(err)
	}
	entries, err := treeEntries(args["entries"])
	if err != nil {
		return fail(err)
	}
	tree, err := git.New(root).Mktree(ctx, entries)
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: tree + "\n"}, nil
}

// treeEntries validates the args["entries"] array into git.TreeEntry values.
func treeEntries(v any) ([]git.TreeEntry, error) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("git.tree: args.entries must be a non-empty array")
	}
	out := make([]git.TreeEntry, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("git.tree: entries[%d] must be an object", i)
		}
		e := git.TreeEntry{
			Mode: stringArg(m, "mode"), Type: stringArg(m, "type"),
			Hash: stringArg(m, "hash"), Name: stringArg(m, "name"),
		}
		if e.Mode == "" || e.Type == "" || e.Hash == "" || e.Name == "" {
			return nil, fmt.Errorf("git.tree: entries[%d] needs mode, type, hash and name", i)
		}
		out = append(out, e)
	}
	return out, nil
}

// commitLeaf is git.commit: a dangling commit for args["tree"], with
// args["parents"] and args["message"]. It moves no ref.
type commitLeaf struct{}

func (commitLeaf) Name() string { return "git.commit" }

func (commitLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	root, err := rootOf(ctx, args)
	if err != nil {
		return fail(err)
	}
	tree := stringArg(args, "tree")
	if tree == "" {
		return fail(fmt.Errorf("git.commit: args.tree is required"))
	}
	parents, err := stringList(args["parents"])
	if err != nil {
		return fail(fmt.Errorf("git.commit: args.parents: %w", err))
	}
	commit, err := git.New(root).CommitTree(ctx, tree, parents, stringArg(args, "message"))
	if err != nil {
		return fail(err)
	}
	return builtin.Result{Output: commit + "\n"}, nil
}

// noteLeaf is git.note: it writes the note blob and tree for args["object"]
// and returns both as JSON. It moves no ref -- the refs/notes update is
// outward and belongs to publish (D1, D4).
type noteLeaf struct{}

func (noteLeaf) Name() string { return "git.note" }

func (noteLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	root, err := rootOf(ctx, args)
	if err != nil {
		return fail(err)
	}
	blob, tree, err := git.New(root).NoteObject(ctx, stringArg(args, "object"), stringArg(args, "message"))
	if err != nil {
		return fail(err)
	}
	return jsonOut(map[string]string{"blob": blob, "tree": tree})
}

// stringList decodes an optional JSON array of strings. A nil value is no
// list; anything else that is not an array of strings is refused.
func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array of strings")
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("item %d must be a string", i)
		}
		out = append(out, s)
	}
	return out, nil
}

// register panics on a registration error: the names are compile-time
// constants and a duplicate is a programming mistake, not a runtime condition.
func register(name string, leaf builtin.Leaf, policy builtin.Policy) {
	if err := builtin.Register(name, leaf, policy); err != nil {
		panic("gitleaves: " + err.Error())
	}
}

func init() {
	register("git.status", statusLeaf{}, builtin.AgentDefault)
	register("git.diff", diffLeaf{}, builtin.AgentDefault)
	register("git.numstat", numstatLeaf{}, builtin.AgentDefault)
	register("git.log", logLeaf{}, builtin.AgentDefault)
	register("git.show", showLeaf{}, builtin.AgentDefault)
	register("git.blob", blobLeaf{}, builtin.HumanOnly)
	register("git.tree", treeLeaf{}, builtin.HumanOnly)
	register("git.commit", commitLeaf{}, builtin.HumanOnly)
	register("git.note", noteLeaf{}, builtin.HumanOnly)
}
