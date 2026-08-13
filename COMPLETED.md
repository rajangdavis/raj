# Completed

What works and what has been fixed. Open work lives in TODO.md; measured numbers
in BENCHMARKS.md; root causes and decisions in INVESTIGATIONS.md.

Entries here are one line each. Where a fix had a non-obvious cause worth
remembering, the explanation lives in INVESTIGATIONS.md rather than being
repeated.

## Working

Typing, motions, selections, multi-cursor (cmd+alt+up/down), undo/redo, new
files (cmd+n), save and save-as,
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
- [x] **New file (cmd+n)** — an empty unnamed buffer in a new tab. Two presses
  mean two buffers: `Tabs.Open` dedupes by path and an unnamed buffer has none.
- [x] **cmd+w asks before discarding.** A dirty tab now stops for Save / Don't
  Save / Cancel, and the Save answer closes the tab only once the bytes are
  actually on disk — a cancelled save-as leaves the tab open rather than
  discarding exactly the work the answer asked to keep.
- [x] **Save-as.** cmd+s on an unnamed buffer asks where to put it, seeded with
  the workspace root; a relative answer resolves against the root rather than
  the process's working directory. An existing file gets an overwrite check
  first. Naming the buffer rebuilds the highlighter, so a scratch file saved as
  `.go` colours immediately.
- [x] **`internal/prompt`** — the modal seam all three of the above needed. Two
  shapes (a text question, a button choice) and a continuation rather than a
  return value, because the interesting flows are chains: close asks whether to
  save, which asks for a path, which may ask about overwriting, and only then
  closes the tab. The dialog closes itself *before* delivering an answer, so the
  next question in a chain does not open underneath the one it replaces.
- [x] **The rebase coordinate frame.** `Session.rebase` was asking one question
  where there were two: where a range is now (the journal's order, which counts
  every op) and whether it survived (the document's contents, which only live
  edits can damage). Splitting them fixed the last of the rune-splitting bug,
  removed the `slide` flag as redundant, and replaced a fuzz seed with five
  deterministic tests — one per mechanism, each verified to fail when its
  mechanism is broken. Full account in INVESTIGATIONS.md.
- [x] **A finished search no longer waits for the tick.** Results are installed
  on the event thread, and nothing woke that thread, so a search finishing
  during a typing pause sat on its answer for up to 150 ms. `Host.Post` and a
  payload-free `ui.Wake` close it — one event for all background work rather
  than one per producer, which is the seam the agent pane needs for the same
  reason. A superseded walk stays quiet, since waking the loop to repaint an
  answer that was thrown away is worse than not waking it at all.
- [x] **cut and copy follow focus.** Both are global actions, claimed before any
  pane sees the chord, so they always acted on `Tabs.Active()`: cmd+c in the
  search box copied from the editor and cmd+x edited the document being
  searched. `app.focusedInput` now asks the focused thing whether it owns a
  field — including the find bar, which lives inside the editor pane and so is
  invisible to focus alone, and the modal dialogs, which had to stop swallowing
  the clipboard chords for it to reach them. Cut with nothing selected in a
  field does nothing rather than emptying it: there is no "current line" to fall
  back on the way a document has.
- [x] **go-to-line (ctrl+g)**, on the dialog seam the save-as work added — which
  is most of why it was cheap. It takes `120`, `12:4` and `:3`, because a
  compiler, a linter and a stack trace all print line:column and pasting one in
  should not need editing down; a bare column means the line already showing,
  and the field is seeded with where the cursor is, so the dialog answers "where
  am I" as well as asking "where to". Out-of-range clamps rather than refuses,
  and the clamping is the view layer's — `LineStart`, `Center` and `OffsetAt`
  all already do it, so repeating it here was deleted as untested duplication.
- [x] **Quit asks about unsaved changes.** cmd+w already guarded one tab, which
  made the guard a property of which chord you pressed rather than of the
  buffer — and quit is the one with every unsaved tab behind it. One dialog:
  the file is named when there is one and counted when there are several, since
  a list of names does not fit and a bare count withholds the only useful detail
  when only one thing is at stake. Save walks every dirty tab, focusing each
  before saving so a save-as dialog is asking about the buffer on screen, and
  recursing through the continuation rather than looping — a loop would have run
  to the end before the first dialog was answered. Cancelling a path cancels the
  quit. A second ctrl+c while the question is up forces the exit, because ctrl+c
  is what people press when they want out now and answering it by asking again
  is the wedge the modal was written to avoid.
