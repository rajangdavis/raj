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
