# HOOKS-SPEC — user-authored host actions with triggers

Status: v0 implemented. The store v2->v3 and v3->v4 `hooks` table (the v4
migration adds `tree` and `detach`), the pure `internal/hooks` package (Parse,
Admit, Gate, Registry, Log), and the CLI
`raj hook add|list|show|rm|enable|disable|run|log|ps|cancel|off|on` exist. The
agent contract is a name-only `run`; authoring, `cancel` and the `off|on` panic
switch are local-only, while `list`, `show`, `log` and `ps` cross both
transports. The v0 subset in §5 is what shipped and names what is deferred.
Relates to the TODO items *format-on-save* and the *workspace-wide `lsp
diagnostics` sweep before the host gate*, which become instances of this
substrate rather than special cases.

## 1. What a hook is

A hook is a **user-authored, host-side action with a trigger**. It has a fixed
action, it is declared once and stored per workspace, and agents may invoke only
the hooks explicitly allowed to them — and only by name.

    { name, action, trigger, tree, agent, cooldown_ms, timeout_ms, may_write, detach, enabled, updated }

- **action** — argv (JSON array) by default, so there is no shell. A shell string
  only when the *author* chose one. A multi-command script is just argv:
  `["bash","scripts/check.sh"]`.
- **trigger** — `agent` (invoked by name), `on-save`, `before-gate`. v0 ships
  `agent`.
- **tree** — `projected` (the default) runs against a scratch tree materialised
  from the live buffers, accepted and proposed text included. `workspace` runs
  against the saved workspace root itself, and then only when the workspace is
  ready (§4). Any other value is refused.
- **detach** — default off. A detached hook starts in its own session, so it
  outlives the request and the editor; its output goes to a file and a later
  editor recovers its exit status (§4.1). Detached runs are for hooks that must
  restart the very editor running them.
- **agent** — the allowlist flag. A hook is agent-callable iff `agent = 1` *and*
  its action is not a `HumanOnly` builtin leaf (§1.1).

Two consequences worth stating:
- There is no separate allowlist concept at the hook level: the per-hook
  `agent` flag is it, further bounded by a builtin leaf's `Policy` (§1.1).
- The agent never supplies argv, env, or cwd. It supplies a name.

### 1.1 Builtin actions

An action has three forms: a bare argv array, `{"shell": "..."}`, or

    {"builtin": "<name>", "args": {...}}

The builtin form names an in-process action the editor already carries — a
registered leaf — instead of a command line. `args` is optional; when present
it must be a JSON object. `detach` and builtin are mutually exclusive: a
detached action is a process, and a leaf is not one.

The registry is `internal/hooks/builtin`. `Register(name, leaf, policy)` records
a `Leaf` under its name at init time, and `Lookup(name)` resolves it to the leaf
and its policy. A `Leaf` is

    Name() string
    Run(ctx context.Context, args map[string]any) (Result, error)

`Result{Output string; Exit int}` is what the caller captures where an external
hook's stdout and exit status would arrive. `Run` receives the run's context, so
a leaf honours the hook timeout and `hook cancel` exactly as an external
command's process-group kill does. A leaf runs in-process with its `args` and no
working directory, so the hook's `tree` selects the cwd for an external command
only.

`Register` also records the leaf's agent `Policy`. An `AgentDefault` leaf is
agent-callable when the hook's `agent` flag allows it; a `HumanOnly` leaf is
refused to an agent caller whatever the row says. The policy is fixed at
registration, so a leaf never decides its own admission.

The name is resolved against the leaf registry at **parse time**, not run time.
A stored row whose builtin is not registered in this build is refused by `Parse`
with the offending name, exactly as an unknown trigger or tree is. A typo is
then a validation error the author fixes once, not a run that fails (or
silently does nothing) later, and the set of actions a stored hook can name
stays closed — which is what lets the dispatch path branch on the action kind
before it builds a command line. Registration happens at init, so a binary that
calls `Parse` must link every package whose leaves it stores; a leaf package the
build does not import is unknown to `Parse`, exactly as if it had never
registered.

