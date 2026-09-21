# Layered proposals — spec (draft)

Status: draft for review, 2026-09-11. No code changes attached. Supersedes
nothing; extends the "Layered proposals" and "Control socket" directions in
`docs/TODO.md` and the Review-mode design in `docs/INVESTIGATIONS.md`.

## 1. The problem in one sentence

Today a proposal's **decision and its text are the same fact**: an agent's apply
lands in the document, and rejecting it *reverses* it, so the decision has to
succeed as an edit. Everything expensive about the review path falls out of
that: a reject can be wedged by a later edit, a moved hunk becomes invisible but
blocking, a restart loses the whole set of decisions, and there is no answer to
"what would this file be if I accepted only some of it?".

## 2. The change: state first-class, text a record

A change set already carries `GroupState` (`Accepted`, `Proposed`, `Rejected`).
Keep that, and redefine what `Rejected` means:

- **Today:** "has been reversed" — the text is gone.
- **Proposed:** "is in the document but is not part of the composition that
  would be saved." The text stays; the decision is metadata.

Concretely, the behaviour asked for:

- `reject` marks the set `Rejected`, changes its tint, and **does not touch the
  text**. No `reverseGroup`, so it cannot fail.
- `accept` marks it `Accepted`. The text never moved either way.
- The **agreed composition** — what `save`, `read` from a driver, `exec` and the
  future git layer see — excludes `Rejected` sets.
- The **view composition** — what the human sees — depends on the mode: edit
  mode shows accepted + proposed (rejected and invalid hidden), review mode
  shows every state, annotated.

The three compositions this yields:

| composition | contents | used by |
| --- | --- | --- |
| edit-mode view | accepted + proposed (rejected absent) | the normal editor |
| review-mode view | every state, annotated | Review mode |
| agreed | accepted only | `save`, `read`, `exec`, git |

This decouples three questions that are currently one: *what happened* (the
journal), *what was decided* (the state), and *what is agreed* (the
composition).

## 3. What already exists (the reason this is not a rewrite)

The substrate is largely there and was built for exactly this:

- `internal/piecetable/oplog.go` — `Op` is a replace of `Del` pieces by `Ins`
  pieces at `Pos`, with `Author`, `Group`, `Seq` and `Kind`. Pieces are records
  into append-only stores, so an op's inverse costs no copied bytes and the
  history is complete.
- `internal/piecetable/session.go` — the append-only journal, `rebase`/`carry`
  (carrying an offset through every later op), `reverseGroup` (all-or-nothing,
  addressed by group), and `live(seq)` derived from reversals rather than a
  flag.
- `internal/piecetable/groups.go` — `GroupState`, `MarkGroup`, `Groups`,
  `DiffPending`, `Pending` (auto-rejects a set with no surviving run).
- `internal/editor` — `PendingMarks`, `File` wrapping the session.
- `internal/app/review.go` — accept/reject/cycle/picker over the above.

The missing piece is conceptual: **`live` is currently the only input to what
the text is, and group state is not part of it.** Layering makes liveness
depend on a *policy* over group state, and makes the document a projection of
the journal under that policy.

## 4. The core invariant (do not break this)

Every offset in the journal is recorded in the coordinates of the version it
was applied at — the **view frame**, which includes every op, whatever its
state. There is one timeline. A composition is a **deterministic projection** of
that one timeline, never a second document with its own offsets.

This is why `rebase`/`carry` already exists and why it should carry the
projection too: mapping a position from the view frame into the agreed frame is
the same walk, with excluded ops contributing their length but not their bytes.

## 5. Projection

Define one API in `piecetable`:

    type Policy uint8 // AcceptedOnly, AcceptedAndProposed, Annotated
    func (s *Session) Project(p Policy) Composed

- `AcceptedOnly` — the agreed composition. Include `KindEdit` members whose
  group is `Accepted` only; `Proposed` and `Rejected` groups are excluded.
  A `Rejected` group is treated as absent: its `Ins` pieces are not in the
  composition and its `Del` pieces are.
- `Annotated` — the view: every op as applied, plus the per-run state needed to
  tint it.
- Positions map through the existing `carry` walk: an excluded op's `Delta`
  still moves later coordinates, because later ops were written in a frame that
  contained it. Excluded bytes are dropped, not the coordinate shift.

  1a defines this only for journals where no included op sits inside an excluded
  op. In production that case cannot arise: the leases in §12.1 make spans
  disjoint, so the projection never chooses an anchor. Overlap is 1c invalidation
  reporting, not a composition rule.

