// Package keys turns terminal bytes into raj actions.
//
// Three layers, each independently testable: Parse decodes bytes into an Event,
// Event.Chord names it, and a Keymap resolves the chord to an Action for the
// focused pane. Nothing here does I/O.
package keys

// Action is what a chord means. String-typed so logs and the session store are
// readable, and so the zero value is naturally "nothing bound".
type Action string

const (
	None Action = ""

	// panes
	ToggleSidebar  Action = "toggle_sidebar"
	ToggleWrap     Action = "toggle_wrap"
	FocusExplorer  Action = "focus_explorer"
	FocusSearch    Action = "focus_search"
	FocusProblems  Action = "focus_problems"
	Settings       Action = "settings"
	FilePicker     Action = "file_picker"
	CommandPalette Action = "command_palette"
	FindInFile     Action = "find_in_file"
	Suspend        Action = "suspend"
	Quit           Action = "quit"
	ToggleDebug    Action = "toggle_debug"
	CycleFocus     Action = "cycle_focus"
	CycleFocusBack Action = "cycle_focus_back"
	Cancel         Action = "cancel"
	Confirm        Action = "confirm"

	// tabs
	CloseTab  Action = "close_tab"
	ReopenTab Action = "reopen_tab"
	NextTab   Action = "next_tab"
	PrevTab   Action = "prev_tab"
	GotoTab1  Action = "goto_tab_1"
	GotoTab2  Action = "goto_tab_2"
	GotoTab3  Action = "goto_tab_3"
	GotoTab4  Action = "goto_tab_4"
	GotoTab5  Action = "goto_tab_5"
	GotoTab6  Action = "goto_tab_6"
	GotoTab7  Action = "goto_tab_7"
	GotoTab8  Action = "goto_tab_8"
	GotoTab9  Action = "goto_tab_9"

	// file
	NewFile Action = "new_file"
	Save    Action = "save"
	Reload  Action = "reload"

	// CopyRelPath copies the active buffer's path relative to the workspace
	// root to the clipboard. It has no chord by design: it is an occasional
	// command, and the palette is where it lives. See keys.Unbound.
	CopyRelPath Action = "copy_relative_path"

	// edit
	Undo            Action = "undo"
	Redo            Action = "redo"
	Cut             Action = "cut"
	Copy            Action = "copy"
	SelectAll       Action = "select_all"
	SelectLine      Action = "select_line"
	ToggleComment   Action = "toggle_comment"
	DeleteLine      Action = "delete_line"
	DeleteToLineEnd Action = "delete_to_line_end"
	LineBelow       Action = "line_below"
	LineAbove       Action = "line_above"
	MoveLineUp      Action = "move_line_up"
	MoveLineDown    Action = "move_line_down"
	CopyLineUp      Action = "copy_line_up"
	CopyLineDown    Action = "copy_line_down"
	Indent          Action = "indent"
	Outdent         Action = "outdent"
	Backspace       Action = "backspace"
	Delete          Action = "delete"

	// multi-cursor
	CursorAbove       Action = "cursor_above"
	CursorBelow       Action = "cursor_below"
	AddNextOccurrence Action = "add_next_occurrence"
	AllOccurrences    Action = "all_occurrences"
	SplitIntoLines    Action = "split_into_lines"
	CursorUndo        Action = "cursor_undo"

	// navigation
	LineStart    Action = "line_start"
	LineEnd      Action = "line_end"
	DocStart     Action = "doc_start"
	DocEnd       Action = "doc_end"
	WordLeft     Action = "word_left"
	WordRight    Action = "word_right"
	CharLeft     Action = "char_left"
	CharRight    Action = "char_right"
	LineUp       Action = "line_up"
	LineDown     Action = "line_down"
	SelLineStart Action = "sel_line_start"
	SelLineEnd   Action = "sel_line_end"
	SelDocStart  Action = "sel_doc_start"
	SelDocEnd    Action = "sel_doc_end"
	SelWordLeft  Action = "sel_word_left"
	SelWordRight Action = "sel_word_right"
	SelCharLeft  Action = "sel_char_left"
	SelCharRight Action = "sel_char_right"
	SelLineUp    Action = "sel_line_up"
	SelLineDown  Action = "sel_line_down"
	PageUp       Action = "page_up"
	PageDown     Action = "page_down"
	SelPageUp    Action = "sel_page_up"
	SelPageDown  Action = "sel_page_down"
	GotoLine     Action = "goto_line"
	FindNext     Action = "find_next"
	FindPrev     Action = "find_prev"
	GotoSymbol   Action = "goto_symbol"
	Hover        Action = "hover"
	GotoDef      Action = "goto_definition"
	// GotoDeclaration, GotoTypeDef and GotoImpl are go-to-definition's
	// siblings: where the name is declared, the definition of the thing's
	// type, and the concrete implementations of an interface method. They
	// share its request pipeline and differ only in the LSP method.
	GotoDecl    Action = "goto_declaration"
	GotoTypeDef Action = "goto_type_definition"
	GotoImpl    Action = "goto_implementation"
	// References lists the uses of the symbol under the cursor, in the picker,
	// so "who calls this" is answered without leaving the editor.
	References Action = "references"
	// WorkspaceSymbols lists the project-wide symbols the language server has
	// indexed, in the picker; the sibling of GotoSymbol's file-scope scan.
	WorkspaceSymbols Action = "workspace_symbols"
	// Rename renames the symbol under the cursor through every file the
	// language server names, collecting the new name through the shared prompt.
	Rename Action = "rename"
	// Complete summons the completion popup deliberately. Without it the popup
	// only ever appears on its own after MinPrefix characters, so there is no
	// way to ask for it after a cursor move or with a one-character prefix.
	Complete Action = "complete"

	// SignatureHelp shows the parameter list of the call the caret is inside,
	// marking the active parameter; the sibling of Complete.
	SignatureHelp Action = "signature_help"

	// CodeAction lists the language server's fixes and refactors for the
	// caret, in the picker, and applies the one chosen.
	CodeAction Action = "code_action"

	// OpenMenu opens the same context menu a right-click opens, for whatever
	// has focus: the focused explorer entry when the sidebar has it, otherwise
	// the active tab. It is the keyboard half of the pointer gesture, so a menu
	// reachable by mouse is reachable without leaving the home row.
	OpenMenu Action = "open_menu"

	// RunCodeLens runs the code lens drawn at the start of the caret's line.
	// Lenses are the server's line-attached actions — "3 references", "Run
	// test" — and this is the keyboard half of the human path; the inline text
	// is the other half.
	RunCodeLens Action = "run_code_lens"

	// Format formats the whole document through the language server, and
	Format      Action = "format"
	FormatRange Action = "format_range"

	// FollowLink follows the document link at the caret: the server names the
	// spans of the document that point at a file or a URL, and this opens a
	// file: target through the same path every other jump uses. A non-file
	// target is refused out loud, because a terminal editor has no browser to
	// hand it to.
	FollowLink Action = "follow_link"

	// ToggleInlayHints flips language-server inlay hints for the active pane.
	// Per-pane rather than app-wide: the application default still applies to
	// files opened later, so silencing hints on one file does not have to
	// silence every file opened next.
	ToggleInlayHints Action = "toggle_inlay_hints"

	// ApplyInlayEdit applies the edits the language server attached to the
	// inlay hint nearest the caret on its line, through the shared
	// one-undo-step server-edit path. A hint with no edits says so.
	ApplyInlayEdit Action = "apply_inlay_edit"

	// ToggleFold closes the folding range at the caret, or opens it when it is
	// already closed. The range is the language server's; the pane holds
	// the closed state, so the buffer is untouched.
	ToggleFold Action = "toggle_fold"

	// ToggleExpandAll expands every folder under the explorer selection, or
	// every file group in the search results, and collapses them again on a
	// second press; scope-bound to cmd+return so it does not disturb the
	// editor's line-below.
	ToggleExpandAll Action = "toggle_expand_all"

	// ToggleDrawer opens or closes the phone profile's bottom action drawer.
	// It is unbound in the ordinary keymap; the phone profile binds esc to it,
	// so the editor's own esc (Cancel) is untouched off the phone.
	ToggleDrawer Action = "toggle_drawer"

	// proposals — an agent change set lands as a proposal: in the document,
	// tinted, save-blocked, and decided one gesture at a time. Accept marks it
	// agreed, reject marks it out of the agreed composition without touching
	// the text, and clear hard-purges a rejected set; review lists what is
	// still pending.
	// ToggleReview switches the whole application between Edit and Review
	// mode. Review makes the document read-only so a review pass cannot edit
	// the text it is reviewing; the review decisions stay live in both modes.
	ToggleReview   Action = "toggle_review"
	AcceptProposed Action = "accept_proposed"
	RejectProposed Action = "reject_proposed"
	ClearRejected  Action = "clear_rejected"
	ReviewProposed Action = "review_proposed"
	NextProposed   Action = "next_proposed"
	PrevProposed   Action = "prev_proposed"
	// PendingRemovals re-raises the oldest pending deletion or dir-removal
	// after the gate prompt was missed or dismissed. It is the persistent
	// surface for a proposal that is not a change set.
	PendingRemovals Action = "pending_removals"
)
