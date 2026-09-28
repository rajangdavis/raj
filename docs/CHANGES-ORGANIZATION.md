# CHANGES-ORGANIZATION — intentions, tension, publish, summon

Status: draft for review, 2026-09-23. The third service spec (`NEXT-STEPS` step 3).
It owns **intentions** and the workflow **actions**; it owns neither the journal
(`LAYERED-PROPOSALS-SPEC.md`) nor git (`GIT-COMPATIBILITY.md`) and reaches them
only through their interfaces.

## 1. Boundary and dependency direction

```
journal/proposals  ←  this service  →  action proposal  →  hooks  →  git capabilities
   (layered)          (intentions, actions)     (review/accept)    (spine)      (leaf)
```

This service does not import the journal, git or hooks. It calls their
interfaces with plain data: a projection policy, a capability name, an action.
No cycles.

## The workflow

Five phases; the human gates the first and the fourth, the orchestrator runs
everything between.

1. **Plan (human + planner agent).** Decide the intentions: each a named scope
   with a base, and the stack order when they chain. Scopes are the human's; the
   planner agent may propose them.
2. **Swarm (n agents + m subagents).** Each proposes, attributed to its
   task/intention, in its own space. Agents may run agent-allowed hooks (`check`)
   against a materialised projection.
3. **Review and orchestrate (the review agent).** One phase, four jobs:
   **reconcile** tension between intentions (never auto-merge); **summon** the
   responsible subagents to fix what they caused; **partition** per the plan and
   **export** each intention (inert objects); **report** — dispose stale,
   superseded and invalid sets, pay debt, write the ledger.
4. **Publish (human).** Approve the publish action; the hook pushes. The only
   outward step.
5. **Learn.** Churn and complexity per area feed a refactor signal; a recurring
   intention's call pattern becomes a skill, then a deterministic plugin.

The review agent is an **orchestrator**, not a cleanup pass: reconcile, summon,
partition, export, report. Its duties are the first thing to codify into hooks
once the pattern is stable.

An intention is built **incrementally**: plan creates it (possibly empty), the
swarm fills it, the orchestrator exports checkpoints, and publish squashes (§5).
The pending proposals are the un-exported tail.

## 2. Intentions

An **intention** is a named, owned selection of groups over a base, stored with
exactly these on-disk fields:

    { name, owner, base, members[group ids], state }

- **Membership is explicit.** Groups join an intention; nothing is grouped by time or
  by file.
- **Base** is a git ref (e.g. `master`). A base that names another intention is
  out of scope (no stacks: one MR per wave); the chain code is left inert and
  DEFERRED.
- An intention is a **projection policy**, so `Materialise(intention)` is deterministic
  (`LAYERED-PROPOSALS-SPEC.md` §4).
- Operations: new, add/remove members, list, show, export, materialise. The ctl
  surface is exactly `raj ctl intent new|add|remove|list|show|export|materialise
  --dry-run`; `intent publish` is H5.
- **An intention is also what you learn from.** Its call pattern is already
  recoverable — `scripts/call-runs.mjs` groups calls by task — so a kind of
  intention that keeps recurring can be codified: first a skill, then, when the
  pattern is stable, a deterministic plugin.

## 3. Tension

Overlap between intentions, computed in the view frame, plus the journal's existing
`Invalid`/`Moved` signals.

- **Disjoint** — stack freely.
- **Touching** — advisory; warn at review.
- **Conflicting** — needs a resolution (§7).

Tension is a **report**, shown when an intention is reviewed or exported. It never
auto-merges, and it never rewrites anything.

**Over time it is also a refactor signal.** An area that keeps attracting work
*and* complexity is worth a look: `scripts/call-runs.mjs` gives the churn, and the
duplication/complexity checks in `TODO.md` give the complexity. Hot and complex
proposes a refactor intention, rather than arguing about whether a 1000-line file
is fine.

## Stacking and the dependency graph *(deferred: no stacks — one MR per wave)*

An intention may be **stacked**: its base is another intention rather than a
ref, so `materialise` sees the parent's composition. A stacked series is a
reviewable set of merge requests with nothing committed.

