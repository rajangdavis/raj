# Claim — design note and verb spec

Status: built and live 2026-09-13/14 (rebuilt and host-verified). Supersedes
the earlier enforced/TTL version. `claim`/`-add`/`-clear`, Guard enforcement,
and `open -create` auto-extend are in the binary; see COMPLETED.md.

## 1. Purpose

An agent declares the file-level working set it is about to edit. Reads are
free; socket **text writes are restricted to the claimed set**; a pathless
write is allowed only when exactly one file is claimed. This is the deliberate
replacement for "the omitted path silently targets the human's active tab"
and for a swarm colliding silently. Doc-only; it does not claim spans (that
stays `Session.Leased`'s reactive job).

## 2. Model

- A claim set is per **identity** (the explicit `register`/`-as` key), not per
  connection and not per app.
- Let n = size of the set. Enforcement (socket writers only; the human UI is
  never gated):
  - n == 1: a pathless write targets the one claimed file; an explicit write
    is allowed only for that file.
  - n == 0: ALL socket writes are refused ("claim a file first"); a pathless
    write too. This is strict on purpose.
  - n > 1: a pathless write is refused ("claim set has N files; name one");
    an explicit write is allowed only if the path is in the set.
- Reads are never gated; a pathless read keeps the focused-buffer default.
- Claims are **not locks**: two identities may claim the same file, and
  `claim` reports other live claimants. Overlapping *text* is still caught by
  the span lease (`change set N owns this text`).
- The set is in-memory, per identity, and resets on editor restart (journal
  persistence is a later work item, section 7).

## 3. Verb surface

- `raj ctl claim <path>...` — set the working set (replace). Relative/absolute/
  editor spellings resolve as every verb does; each path is validated in-root.
  A path that is neither on disk nor an already-open buffer is warned per-path
  and skipped; a path that exists on disk, or that is already open (a buffer
  created with `open -create` before it is saved), is claimed and the command
  succeeds.
- `raj ctl claim <dir>` — a directory operand walks the subtree and claims each
  file under it (a snapshot at claim time; a file created afterwards is not
  auto-claimed). The directory path is also held as a set entry, so `rmdir
  <dir>` is one `claimCheck` on the dir itself (see
  `docs/FILE-LIFECYCLE-SPEC.md` §11).
- `raj ctl claim -add <path>...` — extend the current set.
- `raj ctl claim -clear` — release it.
- `raj ctl claim` (no operands) — report the current set (and other live
  claimants).
- `open -create <path>` — creates the buffer AND auto-extends the claim with
  `<path>` (when the set was empty this is `{path}`). The create itself is the
  declaration of intent; no second command.
- Response shape (proposal): `Claims []string`, `ClaimWarnings []string`,
  `ClaimOverlaps` (identity name + author id + path).

## 4. Enforcement

- Server-side in the `Guard` (the validation chokepoint), so `run -prog`'s
  apply cannot bypass it — not only in the CLI.
- "Write" = text-mutating socket verbs: `apply`, `edit`, `patch`, and the
  apply reachable through `run -prog`.
- The Guard resolves the effective target (explicit path, or the sole claimed
  file for a pathless write) and checks set membership before touching the
  buffer.
- Refusal text names the set size/remedy, e.g. "not in your claim set (a.go,
  b.go); `claim -add <path>`" and, for n > 1 and pathless, "claim set has 2
  files; name one".
- `save` is the human approval gesture, not an agent write. Recommended
  treatment: a socket `save` is NOT gated, because it writes the accepted
  composition and is the user's decision — still an open question (section
  10).
- The existing per-author read-before-write gate is unchanged and still
  applies on top.

## 5. Create and directories

- `open -create` auto-extends (section 3).
- A created file whose parent directory does not exist is a mkdir problem —
  see the lifecycle plan; for now the parent-dir handling of `open -create` is
  an open implementation point.

## 6. File lifecycle

`mkdir`, `delete` and `rename` are built (2026-09-13/14); `rmdir` is specced
(`docs/FILE-LIFECYCLE-SPEC.md` §11) and being built. The concrete, built
semantics live in `docs/FILE-LIFECYCLE-SPEC.md`; the proposed rules below are
kept as the decision history they became:

- `mkdir <dir>` — create a directory (with missing parents). Dirs are not text
  and are not claimed; proposed rule: allowed anywhere under the workspace
  root.
- `rmdir <dir>` — remove a directory. Proposed rule: allowed only if every
  file under it is claimed (or the dir is empty); never a recursive delete of
  unclaimed files.
- `delete` (alias `rm`) `<path>` — remove a file. Proposed rule: allowed only
  for a claimed path, and **as a review decision, not immediate**, matching
  "propose deleting a.go or b.go, but nothing else" — enumerate this as the
  recommended default with the immediate alternative noted.
- `rename <old> <new>` — proposed rule: allowed only for a claimed path; the
  claim follows the new name (or the rename is refused if the caller cannot
  re-claim).
- State the shared open questions: are directories claimable at all; does a
  file claim imply its directory; is a delete a proposal or immediate; does
  rename update the set automatically.

## 7. Persistence

- Now: in-memory per identity, reset on editor restart; the agent re-claims
  after a restart.
- Later (explicitly deferred): persist the claim set through the op log /
  `Registry.Seed` so a durable identity's claim survives a restart, with a
  decision on expiry/invalidation. Note `docs/TODO.md`'s registry/durability
  item.

## 8. Implementation sketch

- Per-identity claim state in the app (a maps from author id → set), alongside
  the Guard.
- New request op (`claim`) and response fields; the path resolver must take the
  author so it can apply the n==1 pathless rule and the membership check (the
  same author-threading the read-gate change used).
- Wire change → host rebuild.

## 9. Work items (proposed order)

1. `claim`/`-add`/`-clear`/report verb, per-identity in-memory state, CLI +
   tests.
2. Enforcement in the Guard + the n==1 pathless rule + refusal messages +
   tests.
3. `open -create` auto-extend.
4. File lifecycle verbs (`mkdir`/`rmdir`/`delete`/`rename`) with claim gating —
   its own design pass before code.
5. Journal persistence of claims (later).
6. Docs: skill/agent briefs and README.

## 10. Open questions

- delete immediate vs review proposal.
- are dirs claimable; does a claim imply the parent dir.
- rename: does the set follow automatically.
- is a socket `save` gated.
- how overlap is surfaced to a second claimant (`who`, or a future `watch`
  push).
- interaction with `open -create` when the parent directory is missing.
