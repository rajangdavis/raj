// Package app is the event loop: it owns focus, routes keys to whichever pane
// has it, and decides when to repaint. Everything it drives is host-agnostic,
// so the whole application runs headlessly under ui.FakeHost.
package app

import (
	"os"
	"path/filepath"
	"strings"

	"raj/internal/complete"
	"raj/internal/editor"
	"raj/internal/explorer"
	"raj/internal/keys"
	"raj/internal/picker"
	"raj/internal/prompt"
	"raj/internal/search"
	"raj/internal/symbols"
	"raj/internal/tabs"
	"raj/internal/ui"
	"raj/internal/widget"
)

// App is one raj instance.
type App struct {
	host   ui.Host
	keymap *keys.Keymap
	screen *ui.Screen

	Tabs     *tabs.Tabs
	Explorer *explorer.Pane
	Search   *search.Pane
	Picker   *picker.Picker
	Prompt   *prompt.Prompt
	Complete complete.Popup

	root    string
	sidebar Sidebar
	focus   Focus
	theme   editor.Theme
	wth     widget.Theme

	Debug     debugLog
	clipboard editor.Clip
	// WrapDefault is applied to every pane as it opens, so the flag and the
	// toggle do not disagree about newly opened files. On by default: a line
	// running off the right edge is worse than one that continues below, and
	// horizontal scrolling is the fallback rather than the norm.
	WrapDefault bool

	// AutoPairs is applied to every pane as it opens, for the same reason as
	// WrapDefault: the setting lives on the application and panes inherit it,
	// rather than each pane deciding for itself and drifting.
	AutoPairs  bool
	dark       bool
	lastLayout Layout
	// promptReturn is where focus goes when a dialog closes. Captured when the
	// first prompt in a chain opens, so an overwrite check answered three
	// dialogs deep still lands back where the user was.
	promptReturn Focus
	status       string
	focused      bool
	quit         bool
	// quitAsked is true while the quit confirmation is on screen, so a second
	// Quit forces the exit instead of reopening the same question.
	quitAsked bool
}

// New builds an application rooted at a directory.
func New(host ui.Host, root string, tabWidth int) *App {
	cols, rows := host.Size()
	a := &App{
		host:     host,
		keymap:   keys.NewKeymap(),
		screen:   ui.NewScreen(cols, rows),
		Tabs:     tabs.New(tabWidth),
		Explorer: explorer.NewPane(root),
		Search:   search.NewPane(root),
		Picker:   picker.New(root),
		Prompt:   prompt.New(),
		root:     root,
		sidebar:  SidebarExplorer,
		focus:    FocusSidebar,
		theme:    editor.DefaultTheme(),
		// On unless a caller turns it off, so an App built in a test or by a
		// future entry point wraps too rather than depending on who remembered
		// to set the field.
		WrapDefault: true,
		AutoPairs:   true,
		dark:        host.Theme().Dark(),
		wth:         widget.DefaultTheme(),
		focused:     true,
	}
	// A finished search used to wait for the 150 ms tick, because the result is
	// installed on the event thread and nothing woke that thread. Posting a
	// Wake closes the gap for typing pauses, and it is the seam the agent pane
	// needs for exactly the same reason.
	a.Search.Notify = func() { host.Post(ui.Wake{}) }
	// Search the buffers, not only the disk. Only dirty ones: a saved buffer
	// and its file are the same bytes, so snapshotting it would copy a
	// document to search it exactly as reading it would have. An unnamed
	// buffer has no path to key on and is skipped — it is the one tab a search
	// cannot reach, for the same reason session restore cannot bring it back.
	a.Search.Buffers = func() search.Docs {
		var open search.Docs
		for _, p := range a.Tabs.All() {
			if p.File.Path == "" || !p.File.Dirty() {
				continue
			}
			if open == nil {
				open = search.Docs{}
			}
			open[p.File.Path] = p.File.Text()
		}
		return open
	}
	return a
}

// syncTheme adopts the terminal's measured background once the OSC query has
// answered. Highlighting a dark theme with light colours is unreadable, and the
// answer arrives asynchronously, so this runs whenever it might have changed.
func (a *App) syncTheme() {
	dark := a.host.Theme().Dark()
	if dark == a.dark {
		return
	}
	a.dark = dark
	for _, p := range a.Tabs.All() {
		p.File.SetDark(dark)
	}
}

