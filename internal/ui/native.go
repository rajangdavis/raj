package ui

import (
	"encoding/base64"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"raj/internal/keys"
	"raj/internal/term"
)

// NativeHost drives a real terminal directly, owning the render loop rather
// than delegating to a framework. raj already replaced the input layer —
// internal/term pushes the KKP flags and internal/keys decodes them — so a
// framework would only be supplying the loop and a diffing renderer, which is
// what this file is.
type NativeHost struct {
	t      *term.Terminal
	out    *os.File
	events chan Event
	prev   *Screen

	mu     sync.Mutex
	cols   int
	rows   int
	theme  Theme
	closed bool
	dirty  bool
	stop   func()
}

// NewNativeHost enters raw mode and starts reading input. tickRate drives Tick
// events; pass 0 to disable them.
func NewNativeHost(in, out *os.File, tickRate time.Duration) (*NativeHost, error) {
	t := term.New(in, out)
	if err := t.Enter(0); err != nil {
		return nil, err
	}
	h := &NativeHost{t: t, out: out, events: make(chan Event, 256)}
	h.stop = t.HandleFatalSignals()
	h.readSize()
	h.watchResize()
	t.QueryTheme()
	go h.readLoop()
	if tickRate > 0 {
		go h.tickLoop(tickRate)
	}
	return h, nil
}

func (h *NativeHost) Events() <-chan Event { return h.events }

func (h *NativeHost) Size() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cols, h.rows
}

func (h *NativeHost) Theme() Theme {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.theme
}

// Invalidate forces the next Present to repaint from a cleared screen.
func (h *NativeHost) Invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dirty = true
}

// Present writes only what changed since the previous frame, or everything
// after an Invalidate.
func (h *NativeHost) Present(s *Screen) error {
	h.mu.Lock()
	dirty := h.dirty
	h.dirty = false
	h.mu.Unlock()

	out := ""
	if dirty {
		// Erase before the full repaint. Writing every cell is not enough on
		// its own: a terminal that has been resized or written to by another
		// program may hold content the new frame never addresses.
		out = "\x1b[2J"
		h.prev = nil
	}
	out += s.Diff(h.prev)
	// Position the terminal's own caret last, so it ends up where the user is
	// typing rather than wherever the final cell happened to be written.
	if s.CursorShown {
		out += cursorAt(s.CursorX, s.CursorY) + barCursor + showCursor
	} else if h.prev == nil || h.prev.CursorShown {
		out += hideCursor
	}
	h.prev = s.Clone()
	if out == "" {
		return nil
	}
	_, err := h.out.WriteString(out)
	return err
}

// Suspend hands the terminal back, stops the process, and forces a full
// repaint on resume — the previous frame is meaningless once a shell has been
// writing to the screen.
func (h *NativeHost) Suspend() error {
	err := h.t.Suspend(func() {
		h.Invalidate()
		h.t.QueryTheme()
	})
	h.emit(Suspended{})
	return err
}

const (
	// barCursor is DECSCUSR 5: a blinking vertical bar, the I-beam shape.
	barCursor  = "\x1b[5 q"
	showCursor = "\x1b[?25h"
	hideCursor = "\x1b[?25l"
)

func cursorAt(x, y int) string {
	return "\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(x+1) + "H"
}

// SetClipboard writes an OSC 52 selection payload.
func (h *NativeHost) SetClipboard(text string) {
	h.out.WriteString("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
}

func (h *NativeHost) Close() error {
	h.mu.Lock()
	already := h.closed
	h.closed = true
	h.mu.Unlock()
	if already {
		return nil
	}
	if h.stop != nil {
		h.stop()
	}
	h.t.Leave()
	return nil
}

// readLoop decodes bytes into events. Key releases and bare modifier presses
// are dropped here so nothing above the host ever has to remember to filter
// them — under KKP flag 2 every chord reports twice, and acting on both applies
// every edit twice.
func (h *NativeHost) readLoop() {
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := h.t.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			h.emit(Quit{})
			return
		}
		for {
			e, used := keys.Parse(buf)
			if used == 0 {
				break
			}
			buf = buf[used:]
			h.dispatch(e)
		}
	}
}

func (h *NativeHost) dispatch(e keys.Event) {
	switch e.Kind {
	case keys.FocusIn:
		h.Invalidate()
		h.emit(Focus{In: true})
	case keys.FocusOut:
		h.emit(Focus{In: false})
	case keys.OSCReply:
		h.applyThemeReply(e.Raw)
	case keys.KeyEvent:
		if e.Type != keys.Release && !e.IsModifierKey() {
			h.emit(Key{e})
		}
	}
}

func (h *NativeHost) applyThemeReply(raw []byte) {
	kind, _, c, ok := term.ParseColorReply(raw)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch kind {
	case 10:
		h.theme.Foreground = RGBColor(c.R, c.G, c.B)
	case 11:
		h.theme.Background = RGBColor(c.R, c.G, c.B)
	}
	h.theme.Known = true
}

func (h *NativeHost) tickLoop(d time.Duration) {
	tk := time.NewTicker(d)
	defer tk.Stop()
	var n uint64
	for range tk.C {
		h.mu.Lock()
		closed := h.closed
		h.mu.Unlock()
		if closed {
			return
		}
		n++
		h.emit(Tick{Count: n})
	}
}

func (h *NativeHost) watchResize() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			h.readSize()
			h.Invalidate()
			cols, rows := h.Size()
			h.emit(Resize{Cols: cols, Rows: rows})
		}
	}()
}

func (h *NativeHost) readSize() {
	cols, rows := term.WindowSize(h.out)
	h.mu.Lock()
	h.cols, h.rows = cols, rows
	h.mu.Unlock()
}

// emit drops events rather than blocking when the consumer has stalled: a
// wedged render loop must not also wedge the input reader, or ctrl+c stops
// working and the only way out is another terminal.
func (h *NativeHost) emit(e Event) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return
	}
	select {
	case h.events <- e:
	default:
	}
}
