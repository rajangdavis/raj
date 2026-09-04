package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workspace(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("hello\nworld\n"), 0o644)
	}
	return root
}

func TestRoundTrip(t *testing.T) {
	root := workspace(t, "a.go", "sub/b.go")
	want := State{
		Tabs: []Tab{
			{Path: filepath.Join(root, "a.go"), Cursor: 3, Top: 0, Wrap: true},
			{Path: filepath.Join(root, "sub/b.go"), Cursor: 0, Top: 1},
		},
		Active:   1,
		Expanded: []string{filepath.Join(root, "sub")},
		Focus:    "sidebar",
	}
	if err := Save(root, want); err != nil {
		t.Fatal(err)
	}
	got := Load(root)
	if len(got.Tabs) != 2 || got.Tabs[0] != want.Tabs[0] || got.Tabs[1] != want.Tabs[1] {
		t.Errorf("tabs = %+v", got.Tabs)
	}
	if got.Active != 1 || got.Focus != "sidebar" || len(got.Expanded) != 1 {
		t.Errorf("got %+v", got)
	}
}

// A workspace with a repository puts the file under .git, which is scratch
// state nothing commits and the sidebar already hides.
func TestFileLocation(t *testing.T) {
	root := t.TempDir()
	if got := File(root); !strings.HasSuffix(got, filepath.Join(".raj", "session.json")) {
		t.Errorf("without .git: %s", got)
	}
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	if got := File(root); !strings.Contains(got, filepath.Join(".git", "raj")) {
		t.Errorf("with .git: %s", got)
	}
	if File("") != "" {
		t.Error("no root should mean no file")
	}
}

// A session is a hint. Every one of these used to be a way to fail at startup
// over a scratch file, which is a much worse outcome than a lost scroll.
func TestLoadIsNeverFatal(t *testing.T) {
	root := workspace(t)
	path := File(root)
	os.MkdirAll(filepath.Dir(path), 0o700)

	for _, body := range []string{
		"",
		"not json at all",
		`{"version": 999, "tabs": [{"path": "/etc/passwd"}]}`,
		`{"version": 1, "tabs": null, "active": 42}`,
		`{"version": 1, "tabs": [{"path": ""}], "active": -1}`,
	} {
		os.WriteFile(path, []byte(body), 0o644)
		st := Load(root)
		if len(st.Tabs) != 0 {
			t.Errorf("%q restored tabs: %+v", body, st.Tabs)
		}
		if st.Active != 0 {
			t.Errorf("%q left Active at %d", body, st.Active)
		}
	}
	os.Remove(path)
	if st := Load(root); len(st.Tabs) != 0 {
		t.Error("a missing file restored something")
	}
}

func TestValidateDropsWhatIsGone(t *testing.T) {
	root := workspace(t, "kept.go")
	gone := filepath.Join(root, "gone.go")
	dir := filepath.Join(root, "adir")
	os.MkdirAll(dir, 0o755)

	Save(root, State{Tabs: []Tab{
		{Path: gone}, // deleted since
		{Path: dir},  // now a directory
		{Path: filepath.Join(root, "kept.go")},
		{Path: filepath.Join(root, "kept.go")}, // duplicate
		{Path: "relative.go"},                  // not absolute
	}, Expanded: []string{dir, filepath.Join(root, "nodir")}})

	st := Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Path != filepath.Join(root, "kept.go") {
		t.Errorf("tabs = %+v", st.Tabs)
	}
	if len(st.Expanded) != 1 || st.Expanded[0] != dir {
		t.Errorf("expanded = %v", st.Expanded)
	}
}

// A session file is on disk and editable, so a path in it is untrusted input.
func TestValidateRejectsPathsOutsideTheWorkspace(t *testing.T) {
	root := workspace(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	os.WriteFile(outside, []byte("x"), 0o644)

	Save(root, State{Tabs: []Tab{{Path: outside}, {Path: "/etc/passwd"}}})
	if st := Load(root); len(st.Tabs) != 0 {
		t.Errorf("restored a tab outside the workspace: %+v", st.Tabs)
	}
}

// A file that shrank since last time still opens, at the top, rather than being
// dropped or restored to an offset past its end.
func TestCursorPastEndIsClamped(t *testing.T) {
	root := workspace(t, "a.go")
	path := filepath.Join(root, "a.go")
	Save(root, State{Tabs: []Tab{{Path: path, Cursor: 9999, Top: 500}}})

	st := Load(root)
	if len(st.Tabs) != 1 {
		t.Fatalf("tabs = %+v", st.Tabs)
	}
	if st.Tabs[0].Cursor != 0 || st.Tabs[0].Top != 0 {
		t.Errorf("cursor = %d, top = %d; want both reset", st.Tabs[0].Cursor, st.Tabs[0].Top)
	}
}

func TestActiveIsClampedToTheKeptTabs(t *testing.T) {
	root := workspace(t, "a.go")
	Save(root, State{Tabs: []Tab{
		{Path: filepath.Join(root, "gone.go")},
		{Path: filepath.Join(root, "a.go")},
	}, Active: 1})
	st := Load(root)
	if len(st.Tabs) != 1 || st.Active != 0 {
		t.Errorf("active = %d over %d tabs", st.Active, len(st.Tabs))
	}
}

// An interrupted write must leave the previous session, not a truncated file
// that fails to parse — which is why Save renames rather than writing in place.
func TestSaveIsAtomic(t *testing.T) {
	root := workspace(t, "a.go")
	Save(root, State{Tabs: []Tab{{Path: filepath.Join(root, "a.go"), Cursor: 5}}})
	if entries, _ := os.ReadDir(filepath.Dir(File(root))); len(entries) != 1 {
		t.Errorf("left %d files behind, want only session.json", len(entries))
	}
	if st := Load(root); len(st.Tabs) != 1 || st.Tabs[0].Cursor != 5 {
		t.Errorf("got %+v", st)
	}
}
