# TODO

Open work only, collapsed 2026-09-17. Measured numbers live in BENCHMARKS.md;
root causes, terminal findings and decisions live in INVESTIGATIONS.md. Raw,
dated agent feedback lives in AGENT-FEEDBACK.md; its still-open items are the
last group under Later. An item states the symptom and why it is worth doing —
if it needs the history it belongs in a spec or INVESTIGATIONS.

**RECURSIVE-RAJ: start here.** A Raj agent improving raj reads
RECURSIVE-RAJ.md first (identity, rebuild boundary, editing discipline, swarm
workflow), then works the Now list.

## Now

- **Phone profile (`raj --phone`).** Touch-sized scrollable tabs, no persistent status bar (a transient overlay instead), a review bar bound to the existing accept/reject/clear/list actions, and ctrl aliases for super chords; spec in docs/MOBILE-REVIEW-SPEC.md. *Review and navigation are usable from a phone without chords or gestures.*
- **Save-review lag.** cmd+s with a review popup leaves a visible beat between
  the tint clearing and the dirty dot going away on ~50 KB docs; the
  `internal/timing` instrument is in tree, so diagnose (write path vs
  re-tokenise vs the `PendingMarks`/`Groups` walk in `Draw`) and fix. *The core
  save gesture stops feeling slow.*
- **A save silently drops invalid/superseded runs.** The LSP campaign left
  declarations that existed only in invalid runs; a plain save would have
  written a file that does not compile. Decide whether the save refuses or the
  runs are disposed first. *A save never silently discards another writer's
  text.*
- **A shape-only external edit is silently reverted on restore.** `Base.Hash`
  hashes decoded text, so a CRLF-to-LF, BOM or charset change passes the guard
  and the log re-encodes the old shape; the smallest fix is a byte digest on
  `Base`. *External byte-level changes survive restore.*
- **A workspace-wide `lsp diagnostics` sweep before the host gate.** Per-file
  checks read `ok` while cross-file references are broken (7 files in the LSP
  review); make an `--all` sweep over the changed files the standard pre-gate
  step. Even a clean sweep is not a package-wide signal: gopls will not report
  a test-only compile error in a file the sweep did not name, nor a `go vet`
  failure, so the host `go test`/`vet` remains the whole-package check.
  *Waves stop exporting breakage to the host's `make check`.*
- **The hover panel is the last D2b seam.** `render.go`'s `sessionTopFor` maps a
  session-line hover anchor to a display row only because `hover()` captures
  `File.LineCol`; move the anchor capture onto `DispPos`/`RowText` as
  `showCompletion` did, then the helper is a plain `Viewport.Top` and goes.
  Every other position consumer (`render.go`, `app.go`, `review.go`,
  `control.go`, `inlay.go`, `session.go`) already projects through `DispPos`/
  `DocAt`/`DispOfDocLine` and clamps on `DisplayLines()` (D2b landed). *The
  projection is correct wherever a fold sits.*
- **Invalid-set handling: the Phase 1c gaps and the reconciliation failure
  modes.** `Project` does not consult `Invalid`; `review`/`proposals` cannot
  name an invalid set; the fold/annotation is not drawn; `buffers`' `pending`
  excludes it; `clear` cannot dispose of it; and a save drops it (above). *An
  invalid proposal is nameable, countable and disposable.*

## Later

One line per item, grouped by theme; nothing here is scheduled. Numbers live in
BENCHMARKS.md and decisions in INVESTIGATIONS.md.

### Layered proposals

- One jump path: move `host.Goto` onto `jumpToSessionLine`, or state why the
  column-addressed jump stays separate.
- **The deletion-only classification is written twice.** `deferredDeletions`
  and the head of `deletionLeases` (`internal/piecetable/groups.go`) each walk
  the journal for the live Proposed sets that have members and no insertions;
  one ordered helper would keep the two from drifting. `foldProjectOracle`
  (`project_test.go`) calling `deferredDeletions` also gives the fuzz oracle the
  same predicate production uses, so it cannot catch a wrong classification.
- Change gutter vs `HEAD`, annotated `read`/`exec`/LSP composition, git verbs
  (read-only diff first).
- Groups carry rebased ranges; the change-gutter and diff-style rendering
  consumers do not use them yet.
- `raj ctl diff -vs HEAD` (git, stretch): needs the range-rebase walk and a
  read-only `git show HEAD:<path>`.
- Diff-style rendering: green additions, red deletions; the gutter carries who.
- A rejected group can be wedged by a later overlap; name what overlapped so the
  caller can re-propose.