// OpenFile opens a path in a tab and focuses the editor.
func (a *App) OpenFile(path string) {
	if path == "" {
		return
	}
	p, err := a.Tabs.Open(path)
	if err != nil {
		a.status = "cannot open: " + err.Error()
		return
	}
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	a.focus = FocusEditor
	a.status = ""
}

// openFromPicker opens a chosen file and honours any position the query was
// pasted with, so pasting a compiler line lands where the compiler pointed
// rather than at the top of the file.
//
// It is separate from OpenFile because a position only makes sense for a choice
// the picker made: nothing else in the app has a pasted `:line:col` to apply.
func (a *App) openFromPicker(path string) {
	if path == "" {
		return
	}
	pos, ok := a.Picker.PositionFor(path)
	a.OpenFile(path)
	if !ok || a.Tabs.Active() == nil {
		return
	}
	if pos.Line > 0 {
		a.jumpTo(pos.Line)
	}
	if pos.Col > 0 {
		a.jumpToColumn(pos.Line, pos.Col)
	}
}

// wheelRows is how far one notch scrolls. Three is the near-universal default
// and the number every terminal that translates the wheel into arrow keys
// sends, so matching it means the feel does not change with the terminal.
const wheelRows = 3

// mouse handles a pointer event. Only the wheel does anything today.
//
// It scrolls whatever is under the pointer rather than whatever has focus,
// because that is what a pointer is for: reaching something without first
// going there. An overlay takes the wheel wherever the pointer is, since it is
// drawn over everything and scrolling the pane behind it would be scrolling
// something the user cannot see.
func (a *App) mouse(ev ui.Mouse) {
	if !ev.IsWheel {
		return
	}
	delta := wheelRows
	if ev.Button == keys.WheelUp {
		delta = -wheelRows
	}
	if ev.Button != keys.WheelUp && ev.Button != keys.WheelDown {
		return // horizontal wheels: nothing scrolls sideways yet
	}

	if a.Picker.Open {
		a.Picker.Scroll(delta)
		return
	}
	cols, rows := a.screen.Size()
	l := computeLayout(cols, rows, a.sidebar, a.focus)
	if l.ShowSidebar && ev.Col >= l.SidebarX && ev.Col < l.SidebarX+l.SidebarW {
		switch a.sidebar {
		case SidebarExplorer:
			a.Explorer.Scroll(delta)
		case SidebarSearch:
			a.Search.Scroll(delta)
		}
		return
	}
	if l.ShowEditor {
		if p := a.Tabs.Active(); p != nil {
			p.ScrollRows(delta)
		}
	}
}

// offerCompletion refreshes the popup for whatever word the cursor is now in.
//
// It is called after the edit rather than before, so the prefix is what is
// actually on screen. Only typing and backspace offer anything; every other
// action closes the popup, because a cursor that jumped somewhere is no longer
// finishing the word it was on.
//
// Multiple cursors close it too. A completion is one word at one place, and
// applying it at four cursors that are mid-word in four different identifiers
// would replace text nobody looked at.
func (a *App) offerCompletion(p *editor.Pane, typing bool) {
	if !typing || a.Tabs.Active() != p || len(p.Cursors.All()) > 1 {
		a.Complete.Hide()
		return
	}
	head := p.Cursors.Primary().Head
	if p.Cursors.Primary().HasSelection() {
		a.Complete.Hide()
		return
	}
	line := p.File.LineOf(head)
	col := head - p.File.LineStart(line)
	prefix := complete.PrefixAt(p.File.Line(line), col)
	if len(prefix) < complete.MinPrefix {
		a.Complete.Hide()
		return
	}
	a.Complete.Show(prefix, a.completionSource(p).Candidates(prefix), line, col-len(prefix))
}

// completionSource is every open buffer plus the declarations raj can already
// find in them. A language server becomes another Source here rather than a
// change to the popup.
func (a *App) completionSource(cur *editor.Pane) complete.Source {
	b := complete.Buffers{
		Current:  cur.File.Path,
		Contents: map[string]string{},
		Symbols:  map[string][]string{},
	}
	for _, p := range a.Tabs.All() {
		path := p.File.Path
		if path == "" {
			path = p.File.Name()
		}
		text := p.File.Text()
		b.Contents[path] = text
		if symbols.Supported(path) {
			var names []string
			for _, s := range symbols.Find(path, text) {
				names = append(names, s.Name)
			}
			b.Symbols[path] = names
		}
	}
	return b
}

