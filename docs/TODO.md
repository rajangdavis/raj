# TODO

Open work only, collapsed 2026-09-17. Measured numbers live in BENCHMARKS.md;
root causes, terminal findings and decisions live in INVESTIGATIONS.md. Raw,
dated agent feedback lives in docs/dev/AGENT-FEEDBACK.md (local, git-ignored); its still-open items are the
last group under Later. An item states the symptom and why it is worth doing —
if it needs the history it belongs in a spec or INVESTIGATIONS.

## Now

Nothing active. The phone profile is largely in place and the save-review lag no longer reproduces (2026-09-27); the open work is in Later, below.

## Later

One line per item, grouped by theme; nothing here is scheduled. Numbers live in
BENCHMARKS.md and decisions in INVESTIGATIONS.md.

### Hooks — see a run while it runs (2026-09-30, user)

- **A run is readable but not pushable.** `raj hook log` (the last 100 runs,
  with exit, duration and `recovered`), `raj hook ps` (in flight) and
  `raj hook log --show <id> --tail N` (a live tail, even for a detached run)
  are all reads that cross TCP, so an agent in a container can already watch a
  cycle. What is missing is ergonomics: no `--follow` on `--show`; no
  `hook-finished` event on the bus, so a waker must poll; no opencode tool for
  `ps`/`log` (the agent shells out); and nothing surfaces runs in flight in
  `raj ctl status`, the status line or the debug overlay. Add those four, and
  settle the durable run log (H3: memory plus journal, or SQL) so a history
  survives a restart rather than only a `recovered` entry.

### Editor — record the drawn screen over time (2026-09-30, user)

- **`screen` reads one frame and nothing records a run of them.** The verb is
  `screen [--until TEXT]` — the drawn screen as text, `--json` adds the cursor
  — and that is the whole surface: no `--save`, no frame log, no timestamps.
  Nor is there a keystroke-injection verb, so an agent can read the screen
  between buffer-level actions but cannot script an interaction. The gap
  matters because the flicker fixed 2026-09-30 was invisible to every
  instrument: no test failed, no diagnostic fired, and it was found only
  because the user watched the debug overlay field flip. Add `screen --follow`
  (or `--record`): emit a frame whenever the drawn screen changes, timestamped,
  so a recording is compact and a temporal or visual bug is answerable.
  `--until` is already the predicate wait; this is the timeline half. Keep it a
  read: a key-injection verb would let an agent drive the UI as the user, which
  crosses the propose/decide line (`accept` and `save` are human-only) and
  would have to be local-human-only if it is ever added.

### Owner gestures (Track G) — review fallout (2026-09-30)

- **Delete the dead `proposeDeleteFile`/`proposeRemoveFolder`.** G1 left them
  in `internal/app/menu.go` when a deletion-only change could not be verified
  through the hook's projected tree; the projection is now pinned by
  `TestProjectAppliesProposedDeletion`, so they are removable — along with the
  `piecetable` import that is their last use — once that test is green on the
  host.
- **The generated keybinding reference omits `Native` chords.** `keys.Doc()`
  renders only `Bindings` + `Reclaim`, so the new waiting-list chord
  `ctrl+alt+v` (`keys.ReviewProposed`, a Native) has no `docs/KEYBINDINGS.md`
  row. Decide whether `Doc()` should render `Natives`.
- **The waiting list's enter opens a non-text row's path.** A publish row
  carries the wave name, so `openFromPicker` tries to open a file named after
  the wave; a delete or rmdir row has a path but no line to jump to. Make
  enter a no-op for a row with no text, or gate it on the row kind.
- **Overlap reporting compares bounding spans.** A large multi-hunk set flags
  every set inside its span, so this wave produced three "conflicts" that were
  disjoint hunks (`conflict_test.go` 3/2, `host.go` 2/5, `table.go` 5/3).
  Compare hunks, or say "encloses" rather than "overlaps".
- **An invalid deletion-only set is invisible.** `Pending()` drops it, so
  neither the waiting list nor `raj ctl proposals` can show it, and its
  projection is unpinned (`AGENT-FEEDBACK`, invalid-set wave). Pin the
  projection and give it a review row.
- **Author ids are reused while their pending sets are live.** A fresh
  `register` was assigned author 130, writer G1's id, so `--mine` adopted G1's
  pending sets and a `revert --author 130` from that connection would have
  discarded them. Do not reuse an id any pending journal op names.

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
- **A zero-width insertion inside an excluded run is not covered by the
  overlap projection rule.** `projOracle.overlap` detects only an included
  edit's *replaced* span entering an excluded inserted run, so an included
  insertion placed strictly between the excluded run's bytes neither sets the
  fuzz witness nor takes `unapplyRemoveIns`'s partially-consumed branch; the
  run is removed whole and the included text can fuse with what follows. Decide
  whether the boundary re-anchor is intended (pin it) or fold the case into the
  structural-separator rule (detect an included piece interleaved between two
  owned pieces). See `docs/dev/AGENT-FEEDBACK.md` (2026-09-26).
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

