package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/search"
	"raj/internal/ui"
	"raj/internal/view"
)

func wakeEvent() ui.Event { return ui.Wake{} }
func pid() int            { return os.Getpid() }

// The control socket's other half: what a request MEANS.
//
// Everything here runs on the event thread, called from the Wake case in
// Handle, which is what makes it allowed to touch the model at all. Nothing in
// internal/control can reach these types, so the unsafe version — editing from
// the accept goroutine — is not merely discouraged, it is not expressible.

// StartControl begins listening. Off unless asked for: a listener that exists
// whenever raj runs is an attack surface for a feature most sessions do not
// use, and on a Unix socket the trust boundary is the filesystem.
//
// addr is a socket path or `tcp://host:port`. remoteExec permits a TCP client
// to run commands here — off by default, because a driver in a container
// asking for that is asking to run outside its container.
func (a *App) StartControl(addr string, remoteExec bool) error {
	if addr == "" {
		addr = control.DefaultPath()
	}
	srv, err := control.Listen(addr, func() { a.host.Post(wakeEvent()) })
	if err != nil {
		return err
	}
	srv.AllowRemoteExec = remoteExec
	a.control = srv
	a.seedParticipants()
	return nil
}

// ControlToken is the secret a TCP client must present, and "" when the
// listener is a Unix socket or absent.
func (a *App) ControlToken() string {
	if a.control == nil {
		return ""
	}
	return a.control.Token()
}

// ControlPath is where the listener is, or "" when there is none. Printed at
// startup so a harness does not have to guess.
func (a *App) ControlPath() string {
	if a.control == nil {
		return ""
	}
	return a.control.Path()
}

// Tell sends the user's message to a connected driver.
//
// Safe to call from the event thread, which is the only place it is called
// from: posting to a mailbox never blocks, and the driver's parked recv is
// woken on its own goroutine. Nothing here waits for the driver to read it.
//
// This is the whole editor-side surface, deliberately small. What is missing
// above it is a chord and a prompt — see docs/TODO.md — and leaving that out is not
// an oversight: a binding is a claim on a chord the terminal then stops
// delivering to anything else, and that is a decision about the keymap rather
// than about messaging.
func (a *App) Tell(to uint8, text string) error {
	if a.control == nil {
		return fmt.Errorf("no control listener; start raj with --control")
	}
	return a.control.Send(to, text)
}

// notifySaved tells every connected driver that a buffer reached disk.
//
// Best-effort by design: the user asked for the save, and a driver whose
// mailbox is full or that went away between the listing and the send must not
// turn that into a failed save. Errors are dropped for the same reason. With no
// control listener there is nothing to tell, which is the ordinary case for a
// session started without --control.
//
// The path is the whole message: a driver knows what it asked for and needs to
// know which save landed. It is a helper rather than an inline loop so the
// control save path can call it too once that path shares App.write's
// bookkeeping — see host.Save.
func (a *App) notifySaved(path string) {
	if a.control == nil {
		return
	}
	for _, d := range a.control.Drivers() {
		if !d.Connected {
			continue
		}
		_ = a.Tell(d.ID, "saved "+path)
	}
}

// Drivers lists who can be told something. Empty when nothing has ever
// connected, which is the case a prompt should refuse rather than ask about.
func (a *App) Drivers() []control.Participant {
	if a.control == nil {
		return nil
	}
	return a.control.Drivers()
}

// StopControl stops listening, and removes the socket if there was one. Called on the way out.
func (a *App) StopControl() {
	if a.control != nil {
		a.control.Close()
		a.control = nil
	}
}

// drainControl executes every parked request. One Wake may cover several.
func (a *App) drainControl() {
	if a.control == nil {
		return
	}
	if a.guard == nil {
		a.guard = control.NewGuard(hostOf(a))
		// The registry is how the guard tells an agent from a person; without
		// it, it falls back to the id-range rule.
		a.guard.Participants = a.control.Participants
	}
	for _, p := range a.control.Take() {
		p.Reply(control.Dispatch(a.guard, p.Req))
	}
}

// host implements control.BufferHost. It is a distinct type rather than methods
// on App so that what the socket can reach is a listed surface: adding a verb
// means adding a method here, not finding that one already worked.
type host struct{ a *App }

func hostOf(a *App) control.BufferHost { return host{a} }

func (h host) Root() string { return h.a.root }

