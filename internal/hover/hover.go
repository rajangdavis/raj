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
//
// Two things the status line could not do are the panel's reason to keep
// growing: an answer past the panel's height is scrolled rather than lost, and
// the markdown a server sends is rendered to a small, explicit subset instead
// of arriving flat. Both are a rendering of the same text model — the server's
// lines — so nothing is thrown away: Text still reports what the server said,
// and the markup is interpreted at draw time.
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

// role is how a run of markdown is drawn, before the theme turns it into an
// appearance. The parser tags runs rather than styling them so the text model
// stays independent of the theme, and so a test can say what the renderer
// understood without reading pixels back out of the cell grid.
type role uint8

const (
	roleText role = iota
	roleCode
	roleBold
	roleItalic
	roleMarker
)

// span is a run of text sharing one role.
type span struct {
	text string
	role role
}

// styledLine is one logical line of rendered markdown.
type styledLine []span

// Panel is the hover box. At most one is open at a time, and it takes no focus:
// it is something shown to you, not something you interact with. It does claim
// the scroll keys while its content overflows, which is why it has to say so —
// see the overflow hint.
type Panel struct {
	Open bool

	// lines is the server's text, one entry per line. It is the text model:
	// markdown is a draw-time rendering of it, so Text can still report exactly
	// what the server said.
	lines []string

	// scroll is the first wrapped body row the panel shows.
	scroll int

	// body, shown and hint are the geometry from the most recent Render. A key
	// arrives between frames, so this is where scrolling learns whether there
	// is anything to scroll and by how much, rather than guessing from the
	// un-wrapped text.
	body  []styledLine
	shown int
	hint  string

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
	body := strings.TrimSpace(text)
	if body == "" {
		p.Hide()
		return
	}
	p.Open = true
	p.lines = strings.Split(body, "\n")
	p.anchorLine, p.anchorCol = line, col
	p.scroll = 0
	p.body = nil
	p.shown = 0
	p.hint = ""
}

// Hide closes the panel.
func (p *Panel) Hide() {
	p.Open = false
	p.lines = nil
	p.scroll = 0
	p.body = nil
	p.shown = 0
	p.hint = ""
}

// Text is what the panel is showing, for tests and for callers that want the
// content without the box. Markdown markers are interpreted — bold and inline
// code lose their punctuation, lists gain their bullet — but a fence is kept
// literally rather than stripped, and code is never treated as prose.
func (p *Panel) Text() string {
	var b strings.Builder
	for i, l := range markdown(p.lines) {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, sp := range l {
			b.WriteString(sp.text)
		}
	}
	return b.String()
}

// Anchor is where the panel is pinned.
func (p *Panel) Anchor() (line, col int) { return p.anchorLine, p.anchorCol }

// Scrollable reports whether there is more content than the last frame showed.
// Keys that scroll ask this before claiming anything, so a panel whose answer
// fits leaves navigation to the document exactly as before.
func (p *Panel) Scrollable() bool {
	return p.Open && p.shown > 0 && len(p.body) > p.shown
}

// Top is the first body row the panel is showing.
func (p *Panel) Top() int { return p.scroll }

// Handle consumes a key if the panel claims it, and reports whether it did.
//
// It always claims escape. It claims the scroll keys — up, down, page up, page
// down — only while Scrollable, because a panel whose answer fits has nothing
// to scroll and taking arrows from the document would be theft. That is also
// why the panel draws an overflow hint: a key claimed silently is a key the
// reader cannot see being claimed.
func (p *Panel) Handle(a keys.Action) bool {
	if !p.Open {
		return false
	}
	if a == keys.Cancel {
		p.Hide()
		return true
	}
	if !p.Scrollable() {
		return false
	}
	switch a {
	case keys.LineUp:
		p.scrollBy(-1)
	case keys.LineDown:
		p.scrollBy(1)
	case keys.PageUp:
		p.scrollBy(-p.shown)
	case keys.PageDown:
		p.scrollBy(p.shown)
	default:
		return false
	}
	return true
}

