package control

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"raj/internal/hooks"
)

// hookUsage is `raj hook`'s help. It names the subcommands and the add grammar,
// and states the local-only rule authoring obeys.
const hookUsage = `usage: raj hook <command> [options]

  add <name> -- ARGV...      store a hook that runs ARGV; --shell S for a shell string,
                             --action JSON for a builtin or composite (steps) action
  list                       list the workspace's hooks, and whether hooks are off
  show <name>                show one hook
  run <name> [NAME=value...] run the hook against its tree: projected or workspace,
                             passing each declared parameter by name
  log [--show RUN-ID [--tail N]]  the last 100 runs, or the tail of one run's log
  ps                         list the runs in flight now
  cancel <run-id>            stop an in-flight run
  off | on                   engage or lift the global hook panic switch
  rm <name>                  remove a hook
  enable <name>              allow a disabled hook to run
  disable <name>             stop a hook from running

add flags: --agent --cooldown-ms N --timeout-ms N --may-write --tree projected|workspace
           --detach --disabled --action JSON
           --param DECL: NAME=enum(a,b,c), NAME=string(<regex>) or NAME=uint, each
           with an optional =default that makes it optional at run time

The editor is found automatically, or named with --addr or RAJ_CONTROL_ADDR.
Authoring (add, rm, enable, disable), cancel and off/on are local-only: a TCP
editor refuses them, so run those on the machine holding the workspace. run
crosses, on both transports, but an agent may only run a hook whose Agent flag
is set; the local human may run any enabled hook. list, show, log and ps are
reads and cross.
`

// HookCLI runs one `raj hook` command and returns a process exit code. It dials
// a running editor exactly as `raj ctl` does, reusing Locate and Dial.
// stringList is a repeatable string flag: each --param appends one declaration
// in the order given, so a hook's parameters keep their authored order.
type stringList []string

// String renders the list for the flag package's usage line.
func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

// Set appends one value. It never fails, so a malformed declaration is the
// server's refusal rather than a parse error in the flag package.
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func HookCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, hookUsage)
		return 2
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("raj hook "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: raj hook %s [options]\n\n", sub)
		PrintFlagUsage(fs.Output(), fs)
	}
	socket := fs.String("socket", "", "path to the editor's control socket")
	addr := fs.String("addr", "", "the editor's control address: a socket path, or tcp://host:port")
	asJSON := fs.Bool("json", false, "machine-readable output")
	identity := fs.String("as", "", "run as this registered identity (a raj ctl register key); RAJ_IDENTITY is the fallback")
	shell := fs.String("shell", "", "add: run through this shell string instead of argv")
	agent := fs.Bool("agent", false, "add: allow agents to invoke this hook")
	cooldown := fs.Int("cooldown-ms", 0, "add: minimum milliseconds between runs")
	timeout := fs.Int("timeout-ms", 0, "add: run timeout in milliseconds")
	mayWrite := fs.Bool("may-write", false, "add: the hook may modify the tree")
	tree := fs.String("tree", "projected", "add: where the hook runs: projected (scratch tree) or workspace (saved root)")
	detach := fs.Bool("detach", false, "add: run the hook in its own session, surviving the request")
	show := fs.String("show", "", "log: show the tail of one run's log by run id")
	tail := fs.Int("tail", 0, "log --show: keep only the last N lines")
	disabled := fs.Bool("disabled", false, "add: store the hook disabled")
	action := fs.String("action", "", `add: the action as raw JSON, e.g. a {"steps":[...]} composite`)
	param := &stringList{}
	fs.Var(param, "param", `add: declare a parameter (repeatable): NAME=enum(a,b,c), NAME=string(<regex>) or NAME=uint, each with an optional =default`)

	// For add, everything after "--" is the hook's argv and must reach it
	// intact, exactly as exec treats a command line.
	var argv []string
	if sub == "add" {
		if i := indexOf(rest, "--"); i >= 0 {
			argv, rest = rest[i+1:], rest[:i]
		}
	}
	// reorder moves the operand (the name) after the flags, so the natural
	// "add check --agent" word order parses the flags rather than treating them
	// as operands. Go's flag package stops at the first non-flag argument.
	if err := fs.Parse(reorder(fs, rest)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	switch sub {
	case "add", "list", "show", "run", "log", "ps", "cancel", "off", "on", "rm", "enable", "disable":
	default:
		fmt.Fprintf(stderr, "raj hook: unknown command %q\n\n%s", sub, hookUsage)
		return 2
	}

	cwd, _ := os.Getwd()
	sock, err := Locate(firstOf(*addr, *socket), cwd)
	if err != nil {
		fmt.Fprintln(stderr, "raj hook:", err)
		return 1
	}
	c, err := Dial(sock)
	if err != nil {
		fmt.Fprintf(stderr, "raj hook: cannot reach raj on %s: %v\n", sock, err)
		return 1
	}
	defer c.Close()
	// Bind the caller's identity when it has one, as `raj ctl` does, so a run
	// is admitted and logged as the agent that asked rather than as a fresh
	// provisional id. With no identity the connection stays unregistered,
	// which on the Unix socket is the local human.
	if bind := firstOf(*identity, os.Getenv("RAJ_IDENTITY")); bind != "" {
		if res, err := c.Do(Request{Op: "hello", Identity: bind}); err != nil || res.Err != "" {
			fmt.Fprintf(stderr, "raj hook: could not bind identity %s: %v%s\n", bind, err, res.Err)
			return 1
		}
	}

	switch sub {
	case "add":
		return hookAdd(c, fs.Arg(0), argv, *shell, *action, *param, *agent, *cooldown, *timeout, *tree, *mayWrite, *detach, *disabled, stdout, stderr, *asJSON)
	case "list":
		return hookList(c, stdout, stderr, *asJSON)
	case "show":
		return hookShow(c, fs.Arg(0), stdout, stderr, *asJSON)
	case "run":
		// Everything after the name is a NAME=value parameter assignment; a
		// bare name has none.
		var params []string
		if fs.NArg() > 1 {
			params = fs.Args()[1:]
		}
		return hookRun(c, fs.Arg(0), params, stdout, stderr, *asJSON)
	case "log":
		return hookLog(c, *show, *tail, stdout, stderr, *asJSON)
	case "ps":
		return hookPS(c, stdout, stderr, *asJSON)
	case "cancel":
		return hookCancel(c, fs.Arg(0), stdout, stderr, *asJSON)
	case "off", "on":
		return hookSwitch(c, sub, stdout, stderr, *asJSON)
	default: // rm, enable, disable
		return hookSimple(c, sub, fs.Arg(0), stdout, stderr, *asJSON)
	}
}

