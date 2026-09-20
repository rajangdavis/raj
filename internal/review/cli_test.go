package review

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintQueueIsTheListSurface covers the non-interactive fallback: the
// header, one row per item and an invalid set's reason, with no terminal.
// Without printQueue the --list form would produce no output, which is the
// fallback the brief asks for when the interactive console is unavailable.
func TestPrintQueueIsTheListSurface(t *testing.T) {
	q := Queue{Items: []Item{
		{File: "a.go", Author: 3, Kind: KindEdit, State: StateProposed,
			Size: Size{Added: 2, Removed: 1}, Excerpt: "new line"},
		{File: "b.go", Author: 5, Kind: KindEdit, State: StateInvalid,
			Excerpt: "(invalid)", Reason: "superseded by set 4 (author 3)"},
	}}
	var b bytes.Buffer
	printQueue(q, &b)
	out := b.String()
	for _, want := range []string{"2 item(s)", "a.go", "b.go",
		"reason: superseded by set 4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--list output missing %q:\n%s", want, out)
		}
	}
}
