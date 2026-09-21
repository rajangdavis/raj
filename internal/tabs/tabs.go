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
	// widthPinned is set by SetTabWidth. Files opened after an explicit width
	// is named take it even when their content suggests another, because an
	// explicit setting outranks detection.
	widthPinned bool
	// preview is the index of the one reusable preview pane, or -1 when there
	// is none. A preview is a tab like any other except that arrowing through
	// the explorer replaces it rather than opening beside it; opening the
	// previewed path for real clears the marking. See OpenPreview.
	preview int
	// phone renders the bar as the phone profile's taller, scrollable chips;
	// see layout. It is fixed when the set is built.
	phone bool
	// scroll is the phone strip's horizontal offset in columns. layout clamps
	// it, so a value past either end is harmless.
	scroll int
	// activeSeen is the active index whose chip the last layout revealed. A
	// user scroll is kept until the active tab changes, then pulled back into
	// view.
	activeSeen int

	// IndentTabs is the fallback style for buffers with nothing to detect —
	// a new file, or one with no indentation yet. A file that answers for
	// itself is left alone, so this is a preference rather than an override.
	IndentTabs bool

	// Load reads a path into a File. Nil means editor.Open. The app sets it to
	// a reader that consults the persisted journal first, so a buffer comes
	// back with its proposals; a caller that never sets it gets the plain disk
	// open, which is what non-App users and tests want.
	Load func(path string, tab int) (*editor.File, error)
}

// load reads path through Load when one is set, and editor.Open otherwise.
func (t *Tabs) load(path string) (*editor.File, error) {
	if t.Load != nil {
		return t.Load(path, t.tab)
	}
	return editor.Open(path, t.tab)
}

// New returns an empty tab set. tab is the indent width for files it opens.
func New(tabWidth int) *Tabs { return &Tabs{tab: tabWidth, preview: -1, activeSeen: -1} }

// SetTabWidth changes the indent width for files opened from now on. It is an
// explicit width: a file opened afterwards takes it even when its own
// indentation suggests a different one, and Reload re-detection cannot take it
// back. Already-open panes are not touched; the app re-applies the width to
// those through the editor, which owns both the indent unit and the display
// width. A non-positive width is ignored.
func (t *Tabs) SetTabWidth(w int) {
	if w <= 0 {
		return
	}
	t.tab = w
	t.widthPinned = true
}

// TabWidth is the width new files are opened with.
func (t *Tabs) TabWidth() int { return t.tab }

// TabWidthPinned reports whether SetTabWidth has named an explicit width.
func (t *Tabs) TabWidthPinned() bool { return t.widthPinned }

// PhoneStripRows is how many screen rows the phone tab strip occupies. The
// renderer and the pointer row test both read StripRows, which agrees.
const PhoneStripRows = 3

// Phone chip geometry: a chip is at least phoneChipW columns so it is a
// comfortable touch target, and phoneChipGap columns separate neighbours.
const (
	phoneChipW   = 16
	phoneChipGap = 1
)

// SetPhone switches the tab strip to the phone profile. It is a launch
// profile, not a stored setting, so nothing flips it after construction.
func (t *Tabs) SetPhone(on bool) { t.phone = on }

// Phone reports whether the phone tab strip is on.
func (t *Tabs) Phone() bool { return t.phone }

// StripRows is how many screen rows the bar occupies: two in the phone
// profile, one otherwise. The pointer's row test reads this rather than
// assuming one, so a tap can only land on a row the bar drew.
func (t *Tabs) StripRows() int {
	if t.phone {
		return PhoneStripRows
	}
	return 1
}

