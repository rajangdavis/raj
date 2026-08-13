package complete

import (
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
// it claims — up, down, tab, enter, escape. Everything else falls through and
// types, which is what keeps the popup from being modal in practice: you can
// ignore it entirely and keep typing.
type Popup struct {
	Open bool

	items  []Candidate
	list   widget.List
	prefix string

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
}

// Hide closes the popup.
func (p *Popup) Hide() {
	p.Open = false
	p.items = nil
	p.prefix = ""
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
		return Candidate{}, false, true
	case keys.LineDown:
		p.list.Move(+1, len(p.items))
		return Candidate{}, false, true
	case keys.Cancel:
		p.Hide()
		return Candidate{}, false, true
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