- An app-level advisory-lease test with two identities (only the Rejected
  refusal is covered today; the wire path is unpinned).
- Durable log: Phase 1 (the SQLite session/positions/settings store,
  `internal/store`) landed 2026-09-17 and is now wired into the app (the
  session blob, per-file positions and resolved settings); still to do: fold
  the journal op log behind the record interface, compaction and checkpoints,
  and the attachment model (loaded vs announced, headless read).

### Editor and LSP
- **In-file find & replace is the next wave.** The find bar is live; the
  replace half, its chords and its undo story are the intended next step.
  *The next wave has its item on the list.*
- Symbols are found by leading keyword, not parsed; per-language scanning is the
  next step (tree-sitter is the direction).
- The reserved-chord tables are short; extend them whenever another collision is
  found the hard way.
- **The `super+comma` keys in the macOS reserved/terminal test maps never
  match the bound chord `super+,`, so the settings chord slips both guards.**
  `internal/keys/table.go` binds `super+,` (the cmd+comma Preferences chord,
  reclaimed with a `kkp_on` line) while `internal/keys/macos_test.go` keys
  `reserved` and `terminal` on `super+comma`; `TestNoMacOSSystemShortcuts` and
  `TestTerminalDefaultsAreAcknowledged` are vacuous for it. Decide whether
  cmd+comma is reclaimable — fix the key to `super+,` and drop it from the
  macOS-reserved set (its note already acknowledges the terminal) — or move the
  chord. *A reserved-chord guard that cannot see the chord is not a guard.*
- Autoscroll at the 150 ms idle tick is visibly stepped; a faster tick while a
  drag is held would smooth it.
- A press on a list does not drag it; rubber-band selection and drag-to-reorder
  are undecided.
- The problems pane filters by severity and open files only; "current package"
  needs a package notion the pane does not have.
- Blinking secondary carets: nice, and a long way down (needs a blink-rate
  tick).
- Mouse-hover tooltips for inlay hints; `cmd+.` apply-hint-edit.
- A stale chord in a test passes vacuously (`TestDefinitionWithoutAServerIsHarmless`
  presses the retired `alt+super+d`); decide whether the harness fails loudly
  and tests read `keys.Bindings`.
- `syntax.go`'s dead chroma cases and the colours their comments describe
  (upgrade chroma or narrow the cases to specific token types).
- The LSP campaign's deliberate follow-ons: `semanticTokens/range` and delta,
  refresh requests, `codeAction/resolve`, lazy `codeLens/resolve`,
  `documentLink` rendering, `signatureHelp` triggers, `onTypeFormatting`
  multi-cursor, dynamic registration beyond `workspace/symbol`, a format-on-save
  setting, `$/progress` and `$/trace`.
- File lifecycle remaining: directory rename and `run --prog` reachability for
  the new verbs.
- Small and split panes, and how they resize.
- No Bubbletea adapter yet (the `ui.Host` surface keeps growing).

- `Tabs.Paths` now has no production caller — every session write goes through
  `SessionState` (`internal/app/session.go`), which iterates `Tabs.All()` itself
  — so it duplicates the preview-skip rule and is kept alive only by its test.
  Retire it or make it the one copy of the rule.
- **Warm-on-save-as does not sync a pane the user has left.** `warmSaved`
  (`internal/app/lsp.go`) starts the server, but `for_` returns nil until the
  handshake finishes so the immediate `syncDoc` is skipped, and the idle-tick
  sync only covers the active pane (`maybeRequestHints`, gated on inlay hints).
  A saved-as buffer that is clean and not active is never `didOpen`'d, so its
  diagnostics still wait for a hover or reopen. Remember the warmed path and
  sync it once on the next idle tick.

### Workspace and search

- **Workspace/search-pane replace is deferred** pending a definition of writing
  unopened files: a replace across results would edit buffers that have no tab,
  and the write target and undo story are unstated. *Deferred with a named
  blocker.*
- Save-as has no directory listing; the natural shape is the completion popup
  anchored under the field.
- Unnamed buffers have nowhere to persist (needs the dirty-buffer journal).
- The buffer overlay copies each dirty document on every search; scanning
  `Spans` in place needs a boundary-crossing matcher.
- Parallel walk: 51% of a search is syscalls; needs streaming results or
  deterministic truncation before a worker pool, and a multicore box to measure.
- Re-run the call census after the next wave against the success criteria in
  AGENT-FEEDBACK (read share from 32.5%, `search→read` from 2,761).
- `search --path` into a hidden directory ignores `--hidden`; decide and state it.
- Multi-target read: `--json` shape differs from the single read (no author, no
  per-file spans); decide whether it carries authorship.
