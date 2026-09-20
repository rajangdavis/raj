// Command raj is a terminal editor that borrows VSCode's keybindings.
//
//	raj                       open the workspace in the file explorer
//	raj file.go               open a file
//	raj some/dir              open a directory as the workspace
//	raj --tab 4 file.go       set the indent width
//	raj --tabs file.go        indent with tabs where the file does not say
//	raj --control             listen on a control socket for buffer edits
//	raj --control-addr tcp://0.0.0.0:7391
//	                          also listen on TCP, for a driver in a container
//	raj --no-restore          start fresh instead of where you left off
//	raj ctl <cmd>             read and edit a running raj's buffers
//	raj --attach              attach to a running raj and render its workspace locally
//	raj --phone               attach as a client with the phone profile
//	raj --daemon              run headless and serve the control socket; a
//	                          hidden alias for `daemon run`
//	raj daemon start          serve the workspace headless in the background
//	raj daemon stop           stop the background daemon; a no-op when none runs
//	raj daemon status         report whether the daemon runs, and its addresses
//	raj --ctrl-aliases        add ctrl+<key> aliases for super+<key> bindings
//	raj --config ghostty      print Ghostty keybindings to install
//	raj --config iterm2       print an iTerm2 dynamic profile
//	raj --config iterm2 --install
//	                          write it where the terminal reads it
//	raj --keys                print the keybinding reference as markdown
//	raj --probe               check which chords this terminal delivers
//	raj --probe --checklist   walk every binding and emit a measured keymap
//	raj --probe --motions     measure touch and trackpad motions
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"raj/internal/app"
	"raj/internal/control"
	"raj/internal/daemon"
	"raj/internal/keys"
	"raj/internal/probe"
	"raj/internal/termconf"
	"raj/internal/ui"

	"raj/internal/safe"
)

// daemonFlag is package-scoped so a test can see the flag is registered; the
// other flags stay local to main because only main reads them.
var daemonFlag = flag.Bool("daemon", false, "run headless and serve the control socket for attached clients (alias for `daemon run`)")

// The advertised daemon entry point is `raj daemon run`; --daemon is kept as a
// hidden alias so existing invocations keep working. MarkHidden is reached
// through an interface because the editor also builds against Go versions
// before it existed; where it is absent the flag stays visible rather than the
// build failing.
func init() {
	if h, ok := any(flag.CommandLine).(interface{ MarkHidden(string) error }); ok {
		_ = h.MarkHidden("daemon")
	}
}

