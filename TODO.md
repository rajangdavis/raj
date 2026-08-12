# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md.

## Panes and fields

- [ ] **Horizontal scroll in the explorer.** The selected path is now spelled
  out on the pane's last row, which answers "which file is this" but not "what
  is the rest of this name" for the rows around it. An offset that follows the
  selection is the other half; it needs a rule for when it returns to zero, or
  the tree jitters sideways as you arrow through mixed depths.
- [ ] Small and split panes, and how they resize.

## Editor

- [ ] **Mouse: everything except the wheel.** Click to position, drag to select,
  cmd-click for an extra cursor, click a tab to switch, click a file in the
  explorer or search results to open it (refusing binaries loudly rather than in
  the status line). The reporting, the decoder and the position routing all
  landed with the wheel, so what is left is mapping a cell to a document offset
  and back. Drag needs DECSET 1002 as well, which is why it was left out: motion
  reports arrive on every cell the pointer crosses and are pure traffic until
  something consumes them.
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

  Order: ~~position mapping, fuzzed~~ and ~~JSON-RPC framing~~ are done →
  **process lifecycle next** (spawn, initialize handshake, shutdown, and the
  three failure modes that matter: a server that crashes, one that hangs, and
  one that takes ten seconds to index before answering anything) → document
  synchronisation → hover → definition → diagnostics → completion.

  Lifecycle is where the design decisions are, not the protocol. A request has
  to be cancellable, because the answer to a hover is worthless once the cursor
  has moved — the search pane already solved this shape with a context per
  generation, and it should be reused rather than reinvented. A server that
  dies must degrade to no language features rather than to a broken editor, and
  restarting it cannot be automatic without a backoff, or a server that crashes
  on startup becomes a fork bomb.

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
- [ ] **Completion rescans every open buffer on every keystroke.** Measured at
  5.3 ms against 2 MB of open buffers, which is a third of a frame on the
  keystroke path — the same path retokenising is deliberately kept off. The scan
  itself is fine (301 MB/s); rescanning buffers that have not changed is the
  waste. Cache the word set per buffer keyed on `Session.Version()`, which is
  the same key the LSP work will need for `textDocument/didChange`, so the two
  want the same thing.
- [ ] **Completion has no trigger key.** It appears on its own after two
  characters and cannot be summoned deliberately, so there is no way to ask for
  it after a cursor move or with a one-character prefix. `ctrl+space` is the
  conventional chord and is currently decoded but unbound.
- [ ] **Highlight the matching bracket under the cursor.** Auto-pairing landed;
  showing which bracket closes the one you are on did not. It is a render
  concern rather than an edit one — find the partner by counting depth outward
  from the cursor, and tint both cells. The scan has to stop somewhere on an
  unbalanced file, and a bracket inside a string or a comment will be counted,
  which is the same blind spot the symbol scanner has and has the same fix.
- [ ] **Auto-indent does not read the language.** A newline carries the
  previous line's whitespace, and a newline between brackets opens a block. It
  does not add a level after `if x {` typed without a closer, and does not
  outdent a line beginning with `}`. Both need to know what the line means, not
  just what it starts with.
- [ ] **Blinking secondary carets.** The real caret blinks because the terminal
  blinks it; a drawn one would need raj to redraw on a timer, which means a tick
  fast enough to be a blink and a dirty-region pass small enough that blinking
  costs one cell rather than a frame. Nice, and a long way down: the tick is
  150 ms today and exists for idle work.
- [ ] Tabs as clickable tags — visual now, clickable once the mouse lands.

## Workspace

- [ ] **Save-as is a bare text field.** No completion, no directory listing, and
  a path whose parent does not exist fails with the raw `os.WriteFile` error
  rather than offering to create it. The picker one chord away already holds a
  fuzzy index of every file in the tree; pointing the save-as field at the same
  index would make it a real file dialog rather than a prompt with a default.

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
- [ ] **Skip known-binary extensions before opening.** Around 17 MB of ghostty
  under the size cap is fonts and images, each costing an open and a 64 KB read
  to discover a NUL byte. Cheap, but measure it rather than assuming: the glob
  finding in BENCHMARKS.md is a standing reminder that a filter can cost more
  than the read it avoids.

## Buffer

- [ ] **Compaction.** Merge adjacent same-author pieces; only flatten spans that
  are both saved and committed.
- [ ] **16 ms coalescing window** for streaming agent hunks.

## Agents (deliberately last)

- [ ] Agent pane and the plumbing from a model's diff to `Session.ApplyDiff`.
- [ ] Region leases to prevent conflicts rather than only detect them.
- [ ] SQLite session store — the op log as the shareable, forkable artifact.

## Known rough edges

- [ ] **Resize still drops events.** `NativeHost.emit` discards on a full
  channel, which a drag burst produces. `Present` reads the true size every
  frame so the picture stays correct, but a dropped event means no redraw is
  triggered until the 150 ms tick — a lag rather than garbage.
- [ ] Resize has no test coverage beyond `Present`'s size guard.
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
  from the terminal for nothing: `ToggleAgent` (cmd+alt+b) and `CommandPalette`
  (cmd+shift+p). `GotoLine` and `GotoSymbol` were the other two.
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
- [ ] **Document what is supported** — a keybinding table generated from
  `keys.Bindings` so it cannot drift, with the unimplemented actions marked.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
