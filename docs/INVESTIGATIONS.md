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

## Why search does not use SIMD, and what it uses instead

Prompted by GitHub's "Don't stop early: case-folding source code at memory
speed", which reports case folding at >45 GiB/s by deleting the early exit from
the ASCII loop so LLVM auto-vectorizes it. The technique does not port, and the
reason is worth writing down because it will come up again.

**Go's compiler has no auto-vectorizer.** Verified directly:
`go build -gcflags=-S` emits zero vector instructions for every form of the
loop. Without the vectorization payoff, the article's central move — dropping
the early exit — is pure added work. Measured on 177 KB of ASCII, the ladder
inverts against the article's:

| variant | Go, this machine | the article, Rust/M4 |
| --- | --- | --- |
| naive: branch test + early exit | 311 MB/s | 3.1 GiB/s |
| branchless body, keeps the break | 1,098 MB/s | 2.6 GiB/s (slower) |
| branchless, no break (their winner) | 943 MB/s | >45 GiB/s |
| SWAR, 8 bytes per word | 3,083 MB/s | n/a |

Three inversions in one table. Dropping the break is a **regression** in Go.
Branchless-with-break was *faster* than naive here, not slower — Go turns the
range test into a conditional move where the ARM build kept a predicted branch.
And writing the range test as arithmetic instead of a comparison made it slower
still (813 MB/s): hand-rolled bit tricks lose to the compiler's cmov when there
is no vectorizer to feed. The article's own caveat — that a branchless body is
worth it *only* as an enabler for vectorization — turns out to be the operative
sentence for Go rather than a footnote.

So the portable answer is SWAR, and the real answer is usually "call the
stdlib": `bytes.Index`, `bytes.IndexByte` and friends are hand-written assembly
with runtime CPU dispatch. The literal fast path is 11x faster than `(?i)` not
because of anything clever in raj but because it hands the work to
`bytes.Index`.

**Where the remaining headroom is not.** A separate experiment walked a 97 MB
tree with the scan removed entirely: traversal alone is 18 ms, reading every
byte into a reused buffer is 90 ms, and a full search with the fast matcher is
92 ms. The scanning is ~2 ms of 92. Any further work on the matching loop —
assembly, `simd/archsimd`, a smarter fold table — is chasing 2% of a search.
The levers that remain are I/O shaped: not opening the file at all, overlapping
the syscalls, or reading less.

**On `simd/archsimd`.** Go 1.26 shipped it under `GOEXPERIMENT=simd` (amd64
only), and 1.27 adds a portable size-agnostic API plus ARM64. It would let raj
reach the article's numbers without assembly. It is still the wrong trade here
for the reason above, and it carries a real cost: the intrinsics panic on
hardware lacking the CPU features, so callers must feature-test. Revisit only if
a profile ever shows the fold mattering, which on this corpus it cannot.

**Parallelism and `MaxMatches` do not compose.** A worker pool over the same
walk was measured at 509-517 matches against a cap of 500, varying run to run,
because workers in flight when the cap trips still append. Worse than the
overshoot: *which* 500 results you get becomes scheduling-dependent, so the same
query returns different results on consecutive runs. Any future parallel walk
has to either drop the cap in favour of streaming, or collect fully and then
sort-and-truncate deterministically. This was not measurable as a speedup on the
one-core box it was tested on, so it stays unimplemented rather than merged
untested.

**A bug the oracle caught and no benchmark would have.** The first reference
folder used `if r-'A' < 26` on a `rune`. That idiom is correct on a `byte` —
unsigned wraparound puts everything below `'A'` above 26 — and silently wrong on
a signed `rune`, where every control character tests true. It only surfaced
because the fold is fuzzed against an independent oracle. The equivalence test
in `internal/search` exists for the same reason: the literal matcher is checked
against the regexp it replaced, over real source and over random input, rather
than trusted because it looks right.

## Root causes

### Syntax colours stuck describing pre-edit text

- [x] **A boolean cannot say which version it went stale against.** The
  highlighter tracked freshness with `stale`/`running` flags and a `pending`
  text slot. An `Ensure` with nothing to do still filled that slot, so the next
  real edit's pass finished, saw a non-empty `pending`, and re-tokenised that
  older text on top of its own fresh result. It then went idle: the flag was
  clear, the cache was wrong, and nothing was scheduled. The colours only came
  right when the next keystroke happened to kick the cache again, which is
  exactly the "comment it out, uncomment it, the colours are still wrong" report.
  Versions fix it because they say what a cached result *is*, where a flag only
  says whether someone thinks it is old.