- Multi-target read: a shared span that overruns one target refuses the whole
  call while a shared `--lines` clamps per file; decide clamp or refuse.
- `Buffer.Bytes` means document length in a `buffers` reply and contributed
  bytes in a multi-read; document it.
- `read --json` now echoes a line span as `line_start`/`line_end`, so a driver
  re-derives the line range it asked for without a second call; a byte-span
  read (`--start`/`--end`) still echoes no `start`/`end`. Echo the byte span
  too, so every span read is self-describing.
- **`read --json` gives no per-line byte index, so a line-addressed `apply`
  needs a second `search --json` (2026-09-22).** A whole-file read returns
  `text` with `bytes`/`lines` but no byte offset for each line, so a driver
  editing at a line it just read must search for the line to get a `byte_start`
  (or do the shift arithmetic the skill forbids). Echo a line-start offset
  array (or one byte offset per line) with the text, so a read is an offset
  source on its own. *A read should be enough to edit where it read.*
- **The per-target `read --at` and multi-`-q` `search` transports are N client
  calls inside one CLI call (2026-09-22).** `read --at` groups targets by
  identical span but still issues one `c.Do` per distinct span, and
  `search -q A -q B` issues one `DoStream` per pattern, so a batched tool call
  is still N round trips. Convert both to a single `run --prog` frame;
  `knownOps`/`verbNames` already carry `OpRead`/`OpSearch` with `OpSpan`/
  `OpQuery`. *One tool call should be one round trip.*
- `run --prog` payload paths are unmapped (`Client.toEditor` maps the field verbs
  only).
- `braceTally` counts markdown fences as code; a per-language "is this source"
  answer is the real fix.

### Saving, buffer and journal

- Owner and group are not preserved (needs a root-capable machine: `tmp.Chown`,
  `EPERM` ignored, a root-gated test).
- `IsBinary` has no production caller; delete it and its test, or document it as
  the shared predicate.
- A 16 ms coalescing window for streaming agent hunks.
- The idle-tick `Compact` scan cost is benchmarked but the numbers are pending
  the host run; then decide a fragmentation trigger or a cheaper `pieceStable`.
- Cursor offsets are not remapped across reverted bytes (a cursor past EOF
  survives `revert`).
- A dangling symlink defeats the resolved-root check (deferred 2026-09-16):
  decide whether to detect `ModeSymlink` and refuse, or keep the lexical
  fallback.
- **The `.git` transient skip is over-broad (2026-09-18).** `isGitPath`
  (`internal/app/session.go`) skips any path with a `.git` component from the
  session and from every journal site, so a deliberately opened `.git/config`
  is never restored. The trade is deliberate (git writes COMMIT_EDITMSG/
  MERGE_MSG there and a reappearing message is noise); decide whether to narrow
  the predicate to the git-written transient names or keep the broad skip.
  *A file the person opened is not the same as a commit message.*

### Control socket and agent surface

- Three nested header strings are still JSON (`DiffJSON`, `LSPJSON`,
  `StatesJSON`).
- The request header can go once recv, hello and cancel have opcodes, or once
  they are decided to stay JSON forever.
- Every agent shares one tint; a user watching two agents cannot tell them
  apart.
- Journal persistence of `claim` remains; the client `watch` push landed (see COMPLETED).
- Nothing reads the `exec` stale-run counter yet.
- A cancelled `exec` can orphan children; a process group and group kill,
  platform-specific.
- No flush mechanics for v2 (temp-file-plus-rename per dirty file).
- Nothing is encrypted; the token authenticates but frames are plaintext.
- Path inference is a single question; a real answer resolves paths relative to
  the editor's root.
- A workspace-visibility flag (`--workspace`/`--allow`) to scope what agents can
  reach.
- Dump snapshots are keyed by author id, not identity; `dump` to `patch` fails
  across a reconnect.
- Only `revert` compares the author to the connection; `patch`,
  `delete --withdraw` and `rmdir --withdraw` trust the field.
- Nothing in the editor calls `App.Tell` except saves; the user-facing prompt,
  its chord and a multi-driver picker are missing.
- A full mailbox is reported to nobody (same missing caller).
- Still no notifications for buffer changes; a `subscribe` op wants a version
  cursor and a slow-reader recovery story.
- `apply` cannot create or reach an unopened file.
- A connection-scoped "editor restarted" marker (a generation counter on
  `whoami`).
- A saved wave has no enumerable diff for the review pass: add a
  `history`/`changes` verb, or make the brief's file list the explicit contract.