Stacking is a relation between intentions, derived from proposals and the
journal in the same view frame tension uses — not a second document.

### Two relations, only one branches

- **Base — a forest.** An intention has at most one base (a ref or another
  intention). This is the materialisation relation; single-parent is what keeps
  `Materialise` deterministic (one base tree + one selection). A linear stack is
  just a path; independent intentions are separate roots. A multi-parent base
  would need three-way merge semantics and is deferred.
- **Dependency — a DAG.** An intention may depend on any number of others,
  independent of its base. Edges are:
  - **overlap** (cheap, derived): groups whose spans intersect, or that a later
    edit moved past — the existing `Invalid`/`Moved` signal, lifted from one
    buffer to cross-buffer, cross-intention;
  - **semantic** (deferred, analysed): an intention reading a symbol another
    introduces, from the symbol index.

Linear stacks stay the common case; the DAG is what lets independent intentions
share a dependency, and what a rejection propagates through.

### Invalidation: block, re-anchor, or resolve

When a group in intention A is rejected or invalidated, every dependent is
re-evaluated. The outcome is not deletion:

- **Overlapping dependent** — the rejected span is one it edited, so it cannot
  be projected without it. It is **blocked**: excluded from export, still
  visible and recoverable, derived and self-clearing like `Invalid`
  (`LAYERED-PROPOSALS-SPEC.md` §12.3).
- **Non-overlapping dependent** — stacked on A but not touching the rejected
  span. Its changes can be **re-anchored** onto the new base. But
  `GIT-COMPATIBILITY.md` §7 says re-anchor is never automatic, so it is an
  **action proposal** the human approves (the `delete`/`publish` family),
  computed by the graph and never applied silently.
- **Independent** — untouched.

Nothing is destroyed: a blocked intention's groups stay in the journal, and
re-accepting the rejected group can clear the subtree.

### Derived, like tension

Every edge and every blocked/re-anchorable verdict is computed from the current
journal and decisions, never stored. As with `Invalid`, it clears when the
collision goes away. The graph is a projection of the one timeline
(`LAYERED-PROPOSALS-SPEC.md` §4).

### Before anything is committed

The graph locates tension ahead of the commit, at three increasing costs:

1. **structural** — overlap edges name the exact contested spans,
   transitively, without materialising anything. First cut.
2. **semantic** — the symbol edges, once the index exists.
3. **behavioural** — materialise the stack and run the check hook on it; a
   build or test failure in the combined projection is the strongest signal and
   costs no commit.

### Ordering and the export gate

Export walks the DAG in dependency order, so a dependent's parent chain is
real. `proposals` gains a `blocked` kind beside `export`/`publish`/`summon`: a
blocked intention is not exportable and names the dependency that blocked it.

### Graph open questions

- Is a multi-parent base (a merge intention) ever needed, or is base-forest +
  dependency-DAG enough?
- Does the graph span workspaces/roots, or one root set?
- Semantic edges: file, symbol, or region granularity?
- Is a re-anchor automatic when every dependent is provably non-overlapping, or
  always a proposed action?

## 4. Export (inert)

`export(intention)` = `materialise(intention)` → blobs → tree → `commit-tree`,
using `GIT-COMPATIBILITY.md`'s capabilities. It writes **objects only** — blob,
tree, commit — and nothing else: no note, no ref, `HEAD` untouched, worktree
untouched. D1 is moot here: there is no note to dangle.

- One commit per intention, parented on the base **ref's head** (the trunk). There
  is no chain to a previous export (no stacks: one MR per wave); a re-export
  writes another commit on the same base, it does not grow a chain.
- Repeatable and reclaimable, so it can run freely; object-writing is human by
  default but may be allowed to an agent because it changes nothing outward.
- Writes the **export record** (`GIT-COMPATIBILITY.md` §8), one row per export:
  `intention / groups → commit_sha, parent_commit_sha, base_commit_sha, tree_sha,
  time`. The record is **bookkeeping**, not a provenance record: it joins the
  intention to the object ids it wrote. Provenance (task, agents) belongs in the
  **raj journal**, not in git.

