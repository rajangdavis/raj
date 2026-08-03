// Package app is the event loop: it owns focus, routes keys to whichever pane
// has it, and decides when to repaint. Everything it drives is host-agnostic,
// so the whole application runs headlessly under ui.FakeHost.
package app

import (
	"raj/internal/editor"
	"raj/internal/explorer"
	"raj/internal/keys"
	"raj/internal/picker"
	"raj/internal/search"
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

	root    string
	sidebar Sidebar
	focus   Focus
	theme   editor.Theme
	wth     widget.Theme

	Debug      debugLog
	clipboard  editor.Clip
	dark       bool
	lastLayout Layout
	status     string
	focused    bool
	quit       bool
}

// New builds an application rooted at a directory.
func New(host ui.Host, root string, tabWidth int) *App {
	cols, rows := host.Size()
	return &App{
		host:     host,
		keymap:   keys.NewKeymap(),
		screen:   ui.NewScreen(cols, rows),
		Tabs:     tabs.New(tabWidth),
		Explorer: explorer.NewPane(root),
		Search:   search.NewPane(root),
		Picker:   picker.New(root),
		root:     root,
		sidebar:  SidebarExplorer,
		focus:    FocusSidebar,
		theme:    editor.DefaultTheme(),
		dark:     host.Theme().Dark(),
		wth:      widget.DefaultTheme(),
		focused:  true,
	}
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
	a.focus = FocusEditor
	a.status = ""
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
		// A bracketed paste is text from outside, so it cannot reuse pieces —
		// unless it is byte-identical to what raj last copied, which is the
		// common case of cmd+c then cmd+v and is worth catching.
		if p := a.Tabs.Active(); p != nil && a.focus == FocusEditor {
			clip := a.clipboard
			if clip.Text != ev.Text || !clip.Internal() {
				clip = editor.Clip{Text: ev.Text}
			}
			p.PasteClip(clip)
		}
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
	if a.handleGlobal(action) {
		return
	}
	defer a.refreshSyntax()

	switch a.focus {
	case FocusPicker:
		a.OpenFile(a.Picker.Handle(action, text))
		if !a.Picker.Open && a.focus == FocusPicker {
			a.focus = FocusEditor
		}
	case FocusSidebar:
		a.handleSidebar(action, text)
	default:
		a.handleEditor(action, text)
	}
}

// scope is the keymap scope for the focused pane. It is what makes tab indent
// in the editor and cycle focus everywhere else.
func (a *App) scope() keys.Scope {
	switch {
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
		a.quit = true
	case keys.Suspend:
		a.host.Suspend()
	case keys.Save:
		a.save()
	case keys.Cut:
		a.clip(true)
	case keys.Copy:
		a.clip(false)
	case keys.FocusExplorer:
		a.openSidebar(SidebarExplorer)
	case keys.FocusSearch:
		a.openSidebar(SidebarSearch)
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
	case keys.CloseTab:
		a.Tabs.Close()
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

// handleSidebar routes to the open sidebar pane. Both panes report when tab has
// walked off their last component, at which point focus crosses to the editor —
// and cannot come back with shift+tab, only with a chord.
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
	if action != keys.None {
		if !p.Handle(action) {
			a.status = "unhandled: " + string(action)
		}
		return
	}
	p.HandleText(text)
	a.Explorer.Tree.MarkChanged(p.File.Path)
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

func (a *App) save() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if err := p.File.Save(); err != nil {
		a.status = "save failed: " + err.Error()
		return
	}
	a.status = "saved " + p.File.Name()
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

// clip copies or cuts to the system clipboard via OSC 52, and keeps an internal
// copy so paste works even where the terminal refuses clipboard writes.
func (a *App) clip(cut bool) {
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