// isAgent asks the registry rather than comparing the id to a constant, so a
// second human's text is not mistaken for an agent's.
func (h host) isAgent(id uint8) bool {
	if h.a.control == nil || h.a.control.Participants == nil {
		return piecetable.Author(id).IsAgent()
	}
	return h.a.control.Participants.IsAgent(id)
}

func (h host) Buffers() []control.Buffer {
	out := make([]control.Buffer, 0, h.a.Tabs.Count())
	for _, p := range h.a.Tabs.All() {
		sess := p.File.Session()
		b := control.Buffer{
			Path:    p.File.Path,
			Version: uint64(sess.Version()),
			Dirty:   p.File.ViewDirty(),
			Bytes:   p.File.Len(),
			Lines:   p.File.Lines(),
			Active:  p == h.a.Tabs.Active(),
		}
		// The pending count is what turns "which open files hold decisions"
		// from 1 + N `groups` calls into the one `buffers` call. Moved reuses
		// the DiffPending accounting — members a later edit has moved past so
		// no honest span can be projected — and is only computed when there is
		// a pending change set to account for.
		if pending := sess.Pending(); len(pending) > 0 {
			b.Pending = len(pending)
			for _, d := range sess.DiffPending() {
				b.Moved += d.Moved
			}
		}
		out = append(out, b)
	}
	return out
}

// canonicalPath maps a request's path to the name a tab is keyed on: a
// relative path resolves against the workspace root, and .. and symlink
// spellings collapse to the one file they name. It is the single seam every
// path-taking verb goes through before comparing, so a relative path means the
// same buffer for read as it does for open instead of resolving against the
// process's working directory.
//
// It does not resolve symlinks in the returned path: the name a new buffer is
// stored under stays the spelling the caller used, and it is sameFile that
// makes two spellings of one file compare equal. An empty path is returned
// unchanged because it means the buffer the user is looking at.
func (h host) canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(h.a.root, path)
	}
	return filepath.Clean(path)
}

// find locates an open buffer. Unnamed buffers are unaddressable, for the same
// reason session restore and search cannot reach them: there is no name to ask
// for.
func (h host) find(path string) (*editor.Pane, error) {
	if path == "" {
		if p := h.a.Tabs.Active(); p != nil && p.File.Path != "" {
			return p, nil
		}
		return nil, control.ErrNoBuffer
	}
	want := h.canonicalPath(path)
	for _, p := range h.a.Tabs.All() {
		if sameFile(want, p.File.Path) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
}

// Resolve maps an empty path to the buffer the user is looking at.
func (h host) Resolve(path string) (string, error) {
	p, err := h.find(path)
	if err != nil {
		return "", err
	}
	return p.File.Path, nil
}

func (h host) Open(path string, create bool) (uint64, error) {
	// A path names a file on disk, and a file can be spelled several ways —
	// through a symlink, through .., through the tab's own form. Reopening one
	// that is already open should focus its tab rather than stack a duplicate:
	// the identity comparison is by file, not by string. Canonicalising first
	// is the step every other verb takes, so a relative path opens the buffer
	// its absolute spelling names rather than one keyed on the process's
	// working directory.
	path = h.canonicalPath(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		for _, p := range h.a.Tabs.All() {
			if sameFile(resolved, p.File.Path) && h.a.Tabs.Focus(p) {
				return uint64(p.File.Session().Version()), nil
			}
		}
	}
	// Without -create, open reaches only something that already exists: a
	// buffer already open (which may have been created earlier and never
	// written, so it is not on disk) or a file raj can read. A path that is
	// neither is a typo, and making an empty buffer for it is how a misspelled
	// name later becomes a file. -create is the caller saying it means to make
	// one.
	if !create {
		if p, err := h.find(path); err == nil {
			h.a.Tabs.Focus(p)
			return uint64(p.File.Session().Version()), nil
		}
		if _, err := os.Stat(path); err != nil {
			return 0, fmt.Errorf("no open buffer or file at %s; pass -create to make a new buffer", path)
		}
	}
	h.a.OpenFile(path)
	p, err := h.find(path)
	if err != nil {
		// OpenFile reports its own refusals to the user; the caller gets the
		// same answer rather than a success for a tab that is not there.
		return 0, fmt.Errorf("could not open %s", path)
	}
	return uint64(p.File.Session().Version()), nil
}

// sameFile reports whether two paths name one file. Both are resolved first:
// a symlink is a different name for the same inode, and a brand-new buffer
// does not exist on disk yet, in which case the string form is the best it
// can do.
func sameFile(a, b string) bool {
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		b = r
	}
	if a == b {
		return true
	}
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(sa, sb)
}

