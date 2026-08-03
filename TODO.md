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

### Next, highest priority

- [ ] **Cursor/viewport spec.** Write down the invariants — offset ↔ line/column
  ↔ screen cell, what each edit does to each cursor, when the viewport may move
  — and assert them as properties rather than examples. Every cursor bug so far
  has been a disagreement between two of those three representations, and they
  are only caught today by tests that happen to look.
- [ ] **Line index update is O(bytes) on insert.** `applyToIndex` materialises
  the inserted span to count newlines, which dominates a large paste and
  allocates a full copy of it. Scanning the inserted pieces directly removes the
  allocation; keeping a newline count per piece would remove the scan.
- [ ] **Scrolling must not move the cursor** in the editor.
- [ ] **Terminal paste** should wrap rather than overflow.
- [ ] **Tabs as clickable tags** — visual now, clickable when mouse lands.

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
- [ ] chroma is pinned to v2.14.0 (v2.27 requires Go 1.25).
- [ ] `raj --config macos` must be regenerated and Ghostty reloaded whenever the
  binding table changes.