- **`servers.stopAll` can block on a handshake still in progress (2026-09-25).**
  `internal/app/lsp.go` `stopAll` calls `srv.Stop()` on every server in `byID`,
  including one whose `starting` flag is set and whose `start` context has a
  60 s timeout, so a daemon shutting down mid-handshake can wait out that
  context. Give `Stop` a bounded grace or cancel the start. *A shutdown that
  waits on a 60 s context is not a shutdown.*

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

- **A save with pending agent proposals is silent, and it approves them (2026-09-28).** The save gesture *is* the approval, so saving while agents are mid-work accepts their partial proposals with no signal — and the user suspects they have been doing exactly that. Warn before the save when the buffer (or workspace) holds pending sets: name the authors and count, and offer save-anyway / review-first. A non-blocking confirm, never a refusal — a check that can block its own fix is the earlier save-guard deadlock. The surface (in-editor confirm vs a status warning vs a before-gate advisory) is a UX call. *Approval without a look is not a review.*
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

- **After a daemon restart a named identity can send under a throwaway token key (2026-09-28).** The cycle announced as key `raj-cycle` (author 130) before the restart and closed as the SAME author 130 but under a `tok_...` key after it: the author row survived the restart (L3.3) but the named key did not rebind for the post-restart send, so a reply to the closing report targets a discarded key. Rebind the key on reconnect, or re-hello the `--as` key after a dial failure, or make the client refuse to send under an adopted token when an explicit `--as` was given. *A restart must not change who a name points at.*
- **Hooks take no arguments; add declared, validated parameters (2026-09-28).** `run` is name-only by design — the agent never supplies argv, env or cwd — so a hook like `cycle` cannot be pointed at one segment, and retrying the announce step means re-running the whole check/build/restart. Extend the contract with DECLARED parameters: the author declares `--param NAME=enum(a,b,c)|string(regex)|uint`, the agent supplies `name NAME=value`, the server validates the value against the declaration and passes it as an environment variable (`RAJ_PARAM_<NAME>`), never as argv. The safety property holds — the agent selects from a declared domain, it does not inject a command — and one `cycle` hook then covers every segment (and split/stack publish, deploy, and the rest). *An agent should name a choice, not a command.* **Done 2026-09-28:** declared, validated parameters landed — a `params` column (store v9->v10), repeatable `raj hook add --param`, `raj hook run <name> NAME=value`, a default making a declared parameter optional so an argument-less caller still runs the hook, and `RAJ_PARAM_<NAME>` delivery to argv, shell, builtin and `steps` actions; `docs/HOOKS-SPEC.md` §1, §2, §3, §4 and §7 updated.
- **The registry leaks a durable row per anonymous handshake (2026-09-27).** `who` shows 122 participants after a day of agent traffic, most `tok_...` rows: a `raj ctl` invocation that arrives with no identity appears to leave a durable participant row rather than the reserved, row-less id the skill documents, so it grows without bound and inflates `who`/`Drivers()`. Confirm and reap, or make the adopted-token path row-less. *An anonymous handshake is not a participant.*
- **Mail is confirmed at the next Park, not after the prompt (2026-09-28).** The store marks a batch delivered when the identity parks again, so a reader that exits 0 and dies before prompting the session loses that batch while the daemon lives, and a daemon restart between hand-off and confirm replays it for a duplicate. There is no client ack for the hand-off. Carry the delivered row ids in the recv response and add a `confirm` op the client sends after the prompt succeeds, or let a re-park on the same connection re-request an unconfirmed batch. *A hand-off is not a delivery.*
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
- **A second `SetHookDir` can double-adopt a detached run (2026-09-26).**
  Recovery is not idempotent: calling it twice on the same directory adopts each
  live `<id>.pid` again and starts a second watcher, so a completion can be
  logged twice and two cancellers race. Guard adoption against an id already in
  the registry, or make `SetHookDir` one-shot. *A recover that runs twice is a
  duplicate, not a no-op.*
- **A malformed `<id>.pid` is ignored but never reaped (2026-09-26).** A legacy
  or corrupt pid record is neither adopted nor logged lost, so its files linger
  until a prune that may never come (a live run below the keep window is never
  pruned). Treat an unreadable pid record as lost, or remove it during recovery.
  *Ignoring a file is not cleaning it up.*