- [x] **shift+tab leaves a sidebar.** Both panes stopped it at their first
  component, so the only way back to the editor from there was tabbing all the
  way forward or a chord. Decided against wrapping the ring: forward already
  exits, so wrapping would make backwards mean something different from forwards
  and land focus at the far end of the pane rather than out of it. Each pane is
  now a ring segment with an exit at both ends. The constraint the dead end was
  protecting is untouched — tab indents in the document, so coming back is still
  a chord, and shift+tab outdents once focus is in the editor.
- [x] **The search pane no longer vanishes on a short terminal.** `Render`
  returned early below twelve rows, so the sidebar opened onto nothing at all —
  not even a border, which reads as a redraw bug rather than a size one. It now
  sheds the glob fields and toggles first, since those are set once and left
  alone while the query and its results are why the pane is open, and says
  "too short" rather than nothing when even the query will not fit. The focus
  ring agrees with the layout: tab skips components that are not drawn, and
  shrinking pulls focus back to the query rather than leaving it on a field that
  is no longer on screen.
- [x] **A per-file match cap.** `MaxMatches` was 500 across the whole search and
  the walk is lexical, so one early file could spend the entire budget before
  the rest of the tree was seen: `te` reported 500 results in 6 files, all at the
  repository root and 200 of them from LICENSE alone, while the rarer `testing`
  reported 361 in 34 files purely because it never hit the cap. The file count
  said more about walk order than about the query, and was least informative
  exactly when the term was most common. Now `MaxPerFile` is 20 and the global
  cap is 2000, so no single file eats the budget and the walk usually finishes.
  Scanning continues past the per-file cap to count — matching is around a
  thirtieth of a search's cost, so the true number is close to free, and the
  pane reports it: "22 of 82 results in 3 files" at the top and "(20 of 80)" on
  the file that was cut down. Without that the cap would have traded a
  misleading number for a quietly missing one. Asserted against a fixture where
  a lexically-first file holds four times the cap and the interesting content is
  in a directory the walk reaches afterwards.


## Dialogs and paste

- [x] **The go-to-line seed is a default, not a prefix.** ctrl+g seeded the
  field with the current line and left the caret at the end, so typing
  appended: on line 1, typing `30` asked for line 130 and clamped to the last
  line. Every test that exercised the dialog pressed cmd+a first, which is
  exactly the keystroke the seed was supposed to save, so nothing noticed. The
  seed is now selected, so the first keystroke or paste replaces it and a bare
  enter still accepts it. `Ask` keeps caret-at-end and `AskSuggestion` selects,
  because the two callers want opposite things: save-as seeds a directory you
  type the rest of, and selecting that would be wrong.
- [x] **A pasted path finds the file.** Paste already reached the picker, so the
  field filled — but the fuzzy score is a subsequence test, and one byte the
  path does not contain empties the list. An absolute path, a `./` prefix or a
  `:464:12` suffix from a compiler or `grep -n` all filled the field and matched
  nothing, which reads as the paste having been dropped. `Picker.Paste` now
  treats the payload as a hint and narrows it — as pasted, then without its
  position suffix, then relative to the root, then the base name — stopping at
  the first form that matches anything. When none match the original is
  restored: a picker showing a base name that also matched nothing would
  misreport what it searched for. Only an all-digit tail is taken as a position,
  and at most two segments of one, because a colon is legal in a file name.
  The picker had no paste coverage at all before this.
- [x] **The picker opened a blank buffer.** Its index is relative to the
  workspace root — that is what the list shows and what the fuzzy score is tuned
  against, since an absolute prefix is the same bytes on every entry and only
  dilutes the ranking. But the relative path was handed straight to `OpenFile`,
  which resolves against the process working directory. Running `raj .` from the
  root made the two the same and hid it; `raj ~/code/thing` from anywhere else
  meant every pick opened an empty buffer named after the file, with no error,
  and saving it would have written a new file next to the shell. `Picker.Resolve`
  is the seam between the two representations. Found by a position test that
  expected line 3 and got line 1 — the buffer was empty. The explorer and the
  search pane were already handing over absolute paths, so the bug was the
  picker's alone.
- [x] **A pasted position is honoured, not just stripped.** The narrowing above
  parses `:464:12` off a pasted path so the path can match, and then threw it
  away — so pasting a compiler line found the file and opened it at the top,
  which is the half of the job that is easy to mistake for the whole of it. The
  picker now carries the position alongside the query and hands it back when the
  chosen file is the one the paste named. That last clause is the fiddly part:
  narrowing to a base name *widens* the query, so `app.go:464` in a tree with
  two app.go files must offer 464 to either of them and to nothing else. The
  rule is a suffix match, which is exact when the paste was already relative and
  base-name when narrowing went that far. Typing detaches the position, because
  what is being looked for is no longer what was pasted.
