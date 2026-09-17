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

- **Save-review lag.** cmd+s with a review popup leaves a visible beat between
  the tint clearing and the dirty dot going away on ~50 KB docs; the
  `internal/timing` instrument is in tree, so diagnose (write path vs
  re-tokenise vs the `PendingMarks`/`Groups` walk in `Draw`) and fix. *The core
  save gesture stops feeling slow.*
- **Editing a buffer that already holds change sets can desynchronise the line
  index.** `read -lines` and `search` disagreed about the same text and one
  `edit` ate a newline (2026-09-17); no reproduction pinned. Look at `File.sync`
  (`internal/editor/file.go`). *Removes a silent wrong-text corruption in the
  edit surface.*
- **Undo-path residual corruption risks (2026-09-17).** `Session.live` has no
  cycle guard (stack overflow on a corrupt restored journal); a failed
  `rollback` can leave a half-reversed document; `resumeSave` can write a pane
  closed during `willSaveWaitUntil`. None has a test. *Closes three routes to
  corrupted text or a wrong-pane write.*
- **A save silently drops invalid/superseded runs.** The LSP campaign left
  declarations that existed only in invalid runs; a plain save would have
  written a file that does not compile. Decide whether the save refuses or the
  runs are disposed first. *A save never silently discards another writer's
  text.*
- **A shape-only external edit is silently reverted on restore.** `Base.Hash`
  hashes decoded text, so a CRLF-to-LF, BOM or charset change passes the guard
  and the log re-encodes the old shape; the smallest fix is a byte digest on
  `Base`. *External byte-level changes survive restore.*
- **An unsupported-encoding open falls to the status line.** `App.OpenFile`
  turns `ErrBinary`/`ErrTooLarge` into a refusal dialog but leaves
  `ErrUnsupportedEncoding` in the generic status. *The refusal shows where every
  other file refusal does.*
- **A second raj process squatting the control port hangs the driver.** The
  connection stays open while the wrong process says nothing; add a client
  connect timeout that names the wrong-process case. *A misconfigured port fails
  loudly instead of hanging.*
- **The proposal tint is hard to read** (user-reported). Pick a higher-contrast
  index, or add a high-contrast mode. *Review is legible.*
- **Display width: `↔` (U+2194) drifts the caret.** The hand-rolled table calls
  three East Asian Ambiguous runes narrow; arrow along a line with `↔` versus an
  em-dash to confirm, then fix the width of the culprit. *The caret lines up on
  real files.*
- **A workspace-wide `lsp diagnostics` sweep before the host gate.** Per-file
  checks read `ok` while cross-file references are broken (7 files in the LSP
  review); make an `--all` sweep over the changed files the standard pre-gate
  step. Even a clean sweep is not a package-wide signal: gopls will not report
  a test-only compile error in a file the sweep did not name, nor a `go vet`
  failure, so the host `go test`/`vet` remains the whole-package check.
  *Waves stop exporting breakage to the host's `make check`.*
- **Unify the two server-edit appliers.** `applyDocEdits` (rename) skips the
  lease pre-check `applyServerEdits` does, so a rename can half-apply around a
  pending, rejected or invalidated run. *Server edits cannot split a proposal.*
- **A whole-file `patch` on a large file arrives as one coarse change set** once
  `n*m > 1<<20` (`control.DiffLines`): a ~1.7k-line `dump` to `patch` reviews as
  a wall of +/- and its lease blocks every other writer. Bound the line diff.
  *Large `patch` reviews stay reviewable.*
- **F3b-ii — the presentation half, with D2b.** Folds are live in Edit mode
  (D2a) but every app consumer still compares session lines to display rows
  (`render.go`, `app.go`, `review.go`, `control.go`, `inlay.go`, `session.go`);
  move them onto `DispPos`/`DocAt`/`DispOfDocLine` and clamp against
  `DisplayLines()`. *The projection is correct wherever a fold sits.*
- **Invalid-set handling: the Phase 1c gaps and the reconciliation failure
  modes.** `Project` does not consult `Invalid`; `review`/`proposals` cannot
  name an invalid set; the fold/annotation is not drawn; `buffers`' `pending`
  excludes it; `clear` cannot dispose of it; and a save drops it (above). *An
  invalid proposal is nameable, countable and disposable.*
- **Deferred deletions.** A proposed deletion is not performed until accept,
  which is what makes a deletion visible at all and the only correct lease for a
  deletion-only set (a write over the planned range currently succeeds).
  *Deletion proposals are visible and lease-safe.*

## Later

One line per item, grouped by theme; nothing here is scheduled. Numbers live in
BENCHMARKS.md and decisions in INVESTIGATIONS.md.

### Layered proposals

- One jump path: move `host.Goto` onto `jumpToSessionLine`, or state why the
  column-addressed jump stays separate.
