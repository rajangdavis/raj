package app

import (
	"fmt"
	"runtime"
	"time"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// debugLog is the diagnostic overlay: what keys arrived and what the buffer
// costs.
//
// It earns its place because the two things raj can be silently wrong about are
// invisible otherwise. A chord that Ghostty swallows produces no event at all,
// which looks identical to a chord that arrived and did nothing; and piece
// count is the number that says when a session has fragmented far enough to
// want compacting, which nothing else surfaces.
type debugLog struct {
	Open  bool
	lines []string
	mem   runtime.MemStats
}

// keep is how many recent keystrokes to retain. Enough to see a chord and its
// consequences, few enough to fit beside the editor.
const keep = 14

// record appends one decoded event. It stores the raw bytes as well as the
// chord, because when a binding misbehaves the question is almost always
// whether the terminal sent what you expected.
func (d *debugLog) record(k ui.Key, scope keys.Scope, action keys.Action, text string) {
	if !d.Open {
		return
	}
	label := string(action)
	if action == keys.None {
		if text == "" {
			label = "—"
		} else {
			label = fmt.Sprintf("text %q", text)
		}
	}
	d.lines = append(d.lines, fmt.Sprintf("%-16s %-18s %s",
		keys.Escape(k.Raw), k.Chord(), label))
	if len(d.lines) > keep {
		d.lines = d.lines[len(d.lines)-keep:]
	}
}

// sample refreshes the memory statistics. Called from the idle tick, never from
// rendering: ReadMemStats stops the world.
func (d *debugLog) sample() {
	if d.Open {
		runtime.ReadMemStats(&d.mem)
	}
}

// Render draws the overlay in the bottom-right of the editor area.
func (d *debugLog) Render(s *ui.Screen, a *App, x, y, w, h int, th widget.Theme) {
	if !d.Open {
		return
	}
	bw := 56
	if bw > w-2 {
		bw = w - 2
	}
	bh := keep + len(d.stats(a)) + 4
	if bh > h {
		bh = h
	}
	if bw < 20 || bh < 8 {
		return
	}
	bx, by := x+w-bw, y+h-bh

	s.Fill(bx, by, bw, bh, ui.DefaultStyle)
	widget.Box(s, bx, by, bw, bh, th.BorderFocus)
	s.SetString(bx+2, by, " debug ", th.Title, bw-4)

	row := by + 1
	for _, line := range d.stats(a) {
		s.SetString(bx+2, row, widget.Truncate(line, bw-4), th.Text, bw-4)
		row++
	}
	row++
	s.SetString(bx+2, row, widget.Truncate("keys (raw / chord / action)", bw-4), th.Dim, bw-4)
	row++
	for _, line := range d.lines {
		if row >= by+bh-1 {
			break
		}
		s.SetString(bx+2, row, widget.Truncate(line, bw-4), th.Text, bw-4)
		row++
	}
}

// stats is the buffer and runtime summary.
func (d *debugLog) stats(a *App) []string {
	out := []string{
		fmt.Sprintf("heap %s   sys %s   gc %d",
			bytes(d.mem.HeapAlloc), bytes(d.mem.Sys), d.mem.NumGC),
		fmt.Sprintf("goroutines %d   tabs %d   focus %s",
			runtime.NumGoroutine(), a.Tabs.Count(), a.focusName()),
		// A search pile-up shows up here before it shows up anywhere else:
		// inflight above one, or a last duration far past the frame budget,
		// means the editor is competing with its own abandoned walks.
		fmt.Sprintf("search %v   scanned %d   inflight %d   abandoned %d",
			a.Search.LastDuration().Round(time.Millisecond),
			a.Search.Result.Considered,
			a.Search.InFlight(), a.Search.Abandoned()),
	}
	p := a.Tabs.Active()
	if p == nil {
		return append(out, "no buffer")
	}
	f := p.File
	store := f.Session().Store()
	pieces := f.Pieces()
	perPiece := 0.0
	if pieces > 0 {
		perPiece = float64(store.Bytes()) / float64(pieces)
	}
	return append(out,
		fmt.Sprintf("doc %s   lines %d   cursors %d",
			bytes(uint64(f.Len())), f.Lines(), p.Cursors.Count()),
		fmt.Sprintf("pieces %d   stores %s   %.1f B/piece",
			pieces, bytes(uint64(store.Bytes())), perPiece),
		fmt.Sprintf("journal %d ops   version %d   syntax %v",
			len(f.Session().Journal()), f.Session().Version(), f.Syntax.Ready()),
	)
}

func bytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// Lines exposes the recorded keystrokes, for tests.
func (d *debugLog) Lines() []string { return append([]string(nil), d.lines...) }
