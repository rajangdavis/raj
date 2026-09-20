package app

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"raj/internal/editor"
	"raj/internal/store"
)

// Settings keys. The resolver knows exactly these; a key it does not know is
// left alone, so a newer raj can add a setting to the store without an older
// one misreading it.
const (
	settingTabWidth   = "tab_width"
	settingTabs       = "tabs"
	settingWrap       = "wrap"
	settingAutoPairs  = "auto_pairs"
	settingInlayHints = "inlay_hints"
)

// Options is the launch configuration main hands to NewWithOptions. Each *Set
// field reports that the user typed the corresponding flag: an explicit flag
// beats a stored setting, while an omitted flag leaves the setting in charge.
// main learns which flags were set from flag.Visit; the zero value applies no
// overrides.
type Options struct {
	TabWidth    int
	TabWidthSet bool
	Tabs        bool
	TabsSet     bool
	Wrap        bool
	WrapSet     bool
	NoRestore   bool

	// Phone selects the phone profile of the normal editor: taller,
	// horizontally scrollable tab chips and a review action bar instead of a
	// status strip. It is a profile switch, not a stored setting, so it has no
	// *Set partner and never enters the flag.Visit override logic.
	Phone bool

	// CtrlAliases adds a ctrl+<key> alias for every super+<key> binding, so a
	// terminal that cannot send super can still reach the action. It is a
	// profile switch, not a stored setting. CtrlAliasesSet reports that the user
	// passed --ctrl-aliases explicitly, which turns off the --phone implication;
	// NewWithOptions resolves the pair, so a caller can pass both raw.
	CtrlAliases    bool
	CtrlAliasesSet bool

	// Attach makes the app a client of a running editor instead of a local
	// one: it opens no file from disk and restores no local session, review is
	// the default mode, and every document comes from the daemon's
	// snapshot/watch. AttachAddr is the daemon address to dial; empty means
	// discovery, the same rule control.Locate applies.
	Attach     bool
	AttachAddr string

	// Name, when set, is the client view key: it names the saved tab view this
	// attach keeps, so a laptop client and a phone keep separate views of the
	// same workspace. Empty falls back to the profile (phone, else attach).
	Name string
}

// clientViewKey names the saved client view an attach keeps. An explicit name
// wins, so two clients can choose their own; without one the profile splits a
// phone from the default attach client.
func clientViewKey(o Options, phoneOn bool) string {
	if o.Name != "" {
		return o.Name
	}
	if phoneOn {
		return "phone"
	}
	return "attach"
}

// ProfileFlags resolves the profile implication: --phone turns on the ctrl
// aliases unless --ctrl-aliases was passed explicitly. The *Set argument is
// that explicitness, from flag.Visit. It is pure so the flag matrix is testable
// without parsing flags.
func ProfileFlags(phone, ctrlAliases, ctrlAliasesSet bool) (phoneOn, aliasesOn bool) {
	phoneOn = phone
	if ctrlAliasesSet {
		aliasesOn = ctrlAliases
	} else {
		aliasesOn = phone
	}
	return phoneOn, aliasesOn
}

// ResolvedSettings is the effective configuration after the scopes are layered.
// It is a value rather than a map, so a caller cannot mutate what the resolver
// decided.
type ResolvedSettings struct {
	TabWidth   int
	Tabs       bool
	Wrap       bool
	AutoPairs  bool
	InlayHints bool
}

// defaultSettings is the built-in baseline. tabWidth is the caller's own
// default (the --tab default, or the argument New was given); a non-positive
// value falls back to 2, the width raj has always used.
func defaultSettings(tabWidth int) ResolvedSettings {
	if tabWidth <= 0 {
		tabWidth = 2
	}
	return ResolvedSettings{
		TabWidth:   tabWidth,
		Wrap:       true,
		AutoPairs:  true,
		InlayHints: true,
	}
}

// resolveSettings layers the two scopes over def: a user value beats the
// default, a workspace value beats a user value. It is pure — no store, no App
// — so it is unit-testable without a database.
//
// A value that will not parse is ignored rather than zeroed: a bad tab_width
// must not collapse the width to zero, and a bad wrap must not read as false.
// The ignored keys are returned as "scope/key" in a stable order so the status
// line can mention them once, exactly as .raj/hidden reports a bad pattern.
func resolveSettings(def ResolvedSettings, user, workspace map[string]string) (ResolvedSettings, []string) {
	out := def
	var bad []string
	overlay := func(scope string, values map[string]string) {
		for key, raw := range values {
			switch key {
			case settingTabWidth:
				n, ok := parseIntSetting(raw)
				if !ok {
					bad = append(bad, scope+"/"+key)
					continue
				}
				out.TabWidth = n
			case settingTabs:
				b, ok := parseBoolSetting(raw)
				if !ok {
					bad = append(bad, scope+"/"+key)
					continue
				}
				out.Tabs = b
			case settingWrap:
				b, ok := parseBoolSetting(raw)
				if !ok {
					bad = append(bad, scope+"/"+key)
					continue
				}
				out.Wrap = b
			case settingAutoPairs:
				b, ok := parseBoolSetting(raw)
				if !ok {
					bad = append(bad, scope+"/"+key)
					continue
				}
				out.AutoPairs = b
			case settingInlayHints:
				b, ok := parseBoolSetting(raw)
				if !ok {
					bad = append(bad, scope+"/"+key)
					continue
				}
				out.InlayHints = b
			}
		}
	}
	overlay(store.ScopeUser, user)
	overlay(store.ScopeWorkspace, workspace)
	sort.Strings(bad)
	return out, bad
}