// acceptCompletion replaces the typed prefix with the chosen word.
//
// Replacing rather than appending the remainder, because the two differ when
// the candidate and the prefix disagree in a way a prefix match still allows —
// and one edit is one undo step, which appending plus fixing up would not be.
func (a *App) acceptCompletion(p *editor.Pane, prefix string, c complete.Candidate) {
	if c.Word == "" || !strings.HasPrefix(c.Word, prefix) {
		return
	}
	p.InsertText(c.Word[len(prefix):])
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// Run processes events until the application quits or the host closes.
func (a *App) Run() error {
	a.Draw()
	for e := range a.host.Events() {
		a.Handle(e)
		if a.quit {
			return nil
		}
		a.Draw()
	}
	return nil
}

// Handle applies one event. Exported so tests can drive the app a step at a
// time and assert between steps rather than only at the end.
func (a *App) Handle(e ui.Event) {
	switch ev := e.(type) {
	case ui.Key:
		a.handleKey(ev)
	case ui.Paste:
		a.paste(ev.Text)
	case ui.Mouse:
		a.mouse(ev)
	case ui.Resize:
		// Invalidate here rather than only in the host: after a resize the
		// terminal's contents outside the old geometry are undefined, and that
		// is true whichever host delivered the event.
		a.screen.Resize(ev.Cols, ev.Rows)
		a.host.Invalidate()
	case ui.Focus:
		a.focused = ev.In
		a.syncTheme()
	case ui.Suspended:
		a.screen.Clear()
		a.host.Invalidate()
	case ui.Wake:
		// Nothing to do here. Background work parks its result where the event
		// thread already looks, and Run draws after every event — so the value
		// of a Wake is entirely in having ended the wait for the next tick.
	case ui.Tick:
		// Idle work only: retokenising costs tens of milliseconds and must
		// never sit on the keystroke path.
		a.refreshSyntax()
		a.Debug.sample()
	case ui.Quit:
		a.quit = true
	}
}

// handleKey resolves a chord in the focused scope, then gives the global
// actions first refusal before the focused pane sees it.
func (a *App) handleKey(k ui.Key) {
	scope := a.scope()
	action, text, ok := a.keymap.Resolve(scope, k.Event)
	if !ok {
		return
	}
	a.Debug.record(k, scope, action, text)
	// A modal dialog gets first refusal, ahead of the globals: cmd+w while
	// "save changes?" is on screen must not close the tab the question is
	// about.
	if a.Prompt.Open && !passesModal(action) {
		a.Prompt.Handle(action, text)
		a.settlePrompt()
		return
	}
	if a.handleGlobal(action) {
		return
	}
	defer a.refreshSyntax()

	switch a.focus {
	case FocusPicker:
		a.openFromPicker(a.Picker.Handle(action, text))
		if !a.Picker.Open && a.focus == FocusPicker {
			a.focus = FocusEditor
		}
	case FocusSidebar:
		a.handleSidebar(action, text)
	default:
		a.handleEditor(action, text)
	}
}

// passesModal names the few actions a dialog must not swallow.
//
// Quit is here so a dialog can never wedge the session. Cut and copy are here
// because the global handler is what knows how to reach a text field at all —
// focusedInput asks the dialog first, so letting them through is what makes
// cmd+c inside a save-as box copy the path rather than do nothing.
func passesModal(a keys.Action) bool {
	return a == keys.Quit || a == keys.Cut || a == keys.Copy
}

// scope is the keymap scope for the focused pane. It is what makes tab indent
// in the editor and cycle focus everywhere else.
func (a *App) scope() keys.Scope {
	switch {
	case a.focus == FocusPrompt:
		return keys.Prompt
	case a.focus == FocusPicker:
		return keys.Picker
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		return keys.Search
	case a.focus == FocusSidebar:
		return keys.Explorer
	default:
		return keys.Editor
	}
}

// handleGlobal deals with actions that work regardless of focus. Returns true
// when the action was consumed.
func (a *App) handleGlobal(action keys.Action) bool {
	switch action {
	case keys.ToggleDebug:
		a.Debug.Open = !a.Debug.Open
		a.Debug.sample()
	case keys.Quit:
		a.tryQuit()
	case keys.Suspend:
		a.host.Suspend()
	case keys.Save:
		a.saveActive(nil)
	case keys.Cut:
		a.clip(true)
	case keys.Copy:
		a.clip(false)
	case keys.FocusExplorer:
		a.openSidebar(SidebarExplorer)
	case keys.FocusSearch:
		a.openSidebar(SidebarSearch)
	case keys.ToggleWrap:
		if p := a.Tabs.Active(); p != nil {
			p.Wrap = !p.Wrap
			a.WrapDefault = p.Wrap
			p.Viewport.Left, p.Viewport.TopRow = 0, 0
			p.FollowCursor()
			a.status = "wrap off"
			if p.Wrap {
				a.status = "wrap on"
			}
		}
		return true
	case keys.ToggleSidebar:
		a.toggleSidebar()
	case keys.FilePicker:
		a.Picker.Show()
		a.focus = FocusPicker
	case keys.FindInFile:
		if p := a.Tabs.Active(); p != nil {
			if p.Find.Open {
				p.Find.Handle(p, keys.FindInFile, "")
			} else {
				p.Find.Show(p)
			}
			a.focus = FocusEditor
			return true
		}
		return false
	case keys.GotoLine:
		a.gotoLine()
	case keys.GotoSymbol:
		a.gotoSymbol()
	case keys.NewFile:
		a.newFile()
	case keys.CloseTab:
		a.closeTab()
	case keys.ReopenTab:
		// Route through OpenFile so a reopened tab gets the same treatment as
		// any other: theme, highlighting, focus.
		if path, ok := a.Tabs.PopClosed(); ok {
			a.OpenFile(path)
		}
	case keys.NextTab:
		a.Tabs.Next()
		a.focusEditor()
	case keys.PrevTab:
		a.Tabs.Prev()
		a.focusEditor()
	default:
		if n, isTab := tabNumber(action); isTab {
			a.Tabs.Goto(n)
			a.focusEditor()
			return true
		}
		return false
	}
	return true
}

// openSidebar shows a sidebar pane and focuses it. Pressing the chord for the
// pane that already has focus closes it, which makes the binding a toggle
// without needing a second key.
func (a *App) openSidebar(s Sidebar) {
	if a.sidebar == s && a.focus == FocusSidebar {
		a.sidebar = SidebarNone
		a.focus = FocusEditor
		return
	}
	a.sidebar = s
	a.focus = FocusSidebar
	switch s {
	case SidebarExplorer:
		a.Explorer.Focus()
	case SidebarSearch:
		a.Search.Focus()
	}
}

func (a *App) toggleSidebar() {
	if a.sidebar == SidebarNone {
		a.sidebar = SidebarExplorer
		a.focus = FocusSidebar
		a.Explorer.Focus()
		return
	}
	a.sidebar = SidebarNone
	a.focus = FocusEditor
}

// handleSidebar routes to the open sidebar pane. Both panes report when focus
// has walked off either end — tab past the last component or shift+tab back
// past the first — at which point it crosses to the editor. Coming back is a
// chord, deliberately: tab indents in the document, so a one-key route in would
// make editing interruptible.
func (a *App) handleSidebar(action keys.Action, text string) {
	switch a.sidebar {
	case SidebarExplorer:
		path, exit := a.Explorer.Handle(action, text)
		if path != "" {
			a.OpenFile(path)
		}
		if exit {
			a.focus = FocusEditor
		}
	case SidebarSearch:
		path, line, exit := a.Search.Handle(action, text)
		if path != "" {
			a.OpenFile(path)
			a.jumpTo(line)
		}
		if exit {
			a.focus = FocusEditor
		}
	}
}

func (a *App) handleEditor(action keys.Action, text string) {
	p := a.Tabs.Active()
	if p != nil && p.Find.Open {
		p.Find.Handle(p, action, text)
		return
	}
	if p == nil {
		if action == keys.FilePicker {
			a.Picker.Show()
			a.focus = FocusPicker
		}
		return
	}
	// The completion popup sees keys before the editor, but claims only the
	// handful it navigates with. Everything else falls through and types,
	// which is what keeps it from being modal: it can be ignored entirely.
	// The prefix has to be read before Handle, which clears it on accept.
	prefix := a.Complete.Prefix()
	if c, accepted, consumed := a.Complete.Handle(action); consumed {
		if accepted {
			a.acceptCompletion(p, prefix, c)
		}
		return
	}
	if action != keys.None {
		if !p.Handle(action) {
			a.status = "unhandled: " + string(action)
		}
		a.offerCompletion(p, action == keys.Backspace)
		return
	}
	p.HandleText(text)
	a.Explorer.Tree.MarkChanged(p.File.Path)
	a.offerCompletion(p, true)
}

// paste routes a bracketed-paste payload to whatever has focus.
//
// Gating this on the editor meant a paste into the search box or the picker
// vanished with no feedback — the payload arrives as one event rather than as
// keystrokes, so the text fields never saw it at all and there was nothing for
// them to fall back to.
func (a *App) paste(text string) {
	if text == "" {
		return
	}
	if a.focus == FocusEditor {
		a.pasteIntoBuffer(text)
		return
	}
	// A single-line field takes the first line only. It has nowhere to put the
	// rest, and inserting literal newlines gives a query box that renders as
	// control-byte placeholders and searches for something no file contains.
	line := firstLine(text)
	if line == "" {
		return
	}
	switch {
	case a.focus == FocusPrompt:
		a.Prompt.Handle(keys.None, line)
		a.settlePrompt()
	case a.focus == FocusPicker:
		// Not Handle: the picker narrows a pasted path against its index
		// rather than taking it verbatim, and a paste never chooses a file.
		a.Picker.Paste(line)
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		a.handleSidebar(keys.None, line)
	}
	// The explorer has no text field, so a paste there is deliberately ignored
	// rather than routed: its only keys.None handler treats a space as the
	// changed-only toggle, and a pasted space should not flip a filter.
}

// pasteIntoBuffer inserts into the active document. Text from outside cannot
// reuse pieces — unless it is byte-identical to what raj last copied, which is
// the common case of cmd+c then cmd+v and is worth catching.
func (a *App) pasteIntoBuffer(text string) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	clip := a.clipboard
	if clip.Text != text || !clip.Internal() {
		clip = editor.Clip{Text: text}
	}
	p.PasteClip(clip)
	a.Explorer.Tree.MarkChanged(p.File.Path)
}