## 5. Publish (an action proposal)

`raj ctl intent publish` (`publish(intention, target)`, the WAVE-PLAN's
`intention-publish`, H5) is **proposed, not performed** — the same family as
`delete`/`rmdir`.

- Lifecycle: proposed → listed in `proposals` → reviewed (diff vs base + tension)
  → `accept` runs the hook → `reject`/`--withdraw` clears it.
- **Human only.** Refs, remotes and pushes are outward; an agent may export, not
  publish.
- Idempotent by intention id / commit sha, so a retry does not push twice.
- **Squash where possible.** Publish puts one commit per intention by default:
  `squash` re-parents the head's tree onto the base (`commit-tree`), so the working
  chain stays as provenance but the MR is a single commit. `--keep-history` keeps
  the chain for an intention that is genuinely several commits. A stacked
  intention squashes onto its dependency's head.

## 5a. Publishing strategies — configurable, in order of arrival

How a body of accepted work becomes reviewable changes on a forge is the
user's choice, not raj's. A workspace setting picks the strategy and the forge;
each is a hook composition, so a team can replace any step.

**Deferred (no stacks: one MR per wave):** the `split` and `stack` strategies
below are not scheduled; start with `single`, one MR per wave.

| strategy | what is published | when to use |
| --- | --- | --- |
| `single` | everything since the base as one commit on one branch, one PR/MR | a one-off catch-up, or a solo repo |
| `split` *(deferred: no stacks — one MR per wave)* | N independent PR/MRs cut by the split planner (§5b), each based on trunk | a large body of work that should be reviewed in parts, merged in any order |
| `stack` *(deferred: no stacks — one MR per wave)* | an ordered chain of PR/MRs, each based on the previous (GitHub `gh stack`; GitLab chained MRs) | work with real dependencies between parts |

- **Forge adapters are hooks:** `local` (commits and branches only), `github`
  (`gh pr create`; `gh stack` for `stack`), `gitlab` (`glab mr create`, chained
  `--target-branch` for `stack`; `glab stack` only once it leaves experiment).
  Tokens stay on the host, where hooks run.
- **Arrival order:** `single` first (it is `export` + one branch + one PR),
  then `split`, then `stack` (GitHub, then GitLab).
