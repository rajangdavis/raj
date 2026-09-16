# F3b-ii — the presentation half: fold representation and the display map

Status: design proposal for review, 2026-09-15. Non-normative where it describes
code already in the tree; normative where it fixes the D1/D2 boundary. Source of
truth for decisions: `docs/LAYERED-PROPOSALS-SPEC.md` §5, §9, §12.1, §13. One
coordinate system; folds are display-only; no second document.

## 0. What the tree already carries, and the one gap

The presentation half is partly built. In tree today:

- `internal/view/projection.go` — `view.Seg`, `view.DispLine`,
  `view.Build(comp, segs)`, `view.Projection`, and the row↔byte map: `DocAt`,
  `DispOfDoc`, `DispOfDocLine`, `Fold`, `At`, `RowText`, `Lines`, `Text`.
- `internal/editor/pane.go` — `Pane.disp *view.Projection`,
  `Pane.SetDisplay(policy piecetable.Policy)` (the one call to `Session.Project`),
  and the pane-level map `line`, `dispPos`, `docAt`, `snapOut`, `DispOfDocLine`,
  `Fold`.
- Projection-aware consumers already moved inside `internal/editor`:
  `motion.go` (`MoveTo`, `MoveVertical`, `AddCursorVertical`),
  `mouse.go` (`OffsetAt`, `offsetInRow`), `wrap.go` (`lineBreaks`, `RowsInLine`,
  `cursorRowCol`, `followCursorWrapped`), `render.go` (`drawFold`,
  `drawCompLine`, `placeCaret`).
- Tests: `internal/view/projection_test.go` (including `checkSessionRows`, the
  `DocAt`∘`DispOfDoc` inverse) and `internal/editor/display_test.go`
  (`roundTripOffsets`, fold rows, restored rows, multi-line folds).

The gap D2a closed (2026-09-15): **`internal/app` now reaches the projection.**
`internal/app/mode.go` `displayPolicy()` maps the mode (`ModeReview → Annotated`,
else `AcceptedAndProposed`), and `internal/app/render.go` `drawEditor` calls
`Pane.UpdateDisplay(policy)` after its nil-pane guard and before
`fitHints`/`RenderFocused`; `UpdateDisplay` memoises on
`(Session.Version(), DecisionGeneration, policy)`, so `Session.Project` is off
the per-frame path. Folds and annotations are live in Edit mode. The app still
reads session coordinates directly in the places listed in §2 below —
`drawDiagnosticMarks` uses `it.Range.Start.Line`, `drawProposalMarks` uses
`m.Line`, `drawCompletion`/`textArea` use `p.Viewport.Top`, `showCompletion`
uses `File.LineOf` — so D1's map is wired at the composition seam and unwired at
the consumer seams: moving those is D2b.

## 1. Fold representation — decision

**A fold is a display row, never a document position and never a model object.**
It is `view.DispLine{SessionLine: -1, Fold: <group>}` produced by `view.Build`,
one per `piecetable.ProjSeg` with `Hide == true`. A `Hide` run has `Len > 0,
DLen == 0`: session bytes the composition drops. A fold is not a `GroupState`,
not a lease, and adds nothing to the journal. This is what §13 means by
display-only, and it is why the "two documents" alternative stays rejected.

**Boundaries come from the projection, not the document.** `Pane.SetDisplay`
copies `DerivedProject.Segments()` (`ProjSeg.Doc/Len/Disp/DLen/Group/Hide/
HiddenLines`) into `view.Seg` and hands them to `Build`; the pane never scans
text to decide what is hidden. `ProjSeg.HiddenLines` — counted by
`piecetable.countNewlines` over the hidden store bytes — is what keeps session
line numbers correct after a multi-line fold without the display holding the
hidden text.

**What makes it atomic.** A fold has no addressable position inside it:

- `Projection.DispOfDoc(off)` maps any offset in `[Doc, Doc+Len)` to the fold
  row at column 0;
- `Projection.DocAt(row, col)` on a fold row returns the hidden run's session
  cursor (`docLo`), so a click cannot land inside;
- `Pane.snapOut(off, dir)` moves a motion endpoint to the run's leading or
  trailing edge;
- the lease closes the loop: in edit mode a fold is a `Rejected` run, and
  `Session.Leased` / `File.EditLeased` refuse any edit intersecting it.

Atomicity is therefore enforced by the lease where it must be (edits) and by the
map where it is visual (caret, mouse).

**Summarisation.** `Pane.Fold(row)` returns `(group, hiddenBytes, ok)`;
`Pane.drawFold` (`internal/editor/render.go`) draws `⋯ <state> · <N> bytes ⋯`,
with `<state>` from `Session.GroupState(group).String()`. This is the decision
for now; the exact label is an open question below.

