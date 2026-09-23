package hidden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the user-level configuration at an empty directory, so a test
// measures the rules under test rather than whatever the developer running it
// happens to have in ~/.config/raj/hidden.
func isolate(t *testing.T) { t.Helper(); t.Setenv("XDG_CONFIG_HOME", t.TempDir()) }

// The defaults are the whole point of the package: .git is noise, and a dotfile
// at the root of a repository usually is not. .gitlab-ci.yml is named
// explicitly because hiding it is the bug this replaced.
func TestDefaults(t *testing.T) {
	r := Default()
	cases := []struct {
		path string
		dir  bool
		want bool
	}{
		{".git", true, true},
		// Judged as one entry: "config" is not itself a hidden name. A walk
		// never asks, having skipped .git whole; a caller holding a path asks
		// per component, and the ".git" component above answers.
		{".git/config", false, false},
		// .raj is hidden whole now: the workspace configuration it used to
		// hold lives in XDG, so nothing inside it is repository content. An
		// entry under it is judged as one entry and does not match the .raj
		// rule; a walk never asks, having skipped .raj.
		{".raj", true, true},
		{".raj/logs", true, false},
		{".raj/hidden", false, false},
		{"node_modules", true, true},
		{"vendor", true, true},
		{".DS_Store", false, true},
		{".gitlab-ci.yml", false, false},
		{".github", true, false},
		{".github/workflows/ci.yml", false, false},
		{".gitignore", false, false},
		{".env", false, false},
		{"src/main.go", false, false},
		{"README.md", false, false},
		// A FILE called vendor or node_modules is not the directory the rule
		// means, and the trailing slash is what says so.
		{"vendor", false, false},
		{"node_modules", false, false},
	}
	for _, c := range cases {
		if got := r.Hidden(c.path, c.dir); got != c.want {
			t.Errorf("Hidden(%q, dir=%v) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}

// .raj is the editor's scratch, and the workspace configuration that used to
// live at .raj/hidden now lives outside the project, so the whole directory is
// hidden. Nothing un-hides .raj/hidden: the legacy path is not special-cased.
func TestRajesOwnStateIsHidden(t *testing.T) {
	isolate(t)
	r := Default()
	if !r.Hidden(".raj", true) {
		t.Fatal(".raj is not hidden")
	}
	for _, p := range r.Patterns() {
		if strings.HasPrefix(p, "!") && strings.Contains(p, ".raj") {
			t.Errorf("default un-hides %q; .raj must be hidden whole", p)
		}
	}
	// Hidden judges one entry at a time, so a path into the directory is
	// reached only by asking about each component in turn — which is what
	// search's eligible does for a buffer the walk never visited. One of them
	// has to answer, or the path is not hidden at all.
	under := []string{".raj", "hidden"}
	hidden := false
	for i := range under {
		if r.Hidden(strings.Join(under[:i+1], "/"), i < len(under)-1) {
			hidden = true
			break
		}
	}
	if !hidden {
		t.Error("a file under .raj never met a hidden component")
	}
	// Adding a rule extends the defaults; it does not replace them.
	if !r.Hidden(".git", true) || !r.Hidden("node_modules", true) ||
		r.Hidden(".github", true) || r.Hidden(".gitlab-ci.yml", false) {
		t.Error("the other defaults changed")
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		path  string
		dir   bool
		want  bool
		alsoP string // a second path that must NOT match
	}{
		{name: "glob on extension", src: "*.log", path: "debug.log", want: true, alsoP: "debug.txt"},
		{name: "exact name at any depth", src: "TAGS", path: "deep/inner/TAGS", want: true},
		{name: "rooted path", src: "build/out", path: "build/out", dir: true, want: true, alsoP: "src/build/out"},
		{name: "leading slash is rooted", src: "/target/", path: "target", dir: true, want: true, alsoP: "sub/target"},
		{name: "dir only ignores files", src: "cache/", path: "cache", dir: false, want: false},
		{name: "negation un-hides a default", src: "!vendor/", path: "vendor", dir: true, want: false},
		{name: "negation then re-hide", src: "!vendor/\nvendor/", path: "vendor", dir: true, want: true},
		{name: "comment ignored", src: "# *.log", path: "debug.log", want: false},
		{name: "blank lines ignored", src: "\n\n*.log\n\n", path: "debug.log", want: true},
		{name: "hide all dotfiles, old behaviour", src: ".*", path: ".gitlab-ci.yml", want: true},
		{name: "hide dotfiles but keep CI", src: ".*\n!.gitlab-ci.yml", path: ".gitlab-ci.yml", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.src)
			if got := r.Hidden(c.path, c.dir); got != c.want {
				t.Errorf("Hidden(%q, dir=%v) = %v, want %v", c.path, c.dir, got, c.want)
			}
			if c.alsoP != "" && r.Hidden(c.alsoP, c.dir) {
				t.Errorf("Hidden(%q) matched but should not have", c.alsoP)
			}
		})
	}
}

// The last matching rule decides, which is what makes a configuration file able
// to disagree with the defaults rather than only add to them.
func TestLastMatchWins(t *testing.T) {
	r := Parse("!node_modules/\n")
	if r.Hidden("node_modules", true) {
		t.Error("configuration could not un-hide a default")
	}
}

// A typo should cost the line, not the editor.
func TestBadPatternsAreReportedNotFatal(t *testing.T) {
	r := Parse("*.log\n[unclosed\n*.tmp\n")
	if len(r.Bad) != 1 || r.Bad[0] != "[unclosed" {
		t.Fatalf("Bad = %q, want the one broken line", r.Bad)
	}
	if !r.Hidden("a.log", false) || !r.Hidden("a.tmp", false) {
		t.Error("valid patterns either side of a bad one were dropped")
	}
}

func TestLoadReadsWorkspaceFile(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	if r := Load([]string{root}); r.Hidden("dist", true) {
		t.Fatal("dist hidden before any configuration said so")
	}
	path := WorkspaceFile([]string{root})
	if path == "" {
		t.Fatal("WorkspaceFile returned nothing under a temp XDG_CONFIG_HOME")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# mine\ndist/\n!vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Load([]string{root})
	if !r.Hidden("dist", true) {
		t.Error("workspace pattern not applied")
	}
	if r.Hidden("vendor", true) {
		t.Error("workspace negation did not override the default")
	}
	if !r.Hidden(".git", true) {
		t.Error("defaults were replaced rather than extended")
	}
	if !r.Hidden(".raj", true) {
		t.Error("the .raj default went missing")
	}
	if len(r.Sources) != 1 {
		t.Errorf("Sources = %v, want the one workspace file", r.Sources)
	}
}

// The workspace file is read after the user's, so a workspace can disagree with
// a personal preference.
func TestWorkspaceOverridesUserConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "raj"), 0o755)
	os.WriteFile(filepath.Join(cfg, "raj", "hidden"), []byte("*.log\n"), 0o644)

	root := t.TempDir()
	if !Load([]string{root}).Hidden("a.log", false) {
		t.Fatal("user configuration not read")
	}
	path := WorkspaceFile([]string{root})
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("!*.log\n"), 0o644)
	if Load([]string{root}).Hidden("a.log", false) {
		t.Error("workspace did not override the user configuration")
	}
}

