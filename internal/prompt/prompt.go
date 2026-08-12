// Package prompt is the modal dialog: a floating overlay that asks exactly one
// question and hands the answer back so the caller can resume what it was
// doing.
//
// Two shapes, one type. Ask takes a line of text (where should this file go?);
// Confirm picks between labelled buttons (this has unsaved changes — now what?).
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
)

// SaveOptions is the standard three-way answer to unsaved changes. Cancel is
// last and Save first, so the default selection is the non-destructive one.
func SaveOptions() []string { return []string{Save, Discard, Cancel} }

type kind int

const (
	ask kind = iota
	confirm
)

// Prompt is a modal question. Only one is open at a time: a dialog that can be
// stacked is a dialog you can get lost in, and every chain here replaces its
// predecessor rather than nesting under it.
type Prompt struct {
	Open bool

	kind    kind
	title   string
	message string
	input   widget.Input
	options []string
	sel     int
	done    func(answer string, ok bool)
}

// New returns a closed prompt.
func New() *Prompt { return &Prompt{} }

// Ask opens a single-line question seeded with initial, caret at the end. Use
// it when the seed is a prefix to keep typing after — a save-as path is the
// directory, not the answer.
func (p *Prompt) Ask(title, initial string, done func(answer string, ok bool)) {
	*p = Prompt{Open: true, kind: ask, title: title, done: done}
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

// ActiveInput is the field of an open text question, and nil for a button
// choice — there is nothing to select in a row of buttons.
func (p *Prompt) ActiveInput() *widget.Input {
	if !p.Open || p.kind != ask {
		return nil
	}
	return &p.input
}

// Title is what the dialog is asking about, for the status line.
func (p *Prompt) Title() string { return p.title }

// Text is the current contents of an Ask field, for tests.
func (p *Prompt) Text() string { return p.input.Text }

// Selected is the highlighted option of a Confirm, for tests.
func (p *Prompt) Selected() string {
	if p.kind != confirm || p.sel >= len(p.options) {
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

func (p *Prompt) answer() string {
	if p.kind == confirm {
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
func (p *Prompt) Render(s *ui.Screen, cols, rows int, th widget.Theme) {
	if !p.Open {
		return
	}
	w := cols * 2 / 3
	if w > 72 {
		w = 72
	}
	if w > cols-4 {
		w = cols - 4
	}
	h := 5
	if p.kind == ask {
		h = 6 // the field draws its own three-row border
	}
	if w < 24 || h > rows-2 {
		return
	}
	x, y := (cols-w)/2, (rows-h)/2

	s.Fill(x, y, w, h, ui.DefaultStyle)
	widget.Box(s, x, y, w, h, th.BorderFocus)
	s.SetString(x+2, y, " "+p.title+" ", th.BorderFocus, w-4)

	if p.kind == ask {
		// Inset by one so the field's border sits inside the dialog's rather
		// than doubling up on it.
		p.input.Render(s, x+2, y+1, w-4, th)
		s.SetString(x+2, y+4, "enter  save      esc  cancel", th.Dim, w-4)
		return
	}
	s.SetString(x+2, y+1, widget.Truncate(p.message, w-4), th.Text, w-4)
	p.renderButtons(s, x, y+3, w, th)
}

func (p *Prompt) renderButtons(s *ui.Screen, x, y, w int, th widget.Theme) {
	total := 0
	for _, o := range p.options {
		total += len(o) + 4
	}
	bx := x + (w-total)/2
	if bx < x+1 {
		bx = x + 1
	}
	for i, o := range p.options {
		label := "  " + o + "  "
		style := th.Text
		if i == p.sel {
			style = th.Selected
		}
		s.SetString(bx, y, label, style, len(label))
		bx += len(label)
	}
}
