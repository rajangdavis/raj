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

// The state dir is .raj even in a repository: whether the workspace is a
// checkout does not change where the editor keeps its scratch state.
func TestFileLocation(t *testing.T) {
	root := t.TempDir()
	if got := File(root); !strings.HasSuffix(got, filepath.Join(".raj", "session.json")) {
		t.Errorf("without .git: %s", got)
	}
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	if got := File(root); !strings.HasSuffix(got, filepath.Join(".raj", "session.json")) ||
		strings.Contains(got, filepath.Join(".git", "raj")) {
		t.Errorf("with .git: %s", got)
	}
	if File("") != "" {
		t.Error("no root should mean no file")
	}
}

// State lives in the XDG state home, outside the workspace, keyed per
// workspace. XDG_STATE_HOME wins when it is set.
func TestStateDirRespectsXDGStateHome(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	root := workspace(t)

	got := StateDir(root)
	wantUnder := filepath.Join(stateHome, "raj", "workspaces")
	if filepath.Dir(got) != wantUnder {
		t.Errorf("StateDir = %q, want a child of %q", got, wantUnder)
	}
	if again := StateDir(root); again != got {
		t.Errorf("StateDir is not stable: %q then %q", got, again)
	}
	if key := filepath.Base(got); !strings.HasPrefix(key, "raj-") {
		t.Errorf("key %q is not a raj workspace key", key)
	}
	if StateDir("") != "" {
		t.Error("no root should mean no state dir")
	}
}

// Without XDG_STATE_HOME the spec's default applies: $HOME/.local/state.
func TestStateDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	root := t.TempDir()

	got := StateDir(root)
	wantUnder := filepath.Join(home, ".local", "state", "raj", "workspaces")
	if filepath.Dir(got) != wantUnder {
		t.Errorf("StateDir = %q, want a child of %q", got, wantUnder)
	}
}

// Two workspaces can share a base name; the digest of the full path is what
// keeps their state apart, and a moved workspace gets a fresh directory.
func TestStateDirDistinguishesSameNamedRoots(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a := filepath.Join(t.TempDir(), "repo")
	b := filepath.Join(t.TempDir(), "repo")
	moved := filepath.Join(t.TempDir(), "repo")
	for _, dir := range []string{a, b, moved} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if StateDir(a) == StateDir(b) {
		t.Fatalf("same base name collided on %q", StateDir(a))
	}
	if StateDir(a) == StateDir(moved) {
		t.Errorf("a moved workspace kept its key: %q", StateDir(a))
	}
	if key := filepath.Base(StateDir(a)); !strings.Contains(key, "repo") {
		t.Errorf("key %q has no readable slug", key)
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

// The ratio survives a round trip, and is omitted when zero — both so a
// session written by an older build still reads (Ratio 0, plain Top) and so
// this build's files stay readable by one.
func TestRatioRoundTrip(t *testing.T) {
	root := workspace(t, "a.go")
	path := filepath.Join(root, "a.go")

	Save(root, State{Tabs: []Tab{{Path: path, Cursor: 3, Top: 10, Ratio: 0.5}}})
	data, _ := os.ReadFile(File(root))
	if !strings.Contains(string(data), "ratio") {
		t.Error("a nonzero ratio was omitted from the saved JSON")
	}
	st := Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Ratio != 0.5 || st.Tabs[0].Top != 10 {
		t.Errorf("tabs = %+v, want ratio 0.5, top 10", st.Tabs)
	}

	// Zero ratio is the same as no ratio: omitted from the JSON, read back
	// as zero.
	Save(root, State{Tabs: []Tab{{Path: path, Cursor: 3, Top: 10}}})
	data, _ = os.ReadFile(File(root))
	if strings.Contains(string(data), "ratio") {
		t.Error("a zero ratio was written into the JSON")
	}
	if st := Load(root); len(st.Tabs) != 1 || st.Tabs[0].Ratio != 0 {
		t.Errorf("zero-ratio round trip = %+v", st.Tabs)
	}
}

// A session file written before the ratio existed loads with Ratio 0 and its
// plain Top intact: restore falls back to the line number.
func TestLegacyJSONWithoutRatio(t *testing.T) {
	root := workspace(t, "a.go")
	path := filepath.Join(root, "a.go")
	p := File(root)
	os.MkdirAll(filepath.Dir(p), 0o700)
	body := `{"version":1,"tabs":[{"path":"` + path + `","cursor":3,"top":7,"wrap":true}],"active":0}`
	os.WriteFile(p, []byte(body), 0o644)

	st := Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Top != 7 || st.Tabs[0].Ratio != 0 {
		t.Errorf("legacy tab = %+v, want top 7, ratio 0", st.Tabs)
	}
}

// Hints is a tri-state on a saved tab: absent means the app default governs,
// so an old session file keeps loading, while an explicit false is written and
// read back as a choice.
func TestHintsRoundTripAndAbsence(t *testing.T) {
	root := workspace(t, "a.go")
	path := filepath.Join(root, "a.go")

	off := false
	if err := Save(root, State{Tabs: []Tab{{Path: path, Hints: &off}}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(File(root))
	if !strings.Contains(string(data), `"hints": false`) {
		t.Errorf("an explicit false was omitted from the saved JSON: %s", data)
	}
	st := Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Hints == nil || *st.Tabs[0].Hints {
		t.Fatalf("tabs = %+v, want an explicit false", st.Tabs)
	}

	on := true
	Save(root, State{Tabs: []Tab{{Path: path, Hints: &on}}})
	st = Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Hints == nil || !*st.Tabs[0].Hints {
		t.Fatalf("tabs = %+v, want an explicit true", st.Tabs)
	}

	// Absence is the app default: a file written before the field existed
	// loads with Hints nil.
	p := File(root)
	body := `{"version":1,"tabs":[{"path":"` + path + `","cursor":0,"top":0,"wrap":true}],"active":0}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st = Load(root)
	if len(st.Tabs) != 1 || st.Tabs[0].Hints != nil {
		t.Errorf("tabs = %+v, want Hints nil so the app default applies", st.Tabs)
	}
}

// The sidebar field is tri-state: a named pane and an explicit closed state
// both survive a round trip, and a session written before the field decodes
// nil so the caller keeps its default.
func TestSidebarRoundTrip(t *testing.T) {
	root := t.TempDir()
	search := "search"
	closed := ""
	for _, tc := range []struct {
		name string
		in   *string
	}{
		{"named", &search},
		{"closed", &closed},
		{"absent", nil},
	} {
		blob, err := Encode(State{Version: Version, Sidebar: tc.in})
		if err != nil {
			t.Fatal(err)
		}
		got := Decode(blob, root)
		if tc.in == nil {
			if got.Sidebar != nil {
				t.Errorf("%s: sidebar = %q, want nil", tc.name, *got.Sidebar)
			}
			continue
		}
		if got.Sidebar == nil || *got.Sidebar != *tc.in {
			t.Errorf("%s: sidebar did not round-trip", tc.name)
		}
	}
}
