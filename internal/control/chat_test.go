package control

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer safe for the chat's two writers (the input
// loop and the receiver goroutine) and the test reading it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.Reset()
}

// waitForOutput waits until the chat has printed want, or fails with the whole
// output. The receiver goroutine writes concurrently, so this polls the buffer
// rather than sleeping a fixed time.
func waitForOutput(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat output = %q, want %q", out.String(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForSuffix waits until the chat's output ends with want — the input prompt
// with no line after it — so a test can tell the loop is parked on a read.
func waitForSuffix(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.HasSuffix(out.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat output = %q, want it to end with %q", out.String(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// chatAuthor returns the author id the server assigned to the mailbox key, so a
// test can post an editor notice into that mailbox. It fails if the key is not
// yet a participant, which is the caller's sign that the chat has not finished
// binding.
func chatAuthor(t *testing.T, ed *fakeEditor, key string) uint8 {
	t.Helper()
	key = strings.TrimSpace(key)
	for _, p := range ed.srv.Roster() {
		if p.Identity == key {
			return p.ID
		}
	}
	t.Fatalf("no participant for key %q", key)
	return 0
}

// `raj chat` sends each typed line to its target and prints what arrives,
// named by sender, and keeps its mailbox key in a file so the next run reopens
// the same mailbox. Precondition: a fake editor with a peer named "claude".
func TestChatSendsAndReceives(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	if _, err := io.WriteString(typed, "hello from the phone\n"); err != nil {
		t.Fatal(err)
	}
	got := recvNow(t, peer)
	if len(got) != 1 || got[0].Text != "hello from the phone" {
		t.Fatalf("peer received %+v, want the typed line", got)
	}

	key, err := os.ReadFile(keyFile)
	if err != nil || !strings.HasPrefix(string(key), "raj-") {
		t.Fatalf("mailbox key file = %q, %v", key, err)
	}
	res, err := peer.Do(Request{Op: "send", To: strings.TrimSpace(string(key)), Message: "hi back"})
	if err != nil || !res.OK {
		t.Fatalf("reply: %v %+v", err, res)
	}
	waitForOutput(t, out, "[claude] hi back")

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// Editor notices are hidden by default: a notice arriving before a peer message
// counts as hidden, the peer message reports the count, /notices turns notices
// on, and a later notice prints. Without the notice filter the notice prints
// always; without the count the reader cannot tell something was suppressed.
func TestChatHidesNotices(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	// One typed line binds the chat's mailbox, so the key file names a
	// participant the notice can be addressed to.
	io.WriteString(typed, "registering\n")
	if got := recvNow(t, peer); len(got) != 1 {
		t.Fatalf("peer received %+v, want the typed line", got)
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(raw))
	chatID := chatAuthor(t, ed, key)

	if err := ed.srv.PostNotice(chatID, "saved /w/a.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Do(Request{Op: "send", To: key, Message: "hi back"}); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, out, "[claude] hi back")
	if got := out.String(); !strings.Contains(got, "(1 notice(s) hidden)") || strings.Contains(got, "saved /w/a.go") {
		t.Fatalf("output = %q, want one hidden notice and no notice text", got)
	}

	io.WriteString(typed, "/notices\n")
	waitForOutput(t, out, "raj chat: notices on")

	if err := ed.srv.PostNotice(chatID, "saved /w/b.go"); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, out, "[editor] saved /w/b.go")

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// A sent line is echoed with a timestamp, the sender's name and the target, so
// the person at the terminal sees what left and to whom. The clock is pinned so
// the 15:04 prefix is asserted rather than tolerated.
func TestChatEchoesOwnLine(t *testing.T) {
	old := chatNow
	chatNow = func() time.Time { return time.Date(2026, 9, 27, 12, 34, 0, 0, time.UTC) }
	defer func() { chatNow = old }()

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--no-color"}, in, out, out)
	}()

	if _, err := io.WriteString(typed, "hi\n"); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, out, "12:34 [me → claude] hi")
	if got := recvNow(t, peer); len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("peer received %+v, want the typed line", got)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// With RAJ_CHAT_TO unset and no --to, there is no target: the banner says so and
// a typed line is refused with a hint, not sent. A hard-coded default would
// send it.
func TestChatNoDefaultTarget(t *testing.T) {
	t.Setenv("RAJ_CHAT_TO", "")
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	waitForOutput(t, out, "sending to (nobody; /to WHO)")
	waitForOutput(t, out, "[phone] > ") // no target: the prompt names only the chat
	io.WriteString(typed, "hello\n")
	waitForOutput(t, out, "raj chat: no recipient — /to WHO first")
	if n := ed.srv.Mail.Unread(peer.Author()); n != 0 {
		t.Errorf("a line with no recipient was sent: %d unread", n)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// Two messages from different speakers are separated by exactly one blank line;
// two in a row from the same speaker are not. A chat that separates every line,
// or none, fails half of this.
func TestChatBlankLineBetweenSpeakers(t *testing.T) {
	old := chatNow
	chatNow = func() time.Time { return time.Date(2026, 9, 27, 12, 34, 0, 0, time.UTC) }
	defer func() { chatNow = old }()

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	claude := joinAs(t, ed, "raj-peer0001", "claude")
	alice := joinAs(t, ed, "raj-peer0002", "alice")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	// A typed line binds the chat's mailbox and prints the echo, so the peers
	// have an address and the output starts from a known speaker.
	if _, err := io.WriteString(typed, "start\n"); err != nil {
		t.Fatal(err)
	}
	if got := recvNow(t, claude); len(got) != 1 {
		t.Fatalf("peer received %+v, want the typed line", got)
	}
	waitForOutput(t, out, "12:34 [phone → claude] start")
	waitForSuffix(t, out, "[phone->claude] > ") // the loop is parked on the next read
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(raw))

	for _, send := range []struct {
		to  *Client
		msg string
	}{
		{claude, "one"},
		{alice, "two"},
		{alice, "three"},
	} {
		if _, err := send.to.Do(Request{Op: "send", To: key, Message: send.msg}); err != nil {
			t.Fatal(err)
		}
	}
	waitForOutput(t, out, "12:34 [alice] three\n[phone->claude] > ")
	want := "12:34 [phone → claude] start\n" +
		"[phone->claude] > \n\n12:34 [claude] one\n" +
		"[phone->claude] > \n\n12:34 [alice] two\n" +
		"[phone->claude] > \n12:34 [alice] three\n[phone->claude] > "
	if got := out.String(); !strings.Contains(got, want) {
		t.Fatalf("chat output = %q, want it to contain %q", got, want)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// The prompt is printed before each read, and an incoming message delivered
// while the read is pending moves off the prompt line and is followed by the
// prompt again. Without the leading newline the message lands on the prompt;
// without the redraw the prompt is gone.
func TestChatPrompt(t *testing.T) {
	old := chatNow
	chatNow = func() time.Time { return time.Date(2026, 9, 27, 12, 34, 0, 0, time.UTC) }
	defer func() { chatNow = old }()

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	// The prompt names the chat and its target, and it is on screen while the
	// read is pending.
	waitForOutput(t, out, "[phone->claude] > ")
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Do(Request{Op: "send", To: strings.TrimSpace(string(raw)), Message: "hi back"}); err != nil {
		t.Fatal(err)
	}
	want := "[phone->claude] > \n12:34 [claude] hi back\n[phone->claude] > "
	waitForOutput(t, out, want)

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// --no-color forces plain output even when the package default would colour a
// terminal, so no SGR reaches a pipe or a log.
func TestChatNoColorFlag(t *testing.T) {
	old := chatColor
	chatColor = true
	t.Cleanup(func() { chatColor = old })

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	io.WriteString(typed, "hi\n")
	waitForOutput(t, out, "[phone → claude] hi")
	if got := out.String(); strings.Contains(got, "\x1b[") {
		t.Fatalf("output carries SGR with --no-color: %q", got)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// With colour on, the whole HH:MM [label] prefix carries SGR and the message
// body does not; a chat that colours the body, or only the label, fails.
func TestChatColorsEnabled(t *testing.T) {
	oldColor := chatColor
	chatColor = true
	t.Cleanup(func() { chatColor = oldColor })
	oldNow := chatNow
	chatNow = func() time.Time { return time.Date(2026, 9, 27, 12, 34, 0, 0, time.UTC) }
	t.Cleanup(func() { chatNow = oldNow })

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone"}, in, out, out)
	}()

	io.WriteString(typed, "hi\n")
	waitForOutput(t, out, "\x1b[36m12:34 [phone → claude]\x1b[0m hi")
	if got := out.String(); strings.Contains(got, "\x1b[36mhi") {
		t.Fatalf("the message body is coloured: %q", got)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// An unknown /word is a command, not a message: it is refused with a hint and
// nothing is sent. A chat that only special-cases the known names would deliver
// "/frobnicate" to the target.
func TestChatUnknownCommand(t *testing.T) {

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	peer := joinAs(t, ed, "raj-peer0001", "claude")
	keyFile := filepath.Join(t.TempDir(), "chat-key")

	in, typed := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- ChatCLI([]string{"--to", "claude", "--key-file", keyFile, "--name", "phone", "--no-color"}, in, out, out)
	}()

	io.WriteString(typed, "/frobnicate\n")
	waitForOutput(t, out, "raj chat: unknown command /frobnicate — /help")
	if n := ed.srv.Mail.Unread(peer.Author()); n != 0 {
		t.Errorf("an unknown command was sent: %d unread", n)
	}

	io.WriteString(typed, "/quit\n")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("chat exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not exit on /quit")
	}
}

// editLine is the raw-mode line editor's pure core. The TTY input path cannot
// run in a test, so the keystrokes are driven here instead: insertion at the
// cursor, backspace, the arrow and home/end keys (CSI and the control-byte
// aliases), submit, ctrl-c and ctrl-d.
func TestEditLine(t *testing.T) {
	cases := []struct {
		name    string
		buf     string
		cursor  int
		in      string
		want    string
		at      int
		wantAct chatAct
	}{
		{"insert at end", "", 0, "hi", "hi", 2, chatActEdit},
		{"insert at cursor", "ac", 1, "b", "abc", 2, chatActEdit},
		{"backspace deletes before", "abc", 3, "\x7f", "ab", 2, chatActEdit},
		{"ctrl-h deletes before", "abc", 2, "\x08", "ac", 1, chatActEdit},
		{"backspace at start is a no-op", "abc", 0, "\x7f", "abc", 0, chatActEdit},
		{"two backspaces", "abc", 3, "\x7f\x08", "a", 1, chatActEdit},
		{"left arrow moves left", "abc", 3, "\x1b[D", "abc", 2, chatActEdit},
		{"right arrow moves right", "abc", 1, "\x1b[C", "abc", 2, chatActEdit},
		{"SS3 left arrow", "ab", 2, "\x1bOD", "ab", 1, chatActEdit},
		{"ctrl-b and ctrl-f", "abc", 1, "\x02\x06", "abc", 1, chatActEdit},
		{"CSI home and end", "abc", 1, "\x1b[H\x1b[F", "abc", 3, chatActEdit},
		{"ctrl-a and ctrl-e", "abc", 1, "\x01\x05", "abc", 3, chatActEdit},
		{"unhandled sequence is swallowed", "x", 1, "\x1b[3~", "x", 1, chatActEdit},
		{"enter submits", "hi", 2, "\r", "hi", 2, chatActSubmit},
		{"newline submits", "hi", 2, "\n", "hi", 2, chatActSubmit},
		{"ctrl-c clears", "abc", 2, "\x03", "", 0, chatActClear},
		{"ctrl-d quits on empty", "", 0, "\x04", "", 0, chatActQuit},
		{"ctrl-d on text is ignored", "a", 1, "\x04", "a", 1, chatActEdit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := editLine([]byte(c.buf), c.cursor, []byte(c.in))
			if string(e.buf) != c.want || e.cursor != c.at || e.act != c.wantAct {
				t.Fatalf("editLine(%q,%d,%q) = (%q,%d,%v), want (%q,%d,%v)",
					c.buf, c.cursor, c.in, e.buf, e.cursor, e.act, c.want, c.at, c.wantAct)
			}
		})
	}
}

// An escape sequence split across reads is neither lost nor printed: the first
// chunk holds the lone ESC as pending, and the next completes the arrow.
func TestEditLineSplitEscape(t *testing.T) {
	e := editLine([]byte("ab"), 2, []byte("\x1b"))
	if string(e.buf) != "ab" || e.cursor != 2 || e.act != chatActEdit {
		t.Fatalf("first chunk = (%q,%d,%v), want (ab,2,edit)", e.buf, e.cursor, e.act)
	}
	if string(e.pending) != "\x1b" {
		t.Fatalf("first chunk pending = %q, want ESC", e.pending)
	}
	e = editLine(e.buf, e.cursor, append(e.pending, '[', 'D'))
	if string(e.buf) != "ab" || e.cursor != 1 || e.act != chatActEdit {
		t.Fatalf("second chunk = (%q,%d,%v), want (ab,1,edit)", e.buf, e.cursor, e.act)
	}
}

// A raw-mode recv clears the row the prompt is on before printing the message
// and redraws the prompt with the half-typed buffer after it, moving the cursor
// back to where it was. This is the point of owning the input line.
func TestChatRawRedrawsTypedLine(t *testing.T) {
	oldColor := chatColor
	chatColor = false
	t.Cleanup(func() { chatColor = oldColor })

	out := &lockedBuffer{}
	o := &chatTty{w: out, raw: true, active: true, prompt: "[me] > ", buf: []byte("hlo"), cursor: 1}
	o.recv(Message{From: 7, Text: "ping"}, "claude")
	got := out.String()
	if !strings.Contains(got, "ping") {
		t.Fatalf("recv output = %q, want the message text", got)
	}
	want := "\r\x1b[K[me] > hlo\x1b[2D"
	if !strings.Contains(got, want) {
		t.Fatalf("recv output = %q, want it to contain %q", got, want)
	}
}

// inputGeometry is the pure geometry behind raw-mode redraws: it turns the
// painted prompt, the buffer and the cursor into a row count and a cursor
// cell. The wrapped cases are the ones that made a redraw append a row.
func TestInputGeometry(t *testing.T) {
	const p = "[me] > " // 7 columns
	cases := []struct {
		name   string
		prompt string
		buf    string
		cursor int
		cols   int
		rows   int
		row    int
		col    int
	}{
		{"short line", p, "hi", 2, 80, 1, 0, 9},
		{"cursor inside a short line", p, "hlo", 1, 80, 1, 0, 8},
		{"prompt colour is zero width", "\x1b[36m" + p + "\x1b[0m", "hi", 2, 80, 1, 0, 9},
		{"exactly cols", p, strings.Repeat("x", 13), 13, 20, 1, 0, 19},
		{"one wrap", p, strings.Repeat("x", 14), 14, 20, 2, 1, 1},
		{"several wraps", p, strings.Repeat("x", 40), 40, 20, 3, 2, 7},
		{"cursor inside a wrapped line", p, strings.Repeat("x", 20), 5, 20, 2, 0, 12},
		{"cursor on a mid-line wrap boundary", p, strings.Repeat("x", 20), 13, 20, 2, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, row, col := inputGeometry(c.prompt, []rune(c.buf), c.cursor, c.cols)
			if rows != c.rows || row != c.row || col != c.col {
				t.Fatalf("inputGeometry(%q,%q,%d,%d) = (%d,%d,%d), want (%d,%d,%d)",
					c.prompt, c.buf, c.cursor, c.cols, rows, row, col, c.rows, c.row, c.col)
			}
		})
	}
}

// A raw redraw of a wrapped input clears every row the line occupied before
// reprinting. Clearing only the last row (where the cursor was) left the rows
// above it and grew the input area by one row per keystroke.
func TestChatRawRedrawsWrappedLine(t *testing.T) {
	oldColor := chatColor
	chatColor = false
	t.Cleanup(func() { chatColor = oldColor })

	out := &lockedBuffer{}
	o := &chatTty{w: out, raw: true, active: true, prompt: "[me] > ", cols: 20,
		buf: []byte(strings.Repeat("x", 14)), cursor: 14}
	o.redrawLocked()
	if o.rows != 2 || o.cursorRow != 1 {
		t.Fatalf("after first draw rows=%d cursorRow=%d, want 2, 1", o.rows, o.cursorRow)
	}
	want := "\r\x1b[K[me] > " + strings.Repeat("x", 14)
	if got := out.String(); !strings.HasPrefix(got, want) {
		t.Fatalf("first redraw = %q, want it to start with %q", got, want)
	}

	out.Reset()
	o.redrawLocked()
	// From row 1 the cursor moves up to row 0, both rows are erased, and the
	// cursor returns to row 0 before the line is painted again.
	want = "\r\x1b[1A\x1b[2K\x1b[1B\x1b[2K\x1b[1A[me] > " + strings.Repeat("x", 14)
	if got := out.String(); !strings.HasPrefix(got, want) {
		t.Fatalf("wrapped redraw = %q, want it to start with %q", got, want)
	}
}
