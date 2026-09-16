package app

import (
	"context"
	"sort"
	"strings"
	"sync"

	"raj/internal/control"
	"raj/internal/lsp"
	"raj/internal/problems"
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
	// publishedPaths is every path a server has published about, including an
	// empty publish. byPath alone cannot tell "the server says this file is
	// clean" from "no server has answered about this file yet": both read as
	// no items, and only the first may be read as no problems.
	publishedPaths map[string]bool
	// versions is the document version each path's last publish applied to,
	// where the server sent one. A missing entry means the publish carried no
	// version, which is not the same as version zero: the freshness check
	// treats an absent version as unknown rather than as describing the first
	// revision of the document.
	versions map[string]*int
	// seq numbers every publish as it arrives, which gives two publishes a
	// total order even when neither carries a version. It is the only way to
	// date a versionless publish, and gopls produces those for a file at
	// version 0 — including its analysis of the on-disk copy, which is not
	// necessarily the buffer.
	seq int
	// publishSeq is the seq of the newest publish for a path, and syncSeq the
	// seq at the moment the editor last told the server about that path's
	// current text. A versionless publish at or before syncSeq cannot be proven
	// to describe the current text, so it is stale rather than clean.
	publishSeq map[string]int
	syncSeq    map[string]int

	// waiters are the callers blocked on a path's next publish. The store's own
	// mutex guards the map; a publish closes and clears the channels for that
	// path, so a waiter wakes on the next publish and only that one. It is a
	// notification, not a result: the waiter re-reads the store afterwards.
	waiters map[string][]chan struct{}
}

func newDiagnostics() *diagnostics {
	return &diagnostics{
		byPath:         map[string][]lsp.Diagnostic{},
		publishedPaths: map[string]bool{},
		versions:       map[string]*int{},
		publishSeq:     map[string]int{},
		syncSeq:        map[string]int{},
		waiters:        map[string][]chan struct{}{},
	}
}

// Severity values, as the protocol numbers them.
const (
	sevError   = 1
	sevWarning = 2
	sevInfo    = 3
	sevHint    = 4
)

// set replaces the diagnostics for a document with a publish that carried no
// version.
func (d *diagnostics) set(path string, items []lsp.Diagnostic) {
	d.setVersion(path, items, nil)
}

// setVersion replaces the diagnostics for a document, recording the document
// version the publish applied to alongside them. A nil version means the
// server sent none, and is stored as absent rather than as zero.
//
// An empty list is meaningful and must be stored as a clearing rather than
// ignored: that is how a server says the problems it reported are fixed, and
// dropping it would leave them on screen forever. It still records its
// version, because a clean publish is exactly the reading the freshness check
// has to date.
func (d *diagnostics) setVersion(path string, items []lsp.Diagnostic, version *int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.publishedPaths == nil {
		d.publishedPaths = map[string]bool{}
	}
	// Every publish takes the next seq, so a versionless one can be ordered
	// against the sync that told the server what the buffer now holds.
	d.seq++
	if d.publishSeq == nil {
		d.publishSeq = map[string]int{}
	}
	d.publishSeq[path] = d.seq
	d.publishedPaths[path] = true
	if version == nil {
		delete(d.versions, path)
	} else {
		if d.versions == nil {
			d.versions = map[string]*int{}
		}
		v := *version
		d.versions[path] = &v
	}
	// A publish is a reading whatever it contains, so release the waiters for
	// this path before the empty-list branch can return.
	d.wakeWaiters(path)
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

// noteSynced records that the editor has just told the server about a path's
// current text. A versionless publish at or before this point cannot be a
// reading of that text: it may be the server's analysis of the on-disk file,
// which is exactly the set gopls emits with no version at file version 0.
func (d *diagnostics) noteSynced(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.syncSeq == nil {
		d.syncSeq = map[string]int{}
	}
	d.syncSeq[path] = d.seq
}

// forPath is the problems in a file.
func (d *diagnostics) forPath(path string) []lsp.Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byPath[path]
}

// published reports whether a server has published diagnostics for a path,
// empty or not. A file never heard from and a file the server called clean
// both have no items; only the latter may be read as no problems.
func (d *diagnostics) published(path string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.publishedPaths[path]
}

// publishedVersion is the document version the last publish for a path applied
// to, and whether the server sent one. An absent version is not version zero:
// the caller uses the bool to tell "the server dated this publish" from "the
// server did not", which the freshness check decides differently.
func (d *diagnostics) publishedVersion(path string) (int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.versions[path]
	if !ok || v == nil {
		return 0, false
	}
	return *v, true
}

// reading is the store's current state for a document judged against version:
// the status diagnosticsStatus would give a buffer at that version, the words
// for it, and the items themselves. The synced and buffer versions are the same
// value because a caller only uses this once it has told the server about that
// exact text, which is the moment a publish for it is the reading worth
// returning. It reuses the freshness rule rather than restating it, so the wait
// path's recompute cannot drift from the request's own judgement.
func (d *diagnostics) reading(path string, version int) (status, detail string, items []lsp.Diagnostic) {
	// One lock for the publish fields and the items together: taking them
	// separately could pair one publish's version with another's items and
	// misjudge a fresh set as stale.
	d.mu.Lock()
	status, detail = d.freshnessLocked(path, version, version)
	items = d.byPath[path]
	d.mu.Unlock()
	return status, detail, items
}