Resolved 2026-09-11:

1. **Undo/redo versus decisions.** Decisions are **not** in `cmd+z`. They are
   toggles: `cmd+ctl+m` accepts and `cmd+ctl+/` rejects the set at the caret,
   flipping its state; `cmd+ctl+k` clears rejected (hard-purges the set).
   Nothing about a decision enters the edit timeline, and `AcceptGroup` no
   longer refuses a set that was once rejected.
2. **Overlap — promote nested edits — SUPERSEDED by §12.1: overlap is prevented by leases and reported as `Invalid`.** When an included op sits inside an
   excluded one, the excluded set's surviving runs drop and the included text
   re-anchors at the boundary: the region a later writer typed into becomes
   theirs (the D4 moved-hunk rule, promoted from display to composition). A
   genuine content dependency — an included replacement whose `Del` is made of
   excluded pieces — is reported as a conflict, never cascaded or clamped. The
   representation already supports this: `removeRange` splits a piece and the
   flanks keep their author's store span.
3. **`live` under a policy.** `live(seq)` is "no live reverser"; the projection
   needs a sibling "in this composition", and the two must not contradict.
4. **Fuzzing.** Project a random journal at random policies and compare against
   a naive fold, as the doc/oracle fuzzers already do.

## 6. Consumers

- **`read`** — returns the session view, with `--annotated` adding the state
  runs. It reports the session version, so a driver's offsets must be session
  coordinates for a later `apply`/`diff` to base on (decision 3). (TODO's
  "which composition does read return?" is settled.)
- **`save`** — writes `AcceptedOnly`, never proposed text. For phase 1 it keeps
  refusing while anything is proposed, so cmd+s still means what it means today;
  the composition split is visible as `save`/`exec` (`AcceptedOnly`) versus the
  screen (accepted + proposed), and accept/save stay separable gestures.
- **`exec`** — materialise `AcceptedOnly` and run against that; the stale-run
  counter finally has a composition to check against.
- **LSP** — sync `AcceptedOnly` under a policy, or the compiler sees rejected
  text. This is one of the sharp edges; decide per-feature.
