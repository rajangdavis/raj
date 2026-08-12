package picker

import (
	"path/filepath"
	"testing"

	"raj/internal/keys"
)

// The index is relative so the list stays readable and the fuzzy score is not
// diluted by a prefix every entry shares. What is handed back must be openable
// from anywhere, because a relative path resolves against the process working
// directory rather than the workspace — which meant picking a file in a
// workspace the shell was not sitting in opened a blank buffer named after it.
func TestConfirmReturnsAnOpenablePath(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Handle(keys.None, "app.go")
	got := p.Handle(keys.Confirm, "")

	want := filepath.Join(p.Root, "internal/app/app.go")
	if got != want {
		t.Errorf("chose %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("chose a relative path %q; it resolves against the shell's cwd", got)
	}
}

// The list still shows relative paths: that is what makes it readable and what
// the ranking is tuned against.
func TestTheListStaysRelative(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Handle(keys.None, "app.go")
	if got := p.Top(); got != filepath.FromSlash("internal/app/app.go") {
		t.Errorf("list shows %q, want the relative path", got)
	}
}

// Confirming with nothing matched chooses nothing rather than the root.
func TestConfirmWithNoMatchChoosesNothing(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Handle(keys.None, "zzzznotafile")
	if got := p.Handle(keys.Confirm, ""); got != "" {
		t.Errorf("chose %q with no matches", got)
	}
}
