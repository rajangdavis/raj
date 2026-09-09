// Package tabs holds the open files and the bar that shows them.
package tabs

import (
	"path/filepath"

	"raj/internal/editor"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Tabs is the set of open files with one active.
//
// Closed tabs are remembered so cmd+shift+t can bring them back. Only the path
// is kept, not the buffer: reopening rereads from disk, which is what you want
// after closing something by accident and is far cheaper than pinning every
// closed file's piece table in memory.
type Tabs struct {
	panes  []*editor.Pane
	active int
	closed []string
	tab    int

	// IndentTabs is the fallback style for buffers with nothing to detect —
	// a new file, or one with no indentation yet. A file that answers for
	// itself is left alone, so this is a preference rather than an override.
	IndentTabs bool
}

// New returns an empty tab set. tab is the indent width for files it opens.
func New(tabWidth int) *Tabs { return &Tabs{tab: tabWidth} }

func (t *Tabs) Count() int          { return len(t.panes) }
func (t *Tabs) All() []*editor.Pane { return t.panes }

// Active is the focused pane, or nil when nothing is open.
func (t *Tabs) Active() *editor.Pane {
	if len(t.panes) == 0 {
		return nil
	}
	if t.active >= len(t.panes) {
		t.active = len(t.panes) - 1
	}
	return t.panes[t.active]
}

// Index is the active tab's position.
func (t *Tabs) Index() int { return t.active }

// Open focuses the tab for path if it is already open, and otherwise opens it.
// Reusing an existing tab matters because the file picker and search results
// both open files, and without it a busy session accumulates duplicates.
func (t *Tabs) Open(path string) (*editor.Pane, error) {
	for i, p := range t.panes {
		if p.File.Path == path {
			t.active = i
			return p, nil
		}
	}
	f, err := editor.Open(path, t.tab)
	if err != nil {
		return nil, err
	}
	f.SetIndentDefault(editor.Indent{Tabs: t.IndentTabs, Width: t.tab})
	pane := editor.NewPane(f)
	t.panes = append(t.panes, pane)
	t.active = len(t.panes) - 1
	return pane, nil
}

// Add takes an already-built pane, for buffers not read from disk.
func (t *Tabs) Add(p *editor.Pane) {
	t.panes = append(t.panes, p)
	t.active = len(t.panes) - 1
}

// NewFile opens an empty unnamed buffer in a new tab and focuses it.
//
// It never reuses an existing untitled tab. Open dedupes by path, and an unnamed
// buffer has none — two cmd+n presses mean two scratch buffers, which is what
// every editor that has the chord does.
func (t *Tabs) NewFile() *editor.Pane {
	f := editor.NewFile("", "", t.tab)
	f.SetIndentDefault(editor.Indent{Tabs: t.IndentTabs, Width: t.tab})
	p := editor.NewPane(f)
	t.Add(p)
	return p
}

// Close removes the active tab. It never closes raj itself: closing the last
// tab leaves an empty editor, which is what you asked for and what stops a
// stray cmd+w from ending the session.
func (t *Tabs) Close() { t.CloseIndex(t.active) }

// PopClosed returns the most recently closed path, for the caller to reopen
// through its usual path rather than bypassing it here.
func (t *Tabs) PopClosed() (string, bool) {
	if len(t.closed) == 0 {
		return "", false
	}
	path := t.closed[len(t.closed)-1]
	t.closed = t.closed[:len(t.closed)-1]
	return path, true
}

// Next and Prev cycle through tabs, wrapping at the ends.
func (t *Tabs) Next() { t.step(+1) }
func (t *Tabs) Prev() { t.step(-1) }

func (t *Tabs) step(d int) {
	if len(t.panes) == 0 {
		return
	}
	t.active = (t.active + d + len(t.panes)) % len(t.panes)
}

// Goto selects the nth tab, 1-based. Out-of-range selections are ignored rather
// than clamped: cmd+7 with four tabs open should do nothing, not jump to the
// last one.
func (t *Tabs) Goto(n int) {
	if n >= 1 && n <= len(t.panes) {
		t.active = n - 1
	}
}

// DirtyCount is how many open files have unsaved changes.
func (t *Tabs) DirtyCount() (n int) {
	for _, p := range t.panes {
		if p.File.Dirty() {
			n++
		}
	}
	return
}

// Dirty lists the panes with unsaved changes, in tab order, so a caller asking
// about them can also name them.
func (t *Tabs) Dirty() []*editor.Pane {
	var out []*editor.Pane
	for _, p := range t.panes {
		if p.File.Dirty() {
			out = append(out, p)
		}
	}
	return out
}

// Focus makes p the active tab. Used when a caller has to visit tabs in turn —
// asking about an unsaved buffer is meaningless if the buffer being asked about
// is not the one on screen.
func (t *Tabs) Focus(p *editor.Pane) bool {
	for i, q := range t.panes {
		if q == p {
			t.active = i
			return true
		}
	}
	return false
}

// Paths lists the open files in order, for session persistence.
func (t *Tabs) Paths() []string {
	out := make([]string, 0, len(t.panes))
	for _, p := range t.panes {
		out = append(out, p.File.Path)
	}
	return out
}

// Span is where one tab was drawn, as columns [Start, End) relative to the
// bar's own origin. Start may be negative when the bar has scrolled past it.
type Span struct{ Start, End int }

// layout is where every tab lands in a bar w columns wide.
//
// Render and HitTest both go through here rather than each doing the arithmetic
// themselves, because the two only have to disagree by one column — a
// separator, a scroll offset — for a click to switch to the tab beside the one
// under the pointer, and nothing on screen would show why.
func (t *Tabs) layout(w int) (labels []string, spans []Span) {
	labels = t.labels()
	total := 0
	for _, l := range labels {
		total += len(l) + 1 // plus the separator
	}

	// Scroll only when the bar overflows, and only far enough to reveal the
	// active tab. A bar that re-centres on every switch is hard to track.
	start := 0
	if total > w {
		before := 0
		for i := 0; i < t.active; i++ {
			before += len(labels[i]) + 1
		}
		if end := before + len(labels[t.active]); end > w {
			start = end - w
		}
	}

	col := -start
	spans = make([]Span, len(labels))
	for i, label := range labels {
		spans[i] = Span{Start: col, End: col + len(label)}
		col += len(label) + 1 // the separator sits between tabs
	}
	return labels, spans
}

// HitTest returns the tab drawn at a screen column, given the bar's origin and
// width. The separator between two tabs belongs to neither: it is one column
// wide and claiming it for a neighbour would make the boundary between tabs a
// column away from where it looks.
func (t *Tabs) HitTest(x, w, col int) (int, bool) {
	if len(t.panes) == 0 || col < x || col >= x+w {
		return 0, false
	}
	dx := col - x
	_, spans := t.layout(w)
	for i, sp := range spans {
		if dx >= sp.Start && dx < sp.End {
			return i, true
		}
	}
	return 0, false
}

// CloseIndex removes the nth tab. Close is this with the active index, so the
// two cannot drift over what closing means for the remembered path or for which
// tab is left focused.
func (t *Tabs) CloseIndex(i int) {
	if i < 0 || i >= len(t.panes) {
		return
	}
	p := t.panes[i]
	if p.File.Path != "" {
		t.closed = append(t.closed, p.File.Path)
	}
	t.panes = append(t.panes[:i], t.panes[i+1:]...)
	if t.active > i || t.active >= len(t.panes) {
		t.active--
	}
	if t.active < 0 {
		t.active = 0
	}
}

// Render draws the tab bar, scrolling so the active tab is always visible.
//
// The active tab is reverse-video rather than merely bold: at a glance the eye
// finds a filled block far faster than a weight difference, and in a terminal
// theme with low contrast bold may not be distinguishable at all. Separators
// keep adjacent names from reading as one string.
func (t *Tabs) Render(s *ui.Screen, x, y, w int, th widget.Theme) {
	s.Fill(x, y, w, 1, th.Dim)
	if len(t.panes) == 0 {
		s.SetString(x+1, y, "no files open", th.Dim, w-1)
		return
	}

	labels, spans := t.layout(w)
	for i, label := range labels {
		style := th.Dim
		if i == t.active {
			style = th.Active
		}
		if sp := spans[i]; sp.End > 0 && sp.Start < w {
			at, text := sp.Start, label
			if at < 0 {
				text, at = clipLeft(label, -at), 0
			}
			s.SetString(x+at, y, text, style, w-at)
		}
		if sep := spans[i].End; sep >= 0 && sep < w && i < len(labels)-1 {
			s.Set(x+sep, y, '│', th.Border)
		}
	}
}

// labels names each tab, adding enough parent directory to tell apart files
// that share a base name. Three tabs all reading "main.go" is worse than no
// labels at all.
func (t *Tabs) labels() []string {
	counts := map[string]int{}
	for _, p := range t.panes {
		counts[p.File.Name()]++
	}
	out := make([]string, len(t.panes))
	for i, p := range t.panes {
		name := p.File.Name()
		if counts[name] > 1 {
			if dir := filepath.Base(filepath.Dir(p.File.Path)); dir != "." && dir != "/" {
				name = dir + "/" + name
			}
		}
		if p.File.Dirty() {
			name += " •"
		}
		if p.DiskStale() {
			name += " !"
		}
		out[i] = " " + name + " "
	}
	return out
}

func clipLeft(s string, n int) string {
	if n <= 0 {
		return s
	}
	if n >= len(s) {
		return ""
	}
	return s[n:]
}
