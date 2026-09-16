package prompt

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// rowText reads the dialog's inner columns of one screen row, so a test sees
// what was drawn without the box border or the surrounding blank cells.
func rowText(s *ui.Screen, x, w, y int) string {
	var sb strings.Builder
	for col := x + 2; col < x+w-1; col++ {
		if c := s.At(col, y); c.Width > 0 {
			sb.WriteRune(c.Rune)
		}
	}
	return strings.TrimSpace(sb.String())
}

// A message longer than the dialog's inner width wraps across rows and the box
// grows to hold it, so the sentence reads whole instead of ending mid-word in an
// ellipsis -- the dir-removal gate's reason was the thing being lost.
func TestConfirmMessageIsReadWhole(t *testing.T) {
	msg := "agent 2 proposed removing pkg. a.go: Unsaved and 1 pending set."
	p := New()
	p.Confirm("Remove directory", msg, []string{"Ignore for now", "Remove forever"}, nil)

	s := ui.NewScreen(80, 24)
	p.Render(s, 80, 24, widget.DefaultTheme())
	x, y, w, _, ok := p.box(80, 24)
	if !ok {
		t.Fatal("dialog does not fit")
	}
	lines := p.messageLines(w)
	if len(lines) < 2 {
		t.Fatalf("message did not wrap: %d line(s)", len(lines))
	}
	var parts []string
	for i, text := range lines {
		if got := rowText(s, x, w, y+1+i); got != text {
			t.Errorf("message row %d = %q, want %q", i, got, text)
		}
		parts = append(parts, text)
	}
	if got := strings.Join(parts, " "); got != msg {
		t.Errorf("rendered message = %q, want %q", got, msg)
	}
	if strings.Contains(strings.Join(parts, ""), "…") {
		t.Errorf("message contains an ellipsis: %q", parts)
	}
}

// A review row holding an absolute path wraps rather than truncating, so the
// whole path is on screen. The old code cut it mid-word at the inner width.
func TestReviewRowShowsWholePath(t *testing.T) {
	path := "/Users/rajandavis/Desktop/projects/raj/internal/app/deletion.go"
	p := New()
	p.Review("Remove directory", "agent 2 proposed removing pkg.",
		[]string{path}, []string{"Ignore for now", "Remove forever"}, nil, nil)

	s := ui.NewScreen(80, 40)
	p.Render(s, 80, 40, widget.DefaultTheme())
	x, y, w, _, ok := p.box(80, 40)
	if !ok {
		t.Fatal("dialog does not fit")
	}
	listing := p.listingLines(w, 40)
	if len(listing) != 1 || len(listing[0]) < 2 {
		t.Fatalf("path did not wrap: %v", listing)
	}
	line := y + 1 + len(p.messageLines(w))
	var parts []string
	for _, ls := range listing {
		for range ls {
			parts = append(parts, rowText(s, x, w, line))
			line++
		}
	}
	if got := strings.Join(parts, ""); got != path {
		t.Errorf("rendered row = %q, want %q", got, path)
	}
	if strings.Contains(strings.Join(parts, ""), "…") {
		t.Errorf("row contains an ellipsis: %q", parts)
	}
}

// A press on any line of a wrapped row selects that row, so a long path is not
// a bigger pointer target than a short one.
func TestClickAtSelectsWrappedRow(t *testing.T) {
	path := "/Users/rajandavis/Desktop/projects/raj/internal/app/deletion.go"
	moved := -1
	p := New()
	p.Review("Remove directory", "agent 2 proposed removing pkg.",
		[]string{"other.go", path}, []string{"Ignore for now", "Remove forever"},
		func(row int) { moved = row }, nil)

	x, y, w, _, ok := p.box(80, 40)
	if !ok {
		t.Fatal("dialog does not fit")
	}
	listing := p.listingLines(w, 40)
	// The second display line of the second row is the continuation of the
	// absolute path.
	line := y + 1 + len(p.messageLines(w)) + len(listing[0]) + 1
	if !p.ClickAt(80, 40, x+2, line) {
		t.Fatal("press outside the dialog")
	}
	if p.row != 1 || moved != 1 {
		t.Errorf("row = %d, moved = %d; want both 1", p.row, moved)
	}
}

