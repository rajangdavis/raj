package hidden

import (
	"os"
	"path/filepath"
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
	if r := Load(root); r.Hidden("dist", true) {
		t.Fatal("dist hidden before any configuration said so")
	}
	os.MkdirAll(filepath.Join(root, ".raj"), 0o755)
	os.WriteFile(filepath.Join(root, ".raj", "hidden"), []byte("# mine\ndist/\n!vendor/\n"), 0o644)

	r := Load(root)
	if !r.Hidden("dist", true) {
		t.Error("workspace pattern not applied")
	}
	if r.Hidden("vendor", true) {
		t.Error("workspace negation did not override the default")
	}
	if !r.Hidden(".git", true) {
		t.Error("defaults were replaced rather than extended")
	}
	if len(r.Sources) != 1 {
		t.Errorf("Sources = %v, want the one workspace file", r.Sources)
	}
}

// The workspace file is read after the user's, so a repository can disagree with
// a personal preference.
func TestWorkspaceOverridesUserConfig(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "raj"), 0o755)
	os.WriteFile(filepath.Join(cfg, "raj", "hidden"), []byte("*.log\n"), 0o644)

	root := t.TempDir()
	if !Load(root).Hidden("a.log", false) {
		t.Fatal("user configuration not read")
	}
	os.MkdirAll(filepath.Join(root, ".raj"), 0o755)
	os.WriteFile(filepath.Join(root, ".raj", "hidden"), []byte("!*.log\n"), 0o644)
	if Load(root).Hidden("a.log", false) {
		t.Error("workspace did not override the user configuration")
	}
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	isolate(t)
	r := Load(filepath.Join(t.TempDir(), "nope"))
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