- [x] **Asynchronous tokenising means every frame between an edit and the pass
  landing draws stale spans against current text.** That is fine for a colour
  and not fine for an offset: a tab inserted at a line start shifts every token
  on the line by one byte, and the eye reads a one-column colour shift
  instantly. Marking the line unhighlighted instead would flicker for the two to
  three frames a pass takes. Splicing the edit into the spans keeps them on
  their characters, and lets the lag be purely a lag in colour.

### Undo applied at the wrong offset, splitting runes

- [x] **`rebase` left a range put when a later insertion landed at its start.**
  The special case exists so that undoing a deletion composes with an insertion
  at the same point, and for an empty range that is right. For a range with
  width — the bytes some op inserted, which is exactly what `rebasedInverse`
  rebases — it is wrong: the new text pushes those bytes along, so leaving the
  range put makes the undo delete the *new* text instead of the old. With
  multi-byte text the deleted span straddles a rune and the document ends up
  holding half of one, which is invalid UTF-8 on disk if you then save. Fixed by
  restricting the case to `start == end`.
- [x] **A resurrected op made the rebase walk incoherent.** The walk skipped ops
  that were not currently live, but an op's `Pos` is in the coordinates of the
  version it was applied at, and liveness is a property of *now*. Redo can
  resurrect an op that was dead when later ops were recorded, and those later
  ops then had positions in a coordinate system the walk was no longer
  reproducing. Traced at seed 578: op3 inserts 2 bytes at 0 and is undone by
  op4; op5 and op6 are recorded without it; op7 redoes op3 by reversing op4.
  Rebasing op2's position counted op3 (live again) *before* op6, whose `Pos` was
  recorded when op3 was absent — the point landed 2 bytes late, inside a rune.

  Both obvious models were measured and both are wrong. Keeping the liveness
  filter fails the rune fuzz at seed 578. Dropping it and walking the journal as
  the sequence it actually is passes 3000 rune seeds and breaks `undo_test` at
  seed 10, where a pair that cancels out is counted twice and reports a conflict
  against a region neither half still touches.

  The resolution is that these are two questions, and `rebase` was asking one:

  - **Where the range is now** is a question about the journal's *order*. Every
    op in the window counts, live or not, because each op's `Pos` was recorded
    in the frame its predecessors produced. Skipping any op shifts every later
    one into a frame it was never written in — and skipping a cancelling *pair*
    is no better, because the ops between them were recorded with the first one
    present.
  - **Whether the range survived** is a question about the document's
    *contents*. Only an ordinary edit still in effect can destroy it. A reversal
    is never damage to a third party: it removes only bytes its target added, or
    restores only bytes its target removed, so it composes with that target to
    nothing — including where the target's insertion had split the range in two.

  Three mechanisms fell out of separating them, and each has a named test that
  fails when it is broken (see `rebase_test.go`):

  1. **Ends are carried independently.** A deletion can take the bytes under one
     end of a range and leave the other, which an interval has nowhere to
     record. A point whose bytes are removed by an op that was later undone is
     *parked* against that op's reversal and restored at the offset it held
     inside it, rather than clamped and lost.
  2. **Anchors carry a direction.** An offset cannot say which side of a gap it
     is on, and a deletion beginning at a point and one ending at a point look
     identical once the bytes are gone — yet the reversal of each is an
     insertion at the same offset, and they want opposite answers. Each end now
     records the deletions flush against it and which side their bytes were on.
  3. **The `slide` flag is gone.** It existed because a diff hunk and an undo
     wanted opposite answers for an insertion landing exactly on `start`. With
     anchors they no longer disagree: the placement comes from what actually
     happened rather than from who is asking.

  Verified at 40000 rune seeds and 20000 seeds of each undo/redo fuzzer, against
  both the tree and the naive oracle. Standing budgets are lower; the numbers
  above were walked by hand.

### The line index and the document disagreed after a batch

- [x] **`applyToIndex` read the inserted span back out of the document.** That
  is correct for exactly one op — the last one applied. Every caller that
  mirrors a batch (`ApplyDiff` over hunks, `reverse` over an undo or redo group,
  a multi-cursor edit) applies all the ops first and mirrors them afterwards, so
  reading at `op.Pos` returns whatever the *later* ops left there. The index
  then recorded newlines at positions nothing had newlines at, and stayed wrong
  until something rebuilt it. Ops carry their inserted pieces and the stores are
  append-only, so the pieces still describe exactly what that op inserted; the
  document does not. Scanning them is both correct and free of the copy.
