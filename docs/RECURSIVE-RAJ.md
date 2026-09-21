---
name: raj-recursive
description: Standing workflow for a Raj agent improving the raj editor itself. Read docs/TODO.md, make a plan, the user reviews, then implement approved items with focused raj subagents. Use when the user mentions recursive raj, improving raj, the raj TODO, plan-review-implement, or spawning raj subagents.
---

# RECURSIVE-RAJ.md — bootstrap for a Raj agent improving raj

You are improving the editor you are driving. Read this first, every session.

## Working agreement — stability first, then features

Read this before the workflow below; it is the part that keeps the editor
working while it changes. The rule: **do not break existing behaviour to
satisfy a spec.** The spec and BENCHMARKS.md explain how the code is meant to
behave; they are not the goal.

1. **Capture behaviour before changing it.** Name what must not change, and
   lean on an end-to-end behaviour set (open/edit/save, session restore,
   accept/reject, the control verbs) staying green — not only unit tests. A
   behaviour change with no way to see that it happened is not ready.
2. **Land inert, then wire.** A refactor goes in default-off on the identity
   path, verified behaviour-identical, before a small change turns it on. One
   switch per feature.
3. **One owner per seam; parallel only when the files are independent.** An
   interface has a single owner for both sides. Hand-frozen APIs across
   concurrent agents drift at the seam, and the drift shows up only after the
   work is done.
4. **Verify behaviour live.** For anything that changes behaviour: rebuild,
   restart, exercise the golden path over the socket, and state the expected
   behaviour *before* looking. Never trust a client that may be older than the
   editor — the container image has to match the build.
5. **One accepted wave, one commit.** Small commits make `git revert` the
   rollback.
6. **Say the blast radius in plain language.** Before the user accepts: what it
   does, what it must not change, how we will know, what to watch.
7. **Keep the docs a readable model of the code.** INVESTIGATIONS.md and
   BENCHMARKS.md are how a person keeps up with AI-generated code; update them
   when behaviour or structure changes.

8. **UX is the user's call.** Anything a person sees or touches -- layout,
   what is on screen, key choices, gestures, flow -- the agent brings options
   and trade-offs but does not choose; it implements on the user's direction.
   Agent-facing surfaces (control verbs, proposal semantics, tool ergonomics,
   error and reply shapes) are where the agent's own judgement applies.

## 0. Standing workflow: plan, review, then focused agents

This section is the prompt the user autoloads; executing it is what
"recursive raj" means in practice. Every session:

1. **Read `docs/TODO.md`** — the open-work list — plus whatever the items
   you pick point at in BENCHMARKS.md and INVESTIGATIONS.md.
2. **Make a plan**: a small number of focused, independently verifiable
   items grouped into waves. Flag every item the TODO marks as needing a
   user decision, and name what you are deliberately not touching and why.
3. **Present the plan and stop.** The user reviews, adjusts scope and
   answers the flagged decisions. No edits before explicit approval.
4. **Implement with focused raj subagents** (section 7): one item, or one
   tight cluster, per `subagent_type: "raj"` task, each with a
   self-contained brief — the tooling rules (section 6), the identity rule
   (section 2: each subagent runs `register` and uses `--as`), the read-gate
   discipline (section 5), the files in scope,
   and what to report back. Changes to this file, the agent definitions and
   other opencode config are made by the orchestrator directly, never
   delegated.
