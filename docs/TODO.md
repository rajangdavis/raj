# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md. Raw, dated agent feedback
lives in RAJ_FEEDBACK.md; its actionable items are in the section below.

**RECURSIVE_RAJ: start here.** A Raj agent improving raj reads
RECURSIVE_RAJ.md first (identity, rebuild boundary, editing discipline, swarm
workflow), then works the active plan in the next section.

## Direction (documented, not scheduled)

- [ ] **Reconciliation UX — layer toggles + conflict navigator.** Design note in
  `docs/RECONCILIATION-UX.md`: generalise the composition from
  accepted/proposed/rejected to layer selection, built on the F3b-ii projection
  and the "Overlap reporting for swarms" item. Open questions for the user.
- [ ] **Agent verb-surface audit — implement the safe simplifications.**
  Findings in `docs/AGENT-VERB-AUDIT.md`: localise paths in every `-json` shape,
  per-verb usage that shows positionals and the default target, one `-json`
  field contract, document `clear`/`open -create`, and line/col in apply/edit
  replies. Do it under the working agreement, before designing reconciliation
  verbs.

## Active plan — recursive raj (2026-09-10, second instance)

Open work:

- [~] **Wave 2 remainder — save-review lag.** Instrumented: `internal/timing`
  behind `RAJ_TIMING` logs the write path, the per-frame draw and pending walk,
  and save-to-clean. The file sink is done — `RAJ_TIMING=1` writes to
  `defaultPath` rather than stderr, which a full-screen editor cannot use as a
  log sink (it painted over the TUI; see INVESTIGATIONS.md). What remains is
  measurement and diagnosis: whether the beat is wall-clock in the write path,
  the `PendingMarks`/`Groups` journal walk during `Draw`, or one frame's paint.
- [ ] **edit -base and miss-point reporting** — under discussion with the
  user; not scheduled.

