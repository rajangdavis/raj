# Phone profile — spec

Status: draft, 2026-09-18; attach wiring landed 2026-09-18. `--phone` now
implies `--attach` and selects this profile on the local-render client, which
shows the daemon-owned document. The profile is chrome on the editor UI, not a
second program or renderer; with it off, the desktop client is unchanged.

## Goal

Make tab navigation and proposal review usable from a phone or tablet over
plain terminal input, by changing the chrome rather than adding a second
program. The review engine already exists in the editor
(`internal/app/review.go`); this only adds a surface for it. In client mode the
decisions proxy to the daemon (`internal/app/client.go`), so the surface reads
and decides the daemon's proposals rather than a local copy.

## Tabs

- In the profile, tabs render as taller chips (two rows, filename centred,
  touch-sized) instead of the one-row strip.
- The strip scrolls horizontally when the tabs overflow: a horizontal wheel or
  a swipe/motion run scrolls it; the existing tab-switch chords still work.
- One layout function serves both the renderer and the pointer, so a tap cannot
  land on a tab other than the one drawn under it.

## Status

- The bottom status bar is hidden and its rows reclaimed.
- Messages become a transient overlay that fades on the idle tick, so nothing
  is lost; the non-phone profile keeps the bar exactly as it is today.

## Review bar

- A row of labelled buttons bound to the existing actions: previous set, next
  set, accept, reject, clear, list.
- A button is a pointer region with hit-testing; the actions keep their key
  bindings, and the bar names its target (the set at the caret, or the visible
  bulk) the same way the keys do.
- No chord and no gesture is required to use it.

## Chords on a phone

The phone terminal cannot send `super` at all (the modifier lives in the
desktop terminal's config, which Termius does not run). In phone mode the
keymap gains a **ctrl alias** for every action bound to `super+<key>`: where
`ctrl+<key>` is free, both chords reach the action; where it is already
bound, the alias is not added and the collision is reported, so the review bar
or a different chord covers it. The alias is built from the binding table, not
by rewriting input, so the same table still generates the desktop configs, and
a test asserts every `super` binding either gets an alias or is reported.

## Non-goals

Chords, gestures, a second program or renderer, editing document text from
the bar, and the remaining detach/reattach and concurrent-view stages of
`docs/ATTACH-DESIGN.md`. (The attach client itself landed; `--phone` selects
this profile within it.)

## Verification

- `raj --phone` (which attaches) on a phone terminal: tabs are touchable and
  scroll; no persistent status bar; the review buttons decide the proposal under
  the caret against the daemon, and the key aliases (ctrl for super) reach the
  same actions.
- `raj` without `--phone`: byte-identical behaviour to before the change.