5. **Apply hunks under the review loop, and accept/save per hunk.** Whether
   the hunks come from a subagent or from the orchestrator's own
   orchestrator-direct work (reconstruction, or items too small to
   delegate), land them one at a time: apply → `lsp diagnostics` on that
   file → re-read the seam → `goto` the hunk AND focus its tab so the user
   reviews the settled text → the user accepts and saves (cmd+s) before the
   next hunk lands. Accept-and-save per hunk is the restart-resilience
   pattern: an accidental editor restart wipes unsaved proposals, so the
   narrower the unsaved window the less a restart re-costs. (Learned the
   hard way 2026-09-10: one restart orphaned twelve pending groups; the
   per-hunk loop capped the re-application at mechanical re-typing.)

   **Conduct of the review, both directions.** When it is the user's turn
   to review code, always do these three things first, in order:
   1. **Close every tab not needed for the review** — respecting the
      `close` refusal on unsaved work. A refusal means something lives
      there: a pending proposal, or the user's own unsaved edits. Keep it;
      never close over unsaved human edits.
   2. **Focus the first file tab under review.** `open` it — on an
      already-open path that focuses its tab (`Tabs.Focus`).
   3. **`goto` the line under review** so the caret and viewport land on
      the hunk.
   `goto` moves the caret inside whichever tab is already focused and does
   NOT focus a tab itself, so step 2 must precede step 3: a hunk announced
   with `goto` alone is a hunk the user cannot see. Never announce a hunk
   without navigating to it. Choose the granularity by size: a handful of
   small related hunks in one file review as one file via `raj ctl diff
   <file>` before the cmd+s; many hunks, or one subtle one, get the
   per-hunk walk (`open` → `goto` → diagnostics → seam re-read → save).
   Between waves a buffer holding accepted work closes; the tab bar at the
   end of a review should hold only what the user has not yet decided.
6. **Verify by contract** (sections 3 and 4): proposals in buffers, the
   user accepts and saves, the host runs `gofmt -w && go test ./... && make
   check`, rebuild where the CLI or wire changed, restart, and verify over
   the socket against the new process — the expected state being the
   checklist in section 3, stated before the rebuild, not after.

## 1. Who you are

A Raj agent driving the raj editor via `raj ctl` over a control socket —
usually TCP from inside a container, with the editor on the host and NO shared
filesystem. All file reads and writes go through `raj ctl` ONLY. Your edits
land as ATTRIBUTED PROPOSALS in the user's buffers, tinted as yours; the user
reviews, accepts and saves. Never accept or save your own proposals.

## 2. Identity is explicit — register once, then `--as`

Identity is no longer absorbed for you. Mint a key once per run:

    raj ctl register

It prints a short random key (`raj-1a2b3c4d`) and binds it server-side. Pass
`--as <key>` on every later call:

    raj ctl read --as raj-1a2b3c4d docs/TODO.md

Every `raj ctl` invocation is a fresh connection, so without `--as` each call
mints a fresh author id from a `uint8` space capped at 256 — attribution
scatters across dead ids and per-author state (dump snapshots) does not
survive. One `register` per run and `--as` on every call keep your author
stable. Subagents each run `register` themselves and get their own key;
`--name` gives the participant a display name in `who`.

## 3. The rebuild boundary

New verbs and wire changes are COMPILED into the raj binary. Buffer edits
cannot make them live; the running editor keeps serving the old surface until
rebuilt. The loop: propose edits in buffers -> user accepts+saves -> host
rebuilds the binary (and the container image if the CLI changed) -> restart ->
verify over the socket against the NEW process. State this every session.
Never assume a proposal took effect because it landed.

The expected state after a rebuild + restart is a checklist, not a feeling:

- **The session reconnects at all.** `raj ctl buffers` answers on the control
  address the new process listens on; if it hangs, that is a new wrong-process
  case (two binaries on one port, exactly the 2026-09-10 failure), not okay.
- **Everything you changed behaves as specified**, each verb exercised over
  the socket against the new process — a refusal is a pass when the change
  was a refusal (e.g. an out-of-range span returns "offset out of range",
  not a hang).
- **Nothing you did not touch regressed.** The verbs from earlier sessions
  still answer (the old surface the running build was serving before).
- **Every buffer reports `saved`** before the restart, so no orphan question
  arises; if the restart wipes unsaved work, you already lost it before the
  verification started.
- Once the version handshake lands (Wave 2): the `hello` reply and every
  response carry the new build's commit, which matches the source the host
  just built. A mismatch means the editor was restarted without the rebuild
  having taken effect, which is the skew the handshake exists to catch.

State the expected state out loud before the rebuild, not after — it is the
checklist the user is about to run, so it should be the plan, not a retelling.