- [x] **go-to-symbol (cmd+shift+o)**, on the same overlay as cmd+p rather than
  beside it. The two are one widget with different rows: splitting them would
  have meant a second overlay, a second focus state, a second keymap scope and a
  second renderer, all to filter a list of strings as you type, and the two
  would have drifted. What actually differs is where the rows come from and what
  choosing one means, which is two fields. A chosen symbol answers with the file
  it lives in and a line — the same shape a pasted path already had — so opening
  and jumping reuses the path that was already there rather than adding one.

  The scanner is a line scan over leading keywords per extension, not a parser:
  Go restricted to column zero because a list containing every closure is a list
  nobody reads, Python and Ruby not restricted because that is where their
  methods live, longest keyword winning so `export default function f` is a
  function rather than an export named "function", and the keyword required to
  be a whole word so `constant` is not a `const`. Markdown gets its headings,
  indented into an outline, since documents are where jumping to a named place
  is most useful and where no declaration keyword exists. Fuzzed for the
  invariants the overlay depends on — non-empty name, line in range, file order,
  and the name actually present on the line it points at — because the failure
  mode of a scanner like this is not a crash but a row that jumps somewhere it
  did not promise. 0.42 ms on 188 KB, so it rescans on open rather than caching:
  the invalidation logic would cost more than the scan it saves.
- [x] **Search reads the buffers, not only the disk.** A workspace search
  opened files and scanned them, so what was on screen and what was searched
  were different documents: an unsaved edit was invisible, and a match the pane
  did report could be stale — sending you to a line that no longer said that.
  Both halves matter, and the second is the one easy to forget when the fix is
  framed as "also search the buffers".

  `search.Docs` is a snapshot of the dirty documents, keyed by absolute path,
  taken on the event thread when a search is scheduled and handed to the worker
  by value. A live view would have been a race by construction: the search runs
  off the event thread, and reading a piece table while the keystrokes that
  provoked the search are still editing it is exactly the bug the debounce
  makes likely rather than rare. Only dirty buffers are snapshotted, since a
  saved one and its file are the same bytes and copying it would cost what
  reading it would have.

  Two things fell out of it. A document with nothing on disk behind it is not
  on the walk at all, so it is swept up afterwards, in sorted order — a result
  list that reshuffles between identical searches is worse than one that is
  merely incomplete. And an open document is filtered on the same terms as a
  saved one: globs, hidden paths, `vendor`, `node_modules`, the size cap and
  the NUL-byte binary rule all apply to a buffer too, or a glob would mean one
  thing for the file being edited and another for its neighbours.

  The substitution seam tests use stayed at three arguments: the snapshot is
  bound by a closure rather than added to the signature, so the ten tests that
  replace the search wholesale needed no change. Asserted with the race
  detector as well as by behaviour.
- [x] **The file picker misranked exact names.** Typing `buffer_test.go` put
  `internal/app/search_buffer_test.go` above the file actually called
  `buffer_test.go` — which is the single most common thing anyone does in a
  picker, so it read as the matching being broken rather than as a ranking
  detail.

  The scan was greedy from the left over the whole path, so a query character
  could be spent on a directory: the `b` of the query went to the `b` in
  "piecetable", splitting the real name into a run of 1 and a run of 13, while
  the other path had no `b` before "buffer" and matched as one clean run of 14.
  Contiguity is quadratic and unbounded, so a longer accidental run outscored an
  exact name no matter how large the name bonus was.

  The match is now anchored to the file name whenever the query fits inside it,
  and falls back to the whole path otherwise. That makes the comparison fair
  rather than adding another bonus to outweigh it: both candidates are scored
  over their names, both as one run, and the prefix and equality bonuses decide
  it as they were always meant to. A query holding a separator, or one whose
  letters are not all in the name, still matches across the path, which is what
  keeps `picker/picker` and directory fragments working.

  Measured rather than argued: typing each of the 125 file names in this
  repository ranked the wrong file first once before and never after. The spec
  is a fixture tree carrying the same collisions.