- [x] **`File.sync` mirrored only the most recent op.** Right when a session
  call appends one op to the journal, wrong when it appends two, and `applied`
  jumped to the new version either way — so the skipped op was never mirrored.
  It now catches up on everything since `applied`.
- [x] **`scanWord` compared bytes to `unicode.IsLetter`.** A continuation byte
  is not a letter, so word motion stopped inside multi-byte runes and left the
  cursor at an offset the rest of the editor cannot address. Both of these were
  found by the properties in CURSOR-VIEWPORT-SPEC.md within seconds of writing
  them, having survived every example test in the repository.

### Escape never arrived without KKP

- [x] **A lone ESC was held forever, so escape did nothing.** `Parse` returns
  "need more bytes" for a one-byte buffer, which is correct — 0x1b is also the
  first byte of every CSI, and consuming it eagerly would split every sequence.
  Under KKP that never mattered: escape arrives as `CSI 27 u` and decodes like
  any other chord. Under a terminal falling back to legacy encoding it is a bare
  0x1b with nothing after it, so it sat in the buffer until the *next*
  keypress — and then ESC+key decoded as alt+key, which is the second symptom:
  escape appeared to do nothing and also ate the key after it. `Cursors.Clear`
  was wired to `keys.Cancel` the whole time; the action simply never got there.
  The fix is the standard one, a timeout: `ParseFinal` decodes a buffer the
  reader has stopped waiting on, and it differs from `Parse` in exactly this one
  case. 25 ms, ncurses' ESCDELAY. `decodeStream` is the seam that makes it
  testable without a terminal.

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


## Agent/raj ctl usage feedback (2026-09-09)

Notes from an agent driving raj through the control socket, with an eye on
what makes the tool intuitive and where it fights back.

- **Path mapping over TCP is transparent once you trust it.** `raj ctl buffers`
  reports `/work/...` because that is the caller-side root, while the editor
  itself holds `/Users/.../projects/raj/...`. The translation works, but the
  first few commands feel like they are addressing a different filesystem. A
  note at startup or in the first `buffers` output would save the "is this the
  right file?" hesitation.
- **Closed files cannot be read without opening them first.** `raj ctl search`
  happily inspects any file in the workspace, but `raj ctl read` refuses a path
  that is not already an open buffer. The fix is `raj ctl open`, which also
  surfaces the file in the editor — a deliberate coupling, but surprising
  when you come from tools where search and read have the same reach.
- **`-include`/`-exclude` globs match basenames, which is the wrong default
  for an agent.** Scoping a search to one package (`-include "internal/tabs/*.go"`)
  silently returns nothing; the only pattern that works is a file extension.
  This is explicitly the TODO item being fixed this session, and it is the
  single biggest friction point so far.
- **There is no `-path`/`-dir` flag for search.** When you only want hits in one
  directory, the only lever is include/exclude globs, and because they are
  basename-only today that lever does not exist. A `-dir` flag would be simpler
  than path globbing for the common "search under X" case.
- **Error messages are good.** "no open buffer for that path" and the refusal
  reasons in the skill doc are concrete enough to recover from without guessing.
- **Byte offsets in `apply` are powerful but unforgiving.** They are the right
  seam for structural edits, but every hunk needs a fresh `read` and a `-base`
  version, which makes multi-file changes verbose. `raj ctl run -prog` is the
  intended batching answer; learning its opcode encoding is the next hill.
- **`search -json` now carries byte offsets.** Implemented during this session:
  `Match` gained `ByteStart`/`ByteEnd`, set in both the fast scanner and the
  non-ASCII fallback, threaded through `control.SearchMatch` and the wire
  `MatchMeta`, and emitted by `raj ctl search -json`. A driver can now turn a
  hit directly into an `apply` span without recomputing line:col itself.
- **`raj ctl read -start/-end` now returns a byte span.** Implemented during this
  session: the request carries start/end through Header and Request, the host
  clips the piece-table spans to the range, and the CLI accepts the same flags.
  Line ranges are still a gap for locked-down containers.

- **Multi-line `raj ctl edit` is fragile through shell quoting.** Passing long
  `-old`/`-new` strings on the command line failed on the first attempt;
  `-old-file` / `-new-file` with heredocs was reliable. If an agent harness is
  generating edits, writing them to temp files and referencing by path is the
  practical path.
