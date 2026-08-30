// Package hover is the floating panel that shows what a language server says
// about the thing under the cursor.
//
// It exists because the status line was the wrong home for it. A signature
// folded onto one row loses the shape that makes a signature readable — the
// parameter list, the return, the line breaks a server put there deliberately —
// and anything longer than the terminal is simply cut off. A paragraph of
// documentation in a status bar is a paragraph nobody reads.
//
// The placement problem was already solved by the completion popup: anchor to
// the cursor, flip above when there is no room below, slide left rather than
// clip. That logic is duplicated here rather than shared, and the duplication is
// deliberate — see Render.
package hover

import (
	"strings"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Bounds on the panel. It sits over the code being read, so it has to be big
// enough to hold a signature and small enough not to bury the function the
// signature belongs to.
const (
	MaxRows  = 12
	MaxWidth = 72
	MinWidth = 16
)

// Panel is the hover box. At most one is open at a time, and it takes no focus:
// it is something shown to you, not something you interact with.
type Panel struct {
	Open bool

	lines []string
	// truncated is how many lines did not fit, so the panel can say that it is
	// not showing everything rather than silently ending mid-sentence.
	truncated int

	// anchorLine and anchorCol are where the cursor was when the hover was
	// asked for, in document coordinates. Anchoring to the request rather than
	// to the live cursor means the panel does not drift if anything moves the
	// cursor between the question and the answer.
	anchorLine, anchorCol int
}

// Show opens the panel with a server's text, anchored at a document position.
//
// Empty text closes it rather than opening an empty box, so a caller can call
// this unconditionally with whatever the server returned.
func (p *Panel) Show(text string, line, col int) {
	body := clean(text)
	if body == "" {
		p.Hide()
		return
	}
	p.Open = true
	p.lines = strings.Split(body, "\n")
	p.anchorLine, p.anchorCol = line, col
	p.truncated = 0
}

// Hide closes the panel.
func (p *Panel) Hide() {
	p.Open = false
	p.lines = nil
	p.truncated = 0
}

// Text is what the panel is showing, for tests and for callers that want the
// content without the box.
func (p *Panel) Text() string { return strings.Join(p.lines, "\n") }

// Anchor is where the panel is pinned.
func (p *Panel) Anchor() (line, col int) { return p.anchorLine, p.anchorCol }

// Handle consumes a key if the panel claims it, and reports whether it did.
//
// It claims exactly one: escape. Every key a panel takes is a key the editor
// does not get, and unlike the completion popup this one can sit on screen
// while you carry on reading — so claiming arrows to scroll it would steal
// navigation from the document underneath at the moment you most want it.
// Content that does not fit is reported as not fitting instead.
func (p *Panel) Handle(a keys.Action) bool {
	if p.Open && a == keys.Cancel {
		p.Hide()
		return true
	}
	return false
}

// Render draws the panel near its anchor, given the editor's screen origin and
// the first document line on screen.
//
// The placement rules are the completion popup's, restated rather than shared.
// Factoring them out would mean a common widget parameterised by anchor, size,
// border and content — and the two differ in all four. What they have in common
// is three lines of arithmetic and a reason, and the reason is worth repeating
// where it applies: a box that runs off the bottom of the terminal does so
// exactly when the cursor is near the bottom, which is where people write.
func (p *Panel) Render(s *ui.Screen, originX, originY, w, h, topLine int, th widget.Theme) {
	if !p.Open || len(p.lines) == 0 {
		return
	}
	width := p.width(w)
	if width < MinWidth || h < 3 {
		return // no room for a box worth drawing
	}

	// Wrapped to the inside of the border, so a long paragraph reflows rather
	// than being cut at the frame.
	body := wrap(p.lines, width-2)
	rows := len(body) + 2 // the border
	if rows > MaxRows {
		rows = MaxRows
	}
	if rows > h {
		rows = h
	}
	shown := rows - 2
	p.truncated = len(body) - shown
	if p.truncated > 0 {
		// The last row goes to saying so, which costs a line of content and
		// buys the reader knowing there is more.
		shown--
		p.truncated++
	}

	x := originX + p.anchorCol
	if x+width > originX+w {
		x = originX + w - width // slide left rather than clip the words
	}
	if x < originX {
		x = originX
	}

	y := originY + (p.anchorLine - topLine) + 1
	if y+rows > originY+h {
		if above := originY + (p.anchorLine - topLine) - rows; above >= originY {
			y = above
		} else {
			y = originY + h - rows
		}
	}
	if y < originY {
		y = originY
	}

	// Filled before the border so the code underneath does not show through
	// the gaps between words.
	s.Fill(x, y, width, rows, th.Text)
	widget.Box(s, x, y, width, rows, th.Border)
	for i := 0; i < shown && i < len(body); i++ {
		s.SetString(x+1, y+1+i, body[i], th.Text, width-2)
	}
	if p.truncated > 0 {
		s.SetString(x+1, y+rows-2, more(p.truncated), th.Dim, width-2)
	}
}

// width is the widest line plus the border, bounded.
func (p *Panel) width(avail int) int {
	max := 0
	for _, l := range p.lines {
		if n := cols(l); n > max {
			max = n
		}
	}
	max += 2 // the border
	// MinWidth is a floor to pad up to, not a threshold to reject: a one-word
	// answer still deserves a box, and a box narrower than this reads as a
	// rendering fault rather than as a short answer.
	if max < MinWidth {
		max = MinWidth
	}
	if max > MaxWidth {
		max = MaxWidth
	}
	if max > avail {
		max = avail // and the caller rejects it if that is too narrow
	}
	return max
}

// more is deliberately terse. The panel is at its narrowest exactly when it is
// most likely to overflow — a narrow box holds fewer lines — so a long label
// would itself be truncated, and "… 31 more line" reads as a bug.
func more(n int) string {
	return "+" + itoa(n) + " more"
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// clean strips the markdown a server wraps its answer in.
//
// gopls returns fenced code blocks, so the raw text contains ``` lines that
// mean "the next part is code" to a markdown renderer and mean nothing at all
// in a terminal box — where they read as three stray backticks above the
// signature you asked for. The fences go; what they contained stays exactly as
// it was, because inside them is the code, and code is the part whose spacing
// matters.
//
// Nothing else about the markdown is interpreted. Bold and links are left as
// written: a half-rendered markdown subset is more confusing than none, and the
// text is readable either way.
func clean(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			continue
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	// Blank runs collapse: removing fences frequently leaves two blank lines
	// where there was one, and vertical space is what the panel has least of.
	trimmed := make([]string, 0, len(out))
	blank := false
	for _, l := range out {
		if l == "" {
			if blank || len(trimmed) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		trimmed = append(trimmed, l)
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return strings.Join(trimmed, "\n")
}

// wrap breaks lines to a width, on spaces where it can.
//
// Leading whitespace is preserved on continuation rows so that wrapped code
// keeps its indentation, which is most of what makes a wrapped signature still
// readable.
func wrap(lines []string, width int) []string {
	if width < 4 {
		return lines
	}
	var out []string
	for _, l := range lines {
		if cols(l) <= width {
			out = append(out, l)
			continue
		}
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		rest := l
		first := true
		for cols(rest) > width {
			prefix := ""
			if !first {
				prefix = indent
			}
			take := fit(rest, width-cols(prefix))
			out = append(out, prefix+rest[:take])
			rest = strings.TrimLeft(rest[take:], " ")
			first = false
			if take == 0 {
				break // nothing fits; give up rather than loop
			}
		}
		if rest != "" {
			if !first {
				rest = indent + rest
			}
			out = append(out, rest)
		}
	}
	return out
}

// fit is how many bytes of s occupy at most width columns, preferring to break
// at the last space so words stay whole.
func fit(s string, width int) int {
	if width < 1 {
		return 0
	}
	at, last, lastSpace := 0, 0, -1
	for i, r := range s {
		rw := ui.RuneWidth(r)
		if at+rw > width {
			break
		}
		at += rw
		last = i + len(string(r))
		if r == ' ' {
			lastSpace = i
		}
	}
	if lastSpace > 0 {
		return lastSpace
	}
	return last
}

// cols is a string's display width, measured the way the renderer measures it.
func cols(s string) (n int) {
	for _, r := range s {
		n += ui.RuneWidth(r)
	}
	return
}
