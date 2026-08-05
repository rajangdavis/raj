# Completed

What works and what has been fixed. Open work lives in TODO.md; measured numbers
in BENCHMARKS.md; root causes and decisions in INVESTIGATIONS.md.

Entries here are one line each. Where a fix had a non-obvious cause worth
remembering, the explanation lives in INVESTIGATIONS.md rather than being
repeated.

## Working

Typing, motions, selections, multi-cursor (cmd+alt+up/down), undo/redo, save,
indent/outdent on selections, tabs with reopen, the explorer with a changed-only
filter, search with include/exclude globs and collapsible per-file groups, the
cmd+p picker, narrow-mode single-pane layout, agent-authored text rendering with
a background tint, suspend/resume, and alt-screen behaviour.

Word wrapping is on by default (`--wrap=false`, or ctrl+alt+w to toggle).
Bracketed paste works, including into the search box and the picker. Search and
picker fields have real selections. raj runs on a patched Ghostty via the
`kkp_on` gate, or on iTerm2 via a dynamic profile.

## Terminal and keys

- [x] **Every chord is accounted for in exactly one table.** `Bindings` for the
  chords a terminal binds, `Reclaim` for the ones it keeps and must be told to
  hand over, `Natives` for the ones nothing claims. Both emitters now walk
  `Emitted()`, so shift+pgup and shift+pgdown are finally taken back from
  iTerm2's scrollback, and a test fails if the keymap and the tables disagree in
  either direction.
- [x] **Escape works on terminals without KKP.** A bare 0x1b was indistinguishable
  from the start of a sequence and was held for the next keypress, so escape
  never reached the app and the key after it decoded as alt+key. The reader now
  flushes a stalled partial after 25 ms. Root cause in INVESTIGATIONS.md.
- [x] **Ghostty `kkp_on` gate** — 53/53 chords measured on hardware.
- [x] **iTerm2 support** via a dynamic profile, so a Ghostty fork is no longer
  the only way to run raj. raj switches to the profile on startup and back on
  exit, using OSC 1337 SetProfile.
- [x] **`raj --probe`** — the key probe is a flag rather than a second binary,
  so testing a terminal needs no separate build.
- [x] **Legacy C0 decoding** — terminals that do not speak the Kitty protocol
  send ctrl chords as single bytes; without this raj could not see ctrl+c at all
  under iTerm2.
- [x] **cmd+1-9 handed back** to the terminal, so terminal tabs are reachable
  without suspending. raj's own tabs are cmd+alt+left/right.
- [x] **Suspend** — ctrl+z pops KKP, the terminal takes its keys back, `fg`
  restores.
- [x] **Theme** — OSC 10/11/4 answered, so colours come from the terminal.
- [x] **Text input** — KKP flag 16 confirmed; shift+a types `A`, dead keys and
  non-Latin layouts covered.

## Editor and buffer

- [x] **Undo no longer deletes the wrong bytes** when a later insertion landed
  exactly at the start of the range being reversed. `rebase` left the range put
  in that case, which is right for an empty point and wrong for a span, so the
  undo removed the new text instead of the old — and with multi-byte text it
  removed part of a rune. Caught by a fuzz that asserts UTF-8 validity rather
  than only that the two engines agree: they can agree on a document neither
  ever created, because both faithfully apply whatever offsets the session hands
  them.
- [x] **The cursor/viewport spec**, written down in CURSOR-VIEWPORT-SPEC.md and
  asserted as properties rather than examples: a random document, a random
  action sequence, every invariant checked after every action. It found two
  bugs on its first run and a third on a wider sweep.
- [x] **Word motion was byte-at-a-time**, so every continuation byte of a
  multi-byte rune read as a non-word character and cmd+left/right stopped the
  cursor inside a rune. Now steps by runes.
- [x] **The line index desynced after a multi-cursor redo**, and updating it
  cost a copy of every paste. Both were the same cause: the inserted span was
  read back out of the document instead of from the op's own pieces. Scanning
  the pieces is correct for every op in a batch rather than only the last, and
  measures 224 us/803 KB -> 13 us/0 B on an 800 KB paste (BENCHMARKS.md).
  `File.sync` now catches up on every op since the index was last current,
  rather than only the most recent one.
- [x] **Secondary cursors draw as a caret-coloured block with the character
  still in it** — dark text on a light cell, visible at a glance and not the
  selection grey or either find colour. Three shapes were tried: a bar glyph
  read best but covered the character ("Paste" as "Pa|te"), reverse video read
  as a selection, and underline kept the glyph but was too faint to find in a
  screen of text. `Theme.Caret` and `Theme.CaretText` are the two fields to
  change; the tests assert the character survives and the cell reads as a
  caret, not the particular styling.
- [x] **Search runs off the event thread, debounced.** It walked the whole tree
  synchronously on every keystroke: 74 ms per character for a query that matches
  nothing, on the thread that draws frames (BENCHMARKS.md). Now a 120 ms
  debounce coalesces a burst into one search, the walk happens on a worker, and
  a generation stamp drops results for queries already typed past. `Settle`
  exists so tests can drive it a step at a time.
- [x] **Scrolling no longer moves the cursor.** Paging moved the caret with the
  view, which reads fine with one cursor and badly with several — paging to look
  at something else collapsed a multi-cursor set. `ScrollPage` moves the
  viewport alone; the next cursor action pulls the view back, because every one
  of them still ends in `FollowCursor`. Shift+page still moves the cursor: a
  selection needs an end to move.
