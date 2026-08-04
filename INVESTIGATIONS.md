# Investigations

Why things are the way they are: terminal behaviour measured on hardware, root
causes of bugs that are now fixed, and decisions that could reasonably have gone
the other way. Numbers live in BENCHMARKS.md; open work lives in TODO.md.

## Settled and proven on hardware

| | |
|---|---|
| Ghostty `kkp_on` gate | 53/53 chords measured via `cmd/keyprobe -checklist` |
| Gate condition | requires KKP flag 8 (`report_all`); flags 1–7 correctly fall through to Ghostty |
| Suspend | ctrl+z pops KKP, Ghostty takes its keys back, `fg` restores |
| Focus loss | Ghostty hands keys back per-surface; raj needs no work here |
| Theme | OSC 10/11/4 answered, so colours are read from the terminal |
| Text input | KKP flag 16 confirmed; shift+a types `A`, dead keys and non-Latin layouts covered |

## Terminals

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

## Reclaiming chords a terminal keeps

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

## Decisions

### Distributing an external clipboard across cursors

A clipboard from another program cannot splice — its bytes are not in the buffer
— so the only question was whether to distribute it. The numbers did not decide
it: distributing is 11x slower at 64 cursors and 127x the pieces, but 127 us is
a hundredth of a frame and both store the clipboard once. It was a semantics
question wearing a performance costume.

**Decided: distribute when the line count matches the cursor count.** The
alternative puts the whole clipboard at the primary cursor and leaves the other
N-1 cursors doing nothing, which is not a useful outcome under any reading. The
failure mode — a coincidental match producing an unwanted distribution — is rare
and costs one cmd+z. Mismatched counts still insert whole.

### Wrapping: hybrid, not word or character

Hybrid breaks at whitespace like word wrap and additionally at the punctuation
that separates code. Character wrap splits identifiers everywhere. Plain word
wrap produces byte-identical output to character wrap on minified output and
deep paths, because those lines contain no whitespace at all — so it degrades to
splitting mid-token on exactly the code where breaking well matters most. It is
dominated: 10% slower than char for nothing on code, and identical to hybrid on
prose. Hybrid splits mid-token only when a single token exceeds the pane, which
is the case nothing can help.

### Wrapping: Viewport.Top stays a line, plus TopRow

The alternative was addressing the viewport in absolute visual rows, backed by a
global line-to-row prefix sum. That is fast to query and disqualified anyway:
17.4 ms to rebuild per width change is more than a whole frame, repeated dozens
of times during a drag, and 3.8 MB on a 500k-line file.

Recomputing the visible window instead costs about 14 us — 0.084% of a frame —
and is O(pane height) rather than O(document), so it does not grow with file
size. A resize clamps `TopRow` against the top line's new row count and nothing
else needs invalidating.

Worth recording honestly: **performance did not decide this.** The line-based
model is fastest on four of five microbenchmarks and would win the fifth given
the same 1.4 KB window cache. What decided it is that `placeCaret` computing
`row := line - Top` ignores every line that wrapped above the cursor, and
`ScrollTo` comparing lines against a row-sized pane reports a cursor visible
when wrapped rows have pushed it off the bottom. Both need the line-to-row
conversion; adding it piecemeal to each call site is the same design assembled
badly.

The API rule that follows: anything needing row positions takes the breaks as an
argument. The renderer lays each visible line out once while drawing it, and
`placeCaret` reads that same slice. An earlier `WrapRowOf` recomputed the layout
per call at 5 us a time — per frame and per keystroke work to recover something
already in hand.

## Root causes

### The undo/redo corruption

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

### Paste

- [x] **Paste has its own path.** `Pane.Paste` appends once and commits one op.
  Measured below.
- **Paste was never wired up.** `ui.Paste` existed and `app` handled it, but
  nothing constructed one: mode 2004 was never enabled and the decoder had no
  case for the markers, so a paste arrived as a burst of synthetic keystrokes,
  and Ghostty warned about it because raj never advertised that it handled
  pastes itself. The payload is now claimed before `parseCSI`, since the bytes
  between the markers are content rather than parameters: a pasted `5;3R` was
  being read as a cursor-position reply. CR and CRLF fold to LF. An unterminated
  paste surrenders its start marker past 16 MB rather than buffering forever.
- **Paste was gated on the editor**, so pasting into the search box or the
  picker vanished silently — the payload arrives as one event, so the text
  fields never saw it and had nothing to fall back to.
- **Copy published two representations that disagreed.** A whole-line copy
  appended a trailing newline to `Text` but not to the captured `Spans`, so the
  internal splice dropped the newline while pasting the same clipboard
  externally kept it. The two are deliberately not the same byte count with
  several cursors: the `\n` separators in `Text` are structural, carrying the
  cursor split to any other program that reads the clipboard, and `PasteClip`
  splits on them to distribute a foreign clipboard back. Folding the newline
  into each part instead — which looks cleaner — breaks that, because the part
  count no longer matches the cursor count and distribution silently stops.

### Fields had no selection

`widget.Input` had no anchor, so every selecting chord was a silent no-op and
cmd+a *emptied the field*: with nothing to represent a selection, select-all did
the destructive thing that resembled it. `WordLeft` was also aliased to
`prevBoundary`, which walks one UTF-8 rune, so alt+left was character motion
under another name — bound, and therefore invisible as a gap. Adding the anchor
immediately panicked the find bar, which found three places assigning `Text`
directly and leaving offsets past the new end; `Fields.Trim` had that bug for
the cursor already.

### The elastic-tab wrap bug

Retreating to a break opportunity does not guarantee the rune now fits: a tab's
width is elastic, so moving it to a new column can make it **wider**. `" 0\t"`
at width 2 with tab 4 retreats to after the space, recomputes the tab as three
columns, and produces a row four columns wide. The retreat is a loop, not a
branch, with `col == 0` as the termination guard. Found by fuzzing, not by
reading, and kept as a seed.

### The screen thrash

Three stacked causes, all in `Present`:

1. **`prev` was recorded before the write, and the error was discarded** at both
   call sites. A short write left the terminal holding part of the old frame
   while `prev` claimed the whole new one had landed, so every later diff
   skipped exactly the cells that never arrived — two frames interleaved
   character by character, clearing at the next full repaint, which is what made
   it look transient rather than like corrupted state. `os.File.Write` does not
   loop on partial writes; it returns `io.ErrShortWrite`. A near-full-screen
   diff is thousands of bytes, and scrolling with wrapping on produces one on
   every keystroke.
2. **Frames sized for a terminal that no longer existed were written anyway.**
   SIGWINCH updates the size from its own goroutine, so mid-drag the frame in
   hand can be too wide — and a frame wider than the terminal wraps at the real
   right edge, pushing every row down and scrolling the screen. A frame narrower
   only leaves stale cells. That asymmetry is why growing a pane looked almost
   clean and shrinking one did not.
3. **The size guard trusted a cache that goes stale on the path it guarded.**
   `h.cols`/`h.rows` are refreshed only by SIGWINCH, and a stopped process
   services no signals — so across ctrl+z and `fg` the cache holds whatever the
   terminal was when raj went to sleep. `Present` now calls `TIOCGWINSZ`
   directly, which is sub-microsecond and cheaper than one wrong repaint.

### Smaller ones

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

