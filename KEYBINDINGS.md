# raj keybindings

Generated from `internal/keys/table.go`. Do not edit by hand:
run `raj --keys > KEYBINDINGS.md`, or just change the table and
let the test tell you.

Chords are written as raj resolves them. The macOS and Linux columns
are what the terminal has to be configured to send — see
`raj --config ghostty` and `raj --config iterm2`.

Actions marked **(unimplemented)** are bound but do nothing yet. The
chord is still claimed from the terminal, which is why they are listed
rather than hidden.

## panes

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| toggle_sidebar | `super+b` | cmd+b | ctrl+b | — |
| focus_explorer | `shift+super+e` | cmd+shift+e | ctrl+shift+e | linux default: new split down |
| focus_search | `shift+super+f` | cmd+shift+f | ctrl+shift+f | — |
| focus_problems | `shift+super+m` | cmd+shift+m | ctrl+shift+m | m for markers, as VS Code names the same pane |
| file_picker | `super+p` | cmd+p | ctrl+p | — |
| command_palette **(unimplemented)** | `shift+super+p` | cmd+shift+p | ctrl+shift+p | no palette yet; the file picker is cmd+p |
| find_in_file | `super+f` | cmd+f | ctrl+f | macOS default: find |
| suspend | `ctrl+z` | ctrl+z | ctrl+alt+z | linux ctrl+z is undo, so suspend moves |
| quit | `ctrl+c` | ctrl+c | ctrl+c | — |
| toggle_debug | `shift+ctrl+d` | ctrl+shift+d | ctrl+shift+d | — |

## tabs

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| close_tab | `super+w` | cmd+w | ctrl+shift+w | ghostty default: close surface |
| reopen_tab | `shift+super+t` | cmd+shift+t | ctrl+shift+t | — |
| next_tab | `alt+super+right` | cmd+alt+right | ctrl+alt+right | ghostty default: focus split right |
| prev_tab | `alt+super+left` | cmd+alt+left | ctrl+alt+left | ghostty default: focus split left |

## file

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| new_file | `super+n` | cmd+n | ctrl+n | ghostty default: new window |
| save | `super+s` | cmd+s | ctrl+s | — |

## edit

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| undo | `super+z` | cmd+z | ctrl+z | — |
| redo | `shift+super+z` | cmd+shift+z | ctrl+shift+z | — |
| cut | `super+x` | cmd+x | ctrl+x | — |
| copy | `super+c` | cmd+c | ctrl+shift+c | ghostty default: copy. raj writes clipboard via OSC 52 |
| select_all | `super+a` | cmd+a | ctrl+a | — |
| select_line | `super+l` | cmd+l | ctrl+l | linux default: clear screen |
| toggle_comment | `super+/` | cmd+slash | ctrl+slash | — |
| delete_line | `shift+super+k` | cmd+shift+k | ctrl+shift+k | — |
| delete_to_line_end | `super+k` | cmd+k | ctrl+k | ghostty and iTerm2 default: clear scrollback |
| line_below | `super+enter` | cmd+enter | ctrl+enter | ghostty default: toggle fullscreen |
| line_above | `shift+super+enter` | cmd+shift+enter | ctrl+shift+enter | ghostty default: split zoom |
| move_line_up | `alt+up` | alt+up | alt+up | — |
| move_line_down | `alt+down` | alt+down | alt+down | — |
| copy_line_up | `shift+alt+up` | shift+alt+up | shift+alt+up | — |
| copy_line_down | `shift+alt+down` | shift+alt+down | shift+alt+down | — |

## cursor

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| cursor_above | `alt+super+up` | cmd+alt+up | ctrl+alt+up | ghostty default: focus split up |
| cursor_below | `alt+super+down` | cmd+alt+down | ctrl+alt+down | ghostty default: focus split down |
| add_next_occurrence | `super+d` | cmd+d | ctrl+d | ghostty default: split right |
| split_into_lines | `shift+super+l` | cmd+shift+l | ctrl+shift+l | sublime: split selection into lines |
| all_occurrences | `ctrl+super+g` | cmd+ctrl+g | ctrl+alt+g | sublime: find all. alt+f3 on linux, but f3 encodes inconsistently |
| cursor_undo **(unimplemented)** | `super+u` | cmd+u | ctrl+u | cursor history is not recorded yet |

## nav

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| line_start | `super+left` | cmd+left | home | — |
| line_end | `super+right` | cmd+right | end | — |
| doc_start | `super+up` | cmd+up | ctrl+home | ghostty default: jump to prev prompt |
| doc_end | `super+down` | cmd+down | ctrl+end | ghostty default: jump to next prompt |
| word_left | `alt+left` | alt+left | ctrl+left | ghostty sends ESC b by default |
| word_right | `alt+right` | alt+right | ctrl+right | ghostty sends ESC f by default |
| sel_line_start | `shift+super+left` | cmd+shift+left | shift+home | — |
| sel_line_end | `shift+super+right` | cmd+shift+right | shift+end | — |
| sel_doc_start | `shift+super+up` | cmd+shift+up | ctrl+shift+home | — |
| sel_doc_end | `shift+super+down` | cmd+shift+down | ctrl+shift+end | — |
| sel_word_left | `shift+alt+left` | shift+alt+left | ctrl+shift+left | — |
| sel_word_right | `shift+alt+right` | shift+alt+right | ctrl+shift+right | — |
| goto_line | `ctrl+g` | ctrl+g | ctrl+g | — |
| find_next | `super+g` | cmd+g | ctrl+alt+n | macOS default: find next |
| find_prev | `shift+super+g` | cmd+shift+g | ctrl+alt+p | macOS default: find previous |
| goto_symbol | `shift+super+o` | cmd+shift+o | ctrl+shift+o | — |
| hover | `super+i` | cmd+i | ctrl+i | — |
| goto_definition | `super+j` | cmd+j | ctrl+alt+j | — |

## Handed back to the terminal

These are configured so the terminal keeps its own behaviour
rather than sending the chord to raj.

| chord | macOS | Linux | notes |
| --- | --- | --- | --- |
| `shift+pgup` | shift+page_up | shift+page_up | iTerm2 default: scroll back |
| `shift+pgdown` | shift+page_down | shift+page_down | iTerm2 default: scroll forward |
