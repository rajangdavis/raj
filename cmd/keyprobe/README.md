# keyprobe

Verifies empirically what Ghostty actually delivers for each chord raj wants to
own, and whether it hands the chord back on suspend and focus loss.

Zero dependencies (raw mode via `stty`), so `go run ./cmd/keyprobe` just works.

## Install the bindings first

```sh
go run ./cmd/keyprobe -config macos > ~/.config/ghostty/raj.conf
# then add to ~/.config/ghostty/config:
#   config-file = ~/.config/ghostty/raj.conf
```

Both platform configs normalise to the *same* CSI sequences, so raj's keymap is
platform-independent — macOS `cmd+w` and Linux `ctrl+shift+w` both arrive as
`ESC [ 119;9u` and raj only ever knows about `super+w`.

Strictly, only the chords Ghostty already binds *need* a `kkp_on:` line —
under `report_all` everything else is auto-encoded. The rest are there so the
encoding is pinned rather than inferred, and so the two platforms agree.

## The four tests

**1. Decode table.** `go run ./cmd/keyprobe`, press everything. Each line shows
raw bytes, the decoded chord, the event type, and the matching raj action.
`unmapped` means the chord arrived but isn't in the table; nothing at all means
Ghostty ate it and the `kkp_on:` line didn't take.

**2. Does the gate need `report_all`?** Run `-flags 1` (disambiguate only, no
report_all) and press `cmd+w`. It **should close the Ghostty tab**. If it
instead sends `ESC [ 119;9u`, the gate is firing on any KKP level rather than
`report_all` — which means Bubbletea's default flags are enough and raj doesn't
need to push flag 8 itself. Either answer is fine, I just need to know which.

**3. Suspend.** Press `ctrl+z`. The probe pops KKP, restores the tty, and stops
itself. While it's backgrounded, `cmd+w` should close the Ghostty tab normally.
`fg` brings it back and re-pushes. If `cmd+w` is still swallowed while
suspended, Ghostty is latching state somewhere and I need to know.

**4. Focus.** Split the window (`cmd+d`), click between splits. The probe prints
`FOCUS OUT` / `FOCUS IN` via mode 1004. With the probe focused, `cmd+w` should
reach it; with the other split focused, `cmd+w` should close that split. If it
doesn't, raj will have to pop KKP on focus-out itself.

## Checklist mode

`go run ./cmd/keyprobe -checklist` walks every binding in order, prompts for
each, records what arrived, and on `ctrl+c` (or `ctrl+q`) prints a summary plus a paste-ready
`map[string]Action` built from measurement rather than from my guesses. `ctrl+n` skips a chord, `ctrl+r` re-queries terminal state.

## Also worth reading in the output

- **`RELEASE`** lines. KKP flag 2 reports key release as its own event; raj must
  filter them or every action fires twice.
- **`reply` lines** at startup: the KKP flags response (`ESC [ ? N u`), the OSC
  10/11 fg/bg answers, and OSC 4 palette. If the OSC queries come back with real
  `rgb:` values, raj can pick its chroma theme from the actual background
  luminance instead of parsing your Ghostty config.
- **Bare modifier presses** (`lsuper`, `lshift`) are filtered but exist —
  another thing raj must ignore.

## Known-unsure

`cmd+left_bracket` / `cmd+right_bracket` — I'm not certain of Ghostty's spelling
for the bracket keys. Run `ghostty +list-keybinds | grep bracket` and correct
the two lines in `bindings.go` if needed; `TestBindingsRoundTrip` will keep the
config and the keymap in sync either way.
