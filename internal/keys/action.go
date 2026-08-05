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
	ToggleAgent    Action = "toggle_agent"
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
	Save Action = "save"

	// edit
	Undo          Action = "undo"
	Redo          Action = "redo"
	Cut           Action = "cut"
	Copy          Action = "copy"
	SelectAll     Action = "select_all"
	SelectLine    Action = "select_line"
	ToggleComment Action = "toggle_comment"
	DeleteLine    Action = "delete_line"
	LineBelow     Action = "line_below"
	LineAbove     Action = "line_above"
	MoveLineUp    Action = "move_line_up"
	MoveLineDown  Action = "move_line_down"
	CopyLineUp    Action = "copy_line_up"
	CopyLineDown  Action = "copy_line_down"
	Indent        Action = "indent"
	Outdent       Action = "outdent"
	Backspace     Action = "backspace"
	Delete        Action = "delete"

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
)