// scrollBy moves the view and clamps it to the content, so the panel bounds the
// view rather than the view walking off the end.
func (p *Panel) scrollBy(delta int) {
	p.scroll += delta
	if max := len(p.body) - p.shown; p.scroll > max {
		p.scroll = max
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
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
	logical := markdown(p.lines)
	width := p.width(logical, w)
	if width < MinWidth || h < 3 {
		p.body, p.shown, p.hint = nil, 0, ""
		return // no room for a box worth drawing
	}

	// Wrapped to the inside of the border, so a long paragraph reflows rather
	// than being cut at the frame.
	p.body = wrapLines(logical, width-2)
	rows := len(p.body) + 2 // the border
	rows = min(rows, MaxRows)
	rows = min(rows, h)
	shown := rows - 2
	overflow := shown > 0 && len(p.body) > shown
	if overflow {
		// The last row goes to saying there is more, which costs a line of
		// content and buys the reader knowing the panel scrolls.
		shown--
	}
	if shown < 0 {
		shown = 0
	}
	p.shown = shown

	if max := len(p.body) - shown; p.scroll > max {
		p.scroll = max
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	p.hint = ""
	if overflow {
		p.hint = hintText(p.scroll, len(p.body)-(p.scroll+shown))
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
	for i := 0; i < shown && p.scroll+i < len(p.body); i++ {
		drawLine(s, x+1, y+1+i, p.body[p.scroll+i], width-2, th)
	}
	if p.hint != "" {
		s.SetString(x+1, y+rows-2, p.hint, th.Dim, width-2)
	}
}

// width is the widest logical line plus the border, bounded.
func (p *Panel) width(lines []styledLine, avail int) int {
	max := 0
	for _, l := range lines {
		if n := lineCols(l); n > max {
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

// hintText says which way there is more to see, and how much. It is
// deliberately terse: the panel is at its narrowest exactly when it is most
// likely to overflow — a narrow box holds fewer lines — so a long label would
// itself be truncated.
func hintText(above, below int) string {
	switch {
	case above == 0 && below == 0:
		return ""
	case above == 0:
		return more(below)
	case below == 0:
		return "↑ " + itoa(above) + " above"
	default:
		return "↑ " + itoa(above) + " ↓ " + itoa(below)
	}
}

// more is the overflowing-below case, which is the common one: the answer
// starts at the top and continues past the box.
func more(n int) string {
	return "↓ " + itoa(n) + " more"
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

// ---------- markdown ----------

// markdown parses the panel's lines into styled logical lines.
//
// The subset is small and line-based on purpose: fenced code, inline code,
// bold, italic and lists. A construct outside it is left as written, because a
// half-interpreted link or heading reads worse than the characters the server
// sent. Nothing here is a markdown engine and it takes no dependency; the
// point is that a server's answer arrives with its shape instead of flat.
func markdown(lines []string) []styledLine {
	var out []styledLine
	inFence, fence, blank := false, "", false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				// The fence is kept, not stripped: it is the marker that says
				// everything between it and its pair is literal code, and a
				// reader can see where the code begins and ends.
				out = append(out, styledLine{{line, roleMarker}})
				inFence = false
				continue
			}
			out = append(out, styledLine{{line, roleCode}})
			blank = false
			continue
		}
		if marker, ok := fenceOpen(trimmed); ok {
			inFence, fence = true, marker
			out = append(out, styledLine{{line, roleMarker}})
			blank = false
			continue
		}
		if trimmed == "" {
			// Blank runs collapse outside a fence. Vertical space is what the
			// panel has least of, and a server separating two paragraphs often
			// sends several blank lines doing it.
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, styledLine(nil))
			continue
		}
		blank = false
		if item, ok := listItem(line); ok {
			out = append(out, item)
			continue
		}
		out = append(out, inline(line))
	}
	for len(out) > 0 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	return out
}

// fenceOpen reports whether a line opens a fenced code block, and the run of
// marks — the fence — that closes it.
func fenceOpen(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return trimmed[:fenceRun(trimmed, '`')], true
	case strings.HasPrefix(trimmed, "~~~"):
		return trimmed[:fenceRun(trimmed, '~')], true
	}
	return "", false
}

// fenceRun counts the leading run of ch, so a closing fence has to be at least
// as long as the run that opened the block: a shorter line is code, not a
// close.
func fenceRun(trimmed string, ch byte) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	return n
}

// listItem recognises an unordered or ordered list item at the start of a line,
// keeping the indentation so a nested list stays nested. The marker is styled
// separately from the text so a reader can see it is a bullet and not prose.
func listItem(line string) (styledLine, bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	indent, rest := line[:i], line[i:]
	if len(rest) >= 2 && (rest[0] == '-' || rest[0] == '*' || rest[0] == '+') && rest[1] == ' ' {
		out := styledLine{}
		if indent != "" {
			out = append(out, span{indent, roleText})
		}
		out = append(out, span{"• ", roleMarker})
		return append(out, inline(rest[2:])...), true
	}
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j > 0 && j+1 < len(rest) && rest[j] == '.' && rest[j+1] == ' ' {
		out := styledLine{}
		if indent != "" {
			out = append(out, span{indent, roleText})
		}
		out = append(out, span{rest[:j+2], roleMarker})
		return append(out, inline(rest[j+2:])...), true
	}
	return nil, false
}

// inline parses emphasis and inline code in one line of prose. It is not
// recursive: nested emphasis is left as written rather than half-parsed, and a
// marker pair must flank a word so plain text is left alone.
func inline(s string) styledLine {
	var out styledLine
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, span{text.String(), roleText})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				flush()
				out = append(out, span{s[i+1 : i+1+j], roleCode})
				i += j + 2
				continue
			}
		case strings.HasPrefix(s[i:], "**"):
			// A marker pair counts only when it flanks a word: the byte just
			// inside each marker must be a word byte. Without this, plain
			// prose with asterisks like `2 * 3 * 4` is read as emphasis.
			j := strings.Index(s[i+2:], "**")
			if j >= 0 && wordByte(s, i+2) && wordByte(s, i+j+1) {
				flush()
				out = append(out, span{s[i+2 : i+2+j], roleBold})
				i += j + 4
				continue
			}
		case s[i] == '*':
			j := strings.IndexByte(s[i+1:], '*')
			if j >= 0 && wordByte(s, i+1) && wordByte(s, i+j) {
				flush()
				out = append(out, span{s[i+1 : i+1+j], roleItalic})
				i += j + 2
				continue
			}
		case s[i] == '_' && !wordByte(s, i-1):
			// Only a boundary underscore opens emphasis, so an identifier like
			// max_width_count is left alone.
			if j := strings.IndexByte(s[i+1:], '_'); j >= 0 && !wordByte(s, i+1+j+1) {
				flush()
				out = append(out, span{s[i+1 : i+1+j], roleItalic})
				i += j + 2
				continue
			}
		}
		text.WriteByte(s[i])
		i++
	}
	flush()
	return out
}

