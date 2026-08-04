// Package probe verifies empirically what a terminal delivers for each chord
// raj owns, and whether it hands those chords back on suspend and focus loss.
//
// It runs on the same internal/keys and internal/term packages raj uses, so a
// passing probe is evidence about raj rather than about a parallel decoder. It
// is terminal-agnostic: what arrives depends on the terminal's own bindings and
// on whether it speaks the Kitty protocol, which is exactly what it is for
// finding out.
package probe

import (
	"fmt"
	"os"
	"strings"

	"raj/internal/keys"
	"raj/internal/term"
)

// Run starts the probe. checklist walks every binding in order and prints a
// measured keymap at the end; otherwise it reports each event as it arrives.
// flags is the KKP flag set to push; 0 uses raj's own.
func Run(flags int, checklist bool) error {
	if flags == 0 {
		flags = term.DefaultFlags
	}
	return run(flags, checklist)
}

func run(flags int, useList bool) error {
	t := term.New(os.Stdin, os.Stdout)
	// -flags 1 is a deliberate test of the gate, so bypass Enter's guard.
	if flags&term.FlagReportAll == 0 {
		fmt.Printf("NOTE: flags %d omit report_all; cmd chords should stay with the terminal.\r\n", flags)
	}
	if err := enter(t, flags); err != nil {
		return err
	}
	defer t.Leave()
	defer t.HandleFatalSignals()()

	banner(flags)
	query(t)

	var list *checklist
	if useList {
		list = newChecklist()
		list.prompt()
	}

	km := keys.NewKeymap()
	var buf []byte
	chunk := make([]byte, 1024)
	for {
		n, err := t.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			return nil
		}
		for {
			e, used := keys.Parse(buf)
			if used == 0 {
				break
			}
			buf = buf[used:]
			if handle(t, e, km, list) {
				if list != nil {
					t.Leave()
					fmt.Print(list.report())
				}
				return nil
			}
		}
	}
}

// enter pushes flags directly when report_all is absent, so the gate itself can
// be tested. Everywhere else raj goes through Terminal.Enter's guard.
func enter(t *term.Terminal, flags int) error {
	if flags&term.FlagReportAll != 0 {
		return t.Enter(flags)
	}
	if err := t.Enter(term.DefaultFlags); err != nil {
		return err
	}
	fmt.Printf("\x1b[<u\x1b[>%du", flags)
	return nil
}

func query(t *term.Terminal) {
	fmt.Print(term.QueryKKPFlags)
	t.QueryTheme(4)
	fmt.Print("\x1b[c")
}

func banner(flags int) {
	fmt.Printf("keyprobe — KKP flags pushed: %d (report_all=%v)\r\n", flags, flags&term.FlagReportAll != 0)
	fmt.Print("ctrl+z suspends (tests the handoff back to the terminal),\r\n")
	fmt.Print("ctrl+c quits, ctrl+r re-queries state.\r\n")
	fmt.Print(strings.Repeat("-", 72) + "\r\n")
}

// handle prints one event and applies the control chords. Returns true to quit.
func handle(t *term.Terminal, e keys.Event, km *keys.Keymap, list *checklist) bool {
	switch e.Kind {
	case keys.FocusIn:
		fmt.Print("--- FOCUS IN  (raj should re-take the bindings here)\r\n")
		return false
	case keys.FocusOut:
		fmt.Print("--- FOCUS OUT (the terminal should own the bindings here)\r\n")
		return false
	case keys.KKPFlags, keys.OSCReply, keys.OtherReply:
		fmt.Printf("    reply %s\r\n", keys.Escape(e.Raw))
		return false
	case keys.PasteEvent:
		// Worth reporting on its own line: a terminal that has not honoured
		// mode 2004 shows the payload here as a stream of key events instead,
		// which is the whole difference and is otherwise invisible.
		fmt.Printf("--- PASTE %d bytes, %d line(s)\r\n",
			len(e.Text), strings.Count(e.Text, "\n")+1)
		return false
	case keys.Partial:
		return false
	}
	// Filter releases FIRST. Under flag 2 every chord reports press and
	// release, so a control chord checked before this fires twice.
	if e.Type == keys.Release {
		if list == nil {
			fmt.Printf("    %-18s %-24s [RELEASE — raj must ignore]\r\n", keys.Escape(e.Raw), e.Chord())
		}
		return false
	}
	if e.IsModifierKey() {
		return false
	}

	switch action := km.Lookup(keys.Global, e.Chord()); {
	case action == keys.Quit:
		return true
	case action == keys.Suspend:
		suspend(t)
		return false
	case e.Chord() == "ctrl+n" && list != nil:
		list.skip()
		return false
	case e.Chord() == "ctrl+r":
		query(t)
		return false
	}

	if list != nil {
		list.record(e)
		return false
	}
	action := km.Lookup(keys.Global, e.Chord())
	label := "unmapped"
	if action != keys.None {
		label = string(action)
	} else if txt := e.Insertable(); txt != "" {
		label = fmt.Sprintf("text %q", txt)
	}
	fmt.Printf("    %-18s %-24s %-8s %s\r\n",
		keys.Escape(e.Raw), e.Chord(), keys.TypeName(e.Type), label)
	return false
}

func suspend(t *term.Terminal) {
	fmt.Print("\r\n--- suspending: KKP popped, the terminal owns the keys.\r\n")
	fmt.Print("--- Try cmd+w now, then `fg` to come back.\r\n")
	err := t.Suspend(func() {
		fmt.Print("\r\n--- resumed: KKP re-pushed, raj owns the keys again.\r\n")
	})
	if err != nil {
		fmt.Printf("--- suspend failed: %v\r\n", err)
		return
	}
	query(t)
}