- [x] **Auto-indent on newline, and brackets that close themselves.** The rule
  everything here is held to: typing must never lose a keystroke. Every
  behaviour either does the obvious thing or does nothing, and doing nothing
  always means the plain character is inserted — a clever guess that eats a
  bracket is worse than no guess, because the second is at least predictable.

  A newline carries the current line's leading whitespace verbatim, so a file
  indented with tabs stays that way, and the cursor lands after the indent
  rather than before it. Between a bracket pair it opens a block: the closer
  moves to its own line and the cursor waits on an indented line between them.

  The pairing rules are all about ambiguity. Typing a closer where that closer
  already sits moves over it, because otherwise auto-closing fights the muscle
  memory of typing the closing bracket yourself. An opening bracket before a
  word character does not close, since typing `(` in front of a name means
  wrapping it and the closer would land mid-word. Quotes are stricter than
  brackets because a quote is its own closer and cannot be told apart by
  looking: a quote adjacent to a word character is an apostrophe and is left
  alone, which is the difference between this feature being liked and hated —
  `don't` and `it's` are far more common in a buffer than a single-quoted
  string. A third quote in a row does not add a fourth, because that is a
  docstring or a fence, not a pair. Backspacing over an empty pair removes both
  halves, so a bracket typed and reconsidered costs one keystroke rather than
  two. Typing a bracket or quote with a selection wraps it.

  Only the keystroke path goes through any of this. A paste, a distributed
  paste and an agent edit all keep their exact bytes: typing a bracket is a
  keystroke, pasting one is data. The invariant is fuzzed rather than argued —
  arbitrary buffers and arbitrary keystrokes, asserting the document never
  shrinks, the typed character always survives, and the cursor stays
  addressable, since an off-by-one here is a panic on the next keystroke.
- [x] **Wheel scroll.** It did nothing in the alternate screen, and the reason
  was that raj never asked for mouse reporting: a terminal that has not been
  asked sends wheel notches to its own scrollback, which the alternate screen is
  not part of. The iTerm2-specific workaround was an application default rather
  than a profile key, so the generated profile could not reach it; asking for
  reporting works everywhere and needs no setup step.

  DECSET 1000 with 1006, not 1002 or 1003. Those add motion reports, which
  arrive on every cell the pointer crosses and would be decoded and discarded
  thousands of times a minute until drag-to-select exists. 1006 is the SGR
  encoding, and it is the only one decoded: the original packs coordinates into
  single bytes offset by 32, so column 224 is unrepresentable and the parameters
  cannot be told apart from arbitrary text. A malformed report is rejected
  rather than read as a click at the origin — mouse reports share the stream
  with text, and misreading one would move the cursor on garbage.

  The wheel scrolls whatever is under the pointer rather than whatever has
  focus, which is what a pointer is for: reaching something without going there
  first. An open overlay takes the wheel wherever the pointer is, since it is
  drawn over everything and scrolling the pane behind it would be scrolling
  something invisible. The cursor does not move — scrolling is looking, not
  navigating, and dragging the cursor along would change what the next keystroke
  edits.

  The bug worth recording is the one the first routing test found. Every pane
  called `list.Follow` when it drew, which pulls the view back to the selection
  — so a wheel scroll was undone before it ever reached the screen, every frame,
  invisibly from inside the list. `List.Scroll` now detaches the view until the
  selection next moves, and render paths call `Settle` instead of `Follow`.
  Moving the selection reattaches, because the keyboard is an explicit statement
  about where attention is and it outranks wherever the wheel left the view.
- [x] **Pasting did nothing, and it was the decoder all along.** The reader
  waits when a sequence looks incomplete, because a lone ESC is the escape key
  rather than the start of something — and a buffer that could not be resolved
  even after waiting was dropped, so a truncated sequence could not wedge the
  reader. A paste in progress landed on exactly that path and was discarded
  whole, without a trace.

  Which is why the symptom was so confusing. A paste small enough to arrive in
  one read parsed immediately and worked; anything large enough to be split, or
  slow enough to straddle the 25 ms wait, vanished. That made it look like the
  picker, then like bracketed paste being off, then like the terminal.

  The fix is one distinction the reader was missing: a paste start marker is
  not ambiguous. Every other partial sequence can be resolved by silence, but
  nothing except a paste begins with that marker, so waiting is always right
  and giving up never is. `PastePending` says so, and the ceiling that already
  bounded a runaway paste is what keeps waiting safe.

  Confirmed by elimination rather than by guessing. The wheel work landed first
  and worked, which proved the host, the decode loop and the event pipeline were
  all sound — same reader, same dispatch, same channel — and left the fault
  somewhere only pastes reach. Two earlier attempts had aimed at the picker and
  at the narrowing, and reverting one of them changed nothing, which was the
  first real evidence that the payload was never arriving at all.

  This also makes the narrowing and the pasted-position work live for the first
  time: pasting `app.go:464:12` into cmd+p now finds the file and opens it at
  464:12, which is what those patches always claimed to do.