## 2. The bidirectional map — API and call sites

Two layers, one authority.

`internal/view` owns rows↔session bytes. It does not know columns or hints —
those live in the pane:

- `Build(comp string, segs []Seg) *Projection` — `nil` iff no decisions;
- `(*Projection) DispOfDoc(off int) (line, col int)`;
- `(*Projection) DocAt(dispLine, col int) int`;
- `(*Projection) DispOfDocLine(sessionLine int) int`;
- `(*Projection) Fold(line int) (group uint64, hiddenBytes int, ok bool)`;
- `(*Projection) At(line int) DispLine`, `RowText(line int) string`,
  `Lines() int`, `Text() string`.

`internal/editor` owns the full bytes↔cells round trip (tabs, wide runes, hints,
wrap), because that is where `view.Columns` and the wrap engine live:

- `Pane.SetDisplay(policy piecetable.Policy)` — the only composition entry;
- `Pane.line(i int) (sessionLine, lo, hi int, fold bool)`;
- `Pane.DispPos(off int) (line, col int)` and `Pane.DocAt(line, col int) int` —
  **currently unexported as `dispPos`/`docAt`; D1 must export them**, because
  `internal/app` cannot otherwise consume the map;
- `Pane.DisplayLines() int` — likewise unexported today (`displayLines`);
- `Pane.snapOut` stays internal; `Pane.DispOfDocLine` and `Pane.Fold` are already
  exported, and `PendingMark.DispLine(p)` already wraps the former.

**D2 consumes only the exported `Pane` methods; it must not call `view` or do
its own arithmetic.** That is the reason the map exists.

Call sites that must move onto it, with the coordinate each uses today:

1. Caret placement — already `Pane.dispPos` in `internal/editor/render.go`
   (`placeCaret`, `placeCaretWrapped`). No change.
2. Caret movement — already `dispPos`/`docAt`/`snapOut` in
   `internal/editor/motion.go`. No change.
3. Scroll anchoring — already `displayLines`/`cursorRowCol` in
   `internal/editor/wrap.go`. No change.
4. Mouse hit-testing — already `docAt` in `internal/editor/mouse.go`. No change.
5. Diagnostics gutter — `internal/app/render.go` `drawDiagnosticMarks` maps
   `it.Range.Start.Line` to a row with `first := p.Viewport.Top` and `ln - first`;
   it must use `p.DispOfDocLine(ln)` and skip `-1`.
6. Proposal gutter — `internal/app/render.go` `drawProposalMarks` uses `m.Line`;
   it must use `m.DispLine(p)` (`internal/editor/proposals.go`) and skip `-1`.
7. Review list and jump — `internal/app/review.go` (`proposalAtCaret`,
   `proposalsVisible`, `rejectedAtCaret`, `proposalGroups`) mixes `File.LineOf`
   with `Viewport.Visible`; display coordinates must go through
   `PendingMark.DispLine`/`DispOfDocLine`.
8. Completion popup — `internal/app/app.go` `showCompletion` anchors on
   `File.LineOf`/`head-LineStart`, and `internal/app/render.go`
   `drawCompletion`/`textArea` place with `p.Viewport.Top`. Both need the pane
   map so a popup does not sit a row or column off when a fold is above it.
9. Session restore — `internal/app/session.go` stores `Viewport.Top` and restores
   it against `File.Lines()`; with folds `Top` is a display row, so persistence
   must clamp against `Pane.DisplayLines()`.
10. Inlay hint window — `internal/app/inlay.go` mixes `p.Viewport.Top` (rows)
    with `File` line APIs (session); the visible window must be converted through
    the map before it asks the server.
11. Status line — `internal/app/render.go` `drawStatus` prints `f.LineCol(...)`
    (session). Decide whether the user sees the session line or the display row;
    today it is the session line, which is probably right.

`internal/app/control.go` `Read` already builds its own `Project(Annotated)` index
for the socket's line translation. That is the composition in socket
coordinates and stays outside the display map.

**When the projection is rebuilt.** `SetDisplay` should run behind a memo keyed
on `(Session.Version(), decision generation)`, not per render call, because
`Session.Project` walks the journal. `File.decisionGen`
(`internal/editor/file.go`) is unexported today; D1 exposes it (for example
`File.DecisionGeneration()`), or the app keys on `ViewDirty`'s existing probes.
The same cache gives the lease check its runs (§3).

## 3. D1 / D2 split

**D1 (`internal/view` + `internal/editor/pane.go`) — done except:**