// wordByte reports whether the byte at i is part of a word, which is how the
// underscore rule tells emphasis from an identifier.
func wordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---------- layout ----------

// wrapLines wraps every logical line to the width, producing the body rows the
// panel scrolls through.
func wrapLines(lines []styledLine, width int) []styledLine {
	var out []styledLine
	for _, l := range lines {
		out = append(out, wrapLine(l, width)...)
	}
	return out
}

// wrapLine breaks one logical line to width display columns, on spaces where it
// can. Leading whitespace is preserved on continuation rows of code so a
// wrapped signature keeps its indentation, which is most of what makes it still
// readable; prose continuation rows start at the margin.
func wrapLine(spans styledLine, width int) []styledLine {
	if width < 4 {
		return []styledLine{spans}
	}
	rs, roles := flatten(spans)
	if len(rs) == 0 {
		return []styledLine{{}}
	}
	if runeCols(rs) <= width {
		return []styledLine{groupSpans(rs, roles)}
	}

	indent := 0
	for indent < len(rs) && (rs[indent] == ' ' || rs[indent] == '\t') {
		indent++
	}
	code := indent < len(rs) && roles[indent] == roleCode

	var rows []styledLine
	start, first := 0, true
	for start < len(rs) {
		var pref []rune
		var prefRoles []role
		if !first && code {
			pref, prefRoles = rs[:indent], roles[:indent]
		}
		avail := width - runeCols(pref)
		avail = max(avail, 1)
		end, lastSpace, used := start, -1, 0
		for end < len(rs) {
			w := ui.RuneWidth(rs[end])
			w = max(w, 1)
			if used+w > avail {
				break
			}
			used += w
			if rs[end] == ' ' {
				lastSpace = end
			}
			end++
		}
		if end < len(rs) && lastSpace > start {
			end = lastSpace // break at the last space so words stay whole
		}
		if end == start {
			end = start + 1 // a wide rune alone in a one-column budget
		}
		rowRunes := append(append([]rune(nil), pref...), rs[start:end]...)
		rowRoles := append(append([]role(nil), prefRoles...), roles[start:end]...)
		rows = append(rows, groupSpans(rowRunes, rowRoles))
		start = end
		if !first {
			for start < len(rs) && rs[start] == ' ' {
				start++
			}
		}
		first = false
	}
	return rows
}