// The workspace file is keyed by the whole root set, so two workspaces do not
// share one configuration.
func TestWorkspaceKeyIsolatesWorkspaces(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	a, b := t.TempDir(), t.TempDir()
	if WorkspaceFile([]string{a}) == WorkspaceFile([]string{b}) {
		t.Fatal("two root sets share one workspace file")
	}
	path := WorkspaceFile([]string{a})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("only-a/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Load([]string{a}).Hidden("only-a", true) {
		t.Error("A's configuration was not read for A")
	}
	if Load([]string{b}).Hidden("only-a", true) {
		t.Error("A's configuration leaked into B")
	}
}

// With no XDG_CONFIG_HOME and no home there is nowhere to key a workspace
// config, and no root is no workspace at all.
func TestWorkspaceFileNeedsAHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := WorkspaceFile([]string{t.TempDir()}); got != "" {
		t.Errorf("WorkspaceFile = %q, want empty with no home", got)
	}
	if got := WorkspaceFile(nil); got != "" {
		t.Errorf("WorkspaceFile(nil) = %q, want empty with no root", got)
	}
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	isolate(t)
	r := Load([]string{filepath.Join(t.TempDir(), "nope")})
	if !r.Hidden(".git", true) || len(r.Sources) != 0 {
		t.Error("a missing configuration file did not fall back to the defaults")
	}
}