// Goto moves the cursor in a buffer, clamping the way the editor's own
// goto-line prompt does: line 9999 in a 300-line file is the end, not an
// error. The line and column are 1-based, or zero when the caller left them
// out — ":40" from a compiler means a column on the line already showing, and
// a missing column means the margin.
func (h host) Goto(path string, line, col int) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	if line <= 0 {
		line, _ = p.File.LineCol(p.Cursors.Primary().Head)
		line++
	}
	if col <= 0 {
		col = 1
	}
	off := p.File.OffsetAt(line-1, col-1)
	p.Cursors.Set(off, off)
	p.Viewport.Center(line-1, p.File.Lines())
	return nil
}

// Close removes a buffer's tab. A buffer with unsaved changes is refused, and
// that refusal is the machine form of the editor's "save first?" prompt: the
// text stays, the driver gets an error, and nobody loses work to a socket
// call.
func (h host) Close(path string) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	if p.File.ViewDirty() {
		return fmt.Errorf("%s has unsaved changes; save or reject them first", p.File.Path)
	}
	for i, q := range h.a.Tabs.All() {
		if q == p {
			h.a.Tabs.CloseIndex(i)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
}

// resolveSpan clamps a byte span to [0, size], refusing the ones no clamp can
// save. A negative start means the whole buffer; a negative end means to the
// end. Anything past size is an error, not a silent empty read: for one agent
// that is a typo, for several writing concurrently it is silent corruption.
func resolveSpan(start, end, size int) (int, int, error) {
	if start < 0 {
		return 0, size, nil
	}
	if start > size || (end >= 0 && (end > size || end < start)) {
		return 0, 0, fmt.Errorf("offset out of range: [%d, %d) is not within [0, %d)", start, end, size)
	}
	if end < 0 {
		end = size
	}
	return start, end, nil
}

// Read hands back the document as authored runs, straight off a projection of
// the session: Spans already reports an author per run, so this is a mapping
// rather than an analysis.
//
// It returns the buffer's own view by default — Project(Annotated), whose text
// is exactly what is on screen, proposed and rejected sets included — because
// apply interprets hunk offsets in the view frame. Returning the agreed
// composition here would make an agent's read and apply disagree whenever a
// decision existed, so read stays on the view until a proper accepted-to-view
// translation exists; the default becoming the agreed composition is a future
// change.
//
// annotated additionally returns the per-run change set and state, so a caller
// can tell accepted from proposed from rejected; the text is the view either
// way.
//
// Byte offsets and -lines are in the returned text's own coordinates, which is
// why the line translation builds an index over the projection it hands back.
func (h host) Read(path string, start, end, lineStart, lineEnd int, annotated bool) ([]control.Span, []control.StateRun, uint64, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, nil, 0, err
	}
	comp := p.File.Session().Project(piecetable.Annotated)
	text := comp.Text()
	if lineStart > 0 {
		// A 1-based inclusive line range, translated by an index over the
		// composition so a driver reads the line a compiler or a search
		// reports without re-implementing the byte model. A missing lineEnd
		// reads to the end; a line past the document clamps to the last one.
		idx := view.NewIndex(text)
		lines := idx.Lines()
		first := lineStart - 1
		if first < 0 {
			first = 0
		}
		if first >= lines {
			first = lines - 1
		}
		start = idx.LineStart(first)
		if lineEnd > 0 && lineEnd < lines {
			end = idx.LineStart(lineEnd) // exclusive: the start of the next line
		} else {
			end = len(text)
		}
	}
	var serr error
	start, end, serr = resolveSpan(start, end, len(text))
	if serr != nil {
		return nil, nil, 0, serr
	}
	var out []control.Span
	for _, s := range comp.Buffer().Spans(start, end-start) {
		if s.Len <= 0 || s.Off < 0 || s.Off+s.Len > len(text) {
			continue
		}
		out = append(out, control.Span{Text: text[s.Off : s.Off+s.Len], Author: uint8(s.Author)})
	}
	if len(out) == 0 && start < end {
		out = []control.Span{{Text: text[start:end], Author: uint8(piecetable.Original)}}
	}
	var states []control.StateRun
	if annotated {
		states = clipStates(comp.States(), start, end)
	}
	return out, states, uint64(p.File.Session().Version()), nil
}

