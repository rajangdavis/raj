# Agent feedback — raw agent notes

Raw, dated feedback from raj agents who drove the editor: tool-usage reviews,
friction notes and field reports. This is the evidence record, moved here from
TODO.md with its dates and internal structure intact; actionable items are
tracked in TODO.md and completed ones are archived in COMPLETED.md. Bullets
whose issue is already tracked there have been pruned, so what remains is the
session narrative, observations and friction with no other home.

## Notes from driving `raj ctl` as an agent

One session's worth of friction, recorded where the next driver will find it.
Each item is cheap relative to what it cost to work around.

- **Container ergonomics: no shared filesystem means stale-file checks are
  manual.** `exec` is correctly refused over TCP, but then nothing warns the
  driver that the bytes it is about to compile in its own sandbox do not
  match the buffers. `buffers -json` answers it with a second round trip;
  what is missing is the prompt to make one. A client-side flag (`exec
  -warn-stale`, refusing locally if any buffer is dirty) would move the
  check into the driver's own sandbox where it belongs.
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
- **The Stream A/B/C/D labels are used but never defined.** The globs fix is
  Stream B, scroll-ratio restore is Stream C, the driver-round additions ride
  Stream D, and no file says what the streams are. Needs definitions from the
  user to write.
- **`-as` is refused before the verb and accepted after it.** "unknown
  command" in one position, silently fine in the other, and the usage text
  shows neither — first flag after the verb is the only placement that
  works. Also observed: `whoami -as X` printed a fresh anon id rather than
  the bound one. Unreproduced since; treat it as a suspected bug and
  confirm before trusting `whoami` as the bind check.
- **Ten exploratory commands burned twelve author ids.** 26/256 used
  mid-session, 55/256 by session end, almost all of them dead anons. That
  is the live validation for server-minted identity, and the evidence for
  recycling `gone` ids — the open decision on server-minted identity in
  TODO.md.
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
  "one listener, not both" item in TODO.md, and a client-side connect timeout
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
RECURSIVE-RAJ §8 step — deduped against the notes above; "none reported"
waves were W2a and most of W2b, meaning the surface held):

- **Bash heredoc/`"` nesting in `edit -new` arguments is fragile.** 3c
  reported a chained command that quoted poorly and had to be split into
  separate guarded edits. Partially a discipline note (already in the
  skill: single-quote args, use -old-file/-new-file for big blocks), but a
  `-new-file` path is the documented escape and subagents reached for it
  only after a failure — briefs should name it up front for multi-line
  payloads.

Agent tool-usage review, wave reports 2026-09-10 (second plan instance, waves
0/1/3 — the sync wave reported the surface held; new items below):

- **Truncated tool output spills to a dead-end path** (harness-side, not raj):
  the spill file under /home/oc/.local is outside raj's root and unreadable by
  a subagent. Driver guidance: keep searches narrow.
- **One garbled line on a long `read -lines 1,330`** — unreproduced on re-read
  of the exact span; suspected display-side one-off, recorded in case it
  repeats.

Agent tool-usage review, wave reports 2026-09-10/11 (this session's swarm —
review-flow chords, relative paths, search honesty, and a read-only save-lag
diagnosis; deduped against the notes above):

- **Concurrent writers share the version stream, and rebasing handles it.**
  Two subagents edited `control.go` concurrently; a hunk applied with a stale
  `-base` rebased onto the other writer transparently. Worth knowing, not a
  defect.
- **A stale root `/work/KEYBINDINGS.md` duplicates the tested
  `docs/KEYBINDINGS.md`.** No test reads it; delete it.

## Agent-efficiency review — the walk and the ctl surface (2026-09-11)

Two reviewers read the D1/D2 and H1/H2/H3 subagent reports and the walks they
implied; a follow-up swarm implemented the cheap half. Every actionable item
from this review is tracked in TODO.md and COMPLETED.md, so the bullets are not
repeated here. "Walk discipline" now lives in RECURSIVE-RAJ §5 (anchor →
locate → owning file → enclosing block → seam).

## Agent tool-usage review, 2026-09-11 (inlay hints, fallback)

The inlay-hints rewrite was the substantive work of this session; the defects
it surfaced (line-index corruption, the `search` `ByteStart`/`LineStart`
mismatch, cross-buffer diagnostics staleness, the missing raw-LSP verb, the
`/tmp/opencode` permissions) and the remaining inlay-hints steps are all
tracked in TODO.md and COMPLETED.md.

## Agent tool-usage review, 2026-09-11 (wave A — control surface)

The three fixes (line-index corruption, `reject -all` newest-first, the
`search` line-start offset) are in tree and tracked under TODO.md's `[~]`
items and COMPLETED.md. One friction item is not tracked anywhere:

- **`apply`/`edit` report a size, not the resulting line.** "goto each
  hunk" costs a second `search`; a line offset in the reply would make it
  mechanical.

## File lifecycle verbs — create, delete, rename (agent feedback, 2026-09-13)

An agent adding modules (`create-store.js`, `hex.js`, `register.js`) or removing
dead files had to hand the work back to the host: `raj ctl` has no create,
delete or rename verb. Creation is partly covered — `open <path> -create` makes
a buffer for a path that is not on disk, and a save writes the file — but the
verb is not discoverable as "create", the container client was built before the
flag existed, and nothing creates a missing parent directory, so a module in a
new directory still fails at save. Delete and rename do not exist at all.

Desired verbs, with the open decisions:

- **`create [path]`** (or keep `open -create` as the one spelling). Empty buffer
  → apply → save writes the file. Decision: parent directories — `MkdirAll` on
  save for an explicitly created buffer, or a separate `mkdir`?
- **`delete [path]`.** Tear the buffer down (`closeDoc`, drop the tab/headless
  entry, remove the journal log, `TouchSession`), then remove the file. Refuse a
  dirty buffer or one holding proposals (decide the work first), a directory,
  and anything outside the workspace. Decision: unlink outright, or move to
  `.raj/trash/` so it is recoverable without a VCS?
- **`rename [path] NEW`** (or `move`). Both paths in the workspace; refuse an
  existing destination and a dirty buffer. Rename on disk, then update
  `File.Path`, close the old journal log (the log name is a hash of the path)
  and the old language-server doc, and update the session. Import edits are
  separate text proposals, not part of the rename.

One design question behind all three: are these immediate verbs like `open` and
`close`, or should a file operation surface as a decision in the review flow the
way text does? The dirty/proposal refusal keeps immediate verbs
review-consistent, and a file operation is not a text span; but a deletion is
irreversible in a way a rejected hunk is not.

Context confirmed while investigating: `open <path> -create` is implemented
server-side (`Request.Create`, `hCreate = 0x12`, `Guard.Open`,
`host.Open(path, create)`); the agent that saw "no create verb" was driving a
container client built before that flag, so rebuilding the container image is
part of any fix.

## Reconciler notes, 2026-09-15 (revert + control transport)

A reconciler read the `revert`/transport batch after it landed green. Verified
clean: `noteDecision` never drops a set's decision (decisions move only in
`Session.RevertAuthor`, behind `hasLiveMembers`), `host.Revert`'s wedge
bookkeeping is complementary to `File.sync`, the `token` wire code is unique,
`Header.Token` genuinely carries both directions, and `listenUnix` had no
references. The findings below are raw; the actionable ones are in TODO.md.

- **`RevertAuthor`'s false return was ambiguous.** `Session.RevertAuthor` can
  drop the newest of an author's sets and then wedge on an older one, returning
  `(false, Block{})` — indistinguishable from "nothing live to reverse", because
  `blockFor(0)` yields the zero `Block` and a clamped inverse reports `at == 0`.
  The first cut only re-synced when `block.Group != 0`, so that path left the
  index behind the journal and the decision generation stale. Now guarded by a
  journal-version comparison (`internal/editor/file.go`).
