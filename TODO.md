# raj — status and TODO

Everything below is verified by `go build ./... && go test ./...` from a clean
extraction: 11 packages, all green. One dependency, chroma, pinned at v2.14.0.

## Where we stand

### Settled and proven on hardware

| | |
|---|---|
| Ghostty `kkp_on` gate | 53/53 chords measured via `cmd/keyprobe -checklist` |
| Gate condition | requires KKP flag 8 (`report_all`); flags 1–7 correctly fall through to Ghostty |
| Suspend | ctrl+z pops KKP, Ghostty takes its keys back, `fg` restores |
| Focus loss | Ghostty hands keys back per-surface; raj needs no work here |
| Theme | OSC 10/11/4 answered, so colours are read from the terminal |
| Text input | KKP flag 16 confirmed; shift+a types `A`, dead keys and non-Latin layouts covered |

### Packages

```
internal/keys        bytes -> chord -> action, scope-aware. Measured table
                     generates both the Ghostty config and the keymap.
internal/term        raw mode, KKP stack, alt screen, suspend, OSC queries.
internal/ui          Host seam: cell grid, frame diff, clipping, sanitisation.
                     NativeHost (real terminal) and FakeHost (headless tests).
internal/piecetable  leaf-embedded B-tree from the benchmark harness, plus
                     per-author stores, Session, ApplyDiff, undo. Fuzzed
                     against a naive oracle.
internal/view        line index (fuzzed vs rescan), columns, viewport.
internal/editor      file, cursors, motions, actions, render, binary sniffing.
internal/widget      inputs, lists, boxes, the focus vocabulary.
internal/tabs        open/close/reopen/switch.
internal/explorer    lazy tree, git-status filter.
internal/search      literal/regex engine, grouped collapsible results.
internal/picker      cmd+p overlay with fuzzy ranking.
internal/app         event loop, focus routing, layout, breakpoints, debug pane.
internal/syntax      chroma tokens, cached per version, tokenised off-thread.
```

### Working

Typing, motions, selections, multi-cursor (cmd+alt+up/down), undo/redo, save,
indent/outdent on selections, tabs with reopen, the explorer with a changed-only
filter, search with include/exclude globs and collapsible per-file groups, the
cmd+p picker, narrow-mode single-pane layout, agent-authored text rendering with
a background tint, suspend/resume, and alt-screen behaviour.

---

## TODO

### Fixed since the last session

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
  Measured below.
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

### Measurements

Paste, a 1080-byte block into a 200-line file, setup excluded from both:

| cursors | path | time | allocs | pieces | ops | bytes stored |
|---|---|---|---|---|---|---|
| 1 | block | 7.5 us | 16 | 1 | 1 | 1080 |
| 1 | per-cursor | 7.0 us | 13 | 1 | 1 | 1080 |
| 4 | block | 6.9 us | 16 | 1 | 1 | 1080 |
| 4 | per-cursor | 22.2 us | 62 | 7 | 4 | 4320 |
| 16 | block | 7.2 us | 16 | 1 | 1 | 1080 |
| 16 | per-cursor | 95.5 us | 217 | 31 | 16 | 17280 |

The block path is flat in cursor count on every axis; the per-cursor path is
linear on all of them, including bytes stored, because each cursor appended its
own copy of the pasted text. At one cursor the two are within noise — the
dedicated path costs nothing to have.

No regression elsewhere: `DocInsert` is 791 ns at 1k pieces and 871 ns at 100k,
0 allocs, unchanged; `Spans` over a document built from 200 pastes is 138 ns.

### Clipboard: measured

Copy captures piece records as well as text. Pasting an internal clip splices
those records, so it **appends nothing**, at any size:

| cursors | clipboard | pieces added | bytes stored |
|---|---|---|---|
| 1 | 40 B | 1 | 0 |
| 4 | 163 B | 7 | 0 |
| 16 | 655 B | 31 | 0 |
| 64 | 2623 B | 127 | 0 |

Verified at 100 B, 10 KB and 1 MB: zero bytes stored in every case. The clip
also outlives deletion of the region it came from, because stores never erase —
the same property that makes undo free. Pasted text keeps its original author,
so agent-written code stays tinted however often it is moved.

Timing, splice versus append:

| size | spliced | appended |
|---|---|---|
| 1 KB | 11.0 us, 6 allocs | 12.1 us, 9 allocs |
| 100 KB | 895 us, 6 allocs | 949 us, 9 allocs |