// clipStates trims the Annotated projection's runs to [start, end) and makes
// their offsets relative to the text a read returns, so a caller can align them
// with the spans it was handed. A run that straddles a boundary is cut, never
// dropped: every returned byte still has exactly one owning state.
func clipStates(runs []piecetable.StateRun, start, end int) []control.StateRun {
	var out []control.StateRun
	for _, r := range runs {
		lo, hi := r.Off, r.Off+r.Len
		if r.Len <= 0 || hi <= start || lo >= end {
			continue
		}
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		out = append(out, control.StateRun{
			Off: lo - start, Len: hi - lo, Group: r.Group, State: r.State.String(),
		})
	}
	return out
}

func (h host) Version(path string) (uint64, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, err
	}
	return uint64(p.File.Session().Version()), nil
}

// Apply is the write surface, and the reason offsets rather than text matching:
// ApplyDiff rebases hunks written against base onto whatever the document is
// now, and rejects the ones it cannot place. Staleness is handled by replaying
// the journal, so there is no old_str to get wrong.
func (h host) Apply(path string, author uint8, base uint64, hunks []control.Hunk) (uint64, []control.Conflict, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, nil, err
	}
	size := len(p.File.Text())
	pt := make([]piecetable.Hunk, 0, len(hunks))
	for i, x := range hunks {
		if _, _, serr := resolveSpan(x.Start, x.End, size); serr != nil {
			return 0, nil, fmt.Errorf("hunk %d: %w", i, serr)
		}
		pt = append(pt, piecetable.Hunk{Start: x.Start, End: x.End, Text: x.Text})
	}
	// The connection's own author id, not a blanket Agent: the tint and the
	// undo stacks are per-author, so two connected agents stay distinguishable
	// from each other as well as from the user, and cmd+z swallows neither.
	// Begin/End makes one diff one undo entry.
	p.File.Begin()
	conflicts := p.File.ApplyDiff(piecetable.Author(author), piecetable.Version(base), pt)
	p.File.End()
	// An agent's change set is a proposal, not an edit: it is in the document
	// and visible, but marked as awaiting a decision. Marked here rather than
	// in the piece table because only this layer knows which authors are
	// agents — and a second human's edits must not be marked.
	if h.isAgent(author) {
		p.File.ProposeGroup(p.File.Session().LastGroup())
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)

	var out []control.Conflict
	for _, c := range conflicts {
		out = append(out, control.Conflict{
			Index: c.Index,
			At:    uint64(c.At),
			// Group is the lease owner: zero for a stale offset, nonzero when
			// a pending or rejected change set refused the hunk. It is what
			// lets the CLI tell "read again and resubmit" from "decide about
			// this text first" instead of printing one message for both.
			Group: c.Group,
			Hunk:  control.Hunk{Start: c.Hunk.Start, End: c.Hunk.End, Text: c.Hunk.Text},
		})
	}
	return uint64(p.File.Session().Version()), out, nil
}

// snapshot is a dump's editor-side copy: the text captured, and the version and
// byte range it was taken at, so a patch can diff old against new and rebase
// onto whatever the document is now.
type snapshot struct {
	author  uint8
	path    string
	version uint64
	start   int
	end     int
	text    string
	hash    string
}

// snapshotHash is a short, content-derived tag a driver can echo back to verify
// it edited the text it was handed rather than a stale copy.
func snapshotHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// Dump captures [start,end) as a snapshot the same writer can patch back. The
// span resolves the way read's does: a missing start means the whole file, a
// missing end reads to the end, and a span past the buffer is an error.
func (h host) Dump(path string, start, end int, author uint8) (uint64, uint64, string, string, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, 0, "", "", err
	}
	text := p.File.Text()
	var serr error
	start, end, serr = resolveSpan(start, end, len(text))
	if serr != nil {
		return 0, 0, "", "", serr
	}
	chunk := text[start:end]
	h.a.snapSeq++
	id := h.a.snapSeq
	if h.a.snapshots == nil {
		h.a.snapshots = make(map[uint64]snapshot)
	}
	h.a.snapshots[id] = snapshot{
		author: author, path: p.File.Path,
		version: uint64(p.File.Session().Version()),
		start:   start, end: end, text: chunk, hash: snapshotHash(chunk),
	}
	return id, uint64(p.File.Session().Version()), chunk, snapshotHash(chunk), nil
}