// parseIntSetting reads a positive tab width. Zero and negatives are refused:
// a zero width would make every indent a no-op.
func parseIntSetting(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parseBoolSetting accepts the spellings a person or a config file is likely to
// write, on top of strconv.ParseBool. Anything else is refused so the caller
// can report the key rather than guess.
func parseBoolSetting(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "yes":
		return true, true
	case "off", "no":
		return false, true
	}
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, false
	}
	return b, true
}

// LSP override keys. A language's server is chosen by `lsp.<lang>.command`, an
// executable name or path, and `lsp.<lang>.args`, its arguments. <lang> is the
// lsp.LanguageID value — "go", "python", "typescriptreact". This is the one
// settings family whose key is not fixed, so knownSetting and
// settingValueValid parse it instead of listing it, and the typed
// ResolvedSettings cannot carry it: App.lspCommand resolves these keys from
// the scopes directly.
//
// Args are split on whitespace and there is no quoting, so an argument that
// must contain a space is not expressible. A command that is empty disables
// the language's server; a command that is unset leaves the built-in command
// map in charge, with an explicit args list replacing the built-in arguments.
const (
	lspSettingPrefix = "lsp."
	lspCommandField  = "command"
	lspArgsField     = "args"
)

// parseLPSSettingKey splits an `lsp.<lang>.<field>` key, reporting field as
// "command" or "args". An empty language, an unknown field, or a key outside
// the family is not an LSP setting.
func parseLPSSettingKey(key string) (lang, field string, ok bool) {
	if !strings.HasPrefix(key, lspSettingPrefix) {
		return "", "", false
	}
	rest := key[len(lspSettingPrefix):]
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return "", "", false
	}
	lang, field = rest[:i], rest[i+1:]
	if field != lspCommandField && field != lspArgsField {
		return "", "", false
	}
	return lang, field, true
}

// knownSetting reports whether key is one the resolver understands. SetSetting
// refuses anything else so an unknown key cannot be written into the store by
// the settings menu.
func knownSetting(key string) bool {
	switch key {
	case settingTabWidth, settingTabs, settingWrap, settingAutoPairs, settingInlayHints:
		return true
	}
	_, _, ok := parseLPSSettingKey(key)
	return ok
}

// settingValueValid reports whether raw is a value the resolver will accept for
// key. SetSetting uses it to leave a bad write inert: the string is persisted
// for audit, but no live field is touched, because the resolved value of a bad
// key is the lower scope or the default rather than a decision — and applying
// that would silently pin or flip a setting the person did not name correctly.
func settingValueValid(key, raw string) bool {
	switch key {
	case settingTabWidth:
		_, ok := parseIntSetting(raw)
		return ok
	case settingTabs, settingWrap, settingAutoPairs, settingInlayHints:
		_, ok := parseBoolSetting(raw)
		return ok
	}
	// An LSP override has nothing to parse yet: an empty command disables the
	// language and an empty args list is no arguments, so every string is a
	// valid value for a well-formed key.
	_, _, ok := parseLPSSettingKey(key)
	return ok
}

// settingScopes reads both settings scopes, degrading to nil on a nil store or
// a read error: a settings failure never stops the editor, it only loses the
// layer that failed.
func settingScopes(state *store.Store) (user, workspace map[string]string) {
	if state == nil {
		return nil, nil
	}
	user, _ = state.Settings(store.ScopeUser)
	workspace, _ = state.Settings(store.ScopeWorkspace)
	return user, workspace
}

// tabWidthExplicit reports whether the launch configuration names a tab width
// the person chose — a positive --tab flag or a stored tab_width — as opposed
// to the built-in default. An explicit width is pinned onto buffers as they
// open; a default one is left to the file's own detection. A non-positive flag
// is not a width and is treated as not set, so it cannot pin a bad value.
func tabWidthExplicit(o Options, user, workspace map[string]string) bool {
	if o.TabWidthSet && o.TabWidth > 0 {
		return true
	}
	if _, ok := parseIntSetting(user[settingTabWidth]); ok {
		return true
	}
	_, ok := parseIntSetting(workspace[settingTabWidth])
	return ok
}