// flatten turns a styled line into its runes and a parallel role per rune, so
// wrapping can break anywhere without losing what a run meant.
func flatten(spans styledLine) ([]rune, []role) {
	var rs []rune
	var roles []role
	for _, sp := range spans {
		for _, r := range sp.text {
			rs = append(rs, r)
			roles = append(roles, sp.role)
		}
	}
	return rs, roles
}

// groupSpans is flatten's inverse: adjacent runes that share a role become one
// span again.
func groupSpans(rs []rune, roles []role) styledLine {
	var out styledLine
	for i := 0; i < len(rs); {
		j := i + 1
		for j < len(rs) && roles[j] == roles[i] {
			j++
		}
		out = append(out, span{string(rs[i:j]), roles[i]})
		i = j
	}
	return out
}

// runeCols is a rune slice's display width.
func runeCols(rs []rune) (n int) {
	for _, r := range rs {
		n += ui.RuneWidth(r)
	}
	return
}

// lineCols is a styled line's display width.
func lineCols(l styledLine) (n int) {
	for _, sp := range l {
		for _, r := range sp.text {
			n += ui.RuneWidth(r)
		}
	}
	return
}

// ---------- drawing ----------

// drawLine writes one styled body row, mapping each span's role to the theme.
func drawLine(s *ui.Screen, x, y int, l styledLine, max int, th widget.Theme) {
	col := 0
	for _, sp := range l {
		if col >= max {
			break
		}
		col += s.SetString(x+col, y, sp.text, roleStyle(sp.role, th), max-col)
	}
}

// roleStyle is the mapping from a role to an appearance. It is the one place
// the theme meets the markdown, and it is deliberately derived from the panel's
// text style rather than from named theme slots: the theme has no slot for code
// or emphasis, and adding one to the shared widget for a single caller would
// make every other pane carry it.
func roleStyle(r role, th widget.Theme) ui.Style {
	switch r {
	case roleCode:
		return th.Text.With(ui.Ansi(6)) // cyan: visibly not prose
	case roleBold:
		return th.Text.Plus(ui.Bold)
	case roleItalic:
		return th.Text.Plus(ui.Italic)
	case roleMarker:
		return th.Dim
	}
	return th.Text
}