- **The mailbox bound drops mail silently (2026-09-27).** `MailboxDepth` (16)
  drops the oldest undelivered messages when a reader is dark and mail piles
  past it, and only the daemon logs it ("dropped N oldest undelivered
  message(s)"); the recipient is told nothing. One save is one message, so a
  short dark window overflows the box - and the plugin's 60s backoffs made
  exactly that window. Decide: coalesce repetitive notices (N saves in one
  message), surface a drop notice to the recipient, or raise or remove the
  bound. *Silent loss is the second path behind the missing oc2 notices.*

- **The CLI never presented itself as human (2026-09-27).** Every `raj ctl`
  connection joined as an agent, so save / save --all / land / approve refused
  the user's own shell. Fixed client-side on unix (Kind:human in the hello;
  TCP stays agent-only and the server downgrades). Re-examine the help wording
  that advertises `save --all` and `land` as human gestures, and the
  TCP-requesting-human path.
- **One generic op-scoped JSON slot on the wire (2026-09-27).** Hook, Git, Diff
  and LSP each carry their own JSON field and response code; collapse them onto
  one op-scoped slot and free three to four response codes.
- **The 0x01-0x7f header space is full (2026-09-28).** `hHookParams = 0x1f`
  took the last free request code: request fields use 0x01-0x1f, response fields
  0x20-0x7f, and 0x00 is unrepresentable in the framing. (The original premise
  was wrong - a code at or above 0x80 does *not* read as a verb in a header;
  that is a program rule, and `decodeHeader` passes no known set, so an unknown
  header code at or above 0x80 is skipped.) A new header field needs a freed
  code (the op-scoped slot above frees three or four), a nested field carrying
  its own sub-codes, or a change to the range rule;
  `TestUnknownHeaderFieldIsSkipped` splices 0x80 because no free argument code
  remains. *A full code space is a design problem, not a numbering one.*
- **`hGenOut 0x5e` is a duplicate, free once nothing emits it (2026-09-29).**
  `encodeHeader` wrote `h.Gen` twice - as `hGen` 0x1a and as `hGenOut` 0x5e -
  and `decodeHeader` read both into the same field. The `hGenOut` write is
  dropped, so nothing emits it; decoding it stays, so a peer that still sends
  it keeps working. What frees the code is that drop: once no build that emits
  0x5e remains in the field, the response code can be reused.
- **`hFindCount 0x58` is written but never read (2026-09-29).** `host.go` fills
  it in find's answer and `wire.go` carries it, but no `raj ctl` client reads
  it (`cli.go` and `client.go` hold no reference), so the response code is
  reclaimable. What frees it is dropping the `FindCount` write in `host.go`
  (or giving `find` a reader such as `--count`, which would keep the code).
- **The builtin in-process environment is two parallel mechanisms (2026-09-28).**
  `builtin.WithStepEnv`/`StepEnv` and `builtin.WithParamEnv`/`ParamEnv` are the
  same context-value pattern twice, carrying the same `[]string` of `KEY=value`
  entries; a builtin leaf reads both, and `runChainStep` concatenates them for a
  shell step. One `WithEnv`/`Env` channel carrying a single combined slice would
  serve both, leave a shell step nothing to merge, and give a third source
  nowhere to duplicate. Leaves in `internal/control/hookbuiltin_test.go` read
  each getter.
- **`intent prove` runs the check hook's argv directly, bypassing parameter
  validation (2026-09-28).** `app.runCheckHook` parses the stored row and runs
  `h.Argv`, so a required parameter on `check` would be silently omitted where
  the server would refuse; it is safe today only because `check` is
  `["make","check"]` with no declarations. Resolve through the same path, or
  refuse a `check` hook that declares a required parameter, so prove and the
  gate cannot diverge. *One hook, two runners, one contract.*
- **Intention membership uses bare session-local group ids (2026-09-27).** A
  group id is unique only within a buffer/session; two buffers can both number
  a group 1, and `Intention.Members` is `[group ids]` alone, so the app resolves
  a member id to one path and a task spanning two buffers with colliding ids
  cannot be named correctly. Proposed shape: qualify membership, e.g.
  `{path, group id}` pairs (or a `path:group` key) resolved per buffer, so a
  multi-buffer task is unambiguous. VERDICT: this BLOCKS S3 for multi-buffer
  waves - S3's premise is "a wave's groups are one intention by task", and a
  wave touches many buffers - unless membership is qualified first or folded
  into S3's spec carve. It only degrades single-buffer intentions.
- **S3's carve must name the qualified member type (2026-09-28).** The
  qualified-membership item (`intent.Member` / `store.IntentionMember`, resolved
  against the buffer at its path) is a defect in H4's landed type, not part of
  S3; S3's carve must take a task's members from `groups --task`'s qualified
  listing and name the qualified member type.