- [x] **cmd+g and cmd+shift+g** step to the next and previous match, with the
  find bar closed. `Hide` keeps the query, so a closed bar recomputes rather
  than doing nothing.
- [x] **The changed-only toggle moved under the heading**, reachable with
  cmd+up/down like the search pane's query and results, and the selected path is
  spelled out on the explorer's last row — truncated from the left, since the
  end of a path is what tells two files apart.
- [x] **Split selection into lines (cmd+shift+l)**, so cmd+l then cmd+shift+l
  is the Sublime idiom: select the block, get one cursor per line, clipped to
  the selection at both ends. A selection ending exactly on a line boundary —
  what cmd+l leaves — drops that last line rather than putting a stray cursor on
  it. Cursors with no selection are carried through untouched.
- [x] **Find All moved to ctrl+cmd+g** (`103;13u`), matching Sublime, now that
  split-into-lines owns cmd+shift+l. Both halves are asserted in one test:
  dropping one is how a working feature disappears silently.
- [x] **cmd+l selects the line** and repeats extend the selection a line at a
  time, as in Sublime Text. One formula covers both the first press and the
  repeat — start of the line the low end is on, to the start of the line after
  the one the high end is on — so there is no "already selected" special case to
  get wrong. Expands every cursor; `Cursors.Normalize` merges the ones that grow
  into each other. Regenerate the Ghostty config and the iTerm2 profile: it is a
  new chord in the table.
- [x] **cmd+z corruption.** Three stacked bugs, found by fuzzing rather than
  inspection. (1) Rebase was not inverse-consistent at boundaries: a deletion
  starting exactly at a point left the point alone while the matching
  re-insertion shifted it, so undo drifted by the deleted length each time it
  crossed a later edit. `rebase` takes a `slide` flag, because a diff hunk and
  an undo want opposite answers there. (2) A group that failed mid-reversal was
  left half-undone; reversal is all-or-nothing with rollback. (3) The `undone`
  flag did not compose — liveness is now *derived*: an op is in effect exactly
  when no live op reverses it. 600 fuzz seeds, including interleaved undo/redo
  checked against the expected state at every step.
- [x] **The pane/buffer desync.** Undo mutated the buffer and nothing moved the
  cursors, so they addressed offsets that no longer existed; a cursor past the
  end of its line reports a large column, which scrolled the view sideways.
  Undo and redo return the ops they applied and the pane repositions.
- [x] **Multi-cursor undo jumped to the bottom.** Collapsing to the last
  committed op landed on whichever site was edited last — and edits apply
  highest-offset-first. Every reversed op now restores its own cursor, so the
  multi-cursor state survives the undo.
- [x] **Paste has its own path.** `Pane.Paste` appends once and commits one op.
- [x] **Leaf iterator.** `PieceBTree.Each` descends once and walks leaves,
  skipping subtrees by cached count. `Spans` on a screenful of a 200k-piece
  document: 126 us -> 62 us.
- [x] **Page up/down** on pgup/pgdown (fn+arrows on macOS), with shift variants.
  The view moves with the cursor.
- [x] **Debug pane** (ctrl+shift+d): recent keystrokes as raw bytes, chord and
  resolved action, plus doc size, piece count, store bytes per piece, journal
  length, goroutines and heap. Memory is sampled on the idle tick, never during
  a frame.
- [x] **Initial focus** is the explorer when no file is named; a named file
  takes the editor.
- [x] **alt+arrows moved the wrong line**; cursors are now recorded as
  (line, column) before the move and restored after.
- [x] **I-beam cursor** (DECSCUSR 5) for the primary cursor; secondary cursors
  still draw as cells, since a terminal has one caret.
- [x] Tab cycles find matches; cmd+up/down jump between the search query and
  results; cmd+1-9 and tab switching focus the editor; cmd+x leaves the cursor
  at the start of the replacement line; changed-only shows in the heading.
- [x] Syntax retokenises after every key, not only on the tick; tab labels
  disambiguate shared base names; workspace root walks up to the nearest .git;
  syntax uses the bright half of the palette.


## This session

- [x] **Bracketed paste.** `ui.Paste` existed and `app` handled it, but nothing
  ever constructed one — mode 2004 was never enabled and the decoder had no case
  for the markers, so pastes arrived as a key storm and Ghostty warned about it.
- [x] **Paste follows focus** — it was gated on the editor, so pasting into the
  search box or the picker vanished silently.
- [x] **Whole-line copy round-trips.** `Copy` published a trailing newline in
  `Text` that the captured `Spans` did not carry, so an internal paste dropped
  it while an external one kept it.
- [x] **Fields have selections.** shift+alt+arrows by word, cmd+shift+arrows to
  the edges. cmd+a used to empty the field, and alt+left was character motion
  wearing the name of word motion.
- [x] **Word wrapping**, hybrid policy — breaks at whitespace and at the
  separators that occur in code, splitting mid-token only when a single token
  exceeds the pane. Fuzzed against a greedy-maximality oracle.
- [x] **Scroll thrash.** `prev` was recorded before the write and the write
  error was discarded, so a short write left every later diff skipping exactly
  the cells that never arrived.
- [x] **Resize thrash.** Frames sized for a terminal that no longer existed were
  written anyway; a frame wider than the terminal wraps and scrolls the screen.
- [x] **Suspend thrash.** The size guard trusted a cache refreshed only by
  SIGWINCH, and a stopped process services no signals.
- [x] **chroma to v2.24.0** — the newest release still declaring `go 1.22`.
- [x] **Documents split** — TODO, BENCHMARKS, INVESTIGATIONS, COMPLETED.