- The installed `raj-editor` skill
  (`~/.config/opencode/skills/raj-editor/SKILL.md`) still names
  `--control-exec`; only the repo copy was updated. The orchestrator edits
  opencode config directly.

### Client mode and attach

- **A snapshot cannot capture mid-transaction.** `SnapshotState` omits
  `Session.depth`, so a snapshot taken between `Begin` and `End` loses the open
  undo transaction and the restored session groups those edits differently.
  Decide whether to refuse a snapshot while a transaction is open or capture
  the depth. *A snapshot is exact or it says it is not.*
- **No test exercises `Compact` before a snapshot.** The compacted-origin path
  is captured (`Snapshot.Compacted`) and restored, but every snapshot test seeds
  a fresh session; a compaction-origin round trip is unpinned. *The subtle path
  is the one that breaks silently.*
- **A client's local undo resets on a re-sync.** Undo in an attached client is
  client-local; the forward is a debounced apply, and a watch install replaces
  the pane's journal, so cmd+z after a refresh cannot reverse the edit it just
  forwarded. Decide whether undo is mirrored across the wire or the pane marks
  the reset. *A gesture that silently stops working is worse than one not
  offered.*
- **A forwarded edit's conflict has no surface.** A local edit the daemon
  cannot place warns on the status line and re-fetches, discarding the local
  text with no diff and no retry. The editor deliberately has no conflict UI
  (`REVIEW-AGENT.md`), so decide the smallest honest surface: keep the refused
  text visible, or name the group that moved. *A refusal the user cannot
  inspect loses work.*
- **Multi-mutator coverage is thin.** The human-write path is pinned one test
  per branch (forward span/base, dirty-pane skip, claim overlap), but nothing
  drives a second attached human, or a human and a fast agent on one buffer,
  so the interaction the feature exists for is untested end to end. *The
  multi-writer case is the reason the feature exists.*
- **`Guard.Patch` still refuses a joined human.** The wave admitted a durable
  human to `Guard.Apply` but left `Patch` agent-only because a snapshot is an
  agent tool. Revisit only if a human path ever wants patch; no test drives a
  human patch today. *A refusal should be a decision, not an oversight.*
- **A TCP attach client does not read back its granted kind, so it forwards edits that can only be proposals.** `hello` downgrades a human request over anything but the unix socket to an agent (`internal/control/control.go`), but the client still forwards its local edits as the person; `host.Apply` admits the agent text as a proposal, so an edit the client believes it accepted lands pending for review. Read the granted kind back from the hello reply (the participant row for the client author) and keep the client read-only when it is not `KindHuman`, or label the forward as a proposal. *A client must not offer an edit the daemon will not land.*
- **The LSP servers are not re-rooted on attach.** `adoptVisibleRoots` rebuilds the explorer, search and picker over the daemon set, but `newServers` is only called from the constructor (`internal/app/app.go`), so a client whose daemon primary differs starts servers against a workspace it is not showing. Decide whether adoption re-resolves the servers or LSP stays launch-rooted by design. *A client should not offer a workspace it does not render.*

### UI, terminals and rough edges

- **The document-highlight backgrounds keep the syntax foreground, so a
  comment on a highlighted occurrence can vanish (2026-09-22).**
  `HighlightRead` (`ui.Ansi(236)`) and `HighlightWrite` (`ui.Ansi(54)`) are
  applied with `style.On(bg)`, which preserves the token foreground; the
  comment grey (`ui.Ansi(8)`) all but disappears on the read grey just as it
  did on the proposal green. The `ui.LegibleOn(bg)` pairing the tints now use
  would fix it, but a highlight that recolours the token is a different look
  from one that emphasises it, and the two highlight colours might change
  instead — decide. *A mark that hides the token it marks is not an emphasis.*
- **The legible tint foreground drops syntax colour on accepted agent text (2026-09-22, user).** The proposed/agent tints now force `ui.LegibleOn(bg)`; the review green reads well, but accepted agent text loses syntax colour and reads worse. Decide: keep it on both, gate the override to comment-class tokens, or choose a tint foreground that stays legible. *The proposal fix should not make accepted text uglier.*
- The smoke suite is Linux and macOS only, and only Linux is proven.
- Smoke scenarios wait on 400 ms of wall clock; a control-socket "queue
  drained?" answer would replace the sleeps.
- Tabs re-anchor at each wrap point (self-consistent, looks slightly off).
- Every OSC raj sends is swallowed under tmux; DCS passthrough belongs in one
  writer wrapper, not at each call site.
- Profile switching in iTerm2 is not clean; deferred, what is there works.
- `cmd+shift+r` to reopen closed tabs, only if Ghostty actually binds it (check
  `+list-keybinds` first).
