# NEXT-STEPS — where we are, and the goal behind it

## Where we are

Written 2026-10-01 from the code and from git, not from memory: the last
commit is `702a0aaa` on `main` (2026-09-30), with 17 files changed since and
not committed (14 edited, 3 new). Each line below says what it was checked
against. This pass ran no tests and could not read the hook run log
(`raj hook log` was denied to the reviewer), so nothing here claims a passing
run that it did not see.

This section is the one place that answers "where are we". The plan
(`docs/dev/WAVE-PLAN.md`, which is not in git) keeps its commit stamp and
points here. That arrangement is proposed by the reviewer, not a rule of the
owner's. His instruction, relayed 2026-10-01 by `raj-f070b379`: "have claude do
a document review pass TODO's => completed, NEXT_STEPS where are we?"

### Live (in the last commit)

- **Agents propose, you decide.** Unchanged: only you accept, save, approve a
  deletion or approve a publish.
- **Agents can check their own work before you look.** They run the `check`
  hook on a copy of the tree that includes their proposed text, and a proposal
  cannot change what that check runs. Checked: `internal/control/materialise.go:174`,
  `internal/control/control.go:2536`.
- **Changes can be grouped into a named seam, reviewed as read-only diff tabs,
  turned into a commit, and published when you approve.** Checked: the
  `internal/intent` package (`export.go`, `publish.go`, `seam.go`, `land.go`)
  and the `intent review` entry in `docs/COMPLETED.md`.
- **Deleting, renaming and restoring files works from the editor itself.**
  A delete goes to the workspace trash and cmd+ctrl+z puts it back; ctrl+alt+v
  lists everything waiting on you; a file deleted outside the editor is marked.
  Checked: see the last section of `docs/COMPLETED.md` for the file and line of
  each.

### In flight (written, not committed)