// hookRun runs one hook. The command runs against the hook's own tree on the
// server -- a projected scratch tree by default, or the saved workspace root --
// so the output streams exactly as `raj ctl exec`'s does and the exit
// status is the hook's own; a refusal -- unknown, disabled, agent-only,
// undeclared or invalid parameters, in flight or cooling -- exits 2, so "the
// hook failed" and "the hook never ran" stay distinguishable. params are the
// NAME=value assignments after the hook name; the server validates each against
// the hook's declarations before the action starts, so a refusal never runs it.
// A detached hook returns before it finishes, so hookRun prints the started
// line and no completion stamp: the stamp belongs to the run end, which the
// reply did not wait for.
func hookRun(c *Client, name string, params []string, stdout, stderr io.Writer, asJSON bool) int {
	if name == "" {
		fmt.Fprintln(stderr, "raj hook run: needs a name")
		return 2
	}
	var outBuf, errBuf []byte
	relay := func(stream uint8, b string) {
		switch {
		case asJSON && stream == StreamStderr:
			errBuf = append(errBuf, b...)
		case asJSON:
			outBuf = append(outBuf, b...)
		case stream == StreamStderr:
			io.WriteString(stderr, b)
		default:
			io.WriteString(stdout, b)
		}
	}
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: name, HookParams: params}, relay)
	if err != nil {
		fmt.Fprintln(stderr, "raj hook run:", err)
		return 2
	}
	if res.Err != "" {
		// The server's message is the whole answer; print it verbatim, then
		// the retry hint the gate supplied when it had one.
		fmt.Fprintln(stderr, res.Err)
		if res.RetryAfterMS > 0 {
			fmt.Fprintf(stderr, "retry after %dms\n", res.RetryAfterMS)
		}
		// A run that started and then timed out or was cancelled still has a
		// stamp; print it so the failure can be cited like a finished run.
		if res.HookRunID != 0 {
			fmt.Fprintf(stderr, "raj: hook %s run %d revision %d HEAD %s dirty %s exit %d duration %dms truncated=%t\n",
				name, res.HookRunID, res.HookRevision, res.HookHead, res.HookDirty, res.Exit, res.HookDurationMS, res.HookTruncated)
		}
		return 2
	}
	if res.PID > 0 {
		// A detached run starts and returns at once; its final frame carries
		// the pid it started and no completion. A synchronous completion
		// leaves pid zero, so this is the one hook-run reply that is a start.
		// The started-detached line already streamed to stderr, and a
		// completion stamp -- exit and duration -- belongs to the run end,
		// which this request did not wait for. Report the start and nothing
		// more.
		if asJSON {
			emit(stdout, map[string]any{
				"name": name, "started": true, "hook_run_id": res.HookRunID,
				"pid": res.PID, "revision": res.HookRevision,
				"head": res.HookHead, "dirty": res.HookDirty,
				"stdout": string(outBuf), "stderr": string(errBuf)})
		}
		return 0
	}
	if asJSON {
		emit(stdout, map[string]any{
			"name": name, "exit": res.Exit,
			"hook_run_id": res.HookRunID, "revision": res.HookRevision,
			"head": res.HookHead, "dirty": res.HookDirty,
			"duration_ms": res.HookDurationMS, "truncated": res.HookTruncated,
			"stdout": string(outBuf), "stderr": string(errBuf)})
		return res.Exit
	}
	// The stamp is printed last, on stderr, so stdout carries the hook output
	// and a reader learns the run id, revision, HEAD and exit from one line.
	fmt.Fprintf(stderr, "raj: hook %s run %d revision %d HEAD %s dirty %s exit %d duration %dms truncated=%t\n",
		name, res.HookRunID, res.HookRevision, res.HookHead, res.HookDirty, res.Exit, res.HookDurationMS, res.HookTruncated)
	return res.Exit
}

