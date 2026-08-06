package search

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// draw renders the pane into a fresh screen and returns it as text.
func draw(p *Pane, w, h int) string {
	s := ui.NewScreen(w, h)
	p.Render(s, 0, 0, w, h, widget.DefaultTheme(), true)
	var b strings.Builder
	for y := 0; y < h; y++ {
		b.WriteString(s.Row(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// The pane used to return early below twelve rows, so a short sidebar opened
// onto nothing at all — no border either, which reads as a redraw bug rather
// than a size one.

func TestShortPaneStillDrawsTheQuery(t *testing.T) {
	p := NewPane(t.TempDir())
	typeQuery(p, "needle")

	for _, h := range []int{4, 6, 10} {
		got := draw(p, 30, h)
		if !strings.Contains(got, "needle") {
			t.Errorf("at %d rows the query is missing:\n%s", h, got)
		}
	}
}

func TestTooShortSaysSoRatherThanNothing(t *testing.T) {
	p := NewPane(t.TempDir())
	got := draw(p, 30, 2)
	if strings.TrimSpace(got) == "" {
		t.Error("a pane too short to draw rendered nothing at all")
	}
	if !strings.Contains(got, "too short") {
		t.Errorf("no explanation drawn:\n%s", got)
	}
}

func TestFullLayoutDrawsTheGlobs(t *testing.T) {
	p := NewPane(t.TempDir())
	typeQuery(p, "needle")
	p.Handle(keys.CycleFocus, "")
	typeQuery(p, "*.go")

	if got := draw(p, 40, 24); !strings.Contains(got, "*.go") {
		t.Errorf("the include glob is missing from the full layout:\n%s", got)
	}
	// The same content must vanish when there is no room for it, or the
	// compact layout is not actually compact.
	if got := draw(p, 40, 6); strings.Contains(got, "*.go") {
		t.Errorf("the glob field survived into the compact layout:\n%s", got)
	}
}

// The focus ring has to agree with the layout, or tab walks onto a component
// that is not drawn and keystrokes disappear into it.
func TestCompactRingSkipsTheHiddenFields(t *testing.T) {
	p := NewPane(t.TempDir())
	draw(p, 30, 6) // compact
	typeQuery(p, "needle")

	p.Handle(keys.CycleFocus, "")
	if in := p.ActiveInput(); in != nil {
		t.Fatalf("tab landed on a hidden field holding %q", in.Text)
	}
	if p.spot != spotResults {
		t.Errorf("spot = %d, want the results", p.spot)
	}

	p.Handle(keys.CycleFocusBack, "")
	if p.spot != spotQuery {
		t.Errorf("shift+tab returned to spot %d, want the query", p.spot)
	}
}

// Shrinking must not leave focus on something that is no longer drawn.
func TestShrinkingPullsFocusBackToTheQuery(t *testing.T) {
	p := NewPane(t.TempDir())
	draw(p, 40, 24)
	p.Handle(keys.CycleFocus, "") // onto the include field
	if p.spot != spotInclude {
		t.Fatalf("setup: spot = %d", p.spot)
	}

	draw(p, 40, 6)
	if p.spot != spotQuery {
		t.Errorf("after shrinking, spot = %d; focus is on a hidden field", p.spot)
	}
}

// The full ring must still reach everything when there is room.
func TestFullRingReachesEveryStop(t *testing.T) {
	p := NewPane(t.TempDir())
	draw(p, 40, 24)
	seen := map[int]bool{p.spot: true}
	for i := 0; i < spotCount+2; i++ {
		if _, _, exit := p.Handle(keys.CycleFocus, ""); exit {
			break
		}
		seen[p.spot] = true
	}
	for spot := 0; spot < spotCount; spot++ {
		if !seen[spot] {
			t.Errorf("tab never reached spot %d", spot)
		}
	}
}
