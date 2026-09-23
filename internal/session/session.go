// Package session remembers where a workspace was left, so returning to raj
// lands you where you were rather than in the explorer.
//
// # What is saved, and what deliberately is not
//
// Open tabs and which one was active, the primary cursor and scroll position in
// each, wrap, whether inlay hints are on, the expanded directories in the
// sidebar, and which pane had focus.
// All of it is view state: recreating it wrong costs a scroll, and recreating it
// not at all costs the same as today.
//
// Not saved: buffer contents. A dirty buffer's text lives in the piece table's
// journal, and persisting that is a different problem with a different failure
// mode — restoring stale text over a file someone edited elsewhere destroys
// work, where restoring a stale cursor position does not. So a dirty buffer is
// restored as its file on disk, and docs/TODO.md keeps the journal as its own item.
//
// For the same reason an unnamed buffer is not restored at all: it is keyed on
// a path and has none.
//
// # Validation
//
// A session is a hint, never an instruction. Every field is re-checked against
// the world as found: a file that has been deleted or has become unreadable is
// dropped, a cursor past the end of a file is clamped, an expanded directory
// that no longer exists is forgotten. The alternative — trusting the file — is
// a startup crash from a JSON blob written by an older build, which is a much
// worse failure than a lost scroll position.
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ws "raj/internal/workspace"
)

// Tab is one restored editor tab.
type Tab struct {
	Path   string `json:"path"`
	Cursor int    `json:"cursor"` // byte offset of the primary cursor
	Top    int    `json:"top"`    // first visible line
	// Ratio is Top/Lines() at save time: the scroll position as a proportion
	// of the document, so a restore lands at the same place in a file that
	// has grown and in a terminal that has resized. Zero when there is
	// nothing to be proportional to (an empty file), and omitted from the
	// JSON so a session written by an older build still reads as Ratio 0.
	Ratio float64 `json:"ratio,omitempty"`
	Wrap  bool    `json:"wrap"`

	// Hints is the pane inlay-hints flag. It is a pointer so absence is a
	// distinct state: an old session with no hints field loads nil and the app
	// default applies, while an explicit false is a choice that is kept.
	Hints *bool `json:"hints,omitempty"`
}

// State is a whole workspace's remembered position.
type State struct {
	// Version guards against a format change. A file from a different version
	// is ignored rather than migrated: this is view state, and the cost of
	// starting fresh is one scroll.
	Version  int      `json:"version"`
	Tabs     []Tab    `json:"tabs"`
	Active   int      `json:"active"`
	Expanded []string `json:"expanded"`
	Focus    string   `json:"focus"`

	// Sidebar is the sidebar pane that was showing, by name, or a pointer to
	// "" when it was closed. It is a pointer so absence is distinct: a session
	// written before the field loads nil and the app keeps its default pane,
	// while an explicit "" restores the closed state.
	Sidebar *string `json:"sidebar,omitempty"`
}

// Version is the current format.
const Version = 1

// StateDir is the workspace's state directory in the XDG state home:
// $XDG_STATE_HOME/raj/workspaces/<key>. The session database, the op logs and
// the trash live here, outside the workspace, so a checkout stays clean and
// state survives a read-only or bare workspace. `.raj` is only the legacy
// location an older build wrote; the one-shot migrations read it and move on.
//
// The key is a readable slug of the root's base name plus a short digest of
// the absolute cleaned root, so two workspaces with the same name do not share
// state and a workspace that moves gets a fresh directory. Empty when there is
// no root or no state home to put it in; callers treat that as "no state".
func StateDir(root string) string {
	return StateDirForRoots([]string{root})
}

// StateDirForRoots is StateDir for a workspace root set: the same
// <stateHome>/raj/workspaces/<key> directory, but keyed by every root, so a
// multi-root workspace owns one state directory and two different sets do not
// share one. Each root is made absolute the way StateDir always did, and the
// key sorts the set first, so the directory does not depend on the order the
// caller supplied. Empty when the set is empty or there is no state home;
// callers treat that as "no state".
func StateDirForRoots(roots []string) string {
	home := stateHome()
	if home == "" {
		return ""
	}
	abs := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		a, err := filepath.Abs(r)
		if err != nil {
			a = r
		}
		abs = append(abs, a)
	}
	if len(abs) == 0 {
		return ""
	}
	return filepath.Join(home, "raj", "workspaces", ws.StateKey(abs))
}

// WorkspacesDir is the parent of every workspace's state directory in the XDG
// state home: <stateHome>/raj/workspaces. StateDir is a child of it, keyed by
// the workspace root. It is the directory `daemon list` walks to find every
// running daemon. Empty when there is no state home, which a caller treats as
// "no state" rather than building a relative path.
func WorkspacesDir() string {
	home := stateHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "raj", "workspaces")
}