- [x] **The picker rewrote the query under you.** Pasting `app.go:464:12` left
  `app.go` in the field: narrowing was implemented as an edit to the input, so
  the position disappeared as you watched and it looked as though the paste had
  been mangled. The position was in fact still held and still applied on enter,
  which made the behaviour worse rather than better — the editor was doing the
  right thing while showing you that it had not.

  Narrowing is a matching strategy, not an edit. It moved into `filter`, where
  the query as typed is always tried first and the narrowed forms are only a
  fallback when nothing matches at all. The field now shows exactly what
  arrived.

  Doing it in `filter` also means it no longer depends on how the bytes got
  there. Typing `app.go:464:12` narrows the same way pasting it does, which
  matters because a terminal that does not honour bracketed paste delivers a
  pasted path as individual keystrokes and the editor cannot tell the
  difference. A query that matches on its own is never narrowed, so a file whose
  name genuinely contains a colon stays reachable.
- [x] **Buffer-word completion.** Not IntelliSense, and the package says so:
  a suggestion means "this string exists in a buffer you have open" and nothing
  more. It knows nothing about types, scope or imports, and it will suggest a
  word from a comment as readily as a function name. What it does know is that
  the word being typed almost certainly appears nearby already, which is true
  often enough to be worth a keystroke.

  Ranking is the whole user-visible behaviour, so the scores are stated in one
  place: a declaration beats a word that merely appears, and a word from the
  file being edited beats the same word from elsewhere — locality is the best
  signal available without types. Matching is a case-sensitive prefix test
  rather than the fuzzy match the picker uses, because completion finishes a
  word you have started and a candidate that does not begin with what you typed
  would replace letters already on screen. Two characters minimum: one matches
  most of a file and ranks it by nothing useful. The sort is total down to
  alphabetical, since a list that reshuffles between identical keystrokes is
  worse than one in a debatable order.

  The popup is not a Picker mode. The picker is a centred modal that takes
  focus and whose query is a field being edited; completion is anchored to the
  caret, takes no focus, and its query is the buffer text behind the cursor.
  Sharing them would mean a modal that is sometimes not modal. It claims five
  keys — up, down, tab, enter, escape — and everything else falls through and
  types, which is what keeps it ignorable; there is a test listing fifteen
  actions that must reach the editor untouched. Several cursors close it,
  because a completion is one word at one place and applying it at cursors
  mid-word in different identifiers would replace text nobody looked at.

  `Source` is the seam a language server plugs into later: the popup only needs
  strings back, so LSP completion becomes another implementation rather than a
  rewrite.

  Measured rather than assumed, and the number is not flattering: 5.3 ms
  against 2 MB of open buffers, on the keystroke path. Recorded in
  BENCHMARKS.md with the caching fix in the TODO rather than quietly shipped.
- [x] **LSP groundwork: position mapping and the wire format.** The two pieces
  that carry the most risk and need no server to verify, done first for exactly
  that reason.

  Positions are the hard part, not the protocol. LSP counts characters in
  UTF-16 code units and raj counts bytes; they agree for ASCII and disagree for
  everything else, so one accented letter, one CJK character or one emoji
  shifts every position after it in a response. The failure mode is not a crash
  but a jump to the wrong column, which is why this is fuzzed rather than
  argued about: 1.2 million executions asserting that a round trip never drifts
  forward, always lands on a rune boundary, and always produces a position
  inside the document. There is also a test that agrees with Go's own UTF-16
  encoder rather than restating this package's arithmetic, since a bug repeated
  in the test is invisible.

  Naming the three coordinate systems is half the value: byte offsets (the
  buffer and every edit), display columns (the renderer and the caret, where a
  CJK character is two wide), and UTF-16 code units (LSP, where a CJK character
  is one and an emoji is two). This package converts the first to the third and
  never touches the second.

  The framing is HTTP-style headers around a JSON body, and `Content-Length` is
  in bytes. That is the one place where getting bytes-versus-characters wrong
  desynchronises the stream permanently — every later frame is then read from
  the middle of the previous one — so the body is read with `ReadFull` and there
  is a test for a payload that contains the header delimiter inside a string.
  Unknown headers are skipped rather than rejected, because failing against a
  server that is merely chattier than expected is not a failure worth having. A
  server error is data attached to one request rather than a connection
  failure: a server that cannot answer a hover can still answer the next
  completion.