- Name an owner for the hot files each wave (a per-wave ownership rule, not a
  tool).

### Tests and workflow
- **The control fake `ping`/`buffers` replies drift from the shipped `Dispatch` (2026-09-22).** `fakeEditor.run` hand-writes the reply literals, so when `Dispatch` learned `Roots` the fake did not and `TestRootsClearsOnSetLessReply` failed the host gate. Build the fake `ping`/`buffers` replies from one helper (or the same reply constructor) so a new header field cannot land in `Dispatch` alone. *A fixture hand-copied from the wire drifts from it.*
- **`TestStopAllStopsEveryRoot` does not assert its two servers are distinct.** A fixture that reused one server for both roots would still pass (both `live` lookups return the same pointer, `stopAll` stops it, both `Start` calls return `ErrClosed`), so the test pins "the servers we saw are stopped", not "every root was stopped". Assert `live[0] != live[1]` and `len(h.servers.byID) == 2` before stopping. *A regression test that a reused server passes is half a test.*

- **Pin the first Draw's erase.** `TestResizeInvalidates` now drives a real size
  change through `FakeHost.SetSize`, and `TestLayoutChangeRepaints` the
  same-size toggle, but nothing asserts the very first frame invalidates; the
  size-aware guard makes that frame clear, and a test should pin it.
- Test the active-target naming path (`targetName`/`activePath`) with an
  `active` fake buffer.
- The invalidation mapping (`host.Groups`/`host.Diff`) has no app-layer test.
- A coordinate-convention guard so a 1-based/0-based assertion fails where it is
  written.
- A format-on-save ordering regression test through the wake path.
- `TestEveryVerbHasACode`'s verb list omits nine verbs, so its guard is only
  true if the list is also edited.
- `TestGuardRefusesSymlinkEscape`'s text/open/apply assertions are vacuous;
  make them reach the check or drop them.
- **The settings pane's click hit test is read-verified only.**
  `settingsPane.ClickAt`'s `dy-1` matches `Render`'s `y+1+i`, but no test
  clicks a row; add one for a row's activation (and that the heading does
  nothing), plus the `w < 8 || h < 2` and height clamp.
- The `.git` restore skip (`restoreLog`) has no test that an already-existing
  log for a `.git` path is ignored rather than replayed; seed a log with
  unsaved ops so the assertion fails without the `isGitPath` guard.
- `markInvalid` adds an O(ops²) journal pass to `Groups()`; measure before
  optimising.
- A superseded warning names the first intersecting run, not the bounding span
  (escalated, not decided).
- A heartbeat tick can enqueue a contentless frame after the final (benign,
  stale-id frames are ignored); decide whether to accept it or sequence the
  final against the tick.
- The container `raj` can lag the editor; rework the release/rebuild step or
  make an empty `srcVersion` detectable.
- The standing between-wave reconciler needs a thin wrapper so the pass is
  invoked rather than remembered.
- A saved buffer can still carry proposed sets after a rebuild and restart;
  needs a reproduction.
- A buffer that is entirely another author's pending proposal was uneditable;
  the overlap/reconciliation case.
- New `raj ctl` against an old server warns falsely on `--include` (the shipped
  skew warning does not suppress the zero-`Considered` message).
- Subagent transcripts feed the efficiency loop (mine task outputs for wasted
  tool calls and brief-quality patterns).

### Agent feedback — actionable (context in AGENT-FEEDBACK.md)

- **`buffers` never populates `Superseded`, so the field and the client mark's
  `superseded` component are inert (2026-09-20).** `host.Buffers()` sets
  `Pending` and `Moved` but not `Superseded`, so the sparse `hBufferSuperseded`
  (0x5d) field is never emitted and `bufferMark.superseded`/`closedMark.Superseded`
  are always zero; the comparison they feed cannot move. Populate it from the
  invalid sets a save would drop (`Session.UnsavedProposed`) or drop the field
  and the mark component. *A wire fact with no producer is a comparison that
  cannot fail.*
- **`TestClientViewReadsLegacyClosedPaths` is vacuous (2026-09-20).** A failed
  legacy parse and a legacy mark both re-add the same daemon tab, so the test
  passes without the `closedMarks.UnmarshalJSON` legacy branch it names; seed a
  case where the two diverge. *A test that passes for the bug it guards is not a
  test.*

- Name the search hit's offsets so a row cannot be mistaken for a byte range:
  `line` is a line number while `line_start`/`line_end` are byte offsets; the
  `SearchMatch` doc comment also says `ByteStart..ByteEnd` bound the match
  "within" the line while the fields are file offsets and `text` is the whole
  line, so the comment contradicts its own fields.