- **A rejected set pins a buffer dirty that a save cannot clean (2026-09-28).**
  A buffer held one accepted set and two rejected sets; saves wrote the agreed
  composition, `raj ctl diff` was empty and disk matched, yet `status` stayed
  dirty across two saves, so the old publish-single.sh readiness gate refused
  forever (that gate is retired; the dirtiness is not). Rejected text stays in
  the view only, and `clear` (claim-gated: path positional, id via `--group`)
  is the only disposal. Now `status` names the count; `reject` still does not
  reverse text.
- **Zero-op sets accumulated (2026-09-28).** One buffer listed seven
  `0 ops, +0 bytes, 0 hunks` sets: clear left a tombstone when it reversed every
  member out, and an apply could commit a no-op replacement. Clear now drops the
  set from the `groups` listing and ApplyDiff skips a rebased no-op hunk.
- **Retroactive wave-to-MR discovery (2026-09-28).** The publish result is NOT
  stored; the mapping is recovered instead. The branch name is deterministic
  (`raj/wave-<wave>`), the pushed sha is `git rev-parse origin/raj/wave-<wave>`
  after a fetch (the remote-tracking ref IS the record), and the MR URL is a
  lookup: `gh pr list --head raj/wave-<wave>` (or the GitHub API
  `GET /repos/<owner>/<repo>/pulls?head=<owner>:raj/wave-<wave>`), with
  `.../pull/new/<branch>` to create one. Add `intent show <wave>
  --remote-status` (or a discovery subcommand) that derives the branch name,
  asks the remote whether it exists, and prints sha + URL. Record nothing till
  then.
- **The container ctl can predate the editor (2026-09-28).** The `raj ctl` in a
  harness container is a build from an older commit, so verbs exist on one side
  only (`--action`, `intent`) and `exec` is refused over TCP - there is no
  workaround from inside. Fix: a build/redeploy step that rebuilds the container
  image AND the editor from the same commit, and/or make the existing
  commit-skew warning actionable - fail loudly with "rebuild the container"
  instead of "unknown command".

- **`MaterialiseTree` has no direct test (2026-09-28).** It is the core of both
  export and prove yet is only compile-covered. Add a direct git unit test next
  pass - a temp repo and a projection, asserting the tree object and that the
  index and worktree are untouched. Not a probe.

### Client mode and attach

- **An attached client never adopts a daemon tab created after it connected (2026-09-25).** `StartClient` mirrors a new dirty daemon tab into `clientOwned`, but `syncClient` (the watch cycle) only re-fetches marks of already-owned paths, so a tab an agent opens or dirties after the client connected stays invisible until reconnect. Run the mirror in `syncClient` too, or push a membership frame. *A viewer that only learns its tabs at connect is stale by construction.*

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
- **An attached client runs its own language servers, and nothing is mirrored (2026-09-29).** `cmd/raj/main.go` skips only the eager warm-up in attach mode (`if !opts.Attach { a.WarmServers() }`); a server still starts lazily when the client's idle tick calls `servers.for_` (`internal/app/lsp.go`), which spawns from the client's own table, roots and PATH. None of the ~25 `a.attach` branches in `internal/app` gate LSP, so the client's diagnostics and inlay stores are filled by its own server and the client wire carries neither. Consequences: a client with no server on PATH draws no marks while the daemon has a full set, and a clean buffer is read from the client's disk (`syncDirtyPane` opens only dirty ones) - which on a remote or phone client is not the daemon's. Fix shape: the daemon owns LSP for attached clients - push its diagnostics and hints over the watch and stop calling `for_` when attached. That is also the only route to a comparable client-frame rebuild (client-mirror plan): today a frame whose client drew LSP marks answers `NOT COMPARABLE` with the reason `client-owned language server`, and moving ownership turns that into a real comparison as a side effect. No stamp compares across the two processes regardless - document versions are per sync connection and the server publishes without one, dated only by a process-local arrival sequence. Open: whether `adoptVisibleRoots` hands the client the daemon's absolute paths, so a same-host client's server reads the same disk. *A client should not compute what the daemon already knows.*

### UI, terminals and rough edges

