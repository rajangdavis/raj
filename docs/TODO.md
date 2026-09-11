# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md.

**RECURSIVE_RAJ: start here.** A Raj agent improving raj reads
RECURSIVE_RAJ.md first (identity, rebuild boundary, editing discipline, swarm
workflow), then works the active plan in the next section.

## Active plan — recursive raj (2026-09-10, second instance)

Landed and host-verified (`make check` green):

- [x] **Wave 1 — incremental LSP sync.** `App.syncDoc` pulls `OpsSince` and
  renders the window through `editsSince`; a live gopls hover check rides the
  rebuild.
- [x] **Wave 2 — review-flow chords and cycling.** Accept/reject replaced
  ctrl+alt+a/x with ctrl+super+m and ctrl+super+/; next/prev cycle on
  ctrl+super+, and ctrl+super+. walks distinct sets in document order, wraps,
  and reports "proposal N of M".
- [x] **Wave 3 — honesty fixes.** FakeHost.Press panics on an unknown chord;
  search refuses a bare positional path and warns when -include matches
  nothing.
- [x] **Wave C — search honesty follow-ons.** Per-file truncation is surfaced
  (response opcode 0x43 plus a CLI "truncated" report) and -regex anchors
  match per line. The CLI half needs the container image rebuilt.

- [x] **D1/D2 — review mode (2026-09-11).** cmd+r toggles an app-level Review
  mode; the document is read-only in it (mutations refused with a status note),
  while decisions, movement, scroll and search stay live; a status-line badge
  plus a keybar show the real chords and `n`/`N`; Reload moves to cmd+shift+r.
  Files: internal/app/{mode,render,app}.go, internal/keys/{action,table}.go,
  docs/KEYBINDINGS.md, tests.
- [x] **D4 — moved-hunk option A (2026-09-11).** `DiffPending`
  (internal/piecetable/groups.go) projects the surviving runs of a proposed
  member instead of dropping it; `Pending()` auto-rejects a set with no
  surviving run; `PendingMarks` follows the fragments. The review bar is
  reconciled to per-member "not shown" wording.

Still open:

- [ ] **Wave 2 remainder — save-review lag.** Diagnosed: the dirty bit and
  the proposal tint clear inside one synchronous `Handle`, so the beat is
  wall-clock — the fsync/rename/read-back write path, the
  `PendingMarks`/`Groups` journal walk during `Draw`, or one frame's paint.
  Measure with an env-gated log in the `Run` loop before any fix.
- [ ] **D3 — `raj ctl review [path]` socket verb.** Enter Review mode and list
  pending sets over the control channel; a new verb (wire, eight layers), so it
  rides a rebuild. The mode itself (D1/D2) landed; the pending-span edit
  decision (adjust / auto-reject / prompt) remains open. Design in
  INVESTIGATIONS.md.
- [ ] **edit -base and miss-point reporting** — under discussion with the
  user; not scheduled.