- **Build a seam from one task's changes in one command** (`raj ctl intent
  group --task T`). The code and eight tests are in the uncommitted files
  (`internal/intent/group.go`, `intentGroup` in `internal/app/intent.go`,
  `internal/app/intent_group_test.go`). It is not in `docs/COMPLETED.md`, and
  the plan's status block listed its files without saying what they do. Unverified: whether
  any check run has passed with it.
- **Say what publishing a seam would push, before pushing** (`raj ctl intent
  next`). Written and recorded in `docs/COMPLETED.md` with check run 162. The
  full cycle after it, run 163, failed on a shell-script lint error. The one-line
  fix for that error is in the uncommitted files
  (`examples/hooks/no-ignored-source.test.sh`). A later full cycle passed, by
  another agent's report (raj-f070b379, 2026-10-01): cycle `20260930-172533`,
  run 169, check and build ok at `702a0aaa`, and the workspace shell lint
  passed in run 168. The reviewer has not read those runs.
- **Review fixes to the delete and remove-folder work.** What is uncommitted
  in `internal/app/deletion.go`, `rmdir.go` and their tests is comment wording
  only (the trash is now always used; the comments still described the old
  opt-in). If the review asked for more than that, it is not among the 17 files.
- **This document pass.** Proposed edits to this file, `docs/TODO.md`,
  `docs/COMPLETED.md` and the plan, waiting for you to accept and save.

### Next

Taken from the plan's status block; an order the agents proposed, not one the
owner is on record choosing.

1. A passing full cycle on the tree as it stands. Reported done by run 169
   (see "In flight"); confirm it covered the 17 files as they are now.
2. Commit the uncommitted work.
3. The cleanup queue in `docs/dev/DEBT-PLAN.md`.
4. The container setup for running the editor and agents (the plan's Track R).

### Waiting on you

- **Accept or reject this pass's document edits.**
- **The 17 uncommitted files are more than one piece of work.** Split them
  before committing, or commit them together?
- **Which Go version is right?** CI pins 1.24 (`.github/workflows/ci.yml:20`);
  `go.mod:3` says 1.25.0.
- **Where should a merge request point when the branch has no upstream?**
  Today the publish script has nothing to fall back on (`docs/TODO.md`, the
  publish artifact review).
- **Should rejecting a text change remove the text in one step?** Today
  rejecting marks it and a second step clears it.
- **One open question looks already answered by the code.** The plan asks
  whether you want the workspace trash or a key that puts the last deleted file
  back. The tree has both. If that is what you wanted, the question can go.

## Background: the goal review of 2026-09-23

Everything from here down was written 2026-09-23 as a review artifact, not a
commitment. It is kept for the reasoning. Where a status line below disagrees
with "Where we are" above, the section above is right.

## The goal

Let agents swarm on code design — many working at once — while keeping the
result organised and safe to land.

- **Swarm without stomping.** Agents propose changes rather than editing files
  directly, each attributed to a task and working in its own space, so none can
  overwrite another's work.
- **Conflicts are expected, and resolved by the agents.** Overlap is shown, not
  merged silently. The agents involved, a dedicated resolver, or the wave's
  review agent are summoned with the conflict, see each other's changes, and
  propose a reconciliation; the human approves it. Attribution makes it clear
  whose change clashes with whose, and why.
- **Partition cleanly into version control.** Changes are grouped into logical
  intentions; an intention materialises to a tree and exports to a commit on
  request.
  Publishing a branch is a separate, human decision — raj owns no branches.
- **Accountable to the session.** Every change carries the session/task that made
  it, so a conflict or bug can be traced back and **summoned**: the responsible
  agent is handed the problem with its context and asked to fix it. Summoning is
  a human decision, and if that agent is gone it is recreated from the recorded
  intent and change.
- **Efficient because the human organises, not edits.** Grouping, resolving and
  publishing are the human's; the agents do the volume. Tension reports point at
  the few places that actually conflict instead of at everything.

The three services deliver it: `HOOKS-SPEC.md` runs and gates the actions,
`GIT-COMPATIBILITY.md` reads git and materialises trees, and the planned
`CHANGES-ORGANIZATION.md` owns intentions, tension, publish and summon.

## 1. Where each thread stands

The table that stood here was dated 2026-09-23 and is replaced by "Where we
are" at the top of this file.

## 2. The convergence

Every live thread turns out to be about **organising changes**:

- layers **group** decisions,
- hooks **verify** changes,
- git **persists and reconciles** them,
- workspaces **isolate** them,
- `proposals` **lists** them.

What is missing is one first-class object underneath all of that.

## 3. The proposed next push: changes as first-class, organisable objects

A minimal vocabulary:

- A **change** is a proposal set (`Group`) plus its state and author — it already
  exists in the journal.
- A **layer** is an ordered, named set of changes over a base. It is a
  *projection policy*, so it needs no second document and no duplicated offsets
  (the §4 invariant).
- **Operations**: list, name, group, stack, materialise, export, reconcile.
- **Materialise** is the shared primitive: it serves hooks (`HOOKS-SPEC` §6),
  `diff -vs HEAD`, and layer export to a git tree.

The clean boundary stays: raj owns the change graph, provenance and review state;
git owns durable history. Compatibility is *reading* git's world and *writing*
clean trees into it at explicit sync points — never owning refs, branches,
remotes, merges, index or GC.

## Direction: intentions, export and publishing

The design that was headed for two separate specs. It stays here until the wave
that implements it is scheduled.

- **An intention is the organising object.** A named, owned selection of groups
  over a base (a git ref, or another intention). Agents fill intentions; the
  human defines them. `Materialise(intention)` projects the selection to a tree,
  and the §4 invariant makes it deterministic without a second document.
- **Export is inert; publish is reviewed.** Export writes objects only
  (`hash-object -w` → `mktree`/`write-tree` → `commit-tree` → `git notes`), so it
  runs freely and is reclaimable. Publish is outward (`update-ref`/`push`/MR), so
  it is an **action proposal** — the same family as `delete`/`rmdir`: propose,
  see it in `proposals`, review the diff and tension, `accept` runs the hook,
  `reject`/`--withdraw` clears it.
- **Tension is shown at review.** Overlap between intentions (the `Invalid`/`Moved`
  machinery generalised), so the human resolves in-journal before anything is
  published; exported trees are already clean.
- **Git plumbing is a builtin hook library**, not new verbs: read-only leaves
  (`git.status/show/diff/numstat`) agent-callable; object-writers
  (`git.blob/tree/commit/note`) human by default; outward leaves (`git.ref/push`)
  human only. `publish-intention = [resolve, materialise, git.blob, git.tree,
  git.commit, git.note, git.ref]` is a composite the user tunes.
- **Provenance rides with the commit:** a `git note` carries the task id and
  agents, joining git history to the DB task key.
- **N agents, M intentions:** agents produce proposals attributed to tasks; the
  human groups them into M intentions; each intention exports independently and
  in parallel; stacked intentions chain parents. raj never owns refs, branches,
  remotes, merges, index or GC.

## Direction: harness adapters and multi-harness recursion

Carved into `docs/dev/HARNESS-ADAPTERS.md` (2026-09-24): the adapter contract, the
stage/hook recursion and the convergence feature. The sequencing below still
applies.

## Direction: workspace host, attach and concurrent views

Folded from `docs/ATTACH-DESIGN.md` (2026-10-01); the source design note is
archived. One workspace-scoped **host** owns the model; later `raj` instances
attach to it rather than starting a second editor. Detach/reattach is the
mobile story (an SSH drop or a screen lock becomes a detach, not a lost
session) and concurrent clients are the phone-and-laptop story. Stage 2 has
landed as a **local-render client**: `raj --attach` loads the daemon's tabs
from `snapshot`, follows `watch`, and owns its cursor and viewport but no model
(`internal/app/client.go`); decisions proxy back. Concurrent views (stage 3)
and detach/reattach remain unscheduled.

- **The host is an explicit daemon (decided 2026-09-18).** `raj --daemon` runs a
  workspace headless and owns the model, session and control socket with no
  terminal, so agents can drive it and clients can attach. `--visible DIR`
  (repeatable, or `--workspace DIR`) scopes what the workspace exposes.
- **Discovery.** A second `raj` in the same workspace finds the host; a stale
  socket is told apart from a live one by a handshake, not by mtime. The socket
  lives under `$XDG_RUNTIME_DIR/raj/`, keyed by the workspace state key.
- **`--attach` means mirror the daemon (decided 2026-09-20).** There is no
  review-tabs mode: an attached client mirrors the daemon's real tabs
  continuously, adopts any host buffer holding a pending change set, and keeps
  both current. The `--review-tabs` flag and
  `Options.ReviewTabs`/`ReviewTabsSet` are removed; `ProfileFlags(phone,
  ctrlAliases, ctrlAliasesSet)` resolves only the
  `--phone`-implies-`--ctrl-aliases` rule.
- **A local client close is a snooze, not a mute (decided 2026-09-20).** It
  hides the tab until that buffer's facts (version or review state) change,
  then it returns. Closed marks are per client and saved with the per-client
  view, with a legacy list-of-paths fallback that re-adds the path because it
  carries no facts to match.
- **The phone profile puts review first:** the queue of pending change sets, a
  compact diff and a decision row, with printable-key commands, never a chord
  or a swipe.
- **Stage 3 — concurrent views (unscheduled).** Option B (thin clients render
  locally and sync through ops, each owning its cursor, viewport and
  projection) or option C (one host, many sized views at per-client sizes),
  only after the input/render protocol exists and has been lived with. A
  second *writer* is not the hard part; a second *viewer* is. Per-client
  viewports and projections are a renderer project, not plumbing.
- **What that stage needs:** detach/reattach with the model untouched;
  input/render channels and per-client size; agents keep the control socket
  while a client is attached, and a host with no client still runs; the local
  Unix socket stays the trust boundary (a TCP attach is a separate decision).

Open questions (from the source; none scheduled):

- **Socket home.** `XDG_RUNTIME_DIR` (per-login, cleared on logout) versus the
  XDG state dir (survives). A stale socket must fail loudly, not hang.
- **Host with no client.** Keep owning the model for agents, or exit?
- **One client or many** in v1. One with a clear refusal is the safe default; a
  read-only mirror is the cheap second step.
- **Detach gesture.** A chord on the laptop, a motion on the phone — the
  motions probe is what tells us which gesture is available.
- **TCP attach.** A remote console is a different security posture; do not
  assume it.
- **Startup.** `raj` becomes the host when none exists and `raj --attach` fails
  when none does. Whether the host is the process that owns a client terminal
  or a separate daemon was the first thing to decide; 2026-09-18 resolved it as
  the daemon.

Not in scope: tmux parity (windows, panes), remote multi-host, collaborative
cursors, and session branching/forking.

## 4. Sequencing (dependency order)

Checked 2026-10-01: steps 1 to 3 are built, not ahead of us. Step 1 is the
`internal/git` package and `raj ctl git`; step 2 is `raj hook`; step 3 is the
`internal/intent` package. Step 4 is still undecided. The list is left as
written.

0. **Soundness first — done (2026-09-23).** The invalid-set Phase 1c gaps are
   closed: an invalid set is named, counted and disposed, and a save disposes
   one rather than silently dropping it. Everything is *organising changes*;
   changes must be sound before they are organised.
1. **`GitView` + `Materialise`** (read-only git; projection → tree). The shared
   substrate. Also closes the long-standing "a saved wave has no enumerable
   diff" gap and lets an agent see git without a checkout.
2. **Hooks v0** (agent-triggered, gated, gated again). Uses `Materialise`.
3. **Change organisation** — name/group/stack layers, and surface them in the
   session/review surface; export to a git tree on request.
4. **Workspace isolation** (multi-ticket) — decide worktree vs native, now that
   changes are organisable.

## Evidence: metrics by run (2026-09-23)

Baseline for the batching argument: what an agent actually calls, measured
across all history.

- **How to get it:** `scripts/call-runs.mjs` reads `opencode.db` directly
  (assistant `message.data.parentID` → user message = the task), so it needs no
  plugin, no ledger and no rebuild, and it backfills all history. In the
  container the baked CLI is `call-runs`; flags `--by task|session|agent`,
  `--subagents`, `--json`.
- **Measured over 1,822 runs (all history):** 49,427 tool calls; median 5
  calls/run; p90 105; 196 runs over 100 calls (10.8%); total cost $171.80; 6.24B
  tokens.
- **The dominant cost is read + search:** 47,092 of 49,427 calls = 95.3%.
- **Batching adoption is uniformly low:** multi-target `read A B C` 374/23,657
  = 1.6%; `search --context` 700/23,435 = 3.0%; `apply --hunks` 102/1,995 = 5.1%;
  multi-path `lsp diagnostics` 234/4,284 = 5.5%.
- **Stamp:** these counts drift as sessions accumulate; re-run `call-runs`
  before quoting a figure.
- **Reading:** the workload is search-then-read and the forms that collapse it
  are almost unused. The uniform low rate points at *discovery* (an agent does
  not know what to batch) more than ergonomics, so the actionable item is a
  discovery-shaped surface fix, not only a discipline note.
- **Task/prompt fields:** `prompt` is dropped from `plugins/raj-gate.ts`
  (privacy, and it had no consumer); `task` is kept for now, decision open.
  `call-runs.mjs` is the attribution source; the ledger's fields are otherwise
  redundant.
- **Open action:** turn the batching-adoption finding into a `TODO` item once the
  discovery fix has a concrete shape.

## 5. Decisions needed before implementation

Checked 2026-10-01: questions 1 and 5 have been overtaken by the work (hooks
and seams are built, and a hook runs on a materialised tree). No dated answer
from the owner was found for questions 3 and 4, so they are left open as
written.

1. Confirm **organising changes** as the next push, ahead of the `TODO` Now bugs
   and the phone profile.
2. ~~Sequencing: soundness (step 0) first, or `GitView`/`Materialise` first?~~
   **Resolved (2026-09-23):** soundness (step 0) landed first, so there is no
   longer a soundness wave to sequence around; step 1 follows.
3. Which surface: extend the session sidebar / review mode, or a new "changes"
   pane?
4. Is a **worktree** an accepted workspace shape? (`TODO` Direction says "no work
   trees or branch hackery" — that line is about conflict navigation, but the
   workspace question needs an explicit answer, not an inherited one.)
5. `HOOKS-SPEC` §9 open question 1: saved-tree-only v0, or build materialisation
   immediately? This decides whether `run` is useful to an agent on day one.

## 6. Artefacts, and when they are carved

Specs are carved from this doc when a wave is scheduled; they are that wave's
contract, not a home for unscheduled ideas.

1. ~~**Soundness wave (step 0):** no new doc — the `TODO` items are the contract.~~
   **Landed (2026-09-23):** step 0 needed no carved doc and is done, so nothing
   remains to carve for it; items 2-4 below are the specs still to come.
2. **`GitView` + `Materialise` (step 1):** carve `docs/GIT-COMPATIBILITY.md` when
   that wave starts.
3. **Hooks v0 (step 2):** `docs/HOOKS-SPEC.md` already exists and is the contract.
4. **Change organisation + publishing (step 3):** `docs/CHANGES-ORGANIZATION.md`
   is carved (2026-09-23).
5. `TODO` updates as decisions land; `COMPLETED.md` as work lands.

## 7. Working constraints to keep in view

- **Proposals are not durable until accepted.** The earlier consolidated notes
  were lost in a restart because they were never saved. Specs written here are
  proposals too; they must be accepted and saved to survive.
- **Rebuild boundary.** Buffer edits cannot make new verbs live: propose →
  accept/save → host rebuild (`make`) → verify against the running binary. State
  this every wave.
- **The container has no git checkout.** That is not a bug to work around; it is
  the reason `GitView` over the control channel matters.
- **raj owns the record; harnesses feed it.** The journal, decisions and manifest
  are raj's. A harness (opencode today) supplies the task key and the analytics —
  the ledger, `opencode.db` — and the key is an opaque string, so another harness
  can feed the same manifest. Treat those as derived, not the keeper of the
  truth: "fixing the session logs" means fixing raj's manifest. The one real
  dependency is prompt *resolution*; store a digest plus text to make the
  manifest self-contained.