- [x] **Completion no longer rescans what has not changed.** It cost 5.68 ms per
  keystroke against 2 MB of open buffers — a third of a frame, on the path this
  codebase otherwise keeps clear on principle. Now 0.93 µs, six thousand times
  cheaper.

  The scan was never the problem; it was already running at 300 MB/s. The waste
  was that typing changes one buffer and leaves every other one byte-identical,
  so nearly every scan recomputed a result that had not moved. Keying the word
  set on the buffer version turns the steady-state cost from "every open byte"
  into "the bytes that changed".

  Two details worth keeping. The text is passed as a closure rather than a
  string, so an unchanged buffer is never read at all — materialising every
  piece table to hand over text that is then discarded would have put back most
  of what the cache saves. And version zero is a legitimate version, an
  untouched buffer, so presence in the map marks an entry valid rather than a
  zero check; there is a test for it because that mistake would silently
  disable the cache for exactly the files nobody has typed in yet.

  The cached and uncached rankings share their ordering code and are asserted
  equal across several prefixes. They are two paths over the same rules, and the
  ranking tests are written against the uncached one — if they drifted, those
  tests would stop covering what actually runs.

  This is also the key `textDocument/didChange` needs, which is why it was worth
  doing before the LSP work rather than after: the cache and the language server
  want the identical thing.
- [x] **LSP lifecycle: spawn, handshake, cancellation, restart.** A language
  server crashes, hangs, and spends ten seconds indexing before it answers
  anything. All three are normal, and the rule the whole package is written to
  is that a dead server degrades to no language features and never to a broken
  editor — so every entry point either answers, fails, or times out, and none
  of them can block the caller indefinitely.

  Cancellation matters more than it looks. The answer to a hover is worthless
  once the cursor has moved, and a completion computed three keystrokes ago is
  worse than nothing because it will be shown as though it were current. A
  cancelled call stops waiting immediately and sends `$/cancelRequest`, which is
  advisory — servers are permitted to answer anyway, so the pending entry is
  dropped regardless and there is a test that a late answer to a cancelled
  request is not delivered to whoever asks next.

  Restarting is deliberately neither automatic nor immediate. A server that
  crashes during startup — missing toolchain, unreadable config, version
  mismatch — crashes every time, and an editor that respawns on exit turns that
  into a fork bomb that is almost impossible to diagnose from inside the editor.
  So failures are counted, spaced by a doubling delay, and give up, with the
  giving-up reported rather than silent.

  **The deadlock is the finding worth recording.** Holding the state lock across
  a write looked obviously correct and deadlocked under concurrent calls: the
  write blocks until the server reads, the server blocks writing its reply
  because the client's reader cannot take the state lock to dispatch it, and so
  the server stops reading. Two locks with no ordering between them is the fix,
  and it is only safe because nothing ever needs both. A fifty-goroutine test
  found it; nothing about reading the code did, and a real server over real
  pipes would have hit it under load rather than in a test.

  A smaller one: a successful handshake cleared the failure count but left the
  pending delay, so a crash hours later would have waited out a backoff earned
  before the server ever worked. Found by a test asserting what "reset" ought to
  mean rather than what the code did.

  Stderr is discarded rather than merged into stdout, because servers log freely
  there and one stray line on the protocol stream desynchronises the framing
  permanently.
- [x] **LSP document synchronisation.** Every request is answered against the
  server's copy of a document, so a desynchronised copy does not produce an
  error — it produces a confidently wrong answer at a position that no longer
  means anything. That failure is silent and looks like the server being bad,
  which is why the tracking is stricter than the protocol requires: a document
  is opened exactly once, a change for a document that was never opened is
  dropped rather than sent, and a version that has not moved is not a change.

  Whole documents rather than incremental ranges, even where the server accepts
  incremental. Incremental is what large files want and it is the obvious next
  step, but it needs the edit ranges in UTF-16 for every edit since the last
  notification, and one wrong range desynchronises the server's copy silently
  and permanently. Whole-document sync cannot desynchronise by construction, the
  cost is bounded by the file rather than the session, and the change is already
  debounced upstream.

  The bug worth recording is small and would have been invisible: `null`
  unmarshals into an int without error and leaves it zero, so a server that
  advertised no `textDocumentSync` at all was read as asking for *no change
  notifications* — the one value that freezes every document at the moment it
  was opened, making every feature answer from stale text with nothing to
  suggest anything was wrong. It is excluded explicitly rather than relied on to
  fail. The test that caught it asserts the direction of the guess (unknown
  means full, never none) rather than the value, which is why it caught a case
  the table did not enumerate.