func main() {
	// `raj ctl ...` is a different program sharing a binary: it talks to a
	// running editor over the control socket and never opens a terminal. It is
	// handled before flag.Parse because its flags are its own.
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(control.CLI(os.Args[2:], os.Stdout, os.Stderr))
	}
	// `raj daemon ...` manages the background host. `daemon run` is the
	// foreground worker itself, so it is stripped and the invocation continues
	// into the ordinary flag parsing with daemon mode forced on; the other
	// subcommands are their own program and never open a terminal. Handled
	// before flag.Parse for the same reason as ctl: their flags are theirs.
	daemonRun := false
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		if len(os.Args) > 2 && os.Args[2] == "run" {
			daemonRun = true
			os.Args = append(os.Args[:1], os.Args[3:]...)
		} else {
			os.Exit(daemon.CLI(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	var (
		tab       = flag.Int("tab", 2, "indent width in spaces, and the display width of a tab")
		useTabs   = flag.Bool("tabs", false, "indent with tabs in files that have no indentation to detect")
		ctl       = flag.Bool("control", false, "listen on a Unix socket for buffer reads and edits")
		ctlPath   = flag.String("control-socket", "", "path for the control socket; implies it")
		ctlAddr   = flag.String("control-addr", "", "also listen on tcp://host:port, or set the socket path; implies --control")
		ctlExec   = flag.Bool("control-exec", false, "with --control-addr: let a remote driver run commands on this machine")
		noRestore = flag.Bool("no-restore", false, "do not reopen the previous session")
		wrap      = flag.Bool("wrap", true, "wrap long lines; --wrap=false scrolls horizontally instead")
		configFor = flag.String("config", "", "emit keybindings: ghostty, ghostty-linux, or iterm2")
		install   = flag.Bool("install", false, "with --config: write the file where the terminal reads it, instead of to stdout")
		keyDoc    = flag.Bool("keys", false, "print the keybinding reference as markdown")
		runProbe  = flag.Bool("probe", false, "report what chords this terminal delivers")
		checklist = flag.Bool("checklist", false, "with --probe: walk every binding in order")
		motions   = flag.Bool("motions", false, "with --probe: measure touch and trackpad motions")
		phone     = flag.Bool("phone", false, "attach with the phone profile: it implies --attach")
		attach    = flag.Bool("attach", false, "attach to a running raj and render its workspace locally")
		name      = flag.String("name", "", "with --attach: name this client saved tab view, so clients keep separate views")
		ctrlAlias = flag.Bool("ctrl-aliases", false, "add ctrl+<key> aliases for super+<key> bindings (implied by --phone)")
		kkpFlags  = flag.Int("kkp", 0, "with --probe: KKP flags to push (0 = raj's own)")
	)
	flag.Parse()

	// daemonRun is `raj daemon run`; --daemon is the same mode and stays a
	// visible, documented alias. A daemon is a headless host, not a client.
	daemonMode := *daemonFlag || daemonRun

	// --attach, and --phone which implies it, make this process a client of a
	// running editor rather than a second editor of its own. Both are
	// ordinary flags now; the address comes from the address flags, or from
	// discovery when they are empty.
	clientMode := attachMode(*attach, *phone)
	attachAddr := *ctlPath
	if *ctlAddr != "" {
		attachAddr = *ctlAddr
	}

	if *keyDoc {
		fmt.Print(keys.Doc())
		return
	}
	if *configFor != "" {
		target, err := termconf.ParseTarget(*configFor)
		if err != nil {
			fail(err)
		}
		if *install {
			if err := installConfig(target); err != nil {
				fail(err)
			}
			return
		}
		fmt.Print(termconf.Render(target))
		return
	}
	if *install {
		fail(errors.New("--install needs --config to say which terminal"))
	}
	if *runProbe {
		// The probe lives behind a flag on raj rather than in its own binary so
		// that testing a terminal needs no second build: whatever raj you are
		// running is the decoder being measured.
		if err := probe.Run(*kkpFlags, *checklist, *motions); err != nil {
			fail(err)
		}
		return
	}
	// A control session always has a Unix socket: it is the local trust
	// anchor, and the one place a script on this machine can read the token
	// out of a running session. A `tcp://` address adds a listener for a
	// driver that does not share the filesystem rather than replacing the
	// socket, so the two can drive one session at once. `--control-socket`,
	// or `--control-addr` naming a path, chooses where the socket lives.
	sock := *ctlPath
	if *ctlAddr != "" && !control.IsTCP(*ctlAddr) {
		sock = *ctlAddr
	}
	var ctlAddrs []string
	if !clientMode && (daemonMode || *ctl || sock != "" || *ctlAddr != "") {
		if sock == "" {
			sock = control.DefaultPath()
		}
		ctlAddrs = append(ctlAddrs, sock)
	}
	if !clientMode && control.IsTCP(*ctlAddr) {
		ctlAddrs = append(ctlAddrs, *ctlAddr)
	}
	// Only a flag the user actually typed may override a stored setting. A flag
	// left at its default is not a decision, and treating it as one would make
	// every setting unreachable behind the command line defaults; flag.Visit
	// visits exactly the flags that were set.
	var tabSet, tabsSet, wrapSet, ctrlAliasesSet bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "tab":
			tabSet = true
		case "tabs":
			tabsSet = true
		case "wrap":
			wrapSet = true
		case "ctrl-aliases":
			ctrlAliasesSet = true
		}
	})
	// --phone implies the ctrl aliases unless --ctrl-aliases was passed
	// explicitly: a phone has no super key, but a keyboard attached to one might,
	// and the explicit flag is how that choice is made. NewWithOptions resolves
	// the implication from these three fields.
	opts := app.Options{
		TabWidth:       *tab,
		TabWidthSet:    tabSet,
		Tabs:           *useTabs,
		TabsSet:        tabsSet,
		Wrap:           *wrap,
		WrapSet:        wrapSet,
		NoRestore:      *noRestore,
		Attach:         clientMode,
		AttachAddr:     attachAddr,
		Name:           *name,
		Phone:          *phone,
		CtrlAliases:    *ctrlAlias,
		CtrlAliasesSet: ctrlAliasesSet,
	}
	if err := run(flag.Arg(0), opts, ctlAddrs, *ctlExec, daemonMode); err != nil {
		fail(err)
	}
}

