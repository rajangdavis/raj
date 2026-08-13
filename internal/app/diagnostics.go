package app

import (
	"sort"
	"strings"
	"sync"

	"raj/internal/lsp"
)

// Diagnostics are the one thing a language server sends without being asked,
// which makes them the only feature here that has to survive arriving at an
// arbitrary moment — while a file is closed, while it is being edited, or for a
// file that was never opened in this session.
//
// They are also absolute rather than incremental: each publish is the complete
// set for that document, so the newest always replaces the previous one whole.
// Merging them would accumulate problems that have already been fixed.

// diagnostics holds the current problems for each file.
type diagnostics struct {
	mu sync.Mutex
	// byPath is the whole set for a file, replaced wholesale on each publish.
	byPath map[string][]lsp.Diagnostic
}

func newDiagnostics() *diagnostics {
	return &diagnostics{byPath: map[string][]lsp.Diagnostic{}}
}

// Severity values, as the protocol numbers them.
const (
	sevError   = 1
	sevWarning = 2
	sevInfo    = 3
	sevHint    = 4
)

// set replaces the diagnostics for a document.
//
// An empty list is meaningful and must be stored as a clearing rather than
// ignored: that is how a server says the problems it reported are fixed, and
// dropping it would leave them on screen forever.
func (d *diagnostics) set(path string, items []lsp.Diagnostic) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(items) == 0 {
		delete(d.byPath, path)
		return
	}
	// Sorted by position so the list reads in file order and a gutter lookup
	// can stop early. Severity does not order here — a warning on line 3 above
	// an error on line 200 is what the file looks like.
	sorted := append([]lsp.Diagnostic(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Range.Start, sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Character < b.Character
	})
	d.byPath[path] = sorted
}

// forPath is the problems in a file.
func (d *diagnostics) forPath(path string) []lsp.Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byPath[path]
}

// atLine is the most severe diagnostic on a line, and whether there is one.
//
// Most severe rather than first: a line with both a warning and an error is an
// error line, and showing the warning because it was published first would
// under-report it.
func (d *diagnostics) atLine(path string, line int) (lsp.Diagnostic, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var best lsp.Diagnostic
	found := false
	for _, it := range d.byPath[path] {
		if it.Range.Start.Line != line {
			continue
		}
		if !found || severityRank(it.Severity) < severityRank(best.Severity) {
			best, found = it, true
		}
	}
	return best, found
}

// counts is how many errors and warnings a file has, for the status line.
func (d *diagnostics) counts(path string) (errors, warnings int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, it := range d.byPath[path] {
		// Ranked rather than matched on the literal value, so an unspecified
		// severity is counted as the error it is treated as everywhere else.
		// Two places deciding that differently is how a file shows a red mark
		// in the gutter and "no problems" in the status line.
		switch severityRank(it.Severity) {
		case 0:
			errors++
		case 1:
			warnings++
		}
	}
	return errors, warnings
}

// clear forgets a file's diagnostics, for when it is closed.
func (d *diagnostics) clear(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byPath, path)
}

// severityRank orders severities by how much they matter, lowest first.
//
// A missing severity is treated as an error, because the protocol says the
// client should decide and a problem a server thought worth publishing is more
// likely to matter than not. Under-reporting a real error is the worse mistake.
func severityRank(sev int) int {
	switch sev {
	case sevError, 0:
		return 0
	case sevWarning:
		return 1
	case sevInfo:
		return 2
	case sevHint:
		return 3
	}
	return 4
}

// mark is the gutter character for a severity.
func severityMark(sev int) string {
	switch severityRank(sev) {
	case 0:
		return "E"
	case 1:
		return "W"
	case 2:
		return "i"
	}
	return "·"
}

// summary is the status-line text for a file's problems, or "" when it has
// none.
func (d *diagnostics) summary(path string) string {
	errors, warnings := d.counts(path)
	switch {
	case errors > 0 && warnings > 0:
		return itoa(errors) + "E " + itoa(warnings) + "W"
	case errors > 0:
		return itoa(errors) + "E"
	case warnings > 0:
		return itoa(warnings) + "W"
	}
	return ""
}

// drainDiagnostics moves everything the server has published into the store.
//
// Called on the event thread when a Wake arrives. The channel is drained rather
// than read once, because several publishes can queue between wakes and only
// the last for each file matters — reading one per wake would show a backlog
// slowly rather than the current state immediately.
func (a *App) drainDiagnostics() {
	a.servers.mu.Lock()
	conns := make([]*lsp.Conn, 0, len(a.servers.byID))
	for _, ls := range a.servers.byID {
		if c := ls.srv.Conn(); c != nil {
			conns = append(conns, c)
		}
	}
	a.servers.mu.Unlock()

	for _, c := range conns {
		for {
			select {
			case d := <-c.Diagnostics:
				a.diags.set(lsp.Path(d.URI), d.Items)
			default:
				goto next
			}
		}
	next:
	}
}

// diagnosticAtCursor is the problem on the cursor's line, for the status line.
func (a *App) diagnosticAtCursor() string {
	p := a.Tabs.Active()
	if p == nil {
		return ""
	}
	path := a.docPath(p)
	if path == "" {
		return ""
	}
	line, _ := p.File.LineCol(p.Cursors.Primary().Head)
	it, ok := a.diags.atLine(path, line)
	if !ok {
		return ""
	}
	// One line, since the status bar is one line. A multi-line message from a
	// type checker is common and folding beats truncating at the newline,
	// which would hide the part that says what to do about it.
	return severityMark(it.Severity) + " " + strings.ReplaceAll(it.Message, "\n", " ")
}
