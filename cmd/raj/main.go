// Command raj is a terminal editor that borrows VSCode's keybindings.
//
//	raj                       open the workspace in the file explorer
//	raj file.go               open a file
//	raj some/dir              open a directory as the workspace
//	raj --tab 4 file.go       set the indent width
//	raj --tabs file.go        indent with tabs where the file does not say
//	raj --control-addr tcp://0.0.0.0:7391
//	                          serve the control socket and this TCP address
//	                          for a driver in a container; a plain raj serves
//	                          no control listener
//	raj --standalone file.go  edit one file with no workspace, no session and no
//	                          control listener, for use as $EDITOR
//	raj --no-restore          start fresh instead of where you left off
//	raj ctl <cmd>             read and edit a running raj's buffers
//	raj --attach              attach to a running raj and render its workspace locally
//	raj --phone               attach as a client with the phone profile
//	raj --attach --workspace NAME
//	                          attach to the running daemon labelled NAME, from
//	                          any directory on this machine
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
	"io"
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
	ws "raj/internal/workspace"

	"raj/internal/safe"
)

// daemonFlag is package-scoped so a test can see the flag is registered. The
// rest are package-scoped too, so a test can print the usage the running
// editor serves without driving main.
var daemonFlag = flag.Bool("daemon", false, "run headless and serve the control socket for attached clients (alias for `daemon run`)")

// standaloneFlag is --standalone: a single-file throwaway editor for $EDITOR.
// It is package-scoped so a test can see the flag is registered and
// editorUsage prints it.
var standaloneFlag = flag.Bool("standalone", false, "edit one file with no workspace, no session and no control listener; for use as $EDITOR")

var (
	tab       = flag.Int("tab", 2, "indent width in spaces, and the display width of a tab")
	useTabs   = flag.Bool("tabs", false, "indent with tabs in files that have no indentation to detect")
	ctlAddr   = flag.String("control-addr", control.DefaultTCPAddr, "TCP control listener, and the address --attach dials; "+control.DefaultTCPAddr+" is the default")
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
	workspace = flag.String("workspace", "", "with --attach or --phone: attach to the running daemon labelled NAME (same host)")
)

// editorUsage writes the flag reference the help path prints. Go's default help
// spells every long option with a single dash, so it is rewritten through
// control's printer to the --name spelling every usage line and script uses.
// `--daemon` is omitted by name: it is a hidden alias for `raj daemon run`, and
// the standard flag package offers no way to mark a flag hidden.
func editorUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: raj [options] [file|dir]")
	control.PrintFlagUsage(w, flag.CommandLine, "daemon")
}

