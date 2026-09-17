# raj keybindings

Generated from `internal/keys/table.go`. Do not edit by hand:
run `raj --keys > docs/KEYBINDINGS.md`, or just change the table and
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
| command_palette | `shift+super+p` | cmd+shift+p | ctrl+shift+p | — |
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
| reload | `shift+super+r` | cmd+shift+r | ctrl+shift+r | reload moved off cmd+r, which now toggles review mode |

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
| cursor_undo | `super+u` | cmd+u | ctrl+u | — |

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
| references | `shift+super+j` | cmd+shift+j | ctrl+shift+j | shifted sibling of go-to-definition's super+j |
| goto_declaration | `ctrl+super+b` | cmd+ctrl+b | ctrl+alt+b | IntelliJ's Ctrl+B is go-to-declaration; where the name is declared, not the body |
| goto_type_definition | `ctrl+super+t` | cmd+ctrl+t | ctrl+alt+t | t for type: the definition of the thing's type |
| goto_implementation | `ctrl+super+i` | cmd+ctrl+i | ctrl+alt+i | i for implementation; the picker opens when the server returns several |
| workspace_symbols | `ctrl+super+o` | cmd+ctrl+o | ctrl+alt+o | project-wide symbols from the language server; the sibling of shift+super+o |
| toggle_inlay_hints | `shift+super+i` | cmd+shift+i | ctrl+shift+i | toggle inline hints for the active pane |
| apply_inlay_edit | `super+.` | cmd+period | ctrl+period | apply the inlay hint's edits on the caret's line |
| signature_help | `shift+ctrl+space` | ctrl+shift+space | ctrl+shift+space | the conventional parameter-info chord; shifted sibling of ctrl+space, which summons completion |
| code_action | `ctrl+super+a` | cmd+ctrl+a | ctrl+alt+a | a for action; list the server's fixes and refactors for the caret |
| open_menu | `shift+f10` | shift+f10 | shift+f10 | the conventional context-menu key; right-click parity for the focused explorer entry, else the active tab |
| run_code_lens | `ctrl+super+l` | cmd+ctrl+l | ctrl+alt+l | run the code lens on the caret's line |
| toggle_fold | `ctrl+super+c` | cmd+ctrl+c | ctrl+alt+c | fold or unfold the language server's range at the caret |
| rename | `ctrl+super+r` | cmd+ctrl+r | ctrl+alt+r | rename the symbol under the cursor; server edits land in every file they touch |
| format | `shift+alt+f` | shift+alt+f | shift+alt+f | format the whole document through the language server |
| format_range | `ctrl+alt+f` | ctrl+alt+f | ctrl+alt+f | format the selected range; needs a selection |
| follow_link | `ctrl+super+y` | cmd+ctrl+y | ctrl+alt+y | follow the document link under the caret |

## proposals

| action | chord | macOS | Linux | notes |
| --- | --- | --- | --- | --- |
| toggle_review | `super+r` | cmd+r | ctrl+r | toggle review mode: the document is read-only while on; reload moved to cmd+shift+r |
| accept_proposed | `ctrl+super+m` | cmd+ctrl+m | ctrl+alt+m | accept the proposed change set at the caret |
| reject_proposed | `ctrl+super+/` | cmd+ctrl+slash | ctrl+alt+slash | reject the proposed change set at the caret |
| clear_rejected | `ctrl+super+k` | cmd+ctrl+k | ctrl+alt+k | hard-purge the rejected change set at the caret |
| prev_proposed | `ctrl+super+,` | cmd+ctrl+comma | ctrl+alt+comma | previous pending change set |
| next_proposed | `ctrl+super+.` | cmd+ctrl+period | ctrl+alt+period | next pending change set |
| pending_removals | `ctrl+alt+d` | ctrl+alt+d | ctrl+alt+d | re-raise the oldest pending deletion or dir-removal; macOS claims cmd+ctrl+d for Look Up in Dictionary |

## Handed back to the terminal

These are configured so the terminal keeps its own behaviour
rather than sending the chord to raj.

| chord | macOS | Linux | notes |
| --- | --- | --- | --- |
| `shift+pgup` | shift+page_up | shift+page_up | iTerm2 default: scroll back |
| `shift+pgdown` | shift+page_down | shift+page_down | iTerm2 default: scroll forward |
