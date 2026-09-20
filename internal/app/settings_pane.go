package app

import (
	"strconv"
	"strings"

	"raj/internal/keys"
	"raj/internal/store"
	"raj/internal/ui"
	"raj/internal/widget"
)

// The settings pane is a sidebar view over App.Settings and App.SetSetting. It
// keeps no setting value of its own: every frame re-reads the effective values,
// so a write made anywhere is what the next frame shows. What it does own is
// where the cursor is, which scope the next write goes to, and the digits of a
// half-typed width.
//
// The language-server settings will be a later section under the five editor
// settings. It is deliberately not sketched here as a row: a row with no
// behaviour behind it is dead code, and settingsRows is the seam to add it.

// settingsRowKind is what a row is and how confirming it behaves.
type settingsRowKind int

const (
	settingsRowScope   settingsRowKind = iota // choose the scope writes go to
	settingsRowNumeric                        // a positive integer, edited in place
	settingsRowBool                           // on or off
)

// settingsRow is one line of the pane.
type settingsRow struct {
	key   string // the setting key; empty on the scope row
	label string
	kind  settingsRowKind
}

// settingsRows is the row model, in display order. The scope row leads so the
// scope a change will be written to is visible before the value it changes.
func settingsRows() []settingsRow {
	return []settingsRow{
		{label: "Scope", kind: settingsRowScope},
		{key: settingTabWidth, label: "Tab width", kind: settingsRowNumeric},
		{key: settingTabs, label: "Indent with tabs", kind: settingsRowBool},
		{key: settingWrap, label: "Wrap lines", kind: settingsRowBool},
		{key: settingAutoPairs, label: "Auto pairs", kind: settingsRowBool},
		{key: settingInlayHints, label: "Inlay hints", kind: settingsRowBool},
	}
}

// settingsPane is the model behind the settings sidebar.
type settingsPane struct {
	row   int    // the selected index into settingsRows
	scope string // the scope the next change is written to

	// editing is true while tab_width is being typed, and digits is the buffer
	// being built. It is a byte buffer rather than the shared widget.Input
	// because the pane draws one flat line per setting, not a three-row box;
	// enter commits and escape cancels.
	editing bool
	digits  string
}

// newSettingsPane starts on the first row, writing to the workspace scope.
// Workspace is the default because a setting changed while looking at a project
// is usually a decision about that project.
func newSettingsPane() settingsPane {
	return settingsPane{scope: store.ScopeWorkspace}
}

// Focus clears any half-typed edit, so a width abandoned by closing the pane is
// not waiting when the pane is reopened.
func (p *settingsPane) Focus() {
	p.editing = false
	p.digits = ""
}

func (p *settingsPane) current() settingsRow {
	rows := settingsRows()
	if p.row < 0 || p.row >= len(rows) {
		return rows[0]
	}
	return rows[p.row]
}

// Handle consumes one key and reports whether focus should leave the pane for
// the editor. The exit contract is the other sidebar panes': tab and shift+tab
// cross to the editor, and escape closes the pane.
func (p *settingsPane) Handle(a *App, action keys.Action, text string) (exit bool) {
	if p.editing {
		return p.handleEdit(a, action, text)
	}
	rows := settingsRows()
	switch action {
	case keys.LineUp:
		if p.row > 0 {
			p.row--
		}
	case keys.LineDown:
		if p.row < len(rows)-1 {
			p.row++
		}
	case keys.Confirm:
		p.activate(a)
	case keys.None:
		// Space arrives as literal text, not Confirm, because in the document
		// it is a printable key. On a settings row it means what enter means.
		if text == " " {
			p.activate(a)
		}
	case keys.CharLeft:
		p.step(a, -1)
	case keys.CharRight:
		p.step(a, +1)
	case keys.CycleFocus, keys.CycleFocusBack:
		return true
	case keys.Cancel:
		return true
	}
	return false
}

// activate is enter or space on the selected row: switch the write scope, begin
// editing a width, or flip a bool.
func (p *settingsPane) activate(a *App) {
	r := p.current()
	switch r.kind {
	case settingsRowScope:
		p.toggleScope()
	case settingsRowNumeric:
		p.editing = true
		p.digits = ""
	case settingsRowBool:
		// The stored value is canonical true/false; boolSetting is only the
		// display spelling, so the store keeps one representation of a bool
		// however the pane happens to draw it.
		p.write(a, r.key, strconv.FormatBool(!truthy(a.Settings()[r.key])))
	}
}

// step is left or right: step a width, set a bool, or switch the scope. The
// direction carries the intent for a width and a bool; the scope row flips
// either way because it has only two values.
func (p *settingsPane) step(a *App, delta int) {
	r := p.current()
	switch r.kind {
	case settingsRowScope:
		p.toggleScope()
	case settingsRowNumeric:
		n, ok := parseIntSetting(a.Settings()[r.key])
		if !ok {
			n = 1
		}
		n += delta
		if n < 1 {
			n = 1
		}
		p.write(a, r.key, strconv.Itoa(n))
	case settingsRowBool:
		p.write(a, r.key, strconv.FormatBool(delta > 0))
	}
}