- **Every `raj ctl apply` bumps the version.** The first edit landed at version
  1, the next at 2, and so on. For sequential edits to the same file, either
  batch them with `run -prog` or use `edit` (string replacement) which does not
  require a `-base`. For structural edits that need offsets, re-reading between
  hunks is the safe pattern.
- **`raj ctl open` is required before `read` or `apply`.** Search can inspect any
  file, but reading and editing need an explicit open. That makes sense for a
  UI editor, but for an agent it is an extra round trip per file.
- **Line-range reading wants a native flag.** To inspect a specific region I
  repeatedly piped `raj ctl read` through `sed -n`. A locked-down container
  will not have `sed`; `raj ctl read -line N` or `-lines START,END` would remove
  the dependency entirely and is a better fit for an agent than byte offsets.

## Review and edit modes (Wave D, 2026-09-10)

The review surface is chord-level today: accept, reject and review on chords,
plus the next/prev cycle over pending change sets. There is no mode, so the
document stays editable while you review it, and a stray keystroke inside a
proposed span is what produces the moved-hunk case below.

### Two app-level modes, toggled by `cmd+r`

Edit is today's editor. Review makes the document read-only: movement, scroll,
search, hover and goto all work; every text mutation (insert, delete,
backspace, enter, paste, cut, undo/redo, indent, move-line) is refused with a
status note. The review decisions stay live in review mode — accept, reject,
next/prev, skip — because they are decisions, not edits. Reject removes text,
and that is the point of it. Reload moves off `cmd+r` to `cmd+shift+r`.

Review mode enters at chunk 1 when there are pending sets; entering with none
is allowed (read-only browse) and says "no proposed changes". A one-line keybar
plus a mode badge in the status line carries progress and the shortcuts:
`Review · 3/12 · n/p move · a accept · x reject · A all · s skip · cmd+r edit`.
The socket verb `raj ctl review [path]` enters review mode and returns the
chunk list; `-json` is the read-only list without entering. This is a new verb
(eight layers, wire) on top of a new mode, so it needs a rebuild.

### The moved-hunk case

`DiffPending` (internal/piecetable/groups.go) projects each proposed member op
by rebasing its inserted span forward and requiring it to keep its original
length. A later edit that reaches inside the span fails that test, the member
is counted in `Moved`, and `PendingMarks` drops it. The group is still
`Proposed` with live ops, so `Pending()` still returns it: the gutter letter
and the review tint vanish, accept/reject at the caret does nothing, next/prev
skips it, and the save-review row has no line to jump to — yet cmd+s is still
blocked by it. Invisible-but-blocking. `raj ctl groups` and `raj ctl diff`
report `Moved`; no editor surface consumes it.

Review mode read-only kills the accidental case. For a deliberate edit in edit
mode the options are:

- **A. Adjust (recommended).** Re-project the surviving piece runs of the
  proposal; the region the user typed into becomes theirs; the set auto-rejects
  only when no agent pieces remain. Needs `DiffPending` to project piece runs
  rather than check one length.
- **B. Auto-reject the set.** Simple, but destructive on an additive edit: it
  removes the agent's surrounding text, which is what the user was editing.
- **C. Prompt on first overlap.** Honest, but modal on the typing path.

Open: pick A, B or C.

## LSP inlay hints (option A — inline overlay)

TODO.md calls inlay hints "a renderer project, not an LSP one", and that is
right: the request is trivial and the drawing is everything. The chosen shape is
**option A**, a true inline overlay — hint text occupies cells in the document
flow, and every column map knows it: the caret, selection and highlights agree
for a line that fits one visual row (see "Decision: the wrap fallback"); a
multi-row line draws no hints. End-of-line-only and detached-overlay hints are
rejected: both
draw text the column map does not know about, which is exactly the class of bug
`internal/view/wrap.go` already warns about (a caret on a row the renderer never
drew). One invariant makes it safe to land in stages: **an empty hint set is
byte-identical to today**, so every existing test stays green and the feature can
be built one seam at a time behind a toggle.

### Protocol

`textDocument/inlayHint` takes `{textDocument:{uri}, range:{start,end}}`; the
range is mandatory and is how a client asks only about the viewport. The result is
`InlayHint[] | null`, a hint being:

    position    Position
    label       string | InlayHintLabelPart[]   // {value, tooltip?, location?, command?}
    kind?       1 Type | 2 Parameter
    textEdits?  TextEdit[]
    tooltip?    string | MarkupContent
    paddingLeft?, paddingRight?   bool