A builtin leaf is not a way around the gate. It runs through the same `Admit`
call and the same `Gate` as an external hook — the `agent` flag, the trigger,
the cooldown and the in-flight coalescing all apply — and its output arrives
where an external hook's stdout would, so `RAJ_STEP_<name>_OUT` chaining treats
the two identically.

### 1.2 The git capability leaves

`internal/hooks/builtin/gitleaves` registers the git leaves: the read leaves
`git.status`, `git.diff`, `git.numstat`, `git.log` and `git.show` are
`AgentDefault`; the object leaves `git.blob`, `git.tree`, `git.commit` and
`git.note` are `HumanOnly`. Each wraps `internal/git`; none is a second
implementation and none extends the read-only `raj ctl git` verb.

- `git.status` returns the structured `GitView` as JSON. `git.diff` and
  `git.show` return raw text (`rev`, `path`), and an empty diff is success:
  no output, exit zero. `git.numstat` and `git.log` return JSON (`rev`, `path`;
  `count`); a binary file's `-` numstat counts become `Binary: true`.
- `git.blob` (`data`, or `path` under the root), `git.tree` (`entries`),
  `git.commit` (`tree`, `parents`, `message`) and `git.note` (`object`,
  `message`) write inert objects. `git.note` writes the note blob and tree
  only: it moves no ref, because the first `refs/notes` write belongs to
  publish (S3), not to object plumbing.
- A leaf has no working directory, so it resolves the workspace root from an
  explicit `args["root"]`, a root carried on the run context by
  `gitleaves.WithRoot`, or the process working directory. The dispatch path
  does not yet pass `prep.Root`; wiring that one line is the open H3 gap.

Composites and the step-chaining contract -- `RAJ_STEP_<name>_OUT`, fail-fast
with `{failed_step, stderr}`, the 64 KiB output spill, and load-time rejection
of two step names that normalise to the same variable -- are **not built**.
`Parse` has no step form and the runner has no chain engine, so `git-context`
and `intention-diff` wait; the leaves land first.

## 2. Storage — the v2 -> v3 and v3 -> v4 migrations

The store already carries a versioned, idempotent migration list. Hooks are
the **v2 -> v3** table and the **v3 -> v4** `tree` and `detach` columns in the
same SQLite database.

    CREATE TABLE IF NOT EXISTS hooks (
      name        TEXT PRIMARY KEY,
      action      TEXT NOT NULL,     -- JSON argv array, {"shell": "..."} or {"builtin": "..."}
      trigger     TEXT NOT NULL,     -- agent | on-save | before-gate
      agent       INTEGER NOT NULL,  -- 0/1; the allowlist flag
      cooldown_ms INTEGER NOT NULL,
      timeout_ms  INTEGER NOT NULL,
      may_write   INTEGER NOT NULL,
      enabled     INTEGER NOT NULL,
      updated     INTEGER NOT NULL
    )

The `tree` and `detach` columns are migration **v3 -> v4**, two `ALTER TABLE`
statements that backfill every row written before them:

    ALTER TABLE hooks ADD COLUMN tree TEXT NOT NULL DEFAULT 'projected'
    ALTER TABLE hooks ADD COLUMN detach INTEGER NOT NULL DEFAULT 0

The step runs in the transaction that records the version, and an `ADD COLUMN`
that finds the column already there is skipped, so re-running or racing the
step is harmless.

- The database is already opened under the workspace state dir keyed by the root
  set (`session.StateDirForRoots`), so these rows are **workspace-specific for
  free**. No `.raj`, no repo file; `TestFreshLaunchLeavesProjectClean` stays true.
- If a user-scoped registry is wanted later, add a `scope` column
  (`user` | `workspace`) and read the overlay the way settings already do. Not
  needed for v0.
- Cooldown and last-run timestamps are runtime state; they live in memory per
  daemon. A persisted run log is separate (section 6) and optional.