// Patch diffs the snapshot's text against newText and rebases the change onto
// the current document, so concurrent edits elsewhere in the file are preserved
// and only the chunk's own change is applied. It refuses a snapshot this writer
// does not own, or one whose buffer has moved on to a different name.
func (h host) Patch(path string, author uint8, id uint64, newText string) (uint64, []control.Conflict, error) {
	snap, ok := h.a.snapshots[id]
	if !ok || snap.author != author {
		return 0, nil, fmt.Errorf("no snapshot %d for this writer (snapshots are per-writer and evicted on restart)", id)
	}
	p, err := h.find(path)
	if err != nil {
		return 0, nil, err
	}
	if snap.path != p.File.Path {
		return 0, nil, fmt.Errorf("snapshot %d is of %s, not %s", id, snap.path, p.File.Path)
	}
	diffs := control.DiffLines(snap.text, newText)
	if len(diffs) == 0 {
		// The caller returned the text unchanged; there is no change set to
		// record, and no group to mark.
		return uint64(p.File.Session().Version()), nil, nil
	}
	pt := make([]piecetable.Hunk, 0, len(diffs))
	for _, d := range diffs {
		pt = append(pt, piecetable.Hunk{Start: snap.start + d.Start, End: snap.start + d.End, Text: d.Text})
	}
	p.File.Begin()
	conflicts := p.File.ApplyDiff(piecetable.Author(author), piecetable.Version(snap.version), pt)
	p.File.End()
	if h.isAgent(author) {
		p.File.ProposeGroup(p.File.Session().LastGroup())
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)

	var out []control.Conflict
	for _, c := range conflicts {
		out = append(out, control.Conflict{
			Index: c.Index,
			At:    uint64(c.At),
			// Group is the lease owner: zero for a stale offset, nonzero when
			// a pending or rejected change set refused the hunk. It is what
			// lets the CLI tell "read again and resubmit" from "decide about
			// this text first" instead of printing one message for both.
			Group: c.Group,
			Hunk:  control.Hunk{Start: c.Hunk.Start, End: c.Hunk.End, Text: c.Hunk.Text},
		})
	}
	return uint64(p.File.Session().Version()), out, nil
}

// Dirty reports unsaved buffers, and whether the human wrote any of the unsaved
// text in each.
//
// "AgentOnly" is decided from the piece authors rather than from who touched
// the file last: a buffer an agent edited and the user then typed one character
// into is the user's unsaved work too, and the span authorship is the only
// thing that knows that.
func (h host) Dirty() []control.DirtyBuffer {
	var out []control.DirtyBuffer
	for _, p := range h.a.Tabs.All() {
		if !p.File.ViewDirty() {
			continue
		}
		agentOnly := true
		for _, s := range p.File.Spans(0, p.File.Len()) {
			// Original text is on disk already; only added spans are unsaved.
			// "Not an agent" rather than "== User": with more than one person
			// editing, a second human's text is the user's unsaved work too.
			if s.Author != piecetable.Original && !h.isAgent(uint8(s.Author)) {
				agentOnly = false
				break
			}
		}
		out = append(out, control.DirtyBuffer{Path: p.File.Path, AgentOnly: agentOnly})
	}
	return out
}

// Snapshot copies the dirty buffers. Called on the event thread, and cheap:
// only buffers that differ from disk are worth overlaying, and there are a
// dozen of those against thousands of files.
func (h host) Snapshot() control.Searcher {
	docs := search.Docs{}
	for _, p := range h.a.Tabs.All() {
		if p.File.Path != "" && p.File.ViewDirty() {
			docs[p.File.Path] = p.File.Text()
		}
	}
	return snapshotSearcher{root: h.a.root, docs: docs}
}

// snapshotSearcher walks off the event thread against the copy it was handed,
// so the editor stays responsive for the seconds a search takes and a cancel
// can be serviced while it runs.
type snapshotSearcher struct {
	root string
	docs search.Docs
}

func (s snapshotSearcher) Search(ctx context.Context, q control.SearchQuery,
	emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	root, err := s.walkRoot(q.Path)
	if err != nil {
		return 0, 0, false, nil, err
	}
	res := search.RunStream(ctx, root, search.Query{
		Text: q.Text, Include: q.Include, Exclude: q.Exclude,
		Regex: q.Regex, Case: q.Case, Word: q.Word,
	}, s.docs, func(batch []search.Match) {
		out := make([]control.SearchMatch, 0, len(batch))
		for _, m := range batch {
			out = append(out, control.SearchMatch{
				Path: m.Path, Line: m.Line, Col: m.Col, Len: m.Len,
				LineStart: m.LineStart, ByteStart: m.ByteStart, ByteEnd: m.ByteEnd, Text: m.Text})
		}
		emit(out)
	})
	var truncated []control.TruncatedFile
	for _, f := range res.Truncated() {
		truncated = append(truncated, control.TruncatedFile{Path: f.Path, Shown: f.Shown, Total: f.Total})
	}
	return res.Files, res.Considered, res.Capped, truncated, res.Err
}