- State or fix the scope split between `groups` (one buffer) and `proposals`
  (whole workspace), so `groups --mine` with no focused buffer does not read as
  "no sets".
- **`NativeHost` never closes its `events` channel (2026-09-21).** The `Host`
  interface says the channel closes when the host shuts down, and `FakeHost`/
  `HeadlessHost` close it, but `NativeHost.Close` does not, so a `range` over
  `Events()` would hang and the contract comment is false. Close it, or correct
  the comment and say why the terminal host differs. *A channel contract only
  two of three hosts honour is not a contract.*
- **`daemon.log` grows without bound (2026-09-21).** `Runner.logWriter`
  (`internal/daemon/daemon.go`) opens the workspace log append-only and never
  closes or rotates it; `logTail` scoping fixed the wrong-run output, not the
  growth. Cap or rotate it, or state why an append-only log is acceptable.
- **`edit --old ... --all` is an unbounded substring replace (2026-09-21).**
  It replaces every occurrence, including one inside a larger identifier — a
  rename of `.root` to `.primaryRoot()` would also rewrite
  `snapshotSearcher.root` and `servers.root` — so it can corrupt a different
  identifier silently. Warn or refuse when an occurrence is flanked by
  identifier bytes, or make `--word` the default with `--all`. *An identifier
  rename is not a substring replacement.*
- **The skill says `search` is case-sensitive by default; the editor is not
  (2026-09-21).** `raj ctl search -q PRIMARYROOT` matches `primaryRoot` with no
  flag and `--case` is what makes it case-sensitive, so the skill text
  "literal and case-sensitive unless `--regex` or `--case`" is backwards.
  Correct the skill (or the default) so a case-sensitive search is what a
  driver gets.
- **DECIDED (user, 2026-09-17): keep the linewise paste text-suffix
  rule.** `PasteClip` keeps keying on `strings.HasSuffix(Text, "\n")`, so a
  characterwise selection ending exactly after a newline still pastes below
  the line; the `Linewise` flag on `Clip` alternative is not taken.
- **Find's smart-case fold can shift byte offsets (medium).** `hasUpper`
  only sees ASCII `A-Z`, so any all-lowercase query takes the `strings.ToLower`
  branch; Go's simple fold is not byte-length-preserving for runes such as
  `İ` (U+0130, 2 bytes → `i`, 1) or `ẞ` (U+1E9E, 3 → `ß`, 2). The haystack is
  folded too, so a document containing such a rune before a match shifts every
  later `matches` offset, and `replaceCurrent`/`replaceAll` now edit at those
  offsets. Fold ASCII-only or track each match's byte length.
- **Review mode over-refuses cmd+enter in the find bar.** `WouldEdit` returns
  true for `keys.LineBelow` even when the replace row is hidden and
  `Find.Handle` would ignore it; the gate should be `return f.replaceShown`,
  so the read-only note matches what would happen.
- **`raj ctl` run from outside the workspace root mistranslates paths.**
  `inferMapper` roots the local side at `WorkspaceRoot(cwd)`, so a driver run
  from `/tmp` and handed `/work/...` is refused (wave 2, 2026-09-17);
  `RAJ_ROOT_MAP` is the workaround. Make root inference cwd-independent or
  state the run-from-root requirement.
- `OpReload` is in `prog.names` but neither `knownOps` nor `verbNames`;
  reconcile the three opcode tables.
- `find` has no `verbCodes` wire code; add it and add the verb to
  `TestEveryVerbHasACode`.
- `exec` in a program cannot set a working directory (no `OpDir`).
- The hover-anchor cross-cutting test is blocked on a fake-LSP-server seam.
- Two encoding-classifier edge cases: `ff fe 00 00` checked as UTF-32LE before
  the UTF-16LE BOM, and unmarked UTF-16 with no NUL decoding as Latin-1;
  detect-and-refuse or document.
- Line/col in `apply`/`edit` replies needs a wire and coordinate decision; the
  reply names the new version but not the new change-set id, so a driver must
  call `groups`/`proposals` to reject, clear or `goto` the set it just applied.
- Make the write target explicit (a mandatory path or `--active`/`--here`).
- Consolidate `read`/`version`/`dump` and collapse the write verbs onto one
  door.
- Raw-LSP passthrough: decide whether it earns its surface, then file it or drop
  it.
- Define or drop the Stream A/B/C/D labels; the user's definitions are needed.
- No `ping`/health-check CLI verb (the protocol op exists).
- `help`/`-h` are reachable but absent from the usage list.
- `whoami --as X` printed a fresh anon id once; confirm before trusting `whoami`
  as the bind check.