- export `DispPos`, `DocAt`, `DisplayLines` (and a decision-generation accessor)
  so D2 is a pure consumer;
- add the fuzz oracle below.

D1 exposes exactly: `SetDisplay`, `DispPos`, `DocAt`, `DispOfDocLine`, `Fold`,
`DisplayLines`, the internal `line`, and — for review surfaces —
`PendingMark.DispLine`. D2 computes no offsets, rows or columns itself.

**D1 fuzz oracle.** Generalize `display_test.go`'s `roundTripOffsets` into a fuzz
over a random document, a random journal of proposed sets, random accept/reject,
and wrap on/off. It must assert:

- **inverse:** for every session offset `o` not inside a `Hide` run,
  `DocAt(DispOfDoc(o)) == o`; the pane-level render/`OffsetAt` round trip holds
  for every drawn offset (`projection_test.go`'s `checkSessionRows` is the
  unit-level version);
- **atomicity:** every offset of a hidden run maps to its fold row, and `DocAt`
  on a fold row never returns an offset strictly inside the run;
- **monotonicity:** `DispOfDoc` is non-decreasing in `off`;
- **text:** concatenating `RowText` over non-fold rows reproduces
  `DerivedProject.Text()`; fold rows contribute nothing;
- **line agreement:** `DispOfDocLine(sl)` is the first row showing `sl` and is
  `-1` iff `sl` is hidden; session-backed rows report valid `SessionLine`s;
- **UTF-8:** no boundary maps mid-rune;
- **identity:** with no decisions `Build` returns `nil` and every conversion is
  the raw-session one (the current `TestNoDecisionsRendersUnchanged`).

**D2 (`internal/app`: `mode.go`, `render.go`, `review.go`, keys) — remaining:**

- [x] call the composition per frame by mode (D2a done): `drawEditor` calls
  `UpdateDisplay(policy)` — `ModeEdit → AcceptedAndProposed`,
  `ModeReview → Annotated`;
- per-state tint and annotation in Review: `Annotated` shows every state, and
  `Rejected`/`Invalid` are annotated there rather than folded (`drawFold`'s
  "rejected" fallback must not become the Review rendering);
- move every app call site in §2 onto the map;
- **lease enforcement at the edit/commit boundary, not per keystroke.** Today
  `Pane.applyEdit`/`leaseBlocks`/`Paste` and `File.Insert`/`Delete` both call
  `File.Leased`/`EditLeased`, each rerunning `Session.Project(Annotated)`
  (`File.Leased → Session.Leased`). D2 checks a user action once, at the
  `File.Begin()`/`End()` change-set boundary, reading the cached projection;
  `File.Insert`/`Delete` keep a cheap guard; `Session.ApplyDiff`'s
  `proposedSpans`/`rejectedLease`/`commitInto` remains the agent-side authority. The mode
  policy (`reviewRefuses`/`mutatesText` in `mode.go`) is not a lease and stays
  where it is.

## 3b. Not in scope: overlap/conflict navigation

F3b-ii renders the **single-document composition** — accepted, proposed, and
rejected-as-folds — and nothing about conflicts between writers. Overlap is
prevented by leases; reconciling a wave's changes is a between-wave agent pass
(`docs/REVIEW-AGENT.md`), and conflict *navigation* (over git diffs, eventually)
is a separate, unscheduled direction. Do not read this note as promising a
navigator, layer toggles, or a merge surface in the editor.

## 4. Open questions for the user

1. **Fold summary.** Is `⋯ rejected · N bytes ⋯` the right marker, or should it
   preview the first hidden line, name the author, or say `N hidden lines`?
   Should an `Invalid` fold (phase 1c) read differently?
2. **Search across hidden text.** `search` runs over the buffer view
   (`Search.Buffers` snapshots `File.Text()`), so it returns hits inside
   rejected/hidden runs. Skip them, or return them labelled with their state
   (spec §6 says label, not silently return)? Which composition should a
   whole-workspace search index?
3. **Selection across a fold.** A selection whose anchor is before a fold and
   head after it spans hidden bytes. Does copy/cut include them or exclude them,
   and is a delete across a fold refused by the lease (it should be)? May a drag
   endpoint rest on a fold row?
4. **Mouse on a fold row.** A click today lands at the hidden run's session
   start. Should it instead do nothing, or reveal/decide the set (for example,
   jump to Review)? What do drag and autoscroll past a fold do?

## Recommendation

Accept the fold model and the map API above; land the small D1 leftovers
(exports, fuzz oracle, decision-generation accessor); then do D2. Because D1's
code is already in tree but unwired, nothing about folds or annotations is
visible to the user until D2 lands.