// A review listing taller than a short screen is windowed to the wrapped height
// the dialog can afford rather than making the box bail and draw nothing. This
// is the 12-row case the app tests hit: absolute paths wrap to two lines each,
// so a row count that ignores width measures a six-line listing as three.
func TestReviewListingFitsShortScreen(t *testing.T) {
	paths := make([]string, 6)
	for i := range paths {
		paths[i] = "/Users/rajandavis/Desktop/projects/raj/internal/app/deletion.go"
	}
	p := New()
	p.Review("Remove directory", "agent 2 proposed removing pkg.", paths,
		[]string{"Ignore for now", "Remove forever"}, nil, nil)
	// Put the selection below the first guess, so the window has to scroll to
	// keep it on screen while still shrinking to the budget.
	p.row = len(paths) - 1

	s := ui.NewScreen(68, 12)
	p.Render(s, 68, 12, widget.DefaultTheme())
	x, y, w, h, ok := p.box(68, 12)
	if !ok {
		t.Fatal("dialog does not fit on a 12-row screen")
	}
	if h > 12-2 {
		t.Fatalf("box height %d exceeds the %d usable rows", h, 12-2)
	}
	first, shown := p.reviewWindowFor(w, 12)
	if shown == 0 {
		t.Fatal("listing window is empty; the rows vanished with the box")
	}
	if first > p.row || p.row >= first+shown {
		t.Fatalf("selected row %d outside window [%d,%d)", p.row, first, first+shown)
	}
	if got, want := p.listingHeight(w, 12), p.wrappedHeight(first, shown, w); got != want {
		t.Errorf("listingHeight = %d, wrappedHeight = %d; want the same window", got, want)
	}
	// The selected row is the last one in the window, so its wrapped tail must
	// be on the row the renderer put it on.
	last := y + 1 + len(p.messageLinesFor(w, 12)) + p.listingHeight(w, 12) - 1
	if got := rowText(s, x, w, last); got == "" {
		t.Error("selected listing row was not drawn")
	}
}

// A message longer than the whole dialog is clamped to the rows the box can
// hold, with an ellipsis on the last visible line, so the box still fits and
// appears instead of bailing. Confirm keeps a blank gap before its buttons, so
// its clamp is one line tighter than review's.
func TestMessageClampFitsScreen(t *testing.T) {
	long := strings.Repeat("pending set ", 200)
	p := New()
	p.Review("Remove directory", long, []string{"a.go"},
		[]string{"Ignore for now", "Remove forever"}, nil, nil)

	for _, rows := range []int{12, 24} {
		_, _, w, h, ok := p.box(68, rows)
		if !ok {
			t.Fatalf("rows %d: dialog does not fit after clamping", rows)
		}
		if h > rows-2 {
			t.Fatalf("rows %d: box height %d exceeds %d", rows, h, rows-2)
		}
		lines := p.messageLinesFor(w, rows)
		if len(lines) > rows-2-3 {
			t.Errorf("rows %d: message is %d lines, want <= %d", rows, len(lines), rows-2-3)
		}
		if len(lines) == 0 || !strings.HasSuffix(lines[len(lines)-1], "…") {
			t.Errorf("rows %d: clamped message not marked: %q", rows, lines)
		}
	}

	c := New()
	c.Confirm("Remove directory", long, []string{"Ignore for now", "Remove forever"}, nil)
	_, _, w, h, ok := c.box(68, 12)
	if !ok || h > 12-2 {
		t.Fatalf("confirm box does not fit: ok = %v, h = %d", ok, h)
	}
	if lines := c.messageLinesFor(w, 12); len(lines) > 12-2-4 {
		t.Errorf("confirm message is %d lines, want <= %d", len(lines), 12-2-4)
	}
}

// A Suggest hook lists a directory's entries under the field as it is typed, so
// a name can be seen before enough of it has been typed to complete.
func TestAskListsSuggestionsUnderTheField(t *testing.T) {
	p := New()
	p.AskList("Save as", "/work/", nil, func(string) []Candidate {
		return []Candidate{
			{Text: "/work/main.go", Display: "main.go"},
			{Text: "/work/pkg/", Display: "pkg/", Dir: true},
		}
	}, nil)

	s := ui.NewScreen(80, 24)
	p.Render(s, 80, 24, widget.DefaultTheme())
	x, y, w, _, ok := p.box(80, 24)
	if !ok {
		t.Fatal("dialog does not fit")
	}
	field := y + 1 + widget.Height
	for i, want := range []string{"main.go", "pkg/"} {
		if got := rowText(s, x, w, field+i); got != want {
			t.Errorf("listing row %d = %q, want %q", i, got, want)
		}
	}
	// The hint stays under the listing rather than being overwritten by it.
	if got := rowText(s, x, w, field+2); !strings.Contains(got, "esc  cancel") {
		t.Errorf("hint row = %q, want the hint under the listing", got)
	}
}