// hookLog prints the in-memory run log, or the tail of one run's file when
// show names a run id (`--show`). Under --json the server answer passes through
// verbatim; the show form prints the text it returned, capped again by tail.
func hookLog(c *Client, show string, tail int, stdout, stderr io.Writer, asJSON bool) int {
	if show != "" {
		id, perr := strconv.ParseUint(show, 10, 64)
		if perr != nil || id == 0 {
			fmt.Fprintf(stderr, "raj hook log: %q is not a run id\n", show)
			return 2
		}
		if tail > hookRunTailLines {
			fmt.Fprintf(stderr, "raj hook log: --tail %d is above the %d-line cap the server returns\n", tail, hookRunTailLines)
			return 2
		}
		res, err := c.Do(Request{Op: "hook", HookMode: "log", HookRunID: id})
		if code := hookFail(stderr, res, err); code != 0 {
			return code
		}
		if asJSON {
			return emit(stdout, map[string]any{"run_id": id, "tail": trimTail(res.HookLogJSON, tail)})
		}
		io.WriteString(stdout, trimTail(res.HookLogJSON, tail))
		return 0
	}

	res, err := c.Do(Request{Op: "hook", HookMode: "log"})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		if res.HookLogJSON == "" {
			res.HookLogJSON = "[]"
		}
		fmt.Fprintln(stdout, res.HookLogJSON)
		return 0
	}
	if res.HookOff {
		fmt.Fprintln(stdout, "hooks are off")
	}
	var rows []hooks.Result
	if err := json.Unmarshal([]byte(res.HookLogJSON), &rows); err != nil {
		fmt.Fprintln(stdout, res.HookLogJSON)
		return 0
	}
	for _, r := range rows {
		fmt.Fprintf(stdout, "%d\t%s\tauthor=%d\trevision=%d\tHEAD=%s\tdirty=%s\texit=%d\tduration=%dms\ttruncated=%t",
			r.ID, r.Hook, r.Author, r.Revision, r.Head, r.Dirty, r.Exit, r.DurationMS, r.Truncated)
		if len(r.Params) > 0 {
			parts := make([]string, 0, len(r.Params))
			for _, p := range r.Params {
				parts = append(parts, p.Name+"="+p.Value)
			}
			fmt.Fprintf(stdout, "\tparams=%s", strings.Join(parts, ","))
		}
		if r.Detach {
			fmt.Fprintf(stdout, "\tdetached")
			if r.PID > 0 {
				fmt.Fprintf(stdout, " pid=%d", r.PID)
			}
			if r.LogPath != "" {
				fmt.Fprintf(stdout, " log=%s", r.LogPath)
			}
		}
		if r.Recovered {
			fmt.Fprint(stdout, "\trecovered")
		}
		if r.Lost {
			fmt.Fprint(stdout, "\tlost")
		}
		if r.Err != "" {
			fmt.Fprintf(stdout, "\terr=%q", r.Err)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

// trimTail keeps the last n lines of text; n <= 0 leaves the server's own cap
// as it came.
func trimTail(text string, n int) string {
	if n <= 0 || text == "" {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// hookPS prints the in-flight runs. Like the log it is a read, and --json is
// the server answer verbatim.
func hookPS(c *Client, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "hook", HookMode: "ps"})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		if res.HookPSJSON == "" {
			res.HookPSJSON = "[]"
		}
		fmt.Fprintln(stdout, res.HookPSJSON)
		return 0
	}
	if res.HookOff {
		fmt.Fprintln(stdout, "hooks are off")
	}
	var runs []hooks.Run
	if err := json.Unmarshal([]byte(res.HookPSJSON), &runs); err != nil {
		fmt.Fprintln(stdout, res.HookPSJSON)
		return 0
	}
	for _, r := range runs {
		fmt.Fprintf(stdout, "%d\t%s\tauthor=%d\tpid=%d\tpgid=%d\tstarted=%s\n",
			r.ID, r.Hook, r.Author, r.PID, r.PGID, r.Started)
	}
	return 0
}

// hookCancel stops one in-flight run by id. A missing or malformed id is
// refused locally, so the request is never sent.
func hookCancel(c *Client, id string, stdout, stderr io.Writer, asJSON bool) int {
	if id == "" {
		fmt.Fprintln(stderr, "raj hook cancel: needs a run id")
		return 2
	}
	n, perr := strconv.ParseUint(id, 10, 64)
	if perr != nil || n == 0 {
		fmt.Fprintf(stderr, "raj hook cancel: %q is not a run id\n", id)
		return 2
	}
	res, err := c.Do(Request{Op: "hook", HookMode: "cancel", HookRunID: n})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "run_id": res.HookRunID,
			"name": res.HookName, "pid": res.PID})
	}
	fmt.Fprintf(stdout, "cancelling %s (run %d, pid %d)\n", res.HookName, res.HookRunID, res.PID)
	return 0
}