- **Phone profile: audit the remainder.** The `--phone` profile is largely in
  place (`docs/MOBILE-REVIEW-SPEC.md`); reconcile the spec against the shipped
  profile and file what is actually left.
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
- **`TestServersArePerRootForRunningAndLive` flakes under load (2026-09-26).** `internal/app/lsp_test.go:1609` fails with `language server handshake did not complete` after ~9 s when the host is busy (seen once in a `raj hook run check` while other hook runs were active; the same revision passed `raj hook run test` right after). Now that agents run `check` themselves, a timing flake reads as their bug. Widen the handshake wait or drive it off a readiness signal instead of wall clock. *A gate an agent trusts must not flake.*
- **A save guard can refuse its own remediation, deadlocking the build (2026-09-25).** `SaveOver` runs `CheckSaveText` on the accepted composition before writing, so when the *guard itself* (or a file that references its new symbols) has a false positive, `raj ctl save` refuses every buffer, including `internal/editor/savecheck.go` and `internal/control/wire.go` whose stale disk copy the host then cannot compile: `bldraj` fails, so the corrected buffer cannot be saved because the old guard is still compiled into the running daemon. There is no verb that writes a buffer past the guard. A guard must never be able to block the build it is part of: gate it behind a restart-safe escape (a stored setting read before the guard, or a `save --no-check` for the user), and never default a heuristic guard on without an escape hatch that survives a stale binary. *A check that can refuse its own fix is a deadlock, not a safeguard.*
- **A refused save must not close the tab (2026-09-25).** The user pressed save on a file the guard refused (status `save refused: …`) and the tab closed. The refusal path in `finishWrite` calls `runThens(thens, false)` and returns, and `closeTabAt` closes without prompting when `a.attach || !p.File.ViewDirty()`, so a transient/incorrect clean flag on the refusal path can drop the tab. Pin with a test that drives the human save gesture on a refused composition and asserts the tab still exists, the status names the refusal, and a following close still prompts. *A refusal that discards the work it refused is worse than the refusal.*

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
  make an empty `srcVersion` detectable. Named again 2026-09-24: `raj ctl git`
  was unknown in-container after the `git` verb landed in source (the image has
  since been rebuilt). Named again 2026-09-28: `raj hook run --param`/RAJ_PARAM
  was absent in-container during the declared-parameters review, so the changed
  CLI could not be driven, and `raj ctl exec` is refused over TCP so the
  host-built binary is not a fallback.
- **`scripts/call-runs.mjs` cannot read the opencode V2 store (2026-09-28).**
  It queries the V1 `part`/`message` tables ("no such table: part"); the store
  is the V2 `session_message`/`session_v2` schema, so the review contract's
  per-run tool-use metrics cannot be produced by the documented command. Point
  it at `session_message.data` or pin the opencode version.
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
- **`TestHookRunDetachedReturnsAndLogs` is load-flaky (2026-09-27).** It
  asserts a detached hook run returns within 1s wall clock; on a loaded machine
  it measured 1.183s and failed (the user hit this). Assert the property - the
  caller was not blocked on the command - rather than a wall-clock budget.

### Agent feedback — actionable (context in docs/dev/AGENT-FEEDBACK.md)

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
- **`read --json`'s `line_end` is a line number, not a byte end (2026-09-23).**
  A whole-line span reports `line_end` as the last line while the returned
  `text` includes that line's trailing newline, so a driver that treats
  `line_end` as a byte offset lands before the newline; the two units are not
  named distinctly. State the unit, or echo a one-past byte end. (Reported by
  the 1.1/1.2 sessions; reproduced here: `lines` counts a trailing empty line,
  so `--lines <lines>,<lines>` reads `""`.)
- **`diff` accepts one path, so a multi-file review is N calls (2026-09-23).**
  Take several paths or `--all` over the pending sets, as `read A B C` and
  `lsp diagnostics --all` now do, so a wave's review is one round trip.
- **No deterministic seam drives `hover()`/`signatureHelp()` (2026-09-23).**
  Only the pointer tooltip is drivable, so the request-time anchor capture in
  `hover()` (the `DispPos(head)` line in `internal/app/lsp.go`) has no test that
  does not need a live language server; `park`/`applyAnswer` install an answer
  but never drive the request. Add a seam as the inlay and completion paths have.
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
- **Repeating `--include` or `--exclude` searches only the last glob
  (2026-09-25).** They are plain `flag.String` values
  (`internal/control/cli.go:233-234`), so `--include *.go --include *.md`
  searches only the last glob, with no warning and a plausible partial result,
  while the documented comma form `--include *.go,*.md` searches both. `-q`
  and `--at` are documented repeatable, so the asymmetry invites the mistake.
  Make the two flags accumulate (split on comma and append, as `-q` does), or
  state not-repeatable, last-wins in usage and the skill. *A verification tool
  must not under-report silently.*

- **A multi-path `lsp diagnostics` entry for a statusless answer carries no
  status (2026-09-22).** An old server's `{}` parses, so the entry is emitted
  with `path` only (`status`/`detail` are `omitempty`) and exit 1, where the
  single-path form refuses with a rebuild-to-match-ctl version-skew message.
  Give a parsed-but-statusless entry the same `lspStatusError` status and
  detail, so every entry in the batch answers with a status.