- **Both revert coverage gaps are closed (2026-09-15).**
  `TestRevertAuthorPartialWedgeLeavesNewerSetReversed` and
  `TestRevertAuthorKeepsASharedGroupsDecision` (`internal/piecetable/groups_test.go`)
  cover the partial wedge and the shared-group decision. No `internal/editor`
  `File.RevertAuthor` test was added: the only extra guard it could reach, the
  unnamed-block version clause, needs an op whose bad offset the public API
  clamps away, so the piecetable test's version assertion pins the fact the
  guard consumes rather than the guard itself.
- **Only `revert` compares the author to the connection — the docs used to
  overstate.** The serve loop enforces a mismatch only for `revert`; `patch`,
  `delete -withdraw` and `rmdir -withdraw` compare the named author to a *stored
  record's owner* instead, and every verb trusts the field for attribution. So a
  hand-built frame can name a peer, be attributed as them, and on the
  claim-gated writes act inside their claim set. The CLI never sends a foreign
  id. `Header.Author`/`Request.Author` and the TODO item now say this exactly
  instead of "only revert refuses".
- **`TestEveryVerbHasACode`'s list is hand-kept and still incomplete.** It omits
  `dump`, `patch`, `lsp`, `lspprep`, `send`, `who`, `reload`, `goto` and
  `close`, so its comment that it "fails when someone adds a verb" only holds if
  someone also edits the list. `token` was added alongside its new code.
- **Cursor offsets are not remapped across reverted bytes.** `File.sync` moves
  the line index and `Cursors.Normalize` sorts and merges, but nothing slides a
  caret across removed text, so a cursor past EOF survives a revert. This
  matches the older `Clear`/`Decide` handlers, so it is pre-existing, not new.
- **A container restart wipes `/tmp`.** Staged patch files for the second
  cleanup pass vanished when the image was rebuilt and the container recreated;
  the orchestrator rebuilt them from task history. Cheap and recoverable, but
  worth knowing before staging anything long-lived under `/tmp`.
- **The reconciler checked identifiers, not arity, and the host caught it.**
  A new test wrote `!s.RevertAuthor(Agent)` — a two-value call in a
  single-value context — and `go vet` stopped the build. Confirming that every
  identifier exists is not the same as checking each call's result count; treat
  multi-return functions as an explicit reconciler checkpoint.

## Agent verb-surface audit — `raj ctl` (2026-09-13)

Read-only audit of the running editor (author 3; root `/work` → `/Users/rajandavis/Desktop/projects/raj`), 2026-09-13.
Sources: `docs/AGENT-FEEDBACK.md`, TODO's "Agent feedback — actionable", `raj ctl -h` plus per-verb `-h`, and `internal/control/{cli,host,control}.go`, `internal/app/control.go`. No code changed.

### Inventory

- **Inspect/size**: `buffers`, `read`, `version`, `dump`, `search`, `lsp`, `whoami`, `who`, `list`, `stats`
- **Write**: `apply`, `edit`, `patch`, `run -prog`, `save`
- **Lifecycle/present**: `open` (`-create`), `close`, `goto`
- **Review/decide**: `groups`, `diff`, `review`, `accept`, `reject`, `clear`
- **Talk/run**: `recv`, `exec`

Protocol-only ops with no CLI verb: `ping` (used by `whoami`), `text` (the `read` op), `execcheck`, `snapshot`, `lspprep`, `prog`, `hello`, `cancel`. `help`/`-h` are handled but absent from the usage list.

### Friction → root cause

1. **Path spelling is not all translated (path resolution).** Relative, `/work`-absolute and editor-spelled paths all resolve *inbound* — `read` verified for all three — and `buffers`/`groups` come back `/work`-spelled. But `diff -json`'s `path`, `search -json`'s `truncated[].Path`, and every error string keep `/Users/...`. Cause: `Client.localise` (client.go) rewrites a fixed field list (`Root`, `Buffers`, `Matches`, `Dirty`, `Groups`) and misses nested `DiffJSON`, `Truncated` and `Err`. One seam, applied unevenly. README claims "what `search` prints is something the agent can open" — `truncated` already contradicts it. (Fixed 2026-09-15: `localise`
now covers the nested `DiffJSON`, `Truncated` and `LSPJSON` shapes and `Err`
text.)
2. **The default target is silent (defaults/discovery).** `apply`/`edit`/`read`/`save`/`close` take an optional positional; omit it and the verb targets whatever tab is active. Verified live: `edit -old <absent>` with no path read the active buffer, and the refusal says "the buffer" without naming it. `-h` prints only the shared FlagSet, so no verb's positional is shown.
3. **Verb redundancy.** `read`/`version`/`dump` overlap; `apply`/`edit`/`patch`/`run` are four write doors; `open -create` is the only create and "create" is not a verb; `groups`/`diff`/`review` list the same sets three ways; `accept`/`reject`/`clear` are three decisions over one state machine.
4. **Lease and error recovery.** Writing over **another writer's** proposal refuses with "change set N owns this text; accept or reject it first"; recovery is reject → `clear` → re-apply. Writing over your **own** `Proposed` set now amends it instead (2026-09-13). `clear` exists but is documented only on its usage line. The session's reported `accepted 0 ops` artifact did not reproduce (the string is absent from the tree).
5. **Output shape — fixed 2026-09-13.** `search -json`, `stats -json` and `run -json` now carry snake_case json tags (`path`/`byte_start`/`line_start`, `runs`/`stale`/`agent_only`), matching `read`/`buffers`/`groups`/`diff`/`version`/`whoami`/`apply`/`lsp` and `search -jsonl`. The contract is snake_case throughout.
6. **Lifecycle gaps.** Create is only `open -create` (no parent-dir handling); no `delete`, no `rename` — those still hand work back to the host.
7. **No region lease.** `apply` rejects a stale hunk after the fact; nothing lets two writers claim a span up front (`claim`/`watch` still unimplemented).

### Ranked simplifications

| # | Change | Removes | Friction | Blast | Safe now? |
|---|---|---|---|---|---|
| 1 | Extend `localise` to `DiffJSON`, `Truncated`, `Err` (or emit caller-relative paths host-side) | `/Users` vs `/work` mismatches | high | low | yes |
| 2 | Per-verb usage showing positionals and the default target; name the buffer in default-target refusals | silent wrong-buffer edits | high | low | yes |
| 3 | One `-json` field contract (snake_case); make `search`/`stats`/`run` match; document it | per-verb parsing special cases | med | low | yes |
| 4 | Document `clear` (reject → clear → re-apply) and `open -create` as create | undiscoverable recovery and creation | med | low | yes |
| 5 | Add resulting line/col to `apply`/`edit` replies | a follow-up `search` to goto each hunk | low | low | yes |
| 6 | Require an explicit target (`-active`/`-here`) or mandatory path for write verbs | the silent-default footgun for good | high | med | needs pass |
| 7 | Consolidate `read`/`version`/`dump`; one write door over `apply`/`edit`/`patch`/`run` | near-duplicate verbs | med | high | needs pass |
| 8 | `create [path]` with `MkdirAll`, plus `delete`/`rename` | host hand-off; phantom-tab creation | med | high | needs pass |
| 9 | Region leases (`claim`/`watch`) + overlap reporting | stale-hunk conflicts after the fact | high | high | needs pass |