// installConfig writes a config and says what the user still has to do.
//
// Printing where the file went is the point: an install that succeeds silently
// is indistinguishable from one that wrote somewhere nothing reads, which is
// the failure this whole path exists to end.
func installConfig(target termconf.Target) error {
	path, err := termconf.Install(target)
	if err != nil {
		return err
	}
	fmt.Println("raj: wrote", path)
	switch target {
	case termconf.ITerm2:
		fmt.Println("raj: iTerm2 picks this up without a restart.")
	default:
		// A generated file nothing includes is the other way this fails
		// silently, and it is the one an install can check for free.
		included, err := termconf.IncludedBy(path)
		if err == nil && !included {
			fmt.Printf("raj: add this line to your Ghostty config, then reload it with cmd+shift+,:\n\n    config-file = %s\n", path)
			return nil
		}
		fmt.Println("raj: reload Ghostty with cmd+shift+, to pick it up.")
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "raj:", err)
	os.Exit(1)
}

// attachMode reports whether this invocation is a client of a running editor.
// --phone implies it: the phone profile exists to read a workspace from a
// terminal that cannot drive the local editor, which is the attach story.
func attachMode(attach, phone bool) bool { return attach || phone }

// runHost builds the host a run drives. --daemon is headless: no terminal, no
// raw mode, frames discarded, and the idle tick as its only input, because a
// daemon is driven over the control socket. The ordinary path is the native
// terminal host, unchanged.
func runHost(headless bool, in, out *os.File) (ui.Host, error) {
	if headless {
		return ui.NewHeadlessHost(), nil
	}
	return ui.NewNativeHost(in, out, 150*time.Millisecond)
}

// signalShutdown turns a daemon termination signal into a graceful close: the
// first closes the host, which ends app.Run and lets its deferred save and the
// caller deferreds run; the second forces an exit so a hung shutdown cannot
// trap the process. The caller owns ch and has already called signal.Notify on
// it; the returned stop removes the handler, closes ch, and waits for the
// watcher, so the deferred host.Close cannot race a shutdown in flight.
//
// exit is injected so a test can see the second-signal decision without ending
// the test process. Off the daemon path nothing is installed.
func signalShutdown(daemon bool, ch chan os.Signal, shutdown func(), exit func(int)) (stop func()) {
	if !daemon || ch == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		seen := 0
		for range ch {
			seen++
			if seen == 1 {
				shutdown()
				continue
			}
			exit(1)
			return
		}
	}()
	return func() {
		signal.Stop(ch)
		close(ch)
		<-done
	}
}