- **`search`'s per-file cap is not configurable and `capped` stays false
  (2026-09-24).** A file with more matches than the per-file limit reports the
  limit (`"truncated": [{path, shown, total}]` plus a stderr note) while
  `"capped": false`, so a caller that reads `capped` alone reads a cut file as
  complete; two wave agents were misled. Add `--max-per-file N`, or make
  `capped` true when `truncated` is non-empty. *A per-file cap is not the
  global one.*
- **A numeric positional path on `read --at` reads as a missing buffer
  (2026-09-24).** `read --at PATH=LO,HI 380,580` refuses with `no open buffer
  for that path: 380,580` rather than naming the span malformed; the flag's own
  refusal is good, so the positional path should recognise a bare `LO,HI`.
  (Reproduced against the running editor.)
- **One file at two `--at` spans collapses to the last span (2026-09-25).**
  `read --at a.go=1,2 --at a.go=20,21` returns only the 20,21 region:
  `atSpanFlag.Set` keys `index` by path and overwrites the earlier entry, and
  `readTargets` dedupes by path, so the CLI never emits two targets for one
  file. Cross-file `--at` frames both, and the `text` op carries a target per
  span, so the wire is not at fault — this is CLI-side. The flag is documented
  "repeatable", so a per-span same-path form (key by path+span, one target per
  entry, keeping the positional one-read dedup) would close it; two disjoint
  regions of one file currently cost two calls. Raw-recorded 2026-09-22; promoted
  here.
- **A bulk decision line names only a per-buffer group id (2026-09-24).**
  `decideAll` prints `accepted change set 3`; under `--all --everywhere` the id
  is per-buffer, so two buffers' set 3 are indistinguishable in the output.
  Include the path in the line.
- **The sanctioned scratch dir `/tmp/opencode` is not writable
  (2026-09-24).** It is `root:root 0755` in the container (probed: `touch`
  denied) while `/tmp` works, so a subagent following the skill's
  already-created, pre-approved path stalls. Fix the image ownership, or have
  the skill name `/tmp` as the fallback. *A pre-approved path that is
  unwritable is worse than no path named.*

- **Group tasks are lost on the gated journal restore (2026-09-24).**
  `NewRestoredSession` (`internal/piecetable/session.go`) takes `groupState`
  but no `groupTask`, and `buildSession` (`internal/app/journal.go`) rebuilds a
  session from base/Op/Decision records only, so a task set with
  `SetGroupTask` is gone after a `RAJ_JOURNAL` restore; the in-memory snapshot
  carries `GroupTask`, the journal does not. Record the task in the journal or
  state the loss. *A manifest keyed on the journal's task must survive the
  journal's own restore.*
- **`Participant.Task` is not on the wire and not seeded across restart
  (2026-09-24).** The header's participant record writes
  id/identity/name/kind/connected (`internal/control/header.go`), and
  `restoredAuthors`/`Registry.Seed` (`internal/app/journal.go`,
  `internal/control/participant.go`) carry no task, so a reconnecting agent
  keeps its task only by re-running `register --task`. Add the field to the
  participant wire record and the journal author table.
- **The opencode plugin computes the task but does not inject `--task`
  (2026-09-24).** `plugins/raj-gate.ts` reads `taskOf(sessionID)` for the
  ledger; putting `--task <session>` on the `raj ctl register` call (or setting
  it per session) would populate `Group.Task` without an agent remembering to.
  Decide whether the plugin should, given it deliberately stopped injecting
  identity.
- **The repo `skills/raj-editor/SKILL.md` lags the new agent surfaces
  (2026-09-24).** It documents `register --as` but not `register --task`,
  `accept`/`reject --all` but not `--all --everywhere`, and none of the
  file-lifecycle verbs (`mkdir`/`delete`/`deletions`/`rename`/`rmdir`/
  `rmdirs`); its `clear` text still describes the invalid-only workspace scope.
  Sync the repo copy (and the installed copy) with the live usage.
- **An agent cannot amend its own pending change set (2026-09-24).** The only
  route is `reject --all --mine`, `clear --all --mine`, re-read the version and
  re-apply; a "replace my own set" verb (or `apply --replace`) would make
  re-drafting one gesture. Promoted from the Wave 4 feedback block.
- **`apply --hunks` is impractical for a large Go block (2026-09-24).** A
  function or whole-file move is one JSONL hunk of kilobytes of JSON-escaped
  Go, so a driver falls back to `edit --old-file` or `dump`/`patch`; make
  `--hunks` accept a raw body, or point the briefs at `dump`/`patch` for a
  structural rewrite.