"Safe now" = docs/defaults/aliases, or a change that does not alter what a verb means. "Needs pass" = semantics or wire; design before code.

### Safe now (no semantics change)

Status 2026-09-15: bullets 1–4 done, bullet 5 deferred. Raw findings and the
wire/design questions in `docs/AGENT-FEEDBACK.md`.

- [done 2026-09-15] Extend `localise` to nested `DiffJSON`/`Truncated` and to
  `Err` text. Those landed before this date; the 2026-09-15 pass also mapped
  nested `LSPJSON` location paths, the one `Response` shape still missed.
- [done 2026-09-15] Per-verb usage text: positionals plus the default target;
  name the buffer in default-target refusals. The usage half was already in
  place; the naming half is `targetName`/`activePath` in `internal/control/cli.go`.
- [done 2026-09-13, verified 2026-09-15] One `-json` field-naming contract; the
  three Go-name verbs move to it. All exported wire/JSON structs carry
  snake_case tags.
- [done 2026-09-15] Document the `clear` recovery path and `open -create` in
  README and the skill. README already had both; the skill gained the
  `open -create` sentence in its open paragraph.
- [reclassified 2026-09-15: needs a design pass] Add the affected line/col to
  `apply`/`edit` replies. A single sparse field cannot carry per-hunk positions,
  and the wire shape plus the resulting-coordinate semantics need a decision.
  See `docs/AGENT-FEEDBACK.md`.

### Needs a design pass

- Line/col on `apply`/`edit` replies (moved here 2026-09-15): per-hunk versus a
  single span, byte column versus editor column, and what a no-op or stale-base
  hunk reports.

- Making the write target explicit (mandatory path or `-active`), which decides the footgun permanently.
- Merging `read`/`version`/`dump` and collapsing the write verbs onto one door.
- `delete`/`rename`/parent-dir create, and whether a file op is immediate like `open` or a review decision like text.
- Region leases (`claim`/`watch`) and overlap reporting.

### Leave alone

- `accept`/`reject`/`clear`: a state machine, not redundancy.
- `apply` (offsets) vs `edit` (quoted string) vs `dump`/`patch` (editor-side diff): three real choices for three situations.
- The read-before-write gate; proposals with the save refusal; `exec` refused over TCP; `run -prog` as the batch door.
- `search` refusing a bare positional; `open` refusing a typo without `-create`.

### Already fixed — do not re-file

- Headless read: `read`/`version`/`lsp diagnostics` load on demand with no tab; `open` now means show (headless-read spec).
- A typo'd `open` is refused unless `-create` (verified live).
- Rebuilt client: `-create` is parsed and forwarded; the "no create verb" report was a stale container binary.
- Registry: gone ids recycled at the 255 cap skipping id 1; `Registry.Seed` keeps attribution across restart (COMPLETED.md).
- Relative/absolute/editor-spelled paths all resolve inbound (`read` verified), and response-side spelling is translated too (`localise` covers nested `DiffJSON`/`Truncated`/`LSPJSON` and `Err` since 2026-09-15).
- Identity: version handshake, server-minted `tok_...`, per-session `RAJ_IDENTITY` absorption by the plugin.
- `reject -all` newest-first, line-index corruption, search `LineStart` — in tree, host verification pending (TODO `[~]`).

### Undocumented / unreachable

- `clear`: reachable and in the usage block, but absent from README, docs and the skill.
- `help`/`-h`: reachable, not listed in usage.
- No CLI verb is unreachable; the protocol-only ops above are internal by design.

## Verifying the safe simplifications, 2026-09-15 (verb audit)


Re-checked the five "Safe now" bullets in the 2026-09-13 verb-surface audit above against the
tree after two of them had landed. The audit is dated; below is what moved, what
changed now, and what is deliberately left open.

Premises that moved since the audit:

- **Pathless writes no longer default to the active tab.** `Guard.claimTarget`
  (`internal/control/host.go`) sends a pathless `apply`/`patch`/`clear`/`revert`
  to the sole claimed file, or refuses with `claim a file first` / `claim set
  has N files; name one`. Friction #2's "apply targets whatever tab is active"
  now holds only for the read-side verbs, and the claim-set refusals already
  name the set.
- **`localise` had grown well past the audit's field list.** `Truncated`,
  `DiffJSON`, `Err` and every flat path list (`Buffers`, `Matches`, `Dirty`,
  `Groups`, `Claims`, `ClaimOverlaps`, `Deletions`, `DirRemovals`, `Proposals`)
  were already mapped. The one `Response` shape still missed was `LSPJSON`: a
  definition/reference answer carried editor-spelled `locations[].path`. Fixed
  2026-09-15 in `internal/control/client.go`, mirroring the `DiffJSON`
  unmarshal/rebase/marshal, with only `Locations` rewritten so hover `text`
  stays content; test added to `TestLocaliseRewritesNestedAndProsePaths`
  (`internal/control/cli_test.go`). `run -prog` payload paths remain unmapped —
  a program carries raw bytes, not a `Response` field — and stay noted in
  TODO.md.
- **`-json` field names were already unified.** Every exported wire/JSON struct
  (`Hunk`, `Group`, `GroupOverlap`, `GroupOverlaps`, `DiffHunk`, `SearchMatch`,
  `TruncatedFile`, `ExecStats`, `DirtyBuffer`, `Buffer`, `Participant`,
  `Message`, the `wire.go` search records) carries lowercase snake_case keys;
  no `json:"CamelCase"` tag remains. Bullet 3 needed no work.

Bullet 2, the naming half: `editTarget` used to say "the buffer the user is
looking at" with no way to name it — the client cannot see which buffer the
server resolved an empty path to, and the comment said so. Added `activePath`
and `targetName` (`internal/control/cli.go`): when a verb was given no path,
the CLI asks `buffers` once and prints the focused buffer's path in the `edit`
refusal, the `goto`/`diff`/`review` outputs, and the `close -discard` note.
`close` resolves the name before the close because closing the active buffer
moves the active tab. It is a message label, never a target: the request still
carries the empty path, so the editor resolves the same buffer it would have.
Only the pathless case pays the extra round trip, and a failed lookup falls
back to the old wording. `decideAll` was left alone: `accept`/`reject` default
to the active buffer but `clear` defaults to the claim set, so one label cannot
name both.

Bullet 5, deferred rather than done:

- **Line/col in `apply`/`edit` replies needs a wire and a coordinate decision.**
  The host already has the primitive — `diffLines` (`internal/app/control.go`)
  turns a hunk span into 1-based `line`/`end_line` for `diff` — and after an
  agent apply the new set is reachable through `Session.DiffPending()` with its
  rebased spans. But a single sparse scalar cannot describe N hunks: `apply
  -hunks` and `edit -all` are both multi-hunk. Carrying a per-hunk list means a
  new header field (the nested-string hatch `DiffJSON`/`StatesJSON` use) plus
  `encodeHeader`/`decodeHeader`, `Response`, the host and the CLI, and it forces
  decisions the audit does not make: is the reported position the hunk start or
  the whole replaced span; is `col` a byte offset or an editor column; what is
  reported for a no-op hunk (`ApplyDiff` skips them, so no set is created) or
  for a hunk that lands against a stale base. Adding the field before answering
  those would freeze a coordinate contract by accident, so this piece stopped
  and is reported instead.

## Documents-consolidation review pass (2026-09-15, between-wave review)