// stateHome resolves the XDG state home: $XDG_STATE_HOME when set, otherwise
// $HOME/.local/state as the base-directory spec prescribes. Empty when neither
// is available, so a caller never builds a relative state path.
func stateHome() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state")
}

// Dir is the workspace's legacy state directory: always .raj, whether or not
// the workspace is a repository. State and the workspace config live outside
// the workspace now, so .raj is read only by the one-shot migrations that
// move what an older build left here.
//
// There is no migration from the old .git/raj: a workspace that has one starts
// fresh under .raj, and the old directory is left untouched.
func Dir(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".raj")
}

// File is where a legacy workspace's session lives, relative to its root. It
// is read only to adopt a session.json written before the store existed, and
// removed once that session has been written to the store. Nothing writes it.
func File(root string) string {
	dir := Dir(root)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "session.json")
}

// Save writes the state. Temp file plus rename, so an interrupted write leaves
// the previous session rather than a truncated file that fails to parse.
func Save(root string, st State) error {
	path := File(root)
	if path == "" {
		return errors.New("session: no workspace root")
	}
	data, err := Encode(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the state and validates it against the filesystem. A missing,
// unreadable, malformed or wrong-version file is an empty state and no error:
// there is nothing a caller could usefully do differently, and failing to start
// because a scratch file is corrupt would be absurd.
func Load(root string) State {
	return LoadForRoots([]string{root})
}

// LoadForRoots is Load for a workspace root set. The legacy session file still
// lives at the primary root (the first of the set), exactly where it always
// did, but every path it carries is validated against every root rather than
// against the primary alone.
func LoadForRoots(roots []string) State {
	path := File(firstRoot(roots))
	if path == "" {
		return State{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	return DecodeForRoots(data, roots)
}

// firstRoot is the primary of a set: the root the workspace was opened on, or
// "" when the set is empty. It mirrors workspace.Roots.Primary without
// depending on the type, because session takes the roots as plain strings.
func firstRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

// containsAny reports whether path is equal to or inside any root, with the
// same component-wise rule workspace.Roots.Contains applies. It is stated here
// rather than calling that method because session takes plain strings and does
// not depend on the workspace package beyond its StateKey.
func containsAny(roots []string, path string) bool {
	if len(roots) == 0 || path == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// Decode parses a state blob and validates it against root. It is the read
// half of the codec, shared by Load and the store: a blob from the database is
// the same JSON as the file, so it gets the same version check and the same
// clamp-and-drop validation. A malformed or wrong-version blob is an empty
// state, never an error.
func Decode(data []byte, root string) State {
	return DecodeForRoots(data, []string{root})
}

// DecodeForRoots is Decode for a workspace root set: the same version check and
// the same clamp-and-drop validation, with a path kept when it is under any
// root rather than only the primary.
func DecodeForRoots(data []byte, roots []string) State {
	var st State
	if err := json.Unmarshal(data, &st); err != nil || st.Version != Version {
		return State{}
	}
	return st.validate(roots)
}

// Encode renders a state as the JSON blob stored on disk and in the store. It
// stamps the current version so a caller cannot persist a state without one.
// The bytes are the JSON the file form has always carried: the store is a
// different place, not a different format.
func Encode(st State) ([]byte, error) {
	st.Version = Version
	return json.MarshalIndent(st, "", "  ")
}

// validate drops what no longer exists and clamps what is out of range.
func (st State) validate(roots []string) State {
	out := State{Version: st.Version, Focus: st.Focus, Sidebar: st.Sidebar}
	seen := map[string]bool{}
	for _, t := range st.Tabs {
		if t.Path == "" || !filepath.IsAbs(t.Path) || seen[t.Path] {
			continue
		}
		// Within the workspace: a session file is on disk and editable, so a
		// path in it is untrusted input like any other. A path matching no
		// root is dropped, because a session is a hint.
		if !containsAny(roots, t.Path) {
			continue
		}
		info, err := os.Stat(t.Path)
		if err != nil || info.IsDir() {
			continue // deleted, renamed, or turned into a directory
		}
		// Clamp rather than drop: a file that shrank since last time should
		// still open, at the top.
		if t.Cursor < 0 || int64(t.Cursor) > info.Size() {
			t.Cursor = 0
			t.Top = 0
		}
		if t.Top < 0 {
			t.Top = 0
		}
		seen[t.Path] = true
		out.Tabs = append(out.Tabs, t)
	}
	out.Active = st.Active
	if out.Active < 0 || out.Active >= len(out.Tabs) {
		out.Active = 0
	}
	for _, d := range st.Expanded {
		// A path no root contains is dropped for the same reason a tab is: a
		// session is a hint about this workspace, not a way to name a
		// directory elsewhere.
		if !containsAny(roots, d) {
			continue
		}
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			out.Expanded = append(out.Expanded, d)
		}
	}
	sort.Strings(out.Expanded)
	return out
}