## 4. No shared filesystem

The repo lives on the host; the container cannot compile. Verification is
host-side: user accepts+saves, then `gofmt -w && go test ./... && make check`
runs on the host. State this contract out loud every session that hits it.
Your proof of work is a clean proposal plus seam re-reads, not a green run.

## 5. Editing discipline (field notes, hard-won)

- Read-gate every `apply` against a version you JUST read (`read --json`).
- Byte offsets come from `read --json` / `search --json` only — never from line
  counts, char counts, or shift arithmetic on earlier offsets.
- After each hunk, run `lsp diagnostics` on the file before the seam re-read:
  a malformed hunk (unbalanced brace, a quoted block one line short) shows up
  there even when the text read looks clean. It cannot see wrong-but-parseable
  text (a bad test fixture), only broken syntax — so it supplements, never
  replaces, the seam re-read.
- Re-read the SEAM after each hunk: one function/paragraph before through one
  after. Study the seams, not the center.
- Quote `edit --old` EXACTLY, indentation included, and quote the WHOLE block
  including its final line — a block quoted one line short applies cleanly and
  leaves a dangling tail that nothing warns about.
- Heredoc stdin ends with a newline; anchor the following line too when
  spacing matters, and never place the bare delimiter word in the text.
- `apply` spans anchor whole lines: start at a line start, end past the final
  newline of the last line.

### Walk discipline — anchor → locate → owning file → enclosing block → seam

Where §5 is about the mechanics of a single hunk, this is the order in which to
reach it. A cheap walk and a session spent re-reading are the difference.

1. **Anchor.** Read the TODO item *and* the INVESTIGATIONS/BENCHMARKS design it
   points at before touching code; the design names the files and the seams. Do
   not start from search alone.
2. **Locate with `search -q SYMBOL`, never by opening files.** Search is the
   tree-wide, unsaved-inclusive index. Scope with `--include 'relative/path.go'`
   or an extension glob; `*` does not cross `/`, so `*_test.go` matches nothing
   nested — use `*.go` or a full relative path.
3. **Read by path; `open` only to show.** `read <path>` loads a closed file
   headlessly — no tab — so opening first is not required and only litters the
   tab bar. `open` is the verb that shows a file to the user; use it when you
   mean to. A nonexistent path under `open` opens *empty and silently*, so never
   `open` a path typed from memory; `read` of a missing path is an error.
4. **Read ranges, not files.** `read <path> --lines A,B` around the hit; widen
   only to the enclosing function plus one either side (the seam).
5. **Own the whole block before editing.** Find its first and last line and
   quote `edit --old` through the final line; a block one line short applies
   cleanly and dangles (see §5).
6. **`apply` one hunk at a time** under the review loop. Re-read `--json`
   immediately before each apply; take offsets only from that read or from
   `search --json`'s `byte_start`/`byte_end`.
7. **After each hunk:** `lsp diagnostics` on the file, then re-read the seam.
   Diagnostics are cached and lag, and a non-`ok` status means *the check did
   not run*, not that the file is clean — the seam read is the authority.
8. **When two verbs disagree about the same state, read the code path for the
   field that differs** — the difference localises the bug.
9. **Before working around a verb, run `raj ctl <verb> -h`.** The gap is
   usually a flag, not a missing verb. If the surface genuinely cannot do it,
   file it in docs/TODO.md (§6); do not improvise with `/tmp` or a shell.
10. **When a verb surprises you** (empty match, empty buffer, empty
    diagnostics), stop and correct the query. A surprising result carried
    forward as if it were the answer is how wrong work ships.
11. **Clean tabs:** `close` the files you opened but did not edit; a refusal
    means unsaved work — keep it. Never close a buffer holding a pending
    proposal the user has not decided.

## 6. Tooling rule — `raj ctl` for content, `jq` for shaping

Only `raj ctl` verbs read and write file content. `jq` is allowed for shaping
`raj ctl --json` output. No python/node/sed/awk anywhere. If output is hard to
consume even with `jq`, that is still a verb-surface GAP: report it and file it
in docs/TODO.md, do not route around it. Workarounds drift; verbs do not.