- Change gutter vs `HEAD`, annotated `read`/`exec`/LSP composition, git verbs
  (read-only diff first).
- Groups carry rebased ranges; the change-gutter and diff-style rendering
  consumers do not use them yet.
- `raj ctl diff -vs HEAD` (git, stretch): needs the range-rebase walk, deferred
  deletions and a read-only `git show HEAD:<path>`.
- Diff-style rendering: green additions, red deletions; the gutter carries who.
- A rejected group can be wedged by a later overlap; name what overlapped so the
  caller can re-propose.
- An app-level advisory-lease test with two identities (only the Rejected
  refusal is covered today; the wire path is unpinned).
- Durable log: fold `session.json` into the store; compaction and checkpoints;
  the SQLite engine behind the record interface; the attachment model (loaded vs
  announced, headless read).

### Editor and LSP

- **In-file find & replace is the next wave.** The find bar is live; the
  replace half, its chords and its undo story are the intended next step.
  *The next wave has its item on the list.*
- Symbols are found by leading keyword, not parsed; per-language scanning is the
  next step (tree-sitter is the direction).
- The reserved-chord tables are short; extend them whenever another collision is
  found the hard way.
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
- File lifecycle remaining: directory rename and `run -prog` reachability for
  the new verbs.
- Small and split panes, and how they resize.
- No Bubbletea adapter yet (the `ui.Host` surface keeps growing).

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
- `search -path` into a hidden directory ignores `-hidden`; decide and state it.
- Multi-target read: `-json` shape differs from the single read (no author, no
  per-file spans); decide whether it carries authorship.
- Multi-target read: a shared span that overruns one target refuses the whole
  call while a shared `-lines` clamps per file; decide clamp or refuse.
- `Buffer.Bytes` means document length in a `buffers` reply and contributed
  bytes in a multi-read; document it.
- `readMany`'s `annotated` parameter is always false; drop it.
- `read -lines` carries no byte offsets; a byte-span read is still wanted.
- `run -prog` payload paths are unmapped (`Client.toEditor` maps the field verbs
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

### Control socket and agent surface

- Three nested header strings are still JSON (`DiffJSON`, `LSPJSON`,
  `StatesJSON`).
- The request header can go once recv, hello and cancel have opcodes, or once
  they are decided to stay JSON forever.
- Every agent shares one tint; a user watching two agents cannot tell them
  apart.
- `watch` (the push) and journal persistence of `claim` remain.
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
  `delete -withdraw` and `rmdir -withdraw` trust the field.
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

### UI, terminals and rough edges

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
- `markInvalid` adds an O(ops²) journal pass to `Groups()`; measure before
  optimising.
- A superseded warning names the first intersecting run, not the bounding span
  (escalated, not decided).
- The container `raj` can lag the editor; rework the release/rebuild step or
  make an empty `srcVersion` detectable.
- A `claim` without `-add` silently replaces the set; warn when it replaces a
  non-empty set.
- The standing between-wave reconciler needs a thin wrapper so the pass is
  invoked rather than remembered.
- A saved buffer can still carry proposed sets after a rebuild and restart;
  needs a reproduction.
- A buffer that is entirely another author's pending proposal was uneditable;
  the overlap/reconciliation case.
- New `raj ctl` against an old server warns falsely on `-include` (the shipped
  skew warning does not suppress the zero-`Considered` message).
- Subagent transcripts feed the efficiency loop (mine task outputs for wasted
  tool calls and brief-quality patterns).

### Agent feedback — actionable (context in AGENT-FEEDBACK.md)

- Name the search hit's offsets so a row cannot be mistaken for a byte range:
  `line` is a line number while `line_start`/`line_end` are byte offsets.
- State or fix the scope split between `groups` (one buffer) and `proposals`
  (whole workspace), so `groups -mine` with no focused buffer does not read as
  "no sets".
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
- Line/col in `apply`/`edit` replies needs a wire and coordinate decision.
- Make the write target explicit (a mandatory path or `-active`/`-here`).
- Consolidate `read`/`version`/`dump` and collapse the write verbs onto one
  door.
- Raw-LSP passthrough: decide whether it earns its surface, then file it or drop
  it.
- Define or drop the Stream A/B/C/D labels; the user's definitions are needed.
- No `ping`/health-check CLI verb (the protocol op exists).
- `help`/`-h` are reachable but absent from the usage list.
- `whoami -as X` printed a fresh anon id once; confirm before trusting `whoami`
  as the bind check.

## Direction (documented, not scheduled)

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
- **`edit -base` and miss-point reporting** — under discussion with the user;
  not scheduled.

## Deliberately not doing

- Horizontal scrolling while wrapped: there is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
- The container build/run stays the external `bldraj`/`oc` shell functions, not
  folded into the binary as `raj box build`/`raj box run`.
- `exec` over TCP is refused by design — the command would run on the editor's
  machine, outside the driver's container.
