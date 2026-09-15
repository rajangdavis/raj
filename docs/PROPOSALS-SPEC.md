# Proposals — one listing over the pending surface

Status: built in buffers 2026-09-15; host verification pending. One verb to list
everything an agent has proposed and is awaiting the human on, instead of one
verb per kind. Implemented as a read-only rollup (`App.Proposals`) behind the
CLI `raj ctl proposals [-mine]`; `-json` is one flat tagged list.

## Purpose

The pending surface is three disjoint listings today: `groups` (change sets,
with `-mine`), `deletions` (pending file deletions), and — after the `rmdir`
wave — `rmdirs` (pending dir-removals). A driver asking "what have I proposed,
and what is left for the human to decide" must call all three and merge by
hand. `proposals` is that merge.

## Scope

Lists only the true proposals — work that waits on a human decision:

- **change sets** — `groups` where state is `proposed` (path, author, group id,
  span or lines).
- **pending deletions** — `deletions` (path, author).
- **pending dir-removals** — `rmdirs` (dir, author).

`mkdir`, `create` (`open -create`) and `rename` are immediate and do NOT
appear. If "every mutation is a proposal" later becomes the direction (option
B from the design review), those verbs surface here; until then they stay out.

## Verb

- `raj ctl proposals` — list every pending proposal, all authors.
- `raj ctl proposals -mine` — this identity only.
- `-json` — machine shape: a single flat tagged list, `{kind, path, author,
  ...}`, so a driver sorts by whichever axis it cares about. Human output
  groups per kind under a short header.

## Entry shape

Each entry carries `kind` (`set` | `delete` | `rmdir`), the `path` (for
`rmdir`, the directory), the proposing `author`, and the kind-specific field: a
`group` id plus span or lines for a change set. This is a read-only rollup over
existing state — no new store; it reads `Session.Groups`, `pendingDeletions`
and the pending dir-removals the way their own verbs do.

## Open questions

- ~~`-json` single payload vs `-jsonl`~~ — decided 2026-09-15: one flat `-json`
  payload, no `-jsonl`; the rollup is bounded by the pending set.
- Whether a single tab or picker should drive accept/reject across all three
  kinds (the human side), instead of the per-kind prompts that exist today.
  Still open: `proposals` is read-only, so approval stays per kind for now.
