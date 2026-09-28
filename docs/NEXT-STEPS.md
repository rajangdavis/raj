# NEXT-STEPS — a review pass

Date: 2026-09-23. A review artifact, not a commitment. Nothing here is
implemented or scheduled until you say so.

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

| Thread | Status | Artefact |
| --- | --- | --- |
| Layered proposals | Spec'd (draft 2026-09-11 + §12 decisions); phase 0 persistence landed; the invalid-set Phase 1c gaps landed (2026-09-23) | `docs/LAYERED-PROPOSALS-SPEC.md`, `TODO` Now |
| Hooks (host commands) | Spec written 2026-09-23, awaiting review | `docs/HOOKS-SPEC.md` |
| Git compatibility | Spec carved 2026-09-23 (`GIT-COMPATIBILITY.md`); the hook sketch is the iteration point | `docs/GIT-COMPATIBILITY.md` |
| Change organisation | Spec carved 2026-09-23 (`CHANGES-ORGANIZATION.md`): intentions, tension, export, publish, summon, resolve | `docs/CHANGES-ORGANIZATION.md` |
| Workspace / multi-ticket / multi-agent | Designs exist; the workspace shape is unsettled (`no work trees`) | `HARNESS-BROKER-AGENT.md`, `ATTACH-DESIGN.md` |
| `TODO` Now list | 5 open items (phone profile, save-review lag, shape-only external edit, hover panel, …) | `docs/TODO.md` |
| Career / marketing | Separate track, not this repo | jobsearch notes |

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

Carved into `docs/HARNESS-ADAPTERS.md` (2026-09-24): the adapter contract, the
stage/hook recursion and the convergence feature. The sequencing below still
applies.

## 4. Sequencing (dependency order)

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