A documentation wave renamed `RECURSIVE-RAJ.md` and `AGENT-FEEDBACK.md`, folded
`PROPOSALS-SPEC.md` into `LAYERED-PROPOSALS-SPEC.md` §14, `AGENT-VERB-AUDIT.md`
into the dated verb-surface audit above, and `RECONCILIATION-UX.md` into
INVESTIGATIONS' "Reconciliation UX" direction, and added the `docs/README.md`
index with its placement rule. The review pass reread every folded section
against its source: the ranked simplification table, the `Leave alone` and
`Already fixed` lists, the pivot, visual models A–D and the open questions all
survived intact, with only heading levels and the old feedback filename's
citation updated to `AGENT-FEEDBACK.md`.

Findings — the wave did its folds but not its deletions:

- **The three folded source docs survived the move.** `docs/PROPOSALS-SPEC.md`,
  `docs/AGENT-VERB-AUDIT.md` and `docs/RECONCILIATION-UX.md` were still on disk
  after the wave was accepted and saved, so searches for the pre-rename
  filenames still resolved (three and two hits), none had a
  `docs/README.md` row, and the placement rule was violated by their continued
  existence. The content is fully folded, so their retention was pure
  duplication; the review pass proposed their deletion.
- **Root `/work/KEYBINDINGS.md` was not deleted either.** The wave's brief said
  it was, and a note in the 2026-09-10/11 wave reports above already called it
  stale. It is 109 lines against the tested doc's 121 and the two have drifted
  (the reload chord, the inlay-hints row, the review chords). Deletion proposed.
- **The dangling-citation cleanup left short lines.** Dropping the
  `EDIT-BUFFER-STRATEGY.md` references from `internal/piecetable/doc.go` and
  `naive.go` (comment-only, correct) orphaned a few words at the end of a
  comment line; the review pass reflowed them without changing the meaning.

With the deletions accepted, `docs/README.md` matches the set exactly (15 docs,
every row a real file) and the placement rule holds for the remaining docs.

## F3b-ii D2a review pass (2026-09-15, between-wave)

Scope: `internal/editor/pane.go` (`UpdateDisplay` memo), `internal/app/mode.go`
(`displayPolicy`), `internal/app/render.go` (`drawEditor` call site), and the
tests `internal/editor/display_test.go` / `internal/app/mode_test.go`. The wave
was accepted and saved; `proposals` and `groups` were empty and the container
has no git checkout, so the wave was enumerated from the brief file list alone
(see the TODO item that raises).

Verified as correct:

- **The memo is sound.** The key is `(Session.Version(),
  DecisionGeneration(), policy)`; `SetDisplay` stores it beside `disp` and
  `dispKeyValid` covers the zero value, so the first call always builds and no
  caller can refresh the projection without its key. A decision moves the
  generation without the version (`File.AcceptGroup`/`RejectGroup` only mark
  state; `noteDecision` bumps `decisionGen`), an edit moves the version, and a
  mode switch moves the policy. `displayBuilds` is asserted in every rebuild
  and every skip test, so a regression trips it.
- **Mode mapping is exhaustive for the two-value enum** (`ModeEdit`,
  `ModeReview`); only Review yields `Annotated`, and no other state reaches the
  `else`.
- **Call-site ordering holds.** `drawEditor` calls `UpdateDisplay` after the
  nil-pane guard and before `fitHints`/`RenderFocused`; the only production
  render path is `App.Draw → drawEditor → RenderFocused`, and the other
  projection readers (`drawDiagnosticMarks`, `drawProposalMarks`,
  `drawCompletion`) run after `drawEditor` in the same frame.
- **No-decisions identity holds**: `view.Build` returns nil iff there are no
  segments, and the identity tests pass unchanged.

Findings:

- **The intermediate mismatch is real and scoped to D2b.** With folds now live
  in Edit mode, `drawDiagnosticMarks`, `drawProposalMarks`,
  `drawCompletion`/`textArea`, `showCompletion`, `proposalsVisible`,
  `cycleProposed`/`reviewPicker`/`reviewRows`/`proposalGroups` plus `jumpTo`,
  `control.go` `Goto`, `inlay.go` `hintLines`/`hintRange` and `session.go`
  restore still compare session coordinates to `Viewport` display rows. A fold
  above a mark shifts it down by the fold hidden line count (or clips it).
  Named precisely in the D2b TODO item; not fixed here per the brief.
- **The display memo was not invalidated when the document was replaced.**
  `Pane.Reload` built a projection, replaced the session, then followed the
  caret through the old map before the next `UpdateDisplay`; `journal.go:563`
  (startup restore) swaps `Pane.File` the same way but before the first frame,
  so only reload was reachable. Fixed in this pass: `Pane.invalidateDisplay`
  resets `disp` and the key, `Pane.Reload` calls it before `FollowCursor`, and
  `TestPaneReloadInvalidatesTheDisplayProjection` covers it. The journal swap
  still relies on happening pre-frame.

- **The `review` agent is not installed in the container.**
  `opencode/agents/review.md` exists in the repo and `plugins/raj-gate.ts`
  allows the type, but `~/.config/opencode/agents/` holds only `raj.md`, so
  `subagent_type: "review"` fails with "Unknown agent type" and this pass ran
  as a `raj` subagent. The installed `skills/raj-review-agent/SKILL.md` is the
  review contract copied without frontmatter, so it does not register as a
  skill either. Actionable item in TODO.md.
- **The review pass has no way to enumerate a saved wave.** `proposals`,
  `groups` and `diff` report nothing once the user accepts and saves, and the
  container has no repository to diff. The brief file list was the only window.
  Actionable item in TODO.md.

## F3b-ii D2b review pass (2026-09-15, between-wave)

The D2b wave moved the app's position consumers onto the display map. The
host `make check` failed on seven tests. The pass below traces six of them to
their fixtures, not to the production move; the seventh is the gutter
placement already fixed in the buffer.

- **The two completion tests could not produce a completion.** With no
  decisions `DispPos(head) == File.LineCol(head)`, so the anchor move could not
  change the result — the failure was that the only buffer word beginning with
  the typed prefix *was the prefix itself* ("foo" at the end of "delta foo"),
  and `complete` deliberately does not offer the word already typed as its own
  completion. `complete.Cache.Rank("foo")` returns `nil` and `Popup.Show`
  closes, which is exactly "no completion was offered". Both fixtures now carry
  "foobar" so "foo" completes to it; the anchor row is still asserted against
  `DispPos`. No production change was needed: `showCompletion`,
  `requestCompletion`, `applyCompletion` and `completionCache.covers` all key
  on the same display line and column, so the cache and the popup cannot
  disagree.
- **`TestHintLinesMapDisplayWindowToSession` counted one row short.** The
  fixture's trailing newline is a real, empty session line and so a real
  display row: `foldedMarkHarness` yields six rows for seven session lines, not
  five, and the test's own comment stopped at the last "ddd" row. The expected
  count is corrected and `hintLines` still returns `(4,5)`.