// firstLine is the payload up to its first newline, trimmed. A path pasted from
// a shell arrives with a trailing newline, which would otherwise be searched for
// literally.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// gotoLine asks for a line number and jumps there.
//
// It accepts "12", "12:5" and ":5" — the line:column form because that is what
// a compiler, a linter and a stack trace all print, and pasting one in should
// work without editing it down. Out-of-range values clamp rather than refuse:
// "go to line 9999" in a 300-line file means the end, and an error message
// there would be pedantry.
// gotoSymbol lists the active file's declarations in the quick-open overlay.
//
// It reuses the picker rather than adding an overlay: a symbol answers with the
// file it lives in and a line, which is the same shape a pasted path already
// had, so opening and jumping is the path openFromPicker was already on.
func (a *App) gotoSymbol() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if !symbols.Supported(p.File.Path) {
		a.status = "no symbols for this file type"
		return
	}
	syms := symbols.Find(p.File.Path, p.File.Text())
	if len(syms) == 0 {
		a.status = "no symbols found"
		return
	}
	a.Picker.ShowSymbols(p.File.Path, syms)
	a.focus = FocusPicker
	a.status = ""
}

func (a *App) gotoLine() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	here, _ := p.File.LineCol(p.Cursors.Primary().Head)
	a.askSuggestion("Go to line", itoa(here+1), func(answer string, ok bool) {
		if !ok {
			return
		}
		line, col, valid := parsePosition(answer)
		if !valid {
			a.status = "not a line number: " + answer
			return
		}
		if line == 0 {
			line = here + 1 // ":40" is a column on the line already showing
		}
		a.jumpTo(line)
		if col > 0 {
			a.jumpToColumn(line, col)
		}
	})
}

