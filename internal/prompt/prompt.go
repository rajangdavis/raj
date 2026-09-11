// Package prompt is the modal dialog: a floating overlay that asks exactly one
// question and hands the answer back so the caller can resume what it was
// doing.
//
// Three shapes, one type. Ask takes a line of text (where should this file go?);
// Confirm picks between labelled buttons (this has unsaved changes — now what?);
// Review is a Confirm with a listing attached: the rows name what the answer
// would be about, and stepping through them comes first (accept the pending
// change sets — now?).
// They share the type because they share every hard part: they take all keys
// while open, they can be dismissed, and their answer arrives later than the
// keystroke that opened them.
//
// The answer is delivered to a continuation rather than returned, because the
// interesting flows are chains. Closing a dirty unnamed buffer asks whether to
// save, which asks for a path, which may ask whether to overwrite — and only
// then closes the tab. A returned value would make each of those a separate
// piece of state in the caller that can disagree with the others.
package prompt

import (
	"fmt"
	"strings"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// The labels a save-or-discard question offers. Exported because the caller
// matches on the answer it gets back, and a shared constant is the only thing
// that stops that comparison from drifting away from what was drawn on screen.
const (
	Save    = "Save"
	Discard = "Don't Save"
	Cancel  = "Cancel"

	// Overwrite is the affirmative answer to an existing file.
	Overwrite = "Overwrite"

	// Reload takes what is on disk and drops the buffer. Its own word rather
	// than a second Discard, because what is being discarded is the thing the
	// user has in front of them rather than the thing on disk, and those two
	// answers sit next to each other in the same dialog.
	Reload = "Reload"

	// Create is the affirmative answer to a directory that does not exist yet.
	// A separate word from Save because the question is not "save?" — it is
	// "make this directory?", and answering it makes something on disk that the
	// user did not explicitly ask for.
	Create = "Create"
)

// SaveOptions is the standard three-way answer to unsaved changes. Cancel is
// last and Save first, so the default selection is the non-destructive one.
func SaveOptions() []string { return []string{Save, Discard, Cancel} }

type kind int

const (
	ask kind = iota
	confirm
	review
)

// Prompt is a modal question. Only one is open at a time: a dialog that can be
// stacked is a dialog you can get lost in, and every chain here replaces its
// predecessor rather than nesting under it.
type Prompt struct {
	Open bool

	// Complete is the tab hook; see SetComplete.
	Complete func(string) string

	kind    kind
	title   string
	message string
	input   widget.Input
	options []string
	sel     int
	done    func(answer string, ok bool)

	// A Review kind also lists rows to step through before answering. moved is
	// the follow-up to a row becoming current — the app jumps the caret.
	rows  []string
	row   int
	moved func(row int)
}

// New returns a closed prompt.
func New() *Prompt { return &Prompt{} }

// Ask opens a single-line question seeded with initial, caret at the end. Use
// it when the seed is a prefix to keep typing after — a save-as path is the
// directory, not the answer.
func (p *Prompt) Ask(title, initial string, done func(answer string, ok bool)) {
	p.AskComplete(title, initial, nil, done)
}

// AskComplete is Ask with a tab-completion hook.
//
// The hook is a parameter rather than a field set beforehand, because Ask
// resets the whole struct — which is right, since a stale option list or a
// stale continuation from the last question would be far worse than a stale
// completer. A field set before the call was silently wiped by that reset, and
// the failure looked like tab not being delivered at all.
func (p *Prompt) AskComplete(title, initial string, complete func(string) string,
	done func(answer string, ok bool)) {
	*p = Prompt{Open: true, kind: ask, title: title, done: done, Complete: complete}
	p.input.Focused = true
	p.input.SetText(initial)
}

// AskSuggestion opens the same question with the seed selected, so the first
// keystroke replaces it.
//
// The distinction is the whole point: a seed is either context to build on or a
// default to overwrite, and the field cannot tell which. Go-to-line seeds the
// line you are on so the dialog says where you are — but nobody types 1 to mean
// line 11, so leaving the caret at the end turned every jump into a select-all
// first. Enter still accepts the suggestion untouched.
func (p *Prompt) AskSuggestion(title, suggestion string, done func(answer string, ok bool)) {
	p.Ask(title, suggestion, done)
	p.input.SelectAll()
}

// Confirm opens a choice between labels, the first selected.
func (p *Prompt) Confirm(title, message string, options []string, done func(answer string, ok bool)) {
	*p = Prompt{Open: true, kind: confirm, title: title, message: message,
		options: options, done: done}
}

// Review opens a Confirm with a listing attached: rows name what the answer
// would be about, and up and down step the current row through them while the
// buttons stay the answer. moved is a follow-up hook, cheap enough that a row
// becoming current can, say, move the caret.
func (p *Prompt) Review(title, message string, rows []string, options []string,
	moved func(row int), done func(answer string, ok bool)) {
	*p = Prompt{Open: true, kind: review, title: title, message: message,
		rows: rows, options: options, moved: moved, done: done}
}

// ActiveInput is the field of an open text question, and nil for a button
// choice — there is nothing to select in a row of buttons.
func (p *Prompt) ActiveInput() *widget.Input {
	if !p.Open || p.kind != ask {
		return nil
	}
	return &p.input
}

// Complete is the tab-completion hook for an Ask field, given the current text
// and returning what it should become. Nil, or a return of "" or of the text
// unchanged, means no completion — which is also how "no matches" is said, so
// a field with nothing to complete simply does nothing rather than beeping.
//
// A hook rather than a path completer built in, because prompt knows about
// fields and dialogs and has no business knowing about directories. What can
// be completed depends on what is being asked for. Set it through AskComplete.
// Title is what the dialog is asking about, for the status line.
func (p *Prompt) Title() string { return p.title }

// Text is the current contents of an Ask field, for tests.
func (p *Prompt) Text() string { return p.input.Text }

// Selected is the highlighted option of a button-choice question — Confirm
// or Review — for tests.
func (p *Prompt) Selected() string {
	if p.kind != confirm && p.kind != review {
		return ""
	}
	if p.sel >= len(p.options) {
		return ""
	}
	return p.options[p.sel]
}

// Handle applies one action. Unrecognised actions are swallowed rather than
// passed on: the prompt is modal, so a stray cmd+w while it is open must not
// close the very tab the question is about.
func (p *Prompt) Handle(a keys.Action, text string) {
	if !p.Open {
		return
	}
	switch a {
	case keys.Cancel:
		p.finish("", false)
		return
	case keys.Confirm:
		p.finish(p.answer(), true)
		return
	}
	if p.kind == ask && a == keys.Indent {
		// Tab completes rather than indenting. There is nothing to indent in a
		// one-line field, and a path field that does not complete is the thing
		// that made save-as feel like a text box rather than a file dialog.
		if p.Complete != nil {
			if done := p.Complete(p.input.Text); done != "" && done != p.input.Text {
				p.input.SetText(done)
			}
		}
		return
	}
	if p.kind == review {
		// Two axes on purpose: left and right pick the answer, up and down
		// step through the listing.
		switch a {
		case keys.CharLeft:
			p.move(-1)
		case keys.CharRight:
			p.move(+1)
		case keys.LineUp:
			p.moveRow(-1)
		case keys.LineDown:
			p.moveRow(+1)
		}
		return
	}
	if p.kind == confirm {
		// Arrows only. Buttons are a row, but a vertical arrow meaning the
		// same thing costs nothing and saves guessing which axis this dialog
		// laid them out on.
		switch a {
		case keys.CharLeft, keys.LineUp:
			p.move(-1)
		case keys.CharRight, keys.LineDown:
			p.move(+1)
		}
		return
	}
	p.input.Handle(a, text)
}

func (p *Prompt) move(d int) {
	if len(p.options) == 0 {
		return
	}
	p.sel = (p.sel + d + len(p.options)) % len(p.options)
}

// moveRow steps the review listing, wrapping, and runs the follow-up hook so
// whatever the current row names can follow too.
func (p *Prompt) moveRow(d int) {
	if len(p.rows) == 0 {
		return
	}
	p.row = (p.row + d + len(p.rows)) % len(p.rows)
	if p.moved != nil {
		p.moved(p.row)
	}
}

// reviewMaxRows is the most of a review listing the dialog will draw before it
// scrolls. A save can carry more pending sets than a dialog can show, and
// burying the buttons is how they stop being reachable.
const reviewMaxRows = 8

// reviewWindow is the slice of rows the dialog draws, with the current one
// always inside it.
func (p *Prompt) reviewWindow() (first, shown int) {
	shown = len(p.rows)
	if shown > reviewMaxRows {
		shown = reviewMaxRows
	}
	if p.row >= shown {
		first = p.row - shown + 1
	}
	if first > len(p.rows)-shown {
		first = len(p.rows) - shown
	}
	return first, shown
}

// buttonsAt is where the options sit, counted in dialog rows: after the
// listing when there is one, where they have always sat otherwise.
func (p *Prompt) buttonsAt() int {
	if p.kind == review {
		_, shown := p.reviewWindow()
		return 2 + shown
	}
	return buttonRow
}

func (p *Prompt) answer() string {
	if p.kind == confirm || p.kind == review {
		return p.Selected()
	}
	return strings.TrimSpace(p.input.Text)
}

// finish closes the prompt BEFORE delivering the answer, so a continuation is
// free to open the next question in a chain without this one reopening over the
// top of it.
func (p *Prompt) finish(answer string, ok bool) {
	done := p.done
	p.Open, p.done = false, nil
	if done != nil {
		done(answer, ok)
	}
}

// Render draws the dialog centred, since unlike the file picker it is a
// question rather than a list: there is nothing below it that wants the room,
// and the middle of the screen is where the eye already is.
// box is where the dialog sits on a screen of the given size, and whether it
// fits at all. Render and ClickAt share it so a press cannot resolve against a
// rectangle other than the one that was drawn.
func (p *Prompt) box(cols, rows int) (x, y, w, h int, ok bool) {
	w = cols * 2 / 3
	if w > 72 {
		w = 72
	}
	if w > cols-4 {
		w = cols - 4
	}
	h = 5
	if p.kind == ask {
		h = 1 + widget.Height + 2 // the field draws its own three-row border
	}
	if p.kind == review {
		// The listing sits between the question and the buttons.
		_, shown := p.reviewWindow()
		h = 4 + shown
	}
	if w < 24 || h > rows-2 {
		return 0, 0, 0, 0, false
	}
	return (cols - w) / 2, (rows - h) / 2, w, h, true
}

// buttonRow is where Confirm draws its options, relative to the dialog's top.
const buttonRow = 3

// ClickAt handles a press at a screen cell while a dialog is open. It reports
// whether the press was inside it — a dialog is modal, so a press outside is
// swallowed rather than reaching whatever is drawn underneath.
//
// A press on a button answers the question rather than merely selecting it.
// Selecting and then requiring a second press would make the pointer slower
// than the keyboard, and the button is already the answer written out.
func (p *Prompt) ClickAt(cols, rows, col, row int) bool {
	if !p.Open {
		return false
	}
	x, y, w, h, ok := p.box(cols, rows)
	if !ok || col < x || col >= x+w || row < y || row >= y+h {
		return false
	}
	dx, dy := col-x, row-y
	if p.kind == ask {
		p.input.ClickAt(dx-2, dy-1, w-4)
		return true
	}
	if p.kind == review && dy >= 2 && dy < p.buttonsAt() {
		// A press on a row selects it, and the follow-up hook fires the same as
		// an arrow would — the pointer is not a shortcut past the listing.
		first, _ := p.reviewWindow()
		if idx := first + dy - 2; idx < len(p.rows) {
			p.row = idx
			if p.moved != nil {
				p.moved(p.row)
			}
		}
		return true
	}
	if dy == p.buttonsAt() {
		for i, sp := range p.buttons(w) {
			if dx >= sp.Start && dx < sp.End {
				p.sel = i
				p.finish(p.options[i], true)
				return true
			}
		}
	}
	return true
}

// span is a drawn button's columns, [Start, End), relative to the dialog.
type span struct{ Start, End int }

// buttons lays the options out across the dialog's width. Both the renderer and
// the pointer go through it, so a button cannot be drawn in one place and
// pressed in another.
func (p *Prompt) buttons(w int) []span {
	total := 0
	for _, o := range p.options {
		total += len(o) + 4
	}
	bx := (w - total) / 2
	if bx < 1 {
		bx = 1
	}
	out := make([]span, len(p.options))
	for i, o := range p.options {
		n := len(o) + 4
		out[i] = span{Start: bx, End: bx + n}
		bx += n
	}
	return out
}

func (p *Prompt) Render(s *ui.Screen, cols, rows int, th widget.Theme) {
	if !p.Open {
		return
	}
	x, y, w, h, ok := p.box(cols, rows)
	if !ok {
		return
	}

	s.Fill(x, y, w, h, ui.DefaultStyle)
	widget.Box(s, x, y, w, h, th.BorderFocus)
	title := p.title
	if p.kind == review && len(p.rows) > 0 {
		// The current row's index is part of the question being asked.
		title = fmt.Sprintf("%s %d/%d", p.title, p.row+1, len(p.rows))
	}
	s.SetString(x+2, y, " "+title+" ", th.BorderFocus, w-4)

	if p.kind == ask {
		// Inset by one so the field's border sits inside the dialog's rather
		// than doubling up on it.
		p.input.Render(s, x+2, y+1, w-4, th)
		s.SetString(x+2, y+1+widget.Height, "enter  save      esc  cancel", th.Dim, w-4)
		return
	}
	s.SetString(x+2, y+1, widget.Truncate(p.message, w-4), th.Text, w-4)
	if p.kind == review {
		// The listing between the question and the buttons; the current row is
		// highlighted and its index named in the title below.
		first, shown := p.reviewWindow()
		for i := 0; i < shown; i++ {
			idx := first + i
			style := th.Text
			if idx == p.row {
				style = th.Selected
			}
			s.SetString(x+2, y+2+i, widget.Truncate(p.rows[idx], w-4), style, w-4)
		}
	}
	p.renderButtons(s, x, y+p.buttonsAt(), w, th)
}

func (p *Prompt) renderButtons(s *ui.Screen, x, y, w int, th widget.Theme) {
	for i, sp := range p.buttons(w) {
		style := th.Text
		if i == p.sel {
			style = th.Selected
		}
		s.SetString(x+sp.Start, y, "  "+p.options[i]+"  ", style, sp.End-sp.Start)
	}
}
