package ui

import (
	"testing"
	"time"
)

// nextKey drains events until a Key arrives, or gives up. Returns the chord.
func nextKey(t *testing.T, h *NativeHost, within time.Duration) string {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e := <-h.events:
			if k, ok := e.(Key); ok {
				return k.Event.Chord()
			}
		case <-deadline:
			return ""
		}
	}
}

// A terminal without KKP sends escape as one byte and nothing follows it. The
// decoder must give up waiting and deliver it, or escape never reaches the app
// at all — which is how multi-cursor became impossible to leave under iTerm2.
func TestLoneEscapeIsDeliveredAfterTheWait(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 8)}
	chunks := make(chan []byte, 4)
	go h.decodeStream(chunks, 5*time.Millisecond)
	defer close(chunks)

	chunks <- []byte{0x1b}
	if got := nextKey(t, h, time.Second); got != "esc" {
		t.Errorf("chord = %q, want esc", got)
	}
}

// The wait must not split a sequence that arrives in pieces: an ESC followed by
// the rest of a CSI is one chord, not escape and then garbage.
func TestSplitSequenceIsNotMistakenForEscape(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 8)}
	chunks := make(chan []byte, 4)
	go h.decodeStream(chunks, 200*time.Millisecond)
	defer close(chunks)

	chunks <- []byte{0x1b}
	chunks <- []byte("[97;9u") // super+a, arriving late but inside the wait
	if got := nextKey(t, h, time.Second); got != "super+a" {
		t.Errorf("chord = %q, want super+a", got)
	}
}

// Escape must not eat the key after it: the classic failure is ESC held in the
// buffer until the next keypress, which then decodes as alt+key.
func TestEscapeThenKeyArrivesAsTwoEvents(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 8)}
	chunks := make(chan []byte, 4)
	go h.decodeStream(chunks, 5*time.Millisecond)
	defer close(chunks)

	chunks <- []byte{0x1b}
	if got := nextKey(t, h, time.Second); got != "esc" {
		t.Fatalf("first chord = %q, want esc", got)
	}
	chunks <- []byte("\x1b[97u")
	if got := nextKey(t, h, time.Second); got != "a" {
		t.Errorf("second chord = %q, want a", got)
	}
}

// A truncated sequence must not wedge the reader on a buffer it can never
// finish decoding.
func TestUnresolvablePartialIsDropped(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 8)}
	chunks := make(chan []byte, 4)
	go h.decodeStream(chunks, 5*time.Millisecond)
	defer close(chunks)

	chunks <- []byte("\x1b[97;") // never terminated
	time.Sleep(30 * time.Millisecond)
	chunks <- []byte("\x1b[98u") // 'b' must still get through
	if got := nextKey(t, h, time.Second); got != "b" {
		t.Errorf("chord = %q, want b", got)
	}
}