- **`lsp diagnostics` reports `status: ok` for a file gopls cannot
  associate with a package (2026-09-25).** In the B2 `internal/hooks` wave,
  `gate_test.go` and `registry_test.go` — members of a brand-new package whose
  files are unsaved buffers — each answered `ok` with a severity-2 `No packages
  found for open file` inside their diagnostics list, so a status-only sweep
  reads them as clean. `read`, `open`, a `--all` sweep, a wait and `mkdir
  internal/hooks` did not clear it. Report the unassociated state as an error
  (or set `status` from the diagnostic) so a verification sweep cannot count a
  file it never checked; the host save stays the only whole-package check.

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
  `tabs`/`wrap`/`auto_pairs`/`inlay_hints`/`save_check` and leaves an unknown key alone
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
- **`edit --base` does not reject a stale base; it ignores it (2026-09-23).**
  `edit` re-reads the buffer and bases the apply on the version it just read, so
  a second edit deliberately based on v2 applies cleanly on top of v3; `--base`
  is accepted and never consulted. `apply` refuses a stale base and rebases
  hunks, so the read-gate the docs promise holds for `apply` but not for `edit`.
  Decide: honour `--base` in `edit` (refuse or rebase a stale one) or drop the
  flag from `edit`'s surface, rather than letting a caller believe the base was
  checked. Under discussion with the user; not scheduled.

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

## New-file flows are hostile to a human (2026-09-28)

Found while landing the H3 leaves; all three hit in one sitting.

- **A new file is invisible until it is saved.** A buffer proposed for a path
  that does not exist on disk appears in `buffers` but in no tree or explorer
  view, so a human cannot find the thing they are being asked to review. The
  explorer should list pending-new buffers, or `status` should mark them.
- **Save applies to the active buffer only, and there is no save-all.** Across
  a batch review the human saves whichever tab is active; the others stay
  accepted-but-unsaved and look identical to work still in progress. A save-all
  gesture, or a status line naming how many buffers hold accepted-unsaved
  changes, would close it.
- **Saving a new file does not create its parent directory.** A buffer for
  `a/b/c.go` whose `a/b` is absent has nowhere to write; `raj ctl mkdir` was the
  workaround. Saving should create missing parents, or the refusal should say so
  instead of failing at the write.


- **A proposed removal never surfaced in the phone client (2026-09-28).**
  `raj ctl delete scratch-l7-ui.md` proposed a removal; the phone client
  showed no trace of it, so the human had no way to see or approve it there -
  and the file would have ridden into a public commit. If a client is
  deliberately read-only, the refusal has to be legible (a note, or the
  removals list in the action drawer, or in the status bar); a proposal that
  silently never appears is worse than a gesture that is refused. Decide
  which it is and make one of them true. The same question applies to
  `rmdir` and to any future approval that is not a save.

## Between-wave review — `intent diff` follow-ups (2026-09-28)

Found by the review pass on the `raj ctl intent diff` wave; none blocks the
wave, which stands.

- **A truncated `intent diff` is not pinned through `--json`.** `capIntentDiff`
  is unit-tested and `truncated`/`omitted_bytes` are `omitempty`, so the two
  forms are distinguishable, but nothing asserts the JSON keys; the new
  `printIntent` test pins the printed note only. Add a marshal assertion.
- **The container `raj` client stays stale**, so a new `raj ctl` surface cannot
  be driven from a review session and `exec` is refused over TCP. Known; hit
  again by `intent diff`.