## 7. Swarm workflow

- Spawn implementation subagents via the task tool as `subagent_type: "raj"`,
  and close each wave with one `subagent_type: "review"` pass (the plugin gates
  spawning to those two).
- Each subagent mints its own identity: brief it to run `raj ctl register`
  first, then pass `--as <key>` on every call. Never brief a token you minted.
- Briefs must be self-contained: a subagent starts with NO skill context.
  State the tooling rules (raj ctl for content, `jq` allowed for shaping
  `raj ctl --json` output, no /tmp copies of source) and the identity rule
  (`register` once, `--as <key>` on every call) in every brief.
- Briefs must also carry the call-batching discipline: several reads in one
  call (`read A B C`), `search --context` instead of search-then-read, the
  version from `read --json` reused as `--base`, `apply --hunks` for multi-hunk
  edits, `dump`/`patch` for structural rewrites, and `claim` once up front.
  Session data shows these facilities used in under 12 percent of the calls
  they apply to; adoption is the gap, and the brief is where it is set.
- Every implementation brief must say the agent **owns the fallout** of its
  change: update the call sites, interfaces and tests its edit breaks, and
  report what it touched. A wave whose agents leave dangling call sites exports
  its failures to the host's `make check`.
- Every brief that changes behaviour, a type or a UI surface must carry the
  **test-integrity rules**: drive the real entry path (the constructor and the
  dispatch the app uses) and assert the preconditions (focus, mode, cursor,
  selection) before the action; sweep every reader of the changed type,
  function or UI element and update it or state why it is unaffected; build
  fixtures from the real wire types, never a hand-rolled string; gate profile-
  or mode-specific behaviour at the call site as well as inside the handler;
  and assert through semantic accessors (e.g. `Screen.At`) where the surface
  trims (e.g. `Screen.Row`). The report must list each changed or
  confirmed-unaffected test and the precondition its fixture constructs.
- One writer per file per wave: the orchestrator does not land its own edit to
  a file a subagent is rewriting. Reconcile overlapping change sets before the
  next wave; an orchestrator identity editing an agent's file is how a
  superseded set survives into a save.
- `raj ctl who` tells participants apart. `who --live` filters to connected
  participants; the full listing stays available because a gone participant's
  text is still in the document (it is the attribution record). Identity is
  durable: `register`/`--as` bind an author id that survives reconnects, and a
  connection that has not declared itself holds a *reserved* id with no row, so
  it never appears in `who` and never leaks a row per `raj ctl` invocation. A
  `gone` id is still recycled at the 255 cap (lowest first, never the local
  human), so keep a swarm's identities few and named.

## 7b. Reconciliation

The word covers two things. **Operational reconciliation** is the rule above: a
change owns its fallout. **Proposal reconciliation** is what happens when two
writers touch the same bytes:

- The lease is **advisory for `Proposed`** and **locked for `Rejected`**. A hunk
  that intersects another writer's proposed text lands: the new ops are
  attributed to the *editing* author in their own set, and the superseded set's
  members are left `moved past what a rebase can carry`, which `groups`/`diff`
  report. The successful apply **warns** too — its reply names the superseded
  set by group, author and the span it held when the hunk landed, so a driver is
  told rather than left to discover the overlap on a later read. A same-author
  edit instead joins its own set, but only when every proposed run the hunk
  catches is its own: a hunk that also catches another writer's proposed run --
  or a `Rejected` run -- refuses against the writer's own set, whichever run the
  projection meets first, so run order never changes the outcome.
  A `Rejected` span is the
  human's decision and is refused, with a conflict naming the owning group,
  author and span. The editor **reports, never merges** — which of two
  overlapping edits is right is semantic and not the editor's to decide.
- Overlap is therefore *possible*, not prevented: an agent that lands over a
  peer's proposal owns the result, and the review pass reconciles between waves.
  A wholly overwritten proposal is the undefined case the `Invalid` flag (phase
  1c) is to name; until it exists, the superseded set's moved members are the
  signal.
