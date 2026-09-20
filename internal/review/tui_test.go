package review

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
)

func uiKey(text string) ui.Key {
	code := 13
	if text != "\n" {
		code = int([]rune(text)[0])
	}
	return ui.Key{Event: keys.Event{Kind: keys.KeyEvent, Code: code, Type: keys.Press, Text: text}}
}

func drawTestView(t *testing.T, q Queue) *View {
	t.Helper()
	c := NewController(&fakeTransport{})
	c.Queue = q
	v := &View{host: ui.NewFakeHost(80, 20), screen: ui.NewScreen(80, 20), ctl: c}
	v.draw()
	return v
}

// TestViewRendersQueueAndDecisionRow draws the two-pane layout and asserts the
// header count, the queue row and the decision row that names the target.
// Without the renderer the console would have a model but nothing on screen to
// decide from.
func TestViewRendersQueueAndDecisionRow(t *testing.T) {
	v := drawTestView(t, Queue{Items: []Item{
		{File: "a.go", Group: 1, Author: 3, Kind: KindEdit, State: StateProposed,
			Size: Size{Added: 2}, Excerpt: "new line"},
	}})
	if top := v.screen.Row(0); !strings.Contains(top, "1 item(s)") {
		t.Fatalf("header row %q missing count", top)
	}
	if body := v.screen.Row(1); !strings.Contains(body, "a.go") {
		t.Fatalf("queue row %q missing file", body)
	}
	bottom := v.screen.Row(19)
	if !strings.Contains(bottom, "a.go") || !strings.Contains(bottom, "author 3") {
		t.Fatalf("decision row %q missing target", bottom)
	}
}

// TestViewPhoneRendersOneItemAtATime checks the phone profile: the summary card
// names the target and the pinned row still carries the decision keys.
func TestViewPhoneRendersOneItemAtATime(t *testing.T) {
	v := drawTestView(t, Queue{Items: []Item{
		{File: "a.go", Group: 1, Author: 3, Kind: KindEdit, State: StateProposed,
			Excerpt: "new line"},
	}})
	v.phone = true
	v.draw()
	if row := v.screen.Row(1); !strings.Contains(row, "a.go") {
		t.Fatalf("phone card %q missing target", row)
	}
	if row := v.screen.Row(19); !strings.Contains(row, "a=accept") {
		t.Fatalf("phone decision row %q missing keys", row)
	}
}

// TestViewKeysMoveAndOpenDetail drives the printable keys through the view and
// checks selection movement and the detail toggle. Without key handling the
// interactive console would render but not respond.
func TestViewKeysMoveAndOpenDetail(t *testing.T) {
	v := drawTestView(t, Queue{Items: []Item{
		{File: "a.go", Group: 1, Kind: KindEdit, State: StateProposed},
		{File: "b.go", Group: 2, Kind: KindEdit, State: StateProposed},
	}})
	v.key(uiKey("n"))
	if v.list.Sel != 1 {
		t.Fatalf("n: sel=%d want 1", v.list.Sel)
	}
	v.key(uiKey("p"))
	if v.list.Sel != 0 {
		t.Fatalf("p: sel=%d want 0", v.list.Sel)
	}
	v.key(uiKey("\n"))
	if !v.detail {
		t.Fatal("enter did not open detail")
	}
	v.key(uiKey("b"))
	if v.detail {
		t.Fatal("b did not close detail")
	}
}

// TestViewHelpIsPrintablesOnly checks the ? overlay lists the keys and that any
// key dismisses it, so help can never trap the console.
func TestViewHelpIsPrintablesOnly(t *testing.T) {
	v := drawTestView(t, Queue{})
	v.key(uiKey("?"))
	if !v.help {
		t.Fatal("? did not open help")
	}
	v.draw()
	if row := v.screen.Row(0); !strings.Contains(row, "review console") {
		t.Fatalf("help row %q missing title", row)
	}
	v.key(uiKey("x"))
	if v.help {
		t.Fatal("a key did not dismiss help")
	}
}
