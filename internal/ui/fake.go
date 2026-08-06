package ui

import (
	"strings"
	"sync"

	"raj/internal/keys"
)

// FakeHost drives the application with no terminal at all. It is the reason the
// Host seam exists: panes can be fed a scripted keystroke sequence and asserted
// against the exact frames they produced, in a unit test, deterministically.
//
// It records every frame rather than only the last, so a test can assert on
// intermediate states — that a search overlay appeared and then closed, for
// instance, rather than only on where things ended up.
type FakeHost struct {
	mu            sync.Mutex
	events        chan Event
	frames        []*Screen
	cols          int
	rows          int
	theme         Theme
	closed        bool
	invalidations int
	clipboard     string
}

// NewFakeHost returns a headless host of the given size.
func NewFakeHost(cols, rows int) *FakeHost {
	return &FakeHost{
		events: make(chan Event, 256),
		cols:   cols,
		rows:   rows,
		theme:  Theme{Background: RGBColor(0, 0, 0), Foreground: RGBColor(0xd0, 0xd0, 0xd0), Known: true},
	}
}

func (f *FakeHost) Events() <-chan Event { return f.events }
func (f *FakeHost) Size() (int, int)     { return f.cols, f.rows }
func (f *FakeHost) Theme() Theme         { return f.theme }
func (f *FakeHost) Suspend() error       { f.Send(Suspended{}); return nil }

// Invalidate records a forced repaint. The headless host has no wire to write
// to, so it only counts them, which is enough to assert that the application
// invalidates when it should.
func (f *FakeHost) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidations++
}

// Invalidations is how many full repaints have been forced.
func (f *FakeHost) Invalidations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invalidations
}

func (f *FakeHost) Present(s *Screen) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, s.Clone())
	return nil
}

func (f *FakeHost) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.events)
	}
	return nil
}

// SetClipboard records the last clipboard write, for tests.
func (f *FakeHost) SetClipboard(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipboard = text
}

// Clipboard returns the last clipboard write.
func (f *FakeHost) Clipboard() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clipboard
}

// Send queues an event.
func (f *FakeHost) Send(e Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.events <- e
	}
}

// Post injects an event, as a worker goroutine would.
func (f *FakeHost) Post(e Event) { f.Send(e) }

// Type queues each rune of text as a keypress, the way a person typing would
// produce them.
func (f *FakeHost) Type(text string) {
	for _, r := range text {
		f.Send(Key{keys.Event{Kind: keys.KeyEvent, Code: int(r), Type: keys.Press}})
	}
}

// Press queues a chord by its canonical name, e.g. "super+s" or "shift+tab".
// It fails silently on an unknown chord name; tests should assert on the
// resulting frames, which will make a typo obvious.
func (f *FakeHost) Press(chord string) {
	if e, ok := eventForChord(chord); ok {
		f.Send(Key{e})
	}
}

// SetTheme overrides the reported theme, for testing light-background handling.
func (f *FakeHost) SetTheme(t Theme) { f.theme = t }

// Frames returns every frame presented so far.
func (f *FakeHost) Frames() []*Screen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Screen(nil), f.frames...)
}

// Last returns the most recent frame, or nil if nothing has been presented.
func (f *FakeHost) Last() *Screen {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return nil
	}
	return f.frames[len(f.frames)-1]
}

// Text renders the last frame as plain lines, for readable test assertions and
// golden files.
func (f *FakeHost) Text() string {
	s := f.Last()
	if s == nil {
		return ""
	}
	_, rows := s.Size()
	lines := make([]string, rows)
	for y := 0; y < rows; y++ {
		lines[y] = s.Row(y)
	}
	return strings.Join(lines, "\n")
}

// eventForChord builds a synthetic key event from a canonical chord name by
// looking it up in the measured binding table, falling back to parsing simple
// modifier+key forms for chords that carry no binding.
func eventForChord(chord string) (keys.Event, bool) {
	for _, b := range keys.Bindings {
		if b.Chord == chord {
			e, n := keys.Parse([]byte("\x1b[" + b.Seq))
			return e, n > 0
		}
	}
	parts := strings.Split(chord, "+")
	e := keys.Event{Kind: keys.KeyEvent, Type: keys.Press}
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "shift":
			e.Mods |= keys.ModShift
		case "ctrl":
			e.Mods |= keys.ModCtrl
		case "alt":
			e.Mods |= keys.ModAlt
		case "super":
			e.Mods |= keys.ModSuper
		default:
			return e, false
		}
	}
	switch name := parts[len(parts)-1]; name {
	case "tab":
		e.Code = 9
	case "enter":
		e.Code = 13
	case "esc":
		e.Code = 27
	case "space":
		e.Code = 32
	case "backspace":
		e.Code = 127
	case "up", "down", "left", "right", "home", "end":
		e.Final = map[string]byte{"up": 'A', "down": 'B', "right": 'C', "left": 'D', "home": 'H', "end": 'F'}[name]
	case "pgup", "pgdown", "insert", "delete":
		// Tilde-form functional keys carry their identity in the number, not
		// the final byte.
		e.Final = '~'
		e.Code = map[string]int{"insert": 2, "delete": 3, "pgup": 5, "pgdown": 6}[name]
	default:
		r := []rune(name)
		if len(r) != 1 {
			return e, false
		}
		e.Code = int(r[0])
	}
	return e, true
}
