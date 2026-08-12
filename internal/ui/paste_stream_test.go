package ui

import (
	"strings"
	"testing"
	"time"
)

// nextPaste drains events until a Paste arrives, or gives up.
func nextPaste(t *testing.T, h *NativeHost, within time.Duration) (string, bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e := <-h.events:
			if p, ok := e.(Paste); ok {
				return p.Text, true
			}
		case <-deadline:
			return "", false
		}
	}
}

func streaming(t *testing.T, wait time.Duration) (*NativeHost, chan []byte) {
	t.Helper()
	h := &NativeHost{events: make(chan Event, 256)}
	chunks := make(chan []byte, 16)
	go h.decodeStream(chunks, wait)
	t.Cleanup(func() { close(chunks) })
	return h, chunks
}

// A paste that straddles the escape timeout must survive it.
//
// This is why pasting into raj did nothing. The timeout exists to resolve one
// ambiguity — a lone ESC is the escape key rather than the start of a sequence
// — and an unresolvable buffer was dropped so the reader could not wedge. A
// paste in progress hit that path and was discarded whole, silently. Small
// pastes arrived in a single read and worked, which is what made it look like
// the picker rather than the decoder.
func TestPasteSurvivesTheEscapeTimeout(t *testing.T) {
	h, chunks := streaming(t, 5*time.Millisecond)

	chunks <- []byte("\x1b[200~internal/app/")
	time.Sleep(40 * time.Millisecond) // several times the wait
	chunks <- []byte("app.go\x1b[201~")

	text, ok := nextPaste(t, h, time.Second)
	if !ok {
		t.Fatal("the paste was dropped")
	}
	if text != "internal/app/app.go" {
		t.Errorf("text = %q, want the whole payload", text)
	}
}

// A large paste arrives in many reads, with the timeout firing between several
// of them. Every byte has to survive, in order.
func TestLargePasteAcrossManyChunks(t *testing.T) {
	h, chunks := streaming(t, 5*time.Millisecond)

	want := strings.Repeat("some line of pasted text\n", 400)
	chunks <- []byte("\x1b[200~")
	for i := 0; i < len(want); i += 500 {
		end := i + 500
		if end > len(want) {
			end = len(want)
		}
		chunks <- []byte(want[i:end])
		time.Sleep(8 * time.Millisecond)
	}
	chunks <- []byte("\x1b[201~")

	text, ok := nextPaste(t, h, 5*time.Second)
	if !ok {
		t.Fatal("the paste was dropped")
	}
	if text != want {
		t.Errorf("got %d bytes, want %d", len(text), len(want))
	}
}

// The escape key still works. Fixing the paste case must not turn the timeout
// off for the ambiguity it exists to resolve.
func TestLoneEscapeStillArrivesAfterTheFix(t *testing.T) {
	h, chunks := streaming(t, 5*time.Millisecond)
	chunks <- []byte{0x1b}
	if got := nextKey(t, h, time.Second); got != "esc" {
		t.Errorf("chord = %q, want esc", got)
	}
}

// A start marker with nothing behind it must not wedge the reader forever.
// MaxPaste is the ceiling that makes waiting safe.
func TestUnterminatedPasteDoesNotWedgeTheReader(t *testing.T) {
	h, chunks := streaming(t, 5*time.Millisecond)

	chunks <- []byte("\x1b[200~")
	chunks <- []byte(strings.Repeat("x", 1024))
	time.Sleep(30 * time.Millisecond)
	// A key typed afterwards still has to get through, which is the property
	// that matters: the reader is alive.
	chunks <- []byte(strings.Repeat("y", 64))
	chunks <- []byte("\x1b[201~")

	if _, ok := nextPaste(t, h, 2*time.Second); !ok {
		t.Error("the reader never recovered")
	}
}

// Newlines inside a paste are folded, and the payload arrives as one event
// rather than as a keystroke per byte.
func TestSplitPasteIsStillOneEvent(t *testing.T) {
	h, chunks := streaming(t, 5*time.Millisecond)

	chunks <- []byte("\x1b[200~line one\r\n")
	time.Sleep(30 * time.Millisecond)
	chunks <- []byte("line two\r\x1b[201~")

	text, ok := nextPaste(t, h, time.Second)
	if !ok {
		t.Fatal("the paste was dropped")
	}
	if text != "line one\nline two\n" {
		t.Errorf("text = %q, want CR and CRLF folded to LF", text)
	}
	select {
	case e := <-h.events:
		if k, ok := e.(Key); ok {
			t.Errorf("a paste byte arrived as the key %q", k.Event.Chord())
		}
	default:
	}
}
