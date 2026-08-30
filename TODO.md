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

- [ ] **Session persistence** — tabs, cursors, scroll, sidebar state, expanded
  directories, and the focused pane, so a returning session lands where it was
  left rather than in the explorer. `.git/raj/session.json`, `--no-restore`.
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

## Agents (deliberately last)

- [ ] Agent pane and the plumbing from a model's diff to `Session.ApplyDiff`.
- [ ] Region leases to prevent conflicts rather than only detect them.
- [ ] SQLite session store — the op log as the shareable, forkable artifact.

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
- [ ] **Three actions are bound but unimplemented**, so their chords are taken
  from the terminal for nothing: `ToggleAgent` (cmd+alt+b), `CommandPalette`
  (cmd+shift+p) and `CursorUndo` (cmd+u). `GotoLine` and `GotoSymbol` were
  among the others and are done.

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
  tab. Under the `kkp_on` gate it is claimed only while raj is focused, so
  Ghostty's own cmd+n is untouched everywhere else; under the iTerm2 profile it
  is claimed for the whole window, which is the same trade the profile already
  makes for cmd+w.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
