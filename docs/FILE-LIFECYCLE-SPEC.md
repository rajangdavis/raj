# File lifecycle — design note and verb spec

Status: built 2026-09-13/14 (rebuilt and host-verified). Companion to
`docs/CLAIM-SPEC.md` (the claim gate the lifecycle verbs are gated on). W4a
(`mkdir`), W4b (`delete` + prompt gate + `RAJ_TRASH`) and W4c-1 (`rename`/`mv`
for files) are in the binary; W4c-2 (`rmdir`/folder deletion) and directory
rename remain.

## 1. Scope and staging

- **W4a `mkdir <dir>`** — creates a directory with parents, under the workspace
  root only; directories are not claimed, so it is ungated. In tree 2026-09-13.
- **W4b `delete <path>`** — a *review primitive* (this note). A file is not
  unlinked until the user approves, because a deletion has no text-diff
  representation and is not recoverable like an edit.
- **W4c `rename`/`rmdir`** — W4c-1 (`rename` files) landed 2026-09-14; W4c-2
  (`rmdir`/folder deletion) remains deferred. Both reason about open buffers,
  so `rmdir` gets its own pass.

## 2. Pending deletions

- A **workspace-level** set of paths an agent has proposed to delete. It is not
  a change-set group: a group is text inside one buffer, while a deletion is a
  path-level fact that outlives any buffer.
- `delete <path>` records a pending deletion for that path and does **not**
  unlink. It is idempotent (a second `delete` of the same path is a no-op).
- Each pending deletion carries the proposing author, so the prompt can name
  who asked and `who` can show it.

## 3. The prompt is the gate

- When a path with a pending deletion is **opened or focused**, the editor
  raises a prompt: *"An agent proposed deleting <file>."* with two answers:
  **Ignore for now** and **Remove forever**.
- If the path is **already open** when the proposal lands, the prompt is raised
  immediately when that buffer is the active one; a background tab gate waits
  until it is next focused, so the editor never steals focus to a file the user
  is not looking at.
- **Ignore for now** leaves the proposal pending; the file works normally and
  the prompt returns the next time it is focused. (A permanent dismiss/reject
  answer is a later addition, not v1.)
- **Remove forever** is offered only when the buffer is **clean** (saved, no
  unsaved changes) and holds **no pending change sets**. When it is not, the
  prompt says why and offers only Ignore. There is **no force path**: discarding
  a writer's pending work to satisfy a deletion is precisely what the gate
  exists to prevent.
- On Remove forever: move or unlink the file (section 4), drop the buffer, and
  clear the pending deletion.

## 4. Trash

- `RAJ_TRASH=1` (and only that value) moves the removed file to `.raj/trash/`
  under a timestamped name instead of unlinking. Any other value, or unset,
  is a hard unlink.

## 5. Gating

- `delete` requires the path to be in the caller's claim set (the W2 gate), so
  an agent can only propose removing a file it declared. Reads are unaffected.
- `delete` is a socket write of a path, not of text; it must still run through
  the `Guard` so `run -prog` cannot bypass the claim check.

## 6. Verb surface (built — delete/deletions; rmdir in §11)

- `raj ctl delete <path>` — record a pending deletion (claim-gated).
- `raj ctl delete -withdraw <path>` — the proposing agent withdraws it.
- `raj ctl deletions` — list pending deletions (path, author), so a driver
  can see them without opening the file. (A timestamp is a later nicety.)
- Approval is the **human prompt** (section 3). No socket accept/reject in v1:
  the whole point is that the human, on seeing the file, decides.
- `run -prog` reachability is out of scope for v1 (as with `mkdir`).

## 7. Folders

- Specced 2026-09-14 as `rmdir` (section 11); the review "tab" won out over the
  earlier review-mode popup sketch. A read-only tab lists the subtree and a
  footer offers `[remove] [cancel]`.

## 8. Work items

1. Pending-deletion state in the app (workspace-level, with author), plus
   `deletions` listing.
2. `delete` / `delete -withdraw` verbs, wire, and the claim gate through the
   Guard.
3. The prompt on open/focus and for already-open buffers; Ignore / Remove
   forever; refuse Remove forever while dirty or pending.
4. `RAJ_TRASH=1` -> `.raj/trash/`.
5. Docs: skill and README.