## 3. The agent contract

One verb: `run <name>`. Name only. Allowed iff the hook exists, `enabled = 1`,
`agent = 1`, its action is not a `HumanOnly` builtin leaf (§1.1), and it belongs
to the current workspace. Anything else refuses with the name and the reason.

The reply carries: run id, hook name, the revision it ran against, the git state
it ran against (section 6), exit status, capped output, and a truncated flag.
Result frames may stream; the last frame is final. A cancelled run reports
*cancelled*, never a zero exit.

There is no verb to define, edit, enable, or run arbitrary commands. A read-only
`run --list` gives the agent its capabilities so it does not have to guess.

## 4. Execution

- Runs on the **host** (where the toolchain and the editor are). Never in the
  container.
- **cwd** = the hook's tree: the scratch root for a projected hook, the primary
  workspace root for a `workspace` hook. A workspace hook is refused unless the
  workspace is ready — no dirty buffer and no pending change set, the predicate
  `raj ctl status` applies — so it only ever builds what the user saved. **env**
  is inherited from the editor unchanged — no separate PATH and no injected
  variables — so `make`, `go` and `docker` resolve as they do in the user's
  shell.
- Wall-clock **timeout**, with a **process-group kill** on timeout or cancel so
  children are not orphaned.
- **Output cap** (bytes) with a truncated flag. Exit status captured even on
  timeout.
- `may_write = 0` means the run must not modify the tree; verify with git
  (section 6). A workspace hook takes a `git status` digest before and after: if
  it moved, the run fails with "hook X modified the workspace" and the log
  records that failure even when the command exited zero. Projected hooks keep
  v0's reported-not-refused behaviour.

### 4.1 Detached runs

A hook with `detach = 1` is started in its own session (`setsid`), so it
survives the editor exiting — what a hook like `cycle` needs, since it restarts
the very process that ran it. Survival is a POSIX property: where there are no
sessions the flag is accepted but the run does not outlive the editor. The reply
returns at once with the run id, pid and log path; the command's output no longer
streams, so it goes to files under the workspace state directory:

    <state dir>/hook-runs/<run-id>.log     the command's stdout and stderr
    <state dir>/hook-runs/<run-id>.exit    the exit status, written by a wrapper
    <state dir>/hook-runs/<run-id>.pid     JSON: pid, start, deadline, hook, and the start stamp

The log file is not capped: a detached run writes as much as it likes, and the
directory is bounded only by the number of runs it keeps (the last 100).
Because the request returns before the run ends, a detached start prints the
started line and no completion stamp: exit and duration belong to the completion
the log records when the run actually ends.

A detached run honours the hook's `timeout_ms`; a zero timeout means no
deadline. The absolute deadline is written into the `<id>.pid` record, so a
later editor keeps it: on that editor's start, every exit file not yet logged
becomes a recovered run; a pid record whose process is gone with no exit becomes
a lost run; a run still alive under its own session is re-adopted — re-added to
`hook ps` and reachable by `cancel` — and watched, polling its exit file about
every two seconds, so its completion (exit, duration, error) is logged when it
ends and its deadline still kills its process group. The record also carries the
run start stamp — the author, revision, HEAD and dirty it began with — so a
recovered or re-adopted log row names what ran and against what, instead of
logging zeros. Consumed marker files are removed so a later start does not log
them twice, and live runs are never pruned.

`raj hook log --show <run-id> [--tail N]` returns the tail of one run's log,
capped at 400 lines and 256 KiB per read; a `--tail` above that cap is refused
rather than silently clamped.

## 5. Gating

### a. Admission
`agent = 1` per hook; name-only; workspace match. A builtin action whose leaf
registered `HumanOnly` is refused to an agent caller even when `agent = 1`
(§1.1). A `workspace` hook is further refused unless the root looks like a git
work tree — a `.git` directory, or a
`.git` file as a linked worktree writes — because the provenance stamp and the
`may_write` check both need git. The admission probe is a filesystem check, not
a git call, so it runs on the event thread without spawning anything; the real
`git view` still happens off-thread in the run path.

