package control

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"raj/internal/term"
)

// `raj chat` is a small line-oriented chat over the participant mailboxes: the
// same send and recv any agent uses, with a person at a terminal. It registers
// one durable mailbox per machine (the key is kept in a file, so a restart
// reopens the same one), prints every message as it arrives, and sends each
// line typed. It works over SSH, which is the point: talking to the agents
// working on a workspace from a phone.
//
// A message sent from here reaches its recipient as from a participant, not as
// the person at the keyboard: raj stamps "the user" only for the editor itself,
// so an approval still belongs in the editor. It is a chat, not a control
// surface — the coordination spec's chat pane is the durable version.

// chatNow stamps every printed message. It is a package var so a test can pin
// the clock and assert the timestamp rather than tolerate a moving one.
var chatNow = time.Now

// chatColor gates SGR colour on the chat's labels and failure lines. It is a
// package var so a test can pin it; --no-color forces it off for a run, and
// NO_COLOR in the environment turns it off before anything prints. The default
// is whether stdout is a terminal.
var chatColor = chatColorDefault()

func chatColorDefault() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// The 16-colour SGR codes chat paints with. paint wraps each and resets, so a
// colour never outlives the prefix it was applied to.
const (
	chatCyan  = "\x1b[36m" // this terminal's own echo
	chatGreen = "\x1b[32m" // another participant's label
	chatDim   = "\x1b[2m"  // the editor's own notices
	chatRed   = "\x1b[31m" // a failure
	chatReset = "\x1b[0m"
)

// chatCommands is the command part of chatUsage, shared so /help prints exactly
// what the usage lists.
const chatCommands = `  /to WHO     change who your lines go to
  /who        list who is connected
  /notices    show or hide editor notices
  /help       list these commands
  /quit       leave (or ctrl+d)
`

const chatUsage = `usage: raj chat [--to WHO] [--name NAME] [--addr ADDR] [--key-file FILE] [--no-color]

Chat with the agents on a running editor. Lines you type are sent to WHO
(a key, a display name, an author id, or all); messages to you print as they
arrive. WHO defaults to RAJ_CHAT_TO; with neither set, /to WHO picks one.

` + chatCommands

// chatTty serialises the chat's output — the input loop and the receiver
// goroutine both write it — and holds the bits they share: whether editor
// notices print, how many have been suppressed since the last report, who spoke
// last, for the blank line between speakers, and the input line, so a message
// that arrives mid-read prints above it and redraws the prompt and the
// half-typed text below. All live behind one mutex so a message and its
// hidden-notice count print together, in order.
type chatTty struct {
	mu      sync.Mutex
	w       io.Writer
	notices bool
	hidden  int
	last    string // speaker of the last printed chat line; "" before any

	prompt    string // the input prompt on screen, "" before the loop prints one
	active    bool   // a read is in flight, so a printed line sits above the prompt
	raw       bool   // the raw line editor owns the line, so a message redraws it
	buf       []byte // the half-typed input line, raw mode only
	cursor    int    // cursor position within buf, raw mode only
	cols      int    // terminal width for raw redraws; 0 asks the terminal
	rows      int    // rows the drawn input occupies, raw mode only
	cursorRow int    // row within the drawn input the cursor is on, raw mode only
}

func (o *chatTty) say(format string, a ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.w, format, a...)
}

// beginPrompt prints the input prompt and marks a read in flight, so a message
// that arrives now knows to move above the prompt and redraw it below it. The
// prompt carries the same cyan as this terminal's own echo; it has no trailing
// newline, so the typed line follows it.
func (o *chatTty) beginPrompt(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prompt = s
	o.active = true
	o.promptLocked()
}

// endPrompt marks the read done. The prompt stays on screen; the input loop
// prints the next one, and the receiver stops redrawing this one.
func (o *chatTty) endPrompt() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active = false
}

// promptDisplay is the prompt as painted: cyan, with no trailing newline, so
// the typed line follows it.
func (o *chatTty) promptDisplay() string {
	return o.paint(chatCyan, o.prompt)
}

// promptLocked writes the current prompt, with no trailing newline.
func (o *chatTty) promptLocked() {
	fmt.Fprint(o.w, o.promptDisplay())
}