## 9. Open questions

- A permanent dismiss/reject answer (v1 is Ignore only).
- Whether a driver should ever be able to accept/reject over the socket, or the
  human prompt stays the only gate.
- Where pending deletions surface: `who`, the status line, or both.
- `run -prog` reachability, punted with `mkdir`'s.

## 10. W4c — `rename` (decided 2026-09-13; W4c-1 built 2026-09-14)

Staged: **W4c-1 files**, then W4c-2 directories. `rename <old> <new>` (alias
`mv`):

- `old` must be in the caller's claim set (the W2 gate); `new` must be under
  the workspace root and must not already exist. The claim set follows
  `old -> new`.
- If a buffer is open for `old`:
  - **clean** (no unsaved changes, no pending change sets) -> carry it: rename
    the file, forget the old path journal log (the buffer is clean, so nothing
    is lost), update the pane path, drop the old path LSP doc and diagnostics
    (the new one re-syncs lazily), refresh the tree, and touch the session so
    the tab follows;
  - **dirty or holding pending sets** -> refuse and say so; the caller resolves
    first. No force.
- **Case-only rename** (`a.go` -> `A.go`): on a case-insensitive filesystem
  `os.Rename` can no-op, so use the two-step (`old -> temp -> new`) and update
  the pane path case. Detect it by `strings.EqualFold(old, new)` with the names
  unequal. The "destination already exists" check exempts `os.SameFile(src,
  dst)`, since on such a filesystem the new spelling resolves to the source.
- Not open -> plain `os.Rename`.

## 11. W4c-2 — `rmdir` (specced 2026-09-14; to be built)

`rmdir` is `delete` widened from a file to a directory: the same propose-then-
review discipline, the same pending-state shape, the same prompt-gate question,
but the unit is a subtree and the approval surface is a read-only tab rather
than a one-file prompt.

**Verb surface** (mirrors `delete`):

- `raj ctl rmdir <dir>` — propose removing the directory and its subtree;
  records a pending dir-removal (with author), unlinks nothing. Idempotent.
- `raj ctl rmdir -withdraw <dir>` — the proposing author retracts it; no-op
  when absent, refused for a proposal from another author.
- `raj ctl rmdirs` — list pending dir-removals (dir, author).
- Approval is the human **remove** answer in the review tab; no socket
  accept/reject in v1. `run -prog` reachability is out of scope, as with
  `mkdir`/`delete`.

**Claim gate.** `claim <dir>` records the directory path as a set entry and
also walks the subtree to claim each file under it (a snapshot at claim time —
a file created under the dir afterwards is not auto-claimed). `rmdir <dir>` is
one `claimCheck` on the directory itself. Decision 1, option (a)-with-auto-
expand, over "every file
individually claimed" (onerous) and over ungated (unsafe); the same choice is
recorded in `docs/CLAIM-SPEC.md` §3.

**Pending dir-removals** are workspace-level, in-memory, per author, keyed by
directory path — a sibling map to `pendingDeletions`, since the unit is a
subtree not a path. The path list that would be removed is computed when the
review tab is raised, so it names what is actually there.

**The review tab** (decision 2, option c). A read-only tab lists every path the
removal would take, one per line. A footer prompt offers `[remove] [cancel]` —
no global chord is claimed, and the tab is the record of what is at stake.
`cancel` clears the pending proposal; `remove` proceeds to the safety check.

**Safety (all-or-nothing).** Before removing, every file under the directory is
checked: a dirty buffer (unsaved text) or pending change sets under the subtree
fails the whole removal, with the reason, and there is no force path — the same
`deletionSafe` rule as `delete`, widened to the subtree. An empty directory is
the degenerate case: nothing under it, so the check passes vacuously.

**Trash (whole directory).** `RAJ_TRASH=1` renames the whole directory into
`.raj/trash/` under a timestamped name (one `os.Rename`, recoverable as a
unit); any other value, or unset, is `os.RemoveAll`. Open buffers under the
tree are dropped the way `closeDeletedPane` drops a deleted file buffer, and
the tree and session are refreshed.

**Left for later.** Per-entry (partial) removal; a permanent dismiss answer; a
socket accept/reject; `run -prog` reachability; directory rename.
