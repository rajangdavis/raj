# Reconciliation UX — direction

How overlapping agent edits and the human's become visible and resolvable.
Companion to the "Direction" section in INVESTIGATIONS.md (2026-09-13); not
scheduled, and it inherits the working agreement at the top of RECURSIVE_RAJ.md.

## Decision (2026-09-15)

The editor-side part of this direction is **decided against**, for editor
simplicity: no layer toggles and no conflict navigator in the TUI. Reconciling
a wave's agent changes is the job of the between-wave **review pass**
(`docs/REVIEW-AGENT.md`, `docs/RECURSIVE_RAJ.md` §7b), the arbiter of "this set
of changes is the correct changes."

The long-term navigation direction stands: conflict navigation **keyed to git
diffs** — reviewing history and the working tree — rather than the
composition's overlap reports. What does not change is that *resolution* stays
native: the piecetable composition, the lease and the review pass, not work
trees or git-branch manipulation. Git is history and navigation; it is not the
merge mechanism.

## The pivot

Today the composition is a fixed policy over one document: the edit view shows
accepted + proposed, Review shows everything annotated. Reconciliation
generalises the policy to **selecting layers**: base, accepted, each agent's
overlay, proposed, rejected. The view is whatever that selection composes to,
and a conflict is intersecting rebased ranges reported to both authors (TODO
"Overlap reporting for swarms"), never clamped. The F3b-ii projection already
turns a policy into display rows and folds, so this generalises existing
machinery rather than adding a second document.

## Visual models

- **A. Layer toggles on one document** (smallest). A chord or picker flips
  which layers are composed; the view recomposes; a gutter chip marks each
  span's layer. This is "toggle between" without a second document.
- **D. Conflict navigator** (cheap, complementary). A hunk list that walks
  conflicting ranges, previews the other side inline, and offers keep-mine /
  keep-theirs / keep-both.
- **B. Side-by-side panes** (merge tool). Two compositions with synced scroll
  and per-hunk accept. Needs split panes.
- **C. Three-way merge.** base | ours | theirs | result. Most powerful, most
  work.

Recommended start: **A + D**.

## What it implies

- Data: composition = select(layers); conflicts are first-class reports.
- Rejecting or merging can move text and the caret, and re-placing a reversed
  span can wedge — the failure the current state-flip design retired. Layer
  selection makes that movement explicit; the projection map keeps the caret
  honest.
- Agent verbs must speak layers: propose-against-a-layer, read-a-composition,
  list-conflicts. The existing claim/watch and overlap-reporting items are the
  foundation; design the verbs after the verb-surface audit so we don't
  simplify the surface twice.

## Open questions for the user

- Does a toggle compose one agent layer at a time, or arbitrary subsets?
- Is a conflict resolved by choosing a side only, or by editing the merge?
- Does the human's own text have a layer, or is it always the base?