// parsePosition reads "line", "line:col" or ":col". A missing line means the
// one the cursor is already on, which is what ":40" from a column-only
// reference should mean.
func parsePosition(text string) (line, col int, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, false
	}
	lineText, colText := text, ""
	if i := strings.IndexByte(text, ':'); i >= 0 {
		lineText, colText = text[:i], text[i+1:]
	}
	if lineText != "" {
		if line, ok = atoi(lineText); !ok {
			return 0, 0, false
		}
	}
	if colText != "" {
		if col, ok = atoi(colText); !ok {
			return 0, 0, false
		}
	}
	return line, col, line > 0 || col > 0
}

// atoi accepts a non-negative decimal and nothing else. strconv would accept a
// leading sign, and "go to line -3" is a typo rather than a request.
func atoi(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30, true // clamped below anyway
		}
	}
	return n, len(s) > 0
}

// jumpToColumn places the cursor within a line already scrolled to.
//
// Out-of-range values need no guard here: Index.LineStart, Viewport.Center and
// File.OffsetAt all clamp, which is where that invariant belongs. "Go to line
// 9999" in a 300-line file lands on the end because the index says so, not
// because this function checked.
func (a *App) jumpToColumn(line, col int) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	off := p.File.OffsetAt(line-1, col-1)
	p.Cursors.Set(off, off)
}