// Typing narrows the listing: what is offered is the entries matching the text
// so far, not the whole directory.
func TestAskSuggestionsTrackTheText(t *testing.T) {
	p := New()
	p.AskList("Save as", "/work/", nil, func(text string) []Candidate {
		if strings.HasSuffix(text, "re") {
			return []Candidate{{Text: "/work/README.md", Display: "README.md"}}
		}
		return []Candidate{
			{Text: "/work/main.go", Display: "main.go"},
			{Text: "/work/README.md", Display: "README.md"},
		}
	}, nil)

	p.Handle(keys.None, "re")
	s := ui.NewScreen(80, 24)
	p.Render(s, 80, 24, widget.DefaultTheme())
	x, y, w, _, _ := p.box(80, 24)
	field := y + 1 + widget.Height
	if got := rowText(s, x, w, field); got != "README.md" {
		t.Errorf("listing row = %q, want README.md", got)
	}
	if got := rowText(s, x, w, field+1); strings.Contains(got, "main.go") {
		t.Errorf("listing was not filtered: %q", got)
	}
}

// An arrow steps into the listing; choosing a directory fills the field and
// keeps the question open, so the listing becomes that directory's contents.
func TestAskChoosingADirectoryDescends(t *testing.T) {
	p := New()
	p.AskList("Save as", "/work/", nil, func(text string) []Candidate {
		if text == "/work/pkg/" {
			return []Candidate{{Text: "/work/pkg/a.go", Display: "a.go"}}
		}
		return []Candidate{
			{Text: "/work/main.go", Display: "main.go"},
			{Text: "/work/pkg/", Display: "pkg/", Dir: true},
		}
	}, nil)

	p.Handle(keys.LineDown, "") // highlight main.go
	p.Handle(keys.LineDown, "") // highlight pkg/
	p.Handle(keys.Confirm, "")
	if !p.Open {
		t.Fatal("choosing a directory closed the question")
	}
	if got := p.Text(); got != "/work/pkg/" {
		t.Errorf("field = %q, want the chosen directory", got)
	}
	if len(p.cands) != 1 || p.cands[0].Display != "a.go" {
		t.Errorf("listing = %v, want the directory's contents", p.cands)
	}
}

// Choosing a file answers with its full text: the highlight is what makes enter
// take a listed entry rather than the typed prefix.
func TestAskChoosingAFileAnswers(t *testing.T) {
	p := New()
	var answer string
	var ok bool
	p.AskList("Save as", "/work/", nil, func(string) []Candidate {
		return []Candidate{
			{Text: "/work/main.go", Display: "main.go"},
			{Text: "/work/notes.md", Display: "notes.md"},
		}
	}, func(a string, o bool) { answer, ok = a, o })

	p.Handle(keys.LineDown, "")
	p.Handle(keys.LineDown, "") // notes.md
	p.Handle(keys.Confirm, "")
	if p.Open {
		t.Fatal("choosing a file did not answer")
	}
	if !ok || answer != "/work/notes.md" {
		t.Errorf("answer = %q ok = %v, want /work/notes.md", answer, ok)
	}
}

// Enter with nothing highlighted still answers with the field, so a listing
// that happens to contain the typed name cannot change the answer.
func TestAskEnterWithoutAHighlightUsesTheField(t *testing.T) {
	p := New()
	var answer string
	var ok bool
	p.AskList("Save as", "/work/notes.md", nil, func(string) []Candidate {
		return []Candidate{{Text: "/work/notes.md", Display: "notes.md"}}
	}, func(a string, o bool) { answer, ok = a, o })

	p.Handle(keys.Confirm, "")
	if !ok || answer != "/work/notes.md" {
		t.Errorf("answer = %q ok = %v, want the typed text", answer, ok)
	}
}
