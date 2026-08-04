# TODO

Open work only. Measured numbers live in BENCHMARKS.md; root causes, terminal
findings and decisions live in INVESTIGATIONS.md.

## Highest priority

- [ ] **Cursor/viewport spec.** Write down the invariants — offset ↔ line/column
  ↔ screen cell, what each edit does to each cursor, when the viewport may move
  — and assert them as properties rather than examples. Every cursor bug so far
  has been a disagreement between two of those three representations, and they
  are only caught today by tests that happen to look. Wrapping has now added a
  fourth representation (visual row), which raises the stakes rather than
  lowering them.
- [ ] **Line index update is O(bytes) on insert.** `applyToIndex` materialises
  the inserted span to count newlines, which dominates a large paste and
  allocates a full copy of it. Scanning the inserted pieces directly removes the
  allocation; keeping a newline count per piece would remove the scan.
- [ ] **Reclaim table for chords a terminal keeps.** The keymap binds 18 chords
  absent from `Bindings`; sixteen arrive by accident and shift+pgup/pgdown do
  not, because iTerm2 claims them for scrollback. Nothing asserts the emitters
  cover what the keymap binds, so each new terminal rediscovers this one chord
  at a time. Wants a third table plus a test that every keymap chord is
  accounted for in exactly one of: `Bindings`, reclaim, or "terminals send this
  natively".

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
- [ ] **The changed-only checkbox** is at the bottom of the explorer and only
  reachable by tabbing through the tree. Move it under the heading and put it on
  cmd+up/down, matching how the search pane already jumps between query and
  results.
- [ ] Small and split panes, and how they resize.

## Editor

- [ ] **Mouse.** Click to position, drag to select, wheel to scroll, cmd-click
  for an extra cursor. Also: click a tab to switch, click a file in the explorer
  or search results to open it (refusing binaries loudly rather than in the
  status line). `Viewport.ScrollBy` already exists and has one caller.
- [ ] go-to-line (ctrl+g), go-to-symbol (cmd+shift+o).
- [ ] Bracket matching and auto-indent on newline.
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
- [ ] **Four actions are bound but unimplemented**, so their chords are taken
  from the terminal for nothing: `ToggleAgent` (cmd+alt+b), `CommandPalette`
  (cmd+shift+p), `GotoLine` (ctrl+g), `GotoSymbol` (cmd+shift+o).
- [ ] `raj --config ghostty` must be regenerated and the terminal reloaded
  whenever the binding table changes. Same for the iTerm2 profile.
- [ ] **Document what is supported** — a keybinding table generated from
  `keys.Bindings` so it cannot drift, with the unimplemented actions marked.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] `cmd+shift+r` to reopen closed tabs, handing `cmd+shift+t` back — only
  worth doing if Ghostty actually binds it; check `+list-keybinds` first.

## Deliberately not doing

- Scrolling moves the cursor. This is intentional and preferred; not a bug.
- Horizontal scrolling while wrapped. There is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
