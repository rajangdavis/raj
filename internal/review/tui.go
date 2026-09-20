package review

import (
	"fmt"
	"strings"

	"raj/internal/ui"
	"raj/internal/widget"
)

// The console names as little colour as possible so it inherits the terminal's
// own theme. The added/removed tints are the one exception: a diff is easier to
// read when the two sides differ.
var (
	styleTitle   = ui.DefaultStyle.Plus(ui.Bold | ui.Reverse)
	styleDim     = ui.DefaultStyle.With(ui.Ansi(8))
	styleSelect  = ui.DefaultStyle.Plus(ui.Reverse)
	styleAdded   = ui.DefaultStyle.With(ui.Ansi(2))
	styleRemoved = ui.DefaultStyle.With(ui.Ansi(1))
	styleTarget  = ui.DefaultStyle.Plus(ui.Bold)
)

// View is the console's interactive state: the queue, the selection, and the
// mode (queue, detail, help). Run drives it over a ui.Host.
type View struct {
	phone  bool
	host   ui.Host
	screen *ui.Screen
	ctl    *Controller
	list   widget.List

	detail bool
	help   bool
	status string
	quit   bool
}

// Run drives the console until the user quits or the host closes. It is a
// ui.Host loop rather than an app, because the console owns no model: every
// change it makes is a decision sent over the transport and a fresh queue.
func Run(host ui.Host, ctl *Controller, phone bool) error {
	cols, rows := host.Size()
	v := &View{phone: phone, host: host, screen: ui.NewScreen(cols, rows), ctl: ctl}
	v.draw()
	if err := host.Present(v.screen); err != nil {
		return err
	}
	for e := range host.Events() {
		switch ev := e.(type) {
		case ui.Key:
			v.key(ev)
		case ui.Resize:
			v.screen.Resize(ev.Cols, ev.Rows)
			host.Invalidate()
		case ui.Quit:
			return nil
		}
		if v.quit {
			return nil
		}
		v.draw()
		if err := host.Present(v.screen); err != nil {
			return err
		}
	}
	return nil
}

// key handles one printable key. No chord and no gesture is ever required: the
// whole surface is single characters.
func (v *View) key(k ui.Key) {
	text := k.Insertable()
	chord := k.Chord()

	if v.help {
		v.help = false
		return
	}
	switch {
	case chord == "esc":
		if v.detail {
			v.detail = false
			return
		}
		v.quit = true
	case chord == "enter" || text == "\n" || text == "d":
		v.detail = !v.detail
	case text == "?":
		v.help = true
	case text == "q" || text == "b":
		if v.detail {
			v.detail = false
			return
		}
		v.quit = true
	case text == "n":
		v.move(1)
	case text == "p":
		v.move(-1)
	case text == "a":
		v.decide(Accept)
	case text == "r":
		v.decide(Reject)
	case text == "c":
		v.decide(Clear)
	case text == "A":
		v.acceptAll("author")
	case text == "f":
		v.acceptAll("file")
	}
}

func (v *View) move(delta int) {
	v.list.Move(delta, len(v.ctl.Queue.Items))
}

// decide sends one decision and re-fetches. A refusal or a drift is reported
// and the queue is re-rendered; nothing is buffered for a retry.
func (v *View) decide(a Action) {
	it, ok := v.current()
	if !ok {
		v.status = "no change set selected"
		return
	}
	if err := v.ctl.Decide(a, it); err != nil {
		v.status = err.Error()
		v.refresh()
		return
	}
	v.status = fmt.Sprintf("%s group %d in %s", a, it.Group, it.File)
	v.refresh()
}

// acceptAll accepts every set the current item's author or file owns. The
// filter is taken from the selection before any decision, because the selection
// moves as the queue shrinks.
func (v *View) acceptAll(scope string) {
	it, ok := v.current()
	if !ok {
		v.status = "no change set selected"
		return
	}
	var match func(Item) bool
	switch scope {
	case "author":
		author := it.Author
		match = func(x Item) bool { return x.Author == author }
	case "file":
		file := it.File
		match = func(x Item) bool { return x.File == file }
	default:
		match = func(Item) bool { return false }
	}
	n, err := v.ctl.AcceptAll(match)
	if err != nil {
		v.status = fmt.Sprintf("accepted %d, then stopped: %s", n, err)
		v.refresh()
		return
	}
	v.status = fmt.Sprintf("accepted %d set(s) by %s", n, scope)
	v.refresh()
}

func (v *View) refresh() {
	if err := v.ctl.Refresh(); err != nil {
		v.status = "re-fetching failed: " + err.Error()
		return
	}
	v.clamp()
}

func (v *View) current() (Item, bool) {
	items := v.ctl.Queue.Items
	if v.list.Sel < 0 || v.list.Sel >= len(items) {
		return Item{}, false
	}
	return items[v.list.Sel], true
}

func (v *View) clamp() {
	n := len(v.ctl.Queue.Items)
	if n == 0 {
		v.list.Reset()
		return
	}
	if v.list.Sel >= n {
		v.list.Sel = n - 1
	}
	if v.list.Sel < 0 {
		v.list.Sel = 0
	}
}