- **`search`** — searches the view (the human's screen), but hits inside
  rejected spans should be labelled, not silently returned as live.

## 7. Persistence

The journal plus decisions is the artifact that survives a restart. It holds:

    base        the origin bytes the log started from: path + hash + content
    store       each author's appended piece blobs, append-only
    journal     the ops, append-only
    decisions   group id -> state
    authors     author id -> identity string, name, kind
    session     tabs, cursors, scroll, focus (today's session.json)

**Engine: deferred.** Phase 0 writes a dependency-free, append-only log: each
record length-prefixed and checksummed, decoded until the first short or bad
record on load, so a crash's torn tail truncates there. SQLite
(`modernc.org/sqlite`, pure Go) stays the target once the schema has proved
itself; the record interface is what isolates the engine choice.

**History is kept from the start.** `base` is fixed when a file first becomes
dirty under raj, and a `save` writes the accepted composition to disk but does
**not** truncate the log. Restore replays the whole log, so the layered state —
proposed, rejected, accepted — comes back, not only what is on disk.
Checkpointing (a periodic snapshot plus a log suffix) is deliberately deferred;
it is the later lever for bounding replay, rebase and memory, and keeping the
prefix on disk is what makes the log forkable.

**Two hashes, not one.** The origin hash detects a workspace that moved under
the editor (a checkout, a pull). A second marker records the hash of what was
last written to disk, so an external edit after a save is distinguishable from
the base itself drifting.

**Write tap: app-level pull, with flush-on-decision.** A debounced task reads
`OpsSince(persistedVersion)` per dirty file — plus each author's store growth —
and appends. The snapshot of ops, blobs and cursor is taken on the event thread
(or under the session lock), because an op must never be persisted without the
store bytes it references. Typing rides the debounce; `accept`, `reject`,
`save`, `close` and process exit flush immediately, so decisions are durable the
moment they are made. This keeps `piecetable` pure and the event loop free of
per-keystroke I/O.

**Restore:** load base + store blobs + replay the journal + decisions. On a
hash mismatch (neither the origin base nor the last-written digest) do not
silently merge: **archive the log and start a fresh one** whose base is the
current disk bytes, and say so. Keeping the mismatched log live is what makes a
later local edit land in a frame it was never recorded against, and a replay
then ambiguous; the archived log stays for inspection. Silently replaying onto
changed bytes is the one unacceptable outcome.

**Baseline a restored buffer at its saved version.** `Written` records the
digest of the bytes written; it also carries the session version they were
written at, so a buffer restored from a log whose disk matches the last write
baselines *clean* instead of dirty.

**This store closes four open TODO items at once:** dirty-buffer restore,
attribution across restarts, proposal-review state across restarts, and unnamed
buffers (keyed by a generated id rather than a path).

**Forkability:** copying the store forks a session. That is the "op log as the
shareable, forkable artifact" direction, and what makes an agent's work
inspectable after the fact.

## 8. Git

Git is long-term history; the journal is fine-grained provenance and review
state that git structurally cannot hold. Keep the boundary clean:

- **Read-only first.** `git show HEAD:<path>` to get the committed text (no
  checkout, no index writes) and diff it against the `AcceptedOnly`
  composition. This is the change gutter and `raj ctl diff -vs HEAD` (TODO).
- **Commit later.** A future stage/commit writes the `AcceptedOnly`
  composition; until then, the user's normal git flow sees the saved file.
- **Reconciliation.** `base` ties the journal to a specific working-tree
  revision. Anything that rewrites the file out from under the editor (pull,
  checkout, another tool) is detected by the base hash and handled as in §7.
- **Out of scope for the first pass:** staging, committing, branch management,
  and using git itself as the persistence store.

## 9. Rendering

- Tints per state: accepted (author tint, as today), proposed (existing agent
  tint), rejected (a distinct, lower-emphasis treatment — dim or struck),
  invalid (dim with a stale marker, since it cannot be accepted). Rejected and
  invalid are drawn **only in review mode**; outside it they are hidden as
  atomic folds, per §12.1–§12.3.
- Gutter: author initial + state; a rejected deletion and an accepted insertion
  must be distinguishable, which is where the change-gutter TODO and deferred
  deletions land.
- A view toggle for "hide rejected" and a review mode that walks decisions as
  states rather than as text positions.

## 10. Attachment: loaded vs announced

A buffer's **document state** and its **presentation state** are separate
concerns, and only the second should scale with what is on screen.

- **Tier A — document state** (journal, stores, line index, version). Every
  *loaded* buffer, updated on every op. O(newlines) per op; it must stay current
  because byte offsets, LSP ranges and agent diffs depend on it.
- **Tier B — presentation state** (wrap layout, syntax spans, pending marks,
  inlay hints, column maps). Only for **announced** panes. Invalidated by a
  version bump; derived **once on announce** at the current version; dropped
  when unannounced. Stale derivations are dropped by generation — the pattern
  `syntax.Highlighter` and the LSP client already use.

A pane is **announced** when it is revealed, pinned, or holds a pending
proposal the user must see. A hidden tab accumulating agents' edits then costs
an index update and a dirty flag, not a retokenise, a layout, a hint fetch or a
frame.

Threading follows from the split: the piece table is mutable shared state and
stays single-writer on the event thread, but *derivations* move to workers —
compute, park the answer, `Notify`, install iff the generation still matches.
Compositing a frame stays on the event thread and gets cheap because Tier B is
already computed.

Consumers to audit so nothing scales with *open tabs × op rate*: the per-search
snapshot of every dirty buffer (`Search.Buffers`), the per-idle-tick `stat` of
every open tab (`diskCheck`), and the all-tab session write.

### Headless read

The same split gives agents read access without a tab. `open` (control) today
conflates *load* with *announce*; separate them:

- a **headless buffer** is loaded, tracked, addressable over the socket, and has
  Tier A — but no tab and no Tier B;
- a tab appears only when it has to: a pending proposal is created, the user
  opens it, or `open` is asked to show it.

This is the TODO's "inspection should not force a tab; proposals should", and it
makes close-when-clean automatic rather than agent-disciplined.

Eviction: a headless buffer with no pending decisions and no unsaved content is
droppable and reloadable, and with the journal on disk, dropping the in-memory
model costs a replay rather than the work. A never-shown buffer still has a
durable op-log, so an agent's work stays inspectable. Announcement is also the
natural subscription boundary: the announced set is what a notification stream
tells a client about.

## 11. Migration, in increments

Each step is independently shippable and testable.

0. **Persistence first** (no semantic change): journal + decisions to the
   append-only log, restore on start. **Verified live 2026-09-11**:
   crash-restore, save-then-restore and external-change skip all behaved. What
   remains are the log-rotation-on-mismatch and saved-version baseline above.
1. **Reject becomes a state flip + tint**, with `Project(AcceptedOnly)` feeding
   `save` and `read`. Retires `reverseGroup` as the reject mechanism (it stays
   for undo).
2. **Change gutter** vs `HEAD`, built on `Project(AcceptedOnly)`.
3. **Annotated read / exec / LSP composition** as their consumers are decided.
4. **Git verbs** (read-only diff first) if still wanted.

The projection algorithm is the risky part; steps 0 and 1 are otherwise small
because the journal, groups, rebasing and attribution are already built. The
regret about not planning this at the start is real, but the cost was mostly
paid forward into a substrate that happens to be the right one.

## 12. Decisions

Resolved 2026-09-11:

1. The first increment is **phase 0 (persistence)** — behaviour-neutral.
2. A rejected span is visible **only in Review mode**; outside it, rejected
   text is absent from the view.
3. `read` returns the **session view** — the whole document, proposed and
   rejected text included, in the session's coordinates — with `--annotated`
   adding the state runs. (Corrected 2026-09-16: the original "defaults to
   `AcceptedOnly`" predated the lease/version model and cannot hold — `read`
   reports the **session** version, so a driver's offsets must be session
   coordinates for a later `apply`/`diff` to base on. `AcceptedOnly` is what
   `save` and `exec` use.)
4. Decisions are **not undoable**; accept/reject toggle the set, and
   `cmd+ctl+k` clears rejected.
5. `save` writes **`AcceptedOnly`** and never proposed text.
6. Rejecting an outer layer **promotes** a nested edit (survivor); a genuine
   content dependency is reported as a conflict.
7. **Engine deferred, history kept.** Phase 0 is a dependency-free append-only
   log; `base` is the origin and a save does not truncate the history.
8. **Write tap is app-level pull with flush-on-decision,** snapshotting ops and
   store growth on the event thread.
9. **Attachment:** document state is separate from presentation; a pane is
   announced by reveal, pin, or a pending proposal, and a headless buffer can be
   loaded and read with no tab.

Resolved 2026-09-12 (supersedes 2, 4 and 6; retires the promote rule):

1. **Leases warn, not wall: Proposed is advisory, Rejected is locked.** A
   rejected or invalidated span is an atomic read-only run, and an edit or an
   agent `apply` whose range intersects one is refused (a status note in the
   editor, a conflict over the socket). A span that is merely `Proposed` is a
   draft, so an agent `apply` whose range intersects *another* writer's Proposed
   run is allowed: the editing author's ops land in their own new group (never
   the original set), and the original set's overlapping members are left moved
   past what a rebase can carry; the successful apply -- and the agent `patch`
   that shares its diff path -- also **warns**, naming
   that set by group, author and the span it held when the hunk landed, so the
   caller is told rather than left to find the overlap in `groups`/`diff` later.
   Its `Moved` count remains the record for the review surface. A `Rejected` set
   still refuses and wins over any Proposed run the same
   hunk also catches, because a rejection is the human's decision rather than a
   draft. A writer amending its **own** `Proposed` set still joins that set
   instead of opening a second one (`Session.ApplyDiff` -> `commitInto`), but
   only when every `Proposed` run the hunk catches is the writer's own. A hunk
   that also catches another writer's `Proposed` run -- or any `Rejected` run --
   is not a clean amendment and refuses, naming the writer's own set, and it
   does so whichever run the projection meets first. When that set is the
   caller's own draft, the CLI words the refusal as such and names the remedy
   the caller actually has -- narrow the hunk off the peer's text -- because a
   writer cannot accept or reject its own proposal; a refusal that names a peer
   keeps the accept/reject wording. The advisory path is
   therefore taken only when every caught `Proposed` run belongs to another
   author, so run order never changes the outcome (`Session.proposedSpans`
   supplies the evidence).
   The human typing path (`EditLeased`) is unchanged and
   still treats a Proposed run as read-only. Overlap through the agent path is
   therefore advisory rather than prevented; where an accepted edit consumes a
   proposal's inserted run, the agreed composition is the undefined overlap
   §12.3's `Invalid` flag resolves, and the projection does not yet.
2. **Rejected and invalidated spans are hidden from the edit view and annotated
   in Review mode.** Edit mode shows accepted + proposed; Review mode shows every
   state with its annotation; `save`/`exec` still see `AcceptedOnly`, while
   `read` returns the session view (decision 3).
3. **Invalidation is orthogonal and recomputed.** A still-`Proposed` set can be
   proposed and stale at once: its recorded edit no longer fits the current
   composition (a later accepted edit consumed its `Del`, two proposals touched
   the same base, the disk moved, an un-reject can no longer restore). This is
   the `Invalid` flag, not a fourth `GroupState`; it is derived from current
   state, not stored in the journal, so it clears when the colliding edit goes
   away. An invalidated span is excluded from the edit and agreed compositions
   and annotated in Review.
4. **Chords.** `cmd+ctl+k` hard-purges rejected sets; `cmd+ctl+l` hard-purges
   invalidated sets. Decisions remain not undoable.

Still open:

- **Store path and forkability.** The state dir is `.raj/`, always, repository
  or not. Forkability of the store is still open, as is how the journal might
  be exported; SQLite remains deferred behind the record interface.

## 13. Alternatives considered

- **Keep reverse-on-reject and only persist decisions.** Far smaller, but keeps
  the wedged reject, the moved hunk and the "decision must succeed as an edit"
  coupling; the persistence would be persisting a mutation whose purpose was to
  make the text match a decision. Rejected.
- **Two documents (view + agreed).** Straightforward text but two coordinate
  systems and two things to keep consistent; the invariant in §4 is exactly what
  this sacrifices.
- **Git as the store (commit every change).** Durable but no per-op provenance,
  no review state, and history noise; the journal is the right granularity.

## 14. The `proposals` rollup (2026-09-15)

Built in buffers 2026-09-15; host verification pending. One verb to list
everything an agent has proposed and is awaiting the human on, instead of one
verb per kind. Implemented as a read-only rollup (`App.Proposals`) behind the
CLI `raj ctl proposals [--mine]`; `--json` is one flat tagged list. This section
folds in the former standalone proposals-listing note.

### Purpose

The pending surface is three disjoint listings today: `groups` (change sets,
with `--mine`), `deletions` (pending file deletions), and — after the `rmdir`
wave — `rmdirs` (pending dir-removals). A driver asking "what have I proposed,
and what is left for the human to decide" must call all three and merge by
hand. `proposals` is that merge.

### Scope

Lists only the true proposals — work that waits on a human decision:

- **change sets** — `groups` where state is `proposed` (path, author, group id,
  span or lines).
- **pending deletions** — `deletions` (path, author).
- **pending dir-removals** — `rmdirs` (dir, author).

`mkdir`, `create` (`open --create`) and `rename` are immediate and do NOT
appear. If "every mutation is a proposal" later becomes the direction (option
B from the design review), those verbs surface here; until then they stay out.

### Verb

- `raj ctl proposals` — list every pending proposal, all authors.
- `raj ctl proposals --mine` — this identity only.
- `--json` — machine shape: a single flat tagged list, `{kind, path, author,
  ...}`, so a driver sorts by whichever axis it cares about. Human output
  groups per kind under a short header.

### Entry shape

Each entry carries `kind` (`set` | `delete` | `rmdir`), the `path` (for
`rmdir`, the directory), the proposing `author`, and the kind-specific field: a
`group` id plus span or lines for a change set. This is a read-only rollup over
existing state — no new store; it reads `Session.Groups`, `pendingDeletions`
and the pending dir-removals the way their own verbs do.

### Open questions

- ~~`--json` single payload vs `--jsonl`~~ — decided 2026-09-15: one flat `--json`
  payload, no `--jsonl`; the rollup is bounded by the pending set.
- Whether a single tab or picker should drive accept/reject across all three
  kinds (the human side), instead of the per-kind prompts that exist today.
  Still open: `proposals` is read-only, so approval stays per kind for now.

## References

- Code: `internal/piecetable/{oplog,session,groups}.go`,
  `internal/editor/{file,proposals,write}.go`, `internal/app/{control,review,mode}.go`,
  `internal/control/{host,control,cli}.go`.
- Decisions: `docs/INVESTIGATIONS.md` ("Review and edit modes", "The line index
  and the document disagreed after a batch", "The timing instrument wrote to the
  terminal").
- Work items: `docs/TODO.md` — SQLite session store, dirty-buffer restore,
  attribution across restarts, proposal-review state across restarts, deferred
  deletions, change gutter, `diff -vs HEAD`, "Which composition does `read`
  return?", layered proposals.
