// Package term owns raj's terminal state: raw mode, the Kitty keyboard
// protocol stack, focus reporting, and the OSC colour queries raj uses to
// inherit the host terminal's theme.
//
// raj must push the KKP flags itself rather than relying on the TUI framework.
// Ghostty's kkp_on keybind gate fires only on report_all (flag 8); the flags a
// framework pushes by default (disambiguate, event types, alternates) are not
// enough, and under them every cmd-chord goes back to Ghostty.
package term

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// KKP progressive-enhancement flags.
const (
	FlagDisambiguate = 1  // report unambiguous escape codes
	FlagEventTypes   = 2  // report press/repeat/release
	FlagAlternates   = 4  // report shifted and base-layout codepoints
	FlagReportAll    = 8  // report ALL keys as escape codes — the gate condition
	FlagText         = 16 // report associated text

	// DefaultFlags is what raj pushes. FlagReportAll is mandatory. FlagText
	// makes Insertable authoritative for dead keys and non-Latin layouts;
	// FlagAlternates is the fallback that makes shift+a type "A" without it.
	DefaultFlags = FlagDisambiguate | FlagEventTypes | FlagAlternates | FlagReportAll | FlagText
)

const (
	kkpPushFmt = "\x1b[>%du"
	kkpPop     = "\x1b[<u"
	focusOn    = "\x1b[?1004h"
	focusOff   = "\x1b[?1004l"
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"

	// Bracketed paste (DEC 2004). Enabling it is what makes a paste arrive as
	// one delimited payload instead of as synthetic keystrokes, and it is also
	// how raj advertises that it handles pastes itself — a terminal that sees
	// the mode set has no reason to warn about multi-line or unsafe pastes on
	// the application's behalf.
	pasteOn  = "\x1b[?2004h"
	pasteOff = "\x1b[?2004l"

	// The alternate screen is what makes raj behave like a full-screen
	// application rather than a very long shell command. Without it the editor
	// is written into the scrollback: previous shell output stays visible above
	// it, suspending leaves the last frame on screen, and any write that
	// reaches the bottom-right cell scrolls the terminal, which silently
	// invalidates every cell position the frame diff is tracking.
	altOn  = "\x1b[?1049h"
	altOff = "\x1b[?1049l"

	// Mouse reporting: button events (1000) in the SGR encoding (1006).
	//
	// 1000 rather than 1002 or 1003 deliberately. Those add motion reports,
	// which arrive on every cell the pointer crosses and are only worth their
	// traffic once drag-to-select exists; until then they would be decoded and
	// discarded thousands of times a minute. 1006 is what lifts the coordinate
	// ceiling: the original encoding packs them into single bytes offset by
	// 32, so column 224 is unrepresentable and the parameters cannot be told
	// apart from arbitrary text.
	//
	// The reason to ask at all is the wheel. Without reporting, a terminal
	// sends wheel notches to its own scrollback — which the alternate screen
	// is not part of, so scrolling silently did nothing. Enabling this means
	// the terminal stops handling the wheel and hands it over, which is only
	// an improvement if raj then acts on it.
	mouseOn  = "\x1b[?1000h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1000l"
)

// Terminal holds the state that must be unwound on exit, crash or suspend. A
// terminal left with KKP pushed swallows the host's own keybindings, so every
// exit path has to run Leave.
type Terminal struct {
	in      *os.File
	out     io.Writer
	saved   string // stty -g settings to restore
	flags   int
	live    bool
	profile profileSwitch
}

// New wraps a tty. in must be a real terminal; out is where control sequences
// are written (normally os.Stdout).
func New(in *os.File, out io.Writer) *Terminal {
	return &Terminal{in: in, out: out, flags: DefaultFlags}
}

// Enter puts the tty in raw mode, pushes the KKP flags and enables focus
// reporting. Pass 0 for flags to use DefaultFlags.
func (t *Terminal) Enter(flags int) error {
	if t.live {
		return nil
	}
	if flags == 0 {
		flags = DefaultFlags
	}
	if flags&FlagReportAll == 0 {
		return fmt.Errorf("term: flags %d omit FlagReportAll; cmd chords will not arrive", flags)
	}
	saved, err := t.stty("-g")
	if err != nil {
		return fmt.Errorf("term: read tty settings: %w", err)
	}
	if _, err := t.stty("raw", "-echo"); err != nil {
		return fmt.Errorf("term: enter raw mode: %w", err)
	}
	t.saved, t.flags, t.live = strings.TrimSpace(saved), flags, true
	t.profile = newProfileSwitch()
	// Before the alt screen, so the profile change applies to the frames raj
	// is about to draw.
	t.profile.enter(t.out)
	fmt.Fprint(t.out, altOn)
	fmt.Fprint(t.out, hideCursor)
	fmt.Fprintf(t.out, kkpPushFmt, flags)
	fmt.Fprint(t.out, focusOn)
	fmt.Fprint(t.out, pasteOn)
	fmt.Fprint(t.out, mouseOn)
	return nil
}

// Leave unwinds everything Enter did. Safe to call on a terminal that never
// entered, and safe to call twice.
func (t *Terminal) Leave() {
	if t == nil || !t.live {
		return
	}
	// Reporting off before the alt screen goes away, so the terminal is
	// handling its own wheel again by the time the shell is visible.
	fmt.Fprint(t.out, mouseOff)
	fmt.Fprint(t.out, pasteOff)
	fmt.Fprint(t.out, focusOff)
	fmt.Fprint(t.out, kkpPop)
	fmt.Fprint(t.out, showCursor)
	fmt.Fprint(t.out, altOff)
	t.profile.leave(t.out)
	if t.saved != "" {
		t.stty(t.saved)
	}
	t.live = false
}

// Flags reports the pushed flag set (0 when not entered).
func (t *Terminal) Flags() int {
	if !t.live {
		return 0
	}
	return t.flags
}

// Read reads raw bytes from the tty.
func (t *Terminal) Read(p []byte) (int, error) { return t.in.Read(p) }

func (t *Terminal) stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = t.in
	out, err := cmd.Output()
	return string(out), err
}

// WindowSize reports the terminal size in character cells, falling back to a
// conventional 80x24 when the ioctl fails (a pipe, or a terminal that does not
// answer). Callers should treat the fallback as usable rather than fatal.
func WindowSize(f *os.File) (cols, rows int) {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	// ioctl rather than shelling out to `stty size`: dragging a window edge
	// produces a burst of SIGWINCH, and forking a process per resize would
	// stall redraws exactly when they matter most.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}