// The advertised daemon entry point is `raj daemon run`; --daemon stays
// registered and functional as its hidden alias, omitted from editorUsage.
func init() {
	flag.Usage = func() { editorUsage(flag.CommandLine.Output()) }
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
	flag.Parse()

	// daemonRun is `raj daemon run`; --daemon is the same mode and stays a
	// visible, documented alias. A daemon is a headless host, not a client.
	daemonMode := *daemonFlag || daemonRun

	// --attach, and --phone which implies it, make this process a client of a
	// running editor rather than a second editor of its own. Both are
	// ordinary flags now; the address comes from --control-addr when the user
	// passes it, or from discovery otherwise.
	clientMode := attachMode(*attach, *phone)

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
	// A standalone editor is one file in a throwaway directory: it is neither
	// a daemon host nor a client of one, and it must name exactly the file it
	// edits.
	if *standaloneFlag {
		if daemonMode {
			fail(errors.New("--standalone cannot be combined with --daemon"))
		}
		if clientMode {
			fail(errors.New("--standalone cannot be combined with --attach or --phone"))
		}
		if flag.NArg() != 1 {
			fail(errors.New("--standalone needs exactly one file argument"))
		}
	}
	// Only a flag the user actually typed may override a stored setting. A flag
	// left at its default is not a decision, and treating it as one would make
	// every setting unreachable behind the command line defaults; flag.Visit
	// visits exactly the flags that were set.
	var tabSet, tabsSet, wrapSet, ctrlAliasesSet, ctlAddrSet, workspaceSet bool
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
		case "control-addr":
			ctlAddrSet = true
		case "workspace":
			workspaceSet = true
		}
	})
	// Control is opt-in. A daemon serves the Unix socket and the default TCP
	// port because it exists to be driven; an editor serves only when
	// --control-addr asks for it, and then the socket listens alongside the
	// named address. A client serves nothing. A standalone editor is private
	// and serves nothing even when an address was named.
	ctlAddrs := controlAddrs(clientMode, daemonMode, ctlAddrSet, control.DefaultPath(), *ctlAddr)
	if *standaloneFlag {
		ctlAddrs = nil
	}
	// A flag left at its default is not a decision: only a --control-addr the
	// user actually typed names the daemon a client dials, and an unset flag
	// leaves the client to discover the local socket.
	attachAddr := clientAddr(*ctlAddr, ctlAddrSet)
	// --workspace names a running daemon to attach to from any directory: the
	// label resolves to the daemon's roots and control address, so the client
	// builds over the daemon's workspace rather than the launch directory.
	var attachRoots []string
	if workspaceSet {
		roots, addr, err := attachWorkspace(*workspace, clientMode, ctlAddrSet, flag.Args(), daemon.FindWorkspace)
		if err != nil {
			fail(err)
		}
		attachRoots, attachAddr = roots, addr
	}
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
		Roots:          attachRoots,
		Name:           *name,
		Phone:          *phone,
		CtrlAliases:    *ctrlAlias,
		CtrlAliasesSet: ctrlAliasesSet,
		Standalone:     *standaloneFlag,
	}
	if err := run(flag.Args(), opts, ctlAddrs, daemonMode); err != nil {
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

// controlAddrs is the listener set a run serves. Control is opt-in: a plain
// editor serves nothing, so nothing on the machine can read its buffers until
// it is asked. --daemon always serves the Unix socket and the default TCP
// address because it exists to be driven; an explicit --control-addr serves the
// socket alongside the address the user named. A client serves nothing.
func controlAddrs(clientMode, daemon, addrSet bool, sock, tcpAddr string) []string {
	if clientMode {
		return nil
	}
	if daemon || addrSet {
		return []string{sock, tcpAddr}
	}
	return nil
}

// clientAddr is the address an attach client dials. It is the explicit
// --control-addr only when the user passed it; a flag left at its default is
// not a decision, so an unset flag yields "" and discovery finds the socket.
func clientAddr(addr string, set bool) string {
	if !set {
		return ""
	}
	return addr
}

// attachWorkspace resolves --workspace to the roots and dial address of the
// running daemon labelled name. It is the whole decision, taken before any App
// exists: the refusals, the label lookup, and the address choice. find is
// injected (main passes daemon.FindWorkspace) so the matrix is unit-testable
// without a live daemon.
//
// The Unix socket is preferred over TCP: it needs no token, while a TCP-only
// daemon relies on RAJ_CONTROL_TOKEN. The roots are a copy, primary first.
func attachWorkspace(name string, clientMode, ctlAddrSet bool, args []string,
	find func(string) (daemon.Entry, bool, error)) (roots []string, addr string, err error) {
	if !clientMode {
		return nil, "", errors.New("--workspace needs --attach or --phone")
	}
	if ctlAddrSet {
		return nil, "", errors.New("--workspace and --control-addr are mutually exclusive")
	}
	if len(args) > 0 {
		return nil, "", errors.New("--workspace cannot be combined with a file or directory argument")
	}
	e, found, err := find(name)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", fmt.Errorf("no running daemon labelled %q; see `raj daemon list`", name)
	}
	if len(e.Roots) == 0 {
		return nil, "", fmt.Errorf("daemon %q has no workspace roots", name)
	}
	switch {
	case e.Socket != "":
		addr = e.Socket
	case e.TCP != "":
		addr = e.TCP
	default:
		return nil, "", fmt.Errorf("daemon %q has no control address", name)
	}
	return append([]string(nil), e.Roots...), addr, nil
}

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

func run(args []string, opts app.Options, ctlAddrs []string, headless bool) error {
	// An attach that named a workspace already resolved its roots: the client
	// builds over the daemon's workspace, so there is no argument to interpret.
	roots, path := opts.Roots, ""
	if len(roots) == 0 {
		var err error
		roots, path, err = resolve(args, opts.Standalone)
		if err != nil {
			return err
		}
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
		defer daemon.Clear(roots)
	}

	a := app.NewWithRoots(host, roots, opts)
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
		if err := a.StartControlAddrs(ctlAddrs); err != nil {
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
			if err := daemon.Record(roots, rec); err != nil {
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

// resolve splits the arguments into a workspace root set and at most one file
// to open. A regular file is the file; every other argument is a root
// directory. With no directory named the root set is the file's repository, or
// the working directory when there is no file. The roots are canonicalised and
// validated by workspace.New, so a nesting argument is an error.
//
// standalone is --standalone: the file's own directory is the single root, with
// no walk up to a repository, because a throwaway editor must not adopt the
// project around a file. It requires exactly one file and refuses a directory.
func resolve(args []string, standalone bool) (roots []string, file string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	if standalone {
		if len(args) != 1 || args[0] == "" {
			return nil, "", errors.New("--standalone needs exactly one file argument")
		}
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return nil, "", err
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return nil, "", fmt.Errorf("--standalone needs a file, not a directory: %s", args[0])
		}
		return []string{filepath.Dir(abs)}, abs, nil
	}
	var dirs []string
	for _, arg := range args {
		if arg == "" {
			continue
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, "", err
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			if file != "" {
				return nil, "", fmt.Errorf("cannot open two files at once: %s and %s", file, abs)
			}
			file = abs
			continue
		}
		dirs = append(dirs, abs)
	}
	if len(dirs) == 0 {
		if file != "" {
			dirs = append(dirs, filepath.Dir(file))
		} else {
			dirs = append(dirs, cwd)
		}
	}
	// Every directory roots at the repository the editor would choose, and
	// workspace.New rejects a set that nests one root inside another.
	for i, d := range dirs {
		dirs[i] = workspaceRoot(d)
	}
	rs, err := ws.New(dirs...)
	if err != nil {
		return nil, "", err
	}
	return rs.All(), file, nil
}

// workspaceRoot walks up to the nearest enclosing repository, falling back to the
// directory itself. Editing one file in a project should still give you the
// project to search and explore.
func workspaceRoot(dir string) string {
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
