# GIT-COMPATIBILITY — read, materialise, export

Status: draft for review, 2026-09-23. The first carved spec (`NEXT-STEPS` step 1).
The boundary is already decided (`LAYERED-PROPOSALS-SPEC.md` §8 and §13); this
freezes the interfaces and *sketches* the hook consumers. Anything under
"Iteration" is explicitly open.

## 1. Scope and boundary

raj owns the in-progress change graph, provenance and review state; git owns
durable history. This spec adds no VCS of its own — it *reads* git's world and
*writes objects* into it at explicit sync points.

Raj does **not** own refs, branches, remotes, merges, the index/worktree, or GC.
"Git as the store" stays rejected (§13): the journal is the fine-grained record.

## 2. GitView — the structured read

One structured reply; nothing to parse out of text.

- repo root; `HEAD` sha; branch name, or detached/unborn;
- worktree state: `status --porcelain` as entries (path, index/worktree code,
  rename-from);
- untracked and ignored paths (what materialisation needs to include);
- optionally: merge/rebase in progress.

Refusals by name: no repository, bare repository, git unavailable.

## 3. Materialise — projection to a tree

**Input:** a projection (path → bytes) — `Project(policy)` over the journal, or a
intention selection. **Output:** a materialised tree (scratch dir), and optionally a
git tree object (§4).

- **Never touch the worktree or the user's index.** Build with `git mktree`, or a
  temporary `GIT_INDEX_FILE` plus `write-tree`.
- **Unsaved proposals are included.** The projection already holds what is not on
  disk, which is the point: a check or an exported tree can see a proposal.
- **`may_write = 0` safety.** Scratch is a reflink/clone where the filesystem
  supports it, a full copy otherwise. **Hardlinks are forbidden** — shared inodes
  would let a build corrupt the source.
- **Ignored dependencies must be present** or builds fail; include them per an
  explicit policy, not by accident.
- **Cleanup is deterministic**, and a tree is reclaimable.

## 4. Object plumbing (the commit pipeline)

```
bytes                  → git hash-object -w        → blob
entries (bottom-up)    → git mktree                → tree   (or temp index + write-tree)
tree + parents + msg   → git commit-tree           → commit
```

Object writes are **inert**: no ref moves, `HEAD` is untouched. `commit-tree` is
raj's ceiling; `update-ref`/`push` are outward and belong to a human hook. An
export writes **objects only** — blob, tree, commit — and nothing else: no note,
no `refs/notes` ref. D1 is moot here: there is no note to dangle. The `git.note`
leaf stays registered (H3), but the export path does not call it.

## 5. Registered capabilities (the hook seam)

The git service registers named capabilities; hooks resolve them without importing
git. Inputs and outputs are plain data; the registry is injectable, so hooks can
be tested against a fake git.

| capability | direction | agent default |
| --- | --- | --- |
| `materialise` | projection → tree | allowed (objects only) |
| `git.status` `git.show` `git.diff` `git.numstat` `git.log` | read | agent-callable |
| `git.blob` `git.tree` `git.commit` `git.note` | object write | human default |
| `git.ref` `git.push` | ref / remote write | human only |
| `squash` | an intention's head tree → one commit, re-parented onto the base | human default |

These are **individual leaves** (decided 2026-09-23; the names may still
iterate). A composite chains them; none of them is a subcommand dispatcher.

## 6. Hooks sketch — ITERATION

The consumers, sketched; names and granularity are open, this is the point of
iteration.

- **Leaves** are one capability each; **composites** compose leaves under
  `HOOKS-SPEC.md` §5 (ordered steps, fail-fast, `RAJ_STEP_<name>_OUT` chaining,
  per-leaf gating and cooldowns).
- `git-context` *(read, agent):* `[git.status]` → the fields a run record stamps.
- `intention-diff` *(read, agent):* `[resolve, materialise, git.diff(base)]` → the MR
  diff; `git.numstat` for churn.