func (v *View) draw() {
	s := v.screen
	s.Clear()
	cols, rows := s.Size()
	if cols <= 0 || rows <= 0 {
		return
	}
	head := v.ctl.Queue.Header().Line()
	if v.status != "" {
		head = v.status + "  |  " + head
	}
	s.SetString(0, 0, widget.Truncate(head, cols), styleTitle, cols)
	if v.help {
		v.drawHelp(cols, rows)
		return
	}
	if v.phone {
		v.drawPhone(cols, rows)
		return
	}
	v.drawPanes(cols, rows)
}

func (v *View) drawPanes(cols, rows int) {
	body := rows - 2
	if body < 1 {
		return
	}
	left := cols * 2 / 5
	if left < 16 {
		left = 16
	}
	if left > cols-12 {
		left = cols - 12
	}
	if left < 1 {
		left = cols / 2
	}
	items := v.ctl.Queue.Items
	v.list.Settle(body, len(items))
	for i := 0; i < body; i++ {
		idx := v.list.Top + i
		if idx >= len(items) {
			break
		}
		st := styleDim
		if idx == v.list.Sel {
			st = styleSelect
		}
		v.screen.SetString(0, 1+i, widget.Truncate(items[idx].Row(), left), st, left)
	}
	for y := 1; y < rows-1; y++ {
		v.screen.Set(left, y, '│', styleDim)
	}
	rightX := left + 1
	if rightW := cols - rightX; rightW > 0 {
		v.drawDiff(rightX, 1, rightW, body)
	}
	v.drawDecision(0, rows-1, cols)
}

func (v *View) drawDiff(x, y, w, h int) {
	it, ok := v.current()
	if !ok {
		v.screen.SetString(x, y, "no pending change sets", styleDim, w)
		return
	}
	v.screen.SetString(x, y, widget.Truncate(it.Target(), w), styleTarget, w)
	lines := it.Diff
	if it.Reason != "" {
		lines = append([]string{it.Reason}, lines...)
	}
	rows := wrapped(lines, w)
	for i := 0; i < h-1 && i < len(rows); i++ {
		v.screen.SetString(x, y+1+i, rows[i], diffStyle(rows[i]), w)
	}
}

func (v *View) drawPhone(cols, rows int) {
	if rows < 3 {
		return
	}
	v.drawDecision(0, rows-1, cols)
	it, ok := v.current()
	if !ok {
		v.screen.SetString(0, 1, "no pending change sets", styleDim, cols)
		return
	}
	y := 1
	v.screen.SetString(0, y, widget.Truncate(it.Target(), cols), styleTarget, cols)
	y++
	v.screen.SetString(0, y, widget.Truncate(fmt.Sprintf("%s  %s  %s",
		it.State, it.Size.String(), it.Excerpt), cols), styleDim, cols)
	y++
	if it.Reason != "" && y < rows-1 {
		v.screen.SetString(0, y, widget.Truncate(it.Reason, cols), styleDim, cols)
		y++
	}
	if v.detail {
		lines := wrapped(it.Diff, cols)
		for i := 0; i < rows-1-y && i < len(lines); i++ {
			v.screen.SetString(0, y+i, lines[i], diffStyle(lines[i]), cols)
		}
	} else if y < rows-1 {
		v.screen.SetString(0, y, "enter or d: show the diff", styleDim, cols)
	}
}

func (v *View) drawDecision(x, y, w int) {
	it, ok := v.current()
	var row string
	switch {
	case !ok:
		row = "no pending change sets   ?=help   q=quit"
	case it.Kind != KindEdit:
		row = it.Target() + " — file removals are decided on the host   ?=help q=quit"
	default:
		row = it.Target() + "   a=accept r=reject c=clear enter=diff ?=help q=quit"
	}
	v.screen.SetString(x, y, widget.Truncate(row, w), styleSelect, w)
}

func (v *View) drawHelp(cols, rows int) {
	lines := []string{
		"review console — printable keys only",
		"",
		"n / p      next / previous item",
		"enter / d  show or hide the diff",
		"a          accept the selected set",
		"r          reject the selected set",
		"c          clear the selected set",
		"A          accept every set from the current author",
		"f          accept every set in the current file",
		"?          this help",
		"b / q      back, or quit",
		"",
		"invalid and superseded sets are shown, never decided for you.",
	}
	for i := 0; i < rows-1 && i < len(lines); i++ {
		st := styleDim
		if i == 0 {
			st = styleTitle
		}
		v.screen.SetString(0, i, widget.Truncate(lines[i], cols), st, cols)
	}
}

// diffStyle tints a rendered diff line by its prefix.
func diffStyle(line string) ui.Style {
	switch {
	case strings.HasPrefix(line, "+"):
		return styleAdded
	case strings.HasPrefix(line, "-"):
		return styleRemoved
	case strings.HasPrefix(line, "@@"):
		return styleTarget
	}
	return styleDim
}

// wrapped flattens diff lines into display rows no wider than w. A long line
// wraps without repeating its -/+ prefix, which is enough for a small screen
// and avoids pretending to be a full diff renderer.
func wrapped(lines []string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, line := range lines {
		out = append(out, widget.Wrap(line, w)...)
	}
	return out
}
