# Cursor and Viewport — the invariants

**Status:** written down and asserted · **Backing code:** `internal/editor`,
`internal/view` · **Backing tests:** `internal/editor/spec_test.go`, which walks
random documents with random action sequences and checks every invariant below
after every action.

Every cursor bug in this repository so far has been a disagreement between two
representations of the same position. There are four, and each is derived from
the one before it:

| representation | what it is | who owns it |
| --- | --- | --- |
| **byte offset** | index into the document | `piecetable`, and every cursor |
| **line, byte-in-line** | which line, how far in | `view.Index` |
| **display column** | after tabs and wide runes | `view.Columns` |
| **visual row** | after wrapping | `view.Wrap`, `editor/wrap.go` |

An example test catches the disagreement it happens to look at. These are
properties, so they catch the ones nobody thought of — which is how the two bugs
listed at the bottom were found.

## I1 — Cursors address real positions

Every `Head` and `Anchor` is in `[0, Len]` and sits on a rune boundary. An
offset inside a multi-byte rune is not a position: the renderer cannot draw it,
`LineCol` cannot describe it, and an edit at it splits a character in half.

Motions must therefore step by runes, never by bytes. *This is what `WordLeft`
and `WordRight` got wrong: they read one byte at a time and treated each as a
character, so every continuation byte looked like a non-word character and the
scan stopped inside the rune.*

## I2 — Cursors are sorted and disjoint

The set is ordered by `Head`, and no two selections overlap. Two cursors
covering the same span apply every subsequent edit twice in one place.
`Cursors.Normalize` is what maintains this, and it runs after every mutation —
`Apply`, `Add`, `Replace`, and `Shift`.

## I3 — Offset and line/column agree, both ways

For any cursor at offset `o`:

- `LineStart(LineOf(o)) <= o <= LineEnd(LineOf(o)) + 1`
- `OffsetAt(LineCol(o)) == o`

The `+1` is the end-of-line position: a cursor may sit after the last character
of a line, where the newline is.

## I4 — The line index agrees with the text

`Lines()` equals the number of newlines plus one, and `LineOf(LineStart(n)) == n`
for every line. The index is derived state, and derived state that drifts turns
every position question into a wrong answer at once.

The index is updated from ops, and **an op is mirrored by scanning its own
inserted pieces, never by reading the document back**. Pieces are immutable, so
they describe what that op inserted no matter what landed afterwards; the
document does not, and a caller that mirrors several ops at once (a multi-hunk
diff, a redo of a multi-cursor edit) does so after all of them have landed.

`File.sync` catches up on **every** op since the index was last current, not
only the most recent one, because a single session call can append more than one.

## I5 — The viewport is inside the document

`0 <= Top < Lines()`. `Resize` and every scroll clamp it.

## I6 — Scrolling moves the view; everything else follows the cursor

- A scroll action (`PageUp`, `PageDown`, and the wheel when it lands) changes
  `Viewport.Top` and **nothing else** — not the cursor count, not any `Head`,
  not any `Anchor`.
- Every other consumed action ends with the primary cursor visible, because
  `Pane.Handle` ends in `FollowCursor`.

Together these give the property that matters: the view can be scrolled away
from a multi-cursor set without collapsing it, and the next keystroke that moves
or edits brings the view back to where the edit will land.

## I7 — Vertical motion is reversible

`LineDown` then `LineUp` returns to the display column you started in, across
short lines and empty ones. This is `Cursor.Goal`, and it is the single most
noticeable cursor bug an editor can have.

## What the properties have caught

- **Word motion landed mid-rune.** Byte-at-a-time scanning in `scanWord`. Fixed.
- **The line index desynced after a multi-cursor redo.** `applyToIndex` read the
  inserted span back out of the document, which is only correct for the last op
  in a batch. Fixed by scanning the op's pieces.
- **Redo can split a rune.** Still open — see TODO.md. Reproduce by raising
  `specSeeds` in `spec_test.go` to 400: wrap=false, seed 245, step 51.

## What is not specified yet

Mouse positioning (no mouse yet), and the rules for a viewport that has been
scrolled away when an *agent* edit lands off screen — an agent hunk should
probably not yank the view the way a keystroke does, but nothing decides that
today.
