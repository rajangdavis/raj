package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"raj/internal/session"
	"raj/internal/store"
	"raj/internal/ui"
)

// newSettingsApp builds an app over a fresh temp root with the given options,
// without opening a file. The store opens inside NewWithOptions, exactly as it
// does in production.
func newSettingsApp(t *testing.T, opts Options) *App {
	t.Helper()
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, t.TempDir(), opts)
	t.Cleanup(a.CloseState)
	return a
}

// seedSettings writes rows into a root's store before an app opens it, the way
// a previous run or the settings menu would have.
func seedSettings(t *testing.T, root, scope string, rows map[string]string) {
	t.Helper()
	s, err := store.Open(filepath.Join(session.StateDir(root), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	for key, value := range rows {
		if err := s.SetSetting(scope, key, value); err != nil {
			t.Fatalf("SetSetting %s/%s: %v", scope, key, err)
		}
	}
}

func TestResolveSettingsDefaults(t *testing.T) {
	got, bad := resolveSettings(defaultSettings(2), nil, nil)
	want := ResolvedSettings{TabWidth: 2, Tabs: false, Wrap: true, AutoPairs: true, InlayHints: true}
	if got != want {
		t.Errorf("resolveSettings defaults = %+v, want %+v", got, want)
	}
	if len(bad) != 0 {
		t.Errorf("bad = %v, want none", bad)
	}
}

func TestResolveSettingsPrecedence(t *testing.T) {
	got, bad := resolveSettings(defaultSettings(2),
		map[string]string{"wrap": "false", "tab_width": "4"},
		map[string]string{"wrap": "true", "inlay_hints": "false"})
	if len(bad) != 0 {
		t.Fatalf("bad = %v, want none", bad)
	}
	if !got.Wrap {
		t.Errorf("wrap = false; a workspace value must beat the user value")
	}
	if got.TabWidth != 4 {
		t.Errorf("tab_width = %d, want the user value 4", got.TabWidth)
	}
	if got.InlayHints {
		t.Errorf("inlay_hints = true, want the workspace value false")
	}
	if !got.AutoPairs || got.Tabs {
		t.Errorf("untouched keys changed: %+v", got)
	}
}

func TestResolveSettingsBadValueIgnored(t *testing.T) {
	got, bad := resolveSettings(defaultSettings(2),
		map[string]string{"tab_width": "wide", "wrap": "maybe"},
		nil)
	if got.TabWidth != 2 {
		t.Errorf("tab_width = %d, want the default 2; a bad value must be ignored, not zeroed", got.TabWidth)
	}
	if !got.Wrap {
		t.Errorf("wrap = false, want the default true; a bad value must not flip a bool")
	}
	wantBad := []string{"user/tab_width", "user/wrap"}
	if !reflect.DeepEqual(bad, wantBad) {
		t.Errorf("bad = %v, want %v", bad, wantBad)
	}
}

func TestNewWithOptionsAppliesSettings(t *testing.T) {
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{
		"tab_width":   "4",
		"tabs":        "true",
		"wrap":        "false",
		"auto_pairs":  "false",
		"inlay_hints": "false",
	})
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)

	if a.tabWidth != 4 {
		t.Errorf("tabWidth = %d, want the setting 4", a.tabWidth)
	}
	if !a.Tabs.IndentTabs {
		t.Error("Tabs.IndentTabs = false, want the setting true")
	}
	if a.WrapDefault || a.AutoPairs || a.InlayHints {
		t.Errorf("defaults not overridden: wrap=%v auto_pairs=%v inlay_hints=%v",
			a.WrapDefault, a.AutoPairs, a.InlayHints)
	}
}

func TestNewWithOptionsExplicitFlagOverridesSettings(t *testing.T) {
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{
		"tab_width": "4",
		"tabs":      "true",
		"wrap":      "false",
	})
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{
		TabWidth: 8, TabWidthSet: true,
		Tabs: false, TabsSet: true,
		Wrap: true, WrapSet: true,
	})
	t.Cleanup(a.CloseState)

	if a.tabWidth != 8 {
		t.Errorf("tabWidth = %d, want the explicit 8", a.tabWidth)
	}
	if a.Tabs.IndentTabs {
		t.Error("Tabs.IndentTabs = true, want the explicit false")
	}
	if !a.WrapDefault {
		t.Error("WrapDefault = false, want the explicit true")
	}
}