### b. Rate and concurrency — cooldown *plus* a revision cap
- A pure cooldown punishes a legitimate fast loop. Cap **runs per revision**
  (e.g. 3) *and* apply a small wall-clock floor (e.g. 2 s). Both must pass.
- **Coalesce:** at most one run in flight per hook and at most one pending rerun;
  never a queue of N. On refusal, return `retry_after_ms`.
- A **global token bucket** per workspace and per agent connection.
- Bounded overall concurrency and queue depth.
- **Revision dedupe:** the same hook at the same revision returns the cached
  result.
- **Thrash breaker:** several requests with no intervening edit are refused.

### c. Execution safety
As section 4, plus: no network by default (documented, not enforced in v0).
cgroup resource limits are future work.

`may_write = 0` on a workspace hook compares `git status` paths and codes before
and after; it does not hash the content of a path that was already dirty, so a
write that leaves the status text unchanged is not caught.

### d. Observability and control
- A run log: hook, revision, git state, who, exit, duration, output hash.
- A user-visible running list, cancel, and a **global panic switch** that disables
  all hooks immediately. Non-negotiable.
- Results are stamped with the revision and git state; when either moves they are
  **stale**, not wrong.

### e. Triggers
`agent` in v0. `on-save` (debounced, serialized per workspace) and `before-gate`
next. Two open TODO items then become instances: format-on-save, and the
workspace-wide diagnostics sweep before the host gate.

### v0 subset
Shipped: name-only + `agent` flag; a `tree` of `projected` (the v0 scratch
tree) or `workspace` (the saved root, gated on readiness); a `detach` flag that
runs the hook in its own session with a file-backed log and exit recovery;
per-hook cooldown **and** per-revision cap;
coalescing; timeout + process-group kill; a 1 MiB per-run output cap whose final
frame carries a `truncated` flag; a final-frame stamp (run id, hook, projection
revision, HEAD, dirty digest, exit, duration, truncated); an in-memory run log of
the last 100 runs (`raj hook log`); in-flight visibility and cancel (`raj hook
ps` / `raj hook cancel`); and a global panic switch (`raj hook off|on`) whose
state `list` and `ps` report. `raj hook run` on the Unix socket binds the local
human, so the person may run a non-agent hook, while a TCP agent is bounded by
admission.

Deferred: revision dedupe and cached results, a persisted run log (D-H3), a
write-guard beyond `git status`, cgroups, confirmation flows, and the scope
overlay.

## 6. Git integration

The workspace is usually a git checkout, which gives several things for free —
but not all of them, and it is worth being precise about which.

### Free
- **Provenance.** Record `HEAD` sha plus a digest of `git status --porcelain`
  (dirty paths and their states) in every run. "Checked at rev N" becomes
  "checked at `<sha>` with dirty set D" — the thing a human can share and
  reproduce.
- **The write guard.** For `may_write = 0`, `git status --porcelain` before and
  after is the check. Git defines "the tree changed," cheaply.
- **The change unit.** The review pass wants an enumerable diff (see TODO). The
  natural vocabulary is `git diff` over the working tree, and hooks validate that
  diff.
- **Parallel workspaces.** If the multi-ticket workflow uses git worktrees, each
  worktree is a workspace and hooks scope to it naturally. *Caveat:* the TODO
  Direction says "no work trees or branch hackery" — but that is about **conflict
  navigation**, not about running a check. Whether a worktree is an accepted
  workspace shape is a decision to make explicitly, not to inherit from that line.
- **Enumeration.** git tells which files are tracked, untracked, or ignored — what
  a scratch materialization needs.