Deliberately excluded this session: claim/watch and region leases (open design
questions for the user — span granularity, advisory versus enforced, claim
lifetime); the flat-record response conversion (wants the user's definition of
the target); owner/group preservation (needs a root-capable machine); parallel
walk (blocked on streaming results); terminal, tmux, iTerm2 and chord items
(need the user's terminal); tree-sitter (its own milestone); the /tmp/opencode
image fix (a container-image change).

## Layered proposals — phases (2026-09-11)

Design and all decisions: `docs/LAYERED-PROPOSALS-SPEC.md`. Phase 0 (durable op log) is archived in COMPLETED.md; each remaining item is one
focused task.

~~Phase 1a — the projection primitive~~ is done and host-verified 2026-09-12
  (`go test`, `make check` green); see COMPLETED.md. `piecetable.Project(policy)`
  returns a `DerivedProject` built by a forward drift-map pass over the shared
  stores, with an independent byte-level fuzz oracle in `project_test.go`.
- [ ] **Phase 1b — decisions as state flips and leases.** `RejectGroup` marks
  `Rejected` (no reversal, cannot fail); `AcceptGroup` can un-reject; a pending,
  rejected or invalidated span is a read-only lease, so an edit or `apply` that
  intersects one is refused — except a writer amending its own `Proposed` set,
  which joins it instead (2026-09-13). `save` writes `Project(AcceptedOnly)`; `read`
  defaults to accepted with an annotated flag; the edit view is
  `AcceptedAndProposed` with inert spans hidden as atomic folds, Review is
  `Annotated`; `cmd+ctl+k` clears rejected, `cmd+ctl+l` clears invalidated.
  - **Done (F3b-i, 2026-09-12):** the state flips (`RejectGroup` marks
    `Rejected`; `AcceptGroup` un-rejects; `ClearRejected` reverses + drops),
    region leases (`Session.Leased`; an intersecting edit/`apply` is refused,
    except a same-author amendment of its own `Proposed` set),
    `save` = `Project(AcceptedOnly)`, `read` = the buffer view with `-annotated`
    states, and `cmd+ctl+k`.
  - [ ] **F3b-ii — the presentation half.** Edit mode renders
    `Project(AcceptedAndProposed)` with inert (rejected) spans hidden as atomic
    folds; Review renders `Project(Annotated)` with per-state tint/annotation;
    lease checks move off the keystroke path to the edit/commit boundary.
    `cmd+ctl+l` and the invalidated state belong to Phase 1c, not here.
  - [ ] **F3b-ii recon (2026-09-13).** The pane renders the raw session view
    (`p.File.Line(...)`, `internal/editor/pane.go`); `Project` is reached only by
    control `read` and the review state listing, so no display composition
    exists yet. Spec §13 rejected a second document, so there is one coordinate
    system and folds are display-only: this needs a bidirectional
    document↔display map (bytes↔rows/cells) covering the caret, scroll, mouse,
    diagnostics gutter and completion. Sequential because D2 uses D1's API:
    **D1** = `internal/view` + `internal/editor/pane.go` — the composition, fold
    rows, and the round-trip map, with unit/fuzz tests; **D2** =
    `internal/app` (`mode.go`, `render.go`, `review.go`, `keys`) — policy by
    mode, tint, and lease enforcement at commit instead of per key. Write a
    short design note fixing the fold representation and the map API first.
- [ ] **Phase 1c — invalidation and conflict reporting.** An orthogonal,
  recomputed `Invalid` marks a still-`Proposed` set whose edit no longer fits
  the current composition; it is excluded from edit/agreed and annotated in
  Review, with the colliding span reported, never cascaded or clamped. The D4
  promote rule is retired. Rendering and gutter polish for the new states.
- [ ] **Then:** change gutter vs `HEAD`, annotated `read`/`exec`/LSP
  composition, git verbs (read-only diff first).
- [ ] **Also open (spec §7, §10):** `session.json` into the store; compaction /
  checkpoints; the engine behind the record interface (SQLite later); the
  attachment model (loaded vs announced, headless read).

## Panes and fields

- [ ] Small and split panes, and how they resize.

## Editor

- [ ] **LSP: hover, go-to-definition, diagnostics, completion.** Staged, because
  each stage is independently useful and the risk is not evenly spread.

  Most of the seams already exist. `search.Docs` is the snapshot
  `textDocument/didChange` wants; `Session.Version()` is LSP's document version;
  the search pane's worker-parks-a-result-then-Notify pattern is the async shape
  a language server needs, already tested under the race detector; and
  go-to-definition returns a path and a position, which is exactly what
  `openFromPicker` already consumes. Completion is a third `Picker` mode
  alongside files and symbols.

  The hard part is not the protocol, it is **position mapping**: LSP counts
  characters in UTF-16 code units and raj counts bytes. That is a third
  coordinate system beside bytes and display columns, and one CJK character or
  emoji silently shifts every position in a response. Fuzz it against the byte
  offsets before anything depends on it.

  Order: ~~position mapping~~, ~~JSON-RPC framing~~, ~~process lifecycle~~,
  ~~document synchronisation~~, ~~hover~~, ~~definition~~ and ~~completion~~
  and ~~diagnostics~~ are done. The staged plan is complete; what remains are
  the follow-ons listed separately below.

  **Inlay type hints are deliberately not on that list.** They require drawing
  text that is not in the document, which perturbs column maths, caret
  positioning and wrapping — the part of the codebase with the most open
  uncertainty already, given the wrap-point tab re-anchoring and the hand-rolled
  width table. That is a renderer project, not an LSP one.
- [ ] **Symbols are found by leading keyword, not parsed.** Good enough to jump
  to a declaration you know is there, and structurally unable to see one written
  any other way: a function assigned to a variable, a decorated definition, a
  C declaration with no keyword at all. It will also name something inside a
  string literal or a block comment that starts a line with `func`. The next
  step up is per-language, and the cheap version of it is chroma — already a
  dependency, already tokenising these buffers off-thread for highlighting, and
  it knows a keyword token from a string token, which is exactly the distinction
  the scanner is missing.

  Half of this now exists: `syntax.Span` carries a `Class`, so "is this offset
  inside a string or a comment" is answerable from tokens the highlighter has
  already computed, and bracket matching uses it. The scanner cannot reuse it
  as it stands — it runs on the keystroke path at 0.42 ms and chroma costs
  ~80 ms, and the highlighter's tokens are per-pane, asynchronous, and absent
  for the first frames of a file. Either the scanner becomes asynchronous like
  the highlighter, or it reuses that highlighter's output and accepts having
  no answer until the first pass lands. Neither is a small change, which is why
  this is still open.
- [ ] **The reserved-chord tables are short.** They list what could be confirmed;
  the real sets are longer and vary with the user's own keyboard settings and
  terminal config, neither of which raj can see. A chord that never arrives is
  invisible from inside the editor and a chord that steals a terminal feature is
  invisible from inside raj entirely, so the tables are the only guard there is
  — worth extending whenever another one is found the hard way. Both were, this
  session.
- [ ] **A completion `textEdit` is ignored.** The server may send an edit with
  its own range instead of plain insert text, and honouring it means applying a
  server-computed edit rather than typing a word — a different operation from
  the one the popup performs. Ignoring it is right for the common case and
  wrong for the ones where the range extends past the prefix, which is how
  import-adding completions work.

  The blocker is layering, not protocol. `complete.Candidate` is deliberately
  free of any LSP type — the package ranks buffer words and the server plugs in
  through the same seam — so carrying a range means either a coordinate system
  `complete` can express on its own, or moving the apply step out of
  `acceptCompletion` entirely. Both are real designs; neither should be picked
  by whoever happens to be decoding the JSON. Ranges are also not guaranteed
  single-line, so the position fuzzing wanted extending to ranges first —
  that extension now exists and is archived in COMPLETED.md, host-side
  verification pending. Decision: deferred,
  subsumed by the announce/reconciliation design under `claim` and `watch` —
  the textEdit range is one more announced span flowing through the same
  reconciliation, so no LSP-specific coordinate decision until that lands.
- [ ] **Autoscroll runs at the idle tick, which is 150 ms.** That is coarse for
  a scroll: proportional speed makes it usable, since pushing further is how you
  ask for faster, but the motion is visibly stepped rather than smooth. A faster
  tick while a drag is held would fix it and means either a second timer or a
  variable tick rate — the current one exists for idle work and 150 ms is right
  for that.
- [ ] **A press on a list does not drag it.** Clicking selects, and holding and
  moving does nothing — neither rubber-band selection nor drag-to-reorder for
  tabs. Both are real gestures a list can carry and neither has an obvious
  meaning here yet, so nothing was guessed at.
- [ ] **The problems pane filters by severity and by open files, and nothing
  else.** "Only the current package" is the third obvious one and needs a notion
  of package the pane does not have — it is given paths, and grouping them by
  directory is right for Go and wrong for most other layouts. A fourth row of
  checkboxes is also where a filter row stops being a filter row, so the next
  one probably wants the search pane's query field rather than another box.
- [ ] **The hover panel does not scroll.** Content past its height is reported
  as `+N more` rather than being reachable. Scrolling means claiming arrow keys
  while a box sits over the document you are reading, which is exactly when
  navigation matters most — so it needs a modal mode with a visible indication
  that it is on, not a pair of extra bindings.
- [ ] **Hover markdown is not rendered.** Fences are stripped because they mean
  nothing in a terminal box; everything else — bold, links, lists — is left as
  written. A half-rendered subset is more confusing than none, so this is only
  worth doing properly or not at all.
- [ ] **Only servers that run with no configuration are listed.** gopls,
  rust-analyzer, pylsp, typescript-language-server, solargraph, clangd. A
  server that needs a config file to start is a setup problem raj should not
  pretend to solve silently, but there is no way to point raj at one either.
  A per-workspace config file is the answer, and it does not exist yet.
- [ ] **Auto-indent knows brackets and nothing else.** Adding a level after an
  unclosed opener and lining up a closer covers C-family languages and leaves
  out everything indented another way: Python's colon, Ruby's `do`/`end`, YAML,
  a `case` inside a `switch`. Each is a per-language rule, and the token class
  the lexer gives us says what a token IS but not what it MEANS — a keyword is
  a keyword whether or not it opens a block. This is where a real per-language
  table starts, and it should wait until something needs it rather than being
  guessed at from one language.
- [ ] **Tree-sitter is the decided direction for the syntactic layer.**
  Auto-indent, symbol navigation and string/comment classification move to
  tree-sitter — in-process, synchronous and deterministic, never a missing
  server — while semantic queries (hover, definition, completion, diagnostics)
  stay on LSP. The grammar-management question — one compiled grammar per
  language, and a C or WASM dependency in a terminal editor — is answered as
  its own milestone before the feature, not slipped in behind another. This
  replaces the per-language table the auto-indent bullet defers to, and
  outranks the chroma half-step on the symbols bullet.
- [ ] **Blinking secondary carets.** The real caret blinks because the terminal
  blinks it; a drawn one would need raj to redraw on a timer, which means a tick
  fast enough to be a blink and a dirty-region pass small enough that blinking
  costs one cell rather than a frame. Nice, and a long way down: the tick is
  150 ms today and exists for idle work.

## Workspace

- [ ] **Save-as has no directory listing.** Tab completes and a missing parent
  is offered rather than failing, but there is still nothing showing what is
  already in the directory being typed into — so completion tells you a name
  exists only once you have typed enough of it. The picker one chord away holds
  a fuzzy index of the whole tree, and the natural shape is the completion
  popup: anchor it under the field and list the matches rather than only
  filling in the common prefix.

- [ ] **Unnamed buffers have nowhere to persist to.** Session restore is keyed
  on paths, so a scratch buffer from cmd+n is the one tab a restored session
  cannot bring back. It needs the dirty-buffer journal below, not a path.

~~Session persistence~~ — tabs, cursors, scroll, expanded directories and the
  focused pane are saved to `.raj/session.json` and restored on start;
  `--no-restore` disables both directions. What is left of it:
~~The session is written only on a clean exit~~ — it is now also written from
  the idle tick, debounced to three seconds, and touched whenever a tab opens or
  closes. A crash loses seconds rather than the session.
- [ ] **Subagent transcripts feed the efficiency loop.** User asked 2026-09-11.
  The orchestrator's §8 loop reviews tool-usage reports by hand; the durable
  version mines captured subagent transcripts (the task tool's outputs in
  opencode) for wasted tool calls, retryable friction, and brief-quality
  patterns — the same loop, fed from data rather than from what the previous
  session remembered. Lives in the orchestrator's review process, not the
  editor's session store; DuckDB becomes right-shaped here only if the
  mining grows analytics-shaped, again not for persistence.
- [ ] **Change gutter** versus git HEAD, distinct from the author tint. This is
  also where deletions get represented, since a deleted span leaves no piece.

- [ ] **The buffer overlay copies rather than reading pieces.** `Search.Buffers`
  snapshots each dirty document with `File.Text()`, which materialises the whole
  buffer on the event thread every time a search is scheduled — a keystroke,
  after the debounce. It is correct and it is bounded by the open tabs rather
  than by the tree, but it is a copy per search of everything being edited.
  Scanning `Spans` in place would avoid it, and needs either a matcher that can
  cross a piece boundary or a guarantee that it never has to.
- [ ] **Parallel walk.** Now the only lever left, and no longer speculative:
  measured on the ghostty checkout, a search is 51% syscalls — 16.7 ms of walk
  and stat, 15.2 ms of open and close, against 1.0 ms of matching. The scanning
  side is done; overlapping the syscalls is what remains.

  It breaks `MaxMatches` as written. A worker pool returned 509-517 results
  against a cap of 500, varying run to run, because workers in flight when the
  cap trips still append — and *which* 500 you get becomes scheduling
  dependent, so the same query returns different results on consecutive runs.
  Needs streaming results or deterministic truncation first. Measure on a
  multicore box: the figures above come from one core, where a pool shows
  nothing.

## Saving and files

The write path is atomic and refuses to clobber another writer; what is left is
the follow-ups that were deliberately kept out of that change.

- [ ] **Owner and group are not preserved.** The temp-and-rename write copies
  the mode but not the uid or gid, so saving a file owned by someone else, as
  root, silently reassigns it. Needs a `Chown` from the stat, and a decision
  about the ordinary case where the chown will fail for want of privilege.
  Decision: ignore `EPERM` so an unprivileged save still succeeds. The fix only
  matters for root saves and was untestable at uid 501, so implementation is
  deferred to a root-capable machine: `tmp.Chown(uid, gid)` from the stat,
  `EPERM` ignored, plus a root-gated test in `write_test.go`.
- [ ] **Save reports a noticeable lag.** User-reported this session: cmd+s
  felt slow on ~50 KB docs. Could be the fsync+rename, could be post-save
  re-parse/re-highlight on the event thread (highlighter re-runs on the
  version bump). Needs a measure before a fix — cheap to add a timing log on
  save and see whether it is the write path or the re-tokenize that shows.
  Refined 2026-09-10: the lag is specifically on cmd+s WITH a review popup —
  a visible beat between the tint clearing and the tab's dirty indicator
  going away. Wave 2 diagnoses.
- [ ] **Encoding is LF, CRLF and a BOM, and nothing else.** UTF-16 and the
  legacy single-byte encodings are read as bytes and will be mangled on save.
  The binary sniffer catches UTF-16 with NULs in it, which is most of it, but
  that is a side effect rather than a decision.

## Buffer

- [ ] **Compaction.** Merge adjacent same-author pieces; only flatten spans that
  are both saved and committed.
- [ ] **16 ms coalescing window** for streaming agent hunks.

## Control socket (replaces the in-editor agent)

- [ ] **Three nested header strings are still JSON.** The response header itself
  is now opcode-encoded (`encodeHeader`/`decodeHeader` in
  `internal/control/header.go`); what remains JSON are the nested `DiffJSON`,
  `LSPJSON` and `StatesJSON` strings — `LSPJSON` a deliberate retreat, since its
  payload is text by construction.
- [ ] **Then the request header can go too.** Every verb a program can reach
  already ignores it; what keeps it alive is exec, recv, hello and cancel, which
  are the four the batch loop deliberately does not model. They need opcodes of
  their own first, or a decision that they stay on a JSON frame forever and the
  two encodings coexist.
- [ ] **exec is unreachable from a program.** Its argv is a list and the opcode
  table has no repeated-argument shape yet — a `arg` op that accumulates would
  do it. It is also the one verb with a remote-execution gate, so widening its
  surface deserves its own change rather than arriving inside a batching one.
  recv, hello and cancel stay out on purpose; see the note in control/prog.go.

The agent pane is not being built. An editor that hosts a model is an editor
that owns a model's lifecycle, its configuration, its failure modes and its
version skew, and none of that is editing. The seam is a socket instead: raj
exposes the buffer over a Unix domain socket and whatever wants to drive it —
an agent harness, a script, a test — is a separate process that can be
restarted, replaced or written in another language without touching the editor,
and increasingly on another machine or in a container.

`Session.ApplyDiff`, the op log and the per-author stores were built for a
writer that is not the user, so the hard part is already there. What is missing
is the transport and the rules around it.

~~The socket and the protocol~~, ~~writes landing on the event thread~~, ~~a
  mandatory base version~~, ~~off by default~~, ~~the driver round: `goto` and
  `close` verbs, the `buffers` active flag, canonicalized `open`~~ and ~~the
  short socket-path / Discover fixture~~ are done: `internal/control` is the
  transport, `internal/app/control.go` is what a request means, the two cannot
  be collapsed because the transport package has no editor types to reach for,
  and the Stream D additions ride the same eight layers. What remains:

- [ ] **A `find` step in the opcode pipeline.** `run -prog` cannot ask "where
  is this text" and get an offset back in the same program, so a batched driver
  round (find → read → apply) is several round trips today. A `find` op
  answering with a byte span is the single-round-trip version of the
  `search` and `read` bullets above.

- [ ] **Attribution has no inverse.** An agent can see which spans are its own,
  but there is no verb to drop them. Reverting its own work means computing a
  reverse diff and applying it, which leaves both edits in the journal. A
  `revert -author` that discards one writer's pieces is the natural companion
  to tracking them, and the store's structure is what makes it cheap.
- [ ] **Every agent shares one tint.** Distinguishable in the data, identical on
  screen, so a user watching two connected agents cannot tell which wrote what.
~~Author ids handed out per connection~~ — ids are now per identity, so a
  harness reconnecting keeps the text it wrote and forty restarts cost one id.
  Distinct identities still consume them and running out is an error rather
  than a wrap.

What a driver round actually hits when the editor is the checker, learned the
hard way: each failure below had a cheap structural remedy on the `raj ctl`
surface, and all three would have caught the byte-drift corruption before a
compiler hand-off.

## Layered proposals

The direction this is heading, not built. An agent change set becomes a
*proposal* rather than an edit: in the document, tinted, but not composed into
what would reach disk until accepted. Groups already exist (`Begin`/`End`), and
undo is already `reverseGroup(group, author)` — so rejection is built. What is
missing is addressing and state.

~~Proposal state on groups~~ — `Session.Groups`, `MarkGroup`, `AcceptGroup`,
  `RejectGroup`, and `groups`/`accept`/`reject` over the socket. An agent apply
  is marked proposed; the user's typing is not. Rejection is undo addressed by
  group, so an older change can go while newer ones stay. What is left:
~~Proposed text could still reach disk~~ — `host.Save` refuses while any change
  set in the buffer is proposed, and the user's own save accepts everything
  pending in that file. So an agent cannot commit its own work, and the human
  gesture that writes the file is the approval. Two things that leaves open:
- [ ] **The user's save is all-or-nothing, but no longer silent.** The
  save-time review popup is done and host-verified: cmd+s with pending sets
  opens `Prompt.Review` — rows name the sets and jump the caret, enter
  accepts-all+saves, esc cancels. What remains open is per-hunk review
  granularity, which the review-flow chords (Wave 2) address.
- [ ] **A dedicated next-hunk jump for cycling proposed changes without
  reopening the list.** Accept/reject/review-picker chords exist
  (ctrl+alt+a/x/v); the gap is cycle-without-relist. Chord scheme decided
  2026-09-10: ctrl+super+, and ctrl+super+. prev/next, ctrl+super+m accept,
  ctrl+super+/ reject. Landing in Wave 2.
- [ ] **A rejected group can be wedged.** If a later edit overlaps it, the
  members cannot be rebased out and the whole thing rolls back — correctly, but
  the caller is told only that it failed. It should be told what overlapped, so
  it can re-propose against the current text instead of guessing.
- [ ] **An edit inside a pending set needs a decision (pick A, B or C).**
  Review mode's read-only rule kills the accidental case; a deliberate edit in
  edit mode still has no answer — adjust and re-project the surviving runs
  (recommended), auto-reject the set, or prompt on first overlap. The removed
  `raj ctl review` D3 bullet pointed here. Design: `docs/INVESTIGATIONS.md`.
- [ ] **Groups have ranges now; rendering does not consume them yet.**
  `piecetable.DiffHunk` carries rebased `Start`/`End`, `Session.DiffPending`
  projects them, `editor.PendingMarks` puts them in current coordinates, and
  the socket `groups`/`diff` listing carries them. What remains is the
  rendering consumers: the change gutter and diff-style rendering.
- [ ] **`raj ctl diff -vs HEAD` (git, stretch).** The accepted composition
  versus git HEAD, distinct from the pending-set view. Depends on the
  range-rebase walk, deferred deletions (a deleted span leaves no piece) and
  the base: raj runs read-only `git show HEAD:<path>` on the host — no
  checkout, no apply, a far smaller surface than `exec` — then diffs
  internally. The programmatic twin of the change gutter versus git HEAD and
  of diff-style rendering; one rebase walk feeds all three. The git half is
  untestable from the container (no shared filesystem).
- [ ] **Deferred deletions.** A proposed deletion is not performed: the pieces
  stay, the range is marked, and acceptance is when the delete runs. This is
  what makes a deletion visible at all — a deleted span leaves no piece — and it
  replaces the change-gutter representation problem rather than solving it.
- [ ] **Diff-style rendering.** Green for pending additions, red for pending
  deletions, as in a git diff. Colour carries state; the gutter carries who,
  from the participant table — with five agents everything is green and the
  colour cannot also mean identity.
- [ ] **Overlap reporting for swarms.** Two proposals whose rebased ranges
  intersect is a mechanical fact. Report it to both with the other's author id
  and the span. The editor must not arbitrate which is right: that is semantic,
  and voting built in here would be wrong in ways nobody can debug.
- [~] **`claim` built; `watch` remains.** `claim <path>...` records enforced
  file-level claims (no spans, no TTL, no overlap refusal); reads stay free, a
  pathless write is allowed only when exactly one file is claimed, `open
  -create` auto-extends, state is in-memory per identity (journal later).
  `apply`/`edit` on an unclaimed path refuse with `claim a file first`;
  `delete`/`rename` are claim-gated, `mkdir` is ungated. Built 2026-09-13/14
  (see COMPLETED.md); journal persistence of claims remains.
  `watch` (the push) stays separate. Design: `docs/CLAIM-SPEC.md`.
- [~] **Which composition does `read` return?** Decided: `AcceptedOnly` by
  default, with an explicit annotated flag (spec §6, §12). Implementation lands
  with phase 1b; the argument stands — an agent proposes against the agreed
  base, not another agent's unaccepted guesses.
~~`exec`~~ — refuse-on-dirty, naming the files, with the counter. `raj ctl stats`
  reports runs, blocks, and how many blocks were agent-authored only. What is
  left:
- [ ] **Nothing reads the counter yet.** `raj ctl stats` records runs against
  stale files and how many were agent-authored only. When layered proposals
  land, `exec` should materialise the accepted composition and run against
  that, and the count says how much that is worth.
- [ ] **A cancelled `exec` can orphan children.** CommandContext kills the
  process it started; `sh -c "go test"` is two, and the test binary survives.
  The run returns promptly, so this is invisible to the caller, but the work
  keeps going. A process group and a group kill is the fix, and it is
  platform-specific in a way nothing else here is.
- [ ] **No flush mechanics.** When v2 lands it needs temp-file-plus-rename per
  dirty file, so no subprocess reads a half-written file.
- [ ] **Discovery is a directory listing.** A driver finds editors by listing
  `$XDG_RUNTIME_DIR/raj/` and asking each socket for its root. That is one round
  trip per editor and cannot go stale, but it also means a driver started
  independently has to know that convention. A `--control-socket` at a path the
  driver chooses is the escape hatch and is probably the common case.

  **It finds nothing over TCP**, and cannot: there is no directory to list on
  the other side of a boundary. `RAJ_CONTROL_ADDR` is the only way to name a
  remote editor, which is fine for one and unhelpful for several.
~~The socket assumes one filesystem~~ — `--control-addr tcp://host:port` is the
  second transport, with a token in every request header standing in for the
  file mode, and `raj ctl` translating paths between the caller's view of the
  tree and the editor's. This exists because the useful arrangement is raj on
  the host, where the terminal delivers its chords, and the driver in a
  container, where it is safe to let it run commands. What is left of it:
- [ ] **The token is printed and never stored.** It goes to stderr at startup
  and nowhere else, so a user who has scrolled past it has to restart raj or
  pin `RAJ_CONTROL_TOKEN` themselves. A file would be the obvious fix and is
  the wrong one — a file the driver could read is a file on a filesystem the
  driver does not share, which is the situation TCP exists for. `raj ctl token`
  reading it out of the running process is the shape that might work, and needs
  the socket to still be listening alongside the port, which it is not.
- [ ] **One listener, not both.** `--control-addr` replaces the socket rather
  than adding to it, so a session driven by a container cannot also be driven
  by a script on the host. Two listeners sharing one queue is a small change;
  what stops it is that `Path()` then has to return two things, and every
  caller of it assumes one.
- [ ] **Nothing is encrypted.** The token authenticates but the frames are
  plaintext, so the buffer contents are readable to anything on the path. That
  is the right trade for a loopback port and a container bridge and the wrong
  one for anything further, and there is no way to tell raj which it has beyond
  the warning it prints when the bind address is not loopback.
- [ ] **Path inference is a single question.** `raj ctl` maps roots when the
  editor's root does not exist on the caller's filesystem, which is right for a
  bind mount of the whole repository and wrong for a mount of a subdirectory,
  or two repositories mounted under one parent. `RAJ_ROOT_MAP` is the escape
  hatch; a real answer would ask the editor to resolve paths relative to its
  root and stop sending absolute ones at all.
- [ ] **A workspace-visibility flag (scope what agents can reach).** The editor
  can be rooted at a parent directory — `raj ~/projects` — but today the
  control socket is bounded by that one root (`Guard.inRoot`), so every project
  under it is readable and writable by any connected agent. The ask: a flag
  such as `--workspace <dir>...` (repeatable) or `--allow <glob>` that narrows
  the *agent-visible* set to the named subdirectories, so raj can run over a
  projects folder while exposing only the chosen repositories. Open: one extra
  root or many; whether it bounds only the socket (UI keeps the full tree) or
  the editor too; how resolution/search/lsp and the claim gate enforce the set;
  interaction with `--control-addr` and `RAJ_ROOT_MAP`; and whether each allowed
  dir is its own virtual root or a subtree of the real one.
- **Identity design — superseded 2026-09-13 by explicit `register`/`-as`.** The
  server-minted/absorbed design is no longer the direction: `raj ctl register`
  mints a short random key and the caller passes `-as <key>` on every call, the
  plugin only gates raj-spawned subagents, and a byID check in `register`
  handles collisions. Kept for the decision history.
- [ ] **Dump snapshots are keyed by author id, not by identity.** A rebound
  identity keeps its text but not its snapshots, so `dump`→`patch` across two
  CLI invocations fails until the driver re-dumps. Keying snapshots by the
  bound identity string keeps them across reconnects.
- [ ] **Editor-side identity durability (the swarm blocker).** Absorption is
  the client half; this is the server half. Verified in source: `Registry`
  (internal/control/participant.go) never recycles — `r.next` only climbs,
  cap 255 — and every connection takes a provisional dead `anon-N` before
  hello (`Server.serve`, `nextAuthor`), so `who` floods with one dead anon
  per CLI call (144 ids for ~2 real participants observed). Identities are
  durable per-identity only within one process; an editor restart re-mints.
  For a usable swarm: (a) recycle `gone` agent ids or persist the registry
  across restarts so an identity's author id outlives the process (currently
  NOT recycled); (b) `who -live` / drop dead anons so a swarm is readable; (c)
  key dump snapshots by identity string, not author id, so `dump`→`patch`
  survives a reconnect; (d) stable swarm names (`who -as X -name Y` already
  binds) so `who` shows `raj-explore`/`raj-impl-1` instead of bare tokens.
  Spec first (design doc + verb spec) before any editor change.