- [x] **Hover (cmd+i) and go-to-definition (cmd+alt+d).** The first language
  features that are visible in the editor, and the first that meet a real
  server.

  The rule the integration follows is that no language feature may make the
  editor worse when it is unavailable. A server that is missing, slow, crashed
  or confused produces no answer and no interruption — never a stall, never an
  error to dismiss, never a modal waiting on a subprocess. An unknown file type,
  an uninstalled server and a server that has given up all arrive at the caller
  the same way, as "no server", because none of them is worth treating
  differently from the user's point of view.

  Servers start on the first request that needs one rather than at launch. Most
  sessions never ask for a hover, and paying gopls's startup on every launch to
  serve the ones that do is the wrong trade; it also means a broken server costs
  nothing until it is asked for. The first request after a start goes
  unanswered, which is the honest behaviour — the handshake takes as long as it
  takes, and blocking the keystroke that triggered it would be worse than
  answering the next one.

  Cancellation reuses the search pane's shape: a generation counter, and an
  answer whose generation has moved on is dropped. That matters more for hover
  than for search, because a hover for a position the cursor has left is not
  merely stale — it is displayed as though it described where the cursor is now.

  The decoding is deliberately permissive about shape and strict about meaning.
  Hover contents has had three legal forms across versions of the protocol and
  servers still send all of them; definition may be a location, an array, or a
  link that names its target under different keys. The link case has a detail
  worth keeping: it carries both the whole declaration and the identifier
  within it, and jumping to the identifier is what someone asking "where is this
  defined" means. Markdown is flattened rather than rendered, since raj draws
  into a cell grid and a hover showing literal backticks is worse than one
  showing the signature they wrapped.

  Answers are parked and collected on the event thread rather than applied where
  they arrive, because applying them directly would touch panes and the screen
  from a goroutine that must not. `ui.Event` is sealed, so the search pane's
  park-and-wake pattern was the fit rather than a new event type.
- [x] **"no language server for this file", on a Go file.** Reported from a
  screenshot, and the message was the bug: four different situations all
  produced it, and on a supported file type it asserted the one thing that was
  not true.

  The four need four different reactions and only one of them is "nothing to be
  done": the binary is not installed (install it), the server is still
  handshaking (wait a moment — gopls takes seconds on a large repository and the
  first press after startup was always going to be unanswered), it kept crashing
  and has been given up on (find out why), or this language genuinely has no
  server configured. They now say so separately, and a missing binary is named
  so the fix is obvious rather than a guess. The binary is looked up before
  spawning, so "not installed" is reported as itself rather than as a start
  failure that burns a restart attempt first.

  Found while fixing it: paths handed to LSP were not made absolute, so a pane
  opened with a workspace-relative path produced `file://internal/editor/
  actions.go` — a URI whose host is "internal" and which names nothing a server
  can open. Every request would have failed silently against a server that was
  otherwise working perfectly. Absolute paths are now the seam every LSP call
  goes through, with a test that a URI has three slashes rather than two.
- [x] **cmd+alt+d never arrived, because macOS hides the Dock with it.** Bound
  to go-to-definition and swallowed system-wide before any terminal saw it, so
  it fired exactly zero times and looked from inside raj like nothing at all —
  no event, no keystroke, nothing to log. Moved to cmd+shift+d.

  The interesting part is that no amount of testing raj could have caught it:
  the key never reaches the process. So the check is a table of the chords macOS
  reserves, asserted against the binding table — the only place the mistake is
  visible is the place the choice is made. Verified by putting the original
  chord back and watching the test name the Dock.

  The replacement was wrong too, for a different reason: cmd+shift+d arrives
  fine, but iTerm2 and Ghostty both split a pane with it, so nothing in raj
  would ever have complained while the user quietly lost a terminal feature.
  Now cmd+j, with a second table for the chords the terminal claims.

  The two tables encode different rules, which is why they are separate. A macOS
  chord is simply unusable — the key never reaches the process. A terminal chord
  is usable but costs the user something, so a binding there is allowed when the
  trade is deliberate: cmd+d takes Ghostty's split-right on purpose and says so
  in the table's notes column. The test requires the note, not the absence of
  the binding.