func TestNewKeepsBuiltInDefaults(t *testing.T) {
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := New(host, t.TempDir(), 2)
	t.Cleanup(a.CloseState)

	if !a.WrapDefault || !a.AutoPairs || !a.InlayHints {
		t.Errorf("New defaults changed: wrap=%v auto_pairs=%v inlay_hints=%v",
			a.WrapDefault, a.AutoPairs, a.InlayHints)
	}
	if a.tabWidth != 2 || a.Tabs.IndentTabs {
		t.Errorf("New tab defaults changed: width=%d tabs=%v", a.tabWidth, a.Tabs.IndentTabs)
	}
	if a.Tabs.TabWidthPinned() {
		t.Error("New pinned a tab width; a default launch must leave detection the winner")
	}
}

func TestSettingsReturnsResolvedMap(t *testing.T) {
	root := t.TempDir()
	seedSettings(t, root, store.ScopeUser, map[string]string{"auto_pairs": "off"})
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)

	got := a.Settings()
	want := map[string]string{
		"tab_width":   "2",
		"tabs":        "false",
		"wrap":        "true",
		"auto_pairs":  "false",
		"inlay_hints": "true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Settings() = %v, want %v", got, want)
	}
}

func TestSetSettingWritesAndApplies(t *testing.T) {
	a := newSettingsApp(t, Options{})
	if err := a.SetSetting(store.ScopeWorkspace, "wrap", "false"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if a.WrapDefault {
		t.Error("SetSetting(wrap) did not turn wrapping off in the running app")
	}
	rows, err := a.state.Settings(store.ScopeWorkspace)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rows["wrap"] != "false" {
		t.Errorf("stored wrap = %q, want false", rows["wrap"])
	}
	if got := a.Settings()["wrap"]; got != "false" {
		t.Errorf("Settings()[wrap] = %q, want false", got)
	}
}

func TestSetSettingRefusedWithoutStore(t *testing.T) {
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	// root == "" means no workspace, so no store; the write must be refused
	// rather than silently lost.
	a := New(host, "", 2)
	if err := a.SetSetting(store.ScopeWorkspace, "wrap", "false"); err == nil {
		t.Fatal("SetSetting with no store returned nil, want an error")
	}
}

func TestControlCloseRemembersPosition(t *testing.T) {
	h := controlHarness(t, "line one\nline two\nline three\n")
	path := h.Tabs.Active().File.Path
	h.Tabs.Active().Cursors.Set(5, 5)

	if err := hostOf(h.App).Close(path); err != nil {
		t.Fatalf("close = %v", err)
	}
	cursor, _, ok, err := h.state.Position(path)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if !ok {
		t.Fatal("no position recorded by a socket close; want the cursor remembered")
	}
	if cursor != 5 {
		t.Errorf("remembered cursor = %d, want 5", cursor)
	}
}

// tab_width is live: setting it resizes an already-open buffer, a buffer opened
// afterwards, and a new unnamed buffer, for both the display width of a tab and
// the width one indent unit inserts. The files detect two-space indentation, so
// the assertions prove the explicit setting outranks detection.
func TestSetSettingTabWidthIsLive(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	if err := os.WriteFile(first, []byte("  alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("  beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newHarnessAt(t, root)
	a.OpenFile(first)
	open := a.Tabs.Active()
	if open.File.Indent.Width != 2 || open.File.Cols.Tab != 2 {
		t.Fatalf("setup: indent %d cols %d, want the detected/default 2/2",
			open.File.Indent.Width, open.File.Cols.Tab)
	}

	if err := a.SetSetting(store.ScopeWorkspace, "tab_width", "8"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	if open.File.Cols.Tab != 8 || open.File.Indent.Width != 8 {
		t.Errorf("open buffer = indent %d cols %d, want 8/8", open.File.Indent.Width, open.File.Cols.Tab)
	}
	if got := open.File.Cols.ColOf("\tx", 1); got != 8 {
		t.Errorf("display column after a tab = %d, want 8", got)
	}
	if got := len(open.File.Indent.Unit()); got != 8 {
		t.Errorf("indent unit = %d spaces, want 8", got)
	}

	// A buffer opened afterwards uses the explicit width even though its own
	// content detects two spaces.
	a.OpenFile(second)
	opened := a.Tabs.Active()
	if opened == open {
		t.Fatal("second open reused the first pane")
	}
	if opened.File.Cols.Tab != 8 || opened.File.Indent.Width != 8 {
		t.Errorf("new open = indent %d cols %d, want 8/8", opened.File.Indent.Width, opened.File.Cols.Tab)
	}

	// And an unnamed buffer created afterwards.
	a.newFile()
	created := a.Tabs.Active()
	if created.File.Cols.Tab != 8 || created.File.Indent.Width != 8 {
		t.Errorf("new unnamed buffer = indent %d cols %d, want 8/8", created.File.Indent.Width, created.File.Cols.Tab)
	}
}

// A stored tab_width is an explicit width, so a file that detects two spaces
// must still open at the stored width: like a --tab flag, it pins detection
// rather than sitting under it. Without tabWidthExplicit and the pin, the
// file's own two spaces win the indent width and only the display width
// follows the setting.
func TestStoredTabWidthPinsDetection(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("  alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{"tab_width": "8"})

	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)

	if !a.Tabs.TabWidthPinned() {
		t.Fatal("a stored tab_width did not pin; the setting is only a fallback")
	}
	a.OpenFile(f)
	p := a.Tabs.Active()
	if p == nil {
		t.Fatal("nothing opened")
	}
	if p.File.Indent.Width != 8 || p.File.Cols.Tab != 8 {
		t.Errorf("stored-width open = indent %d cols %d, want 8/8",
			p.File.Indent.Width, p.File.Cols.Tab)
	}
}

// A write to a lower scope must not beat a value a higher scope still holds in
// the running app: what is applied is the resolved settings (defaults < user <
// workspace), so the live value stays the workspace's. The row is still
// written, so removing the workspace row later would let it surface.
func TestSetSettingLowerScopeDoesNotBeatHigherScope(t *testing.T) {
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{"wrap": "false"})
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)

	if a.WrapDefault {
		t.Fatal("setup: workspace wrap=false did not take effect")
	}
	if err := a.SetSetting(store.ScopeUser, "wrap", "true"); err != nil {
		t.Fatalf("SetSetting(user/wrap): %v", err)
	}
	if a.WrapDefault {
		t.Error("a user write beat the workspace value in the running app")
	}
	if got := a.Settings()["wrap"]; got != "false" {
		t.Errorf("Settings()[wrap] = %q, want the workspace value false", got)
	}
	rows, err := a.state.Settings(store.ScopeUser)
	if err != nil {
		t.Fatalf("read user scope: %v", err)
	}
	if rows["wrap"] != "true" {
		t.Errorf("user row = %q, want the write persisted", rows["wrap"])
	}
}

// A runtime menu write is a deliberate user action, so it takes effect for the
// running session even when a launch flag named the same key; the flag wins
// again on the next launch, where it is layered over the scopes.
func TestSetSettingBeatsLaunchFlagForTheSession(t *testing.T) {
	root := t.TempDir()
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{Wrap: false, WrapSet: true})
	t.Cleanup(a.CloseState)
	if a.WrapDefault {
		t.Fatal("setup: --wrap=false did not take effect")
	}

	if err := a.SetSetting(store.ScopeWorkspace, "wrap", "true"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !a.WrapDefault {
		t.Error("the menu write did not take effect for the running session")
	}
	if got := a.Settings()["wrap"]; got != "true" {
		t.Errorf("Settings()[wrap] = %q, want true", got)
	}

	// A fresh app over the same root with the same flag reads the flag again.
	next := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { next.Close() })
	b := NewWithOptions(next, root, Options{Wrap: false, WrapSet: true})
	t.Cleanup(b.CloseState)
	if b.WrapDefault {
		t.Error("the stored write beat the launch flag on the next launch")
	}
}

// A non-positive explicit --tab is not a width: the built-in fallback stands
// and no pin is set, rather than tabs.New(0) and a SetTabWidth whose guard
// refuses the bad value.
func TestExplicitNonPositiveTabWidthIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  int
	}{
		{"zero", 0},
		{"negative", -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := ui.NewFakeHost(80, 24)
			t.Cleanup(func() { host.Close() })
			a := NewWithOptions(host, t.TempDir(), Options{TabWidth: tc.tab, TabWidthSet: true})
			t.Cleanup(a.CloseState)

			if a.tabWidth != 2 {
				t.Errorf("tabWidth = %d, want the fallback 2", a.tabWidth)
			}
			if a.Tabs.TabWidthPinned() {
				t.Error("a non-positive --tab pinned the width")
			}
			if got := a.Settings()["tab_width"]; got != "2" {
				t.Errorf("Settings()[tab_width] = %q, want the fallback 2", got)
			}
		})
	}
}