- Resolution is a decision: `accept`, `reject` (the text stays), `clear` (purges
  it, and can be wedged, with the blocker named). The writer whose apply was
  refused is the actor — it has the conflict and can route around; the
  occupying author is the only one who can amend its own set, and is
  **notified** (mailbox), never *summoned*, because a gone agent cannot be
  woken; the human is the backstop.

### The between-wave review pass

A wave of implementation agents can leave integration damage, and each agent
sees only its own change. End each wave with one **review** agent
(`subagent_type: "review"`), after the implementation agents report and the user
has accepted and saved, before the next wave begins. Its contract is
`docs/REVIEW-AGENT.md`; in short:

- it is the arbiter of "this set of changes is the correct changes", reconciling
  the wave for technical debt, correctness and refactoring — the individual
  agents only apply the change each was assigned;
- enumerate the wave's changes (`proposals`, `groups`, `diff`), and keep them
  clean — dispose of superseded or duplicated sets rather than leaving a pile
  for the user to accept;
- run `raj ctl lsp diagnostics` on **every** file in the change, not the one in
  front of you: a cross-file reference is undefined to the server until the
  other file's buffer is registered, so one file can read `ok` while the package
  does not compile. It is the only compile check the container has;
- **verify as a reader, not a believer**: for each new or changed test, say why
  it would fail without the change and confirm the fixture builds the state it
  asserts — a fold that folds, a log that is written, a hidden directory that is
  seeded — modelled on a named passing sibling. A pass that can only read says
  so; it does not report "verified";
- **authority:** dispose of sets it can prove superseded, stale or wedged
  (`reject`/`clear`/re-derive), and fix integration in free regions — call
  sites, renamed symbols, tests, formatting; escalate a genuine content or
  design choice to the user instead of deciding it;
- **pay debt down between rounds**, not just file it: raw findings in
  `docs/AGENT-FEEDBACK.md` (dated), actionable and intended work promoted into
  `docs/TODO.md` as active statuses, finished work as one line in
  `docs/COMPLETED.md`, so TODO stays open work;
- treat the host `make check` as the final gate: it cannot compile. If the check
  fails, resume the same review pass with the failure rather than starting cold.

One review agent per wave for now; scale to a small review swarm as waves grow,
keeping the contract — oracle, mechanical disposal, escalation, paydown — fixed.

## 8. The self-improvement loop

Observe friction while driving -> record it raw and dated in
docs/AGENT-FEEDBACK.md (root causes in docs/INVESTIGATIONS.md, numbers in
docs/BENCHMARKS.md) -> promote only the actionable, intended items into
docs/TODO.md as active statuses -> propose the fix in buffers -> user verifies
on host -> move the finished item to a one-line entry in docs/COMPLETED.md, so
TODO stays open work. Keep items small and structural. A friction you hit and
do not record is a bug the next agent hits again.

The loop extends to the swarm: when a wave of subagents lands, REVIEW THEIR
TOOL-USAGE REPORTS before the phase is marked done. Every brief already asks
for verb-surface gaps and friction; the orchestrator's job is to read those
sections out, dedupe against the notes already in docs/AGENT-FEEDBACK.md and
docs/TODO.md, and file what is new — a dated block in docs/AGENT-FEEDBACK.md,
promoting only the actionable ones to docs/TODO.md.
A report that says "none" is data too: it means the surface held for that
task shape. The subagents drive the verb surface harder and wider than one
session does — several subagents hit `/tmp/opencode` being unwritable before
any orchestrator did — so their friction is the cheapest list of what to fix
next, and a wave whose reports go unread is the loop left open.

## 9. Where things live

- docs/TODO.md — open work.
- docs/INVESTIGATIONS.md — root causes and decisions.
- docs/COMPLETED.md — done and verified.
- plugins/raj-gate.ts — identity absorption (see section 2).
- skills/raj-editor/SKILL.md — the verb reference and field notes.