- [ ] **Region leases.** `apply` rejects a stale hunk after the fact; a lease
  would stop the user and a driver being told they both own a span in the first
  place. The conflict report carries the version that invalidated the range, so
  the information a lease needs is already on the wire.
~~No way for the user to speak to a driver~~ — `recv` parks until there is
  something to say, `Server.Send` and `App.Tell` post into a per-participant
  mailbox, and messages keep across a reconnect because the mailbox is keyed on
  the durable author id. No push and no subscription state: the request/response
  shape holds, the request just does not answer yet. What is left of it:
- [ ] **Nothing in the editor calls `App.Tell` — narrowed: saves do.** The save
  path now posts `saved <path>` through `Tell` (`App.notifySaved`, also from
  `host.Save`); what remains is the user-facing half. The transport, the CLI and
  the tests are there; the chord and the prompt are not. A binding is a claim on
  a chord the terminal then stops delivering to anything else, so it belongs in
  the same pass as the accept/reject bindings rather than being spent
  separately — and the two want the same picker when more than one driver is
  connected.
- [ ] **A full mailbox is reported to nobody.** `Post` refuses the seventeenth
  unread message and returns an error, which is right, but with no caller in
  the editor there is nothing to put it in the status line. Same blocker as
  above.
- [ ] **Still no notifications for buffer changes.** A driver wanting to know
  the user has typed must poll `buffers`. The mailbox deliberately does not
  generalise to this: messages are discrete, rare, and must not be coalesced,
  where buffer changes are none of those. A `subscribe` op wants a version
  cursor and a recovery story for a slow reader, and `OpsSince` is the right
  basis — push only "the version is now V" and let the client fetch.