- **`single` today:** `examples/hooks/publish-single.sh` builds the commit from
  the saved working tree with a private index (so `.gitignore` decides what is
  left out and the user's HEAD, index and working tree are never touched),
  parents it on the base, creates a branch that is not checked out, pushes it
  and opens the PR with `gh`. `--dry-run`, `--no-push`, `--no-pr`; its test
  runs against a throwaway repository. Authored as a hook:
  `raj hook add publish --tree workspace -- sh examples/hooks/publish-single.sh`.
- Every strategy is still an action proposal the person accepts (§5); none
  moves a ref or pushes on its own.

## 5b. Split planning — independent changes, decided and then proved *(deferred: no stacks — one MR per wave)*

The question a reviewer actually has: *can this mound of change be cut into N
parts that do not conflict and do not depend on each other, so each can be
reviewed and merged alone?* The planner answers it deterministically, shows its
reasons, and then proves the answer by building each part.

**Input:** the change set from the base (a ref) to the accepted composition —
files, hunks, and the journal's attribution (task, author) as hints, never as
truth.

**Must-stay-together edges** (a graph over files, refined to hunks where the
language allows):

1. **Conflict** — two changes to overlapping lines of one file.
2. **Definition/use** — a change defines or alters a symbol another change uses
   (new identifier, changed signature, new field). For Go: the package graph
   plus a type-checked reference pass (`go/types`, or the language server's
   references) over the changed symbols. Other languages fall back to file
   co-change heuristics, labelled as such.
3. **Build units** — a file and the tests that exercise it; generated files and
   their generators; a doc and the code it documents when the doc names it
   (docs-index-style rules the repo's own tests enforce).
4. **Hints** — the same task id pulls changes together unless an edge above
   already decided; a hint never overrides a proof.

**Plan:** the connected components of that graph are the smallest independent
parts; the planner then merges components toward a requested N (or a size
budget) by minimising cross-review cost (fewest components per part, related
paths together), and prints for every part: files, the edges that bound it,
and why it is independent of the others.

**Proof, per part:** materialise *base + that part alone* and run the
workspace's check hook on it; then materialise base + all parts in a different
order to show there is no hidden ordering. A part that fails alone is not
independent — the planner reports the failing edge (usually a missed
definition/use) and merges it with what it needs, then proves again. Only a
plan whose parts all pass alone is offered for `split` publishing.

**Negotiation:** the person (or an agent) moves a file between parts, pins
parts together, or asks for a different N; every change re-runs the proof on
the parts it touched. The accepted plan becomes N intentions (§2) over the same
base, which `split` publishes and `stack` can later order.

Open: hunk-level splitting of one file across parts (needs the conflict edge at
hunk granularity and a patch per part); how far to trust the language server's
references versus a compile-only proof; a budget for the proof (N check runs).

## 6. Summon

Given a conflict, a bug, or a commit, trace back to the session(s) that produced
it — the task key on each revision plus the export record.

`summon(session | intention, about)` composes a brief: the change, the intent (task /
prompt reference), and the problem (the tension report or the check output), then
hands it to that agent — resuming the session, or recreating it from the audit if
the agent is gone.

- **Human-triggered.** Summoning is consent, like publish.
- The agent's answer is a fresh proposal; nothing is written behind your back.

## 7. Resolve

Who resolves a conflict:

- the **agents involved**, summoned with each other's changes;
- a **dedicated resolver** agent;
- the **wave's review agent** (`REVIEW-AGENT.md` duty 3), after a wave.

A resolution is a **fresh proposal** touching the overlapping regions, attributed
to whoever wrote it. No agent silently rewrites another's accepted work, and the
human approves the result. An optional tuning: pre-authorize auto-accept when
checks pass and only agent-written text is touched.

## 8. Interfaces used

- **layered:** `Project(policy)`, the group listing, `Invalid`/`Moved`.
- **git:** `GitView`, `materialise`, the `git.*` capabilities, the export record.
- **hooks:** `Run(recipe, ctx)`, triggers, gating, run records.

All plain data. This service knows capability *names*, never their
implementations.

## 9. Surfaces

- `proposals` gains action kinds alongside `set`/`delete`/`rmdir`: `export`,
  `publish`, `summon`.
- **A Changes sidebar pane**, alongside Explorer / Search / Problems / Settings,
  listing intentions, stacks, tension and pending actions.
- **A changes page** shows one intention's diff across files, GitHub-style:
  exported history (commits) plus the pending tail. Read-only, built from `diff`
  over a materialised intention; review mode stays the in-buffer, line-level view.
- **A task tab with a linear review set, and layers folding into git (2026-09-28, user).** Each task opens as a tab listing its groups as one ordered, linear review set rendered like a git diff — per-file additions/deletions, the gutter carrying who — so a review reads as a diff instead of scattered in-buffer sets. Reviews organise into layers (intentions, §2); layers fold into the git shaping: materialise -> export -> one commit per layer, stacked MRs at publish (§5a). This is the presentation of §2 + §5, and it is what makes "one commit per MR" legible.
- **Planning** (phase 1) is where scopes and stacks are chosen: the planner agent
  proposes, the human decides.
- Works under `--phone`, like the other panes.

## 10. Open questions

- Is a **worktree** an accepted workspace shape for isolation? (Shared with
  `GIT-COMPATIBILITY.md` §9.)
- Can an intention have several owners, or one?
- How is tension computed across files — per span, or per region of interest?
- What, if anything, is pre-authorized to auto-accept?
- May the orchestrator **summon across waves** — a bug found later calling back an
  older session?
- Does partition/export need the human when the plan is already agreed? (Proposed:
  no — export is inert.)

## 11. Not covered here

The journal and projection (`LAYERED-PROPOSALS-SPEC.md`), git read/materialise
(`GIT-COMPATIBILITY.md`), and hook execution/gating (`HOOKS-SPEC.md`). This spec
owns only how changes are grouped and acted on.