// A value that will not parse is persisted for audit but leaves the live
// setting alone. The resolved value of a bad key is the lower scope or the
// default, and applying that would silently pin or flip what the person has;
// the pin is the observable here, since the fallback width itself is 2 either
// way.
func TestSetSettingBadValueIsStoredButInert(t *testing.T) {
	root := t.TempDir()
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)
	if a.tabWidth != 2 || a.Tabs.TabWidthPinned() {
		t.Fatalf("setup: default width %d pinned %v", a.tabWidth, a.Tabs.TabWidthPinned())
	}

	if err := a.SetSetting(store.ScopeWorkspace, "tab_width", "wide"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if a.tabWidth != 2 {
		t.Errorf("tabWidth = %d, want the default 2 unchanged by a bad value", a.tabWidth)
	}
	if a.Tabs.TabWidthPinned() {
		t.Error("a bad tab_width pinned the default width")
	}
	rows, err := a.state.Settings(store.ScopeWorkspace)
	if err != nil {
		t.Fatalf("read workspace scope: %v", err)
	}
	if rows["tab_width"] != "wide" {
		t.Errorf("workspace tab_width = %q, want the bad value persisted", rows["tab_width"])
	}
}

// lspOverride is the pure resolution behind the cached lspCommand. It layers
// the scopes like resolveSettings: workspace beats user, a command that is
// unset falls back to the built-in map, and a command that is set but empty
// disables the language.
func TestLSPOverrideResolution(t *testing.T) {
	cases := []struct {
		name            string
		user, workspace map[string]string
		id              string
		want            []string
		wantPresent     bool
	}{
		{
			name: "no override",
			id:   "go",
		},
		{
			name:      "workspace command and args replace the built-in",
			workspace: map[string]string{"lsp.python.command": "ty", "lsp.python.args": "--stdio --log"},
			id:        "python",
			want:      []string{"ty", "--stdio", "--log"}, wantPresent: true,
		},
		{
			name:      "workspace beats user",
			user:      map[string]string{"lsp.python.command": "pylsp-user"},
			workspace: map[string]string{"lsp.python.command": "ty"},
			id:        "python",
			want:      []string{"ty"}, wantPresent: true,
		},
		{
			name: "user beats the built-in",
			user: map[string]string{"lsp.python.command": "tpy"},
			id:   "python",
			want: []string{"tpy"}, wantPresent: true,
		},
		{
			name:        "an empty command disables",
			workspace:   map[string]string{"lsp.python.command": ""},
			id:          "python",
			wantPresent: true,
		},
		{
			name:      "args alone keep the built-in command and replace its args",
			workspace: map[string]string{"lsp.typescript.args": "--stdio --log"},
			id:        "typescript",
			want:      []string{"typescript-language-server", "--stdio", "--log"}, wantPresent: true,
		},
		{
			name:      "an unlisted language becomes reachable",
			workspace: map[string]string{"lsp.java.command": "jdtls"},
			id:        "java",
			want:      []string{"jdtls"}, wantPresent: true,
		},
		{
			name:      "args with no command and no built-in are no server",
			workspace: map[string]string{"lsp.java.args": "--stdio"},
			id:        "java",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, present := lspOverride(tc.id, tc.user, tc.workspace)
			if present != tc.wantPresent {
				t.Fatalf("present = %v, want %v", present, tc.wantPresent)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lspOverride(%s) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// The dynamic key family is what knownSetting and settingValueValid have to
// accept, and a key that only looks like one must still be refused.
func TestLSPSettingKeyParsing(t *testing.T) {
	for _, tc := range []struct{ key, lang, field string }{
		{"lsp.go.command", "go", "command"},
		{"lsp.python.args", "python", "args"},
		{"lsp.typescriptreact.command", "typescriptreact", "command"},
	} {
		lang, field, ok := parseLPSSettingKey(tc.key)
		if !ok || lang != tc.lang || field != tc.field {
			t.Errorf("parseLPSSettingKey(%q) = %q, %q, %v; want %q, %q, true",
				tc.key, lang, field, ok, tc.lang, tc.field)
			continue
		}
		if !knownSetting(tc.key) {
			t.Errorf("knownSetting(%q) = false, want true", tc.key)
		}
		if !settingValueValid(tc.key, "") {
			t.Errorf("settingValueValid(%q, empty) = false, want true", tc.key)
		}
	}
	for _, key := range []string{
		"lsp.go", "lsp.go.", "lsp..command", "lsp.go.flags",
		"lsp.go.command.extra", "lp.go.command", "lsp.go.commands",
	} {
		if _, _, ok := parseLPSSettingKey(key); ok {
			t.Errorf("parseLPSSettingKey(%q) parsed, want refused", key)
		}
		if knownSetting(key) {
			t.Errorf("knownSetting(%q) = true, want false", key)
		}
	}
}

// A stored override is resolved at construction, before the first request, and
// servers.for_ reads the cache rather than the store.
func TestNewWithOptionsAppliesLSPOverrides(t *testing.T) {
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{
		"lsp.python.command": "ty",
		"lsp.python.args":    "--stdio",
	})
	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, root, Options{})
	t.Cleanup(a.CloseState)

	argv, ok := a.lspCommand("python")
	if !ok || !reflect.DeepEqual(argv, []string{"ty", "--stdio"}) {
		t.Errorf("lspCommand(python) = %q, %v; want ty --stdio", argv, ok)
	}
	if argv, ok := a.lspCommand("go"); !ok || argv[0] != "gopls" {
		t.Errorf("lspCommand(go) = %q, %v; want the built-in gopls", argv, ok)
	}
}

// SetSetting is the write half: a runtime write refreshes the cache, so the
// next for_ sees the new command, and an empty one disables the language with
// no spawn attempt.
func TestSetSettingRefreshesLSPOverrides(t *testing.T) {
	a := newSettingsApp(t, Options{})
	t.Setenv("PATH", "") // a start is observable as a missing binary, not a spawn

	if argv, ok := a.lspCommand("python"); !ok || argv[0] != "pylsp" {
		t.Fatalf("setup: lspCommand(python) = %q, %v; want the built-in pylsp", argv, ok)
	}
	if err := a.SetSetting(store.ScopeWorkspace, "lsp.python.command", "ty"); err != nil {
		t.Fatalf("SetSetting(command): %v", err)
	}
	if argv, ok := a.lspCommand("python"); !ok || !reflect.DeepEqual(argv, []string{"ty"}) {
		t.Errorf("after write lspCommand(python) = %q, %v; want ty", argv, ok)
	}
	if _, st := a.servers.for_("/w/a.py", nil); st != serverMissing {
		t.Errorf("state = %d, want serverMissing for the override", st)
	}

	if err := a.SetSetting(store.ScopeWorkspace, "lsp.python.command", ""); err != nil {
		t.Fatalf("SetSetting(empty command): %v", err)
	}
	if argv, ok := a.lspCommand("python"); ok || argv != nil {
		t.Errorf("after empty write lspCommand(python) = %q, %v; want disabled", argv, ok)
	}
	before := len(a.servers.byID)
	if _, st := a.servers.for_("/w/a.py", nil); st != serverNone {
		t.Errorf("state = %d, want serverNone for a disabled server", st)
	}
	if got := len(a.servers.byID); got != before {
		t.Errorf("a disabled server registered %d new entry(ies), want none", got-before)
	}
}

// A workspace write beats a user write in the live cache, the same precedence
// the typed settings use.
func TestSetSettingLSPWorkspaceBeatsUser(t *testing.T) {
	a := newSettingsApp(t, Options{})
	if err := a.SetSetting(store.ScopeUser, "lsp.python.command", "tpy"); err != nil {
		t.Fatalf("SetSetting(user): %v", err)
	}
	if argv, ok := a.lspCommand("python"); !ok || argv[0] != "tpy" {
		t.Fatalf("user override = %q, %v; want tpy", argv, ok)
	}
	if err := a.SetSetting(store.ScopeWorkspace, "lsp.python.command", "ty"); err != nil {
		t.Fatalf("SetSetting(workspace): %v", err)
	}
	if argv, ok := a.lspCommand("python"); !ok || argv[0] != "ty" {
		t.Errorf("workspace override = %q, %v; want it to beat the user value", argv, ok)
	}
}

// SetSetting accepts a well-formed lsp.<lang>.<field> key and refuses one that
// only resembles the family, so the new key space reaches the store without
// opening it to typos.
func TestSetSettingLSPKeySeam(t *testing.T) {
	a := newSettingsApp(t, Options{})
	if err := a.SetSetting(store.ScopeWorkspace, "lsp.go.args", "--remote=auto"); err != nil {
		t.Fatalf("SetSetting(lsp.go.args) = %v, want accepted", err)
	}
	if err := a.SetSetting(store.ScopeWorkspace, "lsp.go", "x"); err == nil {
		t.Error("SetSetting(lsp.go) = nil, want an unknown-key error")
	}
}