- [ ] **`apply` cannot create or reach an unopened file.** `open` puts a path in
  a tab first, which also puts it in front of the user — deliberately, since an
  editor silently editing files you cannot see is worse than one extra call.
  Unnamed buffers stay unaddressable: there is no name to ask for.
- [~] **SQLite session store** — the op log is now persisted (`internal/journal`,
  phase 0) under `.raj/` (not `.git/raj/`); whether the store is forkable is
  still open. The engine is deferred (a plain append-only log first, SQLite
  later behind the record interface), as is folding `session.json` into the same
  store. See the spec §7.

## Known rough edges

- [ ] **The smoke suite is Linux and macOS only, and only Linux is proven.**
  `internal/smoke` opens a pty through the ioctls rather than taking a
  dependency for it, and the two platforms do it differently enough to need
  separate files. The darwin path is written from the documented ioctls but has
  not been run — if `make smoke` cannot get a pty on your machine, that is the
  first place to look.
- [ ] **Smoke scenarios wait on wall-clock time.** 400 ms after each chord,
  which is generous on a fast machine and may not be on a loaded one. The
  editor already has a seam that would replace the sleeps — the control socket
  could answer "have you drained the queue?" — and until it does, the suite is
  slower and more fragile than it needs to be.
- [ ] **Display width table is hand-rolled**; suspect it first if the caret
  drifts. Narrowed: TODO.md holds three runes README.md does not — en-dash,
  em-dash, and `↔` U+2194, all East Asian Ambiguous, and raj calls all three
  narrow. `↔` is the likely culprit: emoji-capable without emoji presentation,
  which is the class fonts disagree about. Diagnostic: arrow along a line
  containing `↔` versus one with only an em-dash and see which drifts.
