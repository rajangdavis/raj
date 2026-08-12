package ui

import (
	"encoding/base64"
	"io"
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
	t   *term.Terminal
	out *os.File
	// w is where frames are written. Separate from out, which stays an
	// *os.File because the size query needs the descriptor, so that tests can
	// substitute a writer that fails or writes short.
	w      io.Writer
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
	h := &NativeHost{t: t, out: out, w: out, events: make(chan Event, 256)}
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

// Post injects an event from a background goroutine. It is emit under another
// name — the drop-if-full behaviour is the same — and exists as a separate
// method because the contract differs: emit is the input path, where a dropped
// event is a bug worth fixing, and Post is a notification whose payload lives
// elsewhere, where a drop costs only the wait for the next tick.
func (h *NativeHost) Post(e Event) { h.emit(e) }

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

	cols, rows := h.trueSize()

	// A frame built for a different terminal size must not be written.
	//
	// SIGWINCH updates the size from its own goroutine, so during a drag the
	// frame in hand can be sized for a terminal that no longer exists. Writing
	// it anyway is what produces the thrash: a frame WIDER than the terminal
	// wraps at the real right edge, pushing every subsequent row down and
	// scrolling the screen, while a frame NARROWER only leaves stale cells to
	// the right — which is why growing a pane looks almost clean and shrinking
	// one does not.
	//
	// Dropping the frame costs nothing: the size is already updated, so the
	// next Draw builds at the correct one. dirty stays set, so that frame is a
	// full repaint rather than a diff against a screen that was never written.
	//
	// The comparison is against the terminal's ACTUAL size, not the cached one.
	// The cache is only refreshed by SIGWINCH, so after a suspend — where raj
	// cannot service signals at all while stopped — it can disagree with
	// reality, and a guard trusting it would wave the wrong-sized frame
	// through. That is why the thrash survived on the resume path.
	if sc, sr := s.Size(); sc != cols || sr != rows {
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
		h.prev = nil
		return nil
	}

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
	if out == "" {
		h.prev = s.Clone()
		return nil
	}
	// Record the frame only once every byte of it has reached the terminal.
	//
	// Setting prev first was the bug behind the scroll thrash: a short write
	// left the screen holding part of the previous frame while prev claimed
	// the whole new one had landed, so every later diff skipped exactly the
	// cells that never arrived. The result is two frames interleaved character
	// by character, clearing itself at the next full repaint — which is what
	// made it look transient rather than like corrupted state.
	//
	// A near-full-screen diff is thousands of bytes, and scrolling with wrap on
	// produces one on every keystroke. os.File.Write does not loop on a partial
	// write; it returns io.ErrShortWrite and leaves the rest unsent.
	if err := writeAll(h.w, out); err != nil {
		// The screen is now in an unknown state, so the next frame has to be a
		// full repaint rather than a diff against something never drawn.
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
		h.prev = nil
		return err
	}
	h.prev = s.Clone()
	return nil
}

// writeAll writes every byte, retrying on short writes. A tty in raw mode can
// accept less than it is given when its buffer is full.
func writeAll(w io.Writer, s string) error {
	b := []byte(s)
	for len(b) > 0 {
		n, err := w.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err == io.ErrShortWrite {
			continue // n bytes went; go round again with the rest
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite // no progress; refuse to spin
		}
	}
	return nil
}

// Suspend hands the terminal back, stops the process, and forces a full
// repaint on resume — the previous frame is meaningless once a shell has been
// writing to the screen.
func (h *NativeHost) Suspend() error {
	err := h.t.Suspend(func() {
		// Re-read the size before anything else. A window resized while raj was
		// stopped produced no SIGWINCH it could service, so the cache is only
		// as good as the moment it went to sleep.
		h.readSize()
		h.prev = nil // the shell has been writing to this screen
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

// escTimeout is how long the decoder waits for the rest of a sequence before
// concluding that a lone ESC was the escape key. A terminal that does not speak
// KKP sends escape as a single byte, which is also the first byte of every
// other sequence — the only thing distinguishing them is the gap that follows.
// 25 ms is ncurses' ESCDELAY: long enough that a CSI written in one go is never
// split across the wait, short enough that escape does not feel sticky.
const escTimeout = 25 * time.Millisecond

// readLoop decodes bytes into events. Key releases and bare modifier presses
// are dropped here so nothing above the host ever has to remember to filter
// them — under KKP flag 2 every chord reports twice, and acting on both applies
// every edit twice.
//
// Reading happens in its own goroutine so the decoder can wait on a clock as
// well as on bytes, which is what the escape timeout needs.
func (h *NativeHost) readLoop() {
	chunks := make(chan []byte, 8)
	go func() {
		defer close(chunks)
		chunk := make([]byte, 4096)
		for {
			n, err := h.t.Read(chunk)
			if n > 0 {
				b := make([]byte, n)
				copy(b, chunk[:n])
				chunks <- b
			}
			if err != nil {
				return
			}
		}
	}()
	h.decodeStream(chunks, escTimeout)
}

// decodeStream turns chunks of bytes into events, flushing a stalled partial
// event after wait. Separate from readLoop so tests can drive it without a
// terminal.
func (h *NativeHost) decodeStream(chunks <-chan []byte, wait time.Duration) {
	var buf []byte
	for {
		if len(buf) == 0 {
			b, ok := <-chunks
			if !ok {
				h.emit(Quit{})
				return
			}
			buf = append(buf, b...)
		} else {
			// Something is half-decoded. Wait for the rest, but not forever:
			// a bare ESC is complete and only looks partial.
			select {
			case b, ok := <-chunks:
				if !ok {
					h.emit(Quit{})
					return
				}
				buf = append(buf, b...)
			case <-time.After(wait):
				// A paste in progress is never resolved by waiting less. The
				// timeout exists for one ambiguity — a bare ESC that is the
				// escape key rather than the start of a sequence — and a
				// paste has no such ambiguity: the start marker can only be a
				// paste, so the rest is still coming.
				//
				// Treating it like any other stalled sequence dropped the
				// whole buffer, which is why pasting into raj did nothing at
				// all: a payload large enough to arrive in more than one read,
				// or slow enough to straddle the wait, was discarded without a
				// trace. Small pastes landed in one chunk and worked, which is
				// what made it look like the picker rather than the decoder.
				if keys.PastePending(buf) {
					continue
				}
				e, used := keys.ParseFinal(buf)
				if used == 0 {
					// Not resolvable even as a final read — a truncated
					// sequence. Drop it rather than wedge the reader on it.
					buf = nil
					continue
				}
				buf = buf[used:]
				h.dispatch(e)
			}
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
	case keys.PasteEvent:
		h.emit(Paste{Text: e.Text})
	case keys.MouseEvent:
		h.emit(Mouse{e.Mouse})
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

// trueSize asks the terminal rather than trusting the cache, and refreshes the
// cache while it is there. A TIOCGWINSZ ioctl is sub-microsecond, so paying it
// once a frame is cheaper than a single wrong-sized repaint.
func (h *NativeHost) trueSize() (int, int) {
	if h.out == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.cols, h.rows // tests drive the size directly
	}
	cols, rows := term.WindowSize(h.out)
	h.mu.Lock()
	h.cols, h.rows = cols, rows
	h.mu.Unlock()
	return cols, rows
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