// redrawLocked rewrites the whole raw-mode input line. It first clears every
// row the previous line occupied — a wrapped line occupies several, and
// clearing only the last one (where the cursor sits) made the input area grow
// without bound — then paints the prompt and buffer from the first row and
// moves the cursor to its row and column. It is the one place the raw line is
// painted, so the receiver and the input loop cannot disagree about what is on
// screen.
func (o *chatTty) redrawLocked() {
	o.clearInputLocked()
	prompt := o.promptDisplay()
	o.promptLocked()
	_, _ = o.w.Write(o.buf)

	cols := o.inputCols()
	runes := []rune(string(o.buf))
	cursor := utf8.RuneCount(o.buf[:o.cursor])
	rows, cursorRow, cursorCol := inputGeometry(prompt, runes, cursor, cols)
	o.rows, o.cursorRow = rows, cursorRow

	// The reprint leaves the terminal's cursor one cell past the last
	// character; move it back (and up or down) to where it belongs.
	end := len(stripANSI(prompt)) + len(runes)
	endRow, endCol := wrapAt(end, end, cols)
	o.placeCursorLocked(endRow, endCol, cursorRow, cursorCol)
}

// clearInputLocked moves the cursor back to the first row of the drawn input,
// erases every row it occupied and leaves the cursor at the first row, column
// 0. It forgets the tracked geometry, so the next draw starts from a blank
// input area.
func (o *chatTty) clearInputLocked() {
	fmt.Fprint(o.w, "\r")
	if o.cursorRow > 0 {
		fmt.Fprintf(o.w, "\x1b[%dA", o.cursorRow)
	}
	if o.rows > 1 {
		for i := 0; i < o.rows; i++ {
			fmt.Fprint(o.w, "\x1b[2K")
			if i < o.rows-1 {
				fmt.Fprint(o.w, "\x1b[1B")
			}
		}
		fmt.Fprintf(o.w, "\x1b[%dA", o.rows-1)
	} else {
		fmt.Fprint(o.w, "\x1b[K")
	}
	o.rows, o.cursorRow = 0, 0
}

// placeCursorLocked moves the cursor from the natural end of the just-painted
// input, (endRow, endCol), to (cursorRow, cursorCol); both are relative to the
// input's first row. On one row it is a single horizontal move, which keeps
// the common case one escape.
func (o *chatTty) placeCursorLocked(endRow, endCol, cursorRow, cursorCol int) {
	switch {
	case endRow == cursorRow:
		if endCol > cursorCol {
			fmt.Fprintf(o.w, "\x1b[%dD", endCol-cursorCol)
		} else if endCol < cursorCol {
			fmt.Fprintf(o.w, "\x1b[%dC", cursorCol-endCol)
		}
	case endRow > cursorRow:
		fmt.Fprintf(o.w, "\x1b[%dA", endRow-cursorRow)
		fmt.Fprint(o.w, "\r")
		if cursorCol > 0 {
			fmt.Fprintf(o.w, "\x1b[%dC", cursorCol)
		}
	default:
		fmt.Fprintf(o.w, "\x1b[%dB", cursorRow-endRow)
		fmt.Fprint(o.w, "\r")
		if cursorCol > 0 {
			fmt.Fprintf(o.w, "\x1b[%dC", cursorCol)
		}
	}
}

// inputCols is the terminal width raw redraws wrap at. A test can pin it; a
// real run asks the terminal and treats a zero or unanswered size as 80.
func (o *chatTty) inputCols() int {
	if o.cols > 0 {
		return o.cols
	}
	cols, _ := term.WindowSize(os.Stdout)
	if cols <= 0 {
		cols = 80
	}
	return cols
}

