# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md.

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
  single-line, so the position fuzzing wants extending to ranges first, which is
  the same prerequisite incremental sync has.
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
- [ ] **Document sync is whole-document, not incremental.** Every change sends
  the whole file, which cannot desynchronise by construction but costs the file
  size per change. Incremental sync needs the edit ranges in UTF-16 for every
  edit since the last notification, and one wrong range desynchronises the
  server's copy silently and permanently — so it is worth doing only with the
  position fuzzing extended to cover ranges, not just points.
- [ ] **Auto-indent knows brackets and nothing else.** Adding a level after an
  unclosed opener and lining up a closer covers C-family languages and leaves
  out everything indented another way: Python's colon, Ruby's `do`/`end`, YAML,
  a `case` inside a `switch`. Each is a per-language rule, and the token class
  the lexer gives us says what a token IS but not what it MEANS — a keyword is
  a keyword whether or not it opens a block. This is where a real per-language
  table starts, and it should wait until something needs it rather than being
  guessed at from one language.
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
- [ ] **Scroll is restored as a line number, not a proportion.** Reopening in a
  differently sized terminal clamps rather than adapts, so the cursor can land
  off screen until the first movement.
- [ ] **Dirty-buffer restore** — persist the journal and add-buffers, validated
  by an orig-hash per buffer.
- [ ] **Attribution across restarts** — tint is commit-scoped, so it must
  outlive the process.
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

## Buffer

- [ ] **Compaction.** Merge adjacent same-author pieces; only flatten spans that
  are both saved and committed.
- [ ] **16 ms coalescing window** for streaming agent hunks.

## Control socket (replaces the in-editor agent)

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
mandatory base version~~ and ~~off by default~~ are done: `internal/control` is
the transport, `internal/app/control.go` is what a request means, and the two
cannot be collapsed because the transport package has no editor types to reach
for. What remains:

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
- [ ] **A rejected group can be wedged.** If a later edit overlaps it, the
  members cannot be rebased out and the whole thing rolls back — correctly, but
  the caller is told only that it failed. It should be told what overlapped, so
  it can re-propose against the current text instead of guessing.
- [ ] **Groups have no ranges.** The listing reports ops and net bytes, not
  where. Rendering needs current-coordinate ranges, which means rebasing each
  member forward — the same walk the gutter will need.
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
  the notifications item above.
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
- [ ] **`exec` over TCP is refused, not sandboxed.** The refusal is correct —
  the command would run on the editor's machine, outside the container the
  driver was put in — and it costs the staleness check, which is the one thing
  `exec` was for. A driver running tests in its own sandbox has no way to be
  told it is testing files that do not match the buffers. `buffers` answers it
  with a second round trip and nothing prompts the driver to make one.
~~A stale socket from a killed process is only probed, not reaped~~ — discovery
  now removes what it finds dead. A unix socket with no listener refuses
  immediately, so a live but busy editor is never reaped.
- [ ] **Region leases.** `apply` rejects a stale hunk after the fact; a lease
  would stop the user and a driver being told they both own a span in the first
  place. The conflict report carries the version that invalidated the range, so
  the information a lease needs is already on the wire.
- [ ] **No notifications.** The protocol is request/response only, so a driver
  wanting to know the user has typed must poll `buffers`. A subscribe op would
  need the event thread to push, which is the one direction the park-and-reply
  shape does not cover.
- [ ] **`apply` cannot create or reach an unopened file.** `open` puts a path in
  a tab first, which also puts it in front of the user — deliberately, since an
  editor silently editing files you cannot see is worse than one extra call.
  Unnamed buffers stay unaddressable: there is no name to ask for.
- [ ] **SQLite session store** — the op log as the shareable, forkable artifact.
  Unchanged by the socket, and the socket makes it more useful rather than less.

## Known rough edges

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
- [ ] **Two actions are bound but unimplemented**, so their chords are taken
  from the terminal for nothing: `CommandPalette` (cmd+shift+p) and `CursorUndo`
  (cmd+u). `GotoLine` and `GotoSymbol` were among the others and are done.
  `ToggleAgent` was a third; it was removed rather than implemented, so
  cmd+alt+b goes back to the terminal.

  They are no longer invisible: `keys.Unimplemented` lists them, KEYBINDINGS.md
  marks them, and a test in internal/app presses each one and fails if anything
  handles it — so implementing one without unlisting it breaks the build, and
  so does binding a fourth without noticing. `CursorUndo` was found that way
  rather than by anyone noticing.
- [ ] **Profile switching in iTerm2 is not clean.** raj switches profile on
  entry with OSC 1337 and restores on exit, but installing the profile is still
  a manual step and the switch is visible. Autoloading — write the generated
  profile into `DynamicProfiles/` on first run if it is absent or stale, keyed
  off a hash of `Bindings` — would remove the setup step and keep the profile
  from drifting when the table changes. Deferred: what is there works.
- [ ] `raj --config ghostty` must be regenerated and the terminal reloaded
  whenever the binding table changes. Same for the iTerm2 profile. **Outstanding
  now**: cmd+n was added to `Bindings`, so both configs are stale until they are
  regenerated, and until then the chord opens a Ghostty window rather than a raj
  tab. cmd+k was added and cmd+alt+b removed for the same reason, so both are
  outstanding too — and until the config is regenerated cmd+k still clears the
  terminal's scrollback. Under the `kkp_on` gate it is claimed only while raj is focused, so
  Ghostty's own cmd+n is untouched everywhere else; under the iTerm2 profile it
  is claimed for the whole window, which is the same trade the profile already
  makes for cmd+w.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