// jumpTo moves the active pane's cursor to a 1-based line and centres it.
func (a *App) jumpTo(line int) {
	p := a.Tabs.Active()
	if p == nil || line <= 0 {
		return
	}
	off := p.File.LineStart(line - 1)
	p.Cursors.Set(off, off)
	p.Viewport.Center(line-1, p.File.Lines())
}

// ---------- file lifecycle ----------
//
// New, save and close are one story rather than three features, because the
// interesting cases are where they meet: an unnamed buffer has nowhere to save
// to, and a dirty buffer must not be closed without asking. Both answers arrive
// from a dialog, so each step below takes a continuation and runs it only once
// the previous question has actually been answered.

// newFile opens an empty unnamed buffer.
func (a *App) newFile() {
	p := a.Tabs.NewFile()
	p.File.SetDark(a.host.Theme().Dark())
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	a.focus = FocusEditor
	a.status = ""
}

// closeTab closes the active tab, stopping to ask when it holds unsaved work.
//
// The guard lives here rather than in Tabs because Tabs is a container: it has
// no way to ask a question, and no business deciding whether losing an edit is
// acceptable.
func (a *App) closeTab() {
	p := a.Tabs.Active()
	if p == nil || !p.File.Dirty() {
		a.Tabs.Close()
		return
	}
	a.confirm("Unsaved changes", "Save changes to "+p.File.Name()+" before closing?",
		prompt.SaveOptions(), func(answer string, ok bool) {
			switch {
			case !ok || answer == prompt.Cancel:
				return
			case answer == prompt.Discard:
				a.Tabs.Close()
			default:
				// Close only once the bytes are on disk. A save-as can be
				// cancelled, and closing anyway would discard exactly the work
				// the answer asked to keep.
				a.saveActive(func(saved bool) {
					if saved {
						a.Tabs.Close()
					}
				})
			}
		})
}

// saveActive writes the active buffer, asking where to put it when it has no
// path yet. then reports whether the bytes reached the disk — "the user pressed
// save" and "the file was saved" are different events, and a caller about to
// close a tab needs the second.
func (a *App) saveActive(then func(saved bool)) {
	p := a.Tabs.Active()
	if p == nil {
		report(then, false)
		return
	}
	if p.File.Path == "" {
		a.saveAs(p, then)
		return
	}
	a.writeTo(p, p.File.Path, then)
}

// saveAs asks where an unnamed buffer should go.
//
// The field is seeded with the workspace root and a separator so the common
// answer is a bare file name, and a relative answer is resolved against the
// root rather than the process's working directory — which is wherever raj
// happened to be launched from and is not what "notes.md" means to someone
// looking at this tree.
func (a *App) saveAs(p *editor.Pane, then func(saved bool)) {
	a.ask("Save as", a.root+string(filepath.Separator), func(answer string, ok bool) {
		if !ok || answer == "" {
			a.status = "save cancelled"
			report(then, false)
			return
		}
		path := answer
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.root, path)
		}
		// Stat rather than trusting the name: the picker and the tree both
		// show what is already there, but a typed path does not, and silently
		// replacing a file is the one outcome nobody recovers from.
		if _, err := os.Stat(path); err == nil {
			a.confirm("File exists", filepath.Base(path)+" already exists. Overwrite?",
				[]string{prompt.Overwrite, prompt.Cancel}, func(ans string, ok bool) {
					if !ok || ans != prompt.Overwrite {
						a.status = "save cancelled"
						report(then, false)
						return
					}
					a.writeTo(p, path, then)
				})
			return
		}
		a.writeTo(p, path, then)
	})
}