- **A proposal that cuts a line in half draws on two rows.** The review
  identity test replaced "world" inside "hello world", so the composition is
  `base "hello " | proposed "socket" | base "\n"`; `view.Build`'s
  `emitContent` lays one row *per segment*, so two session lines become three
  rows. That is D1 behaviour D2b exposed, not a D2b regression — `Build` splits
  mid-line folds the same way and its oracle only promises comp is reproduced,
  not one row per line. The review test now uses a whole-line hunk (boundaries
  on the line's start and end) so it exercises the identity it names; the split
  is filed in TODO.md and left alone under the D2b `internal/view` freeze.
- **The journal restore fixture was never written to disk.** `File.Dirty()` is
  content-based and a rejected set is excluded from the agreed composition, so
  a rejected-only buffer reads clean; `flushJournal` then writes no log at all
  and `RestoreSession` has nothing to reopen. The fold was not lost — the log
  never existed. The fixture now leaves a second, still-pending set so the
  buffer is unsaved work; B's `p.SetDisplay(...)` in `restoreLog` is correct and
  is what the test then verifies.
- **`drawDiagnosticMarks` used a display row as a screen `y`.** The pending
  fix to `l.EditorX, top+(row-first)` matches `drawProposalMarks`; checked by
  hand against `gutterCell`'s `TopY+(row-Top)`.
  `TestDiagnosticMarkInsideFoldIsSkipped` was passing vacuously before (the mark
  was painted outside the rows the test scans) and is now a real skip.
- **Deferred seams, still open.** `App.jumpTo` and `reviewJump` are two copies
  of the same map-aware jump; `render.go`'s `sessionTopFor` hover bridge stays
  only because `hover()` still captures session coordinates; and
  `showCompletion` reads the prefix from session bytes because `Pane` exposes
  no row-text accessor. All three are TODO items.

## `ls` + hidden, and the diagnostics false-`ok` review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor. The wave is saved and
green; these are the review pass's observed results and the fixes it proposes.

- **`ls` behaves as specced.** `ls /work` marks directories with `/`; `ls
  /work/.raj` is empty while `ls -hidden /work/.raj` lists `logs/`,
  `session.json`, `trash/`, `w4a/`, `w4b/`; `-json` carries
  `{name,path,dir,size}` and omits `size` for a directory; a file and a missing
  path are both refused (`not a directory`, `no such file or directory`).
  `ls -hidden` reaches `.git`; a root `ls` lists `.raj` itself because the
  defaults hide `.raj/*`, not `.raj`. The brief's `.raj-smoke2`/`.raj-smoke3`
  do not exist in this tree, so the equivalent was checked against `.raj` and
  `.git`.
- **`search -hidden` reaches hidden paths.** `search -q 'refs/heads/main'` from
  the root returns nothing; with `-hidden` it returns `.git/HEAD` and
  `.git/logs/*`. Caveat: `search -path .git` already reached them without
  `-hidden`, because a walk's root is never subject to the hidden policy. That
  may be the intended override; it is filed as a decision in TODO.md.
- **A hidden file that `ls` lists can still refuse `read`.** A `.raj/logs/
  archive` log is listed by `ls -hidden` but `read` answers `no open buffer`
  and `search` finds nothing in it: it is the binary sniffer, and `ls` is a
  filesystem listing that does not sniff. Not a defect.
- **The diagnostics fix is real live.** A throwaway buffer at
  `reviewscratch/broken.go` holding a function that returns a string where an
  `int` is expected answered `status: ok` with the type error and its line
  number, not `ok` with an empty list. The scratch proposal was rejected and
  cleared and its tab closed; a `rmdir /work/reviewscratch` was proposed and the user approved it during the pass, so the scratch tree is gone. The specific
  versionless-publish branch is covered by `internal/app/diagnostics_test.go`
  (`TestDiagnosticsRejectsAPreSyncVersionlessPublish`,
  `TestDiagnosticsRejectsAPostSyncVersionlessPublishForAnEditedBuffer`, and the
  clean counterpart), which the live run could not reach: it needs a clean file
  on disk edited in the buffer, and an agent cannot save its own proposal.
- **Diagnostics of an unsaved multi-file edit compose only once every file is
  registered.** `lsp diagnostics internal/app/control.go` first reported
  `s.runSearch undefined` while the `internal/app/ls.go` buffer had not been
  sent to gopls; after `lsp diagnostics internal/app/ls.go` registered it, both
  reported `ok`. That is the buffer overlay working, but it is a driver gotcha:
  when validating a cross-file change before save, ask diagnostics about every
  file in the change, not just the first.
- **The `ls -h` usage note lied.** The per-verb usage emitted "with no path,
  targets the buffer the user is looking at" for every `[path]` verb, but `ls`
  defaults to the workspace root. Fixed in `internal/control/cli.go`.
- **`/tmp/opencode` is still unwritable** to the uid-501 driver, so the edit
  payloads went to `/tmp`. Already tracked.

## Symlink-guard review pass (2026-09-16, between-wave)

Read-only review of the pending symlink-guard wave: the container cannot build
or run, so these conclusions are from reading and from the brief's stated
falsification, not from a green test run.

- **The `apply -hunks` corruption report is disproved; the tool holds.** The
  symlink agent recorded, as tool friction, that `apply -hunks` corrupts
  non-overlapping hunks. A 2-hunk and a 12-hunk apply (line-internal
  replacements, ascending offsets, all base-relative) both produced
  byte-identical text to the expected result. The claim against the tool is
  withdrawn. The likely cause is hunks computed against a different base than
  the one they were submitted with: the editor rebases each hunk off `-base`,
  so offsets from an earlier read (or a hand-added shift off an earlier hunk)
  splice at the wrong byte while the reply still says "applied". Take offsets
  from the same `read -json` that supplies `-base`, or re-read between batches.
- **The pending `host.go` group is one coarse whole-file hunk.** Reconstructing
  the group's old/new text and diffing shows the net change is exactly the
  symlink guard (`SymlinkEscapeEnv`, `inRootResolved`/`resolveExistingPrefix`,
  the `canonical` pre/post check, `inRootDir`, `Open`, `Mkdir`, `CheckExec`,
  `claimPath`, `lspprep`) with no unrelated edits. It renders as one hunk
  because `DiffLines` degrades to a whole-file replacement past its `n*m` cap —
  filed in TODO.md.
- **`TestGuardRefusesSymlinkEscape` is only half-strong.** The `ls` assertion
  discriminates (without the change `memHost.Ls` returns a canned OK), but the
  `text`, `open` and `apply` assertions refuse for unrelated reasons with the
  fix absent (`memHost.Read` has no buffer; `memHost.Open` without `-create`
  errors; the `apply` read-gate fires), so they are vacuous. The discriminating
  coverage lives in `TestGuardSymlinkEscapeOptIn` (direct `canonical`, both env
  directions) and `TestGuardRefusesSymlinkParentEscape` (direct
  `inRootResolved` plus `open -create`/`claim`). The fixtures are real symlinks
  outside the root, so the state is genuine.
- **Dangling symlinks fall back to the lexical result.** `resolveExistingPrefix`
  cannot `EvalSymlinks` a link whose target is absent, so it treats the name as
  unborn and the in-root lexical check passes. `writeAtomic` also fails to
  resolve a dangling link and renames over the link in-root, so no write
  escapes today; the residual is a TOCTOU window if the outside target appears
  between the check and the save. Filed in TODO.md as a decision.
- **`internal/app/headless.go`'s comment described the old order.** Its doc
  said the Guard "resolves -- and so loads -- before it checks the root";
  `canonical` now pre-checks, so the comment was corrected to name the resolved
  guard check as the gate and `pathInRoot` as the lexical backstop for
  `loadHeadless`'s one caller.

## Advisory-lease + mid-line-merge review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor. These are the pass's
observations, the two doc corrections it proposes, and the decisions it
escalates.

- **The advisory lease is live and behaves as spec §12.1 says.** Two identities
  on a scratch buffer: A proposed `world -> socket`; B's `apply` over A's span
  succeeded (version 2 -> 3, no conflict), B's op opened its own group, and A's
  set reported `1 moved` with its surviving runs while `groups`/`diff` showed
  the overlap on both sets. A hunk catching both a Proposed and a Rejected run
  refused, naming the rejecting set (`change set 1 owns this text (author 4,
  bytes 0..6)`). A same-author amendment folded into the original set (2 ops,
  one group). Scratch buffer discarded; it was never on disk.
- **`exec -dir` is unreachable live over TCP.** `raj ctl exec -dir internal --
  true` answers the transport refusal (`exec is refused over TCP`) before
  `CheckExec` runs, so the relative-`-dir` refusal could not be reproduced over
  this transport. By reading: the live `CheckExec` calls
  `inRootResolved(dir)`, whose first act is `inRoot`'s "a path must be
  absolute", and the pending fix (`inRootDir`) joins the root first. The fix is
  right; only its live falsification is blocked by the TCP `exec` gate.
- **`Session.Leased`'s doc claimed the agent diff is refused.** It said "a
  pending or rejected span is atomic: an edit or an agent diff that intersects
  it is refused". The agent path now goes through `leaseSpan`/`rejectedLease`
  and a Proposed span is advisory, so the sentence was false. Corrected in a
  buffer to name the human typing path (`File.Insert`/`File.Delete`) as its
  consumers.
- **`TestControlGroupsReportLiveOverlaps` cited the old refusal.** Its comment
  said a direct session insert was the only way to reach the overlap "because
  ApplyDiff's lease refuses a hunk inside A's run". An `ApplyDiff` would land
  now and report the same overlap; the comment was corrected.
- **A wholly-overwritten Proposed set stays `proposed` with 0 hunks.** Live:
  after B overwrote all of A's inserted run, `groups` listed the set as
  `proposed ... 0 hunks 1 moved`, while `Pending` dropped it so a save is
  unblocked. `Invalid` (phase 1c) is the state that should name it; until then a
  listing cannot distinguish it from a set with surviving work.
- **`read` returns the view, not `AcceptedOnly`.** A rejected set's text still
  came back from plain `read`; `read -annotated` only added the state runs.
  TODO's decided item is `AcceptedOnly` by default (spec §6/§12.3), while the
  F3b-i note says "read = the buffer view with -annotated states". The two
  disagree and the implementation follows the latter. Escalated as the
  composition decision.
- **Mid-line merge is read-only verified.** The merge in `view.Build` absorbs a
  following kept piece only when `np.disp == merged`, `np.doc == docB`,
  `np.length > 0`, and no fold sits at `merged`; `[a,b)` is bounded to one
  composition line and a restore piece is emitted as its own row, so it cannot
  cross a fold, a restore or a newline. `checkProjectionInvariants` still
  asserts the full round trip, monotonicity and `RowText` reconstruction, so the
  oracle is not weakened, and `randomProjection` exercises adjacent kept pieces
  on every fuzz run.

## Phase 1c recomputed-`Invalid` review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor, two identities
(`review-p1c-01` author 3, `review-p1c-02` author 4) on a scratch `open
-create` buffer. The wave is saved; these are the review pass's observed
results, corrections and open questions.

- **Invalid is derived and clears with its collider, live.** A proposed
  `hello world\n` (set 1); B wholly overwrote it with `goodbye\n` (set 2).
  `groups -json` reported set 1 `"invalid":true` with
  `"invalid_by":{"group":2,"author":4,"start":0,"end":8}`, `hunks:0`,
  `moved:1`; set 1 was absent from `proposals` (`Pending`), and `read`
  returned `goodbye`. `reject`+`clear` of set 2 returned the text to
  `hello world` and set 1 to `hunks:1`, `invalid:false`, back in
  `proposals`. A partly-overwritten two-member set (one member consumed, one
  surviving) stayed `invalid:false` with `hunks:1 moved:1`, and stayed in
  `proposals`. Scratch buffers discarded; never on disk.
- **The predicate is one rule, live and read.** `markInvalid` decides
  survival with `len(projectMember(o)) > 0`, the same per-member projection
  `DiffPending`/`Pending` use to drop a set; `invalidBy` reuses
  `rebase(Pos, Pos+InsLen, Seq+1)` (the `rebasedInverseAt` walk) and
  `blockFor`, and returns false for a nil collider, so a wedge with no set
  stays invalid without a fabricated `InvalidBy`.
- **The wire field is sparse and forwards-compatible.** `hGroupInvalid`
  (0x5a) is a separate opcode emitted only when some group is invalid, one
  flag per group then (when set) `{group,author,start,end}`; `decodeHeader`
  merges it by position after reading every op, so ordering is not load-bearing
  and unknown fields are dropped (`prog.Decode(b, nil)`), not misread. Risk:
  an old reader drops the whole field and so shows an invalid set as valid --
  a display loss, never positional corruption, because it is not part of the
  `hGroups` record.
- **`read` docs corrected; three more stale claims found and fixed.** The
  wave fixed spec §12 decision 3 and the TODO "which composition does `read`
  return?" item. The same false "read defaults to `AcceptedOnly`" survived in
  spec §6 (consumers), spec §12.2 (resolved 2026-09-12), and TODO's Phase 1b
  description. All three are corrected in this pass; spec §5's
  `AcceptedOnly` definition also wrongly said it included `Proposed`.
  `app/control.go` `host.Read` returns `Project(Annotated)` and
  `Session().Version()`, so the corrected sentence and the coordinate argument
  hold.
- **`Project` does not consult `Invalid`.** For a wholly consumed insertion
  the bytes are gone from the session, so the edit composition is absent by
  construction, and `AcceptedOnly` excludes every `Proposed` set anyway. A
  deletion-only invalid member's `Del` bytes are also already applied to the
  session, so `AcceptedAndProposed` still applies its deletion rather than
  "excluding" it; that overlap is the undefined case, not an exclusion. Decide
  whether to wire `Invalid` into `included` or state the limit in the spec.
- **`review` and `proposals` cannot show an invalid set.** Both read
  `Pending()`, which drops it, so the human's review surface and the driver's
  rollup have no invalid row; `groups`/`diff` are the only surfaces. Decide
  whether the rollup should name it.
- **`markInvalid` adds a journal pass to `Groups()`.** It walks the journal
  and calls `projectMember` (two rebase walks) per live edit, so `Groups()`
  goes from O(ops) to O(ops^2); it is reached by `DiffPending`, `Pending`,
  `PendingOverlaps` and every `GroupDiff`, several of which are on the review
  and `groups` paths. Correctness is fine; measure before optimising, and note
  the existing `projection_bench_test.go` seam if it matters.
- **No app-layer unit test for the invalidation mapping.** `groups_test.go`
  (5 tests), `header_test.go` (`TestHeaderKeepsGroupInvalid`) and
  `cli_test.go` (`TestCLIGroupsShowsInvalid`) cover the session predicate, the
  wire and the CLI; the `host.Groups`/`host.Diff` piecetable-to-`control.Group`
  mapping is only live-verified. Note: `TestPartlyOverwrittenProposalSurvives`
  is a regression guard, not a feature test -- it passes without `markInvalid`;
  `TestInvalidWhollyOverwrittenProposal`, `TestInvalidClearsWhenColliderClears`
  and `TestHeaderKeepsGroupInvalid` fail without the change.

## Advisory-lease reporting (warnings) review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor, two identities
(`review-warn-01` author 5, `review-warn-02` author 6) on a scratch `open
-create` buffer that was closed with `-discard` and never reached disk. The
wave's reporting half is live and correct:

- **Overlap warns with group, author and span, live.** A proposed `hello
  world`; B's `apply` over part of A's run succeeded (`ok:true`, the version
  moved) and the reply carried
  `"warnings":[{"group":1,"author":5,"start":0,"end":11}]`; a second setup's
  text mode printed `note: overlapped change set 6 (author 5, bytes 0..3)`.
  The span is the superseded run's full bounds at the moment the hunk landed,
  not the intersection, which matches spec §12.1 "the span it held when the
  hunk landed".
- **Clean, refused and same-author stay distinct, live.** A clean insert at
  the superseded run's end carried no `warnings` key; an apply over a
  `Rejected` run returned only a `conflicts` record naming the owner and no
  warnings; a same-author amendment joined its own set (2 ops) and carried no
  warnings.
- **Wire: separate op, positional conflicts safe.** `hApplyWarnings` (0x5b)
  is a distinct argument-range op emitted after `hConflicts`/
  `hConflictLease`. An old reader's `decodeHeader` switch ignores an unknown
  argument code, so it drops the warnings whole while the positional
  `hConflicts` records still parse; `TestHeaderKeepsApplyWarnings` pins the
  coexistence. Risk: a new reader talking to an old server gets
  `Warnings == nil`, indistinguishable from a clean apply -- there is no
  header version negotiation to say "peer too old".
- **The CLI `-json` ordering fix is right, not hiding a bug.** Live:
  `apply <path> -base 1 -start 0 -end 0 -- -json x` exits 2 with "expected at
  most one replacement operand, got 2" -- after the `--` terminator `-json` is
  a positional. Putting `-json` before `--`, as the test now does, is correct.
- **`patch` silently dropped the warning, live (folded in this pass).**
  Against the pre-fix running binary, B's `patch` over A's proposed run landed
  (`patched snapshot 1 at version 2`) with no note in text mode, and
  `patch -json` returned `{"ok": true, "version": 4}` with no `warnings`;
  A's set went `1 moved` with no signal to B. `host.Patch` calls the same
  `File.ApplyDiff` and discarded the `[]Block`. Now `Patch` returns
  `[]control.GroupOverlap` through `BufferHost`/`Guard`/`memHost`/`Dispatch`,
  and the CLI shares `noteOverlaps` with `apply`. Pending host rebuild.
- **`host.Patch` re-proposed the last group on an all-refused patch (fixed).**
  Unlike `host.Apply`, it marked `LastGroup()` for any agent patch with a
  non-empty diff, even when every hunk conflicted and the version did not
  move; that can flip an accepted or rejected set back to `proposed`. Guarded
  with the same `Version() > before` check `Apply` uses. Pending host rebuild.
- **Mixed apply drops the warnings in text and JSON (CLI gap, filed).** One
  hunk over A's live run landed (a new group appeared and A's run split) while
  a second over a `Rejected` run was refused; the response held both, but the
  CLI printed only the conflict and `-json` emitted
  `{"ok":false,"conflicts":[...]}` with no `warnings`. `reportApply`/
  `patchCmd` return `reportConflicts` as soon as `len(res.Conflicts) > 0`,
  before the warning branch. The same shape makes `run` unable to show it:
  `apply` sets `Response.Err` on any conflict, `runProgram`'s `fail` stops on
  `Err`, and `collectAll` keeps only the final frame, so a program loses the
  warnings from every non-final apply verb.