**The 100 KB row is not the paste.** Storage is O(1) either way; the cost is
`applyToIndex`, which materialises the inserted text to count newlines — O(bytes)
plus a full copy. That is the next thing to fix (below), and fixing it makes a
large internal paste genuinely O(pieces).

### External clipboard: measured, and decided

A clipboard from another program cannot splice — its bytes are not in the
buffer — so the question was only whether to distribute it across cursors.

| cursors | clipboard | distribute | insert whole |
|---|---|---|---|
| 4 | 147 B | 7 pieces, 144 B, 8.5 us | 1 piece, 147 B, 3.7 us |
| 16 | 603 B | 31 pieces, 588 B, 24 us | 1 piece, 603 B, 5.0 us |
| 64 | 2475 B | 127 pieces, 2412 B, 127 us | 1 piece, 2475 B, 11 us |

The numbers do not decide it: distributing is 11x slower at 64 cursors and 127x
the pieces, but 127 us is a hundredth of a frame and both store the clipboard
once. It is a semantics question wearing a performance costume.

**Decided: distribute when the line count matches the cursor count.** The
alternative puts the whole clipboard at the primary cursor and leaves the other
N-1 cursors doing nothing, which is not a useful outcome under any reading. The
failure mode — a coincidental match producing an unwanted distribution — is rare
and costs one cmd+z. Mismatched counts still insert whole.

down), undo/redo, save,
indent/outdent on selections, tabs with reopen, the explorer with a changed-only
filter, search with include/exclude globs and collapsible per-file groups, the
cmd+p picker, narrow-mode single-pane layout, agent-authored text rendering with
a background tint, suspend/resume, and alt-screen behaviour.

---

## TODO

### Fixed since the last session

