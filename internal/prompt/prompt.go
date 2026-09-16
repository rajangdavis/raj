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

// Candidate is one entry a Suggest hook offers under an Ask field. Text is what
// replaces the field when the entry is chosen; Display is what the listing
// draws, so a directory's full path can be inserted while its bare name is
// shown; and Dir records that choosing the entry opens a listing of its own
// rather than answering the question.
type Candidate struct {
	Text    string
	Display string
	Dir     bool
}

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

	// Suggest, when set, returns the entries under an Ask field's current text.
	// The prompt draws them under the field and lets the arrow keys step
	// through them. A hook for the same reason Complete is one: the prompt has
	// no business knowing what a directory is.
	Suggest func(string) []Candidate

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

	// cands is the current listing under an Ask field and cand the highlighted
	// entry, -1 for none. -1 rather than 0 so a typed path that happens to match
	// an entry is still answered by enter: nothing is chosen until an arrow says
	// so.
	cands []Candidate
	cand  int
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
	p.AskList(title, initial, complete, nil, done)
}

// AskList is AskComplete with a listing hook: suggest returns the entries under
// the text so far, which the prompt shows beneath the field and the arrow keys
// step through. Tab still fills the common prefix; the listing is what shows the
// choices a common prefix alone cannot — the contents of the directory being
// typed into.
func (p *Prompt) AskList(title, initial string, complete func(string) string,
	suggest func(string) []Candidate, done func(answer string, ok bool)) {
	*p = Prompt{Open: true, kind: ask, title: title, done: done,
		Complete: complete, Suggest: suggest}
	p.input.Focused = true
	p.input.SetText(initial)
	p.refreshCandidates()
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
		// Enter takes the highlighted listing entry when there is one, so the
		// arrows are a way to pick a file rather than only to look at it. With
		// no highlight it answers with the field, which is what a typed path
		// does.
		if p.kind == ask && p.cand >= 0 && p.cand < len(p.cands) {
			p.chooseCandidate()
			return
		}
		p.finish(p.answer(), true)
		return
	}
	if p.kind == ask {
		switch a {
		case keys.Indent:
			// Tab completes rather than indenting. There is nothing to indent in
			// a one-line field, and a path field that does not complete is the
			// thing that made save-as feel like a text box rather than a file
			// dialog. The listing is recomputed because completing changed which
			// entries match.
			if p.Complete != nil {
				if done := p.Complete(p.input.Text); done != "" && done != p.input.Text {
					p.input.SetText(done)
				}
			}
			p.refreshCandidates()
			return
		case keys.LineUp:
			p.moveCandidate(-1)
			return
		case keys.LineDown:
			p.moveCandidate(+1)
			return
		}
		// Any other key edits or moves in the field. The listing is refreshed
		// only when the text changed, so a caret move does not re-read a
		// directory.
		before := p.input.Text
		p.input.Handle(a, text)
		if p.input.Text != before {
			p.refreshCandidates()
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

// refreshCandidates recomputes the listing for the field's current text and
// clears the highlight. Clearing it matters: the listing is filtered by what was
// typed, so the entry under the old highlight is not the entry under the new
// one, and keeping the index would silently choose text the user never pointed
// at.
func (p *Prompt) refreshCandidates() {
	p.cand = -1
	if p.Suggest == nil || p.kind != ask {
		p.cands = nil
		return
	}
	p.cands = p.Suggest(p.input.Text)
}

// candMaxRows caps the listing drawn under the field. A directory can hold more
// entries than a dialog can show; the window follows the highlight, so the
// arrows reach every entry without the box growing past the screen.
const candMaxRows = 6

// candWindow is the slice of candidates the dialog draws, with the highlighted
// one always inside it, and how many it can afford. The screen caps it as well
// as candMaxRows so a small terminal still gets the field rather than no box at
// all.
func (p *Prompt) candWindow(rows int) (first, shown int) {
	shown = len(p.cands)
	if shown > candMaxRows {
		shown = candMaxRows
	}
	if max := rows - 2 - (1 + widget.Height + 2); shown > max {
		shown = max
	}
	if shown <= 0 {
		return 0, 0
	}
	if p.cand >= shown {
		first = p.cand - shown + 1
	}
	if max := len(p.cands) - shown; first > max {
		first = max
	}
	if first < 0 {
		first = 0
	}
	return first, shown
}

// moveCandidate steps the listing's highlight. From no highlight a forward step
// takes the first entry and a backward step the last, so the arrows enter the
// listing from either end.
func (p *Prompt) moveCandidate(d int) {
	n := len(p.cands)
	if n == 0 {
		return
	}
	if p.cand < 0 {
		if d > 0 {
			p.cand = 0
		} else {
			p.cand = n - 1
		}
		return
	}
	p.cand = (p.cand + d + n) % n
}

// chooseCandidate applies the highlighted entry. A directory fills the field and
// keeps the question open so the listing becomes its contents; anything else
// answers with the entry's full text, which is what enter on a typed path does.
func (p *Prompt) chooseCandidate() {
	c := p.cands[p.cand]
	p.input.SetText(c.Text)
	if c.Dir {
		p.refreshCandidates()
		return
	}
	p.finish(p.answer(), true)
}

// reviewMaxRows is the most of a review listing the dialog will draw before it
// scrolls. A save can carry more pending sets than a dialog can show, and
// burying the buttons is how they stop being reachable.
const reviewMaxRows = 8

// reviewWindowFor is the slice of rows the dialog draws, with the current one
// always inside it, and how many rows it can afford. Two caps, not one: rows
// stop at reviewMaxRows so the buttons are never buried, and the wrapped height
// must fit listingBudget so the box does not outgrow the screen and vanish.
// Width is part of it because an absolute path wraps to two lines, and a count
// of rows that ignores that is not a height.
func (p *Prompt) reviewWindowFor(w, rows int) (first, shown int) {
	shown = len(p.rows)
	if shown > reviewMaxRows {
		shown = reviewMaxRows
	}
	budget := p.listingBudget(w, rows)
	for shown > 0 {
		if p.row >= shown {
			first = p.row - shown + 1
		} else {
			first = 0
		}
		if max := len(p.rows) - shown; first > max {
			first = max
		}
		if p.wrappedHeight(first, shown, w) <= budget {
			break
		}
		shown--
	}
	if shown <= 0 {
		return 0, 0
	}
	return first, shown
}

// wrappedHeight is the dialog rows a window of the listing occupies once every
// row is wrapped, which is the number the box has to budget for.
func (p *Prompt) wrappedHeight(first, shown, w int) int {
	n := 0
	for i := 0; i < shown; i++ {
		n += len(widget.Wrap(p.rows[first+i], w-4))
	}
	return n
}

// listingBudget is how many wrapped dialog rows the listing may occupy: the
// screen, less the two border rows, the gate message and the three rows the
// review box spends on title, buttons and their gap. It is clamped at zero so a
// message that already fills the dialog simply shows no listing rather than
// overrunning it.
func (p *Prompt) listingBudget(w, rows int) int {
	n := rows - 2 - len(p.messageLinesFor(w, rows)) - 3
	if n < 0 {
		return 0
	}
	return n
}

// messageLines wraps the message to the dialog's inner width. The dialog grows
// to hold every line, because the gate messages are sentences whose point is in
// the last word -- "1 pending set." -- and an ellipsis cuts exactly that.
func (p *Prompt) messageLines(w int) []string {
	return widget.Wrap(p.message, w-4)
}

// messageLinesFor wraps the message the same way, but clamps it to the rows the
// dialog can actually hold. A gate message is ordinarily a short sentence; this
// is the pathological case, and a box that bails and draws nothing is a worse
// failure than a message whose tail is cut. The ellipsis marks where it was.
//
// The reserve is the box's non-message rows: three for review (title, buttons,
// their border) and four for confirm, which keeps a blank line before its
// buttons. Clamping to the review figure alone would leave a confirm one row
// too tall, so the kind picks it. box, Render and ClickAt all read this, so the
// height they compute and the lines they draw are the same.
func (p *Prompt) messageLinesFor(w, rows int) []string {
	reserve := 3
	if p.kind == confirm {
		reserve = 4
	}
	lines := p.messageLines(w)
	if max := rows - 2 - reserve; max < len(lines) {
		if max <= 0 {
			return nil
		}
		lines = lines[:max]
		lines[max-1] = widget.Truncate(lines[max-1]+"…", w-4)
	}
	return lines
}

// listingLines wraps each visible review row in turn. One row can occupy more
// than one dialog line, which is what lets an absolute path be read whole
// instead of cut to a prefix.
func (p *Prompt) listingLines(w, rows int) [][]string {
	first, shown := p.reviewWindowFor(w, rows)
	out := make([][]string, shown)
	for i := 0; i < shown; i++ {
		out[i] = widget.Wrap(p.rows[first+i], w-4)
	}
	return out
}

// listingHeight is the number of dialog rows the review listing occupies once
// its rows are wrapped and the window is shrunk to the budget. It is what the
// box spends on the listing, so it must be the same window everything else
// draws and clicks.
func (p *Prompt) listingHeight(w, rows int) int {
	n := 0
	for _, lines := range p.listingLines(w, rows) {
		n += len(lines)
	}
	return n
}

// buttonsAt is where the options sit, counted in dialog rows: after the wrapped
// message and, when there is one, the wrapped listing.
func (p *Prompt) buttonsAt(w, rows int) int {
	if p.kind == review {
		return 1 + len(p.messageLinesFor(w, rows)) + p.listingHeight(w, rows)
	}
	return len(p.messageLinesFor(w, rows)) + 2
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
	switch p.kind {
	case ask:
		// The field draws its own three-row border; the listing, when there is
		// one, sits under it and above the hint.
		_, shown := p.candWindow(rows)
		h = 1 + widget.Height + 2 + shown
	case confirm:
		// The message wraps, so the box is as tall as it needs -- clamped to
		// the screen, because a box taller than the screen draws nothing.
		h = len(p.messageLinesFor(w, rows)) + 4
	case review:
		// The message and the listing both wrap; the listing is windowed to
		// what is left after the message, so the box always fits.
		h = len(p.messageLinesFor(w, rows)) + p.listingHeight(w, rows) + 3
	}
	if w < 24 || h > rows-2 {
		return 0, 0, 0, 0, false
	}
	return (cols - w) / 2, (rows - h) / 2, w, h, true
}

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
	if p.kind == review {
		// A press on a row selects it, and the follow-up hook fires the same as
		// an arrow would — the pointer is not a shortcut past the listing. A
		// wrapped row takes several lines, so find the one dy lands on.
		listTop := 1 + len(p.messageLinesFor(w, rows))
		if dy >= listTop && dy < p.buttonsAt(w, rows) {
			first, _ := p.reviewWindowFor(w, rows)
			line := listTop
			for i, lines := range p.listingLines(w, rows) {
				if dy < line+len(lines) {
					p.row = first + i
					if p.moved != nil {
						p.moved(p.row)
					}
					break
				}
				line += len(lines)
			}
			return true
		}
	}
	if dy == p.buttonsAt(w, rows) {
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
		// The listing sits between the field and the hint: it is what the field
		// would otherwise make you guess, and the hint is a fixed footer.
		first, shown := p.candWindow(rows)
		line := y + 1 + widget.Height
		for i := 0; i < shown; i++ {
			c := p.cands[first+i]
			text := c.Display
			if text == "" {
				text = c.Text
			}
			style := th.Text
			if first+i == p.cand {
				style = th.Selected
			}
			s.SetString(x+2, line, widget.TruncateLeft(text, w-4), style, w-4)
			line++
		}
		s.SetString(x+2, line, "enter  save      esc  cancel", th.Dim, w-4)
		return
	}
	// The message wraps across rows and the box was sized from the same clamped
	// lines, so it reads whole -- unless it is longer than the screen, where the
	// last visible line is ellipsised so the box still appears.
	line := y + 1
	for _, text := range p.messageLinesFor(w, rows) {
		s.SetString(x+2, line, text, th.Text, w-4)
		line++
	}
	if p.kind == review {
		// The listing between the question and the buttons; a row too long for
		// one line continues onto the next, and the current row is highlighted
		// and its index named in the title. The window is the same one box and
		// ClickAt computed, so the drawn rows and the pressable rows agree.
		first, _ := p.reviewWindowFor(w, rows)
		for i, lines := range p.listingLines(w, rows) {
			style := th.Text
			if first+i == p.row {
				style = th.Selected
			}
			for _, text := range lines {
				s.SetString(x+2, line, text, style, w-4)
				line++
			}
		}
	}
	p.renderButtons(s, x, y+p.buttonsAt(w, rows), w, th)
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
