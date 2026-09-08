package termconf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"raj/internal/keys"
)

// isolate points HOME and XDG_CONFIG_HOME at a temp tree and clears the
// overrides, so a test never reads or writes the developer's own config.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(EnvGhosttyConf, "")
	t.Setenv(EnvITerm2Profile, "")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]Target{
		"ghostty": Ghostty, "macos": Ghostty,
		"ghostty-linux": GhosttyLinux, "linux": GhosttyLinux,
		"iterm2": ITerm2,
	} {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("ParseTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseTarget("kitty"); err == nil {
		t.Error("ParseTarget(kitty) should fail")
	}
}

// The Ghostty file goes beside the config Ghostty actually reads, which means
// finding that config rather than assuming a location.
func TestGhosttyPathFollowsTheConfig(t *testing.T) {
	home := isolate(t)
	xdg := filepath.Join(home, ".config", "ghostty")

	if _, err := Path(Ghostty); err == nil {
		t.Error("with no config anywhere, Path should fail rather than guess")
	}

	writeFile(t, filepath.Join(xdg, "config"), "theme = verdant-phosphor\n")
	got, err := Path(Ghostty)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, GhosttyInclude); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Two configs is ambiguous and Ghostty reads both, so raj refuses instead of
// picking one and being included from the other.
func TestGhosttyPathRefusesTwoConfigs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS has a second location")
	}
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".config", "ghostty", "config"), "")
	writeFile(t, filepath.Join(home, "Library", "Application Support",
		"com.mitchellh.ghostty", "config"), "")
	if _, err := Path(Ghostty); err == nil {
		t.Error("two configs should be an error")
	}
}

func TestEnvOverridesWin(t *testing.T) {
	home := isolate(t)
	conf := filepath.Join(home, "elsewhere", "raj.conf")
	t.Setenv(EnvGhosttyConf, conf)
	if got, err := Path(Ghostty); err != nil || got != conf {
		t.Errorf("got %q, %v; want %q", got, err, conf)
	}

	prof := filepath.Join(home, "custom", "raj.json")
	t.Setenv(EnvITerm2Profile, prof)
	if got, err := Path(ITerm2); err != nil || got != prof {
		t.Errorf("got %q, %v; want %q", got, err, prof)
	}
}

// The DynamicProfiles directory is created, but only under an iTerm2 support
// directory that already exists — a profile written under a home with no
// iTerm2 in it is a file nothing will ever read.
func TestITerm2PathNeedsITerm2(t *testing.T) {
	home := isolate(t)
	if _, err := Path(ITerm2); err == nil {
		t.Error("with no iTerm2 support directory, Path should fail")
	}
	support := filepath.Join(home, "Library", "Application Support", "iTerm2")
	if err := os.MkdirAll(support, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Path(ITerm2)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(support, "DynamicProfiles", "raj.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Install is the whole point: it must write what --config prints, and it must
// not touch the user's own config while doing it.
func TestInstallWritesAndLeavesTheConfigAlone(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "ghostty")
	const userConf = "theme = verdant-phosphor\nconfig-file = ~/.config/ghostty/raj.conf\n"
	writeFile(t, filepath.Join(dir, "config"), userConf)

	path, err := Install(Ghostty)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != Render(Ghostty) {
		t.Error("installed file differs from what --config prints")
	}
	after, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != userConf {
		t.Errorf("the user's config was modified:\n%s", after)
	}
	// Nothing left behind: an install that litters temp files in a config
	// directory is one the user has to clean up.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want just config and %s", names, GhosttyInclude)
	}
}

func TestInstallOverwrites(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "ghostty")
	writeFile(t, filepath.Join(dir, "config"), "")
	writeFile(t, filepath.Join(dir, GhosttyInclude), "stale content\n")
	if _, err := Install(Ghostty); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, GhosttyInclude))
	if strings.Contains(string(got), "stale content") {
		t.Error("the old file survived the install")
	}
}

// The startup check has to be silent about everything except a config that is
// genuinely out of date, or it becomes noise a user learns to ignore.
func TestCheckStates(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "ghostty")
	conf := filepath.Join(dir, GhosttyInclude)
	writeFile(t, filepath.Join(dir, "config"), "")

	if state, _ := Check(Ghostty); state != Absent {
		t.Errorf("no file: got %v, want Absent", state)
	}
	writeFile(t, conf, "keybind = a=b\n")
	if state, _ := Check(Ghostty); state != Unstamped {
		t.Errorf("hand-written: got %v, want Unstamped", state)
	}
	writeFile(t, conf, "# "+keys.HashMarker+"0000deadbeef\n")
	if state, _ := Check(Ghostty); state != Stale {
		t.Errorf("old stamp: got %v, want Stale", state)
	}
	if _, err := Install(Ghostty); err != nil {
		t.Fatal(err)
	}
	if state, _ := Check(Ghostty); state != Current {
		t.Errorf("just installed: got %v, want Current", state)
	}
}

// An unresolvable path is not a problem to report: not having iTerm2 is not a
// stale config.
func TestCheckIsAbsentWithoutAPath(t *testing.T) {
	isolate(t)
	if state, _ := Check(ITerm2); state != Absent {
		t.Errorf("got %v, want Absent", state)
	}
	if got := StaleTargets(); len(got) != 0 {
		t.Errorf("StaleTargets on a bare home returned %v", got)
	}
}

func TestIncludedBy(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "ghostty")
	conf := filepath.Join(dir, GhosttyInclude)

	for _, tc := range []struct {
		name, config string
		want         bool
	}{
		{"absolute", "config-file = " + conf + "\n", true},
		{"tilde", "theme = x\nconfig-file = ~/.config/ghostty/raj.conf\n", true},
		{"spaced", "config-file=raj.conf\n", true},
		{"missing", "theme = verdant-phosphor\n", false},
		{"commented out", "# config-file = raj.conf\n", false},
		{"another file", "config-file = other.conf\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, filepath.Join(dir, "config"), tc.config)
			got, err := IncludedBy(conf)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("IncludedBy(%q) = %v, want %v", tc.config, got, tc.want)
			}
		})
	}
}