// inputGeometry reports the screen geometry of the raw-mode input line: how
// many rows promptDisplay+buf occupy in a cols-wide terminal, and the row and
// column the cursor is in. promptDisplay is the prompt as painted; its SGR
// colour codes are stripped and count as zero width. Each element of buf is one
// column — the chat keeps its buffer as bytes, so this is exact for ASCII and
// an approximation for wide characters (a CJK glyph is two columns) or
// combining marks.
func inputGeometry(promptDisplay string, buf []rune, cursor, cols int) (rows, cursorRow, cursorCol int) {
	if cols < 1 {
		cols = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(buf) {
		cursor = len(buf)
	}
	prompt := len(stripANSI(promptDisplay))
	total := prompt + len(buf)
	rows = (total + cols - 1) / cols
	if rows < 1 {
		rows = 1
	}
	cursorRow, cursorCol = wrapAt(prompt+cursor, total, cols)
	return rows, cursorRow, cursorCol
}

// wrapAt maps a cell offset pos in a cols-wide line to a zero-based row and
// column. end is the line's total width: a cursor sitting exactly at end on a
// row boundary shows on the last column of the row it just filled, because
// that is where the terminal leaves it (the pending wrap).
func wrapAt(pos, end, cols int) (row, col int) {
	if pos == end && pos > 0 && pos%cols == 0 {
		return pos/cols - 1, cols - 1
	}
	return pos / cols, pos % cols
}

// stripANSI removes CSI escape sequences from s so its display width can be
// measured. The chat paints only CSI sequences (SGR colour codes), so that is
// all this needs to understand; anything else is left as-is.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// flushHiddenLocked prints the suppressed-notice count once, then resets it.
func (o *chatTty) flushHiddenLocked() {
	if o.hidden > 0 {
		fmt.Fprintf(o.w, "(%d notice(s) hidden)\n", o.hidden)
		o.hidden = 0
	}
}

// speakerLocked prints one blank line when s differs from the last chat line's
// speaker, then remembers s. Informational lines — raj chat: …, the hidden
// count — never call it, so they do not break a run of lines from one speaker.
func (o *chatTty) speakerLocked(s string) {
	if o.last != "" && o.last != s {
		fmt.Fprintln(o.w)
	}
	o.last = s
}

// paint wraps s in one SGR code and resets, returning s untouched when colour
// is off, so the terminal always ends back at its default.
func (o *chatTty) paint(code, s string) string {
	if !chatColor {
		return s
	}
	return code + s + chatReset
}

// labelColor maps a display label to its prefix colour: the person at the
// editor and the editor's notices are dim, everyone else is a peer.
func labelColor(label string) string {
	switch label {
	case "editor user":
		return chatDim
	case "editor":
		return chatDim
	default:
		return chatGreen
	}
}

// recv prints one incoming message. An editor notice is suppressed unless
// notices is on; the count of suppressed notices is reported once, ahead of the
// next message that does print, so a reader knows what it did not see.
func (o *chatTty) recv(m Message, label string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if m.From == AuthorOriginal && !o.notices {
		o.hidden++
		return
	}
	if o.active {
		if o.raw {
			// The raw line editor owns the input: clear every row it wrapped
			// onto before the message, so the message starts where the prompt
			// was and no stale rows survive above it.
			o.clearInputLocked()
		} else {
			// With no raw mode the terminal keeps the half-typed characters
			// in its own line buffer: this moves a message off the prompt row
			// and redraws the prompt, but a partially typed line is not
			// redrawn and the next Enter still submits the whole line.
			fmt.Fprintln(o.w)
		}
	}
	o.flushHiddenLocked()
	o.speakerLocked(fmt.Sprintf("author %d", m.From))
	fmt.Fprintf(o.w, "%s %s\n", o.paint(labelColor(label), fmt.Sprintf("%s [%s]", chatNow().Format("15:04"), label)), m.Text)
	if o.active {
		if o.raw {
			o.redrawLocked()
		} else {
			o.promptLocked()
		}
	}
}

// sent echoes a line this terminal sent: the same shape as an incoming message,
// with the whole HH:MM [sender → target] prefix in cyan. It shares the speaker
// sequence with recv, so the blank line between speakers holds whether a line
// came in or went out.
func (o *chatTty) sent(name, target, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	marker := fmt.Sprintf("%s [%s → %s]", chatNow().Format("15:04"), name, target)
	o.speakerLocked("self")
	fmt.Fprintf(o.w, "%s %s\n", o.paint(chatCyan, marker), text)
}

// fail prints a failure line in red. It is not a chat line, so it does not
// touch the speaker sequence.
func (o *chatTty) fail(format string, a ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprint(o.w, o.paint(chatRed, fmt.Sprintf(format, a...)))
}

// noticesToggle flips notice printing, reports the new state, and reports what
// was hidden since the last report.
func (o *chatTty) noticesToggle() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notices = !o.notices
	state := "off"
	if o.notices {
		state = "on"
	}
	fmt.Fprintf(o.w, "raj chat: notices %s\n", state)
	o.flushHiddenLocked()
}

// chatPrompt is the input prompt: the chat's display name and, when a target is
// set, who a typed line goes to.
func chatPrompt(name, target string) string {
	if target == "" {
		return fmt.Sprintf("[%s] > ", name)
	}
	return fmt.Sprintf("[%s->%s] > ", name, target)
}

// chatAct is what a chunk of raw input asked the input loop to do once the line
// editor had absorbed it.
type chatAct int