// writeTo names the buffer if needed and writes it.
func (a *App) writeTo(p *editor.Pane, path string, then func(saved bool)) {
	renamed := p.File.Path != path
	p.File.SetPath(path)
	if err := p.File.Save(); err != nil {
		a.status = "save failed: " + err.Error()
		report(then, false)
		return
	}
	a.status = "saved " + p.File.Name()
	if renamed {
		// A rename is the only save that puts a file in the tree that was not
		// there before. Refreshing on every save would walk the directory on
		// the keystroke path for nothing.
		a.Explorer.Tree.Refresh()
	}
	report(then, true)
}

func report(then func(bool), ok bool) {
	if then != nil {
		then(ok)
	}
}

// tryQuit exits, stopping first if anything would be lost.
//
// cmd+w already guards one tab. Leaving quit unguarded made that a property of
// which chord you happened to press rather than of the buffer, and quit is the
// one with every unsaved tab behind it rather than one.
//
// A second Quit while the question is on screen forces the exit. ctrl+c is what
// people press when they want out now, and a dialog that answers it by asking
// again is the wedge the modal was written to avoid.
func (a *App) tryQuit() {
	if a.quitAsked {
		a.quit = true
		return
	}
	dirty := a.Tabs.Dirty()
	if len(dirty) == 0 {
		a.quit = true
		return
	}
	a.quitAsked = true
	a.confirm("Unsaved changes", quitMessage(dirty), prompt.SaveOptions(),
		func(answer string, ok bool) {
			a.quitAsked = false
			switch {
			case !ok || answer == prompt.Cancel:
				return
			case answer == prompt.Discard:
				a.quit = true
			default:
				a.saveAllThenQuit(dirty)
			}
		})
}

// quitMessage names the file when there is one and counts them when there are
// several. A list of names would not fit the dialog, and a bare count when only
// one thing is at stake withholds the only detail that matters.
func quitMessage(dirty []*editor.Pane) string {
	if len(dirty) == 1 {
		return "Save changes to " + dirty[0].File.Name() + " before quitting?"
	}
	return "Save changes to " + itoa(len(dirty)) + " files before quitting?"
}

// saveAllThenQuit walks the dirty tabs, saving each and quitting only if they
// all land.
//
// It recurses through the continuation rather than looping, because any of them
// may be unnamed and stop for a path — and a loop would have run to the end
// before the first dialog was answered. Each tab is focused before it is saved,
// so a save-as dialog is asking about the buffer on screen.
func (a *App) saveAllThenQuit(dirty []*editor.Pane) {
	if len(dirty) == 0 {
		a.quit = true
		return
	}
	p := dirty[0]
	a.Tabs.Focus(p)
	a.saveActive(func(saved bool) {
		if !saved {
			// Cancelling a path is cancelling the quit. Exiting anyway would
			// discard exactly the work the answer asked to keep.
			a.status = "quit cancelled"
			return
		}
		a.saveAllThenQuit(dirty[1:])
	})
}

// ---------- dialogs ----------

// ask and confirm open a modal question and hand it focus. Both hide the file
// picker: it is the other full-screen overlay, and two of them at once means
// keys going somewhere invisible.
func (a *App) ask(title, initial string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.Ask(title, initial, done)
}

// askSuggestion is ask with the seed selected: the field offers a default that
// typing replaces rather than a prefix that typing extends.
func (a *App) askSuggestion(title, suggestion string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.AskSuggestion(title, suggestion, done)
}

func (a *App) confirm(title, message string, options []string, done func(string, bool)) {
	a.beforePrompt()
	a.Prompt.Confirm(title, message, options, done)
}

func (a *App) beforePrompt() {
	if a.focus != FocusPrompt {
		a.promptReturn = a.focus
	}
	a.Picker.Hide()
	if a.promptReturn == FocusPicker {
		a.promptReturn = FocusEditor
	}
	a.focus = FocusPrompt
}

