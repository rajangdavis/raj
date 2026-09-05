// Command raj is a terminal editor that borrows VSCode's keybindings.
//
//	raj                       open the workspace in the file explorer
//	raj file.go               open a file
//	raj some/dir              open a directory as the workspace
//	raj --tab 4 file.go       set the indent width
//	raj --tabs file.go        indent with tabs where the file does not say
//	raj --control             listen on a control socket for buffer edits
//	raj --control-addr tcp://0.0.0.0:7391
//	                          listen on TCP, for a driver in a container
//	raj --no-restore          start fresh instead of where you left off
//	raj ctl <cmd>             read and edit a running raj's buffers
//	raj --config ghostty      print Ghostty keybindings to install
//	raj --config iterm2       print an iTerm2 dynamic profile
//	raj --keys                print the keybinding reference as markdown
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
	"raj/internal/control"
	"raj/internal/keys"
	"raj/internal/probe"
	"raj/internal/ui"
)

func main() {
	// `raj ctl ...` is a different program sharing a binary: it talks to a
	// running editor over the control socket and never opens a terminal. It is
	// handled before flag.Parse because its flags are its own.
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(control.CLI(os.Args[2:], os.Stdout, os.Stderr))
	}
	var (
		tab       = flag.Int("tab", 2, "indent width in spaces, and the display width of a tab")
		useTabs   = flag.Bool("tabs", false, "indent with tabs in files that have no indentation to detect")
		ctl       = flag.Bool("control", false, "listen on a Unix socket for buffer reads and edits")
		ctlPath   = flag.String("control-socket", "", "path for --control; implies it")
		ctlAddr   = flag.String("control-addr", "", "listen on tcp://host:port instead of a socket; implies --control")
		ctlExec   = flag.Bool("control-exec", false, "with --control-addr: let a remote driver run commands on this machine")
		noRestore = flag.Bool("no-restore", false, "do not reopen the previous session")
		wrap      = flag.Bool("wrap", true, "wrap long lines; --wrap=false scrolls horizontally instead")
		configFor = flag.String("config", "", "emit keybindings: ghostty, ghostty-linux, or iterm2")
		keyDoc    = flag.Bool("keys", false, "print the keybinding reference as markdown")
		runProbe  = flag.Bool("probe", false, "report what chords this terminal delivers")
		checklist = flag.Bool("checklist", false, "with --probe: walk every binding in order")
		kkpFlags  = flag.Int("kkp", 0, "with --probe: KKP flags to push (0 = raj's own)")
	)
	flag.Parse()

	if *keyDoc {
		fmt.Print(keys.Doc())
		return
	}
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
	addr := *ctlPath
	if *ctlAddr != "" {
		addr = *ctlAddr
	}
	if err := run(flag.Arg(0), *tab, *wrap, *useTabs, *ctl || addr != "", addr, *ctlExec, *noRestore); err != nil {
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

func run(path string, tab int, wrap bool, useTabs bool, ctl bool, ctlAddr string, ctlExec bool, noRestore bool) error {
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
	a.WrapDefault = wrap
	// Only a fallback: a file whose own indentation is readable keeps it, so
	// this decides new buffers and blank ones and nothing else.
	a.Tabs.IndentTabs = useTabs
	a.NoRestore = noRestore
	// Before any file named on the command line, so an explicitly requested
	// file ends up focused rather than buried under restored tabs.
	a.RestoreSession()
	defer func() {
		if err := a.SaveSession(); err != nil {
			fmt.Fprintln(os.Stderr, "raj: could not save session:", err)
		}
	}()
	if ctl {
		if err := a.StartControl(ctlAddr, ctlExec); err != nil {
			return err
		}
		// Printed before the alternate screen is entered, so a harness that
		// started raj can read the address from its output rather than guessing.
		fmt.Fprintln(os.Stderr, "raj: control", a.ControlPath())
		if tok := a.ControlToken(); tok != "" {
			// The token has to leave the process somehow, and stderr is where
			// the address already goes. Not a file: a file the driver could
			// read is a file on a filesystem the driver does not share, which
			// is the situation TCP exists for.
			fmt.Fprintf(os.Stderr, "raj: %s=%s\n", control.TokenEnv, tok)
			if _, address := control.ParseAddr(a.ControlPath()); !control.Loopback(address) {
				fmt.Fprintln(os.Stderr, "raj: warning — this port is open to the network, "+
					"not just to this machine. Anything holding the token can read and "+
					"write your unsaved buffers.")
			}
		}
	}
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
