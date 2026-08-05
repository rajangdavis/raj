# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md.

## Highest priority

- [ ] **Rebase counts a resurrected op in the wrong coordinate frame.** The
  narrower half of the rune-splitting bug is fixed; this is what is left, and it
  can still put a reversal at an offset that splits a rune. `Session.rebase`
  skips ops that are not live, but an op's `Pos` belongs to the version it was
  applied at, and redo can resurrect an op that was absent when later ops were
  recorded — so the walk shifts by an op the later positions do not account for.
  Full trace, and the measurement showing why the obvious repair does not work,
  in INVESTIGATIONS.md. Reproduce by raising `undoRedoSeeds` in
  `internal/piecetable/undo_utf8_test.go` to 3000: seed 578, step 12. Wants a
  decision about what `rebase` is walking before it wants code.
- [ ] **A finished search waits for the next tick.** The walk is off the event
  thread now and the result is installed by `apply`, which runs from `Handle`
  and `Render` — so a search that finishes while you are not typing shows up on
  the 150 ms tick rather than immediately. Fixing it properly means a way for a
  worker to post an event into the loop, which is the same seam the agent pane
  will need. Do it once, for both.
  Cancellation landed first and is the larger half of the same problem: an
  abandoned walk now stops instead of running to completion. What remains is
  the other direction — a finished walk still waits for a tick to be shown.

## Panes and fields

- [ ] **shift+tab cannot leave a sidebar.** Both panes stop it at their first
  component, so the only way back to the editor is tab all the way forward or a
  chord. That was deliberate — tab indents in the editor, so a one-key return
  would make editing interruptible — but it makes the first field of each pane a
  dead end. Decide between wrapping the ring and letting shift+tab exit
  backwards.
- [ ] **cut and copy hit the wrong buffer from a field.** `handleGlobal` claims
  them before any pane sees them and `clip()` operates on `Tabs.Active()`, so
  cmd+c in the search box copies from the editor and cmd+x edits the document
  being searched. More visible now that fields have real selections. `app` needs
  to ask the focused thing whether it owns a selection.
- [ ] **Horizontal scroll in the explorer.** The selected path is now spelled
  out on the pane's last row, which answers "which file is this" but not "what
  is the rest of this name" for the rows around it. An offset that follows the
  selection is the other half; it needs a rule for when it returns to zero, or
  the tree jitters sideways as you arrow through mixed depths.
- [ ] Small and split panes, and how they resize.

## Editor

- [ ] **Mouse.** Click to position, drag to select, wheel to scroll, cmd-click
  for an extra cursor. Also: click a tab to switch, click a file in the explorer
  or search results to open it (refusing binaries loudly rather than in the
  status line). The wheel is the easy half now: `Pane.ScrollPage` moves the view
  without touching a cursor, so wheel events need a decode path and nothing
  else.
- [ ] go-to-line (ctrl+g), go-to-symbol (cmd+shift+o).
- [ ] Bracket matching and auto-indent on newline.
- [ ] **Blinking secondary carets.** The real caret blinks because the terminal
  blinks it; a drawn one would need raj to redraw on a timer, which means a tick
  fast enough to be a blink and a dirty-region pass small enough that blinking
  costs one cell rather than a frame. Nice, and a long way down: the tick is
  150 ms today and exists for idle work.
- [ ] Tabs as clickable tags — visual now, clickable once the mouse lands.

## Workspace

- [ ] **Session persistence** — tabs, cursors, scroll, sidebar state, expanded
  directories, and the focused pane, so a returning session lands where it was
  left rather than in the explorer. `.git/raj/session.json`, `--no-restore`.
- [ ] **Dirty-buffer restore** — persist the journal and add-buffers, validated
  by an orig-hash per buffer.
- [ ] **Attribution across restarts** — tint is commit-scoped, so it must
  outlive the process.
