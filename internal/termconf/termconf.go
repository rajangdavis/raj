// Package termconf resolves where a generated terminal config belongs and
// writes it there.
//
// The two targets are not symmetric, and that asymmetry is the whole design.
//
// The iTerm2 dynamic profile is an artifact raj owns end to end: it lands in a
// drop-in directory, the emitted JSON is the entire file, and iTerm2 reloads it
// without a restart. Overwriting it destroys nothing.
//
// A Ghostty config is not that. It is one user-owned file holding font, theme,
// shaders and everything else, and raj emits only keybind lines. So raj writes
// its own file next to that config and the user includes it once with
//
//	config-file = ~/.config/ghostty/raj.conf
//
// after which raj can rewrite its file forever and never touch a setting it did
// not write. Merging into a file raj did not generate is the alternative, and
// it means parsing a config format to preserve it — a much larger promise than
// this is worth.
package termconf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"raj/internal/keys"
)

// Target is a terminal raj can generate a config for.
type Target string

const (
	Ghostty      Target = "ghostty"
	GhosttyLinux Target = "ghostty-linux"
	ITerm2       Target = "iterm2"
)

// Env vars naming an explicit destination. They exist because both defaults can
// be wrong on a real machine: iTerm2 can be told to load its settings from a
// custom folder, which moves DynamicProfiles/ somewhere raj cannot guess, and a
// Ghostty config can live anywhere a user puts it.
const (
	EnvGhosttyConf   = "RAJ_GHOSTTY_CONF"
	EnvITerm2Profile = "RAJ_ITERM2_PROFILE_PATH"
)

// GhosttyInclude is the file raj writes beside the user's Ghostty config, and
// the name they include.
const GhosttyInclude = "raj.conf"

// ParseTarget accepts the spellings the --config flag has always taken.
func ParseTarget(s string) (Target, error) {
	switch s {
	case "ghostty", "macos":
		return Ghostty, nil
	case "ghostty-linux", "linux":
		return GhosttyLinux, nil
	case "iterm2":
		return ITerm2, nil
	}
	return "", fmt.Errorf("unknown target %q: want ghostty, ghostty-linux, or iterm2", s)
}

// Render is the generated text for a target. Every emitter reads the same
// measured table, so they cannot disagree about what a chord should send.
func Render(t Target) string {
	switch t {
	case Ghostty:
		return keys.GhosttyConfig("macos")
	case GhosttyLinux:
		return keys.GhosttyConfig("linux")
	case ITerm2:
		return keys.ITerm2Profile("raj")
	}
	return ""
}

// ErrNoDir means raj could not find the directory the file belongs in. It is
// deliberately not "so create one": a keybinding file written next to a config
// the terminal is not reading is exactly the invisible failure this whole
// change exists to end.
var ErrNoDir = errors.New("no configuration directory found")

// Path resolves where the generated file for a target belongs.
func Path(t Target) (string, error) {
	switch t {
	case Ghostty, GhosttyLinux:
		return ghosttyPath()
	case ITerm2:
		return iterm2Path()
	}
	return "", fmt.Errorf("unknown target %q", t)
}

// ghosttyPath puts raj.conf beside the config Ghostty actually reads.
//
// On macOS there are two valid homes for that config and raj must not guess
// between them, so it looks for one that exists. Finding both is an error
// rather than a preference: Ghostty reads both, so writing into one and being
// included from the other is a state raj cannot distinguish from working.
func ghosttyPath() (string, error) {
	if p := os.Getenv(EnvGhosttyConf); p != "" {
		return p, nil
	}
	var found []string
	for _, dir := range ghosttyDirs() {
		if st, err := os.Stat(filepath.Join(dir, "config")); err == nil && !st.IsDir() {
			found = append(found, dir)
		}
	}
	switch len(found) {
	case 1:
		return filepath.Join(found[0], GhosttyInclude), nil
	case 0:
		return "", fmt.Errorf("%w: looked for a Ghostty config in %s. Set %s to name one",
			ErrNoDir, strings.Join(ghosttyDirs(), " and "), EnvGhosttyConf)
	default:
		return "", fmt.Errorf("found a Ghostty config in both %s. Ghostty reads both, so raj will not choose: set %s",
			strings.Join(found, " and "), EnvGhosttyConf)
	}
}

// ghosttyDirs lists the directories that can hold a Ghostty config, most
// conventional first.
func ghosttyDirs() []string {
	var dirs []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "ghostty"))
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "ghostty"))
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home,
				"Library", "Application Support", "com.mitchellh.ghostty"))
		}
	}
	return dirs
}

// iterm2Path is the DynamicProfiles drop-in. The directory is created if the
// iTerm2 support directory above it exists — that much is iTerm2's documented
// autoload location — but not otherwise, since a DynamicProfiles directory
// invented under a home with no iTerm2 in it is a file nothing will ever read.
func iterm2Path() (string, error) {
	if p := os.Getenv(EnvITerm2Profile); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	support := filepath.Join(home, "Library", "Application Support", "iTerm2")
	if st, err := os.Stat(support); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%w: %s does not exist. Set %s to name the DynamicProfiles directory",
			ErrNoDir, support, EnvITerm2Profile)
	}
	return filepath.Join(support, "DynamicProfiles", "raj.json"), nil
}

// Install writes the generated config for a target and returns where it went.
func Install(t Target) (string, error) {
	path, err := Path(t)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, write(path, Render(t))
}

// write replaces a file atomically, so an interrupted install leaves the old
// config in place rather than half of a new one. A terminal reading a truncated
// keybinding file is a terminal with some of raj's chords and not others, which
// is harder to diagnose than not having installed it at all.
func write(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".raj-config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// State is what raj knows about an installed config.
type State int

const (
	// Absent: no file at the resolved path, or no resolvable path at all.
	// Never a warning — a user with one terminal should hear nothing about
	// the other.
	Absent State = iota
	// Unstamped: a file exists but carries no hash. Hand-written, or
	// generated before stamping existed. Also never a warning, because raj
	// cannot tell those apart and nagging about a config someone wrote
	// themselves is worse than missing a stale one.
	Unstamped
	// Stale: stamped, and stamped with a different table than this binary
	// emits. The one case worth saying out loud.
	Stale
	Current
)

// Check reports the state of the installed config for a target. An unresolvable
// path is Absent rather than an error: not having iTerm2 is not a problem to
// report.
func Check(t Target) (State, string) {
	path, err := Path(t)
	if err != nil {
		return Absent, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Absent, path
	}
	switch stamp := keys.InstalledHash(string(data)); {
	case stamp == "":
		return Unstamped, path
	case stamp != keys.Hash():
		return Stale, path
	}
	return Current, path
}

// StaleTargets names the installed configs that no longer match this binary's
// table. The caller decides how loudly to say so.
func StaleTargets() []string {
	var out []string
	for _, t := range []Target{HostTarget(), ITerm2} {
		if state, path := Check(t); state == Stale {
			out = append(out, path)
		}
	}
	return out
}

// HostTarget is the Ghostty spelling for the machine raj is running on, so a
// message telling someone what to run names a target they can actually type.
func HostTarget() Target {
	if runtime.GOOS == "darwin" {
		return Ghostty
	}
	return GhosttyLinux
}

// IncludedBy reports whether the user's Ghostty config already includes the
// file raj generates. A generated file nothing includes is the second way this
// fails silently, and it is the one an install can check for free.
func IncludedBy(confPath string) (bool, error) {
	dir := filepath.Dir(confPath)
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		return false, err
	}
	base := filepath.Base(confPath)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "config-file") {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), base) {
			return true, nil
		}
	}
	return false, nil
}