Add `inlayHint` to `clientCapabilities()` in `internal/app/lsp.go` under
`textDocument`, as an empty object. Presence is the advertisement; a server sends
hints only when it is there. Do **not** advertise `resolveSupport` (so we never
need `textDocument/inlayHint/resolve`) and do not advertise
`dynamicRegistration`. `labelFormatSupport` does not exist for this request. The
terminal has no rich text, so `label` parts are flattened to a plain string by
joining `value` in order (ignoring `location` and `command`); `tooltip`, a
`string | MarkupContent`, is flattened the same way — markup reduced to its plain
text, exactly as the caret-driven hover panel reduces it today — and kept for the
mouse-hover tooltip. Both `kind` 1 (type) and `kind` 2 (parameter) hints are
shown; a kind filter is one setting or one constant, not a protocol decision.
`textEdits` are decoded inline — with `resolveSupport` unadvertised a server sends
them with the hint or not at all — and kept for the `cmd+.` apply path below
rather than discarded.

### Data model

`internal/lsp/inlay.go` (new): `InlayHint{Pos Position; Text string; Kind int;
PaddingLeft, PaddingRight bool; Tooltip string; Edits []TextEdit}`, the
`inlayHint` wire struct, `labelText`, a `tooltipText` that flattens `string |
MarkupContent` the way hover does, and `RequestInlayHints(ctx, c, path, r)
([]InlayHint, error)` beside `RequestHover` in `requests.go`. The `textEdits`
are decoded inline; `resolveSupport` is not advertised, so a server either sends
them with the hint or omits them. `internal/editor/hints.go` (new): the
renderer-facing model, deliberately in `editor` so the editor never imports
`lsp`:

    type Hint struct{ Off int; Text string; Left, Right bool; Kind int;
                       Tooltip string; Edits []HintEdit }
    type HintEdit struct{ Start, End int; Text string }   // byte offsets
    type HintSet struct{ byLine map[int][]Hint }   // sorted by Off

`At(line)` returns the slice for a line (nil fast path); the app converts each
`lsp.InlayHint` to a line-relative `editor.Hint` when it installs, turning each
LSP `TextEdit` into byte offsets with `lsp.Document.Span` exactly as completion
`textEdits` are converted, so `editor` stays free of `lsp` imports. The layout
engine only needs widths, so `view` gets a minimal `view.HintCol{Off, Width int}`
and the column/wrap functions take `[]view.HintCol`, avoiding a `view -> editor`
dependency.

### App lifecycle and cache

Mirror the diagnostics store, not the single-answer slot. New
`internal/app/inlay.go`: `inlayStore{byPath map[string]entry}` with
`entry{version int; hints []editor.LineHint}` and `set`/`forPath`/`clear`,
guarded by a mutex. A stored hint is a `LineHint` — a `Hint` beside the line it
anchors on, since its `Off` is line-relative. The cache
is keyed by path **and document version**, because a hint from a version
the buffer has left must never reach the renderer — a stale hint does not merely
look wrong, it moves every column after it.

Hints live on `File` (`File.Hints *HintSet`), installed and cleared on the event
thread. `maybeRequestHints()` runs only from the idle `ui.Tick` case in
`App.Handle` (the existing debounce), and only when the app is in edit mode, the
pane has hints enabled, the path is non-empty, and the requested range or the
document version changed since the last request. The editor's default range is
the visible line range plus a half-screen margin; an agent may override it over
the socket (see "Agent access" below), and the pane then requests what the agent
asked for. Either way it converts both ends with
`lsp.NewDocument(p.File.Text()).Position(...)`, gets the server with
`servers.for_(path, notify)` (which also starts it lazily), calls `syncDoc`, bumps
`a.inlayGen`, and calls `lsp.RequestInlayHints` inside `safe.Go` with a 2 s
context, parking the result and posting `ui.Wake` exactly as `hover()` does. On
the Wake, `applyAnswer` routes an `answerInlay` against `a.inlayGen`; the parked
answer carries path and version, and `applyInlay` installs only if the path is
active and the version still matches. Invalidation is the important half: any
edit (typed key, paste, undo/redo, accept/reject) clears `File.Hints` and bumps
`a.inlayGen` before the next draw, so stale columns cannot survive even one
frame; `closeDoc` calls `inlays.clear(path)`. Files: `internal/app/inlay.go`
(new), `internal/app/lsp.go` (capability, `closeDoc`, answer kind),
`internal/app/app.go` (fields, Tick hook, edit hook), `internal/editor/file.go`
(`Hints`).