- [ ] **Change gutter** versus git HEAD, distinct from the author tint. This is
  also where deletions get represented, since a deleted span leaves no piece.

- [ ] **Search reads the disk, not the buffer.** A workspace search opens files
  and scans them, so a match in an unsaved buffer is invisible and a match it
  does report may be stale. Routing open documents through `Spans` instead of
  `os.Open` is the fix, and it is the same seam an agent would need to search
  what it has just edited.
- [ ] **Parallel walk, if it is worth it.** Measured floors say a search is I/O
  bound: 18 ms traversal, 90 ms to read 97 MB, 92 ms to read and scan it. A
  worker pool is the only remaining lever, but it breaks `MaxMatches` — a pool
  returned 509-517 results against a cap of 500, nondeterministically, because
  workers in flight still append. Needs streaming results or deterministic
  truncation first. Measure on a multicore box before building it; the numbers
  in BENCHMARKS.md came from one core, where it showed nothing.

## Buffer

- [ ] **Compaction.** Merge adjacent same-author pieces; only flatten spans that
  are both saved and committed.
- [ ] **16 ms coalescing window** for streaming agent hunks.

## Agents (deliberately last)

- [ ] Agent pane and the plumbing from a model's diff to `Session.ApplyDiff`.
- [ ] Region leases to prevent conflicts rather than only detect them.
- [ ] SQLite session store — the op log as the shareable, forkable artifact.

## Known rough edges

- [ ] **The match cap makes a common query look narrow.** `MaxMatches` is 500
  across the whole search and the walk is lexical, so a common term spends the
  entire budget on whatever sorts first and `SkipAll` stops before the rest of
  the tree is seen. Measured: `te` reports 500 results in 6 files, all of them at
  the repository root, 200 from `LICENSE` alone; `testing` reports 361 in 34
  files because it never hits the cap. The reported file count therefore says
  more about walk order than about the query, and it is least informative
  exactly when the term is most common. Wants a per-file cap — 20 or so, with
  the header showing the true count — so no single file eats the budget, and a
  global cap high enough that the walk usually finishes. Assert it with a
  fixture where one early file holds more matches than the cap, and check that
  later directories still appear.
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
- [ ] **Four actions are bound but unimplemented**, so their chords are taken
  from the terminal for nothing: `ToggleAgent` (cmd+alt+b), `CommandPalette`
  (cmd+shift+p), `GotoLine` (ctrl+g), `GotoSymbol` (cmd+shift+o).
- [ ] **Wheel scroll does nothing in the alt screen on iTerm2.** raj never
  enables mouse reporting, so iTerm2 sends wheel events to its own scrollback,
  which the alt screen is not part of. iTerm2's translation of the wheel into
  arrow keys is `AlternateMouseScroll`, an *application* default rather than a
  profile key, so the generated dynamic profile cannot turn it on — that one is
  a README line (`defaults write com.googlecode.iterm2 AlternateMouseScroll
  -bool true`), not code. The fix that works on every terminal is raj asking
  for mouse reporting itself (DECSET 1000/1006) and handling wheel events;
  wheel-only is the smallest useful slice of the Mouse item under Editor, and
  scrolling already moves the cursor by design, so it needs no new semantics.
- [ ] **Profile switching in iTerm2 is not clean.** raj switches profile on
  entry with OSC 1337 and restores on exit, but installing the profile is still
  a manual step and the switch is visible. Autoloading — write the generated
  profile into `DynamicProfiles/` on first run if it is absent or stale, keyed
  off a hash of `Bindings` — would remove the setup step and keep the profile
  from drifting when the table changes. Deferred: what is there works.
- [ ] `raj --config ghostty` must be regenerated and the terminal reloaded
  whenever the binding table changes. Same for the iTerm2 profile.
- [ ] **Document what is supported** — a keybinding table generated from
  `keys.Bindings` so it cannot drift, with the unimplemented actions marked.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Deliberately not doing

- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