- [x] **cmd+z corruption.** Three stacked bugs, found by fuzzing rather than
  inspection. (1) Rebase was not inverse-consistent at boundaries: a deletion
  starting exactly at a point left the point alone while the matching
  re-insertion shifted it, so undo drifted by the deleted length each time it
  crossed a later edit. `rebase` takes a `

### Terminals

raj emits its own keybindings for every terminal it supports, from one measured
table, and can measure any terminal it is run under:

    raj --config ghostty       # Ghostty, macOS
    raj --config ghostty-linux
    raj --config iterm2        # an iTerm2 dynamic profile
    raj --probe                # what does THIS terminal deliver?
    raj --probe --checklist    # walk every binding, emit a measured keymap

The probe is a flag on raj rather than a second binary, so testing a terminal
needs no separate build: whatever raj you are running is the decoder being
measured. `cmd/keyprobe` remains as a thin wrapper for building it alone.

Terminals that do not speak the Kitty protocol send ctrl chords as C0 bytes,
which raj decodes as the chords that produced them — iTerm2 answers the colour
and device-attribute queries but not the KKP one, and before that was handled
raj could not see ctrl+c at all.

### iTerm2

`raj --config iterm2` emits a dynamic profile. Install with:

    raj --config iterm2 > "$HOME/Library/Application Support/iTerm2/DynamicProfiles/raj.json"

iTerm2 picks it up without a restart; open a window with the "raj" profile.

iTerm2 supports CSI u, but that is not the issue — cmd+w and friends are
consumed by the menu layer before any protocol is consulted, exactly as in
Ghostty. iTerm2's scoping mechanism is the profile rather than a KKP condition,
which is weaker in one specific way: while the profile is in use, that window
cannot close its own tab with cmd+w, and iTerm2 has no way to know raj has
exited. Ghostty's gate releases the chord the moment raj stops asking for it.

raj switches to that profile on startup and back on exit, so the mappings apply
only while raj is running — the same effect as Ghostty's kkp_on gate, driven
from raj's side rather than the terminal's. It uses OSC 1337 SetProfile, and
reads ITERM_PROFILE to know what to restore. It runs through the same Enter and
Leave that own raw mode and the KKP stack, so suspend, fatal signals and panics
all restore correctly. SIGKILL does not, and nothing can fix that.

Set RAJ_ITERM_PROFILE to use a different profile name, or to empty to disable
switching if you put the mappings in your everyday profile instead.

Verified against iTerm2's own preferences: the two-part "0xCHAR-0xMASK" form is
correct, and mappings fire once the session actually uses the raj profile
(Profiles menu, or cmd+i to check).

One correction that cost an afternoon: AppKit's charactersIgnoringModifiers
applies shift, so shift+L reports "L" and not "l". Generating the lowercase code
made every shift+letter mapping silently miss while unshifted ones worked.

Measured: 36 of 56 chords arrive, **including cmd+w** — so iTerm2 does yield
menu chords to a key mapping, which was the open question. The failures were
every shift+letter chord (the uppercase bug above) and cmd+1 through cmd+9.

That measurement predates dropping cmd+1-9 from the table. They are no longer
asked for, and the terminal keeping them is now the intended outcome rather than
a failure — a re-measurement would report 36 of 47.

### Reclaiming chords a terminal keeps

The keymap binds 18 chords that are not in `Bindings`: tab, shift+tab, esc,
enter, backspace, delete, the four arrows, their shift variants, pgup, pgdown,
and shift+pgup/pgdown. Under Ghostty they arrive free, because `report_all`
auto-encodes anything without a `kkp_on` line. iTerm2 has no such gate: the
profile is exhaustive by omission, so a chord absent from `Bindings` is absent
from the profile, and raj gets it only if iTerm2 both sends it and does not
claim it first.

Sixteen work by that accident. shift+pgup and shift+pgdown do not — iTerm2 binds
them to scrollback paging, so shift+fn+up/down never reaches raj. Confirmed with
`raj --probe`: nothing arrives.

The gap is that this is undetectable. Nothing asserts the emitters cover what
the keymap binds, so each new terminal rediscovers it one chord at a time. Wants
a third table for chords some terminal claims and raj must reclaim, plus a test
that every keymap chord is accounted for in exactly one of: `Bindings`, reclaim,
or an explicit "terminals send this natively" list.

### Fixed this session

- [x] **Paste was never wired up.** `ui.Paste` existed and `app` handled it, but
  nothing constructed one: mode 2004 was never enabled and the decoder had no
  case for the markers, so a paste arrived as a burst of synthetic keystrokes —
  and Ghostty warned about it, because raj never advertised that it handled
  pastes itself. The payload is now claimed before `parseCSI`, since the bytes
  between the markers are content rather than parameters: a pasted `5;3R` was
  being read as a cursor-position reply. CR and CRLF fold to LF. An
  unterminated paste surrenders its start marker past 16 MB rather than
  buffering forever.
- [x] **Paste follows focus.** It was gated on the editor, so pasting into the
  search box or the picker vanished silently. Fields take the first line,
  trimmed; the explorer still ignores pastes, since its only `keys.None` handler
  reads a space as the changed-only toggle.
- [x] **Fields have selections.** `widget.Input` had no anchor, so every
  selecting chord was a silent no-op and cmd+a *emptied the field* — with
  nothing to represent a selection, select-all did the destructive thing that
  looked like it. Also: `WordLeft` was aliased to `prevBoundary`, which walks
  one UTF-8 rune, so alt+left was character motion under another name. Adding
  the anchor immediately panicked the find bar, which found three places
  assigning `Text` directly and leaving offsets past the new end — `Fields.Trim`
  had that bug for the cursor already.
- [x] **cmd+1-9 handed back** to the terminal, so terminal tabs are reachable
  without suspending. `GotoTab1-9` and `tabNumber` stay live but unbound.
- [x] **chroma to v2.24.0** — the newest release still declaring `go 1.22`. The
  wall is v2.25.0, which jumps to `go 1.26`, not v2.27/1.25 as recorded below.

### Next, highest priority


[ ] **Cursor/viewport spec.** Write down the invariants — offset ↔ line/column
  ↔ screen cell, what each edit does to each cursor, when the viewport may move
  — and assert them as properties rather than examples. Every cursor bug so far
  has been a disagreement between two of those three representations, and they
  are only caught today by tests that happen to look.
- [ ] **Line index update is O(bytes) on insert.** `applyToIndex` materialises
  the inserted span to count newlines, which dominates a large paste and
  allocates a full copy of it. Scanning the inserted pieces directly removes the
  allocation; keeping a newline count per piece would remove the scan.
- [ ] **Text wrapping.** Long lines overflow rather than wrap, which is worst on
  a resize. Blocked on the cursor/viewport spec above: wrapping breaks the
  one-line-is-one-row assumption that `Viewport.Top`, `ScrollTo`, the render
  loop and every viewport test bake in.
<this needs to be cleaned up and prioritized>

## TODO
-- Document differences from what is documented in TODO.md
-  - Scrolling must not move the cursor -> I kind of like this, keeping it
-- Mouse events would be sick
-- Multi-cursor all using Ibeams would be nice
-  - Click on editor puts cursor there
-  - Not sure if the highlighted text works the same as highlighting with the cursor
-  - Clicking on tabs changes tab
-  - Clicking on file in explorer/search opens the file unless binary, put a warning for that
-- Text wrapping (especially on resizes :))
-- resizes totally add artifacts :) that persist even when the tabs are deleted
-- shift+tab doesn't get me back out of the explorer/search fields
-- cmd+c / cmd+x in a search field copies from the editor instead of the field
-- Need to move the box to only see checked items, and reach it with cmd+up/down
-- Document what is supported, but need to finish implementing and confirm things
-- Maybe bind cmd+shift+r to reopen old tabs and give cmd+shift+t back to ghostty
-  - only worth it if ghostty actually binds cmd+shift+t; check +list-keybinds first
-- Think about small/split panes and resizing
-- There's some slight spacing differences between files, TODO.md and README.md
-  - narrowed to two runes: check line 170 of TODO.md against a line with an em-dash
-- shift+fn+up/down is eaten by iTerm2 before raj sees it; needs a reclaim mapping
-
-## Done
-
-- Paste works. It was never wired up: bracketed paste was neither enabled nor
-  parsed, so pastes arrived as a key storm and Ghostty warned about it. Pasting
-  into the search box and the picker works too.
-- Search and picker fields have real selections: shift+alt+arrows by word,
-  cmd+shift+arrows to the edges. cmd+a used to empty the field.
-- cmd+1-9 went back to the terminal, so terminal tabs are reachable without
-  suspending. raj's own tabs are cmd+alt+left/right.
-- chroma is on v2.24.0, which is the newest release that still builds on Go 1.22.
-- Debug pane works; leaving it on ctrl+shift+d.
-- iTerm2 is supported via a dynamic profile, so a fork of ghostty is no longer
-  the only way to run this.

</this needs to be cleaned up and prioritized>
- [ ] **Tabs as clickable tags** — visual now, clickable when mouse lands.

(Scrolling moving the cursor is deliberate; see the README. Not a bug.)

### Next, panes and fields

- [ ] **shift+tab cannot leave a sidebar.** Both panes stop it at their first
  component, so the only way back to the editor is tab all the way forward or a
  chord. That was a deliberate choice — tab indents in the editor, so a one-key
  return would make editing interruptible — but it makes the first field of each
  pane a dead end. Decide between wrapping the ring and letting shift+tab exit
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

### Next, editor

- [ ] go-to-line (ctrl+g), go-to-symbol (cmd+shift+o).
- [ ] Mouse: click to position, drag to select, wheel to scroll.
- [ ] Bracket matching and auto-indent on newline.

### Next, workspace (after the buffer work)

- [ ] **Session persistence** — tabs, cursors, scroll, sidebar state, expanded
  directories, and the focused pane, so a returning session lands where it was
  left rather than in the explorer. `.git/raj/session.json`, `--no-restore`.
- [ ] **Dirty-buffer restore** — persist the journal and add-buffers, validated
  by an orig-hash per buffer.
- [ ] **Attribution across restarts** — tint is commit-scoped, so it must
  outlive the process.
- [ ] **Change gutter** versus git HEAD, distinct from the author tint. This is
  also where deletions get represented, since a deleted span leaves no piece.

### Next, buffer

- [ ] **Compaction.** Merge adjacent same-author pieces; only flatten spans that
  are both saved and committed.
- [ ] **16 ms coalescing window** for streaming agent hunks.

### Agents (deliberately last)

- [ ] Agent pane and the plumbing from a model's diff to `Session.ApplyDiff`.
- [ ] Region leases to prevent conflicts rather than only detect them.
- [ ] SQLite session store — the op log as the shareable, forkable artifact.

### Known rough edges

- [ ] Resize has no test coverage.
- [ ] No Bubbletea adapter yet. The `ui.Host` interface is six methods.
- [ ] Display width table is hand-rolled; suspect it first if the caret drifts.
  Narrowed: TODO.md holds three runes README.md does not — en-dash, em-dash, and
  `↔` U+2194, all East Asian Ambiguous. raj calls all three narrow. The em-dash
  is on 17 lines, `↔` on exactly two (170 and 171), so which lines drift says
  which rune disagrees with the terminal. `↔` is the likely one: it is
  emoji-capable without emoji presentation, which is the class fonts disagree
  about.
- [ ] `raj --config ghostty` must be regenerated and the terminal reloaded
  whenever the binding table changes. Same for the iTerm2 profile.
- [ ] Four actions are bound but unimplemented, so their chords are taken from
  the terminal for nothing: `ToggleAgent` (cmd+alt+b), `CommandPalette`
  (cmd+shift+p), `GotoLine` (ctrl+g), `GotoSymbol` (cmd+shift+o).