const (
	chatActEdit   chatAct = iota // the buffer changed; redraw and read on
	chatActSubmit                // Enter: hand the line to the dispatcher
	chatActClear                 // ctrl-c: the line was cleared; redraw empty
	chatActQuit                  // ctrl-d on an empty line: leave
)

// chatEdit is the line editor's decision for one chunk of raw bytes: the new
// buffer and cursor, the action the keys asked for, and any trailing bytes of an
// incomplete escape sequence, which the caller prepends to the next chunk.
type chatEdit struct {
	buf     []byte
	cursor  int
	act     chatAct
	pending []byte
}

// editLine feeds raw bytes to the line editor. Printable bytes insert at the
// cursor; backspace deletes before it; left/right move; home/end jump; Enter,
// ctrl-c and ctrl-d report an action. Escape sequences it does not recognise
// are swallowed rather than printed raw, and one left incomplete at the end of
// the chunk is returned as pending. It is pure, so the tests can drive it
// without a terminal.
func editLine(buf []byte, cursor int, in []byte) chatEdit {
	e := chatEdit{buf: buf, cursor: cursor}
	for i := 0; i < len(in); {
		b := in[i]
		consumed := 1
		switch {
		case b == 0x1b:
			n, key, ok := chatEscape(in[i:])
			if !ok {
				e.pending = append([]byte(nil), in[i:]...)
				return e
			}
			consumed = n
			switch key {
			case 'D': // left
				if e.cursor > 0 {
					e.cursor--
				}
			case 'C': // right
				if e.cursor < len(e.buf) {
					e.cursor++
				}
			case 'H': // home
				e.cursor = 0
			case 'F': // end
				e.cursor = len(e.buf)
			}
		case b == '\r' || b == '\n':
			e.act = chatActSubmit
			return e
		case b == 0x7f || b == 0x08: // backspace
			if e.cursor > 0 {
				e.buf = append(e.buf[:e.cursor-1], e.buf[e.cursor:]...)
				e.cursor--
			}
		case b == 0x02: // ctrl-b: left
			if e.cursor > 0 {
				e.cursor--
			}
		case b == 0x06: // ctrl-f: right
			if e.cursor < len(e.buf) {
				e.cursor++
			}
		case b == 0x01: // ctrl-a: home
			e.cursor = 0
		case b == 0x05: // ctrl-e: end
			e.cursor = len(e.buf)
		case b == 0x03: // ctrl-c: clear the line
			e.buf, e.cursor, e.act = e.buf[:0], 0, chatActClear
		case b == 0x04: // ctrl-d: quit on an empty line
			if len(e.buf) == 0 {
				e.act = chatActQuit
				return e
			}
		case b >= 0x20:
			e.buf = append(e.buf, 0)
			copy(e.buf[e.cursor+1:], e.buf[e.cursor:])
			e.buf[e.cursor] = b
			e.cursor++
		}
		i += consumed
	}
	return e
}

// chatEscape parses one escape sequence at the start of in. It returns the
// bytes consumed, the final byte when the sequence was a CSI or SS3 one, and
// whether it was complete. A complete sequence it does not recognise is still
// consumed, so it never reaches the buffer as text.
func chatEscape(in []byte) (n int, key byte, ok bool) {
	if len(in) < 2 {
		return 0, 0, false // a lone ESC: wait for the rest
	}
	if in[1] != '[' && in[1] != 'O' {
		return 2, 0, true // ESC plus one byte: an Alt chord or a function key
	}
	for i := 2; i < len(in); i++ {
		if in[i] >= 0x40 && in[i] <= 0x7e {
			return i + 1, in[i], true
		}
	}
	return 0, 0, false // no final byte yet
}

// readLine owns the input line in raw mode: it paints the prompt, drives
// editLine over the raw bytes and redraws after every edit. An incoming message
// redraws the same line from recv, under the same mutex. It returns a submitted
// line, or an error for ctrl-d on an empty line or a read failure.
func (o *chatTty) readLine(t *term.Terminal, prompt string) (string, error) {
	o.mu.Lock()
	o.prompt, o.active = prompt, true
	o.buf, o.cursor = o.buf[:0], 0
	o.redrawLocked()
	o.mu.Unlock()

	var (
		pending []byte
		p       [64]byte
	)
	for {
		n, err := t.Read(p[:])
		if n > 0 {
			chunk := p[:n]
			if len(pending) > 0 {
				chunk = append(pending, chunk...)
			}
			o.mu.Lock()
			e := editLine(o.buf, o.cursor, chunk)
			o.buf, o.cursor, pending = e.buf, e.cursor, e.pending
			switch e.act {
			case chatActSubmit:
				line := string(o.buf)
				o.buf, o.cursor, o.active = o.buf[:0], 0, false
				o.clearInputLocked()
				o.mu.Unlock()
				return line, nil
			case chatActQuit:
				o.active = false
				o.clearInputLocked()
				o.mu.Unlock()
				return "", io.EOF
			}
			o.redrawLocked()
			o.mu.Unlock()
		}
		if err != nil {
			o.mu.Lock()
			o.active = false
			o.clearInputLocked()
			o.mu.Unlock()
			return "", err
		}
	}
}