// walkRoot resolves a query's -path against the workspace root. A relative
// path is joined to the root; an absolute one is taken as given. Either way it
// must stay inside the workspace, the same rule the Guard applies to exec's
// -dir: the walk is refused rather than allowed to read outside the tree. The
// Guard validates the query before it reaches here, so this check is the
// backstop for a Searcher driven directly.
func (s snapshotSearcher) walkRoot(path string) (string, error) {
	root := filepath.Clean(s.root)
	if path == "" {
		return root, nil
	}
	dir := path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("search path %q is outside the workspace %s", path, root)
	}
	return dir, nil
}

// Groups lists a buffer's change sets.
func (h host) Groups(path string) ([]control.Group, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	var out []control.Group
	for _, g := range p.File.Session().Groups() {
		out = append(out, control.Group{
			ID: g.ID, Path: p.File.Path, Author: uint8(g.Author),
			State: g.State.String(), Ops: g.Ops, Bytes: g.Bytes,
			First: uint64(g.First), Last: uint64(g.Last),
		})
	}
	return out, nil
}

// Diff hands the pending change sets back as old→new hunks. The session owns
// the rendering: the spans come from the same rebase walk that applies and
// reverses edits, so what a reviewer sees is where the change sits now, not
// where it was written.
func (h host) Diff(path string) ([]control.DiffGroup, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	var out []control.DiffGroup
	for _, g := range p.File.Session().DiffPending() {
		dg := control.DiffGroup{
			Group: control.Group{
				ID: g.Group.ID, Path: p.File.Path, Author: uint8(g.Group.Author),
				State: g.Group.State.String(), Ops: g.Group.Ops, Bytes: g.Group.Bytes,
				First: uint64(g.Group.First), Last: uint64(g.Group.Last),
			},
			Moved: g.Moved,
		}
		for _, hk := range g.Hunks {
			dg.Hunks = append(dg.Hunks, control.DiffHunk{
				Start: hk.Start, End: hk.End, Old: hk.Old, New: hk.New})
		}
		out = append(out, dg)
	}
	return out, nil
}

// Review returns a buffer's pending change sets and, unless listOnly, enters
// Review mode at the first one. It is the socket form of the cmd+r toggle and
// the proposals picker: the list is what `groups` shows filtered to the sets
// still awaiting a decision, and EnterReview is the same enter path the chord
// takes, so the two surfaces cannot drift.
func (h host) Review(path string, listOnly bool) ([]control.Group, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	var out []control.Group
	for _, g := range p.File.Session().Pending() {
		out = append(out, control.Group{
			ID: g.ID, Path: p.File.Path, Author: uint8(g.Author),
			State: g.State.String(), Ops: g.Ops, Bytes: g.Bytes,
			First: uint64(g.First), Last: uint64(g.Last),
		})
	}
	if !listOnly {
		h.a.EnterReview()
	}
	return out, nil
}

// Decide accepts or rejects a change set.
//
// Rejecting is a state flip, so its only failure is about the address: an id
// the buffer does not hold, or a set that is already Rejected. False no longer
// means a later edit wedged the reversal — rejecting never removes text.
func (h host) Decide(path string, group uint64, accept bool) error {
	p, err := h.find(path)
	defer h.a.flushJournal(p)
	if err != nil {
		return err
	}
	sess := p.File.Session()
	if accept {
		p.File.AcceptGroup(group)
		return nil
	}
	var found bool
	for _, g := range sess.Groups() {
		if g.ID == group {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no change set %d in %s", group, path)
	}
	if !p.File.RejectGroup(group) {
		return fmt.Errorf("change set %d is already rejected", group)
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)
	return nil
}

// Clear hard-purges a rejected change set. Unlike Decide this really edits:
// ClearRejected reverses the set's ops out of the document and drops the
// decision, so both the text and the mark leave the view. It returns false for
// a set that is not currently rejected or one a later edit has wedged, and the
// refusal names the group because that is how the caller addressed it.
func (h host) Clear(path string, group uint64) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	defer h.a.flushJournal(p)
	if !p.File.ClearRejected(group) {
		return fmt.Errorf("change set %d is not a rejected set that can be cleared "+
			"(it may be proposed, already gone, or wedged behind a later edit)", group)
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)
	return nil
}

