// Command raj is a terminal editor that borrows VSCode's keybindings.
//
//	raj                       open the workspace in the file explorer
//	raj file.go               open a file
//	raj some/dir              open a directory as the workspace
//	raj --tab 4 file.go       set the indent width
//	raj --config ghostty      print Ghostty keybindings to install
//	raj --config iterm2       print an iTerm2 dynamic profile
//	raj --probe               check which chords this terminal delivers
//	raj --probe --checklist   walk every binding and emit a measured keymap
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"raj/internal/app"
	"raj/internal/keys"
	"raj/internal/probe"
	"raj/internal/ui"
)

func main() {
	var (
		tab       = flag.Int("tab", 2, "indent width in spaces")
		configFor = flag.String("config", "", "emit keybindings: ghostty, ghostty-linux, or iterm2")
		runProbe  = flag.Bool("probe", false, "report what chords this terminal delivers")
		checklist = flag.Bool("checklist", false, "with --probe: walk every binding in order")
		kkpFlags  = flag.Int("kkp", 0, "with --probe: KKP flags to push (0 = raj's own)")
	)
	flag.Parse()

	if *configFor != "" {
		out, err := config(*configFor)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
		return
	}
	if *runProbe {
		// The probe lives behind a flag on raj rather than in its own binary so
		// that testing a terminal needs no second build: whatever raj you are
		// running is the decoder being measured.
		if err := probe.Run(*kkpFlags, *checklist); err != nil {
			fail(err)
		}
		return
	}
	if err := run(flag.Arg(0), *tab); err != nil {
		fail(err)
	}
}

// config renders the keybindings for a terminal. Every emitter reads the same
// measured table, so they cannot disagree about what a chord should send.
func config(target string) (string, error) {
	switch target {
	case "ghostty", "macos":
		return keys.GhosttyConfig("macos"), nil
	case "ghostty-linux", "linux":
		return keys.GhosttyConfig("linux"), nil
	case "iterm2":
		return keys.ITerm2Profile("raj"), nil
	}
	return "", fmt.Errorf("unknown target %q: want ghostty, ghostty-linux, or iterm2", target)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "raj:", err)
	os.Exit(1)
}

func run(path string, tab int) error {
	root, path, err := resolve(path)
	if err != nil {
		return err
	}

	host, err := ui.NewNativeHost(os.Stdin, os.Stdout, 150*time.Millisecond)
	if err != nil {
		return err
	}
	// Leave the terminal usable whatever happens, including a panic: a raj that
	// exits without popping the KKP flags leaves a shell where cmd+w does
	// nothing and there is no obvious way out.
	defer host.Close()

	a := app.New(host, root, tab)
	// A named file takes the focus; otherwise raj opens in the explorer, since
	// an editor with no file is not a useful place for the keys to be.
	if path != "" {
		a.OpenFile(path)
	}
	return a.Run()
}

// resolve splits the argument into a workspace root and a file to open.
func resolve(arg string) (root, file string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	if arg == "" {
		return workspace(cwd), "", nil
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", "", err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return workspace(abs), "", nil
	}
	// A file argument roots the workspace at the file's own project, not at
	// wherever the shell happened to be.
	return workspace(filepath.Dir(abs)), abs, nil
}

// workspace walks up to the nearest enclosing repository, falling back to the
// directory itself. Editing one file in a project should still give you the
// project to search and explore.
func workspace(dir string) string {
	for d := dir; ; {
		if info, err := os.Stat(filepath.Join(d, ".git")); err == nil && info != nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}