- **Warnings are per-hunk and first-set.** `Session.ApplyDiff` appends one
  `Block` per landing hunk, so two hunks over the same set warn twice;
  `leaseSpan`/`rejectedLease` return only the *first* intersecting run, so a
  hunk that overlaps two different Proposed sets warns about one and silently
  moves the other. Deduping per set and reporting every intersected set is a
  content choice; escalated.
- **No mailbox notification to the superseded author.** Spec §7b says the
  occupying author "is **notified** (mailbox), never *summoned*"; the
  implementation appends a warning to the caller's reply only and enqueues
  nothing. Spec and code disagree; escalated.
- **`header.go`'s forward-compat comment names the wrong mechanism.** It says
  "nil is passed as the known set... so `prog.Decode` drops what it does not
  recognise"; with a nil `known`, `prog.Decode` returns every op and it is
  `decodeHeader`'s switch (which has no `default`) that ignores unknown codes.
  The conclusion is right; the comment points at the wrong function.
- **Test honesty.** The piecetable tests
  (`TestApplyDiffWarnsOverAnotherAuthorsProposal`, `...AmendDoesNotWarn`,
  `...CleanHasNoWarnings`, `...RefusalIsNotAWarning`) build their state with
  `groupSession` + `ApplyDiff` + `MarkGroup`/`RejectGroup`, exactly the state
  they assert, and fail without the third return. The wire test and the two
  Dispatch tests are canned wiring tests (they set the field and assert it
  round-trips), so they prove the plumbing, not the semantics; the new
  `TestDispatchPatchCarriesWarnings` and `TestCLIPatchNotesOverlapWarning`
  are the same shape. There is still no app-level two-identity test for the
  real `host.Patch` mapping (the `internal/app` gap already filed), and none
  for the all-refused `ProposeGroup` guard.