// ScrollTabs moves the phone strip's horizontal offset by d columns. The next
// layout clamps it and reveals the active chip if the active tab moved. It is
// a no-op on the ordinary bar.
func (t *Tabs) ScrollTabs(d int) {
	if !t.phone {
		return
	}
	t.scroll += d
}

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
			// A preview is provisional: opening its path for real makes it an
			// ordinary tab, so enter in the explorer commits what the arrows
			// were only showing.
			t.Promote(p)
			return p, nil
		}
	}
	f, err := t.load(path)
	if err != nil {
		return nil, err
	}
	f.SetIndentDefault(editor.Indent{Tabs: t.IndentTabs, Width: t.tab})
	if t.widthPinned {
		f.SetTabWidth(t.tab)
	}
	pane := editor.NewPane(f)
	t.panes = append(t.panes, pane)
	t.active = len(t.panes) - 1
	return pane, nil
}

// OpenPreview shows path in a single reusable preview tab.
//
// Arrowing through the file tree must not spray a tab per file it passes over,
// so there is one provisional slot. A path already open anywhere is shown as it
// is; otherwise a clean preview pane is replaced in place and a dirty one is
// promoted to an ordinary tab rather than discarded, because a preview is still
// unsaved work. The caller decides whether to move focus: this only makes the
// pane active, so the explorer keeps the keys.
func (t *Tabs) OpenPreview(path string) (*editor.Pane, error) {
	for i, p := range t.panes {
		if p.File.Path == path {
			t.active = i
			return p, nil
		}
	}
	f, err := t.load(path)
	if err != nil {
		return nil, err
	}
	f.SetIndentDefault(editor.Indent{Tabs: t.IndentTabs, Width: t.tab})
	if t.widthPinned {
		f.SetTabWidth(t.tab)
	}
	return t.installPreview(editor.NewPane(f)), nil
}

// PreviewPane makes an already-built pane the preview, or focuses it when it is
// already open. It is for a buffer that exists without a tab -- a headless one a
// caller is revealing -- so previewing it shows the same document rather than
// reading a second copy from disk.
func (t *Tabs) PreviewPane(p *editor.Pane) {
	for i, q := range t.panes {
		if q == p {
			t.active = i
			return
		}
	}
	t.installPreview(p)
}

// installPreview puts p in the preview slot: replacing a clean preview, or
// appending and leaving a dirty one in place as an ordinary tab.
func (t *Tabs) installPreview(p *editor.Pane) *editor.Pane {
	if t.preview >= 0 && t.preview < len(t.panes) {
		if t.panes[t.preview].File.ViewDirty() {
			// The old preview has unsaved work, so it cannot be dropped: it
			// stops being the preview and the new pane takes the slot.
			t.preview = -1
		} else {
			t.panes[t.preview] = p
			t.active = t.preview
			return p
		}
	}
	t.panes = append(t.panes, p)
	t.preview = len(t.panes) - 1
	t.active = t.preview
	return p
}

// Promote clears p's preview marking, if it holds one. Opening a previewed file
// for real calls this, so the tab stops being provisional.
func (t *Tabs) Promote(p *editor.Pane) {
	if p != nil && t.preview >= 0 && t.preview < len(t.panes) && t.panes[t.preview] == p {
		t.preview = -1
	}
}

// Preview is the live preview pane, or nil when there is none.
func (t *Tabs) Preview() *editor.Pane {
	if t.preview < 0 || t.preview >= len(t.panes) {
		return nil
	}
	return t.panes[t.preview]
}