- **`--as` must follow the verb.** `raj ctl --as KEY search` fails with `unknown
  command "--as"` and prints the whole usage, so a driver that puts the identity
  flag first is stuck; accept it before the verb too, or state the placement in
  the usage text.

- **`search`'s regex-metachar hint misleads on a literal miss.** The hint
  (`internal/control/cli.go` `hasRegexMeta`) fires whenever a zero-match
  literal pattern contains regex syntax and says "retry with --regex"; when the
  literal really is absent, `--regex` is the wrong fix. It also exits nonzero as
  if the pattern were refused, so a caller cannot tell a literal absence from a
  rejected query (`search -q 'zzq[unlikely'` prints the hint and exits 1).
  Reword to offer both readings (pass `--regex` if you meant a regex; otherwise
  the literal matched nothing) and keep the nonzero exit for "no matches" only.
- **`edit --old` that matches nothing gives no nearest-line hint (2026-09-22).**
  The refusal says the text does not appear and to copy it exactly; it does not
  say where the closest match is, so a block off by one line costs a re-read to
  locate the seam. Name the nearest line, or the longest matching prefix, in the
  refusal. *A failed match should show where the text diverges.*
- **`search --include` and `--path` are two doors with different rules
  (2026-09-22).** `--include` is a comma-separated glob list (a bare `cli.go`
  matches by basename, two files) while `--path` is documented as a directory
  but also accepts a file and searches just it. The usage hint says to use
  `--include` or `--path` without saying which is for one file. Document the
  split, or make `--path` accept a file explicitly and say so.
- **A multi-path `lsp diagnostics` entry for a statusless answer carries no
  status (2026-09-22).** An old server's `{}` parses, so the entry is emitted
  with `path` only (`status`/`detail` are `omitempty`) and exit 1, where the
  single-path form refuses with a rebuild-to-match-ctl version-skew message.
  Give a parsed-but-statusless entry the same `lspStatusError` status and
  detail, so every entry in the batch answers with a status.

### Flag usage printing

- **The `--daemon` alias is not hidden.** The init in `cmd/raj/main.go` guards
  `flag.FlagSet.MarkHidden` behind an interface assertion, but released Go has
  no `MarkHidden` and no `Flag.Hidden`/`hidden` field, so the assertion never
  fires and `control.flagHidden` can never be true. Live `raj --help` prints
  `--daemon daemon run`, the backquoted `` `daemon run` `` taken as the value
  placeholder. Decide: hide it by name in `editorUsage`, or accept it visible
  and delete the dead guard and the false comment. Escalated 2026-09-20.
- **The editor `--help` lost its header.** `editorUsage` calls only
  `control.PrintFlagUsage`, so `raj --help` opens on the flag list with no
  `usage: raj [options] [file|dir]` line where the default printed
  `Usage of <path>:`. Decide whether to add one. Escalated 2026-09-20 (UX).
- **A custom `flag.Value` prints the placeholder `value` in `-h` (2026-09-22).**
  `-q` and `--at` became `flag.Value`s, and `flag.UnquoteUsage` names an
  unknown Value type `value`, so `raj ctl read -h` says `--at value` and
  `search -h` says `-q value` where the hand-written usage says `PATH=LO,HI`
  and `PATTERN`. (A one-letter name already prints with one dash, so the old
  `--q` note is retired.) Put a backquoted placeholder in the usage string,
  or special-case `PrintFlagUsage`, and assert it.
- **Backquoted words in a usage string leak into the placeholder.**
  `flag.UnquoteUsage` treats a backquoted word as the value name, so
  `raj ctl apply -h` prints `--group groups` and `--dump dump` where the value
  is a uint; the same mechanism renders `--daemon daemon run` in the editor
  help. Drop the backquotes or accept the rendering.
- **`PrintFlagUsage` has no panic guard on a default text.** `flag.isZeroValue`
  wraps the zero `String()` call in `recover`; `flagDefaultText` does not, so a
  custom `flag.Value` that panics on a zero receiver would panic the help
  path. Latent: no custom `Value` is registered today. Mirror the stdlib
  recover, or state why not.

### Claim surface

- **`claim` with a bare path silently replaces the working set.** `claim` with
  operands replaces the set unless `--add` is given (documented), so an agent
  that claims a later path loses the earlier ones and its writes are refused
  until it re-claims; both a subagent and this review pass paid a retry for it.
  Warn when a replace would drop a non-empty set, or make `--add` the default
  with an explicit `--replace`. Escalated 2026-09-20.