// settlePrompt restores focus once a dialog has closed for good. A continuation
// is free to open the next question in a chain, so this checks whether one did
// rather than assuming an answered dialog is the end of the story.
func (a *App) settlePrompt() {
	if !a.Prompt.Open && a.focus == FocusPrompt {
		a.focus = a.promptReturn
	}
}

// tabNumber maps the goto-tab actions to their index.
func tabNumber(a keys.Action) (int, bool) {
	for i, want := range []keys.Action{keys.GotoTab1, keys.GotoTab2, keys.GotoTab3,
		keys.GotoTab4, keys.GotoTab5, keys.GotoTab6, keys.GotoTab7, keys.GotoTab8,
		keys.GotoTab9} {
		if a == want {
			return i + 1, true
		}
	}
	return 0, false
}

// Status returns the current status message, for tests.
func (a *App) Status() string { return a.status }

// Focused reports which pane has focus, for tests.
func (a *App) Focused() Focus { return a.focus }

// SidebarMode reports which sidebar is open, for tests.
func (a *App) SidebarMode() Sidebar { return a.sidebar }

// Pane is the active editing pane, or nil.
func (a *App) Pane() *editor.Pane { return a.Tabs.Active() }

// focusEditor moves focus to the editor if a file is open. Choosing a tab means
// you intend to read or edit it, so leaving the keys in the sidebar makes every
// tab switch a two-step operation.
func (a *App) focusEditor() {
	if a.Tabs.Active() != nil {
		a.focus = FocusEditor
	}
}

// refreshSyntax starts a background retokenise if the text has changed. It runs
// after every key rather than only on the idle tick: tokenising is on its own
// goroutine, so starting it immediately costs nothing on the keystroke path and
// removes the visible lag between editing a line and it recolouring.
func (a *App) refreshSyntax() {
	if p := a.Tabs.Active(); p != nil {
		p.File.RefreshSyntax()
	}
}

// focusedInput is the text field the keys are going into, or nil when they are
// going into the document, a list, or a toggle.
//
// cut and copy are global actions — they are claimed before any pane sees the
// chord — so without this they always acted on Tabs.Active(): cmd+c in the
// search box copied from the editor, and cmd+x edited the document being
// searched. Asking the focused thing whether it owns a selection is the fix,
// and the find bar needs it as much as the sidebar does, because it lives
// inside the editor pane rather than beside it.
func (a *App) focusedInput() *widget.Input {
	if in := a.Prompt.ActiveInput(); in != nil {
		return in // modal: it owns the keys whatever focus says
	}
	switch {
	case a.focus == FocusPicker:
		return a.Picker.ActiveInput()
	case a.focus == FocusSidebar && a.sidebar == SidebarSearch:
		return a.Search.ActiveInput()
	case a.focus == FocusEditor:
		if p := a.Tabs.Active(); p != nil {
			return p.Find.ActiveInput()
		}
	}
	return nil
}

// clip copies or cuts to the system clipboard via OSC 52, and keeps an internal
// copy so paste works even where the terminal refuses clipboard writes.
func (a *App) clip(cut bool) {
	if in := a.focusedInput(); in != nil {
		a.clipField(in, cut)
		return
	}
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	clip := p.Copy()
	if cut {
		clip = p.Cut()
	}
	if clip.Empty() {
		return
	}
	a.clipboard = clip
	a.host.SetClipboard(clip.Text)
	verb := "copied"
	if cut {
		verb = "cut"
	}
	a.status = verb + " " + itoa(len(clip.Text)) + " bytes"
}

// clipField copies or cuts inside a text field. A field carries no pieces, so
// this is plain text rather than an editor.Clip with a snapshot — pasting it
// back into a document costs a copy, which for a search query is nothing.
func (a *App) clipField(in *widget.Input, cut bool) {
	text := in.Copy()
	if cut {
		text = in.Cut()
	}
	if text == "" {
		// Nothing selected. Deliberately not falling back to the whole field:
		// cmd+x with no selection emptying the search box would be a
		// destructive surprise, and the document behaviour it would be
		// imitating (cut the current line) has no counterpart here.
		return
	}
	a.clipboard = editor.Clip{Text: text}
	a.host.SetClipboard(text)
	verb := "copied"
	if cut {
		verb = "cut"
	}
	a.status = verb + " " + itoa(len(text)) + " bytes"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