// status is the freshness of a path's last publish judged against the version
// the server was last told about and the buffer's own. It is the request path's
// half of reading, and differs from it only in taking the two versions
// separately rather than assuming the sync already happened.
func (d *diagnostics) status(path string, syncedVersion, bufVersion int) (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.freshnessLocked(path, syncedVersion, bufVersion)
}

// freshnessLocked is the shared judgement, called with d.mu held. It is the
// pure diagnosticsStatus rule plus the sequence rule for a publish that carried
// no version. A versionless publish cannot be dated by version, so it counts as
// a reading only if it arrived after the editor last told the server about the
// current text and the buffer has no edits. gopls omits the version for a file
// at version 0, and "version 0" is both the on-disk copy and an unedited
// buffer, so reading its on-disk analysis as clean for an edited buffer is the
// false ok this exists to prevent.
func (d *diagnostics) freshnessLocked(path string, syncedVersion, bufVersion int) (status, detail string) {
	published := d.publishedPaths[path]
	var pubVersion *int
	if v, ok := d.versions[path]; ok && v != nil {
		vv := *v
		pubVersion = &vv
	}
	status, detail = diagnosticsStatus(published, pubVersion, syncedVersion, bufVersion)
	if status != control.LSPStatusOK {
		return status, detail
	}
	if pubVersion == nil {
		// A versionless publish is a reading only of an unedited buffer, and
		// only if it arrived after the sync. gopls omits the version for a
		// file at version 0 — both the on-disk copy and an unedited buffer —
		// so a buffer with edits (version > 0) can never be answered by one:
		// the server has not republished for the text just sent. Treating its
		// on-disk set as clean is the false ok this rule exists to prevent.
		if d.publishSeq[path] <= d.syncSeq[path] || bufVersion != 0 {
			return control.LSPStatusStale,
				"the language server's last publish carried no version and does not describe the current text"
		}
	}
	return status, detail
}

// waitFor blocks until the next publish for path, or until ctx is done. It
// returns true when a publish arrived and false on cancellation or timeout. The
// channel is registered under the same mutex setVersion holds, so a publish that
// lands while the caller is deciding to wait still releases it: registration
// cannot interleave with the close-and-clear a publish performs.
func (d *diagnostics) waitFor(ctx context.Context, path string) bool {
	ch := make(chan struct{})
	d.mu.Lock()
	if d.waiters == nil {
		d.waiters = map[string][]chan struct{}{}
	}
	d.waiters[path] = append(d.waiters[path], ch)
	d.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-ctx.Done():
		// Drop the registration before deciding, so a publish that lands after
		// this point cannot close a channel nobody is reading. A publish that
		// already happened closed ch and was cleared from the map, so the
		// non-blocking check below still sees it.
		d.mu.Lock()
		d.dropWaiter(path, ch)
		d.mu.Unlock()
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
}

// wakeWaiters releases everyone waiting on a path's next publish. Closing the
// channel rather than sending on it does not lose a release: the store mutex is
// held, so a waiter has either not registered yet — and will read the new state
// in its own check — or is already selecting and sees the close. Called with the
// store mutex held.
func (d *diagnostics) wakeWaiters(path string) {
	for _, ch := range d.waiters[path] {
		close(ch)
	}
	delete(d.waiters, path)
}

// dropWaiter forgets one waiter channel that timed out or was cancelled. Called
// with the store mutex held.
func (d *diagnostics) dropWaiter(path string, ch chan struct{}) {
	list := d.waiters[path]
	for i, c := range list {
		if c == ch {
			d.waiters[path] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(d.waiters[path]) == 0 {
		delete(d.waiters, path)
	}
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
	delete(d.publishedPaths, path)
	delete(d.versions, path)
	delete(d.publishSeq, path)
	delete(d.syncSeq, path)
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
	changed := false
	defer func() {
		if changed {
			a.refreshProblems()
		}
	}()
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
				a.diags.setVersion(lsp.Path(d.URI), d.Items, d.Version)
				changed = true
			default:
				goto next
			}
		}
	next:
	}
}

// all is every file with problems, for the problems pane.
func (d *diagnostics) all() []problems.File {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]problems.File, 0, len(d.byPath))
	for path, items := range d.byPath {
		out = append(out, problems.File{Path: path, Items: items})
	}
	return out
}

// refreshProblems hands the store's current contents to the pane.
//
// Push rather than pull: the pane renders every frame and the store is behind a
// mutex the render path should not be taking. Called when diagnostics change
// and when the pane is opened, which are the only two moments its contents can
// differ from what is on screen.
func (a *App) refreshProblems() {
	a.Problems.Set(a.diags.all())
	a.Problems.SetOpenPaths(a.openPaths())
}

// openPaths is every tab's file, for the pane's "open files" filter.
//
// Pushed for the same reason the diagnostics are: the pane knows nothing about
// tabs, and inverting that so it could ask would make a list of problems depend
// on the editor. Unnamed buffers contribute nothing — they have no path for a
// diagnostic to be keyed against either.
func (a *App) openPaths() []string {
	panes := a.Tabs.All()
	out := make([]string, 0, len(panes))
	for _, p := range panes {
		if path := a.docPath(p); path != "" {
			out = append(out, path)
		}
	}
	return out
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