func run(path string, opts app.Options, ctlAddrs []string, ctlExec, headless bool) error {
	root, path, err := resolve(path)
	if err != nil {
		return err
	}

	host, err := runHost(headless, os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	// Leave the terminal usable whatever happens, including a panic: a raj that
	// exits without popping the KKP flags leaves a shell where cmd+w does
	// nothing and there is no obvious way out.
	defer host.Close()
	// And on a panic anywhere else. A panic on a background goroutine — the
	// input decoder, the resize watcher, the tokeniser — skips every deferred
	// call in the program, so without this the trace is printed into an
	// alternate screen the user is then stuck in.
	safe.OnPanic(func() { host.Close() })
	// A daemon has no terminal to close and no stdin to read, so its shutdown is
	// a signal: the first asks the host to close, which ends Run and lets the
	// deferred save and listener shutdown run; a second forces exit. The
	// terminal host keeps its own fatal-signal handling untouched.
	if headless {
		sig := make(chan os.Signal, 2)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer signalShutdown(true, sig, func() { _ = host.Close() }, os.Exit)()
		// The daemon record outlives the run: Clear is registered before the
		// session save and store close, so it runs after them and a reader
		// never sees the record vanish ahead of the final save.
		defer daemon.Clear(root)
	}

	a := app.NewWithOptions(host, root, opts)
	// The ctrl-alias builder refuses to overwrite an existing ctrl binding, so
	// a phone loses the actions whose ctrl key is already taken. Say how many:
	// silently missing is how a chord looks broken rather than unavailable.
	if report := a.CtrlAliasReport(); report != "" {
		fmt.Fprintln(os.Stderr, "raj: ctrl aliases: "+report)
	}
	// The workspace state database is released on the way out, whatever the
	// exit path. Nil-safe, so a store that never opened costs nothing.
	defer a.CloseState()
	// A stale terminal config fails invisibly — the chord reaches the
	// terminal, the terminal does what it always did, and raj never hears it.
	// The status line is the only place that can be said, and it is said only
	// for a file raj generated and can prove is out of date.
	if stale := termconf.StaleTargets(); len(stale) > 0 {
		a.Notice(fmt.Sprintf("%s is out of date — run: raj --config %s --install",
			strings.Join(stale, " and "), termconf.HostTarget()))
	}
	// A client restores nothing from this machine and saves nothing back: the
	// daemon owns the workspace, and the local session belongs to a local
	// editor. Instead it loads the daemon document and arms a watch before the
	// first frame.
	if opts.Attach {
		a.StartClient()
		defer a.CloseClient()
	} else {
		// Before any file named on the command line, so an explicitly
		// requested file ends up focused rather than buried under restored
		// tabs.
		a.RestoreSession()
		defer func() {
			if err := a.SaveSession(); err != nil {
				fmt.Fprintln(os.Stderr, "raj: could not save session:", err)
			}
		}()
	}
	if len(ctlAddrs) > 0 {
		if err := a.StartControlAddrs(ctlAddrs, ctlExec); err != nil {
			return err
		}
		// Printed before the alternate screen is entered, so a harness that
		// started raj can read the addresses from its output rather than
		// guessing. Every address is printed, because a session can listen on
		// a socket and a port at once.
		paths := a.ControlPaths()
		for _, p := range paths {
			fmt.Fprintln(os.Stderr, "raj: control", p)
		}
		// A daemon records where it is listening and the token a TCP client
		// must present, so `daemon status` and a client can find it without
		// scraping stderr. start wrote an early pidfile; this completes it.
		if headless {
			var rec daemon.State
			rec.PID = os.Getpid()
			for _, p := range paths {
				if control.IsTCP(p) {
					rec.TCP = p
				} else {
					rec.Socket = p
				}
			}
			rec.Token = a.ControlToken()
			if err := daemon.Record(root, rec); err != nil {
				fmt.Fprintln(os.Stderr, "raj: daemon record:", err)
			}
		}
		if tok := a.ControlToken(); tok != "" {
			// The token has to leave the process somehow, and stderr is where
			// the addresses already go. Not a file: a file the driver could
			// read is a file on a filesystem the driver does not share, which
			// is the situation TCP exists for. `raj ctl token` reads it back
			// out of the running process over the local socket.
			fmt.Fprintf(os.Stderr, "raj: %s=%s\n", control.TokenEnv, tok)
			for _, p := range paths {
				if !control.IsTCP(p) {
					continue
				}
				if _, address := control.ParseAddr(p); !control.Loopback(address) {
					fmt.Fprintln(os.Stderr, "raj: warning — this port is open to the network, "+
						"not just to this machine. Anything holding the token can read and "+
						"write your unsaved buffers.")
					break
				}
			}
		}
	}
	if path != "" && !opts.Attach {
		a.OpenFile(path)
	}
	// Start the servers for the languages already open — restored tabs and any
	// file named on the command line — off the event thread, so the first
	// request does not wait on a cold handshake. Not in Run: tests run that and
	// would spawn a server in the test process. A client warms nothing: it
	// opened no local file, and the daemon owns the documents.
	if !opts.Attach {
		a.WarmServers()
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
