// Command raj is a terminal editor that lives in Ghostty, inherits its theme,
// and borrows VSCode's keybindings via the kkp_on gate.
//
//	raj file.go          open a file
//	raj --tab 4 file.go  set the indent width
//	raj --config macos   print the Ghostty keybindings to install, then exit
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"raj/internal/app"
	"raj/internal/keys"
	"raj/internal/ui"
)

func main() {
	var (
		tab       = flag.Int("tab", 2, "indent width in spaces")
		configFor = flag.String("config", "", "print Ghostty keybindings for macos|linux and exit")
	)
	flag.Parse()

	if *configFor != "" {
		fmt.Print(keys.GhosttyConfig(*configFor))
		return
	}
	if err := run(flag.Arg(0), *tab); err != nil {
		fmt.Fprintln(os.Stderr, "raj:", err)
		os.Exit(1)
	}
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
	// an editor with no file is not a useful place for the keys to be. Once
	// session restore lands, a returning session takes precedence over both.
	if path != "" {
		a.OpenFile(path)
	}
	return a.Run()
}

// resolve splits the argument into a workspace root and a file to open.
// `raj .` and `raj some/dir` set the root and open nothing; `raj file.go` roots
// at the working directory and opens the file.
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
	// wherever the shell happened to be. `raj ~/other/thing.go` should show
	// that project in the explorer.
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