// A nil *Rules hides nothing rather than panicking: the field is exported on
// three structs and a caller that builds one by hand should not crash the walk.
func TestNilRulesHideNothing(t *testing.T) {
	var r *Rules
	if r.Hidden(".git", true) || r.HiddenPath("/root", "/root/.git", true) || r.Patterns() != nil {
		t.Error("nil Rules did not behave as empty")
	}
}

// Everything is the -hidden switch's policy: it hides nothing, so a walk that
// applies it reaches .git, node_modules and vendor just as it reaches ordinary
// files. It is the explicit counterpart to nil, which means the built-in
// defaults.
func TestEverythingHidesNothing(t *testing.T) {
	r := Everything()
	if r == nil {
		t.Fatal("Everything returned nil, which means the built-in defaults")
	}
	for _, c := range []struct {
		path string
		dir  bool
	}{
		{".git", true},
		{"node_modules", true},
		{"vendor", true},
		{".raj/trash", true},
		{"a.log", false},
	} {
		if r.Hidden(c.path, c.dir) {
			t.Errorf("Hidden(%q, dir=%v) = true under Everything; it must hide nothing", c.path, c.dir)
		}
	}
	if len(r.Patterns()) != 0 {
		t.Errorf("Patterns() = %v, want none", r.Patterns())
	}
}

func TestHiddenPath(t *testing.T) {
	isolate(t)
	r := Default()
	if !r.HiddenPath("/w", "/w/node_modules", true) {
		t.Error("relative conversion failed")
	}
	if r.HiddenPath("/w", "/w/src/node_modules.go", false) {
		t.Error("a file named after a hidden directory was hidden")
	}
	// An unrelated path is judged on its base name rather than on a chain of
	// "..", which would match nothing and silently include everything.
	if !r.HiddenPath("/w", "/elsewhere/.DS_Store", false) {
		t.Error("path outside the root was not judged on its base name")
	}
}

// Parsing arbitrary bytes must not panic, and the answer must not depend on
// when it is asked — the search worker reads a Rules off the event thread.
func FuzzParse(f *testing.F) {
	f.Add("*.log\n!keep.log\n", "keep.log")
	f.Add("[bad\n", ".git")
	f.Add("!\n/\n//\n./\n", "a/b")
	f.Add("a/b/c/\n**/x\n", "a/b/c")
	f.Fuzz(func(t *testing.T, src, p string) {
		r := Parse(src)
		for _, dir := range []bool{true, false} {
			got := r.Hidden(p, dir)
			if again := r.Hidden(p, dir); again != got {
				t.Fatalf("Hidden(%q) not deterministic", p)
			}
		}
		// Every surviving pattern must be reportable, so the effective list a
		// user is shown cannot silently omit a rule that is being applied.
		if len(r.Patterns()) != len(r.rules) {
			t.Fatalf("Patterns() lost a rule")
		}
	})
}

// The root itself is never hidden: hiding it would empty the editor.
func FuzzRootIsNeverHidden(f *testing.F) {
	f.Add("*\n.*\n/\n")
	f.Fuzz(func(t *testing.T, src string) {
		r := Parse(src)
		for _, p := range []string{"", ".", "./"} {
			if r.Hidden(p, true) {
				t.Fatalf("root %q hidden by %q", p, src)
			}
		}
	})
}

// Hidden runs once per directory entry of every walk, so its cost is multiplied
// by the size of the repository. It replaced a single strings.HasPrefix, which
// is the bar it has to stay near.
func BenchmarkHidden(b *testing.B) {
	r := Default()
	paths := []string{"src/main.go", "README.md", ".gitlab-ci.yml", "node_modules", ".git"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Hidden(paths[i%len(paths)], i%2 == 0)
	}
}