// chatEnterRaw puts the terminal in raw mode when both ends are a real TTY,
// where the line editor must own the input line. Pipes and tests get nil, so
// they keep the terminal's own line discipline; the returned Terminal leaves
// on every exit path.
func chatEnterRaw(stdin io.Reader, stdout io.Writer) *term.Terminal {
	in, ok := stdin.(*os.File)
	if !ok || !chatCharDevice(in) || !chatCharDevice(stdout) {
		return nil
	}
	t := term.New(in, stdout)
	if err := t.Enter(0); err != nil {
		return nil
	}
	// Enter hides the cursor for a full-screen renderer; a line editor needs it.
	fmt.Fprint(stdout, "\x1b[?25h")
	return t
}

// chatCharDevice reports whether w is a character device — a terminal rather
// than a pipe, a file or a test buffer.
func chatCharDevice(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ChatCLI runs `raj chat` and returns a process exit code.
func ChatCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raj chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, chatUsage) }
	to := fs.String("to", os.Getenv("RAJ_CHAT_TO"), "who your lines go to: a key, a name, an author id, or all; defaults to RAJ_CHAT_TO")
	name := fs.String("name", "me", "your display name in `raj ctl who`")
	addr := fs.String("addr", "", "the editor's control address: a socket path, or tcp://host:port")
	keyFile := fs.String("key-file", defaultChatKeyFile(), "where your mailbox key is kept between runs")
	noColor := fs.Bool("no-color", false, "print plain text, never SGR colour")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *noColor {
		chatColor = false
	}

	// Locate again on every dial, not once: a local editor's socket path is
	// per process, so after an editor restart (a cycle) the old path answers
	// nothing and a chat that kept it never reconnected. TCP addresses are
	// stable either way.
	cwd, _ := os.Getwd()
	dial := func() (*Client, error) {
		sock, err := Locate(*addr, cwd)
		if err != nil {
			return nil, err
		}
		return Dial(sock)
	}

	tx, err := dial()
	if err != nil {
		fmt.Fprintln(stderr, "raj chat: cannot reach raj:", err)
		return 1
	}
	defer func() { tx.Close() }()
	key, err := chatKey(tx, *keyFile, *name)
	if err != nil {
		fmt.Fprintln(stderr, "raj chat:", err)
		return 1
	}

	out := &chatTty{w: stdout}
	// A terminal gets raw mode, so the input loop owns the line and a message
	// that arrives mid-line redraws it whole; a pipe keeps the buffered path
	// and its terminal-owned line.
	termIn := chatEnterRaw(stdin, stdout)
	if termIn != nil {
		defer termIn.Leave()
		out.raw = true
	}
	// The receiver is parked in recv on its own connection; closing stop lets
	// it exit at its next wake, and the process ending closes the socket, so
	// the return below does not wait on it.
	stop := make(chan struct{})
	defer close(stop)
	go chatReceive(dial, key, *name, stop, out)

	target := *to
	if target == "" {
		out.say("raj chat: you are %s (%s); sending to (nobody; /to WHO). /to WHO  /who  /notices  /help  /quit\n", *name, key)
	} else {
		out.say("raj chat: you are %s (%s); sending to %s. /to WHO  /who  /notices  /help  /quit\n", *name, key, target)
	}
	in := bufio.NewReader(stdin)
	for {
		var raw string
		var rerr error
		if termIn != nil {
			raw, rerr = out.readLine(termIn, chatPrompt(*name, target))
		} else {
			// A Reader, not a Scanner, so the prompt can bracket the read: it
			// is printed before ReadString blocks, and left for the receiver
			// to redraw if a message arrives first.
			out.beginPrompt(chatPrompt(*name, target))
			raw, rerr = in.ReadString('\n')
			out.endPrompt()
		}
		line := strings.TrimSpace(raw)
		if line != "" {
			switch {
			case line == "/quit":
				return 0
			case line == "/help":
				out.say("%s", chatCommands)
			case line == "/who":
				chatWho(tx, key, *name, out)
			case line == "/notices":
				out.noticesToggle()
			case strings.HasPrefix(line, "/to "):
				target = strings.TrimSpace(strings.TrimPrefix(line, "/to "))
				out.say("raj chat: now sending to %s\n", target)
			case strings.HasPrefix(line, "/"):
				out.fail("raj chat: unknown command %s — /help\n", strings.Fields(line)[0])
			default:
				if target == "" {
					out.fail("raj chat: no recipient — /to WHO first\n")
					break
				}
				res, err := tx.Do(Request{Op: "send", To: target, Message: line})
				if err != nil {
					// The editor restarted under us: reconnect, rebind and retry once.
					tx.Close()
					if tx, err = dial(); err == nil {
						if _, err = tx.Do(Request{Op: "hello", Identity: key, Name: *name}); err == nil {
							res, err = tx.Do(Request{Op: "send", To: target, Message: line})
						}
					}
				}
				switch {
				case err != nil:
					out.fail("raj chat: not sent: %v\n", err)
				case res.Err != "":
					out.fail("raj chat: not sent: %s\n", res.Err)
				default:
					out.sent(*name, target, line)
				}
			}
		}
		if rerr != nil {
			return 0
		}
	}
}