// hookSwitch flips the global panic switch. It is local-only, so a TCP editor
// refuses it before any hook is touched.
func hookSwitch(c *Client, mode string, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "hook", HookMode: mode})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "off": res.HookOff, "mode": mode})
	}
	if res.HookOff {
		fmt.Fprintln(stdout, "hooks are off")
	} else {
		fmt.Fprintln(stdout, "hooks are on")
	}
	return 0
}

// hookAdd builds one HookRow and puts it. The add grammar is the spec's: an
// argv after "--", a --shell string, or --action raw JSON (the path to a
// builtin or a composite steps action), exactly one of the three. The server
// validates the action with the hooks domain, so a bad --action is not stored.
// A new hook defaults to trigger agent, tree projected, agent false (never
// agent-callable by default),
// enabled true unless --disabled, and zero cooldown and timeout.
func hookAdd(c *Client, name string, argv []string, shell, actionJSON string, params []string, agent bool, cooldown, timeout int, tree string, mayWrite, detach, disabled bool, stdout, stderr io.Writer, asJSON bool) int {
	if name == "" {
		fmt.Fprintln(stderr, "raj hook add: needs a name")
		return 2
	}
	forms := 0
	for _, used := range []bool{len(argv) > 0, shell != "", actionJSON != ""} {
		if used {
			forms++
		}
	}
	if forms > 1 {
		fmt.Fprintln(stderr, "raj hook add: --shell, --action and an argv after -- are mutually exclusive")
		return 2
	}
	var action string
	switch {
	case actionJSON != "":
		action = actionJSON
	case shell != "":
		data, err := json.Marshal(map[string]string{"shell": shell})
		if err != nil {
			fmt.Fprintln(stderr, "raj hook add:", err)
			return 1
		}
		action = string(data)
	case len(argv) > 0:
		data, err := json.Marshal(argv)
		if err != nil {
			fmt.Fprintln(stderr, "raj hook add:", err)
			return 1
		}
		action = string(data)
	default:
		fmt.Fprintln(stderr, "raj hook add: needs an argv after --, a --shell string or an --action JSON")
		return 2
	}
	// A declaration list crosses as one JSON array, exactly as the action does;
	// empty means the hook declares no parameters.
	paramsJSON := ""
	if len(params) > 0 {
		data, err := json.Marshal(params)
		if err != nil {
			fmt.Fprintln(stderr, "raj hook add:", err)
			return 1
		}
		paramsJSON = string(data)
	}
	row := HookRow{
		Name: name, Action: action, Trigger: "agent", Tree: tree, Agent: agent,
		CooldownMS: cooldown, TimeoutMS: timeout, MayWrite: mayWrite, Detach: detach,
		Enabled: !disabled, Params: paramsJSON,
	}
	data, err := json.Marshal(row)
	if err != nil {
		fmt.Fprintln(stderr, "raj hook add:", err)
		return 1
	}
	res, err := c.Do(Request{Op: "hook", HookMode: "put", HookJSON: string(data)})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, row)
	}
	fmt.Fprintf(stdout, "added %s\n", name)
	return 0
}

