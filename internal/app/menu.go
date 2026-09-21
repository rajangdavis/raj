package app

import (
	"path/filepath"
	"strings"

	"raj/internal/editor"
	"raj/internal/explorer"
	"raj/internal/piecetable"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Context menus: right-click on an explorer entry or a tab, and the same menu
// opened from the keyboard for whatever has focus. The menu itself is a widget;
// this file is the app's half - what the menu is about, the item sets, and the
// dispatch of a chosen item onto the entry points the keyboard already uses.
//
// The pointer contract in pointer.go decides who gets a press. While the menu
// is open it is drawn over everything but a dialog, so it takes the press
// first: a left-click on a row chooses it, and a press anywhere else closes it
// and is swallowed. Keys follow the same rule from handleKey.

// The dispatch keys a menu row carries. They are the app's own names for the
// actions, not labels: the label is what a row shows, the key is what
// chooseMenu switches on, so rewording a label cannot break dispatch.
const (
	menuOpen         = "open"
	menuNewFile      = "new-file"
	menuNewFolder    = "new-folder"
	menuRename       = "rename"
	menuRenameFile   = "rename-file"
	menuDelete       = "delete"
	menuDeleteFile   = "delete-file"
	menuRemoveFolder = "remove-folder"
	menuCollapse     = "collapse"
	menuExpand       = "expand"
	menuCopyPath     = "copy-path"
	menuClose        = "close"
	menuSave         = "save"
	menuReveal       = "reveal"
)

// menuKind says what an open menu is about.
type menuKind int

const (
	menuNone menuKind = iota
	menuFile
	menuDir
	menuTab
)

// menuTarget names what an open menu acts on. It is captured when the menu
// opens and cleared when it closes, so a chosen item dispatches against the
// target the user saw rather than whatever happens to have focus later.
type menuTarget struct {
	kind menuKind
	path string // the file or directory, or the tab's file
	tab  int    // the tab index for a tab menu; -1 otherwise
}

// openMenu shows the menu for t, anchored at the cell the gesture asked for. A
// target with no items opens nothing, which is the "no menu here" case.
//
// The anchor is remembered rather than the final origin: drawMenu places the
// box against the current frame size every time it draws, so a resize between
// the gesture and the frame cannot leave the hit test measuring stale geometry.
func (a *App) openMenu(t menuTarget, col, row int) {
	if a.Prompt.Open {
		return
	}
	title, items := a.menuItems(t)
	if len(items) == 0 {
		return
	}
	// The menu is the only overlay open now: a dialog would have swallowed the
	// gesture, and the picker is dismissed so two overlays cannot fight over
	// the next press.
	a.Picker.Hide()
	if a.focus == FocusPicker {
		a.focus = FocusEditor
	}
	a.menuTarget = t
	a.menuAnchorCol, a.menuAnchorRow = col, row
	a.Menu.Show(title, items)
}

// closeMenu hides the menu and forgets what it was about.
func (a *App) closeMenu() {
	a.Menu.Hide()
	a.menuTarget = menuTarget{}
}

// drawMenu places the menu against the current frame and paints it, storing
// the origin it drew at so RowAt measures the box the user actually saw. It is
// called from Draw, after the picker and before a dialog, which is the order
// rightClick resolves a press in.
func (a *App) drawMenu(cols, rows int) {
	// The widget places the box itself from the anchor: it slides left and
	// flips above to stay on screen. Origin is where it actually drew, which
	// is what RowAt must measure against, so read it back rather than keeping
	// a second copy of the placement rule.
	a.Menu.Render(a.screen, a.menuAnchorCol, a.menuAnchorRow, cols, rows, a.wth)
	a.menuCol, a.menuRow = a.Menu.Origin()
}

// rightClick is the pointer half of the context menu, called from pointer for
// a right-button press. The open menu has already had its refusal in pointer;
// this is the path that resolves a target and opens one.
//
// It follows the left-click order exactly: a dialog swallows the press, then
// the picker, then the panes. The editor has no menu, so a right-click there
// does nothing.
func (a *App) rightClick(l Layout, ev ui.Mouse) {
	if a.Prompt.Open {
		return
	}
	if a.Picker.Open {
		// The picker is the other overlay. A right-click off it dismisses it,
		// the way a left-click does, and does not open a menu on the file the
		// list was covering.
		a.Picker.Hide()
		a.focus = FocusEditor
		return
	}
	// The tab bar first, then the explorer, the same order the left-click
	// branch checks them. They occupy different rows, so the order is the
	// contract rather than a tie-break.
	if ev.Row == l.TabY {
		a.tabMenu(ev)
		return
	}
	if l.ShowSidebar && a.sidebar == SidebarExplorer &&
		ev.Col >= l.SidebarX && ev.Col < l.SidebarX+l.SidebarW &&
		ev.Row >= l.TopY && ev.Row < l.TopY+l.Rows {
		a.explorerMenu(l, ev)
	}
}

// tabMenu opens the menu for the tab drawn under the pointer.
func (a *App) tabMenu(ev ui.Mouse) {
	cols, _ := a.screen.Size()
	i, ok := a.Tabs.HitTest(0, cols, ev.Col)
	if !ok {
		return
	}
	panes := a.Tabs.All()
	if i < 0 || i >= len(panes) {
		return
	}
	p := panes[i]
	a.openMenu(menuTarget{kind: menuTab, path: p.File.Path, tab: i}, ev.Col, ev.Row)
}

// explorerMenu opens the menu for the explorer row under the pointer, and
// selects that row so the menu and the highlight agree about what it targets.
func (a *App) explorerMenu(l Layout, ev ui.Mouse) {
	idx, e, ok := a.explorerRowAt(l, ev.Col, ev.Row)
	if !ok {
		return
	}
	a.Explorer.List().Sel = idx
	kind := menuFile
	if e.Dir {
		kind = menuDir
	}
	a.openMenu(menuTarget{kind: kind, path: e.Path}, ev.Col, ev.Row)
}

// explorerRowAt resolves a screen cell to the explorer row drawn in it. It uses
// the pane RowAt, the same mapping the left-click path uses, so a tall block
// resolves to one entry on both paths; RowAt never toggles or opens anything,
// which is what the menu path needs.
func (a *App) explorerRowAt(l Layout, col, row int) (int, explorer.Entry, bool) {
	if !l.ShowSidebar || a.sidebar != SidebarExplorer {
		return 0, explorer.Entry{}, false
	}
	if col < l.SidebarX || col >= l.SidebarX+l.SidebarW {
		return 0, explorer.Entry{}, false
	}
	if row < l.SidebarTop || row >= l.SidebarTop+l.SidebarRows {
		return 0, explorer.Entry{}, false
	}
	return a.Explorer.RowAt(row-l.SidebarTop, l.SidebarRows)
}

// openContextMenu opens the menu for whatever has focus: the focused explorer
// entry, or otherwise the active tab. It is the keyboard half of the same menu
// a right-click opens, so every item is reachable without a mouse.
func (a *App) openContextMenu() {
	cols, rows := a.screen.Size()
	l := a.layout(cols, rows)
	if a.focus == FocusSidebar && a.sidebar == SidebarExplorer {
		entries := a.Explorer.Tree.Entries()
		sel := a.Explorer.List().Sel
		if sel >= 0 && sel < len(entries) {
			e := entries[sel]
			kind := menuFile
			if e.Dir {
				kind = menuDir
			}
			row := l.SidebarTop + a.Explorer.RowOffset()
			if row < l.SidebarTop || row >= l.SidebarTop+l.SidebarRows {
				row = l.SidebarTop
			}
			a.openMenu(menuTarget{kind: kind, path: e.Path}, l.SidebarX+2, row)
			return
		}
	}
	if p := a.Tabs.Active(); p != nil {
		col, row := a.tabAnchor(l)
		a.openMenu(menuTarget{kind: menuTab, path: p.File.Path, tab: a.Tabs.Index()}, col, row)
	}
}

// tabAnchor is the cell of the active tab's label, for a menu opened from the
// keyboard. It scans the bar rather than re-deriving the span layout, so it
// cannot disagree with HitTest about where a tab starts.
func (a *App) tabAnchor(l Layout) (int, int) {
	cols, _ := a.screen.Size()
	want := a.Tabs.Index()
	for col := 0; col < cols; col++ {
		if i, ok := a.Tabs.HitTest(0, cols, col); ok && i == want {
			return col, l.TabY
		}
	}
	return 0, l.TabY
}

// menuItems is the item set for a target. A target with no items is the
// "nothing to show" case and openMenu ignores it. Labels are for the eye; the
// Key is what chooseMenu dispatches on.
func (a *App) menuItems(t menuTarget) (string, []widget.MenuItem) {
	switch t.kind {
	case menuFile:
		if t.path == "" {
			return "", nil
		}
		return filepath.Base(t.path), []widget.MenuItem{
			{Label: "Open", Key: menuOpen, Enabled: true},
			{Label: "New File", Key: menuNewFile, Detail: filepath.Dir(t.path), Enabled: true},
			{Label: "New Folder", Key: menuNewFolder, Enabled: true},
			{Label: "Rename", Key: menuRename, Enabled: true},
			{Label: "Delete", Key: menuDelete, Enabled: true},
			{Label: "Copy Path", Key: menuCopyPath, Enabled: true},
		}
	case menuDir:
		if t.path == "" {
			return "", nil
		}
		toggle, key := "Collapse", menuCollapse
		if !a.Explorer.Tree.Expanded(t.path) {
			toggle, key = "Expand", menuExpand
		}
		return filepath.Base(t.path), []widget.MenuItem{
			{Label: "New File", Key: menuNewFile, Detail: t.path, Enabled: true},
			{Label: "New Folder", Key: menuNewFolder, Enabled: true},
			{Label: "Rename", Key: menuRename, Enabled: true},
			{Label: "Remove Folder", Key: menuRemoveFolder, Enabled: true},
			{Label: toggle, Key: key, Enabled: true},
			{Label: "Copy Path", Key: menuCopyPath, Enabled: true},
		}
	case menuTab:
		named := t.path != ""
		return tabTitle(t.path), []widget.MenuItem{
			{Label: "Close", Key: menuClose, Enabled: true},
			{Label: "Save", Key: menuSave, Enabled: true},
			{Label: "Rename File", Key: menuRenameFile, Enabled: named},
			{Label: "Delete File", Key: menuDeleteFile, Enabled: named},
			{Label: "Reveal", Key: menuReveal, Enabled: named},
		}
	}
	return "", nil
}

// tabTitle names a tab menu. An unnamed buffer has no path to show, so it is
// titled the way the tab bar titles it.
func tabTitle(path string) string {
	if path == "" {
		return "Untitled"
	}
	return filepath.Base(path)
}

// chooseMenu runs the item a menu row names and closes the menu. It is the one
// dispatch point, so a row clicked with the mouse and a row confirmed with
// enter run identical code.
func (a *App) chooseMenu(key string) {
	t := a.menuTarget
	a.closeMenu()
	if key == "" || t.kind == menuNone {
		return
	}
	switch key {
	case menuOpen:
		a.OpenFile(t.path)
	case menuClose:
		a.closeTabAt(t.tab)
	case menuSave:
		a.saveTab(t.path)
	case menuRename, menuRenameFile:
		a.renamePath(t.path)
	case menuDelete, menuDeleteFile:
		a.proposeDeleteFile(t.path)
	case menuRemoveFolder:
		a.proposeRemoveFolder(t.path)
	case menuNewFile:
		a.newFileAt(menuDirFor(t))
	case menuNewFolder:
		a.newFolderAt(menuDirFor(t))
	case menuCopyPath:
		a.copyPath(t.path)
	case menuCollapse, menuExpand:
		a.Explorer.Tree.Toggle(t.path)
	case menuReveal:
		a.revealPath(t.path)
	}
}

// menuDirFor is the directory a create item acts in: the target itself for a
// directory row, and the target's own directory for a file row.
func menuDirFor(t menuTarget) string {
	if t.kind == menuDir {
		return t.path
	}
	return filepath.Dir(t.path)
}

// saveTab saves the tab for path. It looks the pane up by path rather than by
// the index captured at open time, because a tab may have moved under the
// menu even though the menu is modal.
func (a *App) saveTab(path string) {
	for _, p := range a.Tabs.All() {
		if path == p.File.Path || sameFile(path, p.File.Path) {
			a.savePane(p, nil)
			return
		}
	}
}

// renamePath collects a new name through the shared prompt and moves the file
// or directory through host.Rename, the same entry point `raj ctl rename`
// uses. That path carries an open clean buffer with the name, refuses a dirty
// one, and refreshes the tree.
func (a *App) renamePath(path string) {
	if path == "" {
		return
	}
	a.askSuggestion("Rename", filepath.Base(path), func(answer string, ok bool) {
		name := strings.TrimSpace(answer)
		if !ok || name == "" {
			a.status = "rename cancelled"
			return
		}
		if name == filepath.Base(path) {
			return
		}
		if strings.ContainsRune(name, filepath.Separator) {
			a.status = "a new name, not a path"
			return
		}
		next := filepath.Join(filepath.Dir(path), name)
		if err := (host{a: a}).Rename(path, next); err != nil {
			a.status = "cannot rename: " + err.Error()
			return
		}
		a.status = "renamed to " + name
		a.TouchSession()
	})
}

// newFileAt is the explorer-anchored create: it opens an empty unnamed buffer
// and drives the ordinary save-as prompt seeded with dir, exactly as cmd+n
// followed by save-as would. A keyboard create path has always existed (cmd+n
// then save-as); what the menu adds is the explorer anchor, not file creation.
// The buffer's own save makes the file, through the same atomic-write,
// missing-parent and encoding path as any other save, and cancelling the
// prompt closes the scratch buffer rather than leaving an untitled tab.
func (a *App) newFileAt(dir string) {
	if dir == "" || dir == "." {
		dir = a.root
	}
	a.newFile()
	p := a.Tabs.Active()
	a.saveAsIn(p, dir, func(saved bool) {
		if !saved {
			a.closeNewPane(p)
		}
	})
}

// closeNewPane discards the scratch buffer newFileAt opened when its save-as
// prompt was cancelled, so a create that never happened leaves no untitled tab
// behind. The dialog is modal, so nothing could have been typed into the buffer
// and there is no unsaved work to ask about; a failed save returns the buffer
// to unnamed and empty, so the same close is right there too.
func (a *App) closeNewPane(p *editor.Pane) {
	if p == nil {
		return
	}
	for i, q := range a.Tabs.All() {
		if q == p {
			a.closeDoc(p)
			a.Tabs.CloseIndex(i)
			a.refreshProblems()
			return
		}
	}
}

// newFolderAt collects a name and creates a directory through host.Mkdir, the
// same entry point `raj ctl mkdir` uses, so the tree refresh happens there.
func (a *App) newFolderAt(dir string) {
	if dir == "" || dir == "." {
		dir = a.root
	}
	a.askPath("New folder", dir+string(filepath.Separator), func(answer string, ok bool) {
		if !ok || strings.TrimSpace(answer) == "" {
			return
		}
		path := answer
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.root, path)
		}
		if !a.pathInRoot(path) {
			a.status = "new folder must be inside the workspace"
			return
		}
		if err := (host{a: a}).Mkdir(path); err != nil {
			a.status = "cannot create folder: " + err.Error()
		}
	})
}