// Save is deliberately its own verb: a caller that edits and saves in one step
// gives the user no moment to look at what arrived before it is on disk.
//
// Being its own verb was not enough on its own. Two calls in a row is still no
// moment at all, so a caller whose changes are still proposed is refused here.
// The buffer keeps the text — it is in the document, tinted, exactly where the
// user can see it — and only the user's own save writes it out.
//
// The refusal is deliberately not conditional on who is asking. An agent that
// has had its work accepted can save; one that has not, cannot; and a human
// second participant is subject to the same rule for the same reason. What
// makes a save legitimate is that somebody agreed to the change, not which
// table row the caller occupies.
//
// A UI save announces itself to connected drivers via App.notifySaved; this
// path does not, because it does not share App.write's bookkeeping yet. When
// it does, the same call belongs here.
func (h host) Save(path string) (uint64, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, err
	}
	if pending := p.File.Session().Pending(); len(pending) > 0 {
		return 0, fmt.Errorf("%d proposed change set(s) await the user's approval; "+
			"the edit is in the buffer and will reach disk when they save", len(pending))
	}
	if err := p.File.Save(); err != nil {
		return 0, err
	}
	return uint64(p.File.Session().Version()), nil
}

// LSP prepares a blocking language-server request for a 1-based line and
// column. Diagnostics returns the cached state without touching the server; the
// others sync the document and capture the server connection and position so
// the request runs off the event thread. A nil caller with a non-nil error is
// a clean "no server" or "not ready" answer the driver can retry.
func (h host) LSP(path string, line, col int, mode string) (control.LSPCaller, error) {
	switch mode {
	case "hover", "definition", "references", "completion", "diagnostics":
	default:
		return nil, fmt.Errorf("unknown lsp mode %q (want hover, definition, references, completion or diagnostics)", mode)
	}
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	lpath := h.a.docPath(p)
	if lpath == "" {
		return nil, fmt.Errorf("no path for this buffer")
	}
	if mode == "diagnostics" {
		// The cached state, never a request: a diagnostic set is what the server
		// last published, and the plan's rule is that this mode never blocks. No
		// server is started here either, so the answer must say which state it
		// is: a missing or not-yet-running server also has no diagnostics, and
		// reporting an empty list would read as a clean file.
		ls, st := h.a.servers.state(lpath)
		if ls == nil {
			return lspCaller{mode: "diagnostics", status: lspStatus(st), detail: st.message(lpath)}, nil
		}
		// The published latch is not a freshness proof. An edit after the
		// publish leaves the previous set in place until the server speaks
		// again, so compare the version the publish applied to, and the version
		// the server was last told about, against the buffer's own. A mismatch
		// reports stale rather than a clean file: a per-hunk compile gate that
		// read "not yet republished" as "no problems" would pass broken code.
		var pubVersion *int
		if v, ok := h.a.diags.publishedVersion(lpath); ok {
			pubVersion = &v
		}
		// state returns a server only alongside a live sync, so this branch is
		// unreachable today; guarding it keeps a nil dereference off the event
		// thread if that invariant ever changes.
		if ls.sync == nil {
			return lspCaller{
				mode:   "diagnostics",
				status: control.LSPStatusStarting,
				detail: "the language server is not ready",
			}, nil
		}
		synced, _ := ls.sync.Version(lpath)
		status, detail := diagnosticsStatus(
			h.a.diags.published(lpath), pubVersion,
			synced, int(p.File.Session().Version()),
		)
		return lspCaller{
			mode:   "diagnostics",
			status: status,
			detail: detail,
			diags:  h.a.diags.forPath(lpath),
		}, nil
	}
	if line < 1 || col < 1 {
		return nil, fmt.Errorf("lsp %s needs a 1-based line and column", mode)
	}

	ls, st := h.a.servers.for_(lpath, func() { h.a.host.Post(ui.Wake{}) })
	if ls == nil {
		if msg := st.message(lpath); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("language server not available")
	}
	if !h.a.syncDoc(ls, p) {
		return nil, fmt.Errorf("could not synchronise the document with the server")
	}
	conn := ls.srv.Conn()
	if conn == nil {
		return nil, fmt.Errorf("language server not ready")
	}
	off := p.File.OffsetAt(line-1, col-1)
	pos := lsp.NewDocument(p.File.Text()).Position(off)
	return lspCaller{mode: mode, conn: conn, path: lpath, pos: pos}, nil
}

