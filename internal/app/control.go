package app

import (
	"context"
	"fmt"
	"os"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/piecetable"
	"raj/internal/search"
	"raj/internal/ui"
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
// above it is a chord and a prompt — see TODO.md — and leaving that out is not
// an oversight: a binding is a claim on a chord the terminal then stops
// delivering to anything else, and that is a decision about the keymap rather
// than about messaging.
func (a *App) Tell(to uint8, text string) error {
	if a.control == nil {
		return fmt.Errorf("no control listener; start raj with --control")
	}
	return a.control.Send(to, text)
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
		out = append(out, control.Buffer{
			Path:    p.File.Path,
			Version: uint64(p.File.Session().Version()),
			Dirty:   p.File.Dirty(),
			Bytes:   p.File.Len(),
			Lines:   p.File.Lines(),
		})
	}
	return out
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
	for _, p := range h.a.Tabs.All() {
		if p.File.Path == path {
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

func (h host) Open(path string) (uint64, error) {
	h.a.OpenFile(path)
	p, err := h.find(path)
	if err != nil {
		// OpenFile reports its own refusals to the user; the caller gets the
		// same answer rather than a success for a tab that is not there.
		return 0, fmt.Errorf("could not open %s", path)
	}
	return uint64(p.File.Session().Version()), nil
}

// Read hands back the document as authored runs, straight off the piece table:
// Spans already reports an author per run, so this is a projection rather than
// an analysis.
func (h host) Read(path string) ([]control.Span, uint64, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, 0, err
	}
	text := p.File.Text()
	var out []control.Span
	for _, s := range p.File.Spans(0, len(text)) {
		if s.Len <= 0 || s.Off < 0 || s.Off+s.Len > len(text) {
			continue
		}
		out = append(out, control.Span{Text: text[s.Off : s.Off+s.Len], Author: uint8(s.Author)})
	}
	if len(out) == 0 && text != "" {
		out = []control.Span{{Text: text, Author: uint8(piecetable.Original)}}
	}
	return out, uint64(p.File.Session().Version()), nil
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
	pt := make([]piecetable.Hunk, 0, len(hunks))
	for _, x := range hunks {
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
		p.File.Session().MarkGroup(p.File.Session().LastGroup(), piecetable.Proposed)
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)

	var out []control.Conflict
	for _, c := range conflicts {
		out = append(out, control.Conflict{
			Index: c.Index,
			At:    uint64(c.At),
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
		if !p.File.Dirty() {
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
		if p.File.Path != "" && p.File.Dirty() {
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
	emit func([]control.SearchMatch)) (int, bool, error) {
	res := search.RunStream(ctx, s.root, search.Query{
		Text: q.Text, Include: q.Include, Exclude: q.Exclude,
		Regex: q.Regex, Case: q.Case, Word: q.Word,
	}, s.docs, func(batch []search.Match) {
		out := make([]control.SearchMatch, 0, len(batch))
		for _, m := range batch {
			out = append(out, control.SearchMatch{
				Path: m.Path, Line: m.Line, Col: m.Col, Len: m.Len, Text: m.Text})
		}
		emit(out)
	})
	return res.Files, res.Capped, res.Err
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

// Decide accepts or rejects a change set.
//
// Rejection can fail without being an error to retry: a group wedged behind a
// later change that overlaps it cannot be rebased out, and the caller has to
// look at what happened since rather than trying again.
func (h host) Decide(path string, group uint64, accept bool) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	sess := p.File.Session()
	if accept {
		sess.AcceptGroup(group)
		return nil
	}
	var author piecetable.Author
	var found bool
	for _, g := range sess.Groups() {
		if g.ID == group {
			author, found = g.Author, true
		}
	}
	if !found {
		return fmt.Errorf("no change set %d in %s", group, path)
	}
	if !sess.RejectGroup(group, author) {
		return fmt.Errorf("change set %d could not be backed out: later edits "+
			"overlap it, so removing it would leave text nobody wrote", group)
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
