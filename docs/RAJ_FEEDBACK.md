# RAJ Feedback — raw agent notes

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
RECURSIVE_RAJ §8 step — deduped against the notes above; "none reported"
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
repeated here. "Walk discipline" now lives in RECURSIVE_RAJ §5 (anchor →
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