- [x] **LSP-backed completion.** The buffer words show instantly and the
  server's answer replaces them when it arrives. That ordering is the whole
  design: a language server takes tens of milliseconds on a good day and
  hundreds on a cold index, and a completion list that appears a noticeable beat
  after you stop typing feels broken even when it is better. Buffer words are
  instant and usually right; the server's answer is better and late, and both
  are available in the order that makes each one useful.

  The server's ordering is kept rather than re-ranked. Its sort keys encode
  scope, type compatibility and usage — things the client cannot see — and
  re-scoring the answer client-side would throw away the entire reason for
  asking a language server rather than scanning the buffer. Only the prefix
  filter is applied, and only to remove what the keystrokes since the request
  excluded. Filtering uses the server's own filter text where it differs from
  the label, which it does more often than it looks: gopls labels a method with
  its signature and filters on the bare name, so matching the label would need a
  parenthesis typed to keep the match alive.

  Snippets are refused and the label inserted instead. A snippet is a template
  with tab stops, and with no snippet engine raj would type `${1:format}` into
  the buffer — worse than inserting a plain identifier. raj also advertises
  `snippetSupport: false`, so a server sending one anyway is not being obliged.

  Two generations, not one. A hover is superseded by a cursor move and a
  completion by further typing, and sharing a counter made every completion
  answer look stale — silently, since the buffer words stay up and the server's
  answer simply never appears. Caught by a test that asserted the replacement
  rather than the absence of a crash.
- [x] **Diagnostics.** The last piece of the staged LSP plan, and the only
  feature here that arrives without being asked — which makes it the only one
  that has to survive turning up at an arbitrary moment: while a file is being
  edited, while it is closed, or for a file never opened this session.

  They are absolute rather than incremental. Each publish is the complete set
  for a document, so the newest replaces the previous one whole; merging would
  accumulate problems that have already been fixed. An empty publish is
  therefore meaningful and is stored as a clearing rather than ignored — it is
  how a server says the problems are gone, and dropping it would leave them on
  screen forever.

  Stored in file order rather than by severity, because a warning on line 3
  above an error on line 200 is what the file actually looks like. A line
  carrying both is an error line: showing the warning because it was published
  first would under-report it. A missing severity counts as an error, since the
  protocol leaves that to the client and under-reporting a real problem is the
  worse mistake.

  The bug worth recording is a disagreement between two places that had to
  agree. `severityRank` treated an unspecified severity as an error and `counts`
  matched on the literal value, so such a diagnostic drew a red mark in the
  gutter and was reported as no problems in the status line. Caught by testing
  the counting against the ranking rather than against itself.

  The marks go over the line numbers rather than beside them: widening the
  gutter for a column that is empty most of the time costs every file a column
  forever, and a number under a mark is still recoverable from the status line.
  The channel is drained rather than read once per wake, because several
  publishes can queue between wakes and only the last for each file matters.
- [x] **Click and drag in the editor.** Click to position, drag to select,
  double click for a word, triple click for a line, shift-click to extend,
  cmd-click for another cursor. The panes and the fields are deliberately not
  included: each needs its own hit-testing against what it drew, and doing them
  together would have meant four half-tested mappings instead of one tested one.

  `OffsetAt` is the exact inverse of `placeCaret`, and the two have to stay
  inverses — a click that lands anywhere other than where the caret would be
  drawn for that offset is a click that appears to miss. So the test asserts the
  round trip across every offset in a buffer rather than either half separately.
  Three coordinate systems meet here and the order is what makes it work: a
  screen cell becomes a display column, a display column becomes a byte offset
  within a line, and only then a document offset. Skipping the middle step is
  what makes clicks land wrong on lines containing tabs or CJK, and `OffsetOf`
  already resolved a column landing inside a tab to that tab's start.

  DECSET 1002 replaces 1000, which is the change that makes dragging possible at
  all: 1000 reports presses and releases, and every cell the pointer crosses in
  between arrives only under 1002. Still not 1003 — that reports motion with no
  button held, which is every cell the pointer crosses at all times, decoded and
  discarded.

  A click past the end of a line lands at the line's end rather than wrapping,
  because dragging past the right edge is how a whole line gets selected and
  wrapping would quietly take the next line's start too. A release ends the drag
  wherever it happens, including outside the editor, since a button released
  over the sidebar is still released. A drag that wanders out of the text area
  clamps rather than stopping: losing the selection because the pointer moved a
  cell too far is worse than extending it to the nearest edge.

  The double-click threshold is time *and* position. Two presses far apart are
  two clicks however fast they arrive, and treating them as a double click would
  select a word the pointer never touched.

  The test bug worth recording: the first click test failed by one row and one
  column, and the cause was that a pane learns its width from being rendered — a
  wrapped pane that has never been drawn wraps at the wrong column, so a click
  resolved against it lands somewhere the user would never have seen. The real
  app draws after every event, so the fixture was wrong about the state a
  pointer always meets, not the code.
