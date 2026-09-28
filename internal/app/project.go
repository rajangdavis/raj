package app

import (
	"raj/internal/editor"
	"raj/internal/piecetable"
)

// Project composes every live buffer that differs from disk under policy,
// keyed by absolute path. Clean buffers are absent: a caller materialising
// the result takes them from the working tree.
//
// The buffer set is the one the socket sees: every tab plus every headless
// buffer, the same panes host.Buffers walks. A pane with no path (a scratch
// buffer) has no absolute key a materialiser could use and is skipped. The
// dirty predicate is ViewDirty rather than Dirty: a buffer holding only a
// proposed change set has an agreed composition equal to disk, so Dirty is
// false while the view still holds bytes a projection must carry. Each value
// is that pane's session composed under p, so the policy decides which change
// sets are admitted.
//
// It has no error return because it cannot fail today. A path can appear twice
// only if a pane is both a tab and headless, which announce prevents; the map
// keeps one entry per path regardless, so the result does not depend on
// iteration order.
func (a *App) Project(p piecetable.Policy) map[string][]byte {
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	out := make(map[string][]byte)
	for _, pane := range panes {
		if pane.File.Path == "" || !pane.File.ViewDirty() {
			continue
		}
		out[pane.File.Path] = []byte(pane.File.Session().Project(p).Text())
	}
	return out
}