- [ ] **Tabs re-anchor at each wrap point** — a continuation row measures tab
  stops from its own start. Self-consistent between the wrap engine and the
  renderer, so the caret stays correct, but it looks slightly off when a line
  with mid-text tabs wraps.
- [ ] **One action is bound but unimplemented**, so its chord is taken from the
  terminal for nothing: `CommandPalette` (cmd+shift+p). `GotoLine` and
  `GotoSymbol` were among the others and are done, and `CursorUndo` (cmd+u)
  joined them this session. `ToggleAgent` was a third; it was removed rather
  than implemented, so cmd+alt+b goes back to the terminal.

  It is no longer invisible: `keys.Unimplemented` lists it, KEYBINDINGS.md
  marks it, and a test in internal/app presses it and fails if anything
  handles it — so implementing it without unlisting it would break the build,
  and so would binding another without noticing.
- [ ] **Every OSC raj sends is swallowed under tmux.** tmux terminates the
  escape stream, so OSC 1337 SetProfile never reaches iTerm2 and OSC 52 never
  reaches the clipboard — which means a tmux session gets none of the iTerm2 key
  mappings, because the profile it needs is never switched to. The escape hatch
  today is `RAJ_ITERM_PROFILE=` plus installing the mappings into the everyday
  profile. The fix is DCS passthrough: with `$TMUX` set, wrap the payload as
  `ESC P tmux; <ESCs doubled> ESC \`, which needs `allow-passthrough on` on the
  tmux side. It belongs in one place — everything raj emits goes through `term`
  and `ui.native` — and it should be a wrapper on the writer rather than a
  condition at each call site, or the next OSC added will miss it.
- [ ] **Profile switching in iTerm2 is not clean.** raj switches profile on
  entry with OSC 1337 and restores on exit, and the switch is visible. Installing
  the profile is no longer manual — `raj --config iterm2 --install` writes it and
  startup warns when it is stale — so what is left is the visible switch itself.
  Deferred: what is there works.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Agent feedback — actionable (context in RAJ_FEEDBACK.md)

Open items extracted from the agent feedback notes now kept in
`docs/RAJ_FEEDBACK.md`; the raw notes, dates and explanations are there. Items
already tracked elsewhere in this file are not repeated. `- [~]` marks work
that is in tree but still needs host verification.

Path and identity:

- [x] **One path-resolution seam for every verb.** The outbound half is
  `Client.toEditor` (`internal/control/client.go`), applied to every
  path-bearing field (`Path`, `NewPath`, `Dir`, `Paths`, `Query.Path`) so a verb
  cannot forget one; the inbound half is `localise`/`Mapper.FromEditor`. Live
  check 2026-09-15: relative, caller-mapped-absolute and editor spellings all
  resolve to the same buffer for read/version/groups/diff/dump/review/goto, and
  the original symptom no longer reproduces. Remaining: `run -prog` payload
  paths and `LSPJSON` location paths are not mapped.

Search:

- [x] **The whole-buffer search `-json` uses Go field names** — done
  2026-09-13: `SearchMatch`/`TruncatedFile`/`ExecStats`/`DirtyBuffer` carry
  snake_case json tags, so `-json` and `-jsonl` agree.
- [~] **Search hits carry a line range now; `read -lines` still has no byte
  span — partly fixed 2026-09-13.** `search -json`/`-jsonl` report a hit's match
  (`byte_start`..`byte_end`) and its line (`line_start`..`line_end`, the latter
  added 2026-09-13), so a whole line/block is addressable without deriving a
  length from the trimmed `text`. Remaining: `read -lines A,B` returns text with
  no byte offsets, so a byte-span read (or offsets on the lines) is still
  wanted.
- [ ] **New `raj ctl` against an old server warns falsely on `-include`.**
  `Considered` rides header field 0x42; an old server never sets it, so every
  `-include` search looks like zero files considered. `warnVersionSkew` is the
  shipped mitigation: it warns on the build mismatch, but does not suppress the
  specific zero-`Considered` message, so a driver still has to recognise the
  pairing.

Diagnostics:

- [ ] **No raw-LSP verb.** No way to inspect inbound `publishDiagnostics`
  frames or their `version` field, so the freshness assumption is
  unverifiable from the socket. A debug verb or a trace would close it.

Dated notes, 2026-09-12 (layered-proposals 1a wave):

- [ ] **A saved buffer can still carry proposed sets.** After a rebuild and
  restart, `buffers` reported `saved` for `internal/piecetable/project_test.go`
  while `groups`/`diff` showed two `proposed` sets whose ops read `moved past
  what a rebase can carry`; the sets then vanished without a visible accept.
  Needs a reproduction before it is a fix — it may be the journal restore
  seeding decisions, or `saved` meaning only that the text matches disk while
  the decisions ride a separate axis. Relevant to 1b, which changes this exact
  reject/decision path.

Dated notes, 2026-09-13 (workflow sim — pseudo-agent scenarios in an empty workspace):

Two raj agents (add-and-wire, delete-dead-code) ran against a seeded fixture to
find the lifecycle gaps instead of guessing at them. Findings:

- [x] **`delete`/`rm` — built 2026-09-13/14 as the W4b review primitive.** A
  deletion has no text-diff representation, so `delete` records a pending
  deletion and never unlinks on its own; the human prompt is the approval
  surface. See COMPLETED.md and `docs/FILE-LIFECYCLE-SPEC.md`.
- [x] **`mkdir` — added 2026-09-13.** `raj ctl mkdir <dir>` creates a directory
  with parents, under the workspace root only (dirs are not claim-gated). Not
  yet reachable from `run -prog` (no prog op entry). `rename`/`mv` landed
  2026-09-14; `rmdir` remains (W4c-2).
- [ ] **A buffer that is entirely another author's pending proposal is
  uneditable** — any edit, even outside the owned span, is refused. Acute here
  because the fixture was seeded as proposals; it is the overlap/reconciliation
  problem.
- Observed: cmd+s with a pending *proposed* set warns then saves (dirty
  clears) — fine, but it does not match the "opens a review popup" description.

Inlay hints, remaining:

- [ ] **Mouse-hover tooltips.** VS Code-style, using the hint `Tooltip`;
  depends on the column map, not on wrap integration.
- [ ] **`cmd+.` apply-hint-edit.** Applies a hint `textEdits`.

Superseded or already tracked, not re-filed here: the positional-span and
path-verb refusals and `FakeHost.Press` panic (Wave 3); the `-include`/
`-exclude` basename fix and the search `truncates`/`-regex` follow-ons (Wave
C); `lsp references`; `edit -base` and miss-point reporting; `/tmp/opencode`;
the `find`/file-listing verb; and the `search` `ByteStart`-vs-`LineStart`,
`groups`-vs-`diff` and `read -lines` reports (fixed; see COMPLETED.md). The rest
of the feedback is context in `docs/RAJ_FEEDBACK.md`.

- [ ] **File lifecycle — remaining: directory rename, `run -prog` reachability.**
  Built and live 2026-09-13/14: `mkdir`, `delete`/`delete -withdraw`/`deletions`
  (a review primitive with a prompt gate and `RAJ_TRASH`), `rename`/`mv`
  (files), and `rmdir`/`rmdirs` with the review-tab UI (verified 2026-09-14) —
  see COMPLETED.md. Still deferred: directory rename, a permanent Ignore answer,
  and `run -prog` op reachability for the new verbs. Specs:
  `docs/FILE-LIFECYCLE-SPEC.md` and `docs/CLAIM-SPEC.md`.
- [x] **`proposals` — one listing over the pending surface.** Built and
  verified live 2026-09-15: `raj ctl proposals [-mine]` is one flat tagged list
  over the pending change sets (`set`), `deletions` (`delete`) and `rmdirs`
  (`rmdir`), human output grouped per kind, `-json` a flat
  `{kind,path,author,group,start,end}` list (a set's span is the min start/max
  end of its rebased hunks, or -1/-1 when every member has moved). Threaded
  through the eight layers: `control.Proposal`, `Response.Proposals`,
  `Header.Proposals` (field `0x4f`), verb code 40, `BufferHost.Proposals`,
  `Guard.Proposals`, `App.Proposals`, the CLI, and `client.go` path rebasing.
  No `run -prog` opcode, matching `deletions`/`rmdirs`. Socket-verified:
  empty/`set`/`delete` round-trips and `-mine`, no regression in the old verbs.
  See COMPLETED.md. Design: `docs/PROPOSALS-SPEC.md`.

Dated notes, 2026-09-14 (rmdir Wave 2 — subagent friction):

- [ ] **`/tmp/opencode` is root-owned 0755 in the container**, so the skill's
  scratch dir is unwritable to the uid-501 driver (recurred this wave; already
  tracked under the container-image work).

Dated notes, 2026-09-14 (rmdir Wave 3 — subagent friction):

- [x] **rmdir UI (Wave 3) — verified 2026-09-14.** `internal/app/rmdir.go`
  (review surface, subtree safety, trash-aware whole-dir removal) +
  `rmdir_test.go` + the `ProposeDirRemoval` prompt trigger; gofmt/vet/test
  green on the host. `TestControlRmdirProposesWithoutRemoving` (Wave 2) still
  passes: `rmdir` pops the prompt, but the test never answers it.
- [x] **Dir gate and file gate disagree on the unsafe answer.** Fixed 2026-09-15:
  `promptDirRemoval` (`internal/app/rmdir.go`) computes `dirRemovalSafe` at
  raise time, offers `Remove forever` only when the subtree is safe, and puts
  the reason in the prompt message otherwise — mirroring `promptDeletion`. The
  two refusal tests press only `enter` (Ignore) and assert the button is absent
  and the reason is in the prompt. The message was then shortened to fit the
  prompt's 68-column line by dropping "directory" (the message-line truncation
  itself is filed in the dated notes below).

Dated notes, 2026-09-15 (proposals Wave 4 + dir-gate subagents):

- [ ] **A `claim` without `-add` silently replaces the set.** A subagent's
  mid-task `claim <newfile>` dropped its earlier claims and a later apply was
  refused (`not in your claim set`). Documented behaviour, but a task-long claim
  set is the common shape, so a warning when `claim` replaces a non-empty set
  would turn the footgun into a note.

Dated notes, 2026-09-15 (Wave 1 swarm — four subagents):

- [ ] **A writer cannot edit a region another writer has proposed.** An `edit`
  aimed at text another agent had just proposed was refused with `change set N
  owns this text` (the region lease). The bytes were a different span, so even a
  non-overlapping edit in the same file was blocked by where the hunk landed.
  Report the owning author/span so the writer can route around it, or scope the
  lease to the exact proposed bytes.



## Wave B proposals, 2026-09-11 (review verb, save-lag timing, inlay toggle)

In tree as proposals; host verification pending.

- [x] **D3 — `raj ctl review [path]`** — the verb is threaded through the eight
  layers (`OpReview`/`OpReviewList`, request field `hReviewList`, `Host.Review`,
  the `memHost` fake), and `App.EnterReview` is now the one enter path shared
  by the cmd+r chord and the socket. `-json` lists without entering the mode.
  Verified live 2026-09-15: `raj ctl review` is in `raj ctl -h` and answers
  over the socket.
- [~] **Save-review lag** — `internal/timing` behind `RAJ_TIMING` logs the
  write-path phases, the per-frame draw and pending walk, and save-to-clean.
  Measurement, not a fix. The first cut defaulted to `os.Stderr`, so with the
  gate on it painted over the TUI and made the editor unusable; the sink is now
  a file (`1` names `defaultPath`) and stderr is barred. See INVESTIGATIONS.md.
- [ ] **The hover-anchor cross-cutting test is blocked on a server seam.**
  `internal/app` has no fake LSP server and the hover path early-returns
  without one; the column math is covered at the editor layer only.

- [x] **A normal save into a missing directory offers to create it.** Fixed in
  tree: `saveNamed` (`internal/app/app.go`) routes through `ensureParent`, so a
  save whose parent is gone gets the create-directory prompt rather than
  `os.CreateTemp`'s `ENOENT` (`TestSaveNamedOffersToCreateAMissingDirectory`,
  `TestSaveNamedDecliningReportsNotSaved`). Verified during the 2026-09-15 wave.

Dated notes, 2026-09-15/16 (Wave 2 host verification):

- [ ] **`.raj/` is not in the hidden defaults.** The workspace scratch dir
  (journal logs, `trash/`, `session`) shows up in the tree, search and picker —
  e.g. `search 'package pkgX'` still hits `.raj/trash/pkgX.<stamp>/`. Add
  `.raj/` to `internal/hidden` defaults, or decide the editor's own state should
  be visible.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
- The container build/run stays the external `bldraj`/`oc` shell functions, not
  folded into the binary as `raj box build`/`raj box run`.
- `exec` over TCP is refused by design — the command would run on the editor's
  machine, outside the driver's container. The driver-side staleness
  substitution (a `buffers`/`read` version check before a hand-off) landed
  instead.