// proposeDeleteFile records a deletion proposal. It never unlinks: the
// propose-then-approve flow in deletion.go is the record and the safety, and
// the gate prompt is the only place the file can actually go.
func (a *App) proposeDeleteFile(path string) {
	if path == "" {
		return
	}
	if err := a.ProposeDeletion(path, uint8(piecetable.User)); err != nil {
		a.status = "cannot propose deletion: " + err.Error()
	}
}

// proposeRemoveFolder records a directory-removal proposal, which raises the
// gate's listing immediately because a directory has no pane to focus.
func (a *App) proposeRemoveFolder(path string) {
	if path == "" {
		return
	}
	if err := a.ProposeDirRemoval(path, uint8(piecetable.User)); err != nil {
		a.status = "cannot propose removal: " + err.Error()
	}
}

// copyPath writes a path to the system clipboard through the same host call
// the copy chord uses, and keeps the internal copy so paste works where the
// terminal refuses an OSC 52 write.
func (a *App) copyPath(path string) {
	if path == "" {
		return
	}
	a.clipboard = editor.Clip{Text: path}
	a.host.SetClipboard(path)
	a.status = "copied path " + path
}

// revealPath shows a tab's file in the explorer: it opens the explorer,
// expands every ancestor above the file, and selects its row.
func (a *App) revealPath(path string) {
	if path == "" || a.standalone {
		return
	}
	a.sidebar = SidebarExplorer
	a.focus = FocusSidebar
	a.Explorer.Focus()
	root := filepath.Clean(a.Explorer.Tree.Root)
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if a.pathInRoot(dir) {
			a.Explorer.Tree.Expand(dir)
		}
		if filepath.Clean(dir) == root || dir == filepath.Dir(dir) {
			break
		}
	}
	a.Explorer.Tree.Refresh()
	for i, e := range a.Explorer.Tree.Entries() {
		if e.Path == path {
			a.Explorer.List().Sel = i
			break
		}
	}
	a.status = "revealed " + filepath.Base(path)
}