- **CLAIM-SPEC still describes the missing-path warn/skip the code deliberately dropped.** `docs/CLAIM-SPEC.md` §3 says a path that is neither on disk nor an open buffer is "warned per-path and skipped", and §10 leaves the `open --create` missing-parent case open, but `Guard.Claim` now keeps a not-on-disk path as a forward claim (it warns only on a non-`IsNotExist` stat error) and `saveNamed`/`ensureParent` answers the parent at save time. Reconcile the spec with the forward-claim choice, or restore the warn. *A spec that contradicts the code is a decision lost.*

### Daemon list and labels

- **`daemon list --json` emits `null` when no daemon runs.** `json.MarshalIndent`
  of a nil `[]Entry` is `null`, so a script must special-case the empty list;
  marshal `[]Entry{}` (or render `[]`) instead. *An empty list is `[]`, not
  `null`.*
- **Decided (2026-09-22): `--workspace` and an explicit `--control-addr` are
  mutually exclusive.** The label already names the daemon and its address, so
  a typed address is a second, contradictory source; the Unix socket is
  preferred over TCP because it needs no token. The attach itself is done — see
  COMPLETED.md. *An attach names the workspace, not the port.*
- **`restartCLI`'s label carry-over has no test.** The carry-versus-override
  decision lives in the CLI function, which has no `Ops`/`Runner` seam and can
  only be driven by a real spawn, so `restart --workspace` and the label
  carried across a plain `restart` are unpinned; extract the decision into a
  pure helper (as `resolveTarget` is) and test it. *A carried label is a
  decision; pin it.*

## Direction (documented, not scheduled)

- **An explicit tab width now overrides a file's detected indentation
  (escalated, 2026-09-17).** Previously `--tab` was only a fallback; an explicit
  width (flag or stored `tab_width`) now pins both the display advance and the
  indent unit and survives Reload re-detection, while a default launch still
  lets detection win. Confirm this is the wanted semantics, or revert the pin
  to a fallback. Do not decide from the review.
- **The settings key set is open; the pane's default scope is settled
  (2026-09-18).** The settings pane (`internal/app/settings_pane.go`) is built
  and writes to the workspace scope by default, with a scope row to switch a
  change to the user scope; the resolver still knows exactly `tab_width`/
  `tabs`/`wrap`/`auto_pairs`/`inlay_hints` and leaves an unknown key alone
  (`SetSetting` refuses one), so a newer build can add a setting without an
  older one misreading it. The remaining question is the key set, and the LSP
  section the pane sketch reserves a place for.
- **LSP server choice should be configurable.** The language-to-process table
  (`internal/app/lsp.go` `command`) is hardcoded, so a `ty`/`pyright` user gets
  `pylsp` and an unlisted server is unreachable; this also answers "only servers
  that run with no configuration are listed". Lightest shape: per-language
  candidate argv lists, then a per-workspace config, then a settings pane.
- **Tree-sitter is the decided direction for the syntactic layer.** Auto-indent,
  symbol navigation and string/comment classification move there; semantic
  queries stay on LSP. Grammar management is its own milestone before the
  feature.
- **Inlay type hints are deliberately out of scope.** Drawing text that is not
  in the document perturbs column maths, caret positioning and wrapping — a
  renderer project, not an LSP one.
- **Conflict navigation over git diffs** (not scheduled): navigation in terms of
  git diffs, with resolution staying native — no work trees or branch hackery.
- **`edit --base` and miss-point reporting** — under discussion with the user;
  not scheduled.
- **East Asian Ambiguous arrow width is terminal-dependent (escalated,
  2026-09-18).** `RuneWidth` now counts `←`/`→`/`↔` two cells, confirmed only
  on the terminal that drifted (Ghostty). A terminal that draws Ambiguous arrows
  one cell wide would regress the same caret by one column per arrow in the
  other direction. Confirm the intended terminals, or gate the exception on a
  terminal capability.
- **Should a Rejected deletion-only set also be leased? (escalated,
  2026-09-18).** `deletionLeases` leases only Proposed gaps, so a Rejected
  deletion's restored bytes have no caret-side lease even though `Leased`
  already refuses edits inside Rejected insertions. The wave left `Rejected`
  behavior unchanged deliberately; decide whether a rejection deserves the same
  gap lease.

## Deliberately not doing

- Horizontal scrolling while wrapped: there is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
- The container build/run stays the external `bldraj`/`oc` shell functions, not
  folded into the binary as `raj box build`/`raj box run`.
- `exec` over TCP is refused by design — the command would run on the editor's
  machine, outside the driver's container.