// Contains reports whether p is one of the open tabs. A caller that replaced a
// preview can use it to tell a pane that was dropped in place from one that was
// promoted and kept.
func (t *Tabs) Contains(p *editor.Pane) bool {
	for _, q := range t.panes {
		if q == p {
			return true
		}
	}
	return false
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
	if t.widthPinned {
		f.SetTabWidth(t.tab)
	}
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
		if p.File.ViewDirty() {
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
		if p.File.ViewDirty() {
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

// Paths lists the open files in order, for session persistence. A preview tab
// is left out: it is transient view state, not a file the user committed to.
func (t *Tabs) Paths() []string {
	out := make([]string, 0, len(t.panes))
	for i, p := range t.panes {
		// A preview is transient, so it is left out: arrowing through files
		// must not churn the session file, and a restart has no reason to
		// reopen a file nobody committed to.
		if i == t.preview {
			continue
		}
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
	if t.phone {
		return labels, t.layoutPhone(labels, w)
	}
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

// layoutPhone places the phone profile's chips. Each chip is at least
// phoneChipW columns wide so it is a comfortable target, with its label
// centred, and the strip scrolls horizontally when the chips do not fit. It
// is the one layout the renderer and HitTest share, so a tap cannot land on a
// chip other than the one drawn under it.
func (t *Tabs) layoutPhone(labels []string, w int) []Span {
	widths := make([]int, len(labels))
	total := 0
	for i, l := range labels {
		cw := max(len(l)+2, phoneChipW)
		widths[i] = cw
		total += cw + phoneChipGap
	}
	if len(labels) > 0 {
		total -= phoneChipGap
	}
	limit := max(total-w, 0)
	// Reveal the active chip when the active tab changed; otherwise keep the
	// offset the user scrolled to.
	if t.active >= 0 && t.active < len(widths) && t.active != t.activeSeen {
		t.activeSeen = t.active
		start := 0
		for i := 0; i < t.active; i++ {
			start += widths[i] + phoneChipGap
		}
		if start < t.scroll || start+widths[t.active] > t.scroll+w {
			t.scroll = start
		}
	}
	if t.scroll > limit {
		t.scroll = limit
	}
	if t.scroll < 0 {
		t.scroll = 0
	}
	spans := make([]Span, len(labels))
	col := -t.scroll
	for i, cw := range widths {
		spans[i] = Span{Start: col, End: col + cw}
		col += cw + phoneChipGap
	}
	return spans
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
	if t.preview == i {
		// The preview was closed; there is nothing left to reuse.
		t.preview = -1
	} else if t.preview > i {
		t.preview--
	}
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
	s.Fill(x, y, w, t.StripRows(), th.Dim)
	if len(t.panes) == 0 {
		s.SetString(x+1, y, "no files open", th.Dim, w-1)
		return
	}

	labels, spans := t.layout(w)
	if t.phone {
		t.renderPhone(s, x, y, w, th, labels, spans)
		return
	}
	for i, label := range labels {
		style := th.Dim
		if i == t.active {
			style = th.Active
		}
		// A preview is provisional; italic marks it without changing the
		// label, so the bar's layout and hit-testing are untouched.
		if i == t.preview {
			style = style.Plus(ui.Italic)
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

// renderPhone draws the phone chips: a two-row cell per tab with its label
// centred horizontally, clipped at the bar's edges as the strip scrolls. Both
// rows are filled with the chip style so the whole cell is a touch target.
func (t *Tabs) renderPhone(s *ui.Screen, x, y, w int, th widget.Theme, labels []string, spans []Span) {
	for i, label := range labels {
		style := th.Dim
		if i == t.active {
			style = th.Active
		}
		if i == t.preview {
			style = style.Plus(ui.Italic)
		}
		sp := spans[i]
		for dy := range PhoneStripRows {
			for col := sp.Start; col < sp.End; col++ {
				if col < 0 || col >= w {
					continue
				}
				s.Set(x+col, y+dy, ' ', style)
			}
		}
		pad := (sp.End - sp.Start - len(label)) / 2
		at, text := sp.Start+pad, label
		if at < 0 {
			text, at = clipLeft(text, -at), 0
		}
		if at < w {
			s.SetString(x+at, y+PhoneStripRows/2, text, style, w-at)
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
		// A snapshot buffer has no local save baseline, so ViewDirty is true
		// for almost any non-empty one. The honest marker for a client tab is
		// the review state it mirrors; a local buffer keeps the save test.
		if p.File.IsSnapshot() {
			if len(p.File.Session().Pending()) > 0 {
				name += " •"
			}
		} else if p.File.ViewDirty() {
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