// lspCaller runs one blocking language-server request off the event thread. It
// is what host.LSP hands the connection, so a slow server blocks the driver's
// request, not the editor.
type lspCaller struct {
	mode   string
	conn   *lsp.Conn
	path   string
	pos    lsp.Position
	diags  []lsp.Diagnostic
	status string
	detail string
}

func (c lspCaller) Run(ctx context.Context) ([]byte, error) {
	var out control.LSPResult
	switch c.mode {
	case "diagnostics":
		out.Status = c.status
		out.Detail = c.detail
		out.Diags = make([]control.LSPDiag, 0, len(c.diags))
		for _, d := range c.diags {
			out.Diags = append(out.Diags, control.LSPDiag{
				Line: d.Range.Start.Line + 1, Col: d.Range.Start.Character + 1,
				Severity: d.Severity, Message: d.Message,
			})
		}
		return json.Marshal(out)
	case "hover":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		h, err := lsp.RequestHover(ctx, c.conn, c.path, c.pos)
		if err != nil {
			return nil, err
		}
		if h != nil {
			out.Text = h.Text
		}
		return json.Marshal(out)
	case "definition":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		locs, err := lsp.RequestDefinition(ctx, c.conn, c.path, c.pos)
		if err != nil {
			return nil, err
		}
		out.Locations = make([]control.LSPLocation, 0, len(locs))
		for _, l := range locs {
			out.Locations = append(out.Locations, control.LSPLocation{
				Path: l.Path, Line: l.Range.Start.Line + 1, Col: l.Range.Start.Character + 1,
			})
		}
		return json.Marshal(out)
	case "references":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		locs, err := lsp.RequestReferences(ctx, c.conn, c.path, c.pos, true)
		if err != nil {
			return nil, err
		}
		out.Locations = make([]control.LSPLocation, 0, len(locs))
		for _, l := range locs {
			out.Locations = append(out.Locations, control.LSPLocation{
				Path: l.Path, Line: l.Range.Start.Line + 1, Col: l.Range.Start.Character + 1,
			})
		}
		return json.Marshal(out)
	case "completion":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		items, _, err := lsp.Completions(ctx, c.conn, c.path, c.pos)
		if err != nil {
			return nil, err
		}
		out.Items = make([]control.LSPItem, 0, len(items))
		for _, it := range items {
			out.Items = append(out.Items, control.LSPItem{
				Label: it.Label, Detail: it.Detail, Kind: completionKindName(it.Kind),
			})
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("unknown lsp mode %q", c.mode)
}

// lspStatus names the state a diagnostics answer reports, so a caller never has
// to read an empty list as "no problems" when the real reason is "no server".
func lspStatus(st serverState) string {
	switch st {
	case serverReady:
		return control.LSPStatusOK
	case serverStarting:
		return control.LSPStatusStarting
	case serverNotStarted:
		return control.LSPStatusNotStarted
	case serverMissing:
		return control.LSPStatusMissing
	case serverGaveUp:
		return control.LSPStatusGaveUp
	default:
		return control.LSPStatusNoServer
	}
}

// diagnosticsStatus decides whether a cached publish is a reading of the
// buffer in front of the caller, and words the reason when it is not.
//
// It is pure so the rule can be tested without a live server. published is the
// store's latch, pubVersion the document version that publish applied to (nil
// when the server sent none), syncedVersion the version the server was last
// told about, and bufVersion the buffer's current version. A set is a real
// reading only when it was published for the text the buffer holds now: a
// publish that predates it, or a server that has not even been told about it,
// is stale rather than clean.
func diagnosticsStatus(published bool, pubVersion *int, syncedVersion, bufVersion int) (status, detail string) {
	switch {
	case !published:
		return control.LSPStatusUnpublished,
			"the language server has not published diagnostics for this file yet"
	case syncedVersion != bufVersion:
		return control.LSPStatusStale,
			"the language server has not been told about the current text yet"
	case pubVersion != nil && *pubVersion != bufVersion:
		return control.LSPStatusStale,
			"the language server's diagnostics are for an earlier version of the file"
	}
	return control.LSPStatusOK, ""
}

// completionKindName folds the protocol's kind number down to the handful a
// driver would switch on.
func completionKindName(k int) string {
	switch k {
	case lsp.KindMethod:
		return "method"
	case lsp.KindFunction:
		return "function"
	case lsp.KindField:
		return "field"
	case lsp.KindVariable:
		return "variable"
	case lsp.KindKeyword:
		return "keyword"
	case lsp.KindSnippet:
		return "snippet"
	}
	return ""
}