### Not free — the important one
- **Git does not materialize unsaved proposals.** Buffers can hold accepted and
  proposed text that is not on disk, and a check must compile files. Git has no
  view of buffers, so a run against the working tree tests the **last saved
  state**, not what the agent is proposing. Options:
  1. **v0, in place and honest.** The reply states "executed against the saved
     tree; unsaved proposals are not included." An agentic loop then needs the
     human to save between iterations, or the agent's edits are invisible.
  2. **Later, a scratch tree.** Copy or reflink the workspace, overwrite the dirty
     buffer paths with buffer text, run there. **Hardlinks are not safe** for
     `may_write` hooks — they share inodes. Use reflink (APFS clone, btrfs/xfs
     reflink) with a full-copy fallback. Ignored build deps must come along or the
     tree will not build.
  3. **Name the difference.** `run` executes against the saved tree; a future
     `run --proposed` would materialize. Say which in the reply so no one
     confuses the two.

### Naming
These are **raj hooks**, not git hooks — same word, different layer. A bridge is
possible later (a git `pre-commit` calling `raj hook run check`), but not in v0.

## 7. CLI

    raj hook add <name> -- <argv...>     # or --shell "..."
    raj hook add <name> --tree projected|workspace   # default projected
    raj hook add <name> --detach                 # run in its own session
    raj hook list                        # trigger, agent, cooldown, and the panic switch
    raj hook show <name>
    raj hook rm <name>
    raj hook enable|disable <name>
    raj hook run <name>                  # the human equivalent of the agent verb
    raj hook log [--show RUN-ID [--tail N]]   # the last 100 runs, or one run's tail
    raj hook ps                          # the runs in flight now
    raj hook cancel <run-id>             # local-only
    raj hook off | on                    # local-only panic switch

Creation defaults to the workspace, matching the settings pane's default scope.
The CLI is the only way to author hooks; the agent surface is read/execute only.
Authoring, `cancel` and `off|on` are local-only over the transport; `log` and
`ps` are reads that cross. The agent's own `run` crosses, bounded by each hook's
`agent` flag.

## 8. Lifecycle and who may author

Hooks are host state, so "who can make one" is a security question. The answer
is a transport rule, not a build flag.

- **Authoring is local-only.** `hook add`, `hook edit`, `hook rm` and `hook
  import` are refused over TCP, exactly as `exec` is. Only the local human — the
  unix socket, or the editor — can write a hook. An agent driving over TCP can
  run your hooks; it can never create or change one. That is the physical
  guarantee: the container has no path to the store, and the one door it does
  have refuses authoring.
- **Running is what crosses the line.** `run <name>` is allowed over TCP, but
  only for hooks with `agent = 1` and only when the action is not a `HumanOnly`
  leaf (§1.1). A new hook is never agent-callable by default; the author turns
  that on deliberately.
- **A composite cannot exceed its parts.** The transitive rule in §5 holds, so
  chaining cannot manufacture a capability.
- **Loaded at start, resolved at each run.** The daemon loads and validates the
  rows at startup (argv parses, sub-hooks exist, no cycles, depth cap). So
  `raj hook add …` followed by a daemon restart is enough: the list was saved, so
  it comes back. Better, resolve the row at each `run`, so edits are live without
  a restart; restart is the fallback.
- **Import is for generation, by a human.** A script may write a file of hooks,
  but a human runs the import, and the import verb is local-only like the rest.
- **If the daemon ever runs in a container**, hooks must live in that daemon's
  persisted state (or be imported at start). The image is the wrong home: hooks
  are policy, and changing policy should not need a rebuild.
- **A build flag is optional and weaker.** Compiling authoring out stops that
  binary from authoring, but the transport refusal protects the running daemon
  however it was built. Prefer the rule.

## 9. Open questions

- Run against the saved tree only in v0, or build the scratch materialization
  immediately? **Answered:** both, chosen per hook by `tree`; the scratch tree
  stays the default and `workspace` adds the saved root behind the readiness
  gate.
- Is a user-scoped registry needed, or is per-workspace enough?
- Do `on-save` and `before-gate` ship with this spec, or after `agent` is proven?
- Are worktrees an accepted workspace shape (parallel tickets), given the
  Direction line?