Deliberately excluded this session: claim/watch and region leases (open design
questions for the user — span granularity, advisory versus enforced, claim
lifetime); the flat-record response conversion (wants the user's definition of
the target); owner/group preservation (needs a root-capable machine); parallel
walk (blocked on streaming results); terminal, tmux, iTerm2 and chord items
(need the user's terminal); tree-sitter (its own milestone); the /tmp/opencode
image fix (rides the `raj box` work).

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
- [x] **Document sync is incremental now.** `App.syncDoc` pulls the window
  `Session.OpsSince(since)` — keystrokes, ApplyDiff hunks, undo and reject
  reversals all land in the one journal — converts each span to a UTF-16 Range
  against the frame its predecessors produced, and replays the batch through a
  pinned copy of the server's last-known text: ranges go out only when the
  replay reproduces the buffer byte-for-byte. On any doubt it sends the whole
  document instead (kind not Incremental, version reset, batch over 64 edits,
  frame mismatch, mid-rune edge, replay disagreement); the capability is parsed
  number-or-options-object, unknown means Full. Found already in tree this
  session — the "unblocked" bullet above was stale; TestEditsSinceMultiHunkDiff
  added 2026-09-10. Host verification of the fuzz gates and a live gopls hover
  check ride the next rebuild+restart.
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
  focused pane are saved to `.git/raj/session.json` (or `.raj/` without a
  repository) and restored on start; `--no-restore` disables both directions.
  What is left of it:
~~The session is written only on a clean exit~~ — it is now also written from
  the idle tick, debounced to three seconds, and touched whenever a tab opens or
  closes. A crash loses seconds rather than the session.
- [ ] **Dirty-buffer restore** — persist the journal and add-buffers, validated
  by an orig-hash per buffer.
- [ ] **Attribution across restarts** — tint is commit-scoped, so it must
  outlive the process.
- [ ] **Proposal-review state must outlive the process too.** Raised by the
  user 2026-09-11, and the session made the case: a wave of subagent work
  lands as proposed change sets, the review loop walks them one at a time,
  and an accidental restart mid-review loses every pending decision (the
  restart that orphaned twelve groups this session lost the *proposals*; had
  any been accepted-but-unsaved, the acceptances would have gone too). Group
  state — proposed/accepted/rejected, keyed by group id and author — is
  small beside the journal, and it belongs with the two items above rather
  than on its own: persist the journal (dirty-buffer restore), the author
  table (attribution), and the group decisions (this) in one store, so a
  restored session resumes a review where it stopped instead of pretending
  nothing was pending. SQLite session store is the existing candidate for
  the journal; group state rides the same artifact. Storage shape evaluated
  against DuckDB (user asked 2026-09-11): columnar analytics over op-log
  appends is wrong-weight for a per-editor embedded store; the fit for
  heavy dependency is SQLite (modernc pure-Go, no cgo). If op-log analytics
  ever lands as a separate tool, DuckDB reopens then, not for the session
  store itself.
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

- [ ] **Responses are still JSON.** The flat record layout they need now exists
  — `prog.Writer` and `prog.Reader`, fields in a fixed order both ends read from
  the same source file. What is left is the conversion: `Header` has 35 fields,
  nine of them lists, `EncodeResponse` and `DecodeResponse` are the only two
  places that build one, and `.Header.` appears at 26 call sites. Worth doing
  verb by verb — ping, version, buffers, groups, read — with the header carrying
  whatever has not moved yet, rather than as one change that cannot be tested
  until all of it works. Caveat from the last driver session: the request side
  already rides opcodes, so what this conversion should now target wants the
  user's definition before it is worth implementing.
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

- [x] **`patch` (and `prog`) dropped their path on the wire.** `EncodeRequest`
  returned early for `patch` and `prog` before setting `h.Path`, so a patch
  request arrived pathless and the host resolved it against the active tab —
  refused when the snapshot named another file, silently misapplied if it
  happened to match. Found while driving dump/patch over TCP. Fixed in tree:
  `Path` is set in the `EncodeRequest` header literal, with a comment
  explaining why, and the regression case rides `TestFrameRoundTrip`; host
  verification rides the next `make check`.
- [ ] **A `find` step in the opcode pipeline.** `run -prog` cannot ask "where
  is this text" and get an offset back in the same program, so a batched driver
  round (find → read → apply) is several round trips today. A `find` op
  answering with a byte span is the single-round-trip version of the
  `search` and `read` bullets above.

- [x] **`apply` did not bounds-check its span.** Verified live: an apply with
  `-start 99999 -end 99999` against a ~50 KB buffer was accepted silently
  instead of refused, and the text landed in a near-empty document — the
  clamp-or-refuse guard the skill's offset warnings assumed was not there. For
  one agent that is a typo; for several writing concurrently it is silent
  corruption. Fixed: a shared `resolveSpan(start, end, size)` in
  internal/app/control.go refuses a span outside `[0, len]` with "offset out
  of range: [s, e) is not within [0, size)", and `host.Read`, `host.Dump` and
  `host.Apply` all resolve through it (apply wraps it as `hunk N: ...`).
  Regression: `TestSpanBoundsAreChecked` (app/control_test.go). Host-verified
  2026-09-10 (make check green); live after the next rebuild+restart.
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

- [x] **`buffers` reported size, not health.** A missing `}` and a stray ` }`
  both survived text-level reads, and gofmt masked the real break behind
  cascading "expected declaration" echoes. Now: `braceTally` (cli.go) computes
  a per-buffer net balance per bracket kind, string- and comment-aware through
  the same `syntax.ClassAt` seam bracket matching uses (nil spans degrade to
  plain counting, mirroring the matcher). `buffers -json` attaches it as
  `tally` per named buffer, computed CLI-side from live text, unsaved edits
  included; unnamed/unreadable buffers carry nil (omitted, never a false
  zero). Host-verified 2026-09-10 (`TestBraceTally{Balance,
  IgnoresStringsAndComments,WithoutALexer}`). Follow-up, not done: move it
  host-side into `host.Buffers()` + the `hBuffers` encoding once that wire is
  unfrozen — the CLI-side shape was forced by the frozen header, not chosen.
  Cheap enough to grow into the proposed `lint` verb.
- [ ] **`exec` is refused over TCP.** The verify-after-save loop (`raj ctl
  exec -- make check` once buffers land) is blocked unless raj starts with
  `--control-exec`, so the user hand-off is the only gate today. If the refusal
  is deliberate, the two bullets above are the driver-side substitute. If not,
  `--control-exec` on the canonical start line turns the gate into a
  self-service loop and this bullet disappears.

- [x] **`read`/`dump -start 0` returned the whole file.** Absent-means-zero on
  the wire header: an explicit `-start 0` was indistinguishable from no flag,
  so a span read or dump from byte zero came back as the whole buffer.
  Verified live against `read` too, not just `dump`: `read -start 0 -end 400`
  returned everything while `-start 100 -end 400` worked. Fixed with a
  presence fix, not a bit: the four span fields (`Start`/`End`/`LineStart`/
  `LineEnd`) are pointers, and `encodeHeader` now emits their op directly
  under the nil check (mirroring `Base`) instead of routing through the
  zero-skipping `num` helper. Regression: `TestStartZeroSurvives`
  (wire_test.go). Host-verified 2026-09-10 (make check green); live after the
  next rebuild+restart.
- [ ] **`search` has no `-path`/root flag.** `-include` globs suffice; noted
  by a driver for completeness.

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
- [ ] **`claim` and `watch`.** An agent announces the files it is about to touch;
  others are told. Streaming frames and cancellation already exist, so a pushed
  event is nearly free and beats polling — this is the use case that justifies
  the notifications item above. The fuller design from the last driver
  session: an agent declares intent (file, optional spans) before writing; the
  editor records the claim, makes it queryable over the socket, and reconciles
  overlapping edits through the rebase walk — conflict, not clamp. It
  generalises the dump/patch version-pin to a shared coordination surface and
  derives its span record from the same group-ranges work as the proposals
  remainder. Open design questions for the user: span granularity (file plus
  refineable spans?), advisory versus enforced overlap, and claim lifetime
  (tie to connection liveness). Design doc and verb spec before
  implementation.
- [ ] **Which composition does `read` return?** Accepted-only is the argument:
  an agent should propose against the agreed base, not against another agent's
  unaccepted guesses, or overlap detection compares offsets in different
  frames. A flag would ask for the annotated view.
- [ ] **The sidebar does not use the streaming path yet.** `search.RunStream`
  and the snapshot split exist and the socket uses them, so a search over the
  socket runs off the event thread and reports as it goes. The pane still calls
  `RunDocs` and waits. Moving it over is the other half of the seam this file
  already names, and it is now a small change rather than a design.
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
- [ ] **Version handshake on the control connection.** The concrete answer to
  the version skew the notes section leads with, spec against the wire
  (`internal/control/control.go`). Server side: read `vcs.revision` from
  `debug.ReadBuildInfo()` at startup — embedded automatically from a VCS
  checkout, confirmed present in both `bin/raj` and `bin/raj-linux`, so no
  `-ldflags` stamp is needed — add `SrcVersion string` to `Response`,
  populate it in the `hello` reply, and stamp it on every response in
  `connection.send` so a reconnecting client learns it without
  re-handshaking. Client side: read its own revision the same way and on
  mismatch print a one-line warning naming both commits — warn, not refuse: a
  stale pair still works for the verbs that did not change, and the user
  decides. Wire-compatible: `Response` fields are sent only when nonzero, so
  an old client ignores the new field and an old server omits it; empty means
  "unknown" and the check stays silent. What this catches that
  `go version -m <file>` cannot: the running process reports its own commit,
  so "rebuilt `./bin/raj` but never restarted the editor" surfaces at connect
  time instead of mid-session. Open sub-question for the user: also expose it
  as `raj ctl version` with no path (server build info rather than buffer
  version), or keep it handshake-only so there is exactly one place the
  comparison lives?
- [ ] **Server-minted agent identity, in the same handshake.** Same `hello`
  wire change as the version handshake, shipped as one proposal — both are
  metadata the handshake should carry. Verified live: one session of `raj ctl`
  over TCP minted **253 anonymous author ids** (each invocation is a fresh
  process that mints an id and abandons it), the `uint8` space caps at 256,
  and pending proposals end up attributed to a dead `anon-N`, so "who wrote
  this" is meaningless and `nextAuthor()` drifts toward its `FirstAgent`
  fallback — silent misattribution. The durable-identity mechanism already
  exists and is proven (`TestHelloRebindsTheAuthorID`): `hello` carries
  `Identity`, and `Participants.Join` maps it to the same author id on every
  reconnect. What is missing is that the client must invent and remember the
  string, and `raj ctl hello` is not a command (notes section, item 3), so
  almost nothing binds first. Fix: on first anonymous TCP connect the server
  mints an identity token and returns it in the `hello` response; the client
  persists it in `RAJ_IDENTITY` (like `RAJ_CONTROL_TOKEN`) and presents it on
  every later connect, rebinding to the same author. Server-minted, not
  client-chosen: no collisions, no two agents picking `claude-1`. `raj ctl`
  reads `RAJ_IDENTITY` and says `hello` with it automatically before any other
  verb — bind-first as the default; when unset, it adopts the server's token
  and prints it once ("set RAJ_IDENTITY=tok_…"). `raj box` injects
  `RAJ_IDENTITY` alongside `RAJ_CONTROL_ADDR`/`RAJ_CONTROL_TOKEN`. Resolves
  notes items 3 and 8 and the proposals-on-dead-ids review problem. Open: with
  identities durable, does the 256 cap still bind, or are `gone` ids recycled?
- [x] **`who` listed every participant the process had ever seen.** After one
  TCP session the listing held 255 entries, all but three dead anons, which
  made it noise exactly when several drivers need reading apart. Fixed as a
  `-live` flag, client-side on purpose: the registry's full listing is the
  attribution record (a gone participant's text is still in the document), so
  the wire keeps every row and the flag is a view. `Registry.Join`/`Leave`
  flip `Connected`; `-live` filters on it. Regression:
  `TestLeaveMarksGoneNotLive` (participant_test.go). Host-verified 2026-09-10
  (make check green); live after the next rebuild+restart.
- [ ] **Dump snapshots are keyed by author id, not by identity.** A rebound
  identity keeps its text but not its snapshots, so `dump`→`patch` across two
  CLI invocations fails until the driver re-dumps. Keying snapshots by the
  bound identity string keeps them across reconnects.
- [~] **`RAJ_IDENTITY` is absorbed by the plugin, not the agent — DONE and
  reworked for swarms.** Absorption was already live (the running agent never
  saw a token). The plugin has now been reworked from "process-wide shared
  token" to **distinct-per-session**, proposed in buffers (2 groups, author 3)
  and pending host verification — NOT yet live, since plugins load at opencode
  start. New design: a session's first `raj ctl` injects nothing; the server
  mints a fresh durable `tok_…`, the CLI prints the adopt line, the plugin
  captures it into a per-session Map and scrubs it before it reaches the
  model; later shells of that session get their OWN token reinjected via
  `shell.env`. REMOVED: the `sharedToken` fallback and the
  `process.env.RAJ_IDENTITY` write — the process-wide pin was the swarm bug
  (a spawned child silently inherited the parent's author id, one tint for
  the whole swarm). Known-raj gating is now structural (only a session's own
  minted token is ever injected); capture+scrub stay ungated so a subagent's
  adopt line is never leaked. Explicit `$RAJ_IDENTITY`/`-as` still wins.
  Tension resolved: "same agent resumed" vs "sibling spawned" are
  indistinguishable at the plugin API (both a new sessionID); distinct-per-
  session wins — re-pin explicitly to resume an id. Remaining pairing: `raj
  box run` injecting `RAJ_IDENTITY` alongside `RAJ_CONTROL_ADDR`/
  `RAJ_CONTROL_TOKEN`. Host verification steps are in the plugin header
  (steps 1–6, including the two-subagents-three-ids swarm check). The
  original success criterion is met and extended: the skill no longer
  mentions token handling (SKILL.md trimmed), and each session/subagent now
  gets a DISTINCT author id without the agent knowing why.
- [ ] **Editor-side identity durability (the swarm blocker).** Absorption is
  the client half; this is the server half. Verified in source: `Registry`
  (internal/control/participant.go) never recycles — `r.next` only climbs,
  cap 255 — and every connection takes a provisional dead `anon-N` before
  hello (`Server.serve`, `nextAuthor`), so `who` floods with one dead anon
  per CLI call (144 ids for ~2 real participants observed). Identities are
  durable per-identity only within one process; an editor restart re-mints.
  For a usable swarm: (a) recycle `gone` agent ids or persist the registry
  across restarts so an identity's author id outlives the process (answers
  the open question on the server-minted-identity bullet — currently NOT
  recycled); (b) `who -live` / drop dead anons so a swarm is readable; (c)
  key dump snapshots by identity string, not author id, so `dump`→`patch`
  survives a reconnect; (d) stable swarm names (`who -as X -name Y` already
  binds) so `who` shows `raj-explore`/`raj-impl-1` instead of bare tokens.
  Spec first (design doc + verb spec) before any editor change.
- [ ] **`exec` over TCP is refused, not sandboxed.** The refusal is correct —
  the command would run on the editor's machine, outside the container the
  driver was put in — and it costs the staleness check, which is the one thing
  `exec` was for. A driver running tests in its own sandbox has no way to be
  told it is testing files that do not match the buffers. `buffers` answers it
  with a second round trip and nothing prompts the driver to make one.
- [ ] **Region leases.** `apply` rejects a stale hunk after the fact; a lease
  would stop the user and a driver being told they both own a span in the first
  place. The conflict report carries the version that invalidated the range, so
  the information a lease needs is already on the wire.
~~No way for the user to speak to a driver~~ — `recv` parks until there is
  something to say, `Server.Send` and `App.Tell` post into a per-participant
  mailbox, and messages keep across a reconnect because the mailbox is keyed on
  the durable author id. No push and no subscription state: the request/response
  shape holds, the request just does not answer yet. What is left of it:
- [ ] **Nothing in the editor calls `App.Tell`.** The transport, the CLI and the
  tests are there; the chord and the prompt are not. A binding is a claim on a
  chord the terminal then stops delivering to anything else, so it belongs in
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
- [ ] **Inspection should not force a tab; proposals should.** Today even a
  read-only look — `read`, `version`, `lsp diagnostics` — needs `open` first,
  so an agent surveying twenty files puts twenty tabs in front of the user and
  then closes the ones it did not touch. Flip the default: the read-before-write
  gate opens a buffer headlessly (tracked, not shown), and the tab appears the
  moment a buffer holds pending proposals — review is exactly when the user
  needs the file on screen, and a buffer with review chunks should not stay
  hidden. `open` then means what it says: a request to show the user. The
  close-when-clean convention in the skill becomes automatic rather than
  agent-disciplined.
- [ ] **SQLite session store** — the op log as the shareable, forkable artifact.
  Unchanged by the socket, and the socket makes it more useful rather than less.

## `raj box` — the container build/run, folded into the CLI

The two shell functions (`bldraj`, `oc`) that build the opencode image and run
the agent container move into the binary as `raj box build` / `raj box run`,
added at the `raj ctl` seam in `cmd/raj/main.go` — a different program sharing
a binary, dispatched before `flag.Parse`. Goals: the agent always runs against
an up-to-date binary, and the address/token/port wiring stops being manual.
The image (`Dockerfile.opencode`) is `node:22-slim` + opencode + an `oc` user
(UID 501/GID 20), and COPYs four files out of this repo: `bin/raj-linux`,
`skills/raj-editor/SKILL.md`, `plugins/raj-gate.ts` and
`opencode/{opencode.json,agents/raj.md}`.

- [ ] **`raj box build`** — cross-compile (`GOOS=linux GOARCH=amd64
  CGO_ENABLED=0 go build -o bin/raj-linux ./cmd/raj`), then `docker build`
  with the build context at the repo root rather than `~/Desktop/projects`,
  so the COPY paths are repo-relative and always current (today the Dockerfile
  lives one directory up and only sees a committed binary). Stamp the image
  with `--label raj.src=$(git rev-parse HEAD)` — a diagnostic only, not the
  freshness guard (see the resolution below).
- [ ] **`raj box run`** — `--agent raj --rm -it`, the data volume and the
  API-key passthrough. Generates the control token itself (crypto/rand,
  replacing the manual `openssl rand -hex 32` export) and injects it into both
  the container env and the control address, and picks a free loopback port
  instead of hardcoding 7391, so two instances no longer collide.

The stale-binary fork is resolved. The Dockerfile puts raj the binary in the
box, but in the current arrangement the agent is a pure `raj ctl` client of
raj the editor on the host over TCP: rebuilding the image refreshes only the
binary baked in at `docker build` time, a `-v` mount never touches
`/usr/local/bin/raj`, and neither reaches the long-running host process that
actually holds the buffers. So the immediate fix is model (b): mount the repo
(`-v "$PWD:/work"`) and run raj's own editor in the box, so a rebuild
genuinely delivers a fresh binary — and with the source mounted, a Go
toolchain in the image becomes worth adding. The durable fix for two binaries
that can drift is the version handshake on the control connection (spec above,
with the TCP items); the originally proposed `-ldflags -X main.version` stamp
is unnecessary because `vcs.revision` is already embedded (confirmed in both
`bin/raj` and `bin/raj-linux`).

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

## Notes from driving `raj ctl` as an agent

One session's worth of friction, recorded where the next driver will find it.
Each item is cheap relative to what it cost to work around.

- **Version skew between host binary and container CLI is invisible.** The
  running editor predated `read -start/-end` in its own source: the request
  fields were silently ignored and the whole buffer came back, with no hint
  that anything had been dropped. Two things would have caught it in one
  round trip: `whoami -json` (or an `about` verb) reporting a build version
  or commit the driver can compare against the source tree it sees, and a
  `-strict` mode where a request carrying fields the server does not know is
  refused rather than served degraded. Decode-silently-ignores is the right
  default for forward compatibility; an opt-in refusal is the mode an agent
  wants, because its workaround for a missing feature is always worse than
  the error. The build-version half of this now has a concrete design: the
  version handshake on the control connection, spec among the TCP items
  above.
- **`edit`'s refusal does not quote what it saw.** "does not appear in the
  buffer" against a 40-line `-old` leaves the driver diffing blind.
  Reporting the longest common prefix of the miss (or the offset of the
  nearest match) would turn the retry into a targeted fix instead of a
  re-read of the whole file.
- **Container ergonomics: no shared filesystem means stale-file checks are
  manual.** `exec` is correctly refused over TCP, but then nothing warns the
  driver that the bytes it is about to compile in its own sandbox do not
  match the buffers. `buffers -json` answers it with a second round trip;
  what is missing is the prompt to make one. A client-side flag (`exec
  -warn-stale`, refusing locally if any buffer is dirty) would move the
  check into the driver's own sandbox where it belongs.
- **Path mapping is applied inconsistently across verbs.** After an editor
  restart with `RAJ_ROOT_MAP=/work=/Users/.../raj` in force, `buffers`
  reported `/work/TODO.md` and `open`/`version` accepted it, but `read
  /work/TODO.md` answered "no open buffer for that path" while `read
  /Users/.../raj/TODO.md` succeeded — the same string the editor itself had
  just reported. The `text` op resolves the path without translating it,
  where `open` translates first; a driver should never see the editor's
  internal paths at all, and the ones it is handed should round-trip through
  every verb.
- **An editor restart orphans the driver's read-before-write state.** The
  rebuilt editor kept the buffers open but reset every version to 0, so a
  driver holding offsets from before the restart has coordinates against a
  document that no longer exists. That is correct — anything else would be
  worse — but nothing says so. A connection-scoped "editor restarted" marker
  (a generation counter on `whoami`, bumped per listen) would let a driver
  bin its cached versions instead of discovering staleness one refused
  apply at a time.
- **No Go toolchain in the container, so the host verifies.** No shared
  filesystem and `exec` refused over TCP: agent work lands as proposals in
  buffers, and the contract is that the user accepts and saves, then
  `gofmt -w && go test ./... && make check` runs on the host. State it every
  session.
- **Bind an identity first over TCP, or every invocation is a new author.**
  Each `raj ctl` call reconnects, and an anonymous reconnect mints a fresh
  author id — per-author state (dump snapshots) does not survive between
  invocations and proposals scatter across dead ids. `hello` is not a
  `raj ctl` command: identity binding goes through `who` — `who -as X -name Y`
  binds a name, and later `-as X` connections keep its id. Bind first, then
  work. Open: add `raj ctl hello -as X -name Y` as the identity verb, or
  document `who` as that verb.
- **The Stream A/B/C/D labels are used but never defined.** The globs fix is
  Stream B, scroll-ratio restore is Stream C, the driver-round additions ride
  Stream D, and no file says what the streams are. Needs definitions from the
  user to write.

- **The rebuild boundary is not written down as a workflow.** New verbs are
  compiled into the binary; buffer edits cannot make them live. Both
  subagents in the 2026-09-09 session handled it correctly — verify semantics
  against the running editor, state the host-side test contract — and each
  had to discover the boundary for itself. The skill should state the loop
  explicitly: propose in buffers, user accepts and saves, host rebuilds,
  verify over the socket.
- **The tooling prohibition needs to name the temptation.** "Use only raj
  ctl verbs" invites the reading "for file access", and JSON post-processing
  slips in under "just parsing tool output" — the orchestrator itself did it
  once that session (python3 on `read -json` output; it failed on the spot,
  no source edits went through it). Briefs should say: no interpreters
  (python/node/jq) anywhere in the pipeline, including on `raj ctl` output;
  if the output is hard to consume, that is a verb-surface gap to report. A
  query flag on the verb (`read -json -field text`, in the spirit of the
  flat-record item) is the sanctioned shape, not a pipe to an interpreter.
- **The baked skill led with a verb the running build did not have.** The
  image baked into the container instructed `raj ctl hello -as ... -name
  ...`, which fails on contact against a pre-handshake build: there is no
  `hello` command. The working pattern is `who -as X -name Y` once, then
  `-as X` as the first flag after the verb on every call. This is the
  version-skew class the handshake exists to kill, biting the skill itself.
  The skill is fixed now; the note stays as the recorded failure mode,
  because any image older than the editor will produce it again.
- **`-as` is refused before the verb and accepted after it.** "unknown
  command" in one position, silently fine in the other, and the usage text
  shows neither — first flag after the verb is the only placement that
  works. Also observed: `whoami -as X` printed a fresh anon id rather than
  the bound one. Unreproduced since; treat it as a suspected bug and
  confirm before trusting `whoami` as the bind check.
- **`RAJ_IDENTITY` was not honoured by the pre-handshake build.** Set and
  exported, a fresh anon was minted anyway. Now fixed — the handshake
  session verified a server-minted token is adopted and rebinds — so this
  stays only so a driver on an old build recognises the symptom instead of
  debugging its own environment.
- **Ten exploratory commands burned twelve author ids.** 26/256 used
  mid-session, 55/256 by session end, almost all of them dead anons. That
  is the live validation for server-minted identity, and the evidence for
  recycling `gone` ids — the open decision on the server-minted-identity
  item above.
- **An empty `/work` in the container is expected, and `ls` is the trap.**
  The mount point is empty inside the container while the editor's root
  maps to the host repo, so the reflexive sanity check reads exactly like
  "the files are gone". The check that works is `raj ctl buffers`; the
  container filesystem was never the truth.

Notes from the Wave 1 reconstruction session (2026-09-10), after the
accidental restart wiped the unsaved proposals and they were re-applied
piece by piece:

- **Two raj processes squatted one TCP port and the driver talked to the
  other one.** An accidental editor restart left PID A bound to
  `127.0.0.1:7391` and PID B to `*:7391`; the container reaches the host
  via `host.docker.internal` (non-loopback), so only the wildcard bind was
  reachable, and that process held the connection open while speaking
  nothing. Every verb hung, with no refusal. The token was right all along
  — the port was wrong. `lsof -iTCP:7391 -sTCP:LISTEN` on the host was the
  only way to see it. Both failure modes want a real answer: the still-open
  "one listener, not both" bullet above, and a client-side connect timeout
  that names "the port answers but no control frame came back" as the
  wrong-process case rather than hanging.
- **A restarted editor orphans in-flight proposals, and the recovery is in
  the driver, not the editor.** The orchestrator re-applied Wave 1 hunk by
  hunk from its own task history after the restart. That worked because the
  subagents had reported complete function-level designs. It is still the
  user hand-off gap the dirty-buffer-journal item names; the session notes
  here just add that a clean `raj ctl buffers` read (all `saved`) is what
  confirmed the proposals were really gone rather than merely unaccepted.
- **`lsp diagnostics` as a per-hunk smoke check worked.** The per-hunk loop
  settled into: apply → `lsp diagnostics` → seam re-read → `goto` + focus
  the tab → user review → cmd+s. The diagnostics call caught nothing this
  session, but it occupied the slot where a malformed hunk would have shown
  (the one bad hunk in Wave 1 was a bad test fixture, which diagnostics
  cannot see). Cheap enough to keep in the skill's loop.
- **The `braceTally` fixture failures were bad fixtures, two rounds.** A
  raw-string case and an unbalanced `{ [(] }` both failed; ambiguity about
  which constructs the Go lexer unambiguously classifies means the durable
  fixture is double-quoted strings plus comments, nothing fancier. The
  lesson to keep: when a tally is string/comment-aware, its tests will be
  only as stable as the lexer's classification of the constructs chosen.
- **`exec` refused over TCP still costs the verify loop.** Wave 1 needed
  `go test ./internal/control -run TestBraceTally -v` on the host twice;
  the agent could not run it, and each round was a user hand-off. That is
  the intentionally-recorded trade (the refusal is correct), but the
  per-hunk-loop above now has to lean on `lsp diagnostics` and claim that
  semantic checks move to the user. Correction to an earlier draft of this
  note: there is no `ping` verb (`raj ctl ping` is "unknown command") — ping
  exists only as a protocol op the test harness sends. If a health-check
  verb is wanted, that is a (cheap) gap, not an omission from usage.
- **Color/readability on proposals is a user-reported niggle.** A
  screenshot review of a proposal hunk prompted "the colors are a bit
  difficult to read" — recorded here rather than guessed at, since the tint
  palette is the user's terminal theme and any fix is a renderer design
  question (which palette index proposals use, or a high-contrast mode),
  not a Wave 1 item.
- **`braceTally` counts markdown fences as code.** `buffers -json` on
  TODO.md reports nonzero parens/brackets because unclosed `(`/`[` in prose
  are counted — correct for a string/comment-aware tally that has no
  markdown-fence class, but a false positive the reader has to dismiss. If
  it ever becomes the `lint` verb, "is this file really source" is a
  per-language question it currently cannot answer.

Agent tool-usage review, wave reports 2026-09-10/11 (filed per the new
RECURSIVE_RAJ §8 step — deduped against the notes above; "none reported"
waves were W2a and most of W2b, meaning the surface held):

- [ ] **`edit` takes no `-base`, and a failed one does not report the miss
  point.** Reported by the 3c subagent. `edit` is the convenience form of
  `apply`, but it drops the version pin entirely: an edit against a stale
  read replaces whatever text is CURRENTLY there. Two separable fixes: a
  `-base` on edit (the old text must match as of that version), and the
  refusal quoting the longest-common-prefix point, which the earlier note
  already asks for. Also: that multiline `edit -old` works by exact match
  is undocumented — one line in `--help` or the skill.
- [ ] **`search` silently ignores a bare path argument and an unmatched
  `-include`.** Reported by Wave 1 subagent A: a positional path is
  accepted and ignored, and a `-include` glob that matches nothing returns
  `(no output)` — indistinguishable from "no matches". Both want a refusal
  or a warning; the current shape trains drivers to believe an empty
  result is authoritative.
- [ ] **`read` rejects relative paths while `open` accepts them.** Wave 1
  subagent A: `read path/to/file.go` answered "no open buffer for that
  path" where the absolute path worked. Same class as the path-mapping
  note above (verbs resolve paths inconsistently) — one resolution seam,
  used by every verb.
- [ ] **`/tmp/opencode` is not writable in the opencode image.** Reported
  independently by subagents B, 3b, and W2b: the directory is root-owned
  0555 while the tooling docs call it pre-approved scratch space. Every
  subagent routed to plain `/tmp`. Fix belongs in the image build
  (Dockerfile.opencode: `mkdir -m 1777 /tmp/opencode` or chown to oc) and
  rides the `raj box` work.
- **Bash heredoc/`"` nesting in `edit -new` arguments is fragile.** 3c
  reported a chained command that quoted poorly and had to be split into
  separate guarded edits. Partially a discipline note (already in the
  skill: single-quote args, use -old-file/-new-file for big blocks), but a
  `-new-file` path is the documented escape and subagents reached for it
  only after a failure — briefs should name it up front for multi-line
  payloads.
- [ ] **FakeHost.Press fails silently on an unknown chord name.** Cost a
  make-check round 2026-09-11: a test pressed "escape" where the canonical
  name is "esc", the host dropped it, and with a modal Prompt swallowing
  every key the only signal was an assertion three steps later. The fix was
  the test name, but Press should say so: panic or fail the test on an
  unknown chord. One line in fake.go, and every future mistyped chord stops
  being a debugging session.

Agent tool-usage review, wave reports 2026-09-10 (second plan instance, waves
0/1/3 — the sync wave reported the surface held; new items below):

- [x] **`search` truncates silently.** Fixed 2026-09-10: the per-file cap is
  surfaced (response opcode 0x43 plus a CLI "truncated" report naming each cut
  file as shown-of-total). Needs a rebuild, and the container image for the
  CLI half.
- [x] **`-regex` anchor semantics are undocumented.** Fixed 2026-09-10:
  `compile` adds `(?m)`, so `^`/`$` match per line as a grep user expects;
  literal quoting keeps a literal `^` literal.
- [ ] **No file-listing verb.** Package discovery needs a
  `search -q 'package x'` workaround; a glob/list verb would close it.
- [ ] **`-include` glob semantics surprise.** `*COMPLETED*` matched nothing
  against a file named COMPLETED.md — matching runs against walker-relative
  paths in a way a driver cannot predict. The new matched-nothing warning says
  THAT nothing matched, not why.
- [ ] **New ctl against an old server warns falsely on -include.** `Considered`
  rides a new header field (0x42); an old server never sets it, so every
  -include search looks like zero files considered. warnVersionSkew prints on
  the mismatch, which is the designed mitigation — recorded so a driver
  recognises the pairing.
- **Truncated tool output spills to a dead-end path** (harness-side, not raj):
  the spill file under /home/oc/.local is outside raj's root and unreadable by
  a subagent. Driver guidance: keep searches narrow.
- **`open` on a nonexistent path creates the buffer** — relied on by a
  subagent to create a test file; works, undocumented in the skill's refusal
  table. One line to add (orchestrator direct).
- **One garbled line on a long `read -lines 1,330`** — unreproduced on re-read
  of the exact span; suspected display-side one-off, recorded in case it
  repeats.

Agent tool-usage review, wave reports 2026-09-10/11 (this session's swarm —
review-flow chords, relative paths, search honesty, and a read-only save-lag
diagnosis; deduped against the notes above):

- [ ] **`raj ctl groups` under-reports a multi-hunk proposal.** On
  `internal/keys/table.go` it listed one set (`1 ... +0 bytes`) while
  `raj ctl diff` rendered three. The summary is actively misleading for
  review; `diff` is the honest listing.
- [ ] **`lsp diagnostics` returns `{}` for every file, including while gopls
  is starting.** An empty object is indistinguishable from "no problems", so
  it cannot serve as the per-hunk compile check the review loop leans on. A
  "server starting" / "no server" status would be honest.
- [ ] **`edit` has no stdin form.** `-old-file -`/`-new-file -` do not exist,
  and a multi-line inline `-new` breaks on an apostrophe closing the shell's
  single-quoted string, leaving a broken intermediate group. The file form
  works but subagents reach for inline first; a stdin form would remove the
  trap.
- [ ] **A positional span argument is silently ignored.** `raj ctl read
  <path> 940 1000` returns the whole file with no error; the numbers look like
  offsets. Refuse, or say the extra argument was ignored.
- [ ] **`search` gives no hint when a literal query has metacharacters.**
  `-q 'func (a \*App)'` matched nothing while `-regex` would have; a note when
  a pattern contains regex metacharacters but matches literally zero would
  save a round trip.
- [ ] **`read` on an unopened path reports the editor's host path.** It
  answers with the `/Users/...` spelling rather than the translated `/work`
  one the driver can see, which reads as a mismatch (same family as the path
  mapping note above).
- **Concurrent writers share the version stream, and rebasing handles it.**
  Two subagents edited `control.go` concurrently; a hunk applied with a stale
  `-base` rebased onto the other writer transparently. Worth knowing, not a
  defect.
- **A stale root `/work/KEYBINDINGS.md` duplicates the tested
  `docs/KEYBINDINGS.md`.** No test reads it; delete it.

## Agent-efficiency review — the walk and the ctl surface (2026-09-11)

Two reviewers read the D1/D2 and H1/H2/H3 subagent reports and the walks they
implied; a follow-up swarm implemented the cheap half. "Walk discipline" now
lives in RECURSIVE_RAJ §5 (anchor → locate → owning file → enclosing block →
seam). Items below are deduped against the notes above. Items marked `[x]` are
in tree and host-verified 2026-09-11; the rest still need the host rebuild, or
remain open.

Cheap, client-only or one-file:

- [x] **`apply` cannot take more than one hunk.** `-start/-end/-text` build one
  `Hunk`, though `Request.Hunks` and the encode loop already carry a list. A
  `-hunks FILE` form (JSON Lines of `{start,end,text}`, `-` for stdin) landed.
  In tree, host-verified 2026-09-11. Follow-on: nothing emits that format, so
  an agent still computes offsets by hand — a `diff -hunks` or
  `apply -from-diff` would close the loop.
- [ ] **`lsp references`.** No way to ask "who calls this"; the modes are
  hover/definition/completion/diagnostics. Reuses `LSPResult.Locations`; no
  header change. Replaces `search -q name` plus a read per hit, and separates
  same-named symbols.
- [ ] **`diff` hunks have no line numbers.** Byte `start/end` only, so naming
  or reading a hunk costs another call. Add line/end_line (nested DiffJSON) and
  print `@@ L12..L18 (bytes 120..148) @@` (in the buffers).
- [ ] **`diff` drops moved hunks to a bare count.** A brand-new file edited
  after insertion reports "moved … review the buffer directly" and shows
  nothing. Emit the recorded old/new marked as-written.
- [x] **`-include`/`-exclude` match basenames for no-slash patterns.** Fixed in
  `internal/search/search.go`; corrects a wrong comment there and a wrong
  COMPLETED.md claim. In tree, host-verified 2026-09-11. Distinct from the
  existing `search -path` item.
- [ ] **`open` creates a buffer for a typo'd path, silently.** Refuse a
  nonexistent path unless `-create`; say which happened. A swarm making this
  mistake creates phantom tabs.
- [ ] **`lsp diagnostics` says ok+clean when the server never published.** The
  Status/Detail work is in the buffers; this is its one hole — add a
  `published` set and an `unpublished` status.
- [x] **`decodeHeader` silently truncates a malformed list.** Fixed (checks
  `Reader.Bad` after each list). In tree, host-verified 2026-09-11; records the
  failure mode that outlived the signed-varint fix.

Design first — wire (header + encode/decode + host + CLI) or a new op:

- [x] **`buffers -json` pending/moved per buffer.** Landed as a new sparse
  `hBufferState` field (0x44): appending to the record-shaped `hBuffers` would
  break old readers, so the counts ride a separate argument field. The swarm
  read path: "which open files hold proposals" in one call. In tree,
  host-verified 2026-09-11. Note it also makes `version` pay for the
  `DiffPending` walk (host.Buffers is reused) — split a cheap path if that
  latency matters.
- [x] **`accept -all` / `reject -all`.** Client loop landed. In tree,
  host-verified 2026-09-11; the one-frame form still wants an `All` argument op.
- [ ] **`reject -all` should go newest-first.** Verified live 2026-09-11: with
  an earlier set whose span a later set edited, the client loop rejects in
  document order, so the earlier `reverseGroup` is blocked by the later
  overlap, the loop reports "1 of 2 could not be rejected", and the older set
  stays applied. Rejecting in reverse document order (and re-listing after)
  would let a bulk reject unwind an overlapping stack; `accept -all` is
  order-independent. A second live run showed it worse: after rejecting the
  first set, the second reported "later edits overlap it" even though both
  ended at 0 ops, leaving the buffer dirty-but-empty and unclosable without a
  save — so the bulk form can wedge state, not just skip a set.
- [ ] **`groups` hunks/moved.** `groups` still cannot say a set has N hunks or
  M moved members without `diff`; `Group.Hunks`/`.Moved` keeps the listing from
  drifting again.
- [ ] **`open -create`.** Argument op + `host.Open` signature.

New friction from the 2026-09-11 swarm wave (all subagents, deduped):

- [ ] **`search -json` `ByteStart` is the matched substring, not the line
  start.** An apply anchored on it began 25 bytes into a bullet and ate the
  next line's prefix. Document that offsets are matched-text coordinates, or
  add a line-start offset.
- [ ] **`search` offsets and displayed `Text` disagree on leading
  indentation.** A match at a line start shows a leading tab in `Text` but
  `ByteEnd-ByteStart` excludes it, so an insert lands one tab late. Same family
  as the item above.
- [ ] **`groups` and `diff` list different pending sets.** Reported by three
  agents this wave: `groups` returned 5 of 6 rows (one with `bytes 0`), and 2
  where `diff` showed 3 hunks; ids do not line up between the views. The H2
  varint fix is in tree (host-verified 2026-09-11); re-check on the rebuilt
  binary before calling it fixed.
- [ ] **`read -lines` disagrees with the reported line count at EOF.** After
  edits, `read -lines N,N` on the last reported line fails "offset out of
  range" (off-by-one/trailing newline). Byte edits are unaffected; the line
  counts are not trustworthy.
- [ ] **`-text-file -` heredocs always end in a newline** and split a one-line
  literal mid-line when the payload was meant to replace it verbatim. A
  "no trailing newline" note or a verbatim flag would remove the trap (the same
  reason `printf` got reached for).
- [x] **The `raj-editor` skill's glob field note is stale.** It said globs
  match the relative path, not the basename; the no-slash change makes the
  basename apply too. `skills/raj-editor/SKILL.md` now documents the rule. In
  tree, host-verified 2026-09-11; the container copy still rides the image
  rebuild.
- [x] **Option A moved the moved-hunk goalposts for tests.** Under the
  projected-run model an additive edit leaves surviving runs; the
  `internal/app/mode_test.go` moved-past assertions were updated with D4. In
  tree, host-verified 2026-09-11.
- [ ] **`edit` (and possibly other write verbs) need the editor's own path
  spelling.** `read`/`search`/`apply` accept the translated `/work/...` path;
  `edit /work/...` can fail "no open buffer for that path" or "outside the
  workspace", and the host spelling works. Same family as the path-mapping note
  above; re-check which verbs translate on the rebuilt binary.

- [ ] **A save can signal the driver (review-loop handoff).** Low priority, and
  the same gap as "nothing calls `App.Tell`": in a per-file review the user's
  cmd+s is the decision point, but the agent sits at a turn boundary rather
  than parked on a socket read. A save hook that posts `saved <path>` via
  `App.Tell`, plus a client parked on `recv`, would let a save start the next
  review step without a typed "next". The opencode half — a plugin/skill that
  turns a mailbox message into the next turn — is what makes it automatic.

Duplicates, deliberately not re-filed: the `find` opcode, a file-listing verb,
`read -json -field text`, flat-record responses, the version handshake,
`edit -base` + miss-point reporting, `search -path`/`-dir`, inspection-without-
a-tab, `read` on an unopened path reporting the host spelling, `exec
-warn-stale`.

## Agent tool-usage review, 2026-09-11 (inlay hints, fallback)

Deduped against the notes above. The inlay-hints rewrite is the substantive work
of this session; the defects it surfaced are filed here, and the remaining
inlay-hints steps follow.

- [ ] **Line-index corruption (severe).** A buffer driven through many
  `apply`/`reject`/`patch` cycles reported ~2x the true line count (`version
  -json` said 1212 lines for a 21022-byte file) and `read -lines` near EOF
  computed offsets past EOF, while the text stayed correct; it cleared after
  save + close/reopen. The existing "The line index and the document disagreed
  after a batch" note (INVESTIGATIONS.md) is the same class, and this is a hard
  reproduction, so the two want investigating together.
- [ ] **`search -json` `ByteStart` recurrence (dedup of the two items above).**
  Still the matched-substring start, not the line start, and still excludes the
  displayed `Text` leading tab; this session it cost a corrupted apply in the
  mouse.go span. Third report; the existing items stand.
- [ ] **Cross-buffer diagnostics staleness.** gopls reports `undefined`/stale
  results when a different unsaved buffer is unsynced, and `lsp diagnostics` can
  read `ok` for file A while file B is stale. The freshness fix only covers a
  same-file version mismatch, so the cross-buffer case is still open.
- [ ] **No raw-LSP verb.** There is no way to inspect inbound
  `publishDiagnostics` frames or their `version` field, so the assumption in the
  freshness fix that gopls sends `version` is unverifiable from the socket. A
  debug verb or a trace would close it.
- [ ] **`/tmp/opencode` is root-owned and not writable.** Still true (dedup of
  the earlier note); rides the `raj box` work.

Inlay hints - remaining:

- [ ] **Toggle chord.** `keys.ToggleInlayHints` plus a `keys/table.go` row:
  native `shift+super+i`, CSI-u `105;10u`, mac `cmd+shift+i`, linux
  `ctrl+shift+i`; persisted as `session.Pane.Hints`; a `KEYBINDINGS.md` row; the
  accounting tests. (`App.InlayHints` defaults true and `p.Hints` already
  exists.)
- [ ] **Cross-cutting tests.** Hover anchor, find-after-hint,
  selection-over-hint.
- [ ] **`raj ctl lsp inlay-hints` verb.** Whole file with `-lines A,B`; the
  eight control layers plus the range plumbing.
- [ ] **Mouse-hover tooltips.** VS Code-style, using the hint `Tooltip`; depends
  on the column map, not on wrap integration.
- [ ] **`cmd+.` apply-hint-edit.** Applies a hint `textEdits`.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