### Agent access: `lsp inlay-hints`

`raj ctl lsp` gains an `inlay-hints` mode: `raj ctl lsp inlay-hints <path>`
returns hints for the whole file, and `-lines A,B` (mirroring `read -lines`,
1-based inclusive) restricts it. The result JSON mirrors the other `lsp` modes —
1-based `line`/`col` positions in the editor's own coordinates, plus `text`,
`kind`, `paddingLeft`/`paddingRight`, `tooltip` and `textEdits` — so a driver
never parses a server payload directly. This is a range parameter on the LSP
request and the control host threaded down to `lsp.RequestInlayHints`: the
request already takes a `range`, but the control host, the `BufferHost` interface
and the CLI have no way to pass one for this verb yet, which is why it is its own
implementation step (step 8) rather than part of the editor-side fetch. The
editor's own `maybeRequestHints` keeps the viewport default; the verb exists for
whole-file audits and for verifying hint and column behaviour from a test.

### Renderer integration (the crux)

The column model becomes: `col(off) = baseCol(off) + sum(width(hint))` for hints
anchored strictly before `off` (a hint at the boundary is drawn starting there,
with the caret before it); the inverse subtracts the same sum, and a column
landing inside a hint resolves to that hint’s anchor offset, never a byte inside
it. LSP positions are untouched: they stay UTF-16 over bytes
(`internal/lsp/position.go`), and only display columns change.

This is the SHIPPED scope: hints are placed through the hint-aware column map
for lines that fit a single visual row (see "Decision: the wrap fallback"
below). There is no hint-aware wrap; a line that wraps to more than one visual
row contributes no hints and its layout is byte-identical to today. The
functions that consult the hint model:

- `internal/view/columns.go` `HintCol` and the hint-aware `ColOfHints`,
  `OffsetOfHints` and `WidthHints` -- the pure conversions. The boundary
  convention is that a hint at `Off` draws starting there, so the caret sits
  before it.
- `internal/editor/file.go` `File.LineCol` and `File.OffsetAt` -- the chokepoints
  the caret, motion, mouse and hover anchor already go through, hint-aware for
  the single-row case.
- `internal/editor/wrap.go` `cursorRowCol`, `moveVerticalWrapped` and
  `offsetInRow` -- the row/caret helpers, hint-aware only for a fitting
  single-row line.
- `internal/editor/render.go` `drawLine`/`drawHint` -- `Hint.Width` advances
  `col` by the hint width and the hint is painted with `Theme.InlayHint`; the EOL
  secondary-cursor cell uses the hint-aware width.
- `internal/editor/mouse.go` `OffsetAt` -- the inverse; a click inside a hint
  clamps to its anchor, so the caret cannot land inside one.

`internal/view/wrap.go` is deliberately untouched: it has no hint-aware
functions. A hint occupies columns without a byte, so a hint-only row cannot be
represented in a byte-offset row model (see "Decision: the wrap fallback").

Hint text and padding order at the anchor: `[space if paddingLeft][text][space
if paddingRight]`, one atomic cell run whose width is the display width of the
text (`ui.RuneWidth`, not `len`) plus the padding. A line whose hints push it
past one visual row is dropped as a whole, hint run included; the wrapper never
sees a hint.

Selection and find are tested per byte (`inAny(sel, off)`,
`Find.Highlight(off)`), so a hint cell, which has no byte, is never painted with
a selection, find or author tint. Requirement (b) falls out of the model rather
than needing new checks.

### Decision: the wrap fallback

Full wrap integration was attempted and ABANDONED. The root cause is structural,
not a matter of effort: the wrap/row model in `internal/view/wrap.go` is a list
of BYTE offsets, and a hint occupies columns with no bytes. A row holding only a
hint therefore has no byte to anchor it: the layout emits a duplicate break at
the same byte (an empty row), and `RowOfBreaks` maps that byte to the text row,
leaving the hint-only row unreachable by the renderer. Every one of
`AppendWrap`, `WrapRows`, `RowOfBreaks` and `OffsetAtRow` would have to agree on
a row no byte identifies, which is exactly the caret-on-an-undrawn-row failure
the wrap code already warns about.

The shipped design is the per-line fit fallback. The hints on a line are kept
only when the whole line, hints included, fits one visual row:

    WidthHints(line, hintCols) <= p.TextWidth()