- Should the run log persist in SQL, or is in-memory plus the existing journal
  enough?

## 10. The `cycle` hook (L3, `scripts/raj-cycle.sh`)

`cycle` turns a landed wave into a running build. The user authors the row on
the host; this section documents it and does not install it:

    raj hook add cycle --agent --tree workspace --detach --may-write \
      --timeout-ms 1800000 -- sh scripts/raj-cycle.sh

- `--tree workspace` runs at the saved workspace root, gated on readiness, so it
  only ever builds what the user saved.
- `--detach` is required: the run restarts the editor that started it, so its
  output goes to `<state dir>/hook-runs/<run-id>.log` (§4.1).
- `--may-write` because it writes `bin/`, rebuilds images and copies into
  containers.
- `--agent` makes it callable by name (`raj hook run cycle`) from an allowed
  connection.
- 1800000 ms (30 min) bounds the whole check + build + restart.

The script runs in the workspace root and stops at the first failure with a
named exit code:

| exit | name | step |
|---|---|---|
| 3 | status-not-ready | `raj ctl status` is not ready |
| 4 | lock-held | another cycle holds the lock |
| 5 | check-failed | `make check` |
| 6 | build-failed | `make build` or the `bin/raj-linux` cross-build |
| 7 | image-failed | a `harness/Dockerfile.<name>` image build |
| 8 | cli-copy-failed | `docker cp` into a running container |
| 9 | announce-failed | the "restart in Ns" broadcast |
| 10 | restart-failed | `raj daemon restart` |
| 11 | report-failed | the closing broadcast (the work above still happened) |

Steps, each one timestamped line: 0 `raj ctl status` and the lock (the lock
records the holder pid; a lock whose pid is gone is taken over); 1
`make check`; 2 `make build`, the Linux cross-build (`GOOS=linux GOARCH=amd64
CGO_ENABLED=0 go build -o bin/raj-linux ./cmd/raj`) and each image in
`RAJ_CYCLE_IMAGES` (default `opencode`; allowed `opencode`, `opencode2`,
`claude`, `codex` —
`harness/Dockerfile.<name>` tagged `<name>-box`, built from the repo root, as
`harness/harness-functions.sh` does); 3 `docker cp bin/raj-linux
<id>:/usr/local/bin/raj` into every running container from any harness image
(`opencode-box`, `opencode2-box`, `claude-box`, `codex-box`) — the ids are
captured BEFORE the build, by image tag and previous image id, so a rebuilt tag
cannot hide a running container (no restart needed for the CLI); 4 remember the
live agents, then `raj ctl send --to
all "cycle <run>: editor restarts in 20s"` and sleep 20; 5 `raj daemon restart
--workspace ${RAJ_CYCLE_WORKSPACE:-raj}`; 6 poll `raj ctl who --live --json` for
up to 120 s until every remembered agent is back — compared by durable
`identity`, not display name, with anonymous `tok_`/`anon` rows ignored, so an
agent that reconnects under a new name still counts; 7 one summary line per step to
stdout and a closing `send --to all` with the result. Containers that need a new
image, not just the CLI, are listed as "restart to pick up the new image";
recreating them is L4, not this script.

`--dry-run` prints the steps and the commands it would run, executes nothing,
and exits 0. `scripts/raj-cycle.test.sh` (the `cycle-test` Make target, run by
`make check` beside `guard-test`) asserts the order and the not-ready refusal
with a stub `raj` on PATH.

Environment: `RAJ_CYCLE_RAJ` (the raj CLI to drive; default `raj`),
`RAJ_CYCLE_IMAGES`, `RAJ_CYCLE_WORKSPACE`, `RAJ_CYCLE_WAIT`, `RAJ_CYCLE_REWAIT`,
`RAJ_CYCLE_LOCK_DIR`, `RAJ_CYCLE_RUN`.