// handleEdit is the tab_width field: digits build the buffer, enter commits,
// escape cancels, and backspace deletes. Anything that is not a digit is
// ignored rather than typed, so the buffer cannot hold a value the resolver
// would refuse.
func (p *settingsPane) handleEdit(a *App, action keys.Action, text string) bool {
	switch action {
	case keys.Cancel:
		p.editing = false
		p.digits = ""
	case keys.Confirm:
		p.commit(a)
	case keys.Backspace:
		if len(p.digits) > 0 {
			p.digits = p.digits[:len(p.digits)-1]
		}
	case keys.None:
		if text == "" || text == "\n" || strings.Trim(text, "0123456789") != "" {
			return false
		}
		p.digits += text
	}
	return false
}

// commit writes the typed width. An empty or invalid buffer is refused on the
// status line rather than written: a bad value would be stored and change
// nothing, which looks like the pane losing the keystroke. The buffer stays
// open so a typo can be corrected.
func (p *settingsPane) commit(a *App) {
	n, ok := parseIntSetting(p.digits)
	if !ok {
		a.status = "tab width must be a positive number"
		return
	}
	p.editing = false
	p.digits = ""
	p.write(a, settingTabWidth, strconv.Itoa(n))
}

// write sends one change through App.SetSetting and reports a refusal on the
// status line. A missing store is the case that matters: the value stays
// unpersisted, and saying so is the difference between a degraded editor and
// one that appears to ignore the key.
func (p *settingsPane) write(a *App, key, value string) {
	if err := a.SetSetting(p.scope, key, value); err != nil {
		a.status = err.Error()
		return
	}
	a.status = ""
}

// toggleScope flips the write scope between the project and the person.
func (p *settingsPane) toggleScope() {
	if p.scope == store.ScopeUser {
		p.scope = store.ScopeWorkspace
		return
	}
	p.scope = store.ScopeUser
}

// boolSetting spells a bool the way the pane shows it.
func boolSetting(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// truthy reads a resolved bool setting. The resolver guarantees a good value;
// the default here only keeps a malformed one from reading as true.
func truthy(raw string) bool {
	b, _ := parseBoolSetting(raw)
	return b
}

// ClickAt selects and activates the row under a press at dy, measured from the
// pane's content origin. The heading is not a row, so dy 0 does nothing.
func (p *settingsPane) ClickAt(a *App, dy int) bool {
	i := dy - 1
	if i < 0 || i >= len(settingsRows()) {
		return false
	}
	p.row = i
	p.editing = false
	p.digits = ""
	p.activate(a)
	return true
}

// Render draws the pane: a heading, then one flat line per row. Values are
// re-read every frame, so the pane is always showing the effective setting.
func (p *settingsPane) Render(a *App, s *ui.Screen, x, y, w, h int, focused bool) {
	if w < 8 || h < 2 {
		return
	}
	th := a.wth
	s.SetString(x+1, y, widget.Truncate("Settings", w-2), th.Heading(focused), w-2)

	vals := a.Settings()
	origin := a.settingOrigins()
	for i, r := range settingsRows() {
		row := y + 1 + i
		if row >= y+h {
			break
		}
		style := th.Focus(i == p.row, focused)
		s.Fill(x, row, w, 1, style)
		s.SetString(x, row, widget.Truncate(p.line(r, vals, origin, w), w), style, w)
	}
}

// line is one row's text: the label left, the value and the scope it came from
// right. The scope row shows the write scope instead of a source, because it
// chooses where a change goes rather than reporting where a value came from.
func (p *settingsPane) line(r settingsRow, vals, origin map[string]string, w int) string {
	value := p.scope
	if r.kind != settingsRowScope {
		value = vals[r.key]
		if r.kind == settingsRowBool {
			if b, ok := parseBoolSetting(value); ok {
				value = boolSetting(b)
			}
		}
		if src := origin[r.key]; src != "" {
			value += "  " + src
		}
		if p.editing && r.key == settingTabWidth {
			value = p.digits
			if value == "" {
				value = "_"
			}
		}
	}
	line := " " + r.label
	pad := w - len(line) - len(value) - 1
	if pad < 1 {
		pad = 1
	}
	return line + strings.Repeat(" ", pad) + value
}

// settingOrigins names the scope each effective setting came from, for the
// pane's right-hand annotation. It re-checks validity because a scope can hold
// a row the resolver ignored: a bad value in the workspace must report the user
// or default layer that actually won, not the layer that named the broken key.
func (a *App) settingOrigins() map[string]string {
	user, workspace := settingScopes(a.state)
	out := make(map[string]string, 5)
	for _, key := range []string{settingTabWidth, settingTabs, settingWrap, settingAutoPairs, settingInlayHints} {
		switch {
		case settingValueValid(key, workspace[key]):
			out[key] = store.ScopeWorkspace
		case settingValueValid(key, user[key]):
			out[key] = store.ScopeUser
		default:
			out[key] = "default"
		}
	}
	return out
}
