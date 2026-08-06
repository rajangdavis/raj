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

- [ ] **Mouse.** Click to position, drag to select, wheel to scroll, cmd-click
  for an extra cursor. Also: click a tab to switch, click a file in the explorer
  or search results to open it (refusing binaries loudly rather than in the
  status line). The wheel is the easy half now: `Pane.ScrollPage` moves the view
  without touching a cursor, so wheel events need a decode path and nothing
  else.
- [ ] go-to-symbol (cmd+shift+o). go-to-line landed on the dialog seam.
- [ ] Bracket matching and auto-indent on newline.
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

- [ ] **Search reads the disk, not the buffer.** A workspace search opens files
  and scans them, so a match in an unsaved buffer is invisible and a match it
  does report may be stale. Routing open documents through `Spans` instead of
  `os.Open` is the fix, and it is the same seam an agent would need to search
  what it has just edited.
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
- [ ] **Three actions are bound but unimplemented**, so their chords are taken
  from the terminal for nothing: `ToggleAgent` (cmd+alt+b), `CommandPalette`
  (cmd+shift+p), `GotoSymbol` (cmd+shift+o). `GotoLine` was the fourth.
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