// chatKey returns this machine's mailbox key, binding it with a hello. The
// first run mints one exactly as `raj ctl register` does and keeps it in
// keyFile, so every later run reopens the same mailbox and any mail that
// arrived in between is still there.
func chatKey(c *Client, keyFile, name string) (string, error) {
	var key string
	if b, err := os.ReadFile(keyFile); err == nil {
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		minted, err := mintRegisterKey(c, randomRegisterKey)
		if err != nil {
			return "", err
		}
		key = minted
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	res, err := c.Do(Request{Op: "hello", Identity: key, Name: name})
	if err != nil {
		return "", err
	}
	if res.Err != "" {
		return "", errors.New(res.Err)
	}
	return key, nil
}

// chatReceive parks recv on its own connection (a parked recv would block every
// other request on a shared client) and prints what arrives, naming the sender.
// A dropped connection — an editor restart — is redialled after a pause.
func chatReceive(dial func() (*Client, error), key, name string, stop <-chan struct{}, out *chatTty) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		rx, err := dial()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		hi, err := rx.Do(Request{Op: "hello", Identity: key, Name: name})
		if err != nil {
			rx.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		names := chatNames(hi.Participants)
		for {
			res, err := rx.Do(Request{Op: "recv"})
			if err != nil {
				break
			}
			select {
			case <-stop:
				rx.Close()
				return
			default:
			}
			for _, m := range res.Messages {
				label, ok := names[m.From]
				if !ok {
					// A sender that joined after our hello: refresh the names.
					if hi, err := rx.Do(Request{Op: "hello", Identity: key, Name: name}); err == nil {
						names = chatNames(hi.Participants)
					}
					if label, ok = names[m.From]; !ok {
						label = fmt.Sprintf("author %d", m.From)
					}
				}
				out.recv(m, label)
			}
		}
		rx.Close()
		time.Sleep(time.Second)
	}
}

// chatNames labels each participant for display: the person at the editor,
// the editor's own notices, and everyone else by name.
func chatNames(ps []Participant) map[uint8]string {
	names := map[uint8]string{AuthorUser: "editor user", AuthorOriginal: "editor"}
	for _, p := range ps {
		if p.ID == AuthorUser || p.ID == AuthorOriginal {
			continue
		}
		names[p.ID] = p.Name
	}
	return names
}

// chatWho prints who is connected, from the hello reply's participant list. One
// row per connected participant — name, kind, working state and key — with this
// chat's own row marked so a reader can find itself.
func chatWho(c *Client, key, name string, out *chatTty) {
	res, err := c.Do(Request{Op: "hello", Identity: key, Name: name})
	if err != nil {
		out.fail("raj chat: %v\n", err)
		return
	}
	for _, p := range res.Participants {
		if !p.Connected {
			continue
		}
		mark := "  "
		if p.Identity == key {
			mark = "* "
		}
		out.say("%s%s\t%s\t%s\t%s\n", mark, p.Name, p.Kind, p.State, p.Identity)
	}
}

// defaultChatKeyFile keeps the mailbox key with the user's config, falling back
// to the home directory.
func defaultChatKeyFile() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "raj", "chat-key")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".raj-chat-key")
	}
	return ".raj-chat-key"
}
