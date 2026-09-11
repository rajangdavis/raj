// Package session remembers where a workspace was left, so returning to raj
// lands you where you were rather than in the explorer.
//
// # What is saved, and what deliberately is not
//
// Open tabs and which one was active, the primary cursor and scroll position in
// each, wrap, the expanded directories in the sidebar, and which pane had focus.
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
}

// Version is the current format.
const Version = 1

// File is where a workspace's state lives, relative to its root.
//
// Under .git rather than beside it: this is per-checkout scratch state, not
// something to commit or to show in the sidebar, and .git is already the
// directory tools put such things in and that everything ignores. A workspace
// with no .git falls back to .raj, which is where the hidden-files config
// already lives.
func File(root string) string {
	if root == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, ".git")); err == nil && info.IsDir() {
		return filepath.Join(root, ".git", "raj", "session.json")
	}
	return filepath.Join(root, ".raj", "session.json")
}

// Save writes the state. Temp file plus rename, so an interrupted write leaves
// the previous session rather than a truncated file that fails to parse.
func Save(root string, st State) error {
	path := File(root)
	if path == "" {
		return errors.New("session: no workspace root")
	}
	st.Version = Version
	data, err := json.MarshalIndent(st, "", "  ")
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
	path := File(root)
	if path == "" {
		return State{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil || st.Version != Version {
		return State{}
	}
	return st.validate(root)
}

// validate drops what no longer exists and clamps what is out of range.
func (st State) validate(root string) State {
	out := State{Version: st.Version, Focus: st.Focus}
	seen := map[string]bool{}
	for _, t := range st.Tabs {
		if t.Path == "" || !filepath.IsAbs(t.Path) || seen[t.Path] {
			continue
		}
		// Within the workspace: a session file is on disk and editable, so a
		// path in it is untrusted input like any other.
		if rel, err := filepath.Rel(root, t.Path); err != nil || rel == ".." ||
			(len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator)) {
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
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			out.Expanded = append(out.Expanded, d)
		}
	}
	sort.Strings(out.Expanded)
	return out
}