otherwise that line contributes no hints. `editor.HintsThatFit` decides,
`File.SetHintsFiltered` and `File.HintWidth` hold the filtered set on the file,
and `App.fitHints` re-filters from the store when the pane width changes, called
from `drawEditor`. The hint-aware `internal/view/wrap.go` additions were
reverted; `view` has NO hint-aware wrap functions, and that is deliberate. The
single-row hint-aware column mapping (step 4a) is all the wrap path needs, and
the row/caret helpers that do touch hints (`cursorRowCol`, `moveVerticalWrapped`,
`offsetInRow`) handle only the fitting single-row case. The old `!p.Wrap` gate
is gone, so hints show with wrapping ON for every line that fits; a multi-row
line simply shows none. That is a real limitation of the shipped design, stated
here rather than left to be discovered.

### Interaction with the rest

Diagnostics are gutter marks (`app.drawDiagnosticMarks`) keyed by line plus a
status-line summary (`diagnostics.atLine`); neither touches columns, so a hint on
a diagnosed line is indifferent to the mark. There are no diagnostic underlines
today; if one is added it is a byte span and must skip hint cells for the same
reason selection does. Search highlights move with the hint because `drawLine`
advances `col` by the hint width, and find's jump uses the hint-aware
`File.LineCol`. The hover panel is anchored with `p.File.LineCol(head)` in
`applyHover`, so it keeps tracking the text and `Panel.Render` needs no change;
the mouse-hover hint tooltip shares the same `hover.Panel` (see below), so any
action that hides the caret panel hides the tooltip too, and the two must not
both claim the panel in one frame.
Copy and selection read byte ranges from the buffer, so hint text can never be
selected or copied. Performance is preserved because hints are per-line and
range-scoped: `RenderFocused` asks `HintSet.At(line)` for the same visible lines
it already draws, the fetch covers only the viewport (or the agent's requested
range), and no frame ever walks the file.

### Tooltips (mouse-hover)

The one thing the caret cannot reach is the thing a tooltip hangs off: a hint
occupies cells no caret may enter, so the existing caret-driven hover path can
never anchor one. The mouse can. `internal/keys/mouse.go` already decodes a bare
move as `Mouse{Motion: true, Button: MouseNone}` and `internal/ui/host.go`
delivers it as `ui.Mouse`; `internal/app/app.go`'s mouse handling and
`internal/app/pointer.go` act only on wheel and clicks today, so the motion event
is received and ignored. No terminal or protocol change is needed — only an
app-side motion path.

