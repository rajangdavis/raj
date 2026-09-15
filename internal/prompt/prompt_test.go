package prompt

import (
	"strings"
	"testing"

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