// values renders the resolved settings as the map Settings returns. The keys
// are the store's own, so a caller can round-trip them straight back to
// SetSetting.
func (r ResolvedSettings) values() map[string]string {
	return map[string]string{
		settingTabWidth:   strconv.Itoa(r.TabWidth),
		settingTabs:       strconv.FormatBool(r.Tabs),
		settingWrap:       strconv.FormatBool(r.Wrap),
		settingAutoPairs:  strconv.FormatBool(r.AutoPairs),
		settingInlayHints: strconv.FormatBool(r.InlayHints),
	}
}

// Settings returns the resolved effective values — defaults, overlaid by the
// user scope, overlaid by the workspace scope, then any explicit launch flag.
// It is the read half of the settings menu seam.
func (a *App) Settings() map[string]string {
	return a.settings.values()
}

// SetSetting writes key/value into scope and applies the result to the running
// editor. It is the write half of the settings menu seam; the menu itself is
// not built here.
//
// A nil store is an error rather than a silent success: there is nowhere to
// persist the value, and the caller has to say so.
//
// What is applied is the *resolved* value, not the raw one. After the write the
// two scopes are re-read and layered (defaults < user < workspace) and the
// written key takes that resolved value, so a write to a lower scope while a
// higher scope still holds the key leaves the effective value unchanged, and
// the running app agrees with what a restart would resolve for a valid write.
// A value that will not parse is stored but changes nothing live, the same rule
// resolveSettings follows at startup.
//
// A runtime write is a deliberate user action, so it takes effect for this
// session even when a launch flag named the same key; the flag wins again on
// the next launch, where it is layered over the scopes at startup.
func (a *App) SetSetting(scope, key, value string) error {
	if a.state == nil {
		return errors.New("settings: no workspace store to write to")
	}
	if !knownSetting(key) {
		return fmt.Errorf("settings: unknown key %q", key)
	}
	if err := a.state.SetSetting(scope, key, value); err != nil {
		return err
	}
	if !settingValueValid(key, value) {
		return nil
	}
	user, workspace := settingScopes(a.state)
	res, _ := resolveSettings(defaultSettings(a.tabWidth), user, workspace)
	// The lsp.<lang>.* keys are dynamic and have no field in the typed
	// settings, so a valid write just refreshes the command cache; every other
	// key folds into its live representation as before.
	if _, _, ok := parseLPSSettingKey(key); ok {
		a.refreshLSPOverrides()
		return nil
	}
	a.applySetting(key, res.values()[key])
	return nil
}

// applySetting folds a stored value into the running app. Every key here has a
// live representation on the App or its panes, so the change is visible without
// a restart.
//
// tab_width is live: the tab set width is retargeted so buffers opened from
// now on use the new width, and every open buffer is pinned to it — the display
// width a tab advances and the width one indent level inserts. The pin outranks
// the indentation a buffer detects for itself and survives Reload, because an
// explicit setting is a decision the content cannot overrule.
func (a *App) applySetting(key, value string) {
	switch key {
	case settingTabWidth:
		n, ok := parseIntSetting(value)
		if !ok {
			return
		}
		a.tabWidth = n
		a.settings.TabWidth = n
		a.Tabs.SetTabWidth(n)
		for _, p := range a.Tabs.All() {
			// Both halves: SetIndentDefault keeps the tabs/spaces style and
			// covers buffers still on the default, and SetTabWidth pins the width
			// over a file that detected its own.
			p.File.SetIndentDefault(editor.Indent{Tabs: a.Tabs.IndentTabs, Width: n})
			p.File.SetTabWidth(n)
		}
	case settingTabs:
		b, ok := parseBoolSetting(value)
		if !ok {
			return
		}
		a.Tabs.IndentTabs = b
		a.settings.Tabs = b
		a.reindent()
	case settingWrap:
		b, ok := parseBoolSetting(value)
		if !ok {
			return
		}
		a.WrapDefault = b
		a.settings.Wrap = b
		for _, p := range a.Tabs.All() {
			p.Wrap = b
		}
	case settingAutoPairs:
		b, ok := parseBoolSetting(value)
		if !ok {
			return
		}
		a.AutoPairs = b
		a.settings.AutoPairs = b
		for _, p := range a.Tabs.All() {
			p.AutoPairs = b
		}
	case settingInlayHints:
		b, ok := parseBoolSetting(value)
		if !ok {
			return
		}
		a.InlayHints = b
		a.settings.InlayHints = b
		for _, p := range a.Tabs.All() {
			p.Hints = b
		}
	}
}

// reindent re-applies the application's indent default to every open buffer.
// File.SetIndentDefault yields to a buffer that detected its own indentation,
// so this moves only the buffers that were still on the default — precisely the
// set a changed tabs setting should reach. tab_width goes through SetTabWidth
// instead, which pins the width over detection.
func (a *App) reindent() {
	for _, p := range a.Tabs.All() {
		p.File.SetIndentDefault(editor.Indent{Tabs: a.Tabs.IndentTabs, Width: a.tabWidth})
	}
}
