package complete

import (
	"raj/internal/hover"
	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// Popup is the completion list drawn beside the cursor.
//
// It is not a Picker mode. The picker is a centred modal that takes focus and
// whose query is a field you are editing; completion is anchored to the caret,
// takes no focus, and its "query" is the buffer text behind the cursor. Sharing
// the widget would mean a modal that is sometimes not modal and a field that is
// sometimes not a field. What they do share is the list, which is the part
// worth reusing.
//
// Keys reach it before the editor only while it is open, and only the handful
// it claims — up, down, tab, enter, escape, and the page keys while there is
// documentation to scroll. Everything else falls through and types, which is
// what keeps the popup from being modal in practice: you can ignore it entirely
// and keep typing.
type Popup struct {
	Open bool

	items  []Candidate
	list   widget.List
	prefix string

	// doc is the documentation panel for the highlighted candidate. It is the
	// hover panel rather than a second renderer, because that is the code that
	// already reads the markdown subset and scrolls. It is a sub-panel of the
	// list rather than a sibling overlay so the list's own up/down navigation
	// keeps working: only the page keys, which the list does not use, are
	// forwarded to it.
	doc     hover.Panel
	docText string

	// anchorLine and anchorCol are where the word being completed starts, in
	// document coordinates. The popup is placed from there rather than from the
	// caret so it does not walk sideways as you type.
	anchorLine, anchorCol int
}

// MaxRows is how tall the popup gets. Deliberately short: it sits over the code
// being written, and a list that covers the function you are calling is worse
// than a list that scrolls.
const MaxRows = 8

// Show opens the popup with candidates for prefix, anchored where the word
// starts. It closes itself when there is nothing to offer, so a caller can call
// it unconditionally after every keystroke.
func (p *Popup) Show(prefix string, cands []Candidate, line, col int) {
	if len(cands) == 0 {
		p.Hide()
		return
	}
	p.Open = true
	p.items = cands
	p.prefix = prefix
	p.anchorLine, p.anchorCol = line, col
	p.list.Reset()
	p.setDoc(p.selectedDoc())
}

// Hide closes the popup.
func (p *Popup) Hide() {
	p.Open = false
	p.items = nil
	p.prefix = ""
	p.doc.Hide()
	p.docText = ""
}

// Prefix is the text the current candidates complete, which a caller needs to
// know how much to replace when one is accepted.
func (p *Popup) Prefix() string { return p.prefix }

// Anchor is where the word being completed starts, so a caller replacing the
// candidate list can put the popup back in the same place.
func (p *Popup) Anchor() (line, col int) { return p.anchorLine, p.anchorCol }

// Count is how many candidates are showing.
func (p *Popup) Count() int { return len(p.items) }

// Selected is the highlighted candidate, or false when the popup is closed.
func (p *Popup) Selected() (Candidate, bool) {
	if !p.Open || p.list.Sel >= len(p.items) {
		return Candidate{}, false
	}
	return p.items[p.list.Sel], true
}

// selectedDoc is the documentation of the highlighted candidate, or "" when
// nothing is selected. It is the one expression Show, navigation and Render
// each use, so the panel follows the highlight the same way whichever path
// moved it.
func (p *Popup) selectedDoc() string {
	c, _ := p.Selected()
	return c.Documentation
}

// SetResolved records a resolved item's documentation and detail on every
// candidate carrying its key, and refreshes the panel when the highlighted
// candidate is one of them. Detail is replaced only when the server sent one,
// so a resolve that fills documentation cannot blank a detail already shown.
func (p *Popup) SetResolved(key, doc, detail string) {
	if key == "" {
		return
	}
	for i := range p.items {
		if p.items[i].ResolveKey != key {
			continue
		}
		if doc != "" {
			p.items[i].Documentation = doc
		}
		if detail != "" {
			p.items[i].Detail = detail
		}
	}
	if c, ok := p.Selected(); ok && c.ResolveKey == key {
		p.setDoc(c.Documentation)
	}
}

// DocText is the documentation shown for the highlighted candidate, as the
// markdown subset renders it. For tests and for a caller that wants the text
// without the box.
func (p *Popup) DocText() string { return p.doc.Text() }

// setDoc points the documentation panel at text, hiding it when there is none.
// It is the one place the panel is opened and closed, so Show, a resolved item
// and the render path cannot disagree about what it shows. An unchanged text is
// left alone so the reader's scroll position survives a redraw.
func (p *Popup) setDoc(text string) {
	if text == p.docText && p.doc.Open == (text != "") {
		return
	}
	p.docText = text
	if text == "" {
		p.doc.Hide()
		return
	}
	p.doc.Show(text, -1, 0)
}

// Handle consumes a key if the popup claims it, and reports whether it did.
//
// The claimed set is deliberately small. Every key the popup takes is a key the
// editor does not get, and a completion list that swallows keystrokes is worse
// than no completion list — so anything not listed here falls through and types
// normally, closing the popup only when the caller decides the word has ended.
func (p *Popup) Handle(a keys.Action) (accepted Candidate, ok bool, consumed bool) {
	if !p.Open {
		return Candidate{}, false, false
	}
	switch a {
	case keys.LineUp:
		p.list.Move(-1, len(p.items))
		p.setDoc(p.selectedDoc())
		return Candidate{}, false, true
	case keys.LineDown:
		p.list.Move(+1, len(p.items))
		p.setDoc(p.selectedDoc())
		return Candidate{}, false, true
	case keys.Cancel:
		p.Hide()
		return Candidate{}, false, true
	case keys.PageUp, keys.PageDown:
		// The list navigates with up/down and leaves the page keys to the
		// documentation, so a long doc scrolls without stealing a keystroke
		// navigation already means something else by.
		if p.doc.Handle(a) {
			return Candidate{}, false, true
		}
	case keys.Indent, keys.Confirm:
		// Tab and enter both accept. Tab because it is what every editor uses
		// and what the fingers expect; enter because refusing it means the
		// list is showing a highlighted row that enter does not take, which
		// nobody believes.
		c, has := p.Selected()
		p.Hide()
		return c, has, true
	}
	return Candidate{}, false, false
}

// Render draws the popup near its anchor, given the editor's screen origin and
// the first document line on screen.
//
// It is placed below the anchor when there is room and above when there is not,
// because the alternative is a list that runs off the bottom of the terminal
// exactly when the cursor is near it — which is most of the time, since that is
// where people write.
func (p *Popup) Render(s *ui.Screen, originX, originY, w, h, topLine int, th widget.Theme) {
	if !p.Open || len(p.items) == 0 {
		return
	}
	rows := len(p.items)
	if rows > MaxRows {
		rows = MaxRows
	}
	p.list.Rows = rows

	width := p.width()
	if width > w {
		width = w
	}
	if width < 4 || rows < 1 {
		return
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

	// The documentation is drawn before the list, so a panel that has to extend
	// upward is covered by the list rather than covering it. The panel does its
	// own placement: it flips up and clamps when the rows below do not fit, and
	// the list drawn afterward hides any overlap.
	p.setDoc(p.selectedDoc())
	if p.doc.Open {
		bottom := y + rows
		p.doc.Render(s, x, bottom, originX+w-x, originY+h-bottom, 0, th)
	}

	p.list.Follow(len(p.items))
	for row := 0; row < rows; row++ {
		i := p.list.Top + row
		if i >= len(p.items) {
			break
		}
		// Focused rather than not: the popup is only drawn when it is the thing
		// being interacted with, so its selection should read as live.
		style := th.Focus(i == p.list.Sel, true)
		s.Fill(x, y+row, width, 1, style)
		label := p.items[i].Word
		if d := p.items[i].Detail; d != "" {
			label += "  " + d
		}
		s.SetString(x, y+row, widget.Truncate(" "+label, width), style, width)
	}
}

// width is the widest label plus padding, bounded so a long identifier does not
// take the screen.
func (p *Popup) width() int {
	max := 0
	for _, c := range p.items {
		n := len(c.Word) + 2
		if c.Detail != "" {
			n += len(c.Detail) + 2
		}
		if n > max {
			max = n
		}
	}
	if max > 48 {
		max = 48
	}
	return max
}