On a motion event, map the pointer's `(Col, Row)` to a visible hint cell: row to
line through the wrap map (`Layout{EditorX, EditorW, TabY, TopY, Rows}` plus the
editor's row/line functions), the hint's display span from the hint-aware column
map including its padding. After a short dwell on the same cell, show the hint's
`Tooltip` in `hover.Panel` via `Show(text, line, col)` anchored at the hint.
Hide it on movement off the cell, on scroll, on typing, and on any action —
`Panel.Handle` already hides on any action, and motion needs its own hide-on-leave
because the caret never moved. Because the whole path reads the hint-aware
column map, it is a late step: it lands after the column map (step 4). There
is no hint-aware wrap to wait on (see "Decision: the wrap fallback").

Hint tooltips are mouse-only. A hint cell is not a caret position, so there is no
keyboard anchor to hang a tooltip on; the caret hover path keeps serving symbols
and is deliberately not repurposed, because showing a hint's tooltip from a caret
that cannot rest on the hint would make one panel answer two different questions.
A frame with no mouse simply gets no hint tooltips, and that is the documented
fallback.

### Applying a hint's `textEdits` (`cmd+.`)

A hint's `textEdits` are applied, VS Code quick-fix style. The chord `super+.`
(native, CSI-u `46;9u`; mac `cmd+period`, Linux `ctrl+period`) bound to an action
named `ApplyInlayEdit` finds the hint nearest the caret on the current line, takes
its `Edits`, and applies them through the same range-replacement machinery
completion edits use, as one undo step. The edits are already byte-offset
`editor.HintEdit`s, converted when the hint was installed, so no LSP coordinate
conversion happens here. When there is no hint on the line, or the nearest hint
carries no edits, the action is a no-op that reports through the status line
rather than silently doing nothing. In review mode, where mutations are refused,
it is likewise refused. The accounting tests
(`TestEveryKeymapChordIsAccountedFor`, `TestBindingsRoundTrip`) and the
`KEYBINDINGS.md` row apply to this chord exactly as to the toggle.

### Toggle and UX

`App.InlayHints` is default on, inherited by panes like
`WrapDefault`/`AutoPairs` and persisted as `session.Pane.Hints`; on matches the
existing hover and diagnostics defaults and makes the feature discoverable. Both
kind 1 (type) and kind 2 (parameter) hints are shown, with a kind filter
available as one setting or one constant. The toggle chord is `shift+super+i`
(native, CSI-u `105;10u`; mac `cmd+shift+i`, Linux `ctrl+shift+i`), following the
table's existing `shift+super+<letter>` / `ctrl+shift+<letter>` pattern. Add
`keys.ToggleInlayHints` to `keys/action.go` and a `Natives` row in
`keys/table.go`; `TestEveryKeymapChordIsAccountedFor` and
`TestBindingsRoundTrip` enforce the accounting, and `KEYBINDINGS.md` gets the
row. Hint cells use a dedicated `Theme.InlayHint` style (dim/italic) so they read
as annotations and not as document text; nothing else advertises that they are
non-editable, because nothing can — the caret cannot enter them, selection and
find never cover them, and a marker glyph would itself have to be
width-accounted.

### Step plan

Steps 1-5 have shipped; the remaining five are listed after them.

Shipped:

1. Protocol and capability. `internal/lsp/inlay.go` (`InlayHint`,
   `RequestInlayHints(ctx,c,path,r)`, `labelText`, `tooltipText`);
   `textDocument.inlayHint` advertised as an empty object in
   `clientCapabilities()` (no `resolveSupport`).
2. Hint model and store. `internal/editor/hints.go` (`Hint`, `HintEdit`,
   `HintSet`, `LineHint`; `File.Hints`, `HintsAt`/`SetHints`/`ClearHints`);
   `internal/app/inlay.go` (`inlayStore` keyed by path+version).
3. Fetch lifecycle. `maybeRequestHints` on the idle tick (visible range plus a
   half-screen margin), `answerInlay`, `applyInlay`, `invalidateHints`;
   generation and version drops.
4. Column map. `internal/view/columns.go` `HintCol` and the hint-aware
   `ColOfHints`/`OffsetOfHints`/`WidthHints` (boundary convention: a hint at
   `Off` draws starting there, caret before it); `Hint.Width`/`HintCols`;
   `File.LineCol`/`OffsetAt` hint-aware; `drawLine`/`drawHint` paint with
   `Theme.InlayHint`; `mouse.OffsetAt` clamps inside a hint to its anchor.
5. Wrap integration - ATTEMPTED AND ABANDONED in favour of the per-line fit
   fallback (see "Decision: the wrap fallback"). `editor.HintsThatFit`,
   `File.SetHintsFiltered`/`HintWidth`, `App.fitHints`; the hint-aware
   `view/wrap.go` additions were reverted. Hints show with `Wrap` on for every
   line whose hints fit one row; multi-row lines show none.

Remaining:

6. Toggle chord, persistence and docs. `keys.ToggleInlayHints` plus a
   `keys/table.go` row: native `shift+super+i`, CSI-u `105;10u`, mac
   `cmd+shift+i`, linux `ctrl+shift+i`; persisted as `session.Pane.Hints`; a
   `KEYBINDINGS.md` row; the accounting tests. (`App.InlayHints` defaults true
   and `p.Hints` already exists.)
7. Cross-cutting tests. Hover anchor, find-after-hint, selection-over-hint.
8. Agent `lsp inlay-hints` verb and range. Whole file, `-lines A,B`; the eight
   control layers plus the range plumbing.
9. Mouse-hover tooltips. `internal/app/pointer.go` motion path, dwell and
   motion dispatch, the row-to-line and hint-cell lookup, and `internal/hover`
   `Panel` reuse; depends on the column map, not on wrap integration.
10. `cmd+.` apply-hint-edit. `keys/action.go`, `keys/table.go`, `app.go`
    `handleGlobal`, the completion-edit machinery, and the status note; the
    chord accounting and the `KEYBINDINGS.md` row.

### Decisions

- Dwell before a tooltip appears is 300 ms (two idle ticks), long enough not to
  flash on a pass and short enough not to feel broken. A named constant.
- A hint with an empty `Tooltip` shows no panel, matching VS Code: the label is
  already visible inline, and only a server-supplied tooltip adds anything.
- `cmd+.` is caret-adjacent only, matching VS Code's quick-fix action: it applies
  the nearest hint's edits on the current line. A whole-line or whole-file apply
  is not offered.