- `intent export` (H4; the WAVE-PLAN's `intention-export` composite, objects,
  human default): `[resolve, materialise, git.blob, git.tree, git.commit]` → a
  commit sha, no note, no ref.
- `intent publish` (H5; the WAVE-PLAN's `intention-publish` composite, outward,
  human only): `[intent export, squash, git.ref]`, or a push/PR. `squash`
  re-parents the head's tree onto the base, so the MR is one commit by default;
  `--keep-history` skips it.
- **Context in:** intention id, base ref/sha, a projection *descriptor* (not a handle),
  tension report.
- **Tension gate:** export refuses (or warns) when the intention's spans overlap
  another intention's, using the journal's `Invalid`/`Moved`; the human resolves before
  publish, so an exported tree is already clean.

Iteration points:
- **Decided 2026-09-23:** the git capabilities are individual *leaves* (§5), not
  one `git` capability with a subcommand.
- **How `GitView` surfaces** (open). One internal `GitView()` function is the
  source; the transport is separate. Order of arrival: register `git.status` as
  a capability first (no protocol change, hooks get it immediately), and add a
  structured control reply only when an agent-facing consumer needs it without
  going through hooks. Adding fields to an existing reply has precedent
  (`hRoots` on `ping`/`buffers`), so a reply need not cost a whole new verb.
- **Projection currency** (open, recommended). The hook context carries a
  projection *descriptor* (intention id, base, opaque projection id), never a live
  handle. The builtin `materialise` leaf resolves it to a path; later steps read
  that path from `RAJ_STEP_materialise_OUT`. This keeps materialisation lazy — a
  status-only hook builds no tree — and keeps every leaf, Go or shell, on plain
  data. The projection registry is in-process and private to the app.
- **Where `intent export` lives:** a workflow question to settle in
  `CHANGES-ORGANIZATION.md`; deferred here.

## 7. Re-anchor and reconciliation

`base` ties the journal to a working-tree revision; the base digest detects a
pull, a checkout, or another writer. Follow `LAYERED-PROPOSALS-SPEC.md` §7: on
mismatch, **archive and restart**, never silently merge. A future explicit
re-anchor may replay non-overlapping changes onto a new base; never automatic.

## 8. Provenance

**Provenance belongs in the raj journal, not in git.** A run record carries
`{head, dirtyDigest}` from `GitView`; the task and agents that produced a commit
are read from the journal, not from a git note. An export writes no note at all
(D1 is moot: there is no note to dangle). The `git.note` leaf stays registered
(H3) but the export path does not call it.

**The export record** is bookkeeping that ties an intention to the objects it
wrote. Each export writes:

    intention / groups  →  commit_sha, parent_commit_sha, base_commit_sha, tree_sha, time

It is not a provenance record, and it is one row per export, not per op. There is
no chain: each export is one commit parented on the base **ref's head** (no
stacks: one MR per wave), so `parent_commit_sha` is the base commit and a
re-export writes a sibling, not a descendant. With it, both directions are
answerable: a commit names the groups (and so the buffers and revisions) that
produced it, and a revision's `blob_sha` (see `LAYERED-PROPOSALS-SPEC.md` §7) can
be matched against a commit's tree to find where it landed. A commit spans files;
this record is what holds them together.

## 9. Open questions

- Is a **worktree** an accepted workspace shape? It affects hook scoping and
  materialisation, and the `TODO` Direction line only rules out worktrees for
  conflict *navigation*.
- Where `GitView` surfaces (§6 iteration).
- `commit-tree` or `export-tree` as the ceiling.
- Materialisation cost on a large repo: cache a seed tree, or copy each time?

## 10. Not covered here

Hooks' own contract is `HOOKS-SPEC.md`; intentions, tension and publish actions are
`CHANGES-ORGANIZATION.md`. This spec fixes only what git offers to them.

