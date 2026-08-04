// Package picker is the cmd+p file finder: a floating overlay, not a sidebar.
package picker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// MaxFiles bounds the index. A picker that takes seconds to open in a large
// repository is worse than one that quietly stops indexing.
const MaxFiles = 20000

// Picker is a floating file finder.
type Picker struct {
	Root  string
	Open  bool
	files []string
	shown []scored
	input widget.Input
	list  widget.List
}

type scored struct {
	path  string
	score int
	hits  []int // byte offsets in the path that matched, for highlighting
}

// New builds a picker rooted at a directory.
func New(root string) *Picker {
	p := &Picker{Root: root}
	p.input = widget.Input{Label: "Go to file"}
	return p
}

// Show opens the picker, reindexing so files created since last time appear.
func (p *Picker) Show() {
	p.Open = true
	p.input.SetText("")
	p.input.Focused = true
	p.index()
	p.filter()
}

// Hide closes it.
func (p *Picker) Hide() { p.Open = false }

// Handle applies an action, returning a chosen path.
func (p *Picker) Handle(a keys.Action, text string) (path string) {
	switch a {
	case keys.Cancel:
		p.Hide()
		return ""
	case keys.LineUp:
		p.list.Move(-1, len(p.shown))
		return ""
	case keys.LineDown:
		p.list.Move(+1, len(p.shown))
		return ""
	case keys.Confirm:
		if p.list.Sel < len(p.shown) {
			chosen := p.shown[p.list.Sel].path
			p.Hide()
			return chosen
		}
		return ""
	}
	if p.input.Handle(a, text) {
		p.filter()
	}
	return ""
}

func (p *Picker) index() {
	p.files = p.files[:0]
	filepath.WalkDir(p.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != p.Root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if len(p.files) >= MaxFiles {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(p.Root, path)
		if err == nil {
			p.files = append(p.files, rel)
		}
		return nil
	})
}

// filter rescores every file against the query. An empty query lists
// everything, so cmd+p with no typing is a plain file list.
func (p *Picker) filter() {
	p.shown = p.shown[:0]
	q := strings.ToLower(p.input.Text)
	for _, f := range p.files {
		if q == "" {
			p.shown = append(p.shown, scored{path: f})
			continue
		}
		if s, hits, ok := fuzzy(f, q); ok {
			p.shown = append(p.shown, scored{f, s, hits})
		}
	}
	if q != "" {
		sort.SliceStable(p.shown, func(i, j int) bool { return p.shown[i].score > p.shown[j].score })
	}
	p.list.Reset()
}

// fuzzy scores a subsequence match.
//
// Scattered subsequence matches are the trap: "test" is a subsequence of
// in-t-ernal/ui/styl-e.go -> s -> t, so a naive per-character score ranks it
// alongside a file actually named test.go. The fix is to make contiguity
// dominate — a run of n adjacent characters scores quadratically, gaps cost,
// and an exact substring of the base name outweighs anything spread across
// directories.
func fuzzy(path, query string) (score int, hits []int, ok bool) {
	lower := strings.ToLower(path)
	baseAt := strings.LastIndexByte(path, filepath.Separator) + 1
	base := lower[baseAt:]

	qi, run, gaps := 0, 0, 0
	for i := 0; i < len(lower) && qi < len(query); i++ {
		if lower[i] != query[qi] {
			if qi > 0 {
				gaps++
			}
			run = 0
			continue
		}
		hits = append(hits, i)
		run++
		score += run * run * 3 // contiguity dominates
		if i >= baseAt {
			score += 8 // in the file name rather than a directory
		}
		if i == baseAt || i == 0 || lower[i-1] == filepath.Separator ||
			lower[i-1] == '_' || lower[i-1] == '-' || lower[i-1] == '.' {
			score += 12 // at a word boundary
		}
		qi++
	}
	if qi < len(query) {
		return 0, nil, false
	}

	if strings.Contains(base, query) {
		score += 200 // the name literally contains what was typed
		if strings.HasPrefix(base, query) {
			score += 100
		}
	} else if strings.Contains(lower, query) {
		score += 60 // contiguous, but somewhere in the path
	}
	return score - gaps*4 - len(path)/4, hits, true
}

// Render draws the overlay centred horizontally in the upper third, where
// VSCode puts it — close to the top so results have room, but not so high it
// looks like part of the tab bar.
func (p *Picker) Render(s *ui.Screen, cols, rows int, th widget.Theme) {
	if !p.Open {
		return
	}
	w := cols * 2 / 3
	if w < 30 {
		w = cols - 4
	}
	if w < 10 {
		return
	}
	h := 15
	if h > rows-4 {
		h = rows - 4
	}
	if h < 5 {
		return
	}
	x, y := (cols-w)/2, 2

	s.Fill(x, y, w, h, ui.DefaultStyle)
	widget.Box(s, x, y, w, h, th.BorderFocus)
	// Inset by one so the field's own border sits inside the overlay's rather
	// than doubling up on it.
	p.input.Render(s, x+2, y+1, w-4, th)

	top := y + 4
	p.list.Rows = h - 5
	p.list.Follow(len(p.shown))
	for row := 0; row < p.list.Rows; row++ {
		i := p.list.Top + row
		if i >= len(p.shown) {
			break
		}
		style := th.Focus(i == p.list.Sel, true)
		label := widget.TruncateLeft(p.shown[i].path, w-4)
		s.Fill(x+1, top+row, w-2, 1, style)
		s.SetString(x+2, top+row, label, style, w-4)
	}
	s.SetString(x+2, y+h-1, " "+itoa(len(p.shown))+" files ", th.Dim, w-4)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
