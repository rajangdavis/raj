// Package ui is the seam between raj and whatever is driving the terminal.
//
// raj renders into a Screen — a grid of cells — and reads Events from a Host.
// Nothing above this package knows whether the pixels reach a real terminal, a
// TUI framework, or a test harness. Three hosts implement it:
//
//	NativeHost  raw terminal via internal/term and internal/keys
//	FakeHost    headless: scripted events in, captured frames out
//	(adapter)   a framework such as Bubbletea, which can paint a Screen by
//	            encoding it to the string its View returns
//
// The seam is deliberately the screen rather than the model. A model-level
// interface would drag a framework's update semantics into raj's core; a cell
// grid is a value type any of them can paint, and it is what makes the headless
// host possible.
package ui

import "raj/internal/keys"

// Event is anything the host delivers to the application.
type Event interface{ isEvent() }

// Key is a decoded keypress. Releases and bare modifier presses are filtered by
// the host, so anything arriving here is meant to be acted on.
type Key struct{ keys.Event }

// Resize reports a new terminal size in character cells.
type Resize struct{ Cols, Rows int }

// Focus reports the terminal gaining or losing focus. raj uses this to dim
// inactive panes and pause agent output; it does NOT need to release
// keybindings, because Ghostty hands those back per-surface on its own.
type Focus struct{ In bool }

// Paste is a bracketed-paste payload. It arrives whole rather than as
// individual keys, so a large paste is one buffer edit instead of thousands.
type Paste struct{ Text string }

// Tick drives time-based work: the coalescing window that batches streaming
// agent hunks, cursor blink, and status refreshes.
type Tick struct{ Count uint64 }

// Suspended reports that the process was backgrounded and has resumed. The
// terminal has already been rebuilt; the application should redraw everything.
type Suspended struct{}

// Quit asks the loop to stop. Hosts emit it on unrecoverable input errors.
type Quit struct{}

func (Key) isEvent()       {}
func (Resize) isEvent()    {}
func (Focus) isEvent()     {}
func (Paste) isEvent()     {}
func (Tick) isEvent()      {}
func (Suspended) isEvent() {}
func (Quit) isEvent()      {}

// Host drives one terminal-shaped surface.
//
// Implementations must be safe to Close twice, because both the ordinary exit
// path and the signal handler will try.
type Host interface {
	// Events delivers input. The channel closes when the host shuts down.
	Events() <-chan Event

	// Size is the current surface size in cells.
	Size() (cols, rows int)

	// Present paints a frame. Hosts are expected to diff against the previous
	// frame and emit only what changed.
	Present(*Screen) error

	// Invalidate discards the host's record of what is on screen, so the next
	// Present repaints everything from a cleared surface.
	//
	// Frame diffing assumes the terminal still shows the last frame. That
	// assumption breaks whenever something outside raj touches the screen:
	// a resize leaves the contents outside the old geometry undefined, a
	// resume returns from a shell that has been drawing, and a terminal
	// regaining focus may have been repainted by the window system. Trusting
	// the stale frame in those cases leaves residue in exactly the cells the
	// diff decides are already correct.
	Invalidate()

	// Suspend backgrounds the process, restoring the terminal first and
	// rebuilding it on resume. A Suspended event follows.
	Suspend() error

	// Theme reports the host terminal's colours so raj can inherit the user's
	// configuration rather than shipping a palette. Zero value means unknown,
	// and callers should fall back to default-coloured output.
	Theme() Theme

	// SetClipboard writes to the system clipboard via OSC 52. Terminals may
	// refuse — Ghostty allows it by default, others require opt-in — so callers
	// must keep their own copy rather than treating this as storage.
	SetClipboard(text string)

	Close() error
}

// Theme is the host terminal's own colours, queried at startup.
type Theme struct {
	Background Color
	Foreground Color
	Known      bool
}

// Dark reports whether to pick dark-background syntax colours. It defaults to
// true when the terminal did not answer, because a dark default is far more
// often right and is the less painful failure.
func (t Theme) Dark() bool {
	if !t.Known {
		return true
	}
	r, g, b, ok := t.Background.RGB()
	if !ok {
		return true
	}
	return (0.299*float64(r)+0.587*float64(g)+0.114*float64(b))/255 < 0.5
}