// hookList lists the workspace's hooks. The JSON form is the server's answer
// verbatim; the plain form is one line per hook.
func hookList(c *Client, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "hook", HookMode: "list"})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		if res.HookOff {
			fmt.Fprintln(stderr, "hooks are off")
		}
		if res.HookJSON == "" {
			res.HookJSON = "[]"
		}
		fmt.Fprintln(stdout, res.HookJSON)
		return 0
	}
	if res.HookOff {
		fmt.Fprintln(stdout, "hooks are off")
	}
	rows, ok := decodeHookRows(res.HookJSON, stdout)
	if !ok {
		return 0
	}
	return printHooks(stdout, rows)
}

// hookShow shows one hook. A missing name is refused locally so the request is
// never sent; a name the server does not know comes back as its refusal.
func hookShow(c *Client, name string, stdout, stderr io.Writer, asJSON bool) int {
	if name == "" {
		fmt.Fprintln(stderr, "raj hook show: needs a name")
		return 2
	}
	res, err := c.Do(Request{Op: "hook", HookMode: "show", HookName: name})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		fmt.Fprintln(stdout, res.HookJSON)
		return 0
	}
	var row HookRow
	if uerr := json.Unmarshal([]byte(res.HookJSON), &row); uerr != nil {
		fmt.Fprintln(stdout, res.HookJSON)
		return 0
	}
	return printHooks(stdout, []HookRow{row})
}

// hookSimple is rm, enable and disable: a named authoring call with no payload.
func hookSimple(c *Client, mode, name string, stdout, stderr io.Writer, asJSON bool) int {
	if name == "" {
		fmt.Fprintf(stderr, "raj hook %s: needs a name\n", mode)
		return 2
	}
	res, err := c.Do(Request{Op: "hook", HookMode: mode, HookName: name})
	if code := hookFail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "mode": mode, "name": name})
	}
	switch mode {
	case "rm":
		fmt.Fprintf(stdout, "removed %s\n", name)
	case "enable":
		fmt.Fprintf(stdout, "enabled %s\n", name)
	case "disable":
		fmt.Fprintf(stdout, "disabled %s\n", name)
	}
	return 0
}

// decodeHookRows parses a list answer. An answer this build cannot parse is
// echoed rather than dropped: it is still the server's answer. ok is false when
// the text was echoed.
func decodeHookRows(jsonText string, stdout io.Writer) (rows []HookRow, ok bool) {
	if err := json.Unmarshal([]byte(jsonText), &rows); err != nil {
		fmt.Fprintln(stdout, jsonText)
		return nil, false
	}
	return rows, true
}

// printHooks renders rows for a person: the fields a reader needs to decide
// whether to run or edit each hook.
func printHooks(stdout io.Writer, rows []HookRow) int {
	for _, r := range rows {
		state, agent := "enabled", "-"
		if !r.Enabled {
			state = "disabled"
		}
		if r.Agent {
			agent = "agent"
		}
		line := fmt.Sprintf("%s\ttrigger=%s\ttree=%s\t%s\t%s\tcooldown=%dms\ttimeout=%dms\tmay-write=%t\tdetach=%t",
			r.Name, r.Trigger, r.Tree, agent, state, r.CooldownMS, r.TimeoutMS, r.MayWrite, r.Detach)
		if r.Params != "" && r.Params != "[]" {
			line += "\tparams=" + r.Params
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// hookFail reports a transport error or the server's refusal. A refusal is
// printed verbatim: the server's message is the whole answer, so a TCP
// authoring refusal reads exactly as the server wrote it.
func hookFail(stderr io.Writer, res Response, err error) int {
	if err != nil {
		fmt.Fprintln(stderr, "raj hook:", err)
		return 1
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, res.Err)
		return 1
	}
	return 0
}