- **A seam's diff and proof include other seams' accepted work.** The contract
  in `internal/app/seam.go` is "materialise it alone over its base (its members
  only, no other uncommitted change)", but `intentProjection` composes each
  touched path from the buffer's AGREED text plus the member groups, and the
  agreed text already carries every accepted-but-uncommitted set, whichever
  seam it belongs to. So with seams A and B both accepted in one file, A's
  review shows B's hunks and A's proof checks B's code. It hits both `intent
  diff` and `intent prove`, which share `materialiseIntention`. The existing
  pin cannot see it: `TestIntentDiffShowsMemberSliceNotWorkspace` dirties the
  worktree and proposes one seam, so the agreed text is the members. Fix by
  composing from the base blob plus the chain's members, and pin it with a
  two-seam case (both accepted in a shared file; A's diff must not contain B's
  hunk). Independent of the Q12 answer.

  **Closed 2026-09-30:** the code is already the fix. `memberSlice`
  (`internal/app/intent.go:372`) is the single entry point: it clones the
  session and marks every non-admitted set `Rejected`, so the composition is
  the base plus the chain's members only, and both `intentProjection` and
  `waveProjection` compose through it. The only thing missing was the pin:
  `TestIntentReviewTabShowsOnlyItsSeamHunks` exercises the same composition
  through `intent review`; the direct `intent diff`/`intent prove` two-seam pin
  is re-scoped in `docs/dev/DEBT-PLAN.md` (D1).

## Between-wave review — shellcheck/gates + publish artifact (2026-09-29)

Found by the review pass on the two waves that made `make check` green and
moved publish onto the exported artifact; neither blocks the waves, which stand.

- **`docs/ARCHITECTURE.md` §6 still lists a resolved disagreement.** Its bullet
  ("The `publish-single.sh` header describes building its own commit from the
  working tree on disk; `intent/land.go` says 'publish only pushes a commit that
  already exists'. Neither says which commit ships when both apply.") is no
  longer true: the header now pushes the artifact (the export's commit) and the
  working-tree build is gone. Remove the bullet. (This file belongs to another
  writer; filed here rather than edited.)
- **The publish PR target is auto-detected from `@{upstream}` with no fallback
  (2026-09-29).** `publish-single.sh` takes `base_branch` from the current
  branch's upstream; the machine path always passes `--base` as a raw SHA, so
  the `--base <branch-name>` fallback never fires and a checkout with no
  upstream leaves it empty, making `gh pr create --base ""` fail (exit 9) after
  the branch is already pushed. Escalated, not decided: derive the target from
  the remote default (`refs/remotes/<remote>/HEAD`), carry the export's base
  *ref name* (not only its SHA) through `intent.Publish`, or refuse before the
  push with a named diagnostic — but do not hardcode `main`. See
  `docs/dev/AGENT-FEEDBACK.md`.
- **`Publish.BaseRef` is now a copy of `BaseSHA`.** `proposePublish` sets
  `BaseRef: rec.BaseSHA, BaseSHA: rec.BaseSHA`, so the JSON `base_ref` field
  carries a SHA and the only reader (`CheckPins`' `CheckBranch`) sees the
  base-equality rule become a no-op. Collapse the two fields (drop `base_ref`)
  or carry the export's base ref name. (Low.)

## Quality gates — duplication and cyclomatic complexity (2026-09-29, user)

- **Add clone detection and cyclomatic-complexity analysis as pinned deps, `make` targets, and hooks.** Ask: analyzers for duplication and complexity, wired into the Makefile, then run as `raj` hooks the way `check` is (`["make","check"]`).
  - Shape: pin the tools in `go.mod` behind a `//go:build tools` file so `go run <pkg>` uses the pinned version and the shipped binary carries nothing; `make cyclo` (`gocyclo -over N ./...`), `make dupl` (`dupl -threshold N ./...`), and a `quality` target running both; then hook rows naming those.
  - Two single-purpose tools against `golangci-lint`: the latter is one dep but a large tree, and these two linters are all that is wanted from it. Decide, because it also brings a config surface.
  - Calibrate before gating: set the threshold where the tree is today and ratchet, or `check` goes red on its first run. Two phases - a report target first, thresholds into `check` once the numbers are known.
  - Expect legitimate findings: the mirror packages, the control verb handlers and the per-platform hosts duplicate by construction, and the big render/state functions are long deliberately; a raw count reads as noise, so ratchet or allowlist rather than chase zero.
  - Enumerate with `./...`, not `git ls-files`: the projected gate tree has no `.git` (the same reason `sh-parse` no-ops there), and follow `shellcheck`'s tool-absent precedent - one line, skip - unless a pinned module dep makes the tool always resolvable from the module cache.
  - Hook authoring is the human gesture: the targets and any hook body can land first; the rows are the user's to add.

## X1 runner image follow-ups (2026-09-30)

Found while re-briefing X1 (`harness/Dockerfile.runner`); none blocks it.

- **A new-path proposal is lost or invisible after a restart.** With the
  journal off (`RAJ_JOURNAL` unset, the default) a restart drops pending sets
  outright. With it on, the sets survive but `proposals` does not enumerate a
  journal-only path and `read` refuses it ("no open buffer for that path")
  until some other action loads the path; then the journal restores every set,
  so recovery depends on someone happening to touch the path and an agent
  cannot find its own reviewed work. Seen with the X1 runner Dockerfile
  (groups 1-3 restored, then superseded by group 4). Loss by default,
  discoverability with the journal; related to "A new file is invisible until
  it is saved" above.
- **CI and `go.mod` disagree on the Go version.** `go.mod` declares
  `go 1.25.0`; `.github/workflows/ci.yml` pins `go-version: '1.24'` and its
  comment calls that newer than `go.mod`. It is not, and 1.24 toolchain-switches
  up to 1.25.0 anyway. One of the two is wrong; the runner ships 1.25.0 because
  `GOTOOLCHAIN=local` plus `--network none` forbids the switch.
- **`docs/HOOKS-SPEC.md` §10 lists four cycle images.** X1 adds a fifth
  (`runner`) to the `scripts/raj-cycle.sh` allow-list; the spec is stale.
- **`RAJ_CYCLE_IMAGES` still defaults to `opencode`**, so the runner image is
  built only when named. Whether every cycle builds it is an owner/X2 call.