## Advisory-lease reporting completion review pass (2026-09-16, between-wave)

Live over the socket against the rebuilt editor, two identities
(`review-warn3-01` author 3, `review-warn3-B` author 4) on scratch
`open -create` buffers, all closed with `-discard` and none written to disk.
The wave's three standing decisions are implemented: the reporting half is
live and correct, and the mailbox half is live; the CLI *display* half could
not be exercised because the container client predates the wave.

- **Every intersected set, once each, live.** Two Proposed sets abutted at
  0..3 and 3..6 (authors 3 and 4); one hunk over [0,6) warned both, once
  each: `note: overlapped change set 1 (author 3, bytes 0..3)` and
  `note: overlapped change set 2 (author 4, bytes 3..6)`.
- **One set caught by two hunks warns once, live.** A set with two surviving
  runs at 0..4 and 8..12 (a `patch` opened one set over two disjoint lines,
  and a peer's run between them keeps `stateRuns` from merging them); one
  `apply -hunks` batch with a hunk over each run printed exactly one warning
  (`change set 2 ... bytes 0..4`), while `groups` showed `2 moved`. That is
  the end-to-end pin for the dedupe, and it matches
  `TestApplyDiffWarnsOnceForASetCaughtTwice`.
- **Mailbox notice, live.** The superseded author's `recv` returned
  `{"from":0,"text":"change set N (bytes s..e) in <path> was landed over by
  author M"}`; `from` is `AuthorOriginal`. A clean insert flush against a run
  and a same-author amendment produced none (`recv -wait 1s` exits 3). It
  fired from both `apply` and `patch`, and it fires for `edit` too — `edit`
  is `apply` on the wire — so "should edit notify?" is answered by the
  encoding rather than a separate decision.
- **The partial-refusal CLI display was NOT verifiable live: stale client.**
  A mixed batch (one hunk over a Rejected run, one over a Proposed run)
  landed the second hunk and refused the first; the container's
  `/usr/local/bin/raj` printed only the refusal and emitted
  `{"ok":false,"conflicts":[...]}` with no `warnings`. That is the pre-wave
  behaviour — the binary contains `note: overlapped change set` but not
  `was landed over by author` or `proposedSpans`, so it predates this wave.
  The server half is proven by consequence: the occupant's `recv` showed
  `change set 2 ... partial.txt ... author 3`, and `notifySuperseded` only
  posts when `Response.Warnings` is non-empty. The authoritative check for
  the CLI fix is the host `make check` on the two `cli_test.go` refusal
  tests; the live client must be rebuilt first.
- **The notice channel is immune to the `run`/program blind spot.** Because
  `notifySuperseded` is driven off `res.Warnings` per dispatched request
  (before any frame is chosen), a mixed apply still notifies the superseded
  author even though the program/CLI may drop the caller-side warning. A
  program that applies over a proposal notifies; it just cannot read back
  *why* until the run path keeps non-final frames.
- **`recv -json` timeout exits 0, not the documented 3.** Text mode
  `recv -wait 1s` exits 3 with no message, but `recv -wait 1s -json` prints
  `[]` and exits 0 (the cancelled branch returns `emit`'s code in the JSON
  case). A polling loop can read the empty array, so this is not fatal, but
  the comment claiming "3 means no message" is false in JSON mode.
- **The notice names the editor's path, not the caller's.** The driver worked
  `/work/review-warn3-scratch.txt`; the notice read
  `/Users/rajandavis/Desktop/projects/raj/review-warn3-scratch.txt` — the
  server composes `req.Path` after its own root mapping. Harmless to a human,
  confusing to a script.
- **A pathless apply/patch notice omits the path.** `notifySuperseded` reads
  `req.Path`; the buffer name the guard/resolver chose is not plumbed back,
  so `where` is empty for a request that named no path. The set, span and
  author still ride.
- **First-run vs bounding span is still a choice.** `proposedSpans` reports
  the first intersecting run's bounds per set, not the union of runs the hunk
  caught. The live dedupe warning named `bytes 0..4` while the set also held
  `8..12`. For "which set was superseded" the first run is enough; a bounding
  span is what a human would draw. Escalated before; still escalated.
- **Test honesty.** Semantic pins: `TestApplyDiffWarnsOverEveryProposedSet`
  and `TestApplyDiffWarnsOnceForASetCaughtTwice` (piecetable, modelled on the
  named lease sibling, each fails without the third return / the dedupe), and
  the real-host `TestControlApplyOverAProposalWarnsTheRealSet`,
  `TestControlPatchOverAProposalWarnsTheRealSet` and
  `TestControlSupersededAuthorIsNotified` (controlHarness, two identities,
  modelled on `TestControlLeaseRefusalCarriesTheGroup`). Canned plumbing
  only: the `memHost`/`Dispatch` tests (`TestDispatchApplyCarriesWarnings`,
  `TestDispatchPatchCarriesWarnings`) and the CLI tests. The CLI tests are
  honest pins of the CLI code path — the fake returns `Conflicts` and
  `Warnings` together, so the refusal tests fail without the change — but
  they prove the client logic, not the wire. No vacuous new test was found.
- **Still untested: the all-refused `ProposeGroup` guard.** `host.Patch`'s
  `Version() > before` guard is read-only-verified; no test drives a patch
  whose every hunk conflicts and asserts the previous set's state does not
  flip to proposed. The filed `controlHarness` test request survives.
- **`/tmp/opencode` is still root-owned 0755.** The skill's scratch dir was
  unwritable to uid 501; the `apply -hunks` fixture went to `/tmp` instead.
  Recurrence of the container-image item.
- **Own-set + other-set resolution flips with run order (a real
  inconsistency).** A hunk that spans the editing author's own Proposed set
  *and* another writer's resolves two ways by which run `leaseSpan` meets
  first. Live: B inserted at 0..3 and A at 3..6, then B's hunk over [0,6) was
  refused (`change set 1 owns this text (author 4, bytes 0..3)`); in the
  mirrored buffer (A's run first, multi2) the same shape landed and warned
  about both sets. The `ApplyDiff` comment says own+other "is not a clean
  amendment and still conflicts", so the mirrored case silently supersedes
  the author's own draft instead of refusing. No test pins own+other at all:
  `TestApplyDiffWarnsOverEveryProposedSet` uses two *other* authors and a
  third editor. Escalated: decide conflict-everywhere or
  land-and-warn-everywhere, make the two orderings agree, and pin it.

## Order-independence review pass (2026-09-16, between-wave)

Live over the socket against the rebuilt editor and the matching container
client, three identities (`review-order-01` author 4, `review-order-02` author
5, `review-order-03` author 6) on scratch `open -create` buffers, all closed
with `-discard` and none written to disk. The wave's order-independence fix is
live and correct; the pass removed two functions the fix orphaned and filed one
wording item.

- **Order independence is live in both directions.** Buffer A: own run at
  0..3 (author 4) then a peer's at 3..6 (author 5); author 4's hunk over [0,6)
  refused, naming set 1 / author 4 / bytes 0..3. Buffer B: the peer's run first
  at 0..3, own at 3..6; the same hunk refused, naming set 2 / author 4 / bytes
  3..6. Same outcome, own set named, version unchanged, no warnings.
- **Only-others lands and warns; wholly-own joins.** Buffer C (two peers,
  authors 5 and 6): author 4's hunk over both landed and warned both once each
  (groups 1 and 2), both sets going `0 hunks 1 moved` + invalid. Buffer D
  (author 4's own run): a second hunk wholly inside it joined the same set
  (2 ops) with no `warnings` key.
- **The mixed batch now exercises the CLI fix live.** Buffer E: a `Rejected`
  set at 0..3 and a `Proposed` set at 3..6 (both author 5); author 4's two-hunk
  batch refused the first and landed the second. Text mode exited 1 and printed
  `raj ctl apply: change set 1 owns this text ...` and
  `note: overlapped change set 2 (author 5, bytes 3..6)`; `-json` carried both
  `conflicts` and `warnings`; the superseded author's `recv` delivered the
  notice. This is the check the previous pass could not run for the stale
  client.
- **`recv -json` cancellation.** `recv -wait 500ms -json` exited 3 and printed
  `[]`, matching text mode; the notice text is
  `change set N (bytes s..e) was landed over by author M` -- no path.
- **All-refused patch.** A rejected set at 0..3, a whole-file dump taken
  before the rejection, then a patch into the rejected run: refused, version
  unchanged, and the set stayed `rejected` (not flipped).
- **`leaseSpan` and `leasedElsewhere` were both orphaned by the fix, not just
  the one the brief named.** After `ApplyDiff` moved to
  `proposedSpans`/`rejectedLease`, a tree-wide search found no production or
  test callers for either; `F3B-II-DESIGN.md:196` still named
  `leasedElsewhere`/`commitInto` as the future authority and the `Leased` doc
  comment still named `leaseSpan`. Removed both, repointed the design doc to
  `proposedSpans`/`rejectedLease`/`commitInto`, and dropped the dangling
  `leaseSpan` reference in `proposedSpans`' doc. `lsp diagnostics` clean on
  both files; the removals are in buffers pending the user's save and the next
  `make check`.
- **The own+other conflict message does not describe its own case.** The
  refused hunk's message is the generic lease sentence `change set N owns this
  text (author A, bytes s..e); accept or reject it first`, but the set it names
  is the *writer's own*; the driver cannot accept its own draft, and the
  actionable fix (narrow the hunk off the peer's text) is not stated.
  `Conflict` carries no marker distinguishing own+other from a genuine peer
  lease. Filed as a content decision.
- **The all-refused patch test's discriminating assertion is the second one.**
  Without the `Version() > before` guard, `Begin` has already reserved a new
  group id and `ProposeGroup(LastGroup())` marks *that* empty group proposed,
  not the prior set; the prior set stays rejected either way. The test's first
  assertion (`prior == Rejected`) therefore passes with and without the guard;
  only `GroupState(LastGroup()) != Proposed` fails when the guard is absent.
  The name and comment ("repropose the prior set") overstate what flips. The
  test is honest, not vacuous; the fixture description should name the phantom
  group.
- **No other vacuous new test.** `TestApplyDiffRefusesOwnAndOtherProposalEitherOrder`
  really builds both projection orders (own at 0..3, met first, in one subtest
  and own at 3..6, met second, in the other) and fails without the change in
  the second; `TestCLIRecvJSONTimeoutExitsThree` drives the real server's
  cancelled frame; `TestControlSupersededNoticeShipsNoPath` asserts the message
  contains no `/`.


