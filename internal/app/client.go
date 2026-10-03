package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/intent"
	"raj/internal/piecetable"
	"raj/internal/prompt"
	"raj/internal/safe"
	"raj/internal/store"
	"raj/internal/ui"
)

// clientConn is the control connection the client uses: a request/response
// call, the root handshake, and a close. It is an interface so a test can
// script the daemon going away and coming back; *control.Client is production.
type clientConn interface {
	Do(control.Request) (control.Response, error)
	ResolveRoots(string) (control.Mapper, error)
	// Roots is the workspace root set the daemon last reported, nil for a
	// server that does not send one. The client adopts it as its visible
	// workspace once the handshake succeeds.
	Roots() []string
	// Kind is the participant kind the daemon granted this connection on the
	// hello reply, learned from the response; "" means an older daemon that
	// sent none, which the attach path reads as unknown and fails open.
	Kind() control.Kind
	Close() error
}

// clientDial opens a control connection. It is a package var so a test can
// script reconnects; dialControl serialises a test write with the watch
// goroutine read, and production leaves it as control.Dial.
var (
	clientDialMu sync.Mutex
	clientDial   = func(addr string) (clientConn, error) { return control.Dial(addr) }
)

// dialControl opens addr through the current clientDial.
func dialControl(addr string) (clientConn, error) {
	clientDialMu.Lock()
	d := clientDial
	clientDialMu.Unlock()
	return d(addr)
}

// clientFile is one buffer the daemon sent, built by the watch goroutine and
// parked for the event thread to install. Building a File only reads the
// snapshot, so it is safe off the event thread; mounting it in a pane is not,
// which is why the two halves are split by the handoff box.
type clientFile struct {
	path string
	file *editor.File
	// deleted is the daemon's answer for this path: its file is gone from
	// disk. It rides the snapshot handoff because the deletion is a fact
	// about the same buffer the snapshot is for, and installClientFile applies
	// it to the pane's tab mark.
	deleted bool
	// adopted marks a file that changed the client view membership — a host
	// proposal the client took on, or a daemon tab it newly mirrors — so the
	// event thread persists the view once it installs.
	adopted bool
	// reveal, when set, is a user-initiated reveal: the pane is focused and the
	// caret placed at the span start (the top of the file for a whole-file
	// reveal). It rides the watch answer and becomes a clientFile here, so a
	// reveal reaches every attached client without a reconnect.
	reveal *control.Reveal
}

// bufferMark is what the client last fetched for one path: the facts that
// decide whether its snapshot is still current. Version moves on a text edit;
// pending, moved and superseded move on a decision, and dirty on a save, none
// of which need move the version. Any difference from the control.Buffer the
// daemon sent means the held snapshot is stale even though its text version
// did not move.
type bufferMark struct {
	version    uint64
	pending    uint64
	moved      uint64
	superseded uint64
	dirty      bool
	// deleted records the daemon saying the file is gone from disk. It is part
	// of the mark because a deletion moves no version and no review count: a
	// held snapshot would otherwise stay "current" while the daemon tab
	// carries the deleted mark, and the client would never fetch the change.
	deleted bool
}

// markOf is the mark for a daemon buffer as fetched: the version the snapshot
// came back with, and the review facts the daemon reported for it.
func markOf(version uint64, b control.Buffer) bufferMark {
	return bufferMark{
		version:    version,
		pending:    uint64(b.Pending),
		moved:      uint64(b.Moved),
		superseded: uint64(b.Superseded),
		dirty:      b.Dirty,
		deleted:    b.Deleted,
	}
}

// matches reports whether the fetched mark still agrees with the current
// daemon facts for the same buffer.
func (m bufferMark) matches(b control.Buffer) bool {
	return m.version == b.Version && m.dirty == b.Dirty &&
		m.pending == uint64(b.Pending) && m.moved == uint64(b.Moved) &&
		m.superseded == uint64(b.Superseded) && m.deleted == b.Deleted
}

// hasUnsavedWork reports whether a daemon buffer is one the viewer mirrors: a
// real tab with unsaved text or a pending change set. A clean tab is the
// daemon own to show and is never adopted by a viewer that did not open it.
func hasUnsavedWork(b control.Buffer) bool {
	return b.Dirty || b.Pending > 0
}

// stillClosed reports whether the daemon buffer still matches the facts a local
// close recorded, so the path stays snoozed. Dirty is ignored: a save clears it
// without changing the work, and a close is not reopened by a save.
func (m bufferMark) stillClosed(b control.Buffer) bool {
	return m.stillClosedMark(markOf(b.Version, b))
}

// stillClosedMark is stillClosed against another mark: the same version and
// review facts, with dirty ignored.
func (m bufferMark) stillClosedMark(n bufferMark) bool {
	return m.version == n.version && m.pending == n.pending &&
		m.moved == n.moved && m.superseded == n.superseded
}

// markFromFile is the daemon facts for a client pane as last fetched: the
// session version plus the review counts the pane carries. It is the mark a
// close records, so the path is snoozed only while the daemon buffer has not
// moved under it. Dirty is left zero because a client file cannot be dirty.
func markFromFile(f *editor.File) bufferMark {
	if f == nil {
		return bufferMark{}
	}
	sess := f.Session()
	m := bufferMark{version: uint64(sess.Version())}
	if pending := sess.Pending(); len(pending) > 0 {
		m.pending = uint64(len(pending))
		for _, d := range sess.DiffPending() {
			m.moved += uint64(d.Moved)
		}
	}
	return m
}

// helloRequest is the hello every client connection sends: the watch and the
// decision connections ask to join the same durable identity, so two clients of
// one workspace stay separate people. It asks for the human kind, but that is
// only a request: the daemon grants it on the local socket and downgrades it to
// an agent on TCP, and the granted kind comes back on the reply for the attach
// to read (see clientConn.Kind).
func (a *App) helloRequest() control.Request {
	key := a.attachKey
	if key == "" {
		key = "attach"
	}
	return control.Request{Op: "hello", Identity: "client:" + key, Name: key,
		Kind: string(control.KindHuman)}
}

// joinClient says hello on a freshly dialed connection, binding it to the
// client's durable identity before any document verb runs. A failure is
// returned so the caller can close the connection and report the status. The
// daemon grants the human kind only on the local socket, so a local client's
// edit lands as accepted text while a TCP client's arrives as a proposal; the
// granted kind is what c.Kind reports and what StartClient's mode follows.
func (a *App) joinClient(c clientConn) error {
	res, err := c.Do(a.helloRequest())
	if err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Err)
	}
	return nil
}

// StartClient connects to the daemon this app was launched to attach to, loads
// its open documents as tabs, starts in the mode the kind the daemon grants
// calls for, and arms a watch. It runs before Run; the connection and the buffer
// marks then belong to the watch goroutine, so the client lock serialises its
// requests. A failure at any step is a status line, not a crash: an attach with
// no daemon says why and leaves an empty editor rather than taking the terminal
// down.
func (a *App) StartClient() {
	// The dot starts red and turns green only once the daemon answers: an
	// attach that fails before it ever connects must not look healthy.
	a.clientMu.Lock()
	a.clientDown = true
	a.clientMu.Unlock()

	addr, err := control.Locate(a.attachAddr, a.primaryRoot())
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	c, err := dialControl(addr)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if err := a.joinClient(c); err != nil {
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	if _, err := c.ResolveRoots(a.primaryRoot()); err != nil {
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	res, err := c.Do(control.Request{Op: "buffers"})
	if err != nil {
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		c.Close()
		a.status = "attach: " + res.Err
		return
	}
	// The daemon's workspace is what the client shows: adopt its root set for
	// the explorer, search, ls and containment. The store was opened in the
	// constructor against the view roots and stays there — the daemon owns the
	// workspace's state database and two processes must not open it. A reply
	// with no set (an old server, or a transport that does not carry one)
	// leaves the launch-root workspace in place.
	if roots := c.Roots(); len(roots) > 0 {
		a.adoptVisibleRoots(roots)
	}
	vers := make(map[string]bufferMark, len(res.Buffers))
	a.clientOwned = make(map[string]bool, len(res.Buffers))
	a.clientClosed = make(map[string]bufferMark)
	a.clientEdits = make(map[string]*clientEdit)
	// The daemon facts for every buffer, keyed by path, seed the marks below.
	// A saved-view path is not necessarily a daemon tab, so an absent entry is
	// the zero buffer: its review facts start at zero and the first wake
	// settles them.
	byPath := make(map[string]control.Buffer, len(res.Buffers))
	for _, b := range res.Buffers {
		if b.Path != "" {
			byPath[b.Path] = b
		}
	}
	var files []clientFile
	var dropped []string
	// The saved view restores the owned tab set and the client own closed
	// marks. Ownership is sticky: a path the client showed before is restored
	// whether or not the daemon still lists it, and the mirror is re-derived
	// below from the daemon's unsaved tabs rather than re-adopted from the
	// saved list.
	if saved, ok := a.readClientView(); ok {
		for _, cm := range saved.Closed {
			if cm.legacy || cm.Path == "" {
				// An old string entry carries no facts to match, so the path is
				// re-added rather than staying closed forever.
				continue
			}
			a.clientClosed[cm.Path] = cm.mark()
		}
		for _, path := range saved.ownedForAttach(byPath) {
			if _, seen := a.clientOwned[path]; seen {
				continue
			}
			b := byPath[path]
			if a.clientTabSnoozed(path, b) {
				continue
			}
			f, v, err := a.fetchSnapshot(c, path)
			if err != nil {
				dropped = append(dropped, path)
				continue
			}
			vers[path] = markOf(v, b)
			a.clientOwned[path] = true
			files = append(files, clientFile{path: path, file: f})
		}
	}
	// Mirror the daemon real tabs that carry unsaved work: a dirty buffer or
	// one with a pending change set. A headless buffer is one an agent loaded
	// to read, and a clean tab is the daemon's own to show; neither is a
	// reason to put a tab in front of the viewer.
	for _, b := range res.Buffers {
		if b.Path == "" || b.Headless || !hasUnsavedWork(b) {
			continue
		}
		if _, seen := a.clientOwned[b.Path]; seen {
			continue
		}
		if a.clientTabSnoozed(b.Path, b) {
			continue
		}
		f, v, err := a.fetchSnapshot(c, b.Path)
		if err != nil {
			c.Close()
			a.status = "attach: " + err.Error()
			return
		}
		vers[b.Path] = markOf(v, b)
		a.clientOwned[b.Path] = true
		files = append(files, clientFile{path: b.Path, file: f})
	}
	// Adoption is what attach means: a host path with a pending change set that
	// the client does not show becomes a tab, so a proposal in a file the client
	// never opened is visible.
	files = append(files, a.adoptPending(c, byPath, vers, a.clientOwned)...)
	// The daemon's deleted flag rides the buffers reply, not the snapshot, so
	// tag each fetched file with it and let the install mark the pane. A path
	// the daemon did not list decodes as the zero buffer: not deleted.
	for i := range files {
		files[i].deleted = byPath[files[i].path].Deleted
	}
	for _, cf := range files {
		a.installClientFile(cf)
	}
	a.saveClientView()
	// A second connection carries decisions, so the event thread never waits on
	// the parked watch for the client lock. It hellos the same durable human
	// identity as the watch, so a decision and a forwarded edit share one
	// author; clear still claims the path on this connection before it clears,
	// because the write gate is per author, not per connection.
	decide, err := dialControl(addr)
	if err != nil {
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	if err := a.joinClient(decide); err != nil {
		decide.Close()
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	if _, err := decide.ResolveRoots(a.primaryRoot()); err != nil {
		decide.Close()
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	a.clientStop = make(chan struct{})
	a.clientDone = make(chan struct{})
	a.publishClient(c, decide)
	// Loading the daemon document is an open, so the editor takes the focus
	// the same way OpenFile gives it. Without this the sidebar keeps the keys
	// and a typed rune is refused by the wrong surface, silently, instead of
	// reaching the editor read-only gate and its note.
	if a.Tabs.Active() != nil {
		a.focus = FocusEditor
	}
	// The mode follows the kind the daemon granted on hello, read from the
	// watch connection that just helloed. A client on the editor's own machine
	// -- the local Unix socket, including one reached over SSH there -- is
	// granted KindHuman and stays fully editable, exactly as before. A client
	// on another machine (TCP) is downgraded to KindAgent and starts in
	// Review, the client's read-only gate, with a status that names it. Any
	// other kind, including the empty kind an older daemon's hello reply
	// carries, is unknown and fails open to ModeEdit, so a field we cannot
	// read can never silently lock a local client out of editing.
	mode, agent := attachStartMode(c.Kind())
	a.mode = mode
	a.attachedAsAgent = agent
	if a.Tabs.Active() == nil {
		// An attach with nothing to show says so rather than presenting an
		// empty editor as if it were the daemon's workspace.
		a.status = "attach: the daemon has no open tabs"
	}
	if len(dropped) > 0 {
		a.status = "attach: dropped " + strings.Join(dropped, ", ")
	}
	if agent {
		// The gate note outranks the attach housekeeping messages: it is the
		// one line that says why a typed edit will not land as this client's
		// own text.
		a.status = attachAgentNote
	}
	// A workspace that differs from the launch roots is named last, so the
	// housekeeping above cannot bury which workspace the client is showing.
	a.noteAttachedRoots()
	a.setClientConnected(true)
	safe.Go(func() {
		defer close(a.clientDone)
		a.watchLoop(c, res.Gen, vers)
	})
}

// attachStartMode maps the participant kind the daemon granted an attached
// client on hello to the mode it starts in and whether it must keep saying its
// edits arrive as proposals. KindHuman -- the editor's own Unix socket, which
// includes a client run over SSH on that machine -- stays fully editable in
// ModeEdit. KindAgent -- a client on another machine, over TCP, downgraded
// because only the local socket is the person's own connection -- starts in
// Review, the client's read-only gate. Anything else, including the empty kind
// an older daemon sends, is unknown and fails open to ModeEdit: a kind we
// cannot name must never silently lock a local client out of editing.
func attachStartMode(kind control.Kind) (mode Mode, agent bool) {
	if kind == control.KindAgent {
		return ModeReview, true
	}
	return ModeEdit, false
}

// attachAgentNote is the status an agent-kind attach starts with: it names the
// read-only gate and what a typed edit would become, not only the mode.
const attachAgentNote = "attached as an agent: read-only; edits would land as proposals"

// attachAgentEditNote is the status when an agent-kind client leaves Review
// with cmd+r. Leaving is allowed, but the note must still say its edits arrive
// as proposals rather than as its own text.
const attachAgentEditNote = "edit mode: attached as an agent; edits land as proposals"

// fetchSnapshot reads one buffer whole document and builds its pane file. The
// decode and the build happen here, off the event thread, so a large document
// does not stall a frame.
func (a *App) fetchSnapshot(c clientConn, path string) (*editor.File, uint64, error) {
	res, err := c.Do(control.Request{Op: "snapshot", Path: path})
	if err != nil {
		return nil, 0, err
	}
	if !res.OK {
		return nil, 0, errors.New(res.Err)
	}
	f, err := a.decodeSnapshot(path, res)
	if err != nil {
		return nil, 0, err
	}
	return f, res.Version, nil
}

// decodeSnapshot turns a snapshot response into a pane file. It is the half
// fetchSnapshot and openRemote share, so a host refusal is handled at the call
// site with its own wording.
func (a *App) decodeSnapshot(path string, res control.Response) (*editor.File, error) {
	sess, err := piecetable.DecodeSnapshot([]byte(res.SnapshotJSON))
	if err != nil {
		return nil, err
	}
	var enc editor.Encoding
	if res.EncodingJSON != "" {
		if err := json.Unmarshal([]byte(res.EncodingJSON), &enc); err != nil {
			return nil, err
		}
	}
	return editor.OpenSnapshotSession(path, sess, enc, a.tabWidth), nil
}

// snapshotDeleted reports whether a snapshot reply said the file it describes is
// gone from disk. It is sparse: an ordinary snapshot carries no buffer record,
// and an old server carries none at all, so absence reads as still present.
func snapshotDeleted(res control.Response, path string) bool {
	for _, b := range res.Buffers {
		if b.Path == path && b.Deleted {
			return true
		}
	}
	return false
}

// clientRetryMin and clientRetryMax bound the reconnect backoff: the first
// retry is quick, then the wait doubles to the cap, so a daemon that stays down
// is not hammered by a busy spin.
const (
	clientRetryMin = 100 * time.Millisecond
	clientRetryMax = 5 * time.Second
)

// watchLoop keeps the view in step with the daemon. It parks one watch
// after another and re-fetches every buffer whose mark no longer matches the
// daemon facts: version, pending, moved, superseded or dirty. A transport
// failure does not end the loop: the client reconnects with backoff and
// re-derives the view, so a daemon restart no longer strands the phone. vers is
// owned by this goroutine alone.
func (a *App) watchLoop(c clientConn, gen uint64, vers map[string]bufferMark) {
	for {
		res, err := c.Do(control.Request{Op: "watch", Gen: gen})
		if err == nil && res.OK {
			// A watch answer means the link is alive.
			a.setClientConnected(true)
			if serr := a.syncClient(c, res, vers); serr == nil {
				gen = res.Gen
				continue
			}
		}
		if a.clientStopped() {
			return
		}
		// The link is gone. Mark it down (the red dot), keep the last document
		// on screen, and dial again; the resync re-fetches the owned paths.
		a.setClientConnected(false)
		nc, ngen, ok := a.reconnectClient(vers)
		if !ok {
			return
		}
		c, gen = nc, ngen
	}
}

// syncClient turns one watch answer into the client files the event thread
// should install. It is the body the watch loop and the reconnect resync share,
// so a reconnected client re-derives its view by the same rules. A transport
// error while fetching is returned, because it means the link went away
// mid-cycle and the caller should reconnect.
func (a *App) syncClient(c clientConn, res control.Response, vers map[string]bufferMark) error {
	// The owned set is the membership: a client-opened path, or a daemon tab
	// with unsaved work that was mirrored, stays a tab until the user closes it
	// here. The mirror is re-derived each cycle, so a clean daemon tab is never
	// taken on and a daemon tab that goes clean keeps its tab rather than
	// dropping.
	a.clientTabMu.Lock()
	owned := make(map[string]bool, len(a.clientOwned))
	for path := range a.clientOwned {
		owned[path] = true
	}
	a.clientTabMu.Unlock()

	byPath := make(map[string]control.Buffer, len(res.Buffers))
	for _, b := range res.Buffers {
		if b.Path == "" {
			continue
		}
		byPath[b.Path] = b
	}

	var files []clientFile
	for path := range owned {
		b, ok := byPath[path]
		if !ok {
			// The daemon dropped it, but the tab is owned: it stays, holding
			// the last snapshot the client had.
			continue
		}
		if a.clientTabSnoozed(path, b) {
			// Closed after this cycle copied the set: skip the fetch. A stale
			// fetch is dropped by the install re-check, which is the durable
			// guard; this one also saves the round trip.
			continue
		}
		if cur, ok := vers[path]; ok && cur.matches(b) {
			continue
		}
		f, fv, err := a.fetchSnapshot(c, path)
		if err != nil {
			return err
		}
		vers[path] = markOf(fv, b)
		files = append(files, clientFile{path: path, file: f})
	}
	// Mirror a daemon real tab with unsaved work that the client does not own,
	// so a new proposal or an unsaved edit appears with no relaunch. A path the
	// user closed is skipped only while the daemon facts still match the close
	// mark; a later proposal, decision or edit clears the mark and the path is
	// re-mirrored.
	for _, b := range res.Buffers {
		if b.Path == "" || b.Headless || !hasUnsavedWork(b) {
			continue
		}
		if _, has := owned[b.Path]; has {
			continue
		}
		if a.clientTabSnoozed(b.Path, b) {
			continue
		}
		f, fv, err := a.fetchSnapshot(c, b.Path)
		if err != nil {
			return err
		}
		vers[b.Path] = markOf(fv, b)
		a.markClientOwned(b.Path)
		owned[b.Path] = true
		files = append(files, clientFile{path: b.Path, file: f, adopted: true})
	}
	// Adopt host buffers that have picked up a pending change set since the
	// last wake, so a proposal in a file the client never opened is visible.
	files = append(files, a.adoptPending(c, byPath, vers, owned)...)
	// A reveal is a user-initiated request to put a path in front of every
	// attached client. It rides the watch answer and becomes a clientFile the
	// event thread installs, so the client mounts or re-points the tab with no
	// reconnect. An explicit reveal overrides a local close, and the path joins
	// the owned set so it persists.
	for _, rev := range res.Reveals {
		if rev.Path == "" {
			continue
		}
		r := rev
		a.clearClientClosed(r.Path)
		if cf := clientFileFor(files, r.Path); cf != nil {
			cf.reveal = &r
			a.markClientOwned(r.Path)
			owned[r.Path] = true
			continue
		}
		b, ok := byPath[r.Path]
		f, fv, err := a.fetchSnapshot(c, r.Path)
		if err != nil {
			return err
		}
		if ok {
			vers[r.Path] = markOf(fv, b)
		} else {
			vers[r.Path] = bufferMark{version: fv}
		}
		a.markClientOwned(r.Path)
		owned[r.Path] = true
		files = append(files, clientFile{path: r.Path, file: f, adopted: true, reveal: &r})
	}
	// Tag each fetched file with the daemon's deleted flag, so the install marks
	// the pane exactly as the daemon's own tab is marked. A path the daemon did
	// not list decodes as the zero buffer: not deleted.
	for i := range files {
		files[i].deleted = byPath[files[i].path].Deleted
	}
	if len(files) > 0 {
		a.clientMu.Lock()
		a.clientFiles = append(a.clientFiles, files...)
		a.clientMu.Unlock()
		a.host.Post(ui.Wake{})
	}
	return nil
}

// clientFileFor returns the queued clientFile for a path, or nil. A reveal
// uses it to attach its span to a snapshot the same watch cycle already queued
// rather than fetching the same buffer twice.
func clientFileFor(files []clientFile, path string) *clientFile {
	for i := range files {
		if files[i].path == path {
			return &files[i]
		}
	}
	return nil
}

// revealClientFile focuses a revealed pane and places its caret at the reveal
// span start (the top of the file for a whole-file reveal), centring the
// viewport. It shares placeCaret with the daemon host, so both sides of the
// same gesture place a caret identically.
func (a *App) revealClientFile(p *editor.Pane, rev *control.Reveal) {
	if p == nil || rev == nil {
		return
	}
	off := 0
	if rev.Start >= 0 {
		off = rev.Start
	}
	placeCaret(p, off)
	a.Tabs.Focus(p)
	if !a.Prompt.Open {
		a.focus = FocusEditor
	}
}

// reconnectClient dials the daemon again with a bounded backoff until it has a
// live watch connection and a re-derived view, or until CloseClient stops it.
// It returns the connection and the generation to re-arm from, and false when
// the client is stopping, so an orderly quit never reconnects.
func (a *App) reconnectClient(vers map[string]bufferMark) (clientConn, uint64, bool) {
	delay := clientRetryMin
	for {
		if a.clientStopped() {
			return nil, 0, false
		}
		c, err := a.dialClient()
		if err == nil {
			gen, ok := a.resyncClient(c, vers)
			if ok {
				decide, derr := a.dialClient()
				if derr == nil {
					if a.clientStopped() {
						// CloseClient ran while this dial was in flight: do
						// not publish a live pair after shutdown.
						c.Close()
						decide.Close()
						return nil, 0, false
					}
					a.publishClient(c, decide)
					// The handshake succeeded, so the link is healthy again:
					// flip the dot now rather than waiting for the watch to
					// return from its park.
					a.setClientConnected(true)
					return c, gen, true
				}
			}
			c.Close()
		}
		select {
		case <-a.clientStop:
			return nil, 0, false
		case <-time.After(delay):
		}
		if delay < clientRetryMax {
			delay *= 2
			if delay > clientRetryMax {
				delay = clientRetryMax
			}
		}
	}
}

// dialClient opens one control connection and completes the root handshake,
// with the explicit address when one was given and discovery otherwise.
func (a *App) dialClient() (clientConn, error) {
	addr, err := control.Locate(a.attachAddr, a.primaryRoot())
	if err != nil {
		return nil, err
	}
	c, err := dialControl(addr)
	if err != nil {
		return nil, err
	}
	if err := a.joinClient(c); err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.ResolveRoots(a.primaryRoot()); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// resyncClient re-derives the view after a reconnect: it reads the daemon
// buffer list, re-fetches every path the client owns and re-runs adoption. The
// marks are emptied first because a restarted daemon numbers its versions from
// zero and a coincidence would otherwise skip the fetch. The returned
// generation is what the watch re-arms from.
func (a *App) resyncClient(c clientConn, vers map[string]bufferMark) (uint64, bool) {
	res, err := c.Do(control.Request{Op: "buffers"})
	if err != nil || !res.OK {
		return 0, false
	}
	// A daemon that came back serving a different workspace must re-root the
	// client before the view is re-derived, or the explorer, search and
	// containment keep describing roots that are no longer there. Adoption
	// mutates event-thread state (the explorer, search and picker panes), so
	// the watch goroutine only queues the set and drainClient adopts it on the
	// event thread.
	roots := res.Roots
	if len(roots) == 0 {
		roots = c.Roots()
	}
	if len(roots) > 0 {
		a.clientMu.Lock()
		a.clientRoots = append([]string(nil), roots...)
		a.clientMu.Unlock()
		a.host.Post(ui.Wake{})
	}
	for path := range vers {
		delete(vers, path)
	}
	if err := a.syncClient(c, res, vers); err != nil {
		return 0, false
	}
	return res.Gen, true
}

// sameRootSet reports whether two root lists name the same roots in the same
// order.
func sameRootSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// noteAttachedRoots names the visible workspace in the status line when it
// differs from the roots this client was launched with. A client launched in a
// parent of the daemon's workspace otherwise renders a tree with no line
// saying which workspace it is; the note is what makes the next such report
// self-explaining. When the visible and launch sets agree the line is left
// untouched, and any message already there is kept after the note so attach
// housekeeping is not lost.
func (a *App) noteAttachedRoots() {
	if note := clientRootsNote(a.visible.All(), a.roots.All()); note != "" {
		if a.status == "" {
			a.status = note
		} else {
			a.status = note + "; " + a.status
		}
	}
}

// clientRootsNote is the status for a visible workspace that differs from the
// roots the client was launched with: "attached to <root>[, <root>...]". It is
// empty when the two sets agree, which is the ordinary attach where the launch
// root already is the workspace.
func clientRootsNote(visible, launch []string) string {
	if sameRootSet(visible, launch) {
		return ""
	}
	return "attached to " + strings.Join(visible, ", ")
}

// publishClient swaps in a connection pair and closes the old ones. The
// decision connection is read on the event thread, so the swap is under
// clientConnMu; closing the old pair after the swap means a decision in flight
// sees an error rather than a half-state.
func (a *App) publishClient(c, decide clientConn) {
	a.clientConnMu.Lock()
	oldWatch, oldDecide := a.client, a.clientDecide
	a.client, a.clientDecide = c, decide
	a.clientConnMu.Unlock()
	if oldWatch != nil {
		_ = oldWatch.Close()
	}
	if oldDecide != nil {
		_ = oldDecide.Close()
	}
}

// decideClient is the decision connection under the connection mutex, or nil
// when the client is not attached.
func (a *App) decideClient() clientConn {
	a.clientConnMu.Lock()
	defer a.clientConnMu.Unlock()
	return a.clientDecide
}

// clientStopped reports whether CloseClient has run. A nil stop channel means
// the client never got as far as starting, which is not a stop.
func (a *App) clientStopped() bool {
	if a.clientStop == nil {
		return false
	}
	select {
	case <-a.clientStop:
		return true
	default:
		return false
	}
}

// setClientConnected records the transport state the connection dot shows,
// waking the event thread only when it changes.
func (a *App) setClientConnected(up bool) {
	a.clientMu.Lock()
	if a.clientDown == !up {
		a.clientMu.Unlock()
		return
	}
	a.clientDown = !up
	a.clientMu.Unlock()
	a.host.Post(ui.Wake{})
}

// clientIsDown reports whether the attached client has lost its daemon. It is
// the connection dot state: true between a transport failure and the next
// successful reconnect.
func (a *App) clientIsDown() bool {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	return a.clientDown
}

// drainClient installs any files the watch goroutine rebuilt and shows a lost
// connection once. It runs on the event thread, which is the only place a pane
// may be mounted.
func (a *App) drainClient() {
	a.clientMu.Lock()
	files := a.clientFiles
	a.clientFiles = nil
	roots := a.clientRoots
	a.clientRoots = nil
	lost := a.clientLost
	a.clientLost = ""
	note := a.clientNote
	a.clientNote = ""
	refetch := a.clientRefetch
	a.clientRefetch = nil
	removals := a.clientRemovals
	a.clientRemovals = nil
	a.clientMu.Unlock()
	if lost != "" {
		a.status = lost
	}
	if note != "" {
		a.status = note
	}
	if len(roots) > 0 && !sameRootSet(a.visible.All(), roots) {
		if a.adoptVisibleRoots(roots) {
			a.noteAttachedRoots()
		}
	}
	adopted := false
	for _, cf := range files {
		if cf.adopted {
			adopted = true
		}
		a.installClientFile(cf)
	}
	// A removal or publish proposal the watch cycle found becomes visible
	// pending state the moment its Wake is collected, so the waiting list, the
	// note and the gate all see it without the daemon re-announcing it.
	for _, pr := range removals {
		a.mirrorRemoval(pr)
	}
	// A refused forward leaves local text the daemon never saw; restore the pane
	// from the daemon now that the queued snapshot decisions have settled.
	for _, path := range refetch {
		for _, p := range a.Tabs.All() {
			if p.File.Path == path {
				a.refetchClient(p)
				break
			}
		}
	}
	if adopted {
		// New membership — an adopted proposal or a newly mirrored daemon tab —
		// is part of the client view, so persist it with the rest; a plain sync
		// update changes no membership and is not written.
		a.saveClientView()
	}
}

// CloseClient stops the watch and drops the connection. Closing the socket is
// what unblocks a parked watch, so this returns even mid-park.
func (a *App) CloseClient() {
	if a.clientStop != nil {
		select {
		case <-a.clientStop:
		default:
			close(a.clientStop)
		}
	}
	a.clientConnMu.Lock()
	watch, decide := a.client, a.clientDecide
	a.client, a.clientDecide = nil, nil
	a.clientConnMu.Unlock()
	if watch != nil {
		_ = watch.Close()
	}
	if decide != nil {
		_ = decide.Close()
	}
}

// installClientFile mounts a daemon buffer: it replaces the file of an open tab
// for the same path, preserving the cursor and viewport, or adds a tab for a
// buffer the client has not seen.
func (a *App) installClientFile(cf clientFile) *editor.Pane {
	// The user may have closed this tab after the snapshot was fetched. The
	// close mark is the guard the watch cannot race: an install that still
	// agrees with the facts recorded at close is a stale fetch and is dropped,
	// while one the daemon has moved past belongs to a later change and is
	// installed.
	if a.clientTabSnoozedFile(cf.path, cf.file) {
		return nil
	}
	// A pane with an unforwarded local edit must not be replaced by a daemon
	// snapshot: the watch would clobber text the daemon has not seen. The
	// forward clears the mark (or re-fetches on refusal), after which the next
	// snapshot installs.
	if a.clientEditBusy(cf.path) {
		return nil
	}
	for _, p := range a.Tabs.All() {
		if p.File.Path == cf.path {
			a.applyClientFile(p, cf.file)
			a.applyClientDiskMark(p, cf.deleted)
			a.revealClientFile(p, cf.reveal)
			return p
		}
	}
	prev := a.Tabs.Active()
	cf.file.SetDark(a.host.Theme().Dark())
	if a.Tabs.TabWidthPinned() {
		cf.file.SetTabWidth(a.tabWidth)
	}
	p := editor.NewPane(cf.file)
	p.Wrap = a.WrapDefault
	p.AutoPairs = a.AutoPairs
	p.Hints = a.InlayHints
	p.SetDisplay(a.displayPolicy())
	a.Tabs.Add(p)
	a.applyClientDiskMark(p, cf.deleted)
	a.recordClientSynced(cf.path, cf.file)
	if cf.reveal != nil {
		// A reveal is the one install that is meant to move the viewer.
		a.revealClientFile(p, cf.reveal)
	} else if prev != nil {
		// A buffer opened on the daemon after the attach is announced by
		// adding the tab, not by stealing the tab the user is reading.
		a.Tabs.Focus(prev)
	}
	return p
}

// applyClientDiskMark copies the daemon's on-disk answer onto a client pane:
// the changed-on-disk mark, and the reason when the reason is deletion. A
// client cannot stat the daemon's filesystem, so this is the only way the tab
// mark reaches it; it is idempotent so an ordinary re-sync does not restate the
// status.
func (a *App) applyClientDiskMark(p *editor.Pane, deleted bool) {
	if p == nil || p.DiskDeleted() == deleted {
		return
	}
	if !deleted {
		// The daemon has the file back; the mark the deletion set is done.
		p.ClearDiskStale()
		return
	}
	p.MarkDiskDeleted()
	a.status = p.File.Name() + " was deleted on disk"
}

// applyClientFile swaps a tab file for a newer snapshot while keeping what the
// viewer was looking at: the cursor line and column, and the viewport scrolled
// by the same number of lines the cursor moved.
func (a *App) applyClientFile(p *editor.Pane, f *editor.File) {
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	top := p.Viewport.Top

	// A snapshot that repeats the document the pane already shows must not
	// rebuild the highlighter: a fresh one is cold, so the file repaints plain
	// until its tokenise lands, and the watch fetches every buffer whenever any
	// one of them moves the generation. The file is still swapped, so the
	// refresh path is unchanged; only the warm spans are carried across.
	if p.File.Path == f.Path && p.File.Syntax.Ready() &&
		p.File.Len() == f.Len() && p.File.Text() == f.Text() {
		f.AdoptSyntax(p.File)
	} else {
		f.SetDark(a.host.Theme().Dark())
	}
	if a.Tabs.TabWidthPinned() {
		f.SetTabWidth(a.tabWidth)
	}
	p.File = f
	// The projection was memoised against the old session; rebuild it before
	// anything measures the pane, exactly as a journal restore does.
	p.SetDisplay(a.displayPolicy())

	off := f.OffsetAt(line, col)
	if off > f.Len() {
		off = f.Len()
	}
	p.Cursors.Set(off, off)

	top += f.LineOf(off) - line
	if max := p.DisplayLines() - 1; top > max {
		top = max
	}
	if top < 0 {
		top = 0
	}
	p.Viewport.Top = top
	// The pane now shows this snapshot, so it is the daemon text a later local
	// edit will be diffed against. Recording it here covers both the install
	// path and a decision's read-back, so every replacement resets the base.
	a.recordClientSynced(p.File.Path, f)
	// Deliberately no FollowCursor: this is a background refresh of a document
	// the viewer may have scrolled away from. The top adjustment above already
	// keeps the same rows under the cursor, and following would instead drag
	// the viewport back to the cursor, which is what makes scrolling a
	// refreshed tab feel impossible until it is reopened.
}

// clientEdit is the daemon state one owned pane's local copy was synced from, so
// a local edit can be forwarded as an apply against that version. synced is the
// text the version describes; pending is the newest local text seen; dirty
// marks an unforwarded change and running marks an active forward loop, so the
// event thread never starts a second one for the same path.
type clientEdit struct {
	version uint64
	synced  string
	pending string
	dirty   bool
	running bool
}

// clientForwardDebounce is how long a burst of typing settles before the edit
// is forwarded, so a fast run of keystrokes becomes one apply rather than one
// per key. It is a var so a test can shorten it.
var clientForwardDebounce = 40 * time.Millisecond

// recordClientSynced records the daemon version and text a pane was installed
// from. It is a no-op for a busy path: a snapshot is not authoritative while a
// local edit is still being forwarded, and a successful forward records its own
// adopted version instead.
func (a *App) recordClientSynced(path string, f *editor.File) {
	if path == "" || f == nil {
		return
	}
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.clientEditDirtyLocked(path) {
		return
	}
	if a.clientEdits == nil {
		a.clientEdits = map[string]*clientEdit{}
	}
	a.clientEdits[path] = &clientEdit{version: uint64(f.Session().Version()), synced: f.Text()}
}

func (a *App) clientEditBusyLocked(path string) bool {
	e, ok := a.clientEdits[path]
	return ok && (e.dirty || e.running)
}

// clientEditDirtyLocked reports an unforwarded local edit, ignoring a forward
// that is merely in flight. A refused forward clears dirty before its loop
// exits, so the refetch it queued is not blocked by the still-running flag.
func (a *App) clientEditDirtyLocked(path string) bool {
	e, ok := a.clientEdits[path]
	return ok && e.dirty
}

// clientEditBusy reports whether a path has an unforwarded local edit or one in
// flight. The install path uses it to leave a dirty pane alone.
func (a *App) clientEditBusy(path string) bool {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	return a.clientEditBusyLocked(path)
}

// clientEditDirty reports an unforwarded local edit, ignoring a forward that is
// only in flight.
func (a *App) clientEditDirty(path string) bool {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	return a.clientEditDirtyLocked(path)
}

// scanClientEdits sweeps every owned client tab for text that differs from the
// daemon text it was synced from and schedules one forward per changed path. It
// runs on the event thread, so the pane text it reads is stable; the forward
// itself is off-thread and coalesced by the per-path running flag.
func (a *App) scanClientEdits() {
	if !a.attach {
		return
	}
	a.clientMu.Lock()
	var started []string
	for _, p := range a.Tabs.All() {
		path := p.File.Path
		e, ok := a.clientEdits[path]
		if !ok || e.running {
			continue
		}
		if len(p.File.Session().Pending()) > 0 && a.clientNote == "" {
			a.clientNote = "agent proposals here"
		}
		cur := p.File.Text()
		if cur == e.synced && !e.dirty {
			continue
		}
		e.pending = cur
		e.dirty = true
		e.running = true
		started = append(started, path)
	}
	a.clientMu.Unlock()
	for _, path := range started {
		path := path
		safe.Go(func() { a.forwardClientEdit(path) })
	}
}

// forwardClientEdit is the off-thread forward loop for one path. It debounces,
// then repeatedly diffs the synced daemon text against the newest local text and
// sends one apply against the version the local copy came from. The daemon's
// own rebase is the merge: nothing is merged locally. An edit made while an
// apply is in flight is forwarded by the next turn of the loop.
func (a *App) forwardClientEdit(path string) {
	for {
		time.Sleep(clientForwardDebounce)
		a.clientMu.Lock()
		e, ok := a.clientEdits[path]
		if !ok {
			a.clientMu.Unlock()
			return
		}
		if !e.dirty {
			e.running = false
			a.clientMu.Unlock()
			return
		}
		base, synced, cur := e.version, e.synced, e.pending
		a.clientMu.Unlock()

		newVersion, ok := a.forwardApply(path, base, synced, cur)

		a.clientMu.Lock()
		if e2, still := a.clientEdits[path]; still && e2 == e {
			if ok {
				e.version, e.synced = newVersion, cur
				if e.pending == cur {
					e.dirty = false
				}
			} else {
				// The daemon kept its text; the refetch queued by forwardApply
				// restores the pane, so stop retrying this path.
				e.synced, e.dirty = cur, false
			}
		}
		a.clientMu.Unlock()
	}
}

// forwardApply sends one apply on the decision connection for a single local
// edit, expressed as the changed middle between the synced text and the local
// text. It claims the path first (learning any agent overlap) and reads the
// version so the write gate is satisfied, then applies against the tracked base.
// The daemon rebases; a success returns the new version.
func (a *App) forwardApply(path string, base uint64, synced, cur string) (uint64, bool) {
	c := a.decideClient()
	if c == nil {
		a.warnClientEdit(path, "attach: not connected to a daemon")
		return 0, false
	}
	if claim, err := c.Do(control.Request{Op: "claim", Paths: []string{path}, ClaimAdd: true}); err == nil && claim.OK {
		a.noteClaimOverlap(c, path, claim.ClaimOverlaps)
	}
	if _, err := c.Do(control.Request{Op: "version", Path: path}); err != nil {
		a.warnClientEdit(path, "attach: "+err.Error())
		return 0, false
	}
	start, endOld, endNew := editMiddle(synced, cur)
	res, err := c.Do(control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: start, End: endOld, Text: cur[start:endNew]}}})
	if err != nil {
		a.warnClientEdit(path, "attach: "+err.Error())
		return 0, false
	}
	if !res.OK {
		a.warnClientEdit(path, "changed elsewhere; your edit was not placed")
		return 0, false
	}
	return res.Version, true
}

// flushClientEdit forwards any pending local edit for a pane synchronously,
// before a save. It runs on the event thread, where a client save already waits
// on the decision connection, so blocking for the round trip costs no more than
// the save itself.
func (a *App) flushClientEdit(p *editor.Pane) {
	if p == nil || p.File.Path == "" {
		return
	}
	path := p.File.Path
	a.clientMu.Lock()
	e, ok := a.clientEdits[path]
	if !ok || !e.dirty {
		a.clientMu.Unlock()
		return
	}
	cur := p.File.Text()
	base, synced := e.version, e.synced
	if cur == synced {
		e.dirty = false
		a.clientMu.Unlock()
		return
	}
	a.clientMu.Unlock()

	newVersion, ok := a.forwardApply(path, base, synced, cur)

	a.clientMu.Lock()
	if e2, still := a.clientEdits[path]; still && e2 == e {
		if ok {
			e.version, e.synced = newVersion, cur
			if e.pending == cur {
				e.dirty = false
			}
		} else {
			e.synced, e.dirty = cur, false
		}
	}
	a.clientMu.Unlock()
}

// editMiddle returns the common prefix and the two suffix cut points of old and
// cur, so the single changed span is old[start:endOld] replaced by
// cur[start:endNew]. Equal strings yield an empty span, which drops out as a
// no-op apply.
func editMiddle(old, cur string) (start, endOld, endNew int) {
	n := len(old)
	if len(cur) < n {
		n = len(cur)
	}
	for start < n && old[start] == cur[start] {
		start++
	}
	endOld, endNew = len(old), len(cur)
	for endOld > start && endNew > start && old[endOld-1] == cur[endNew-1] {
		endOld--
		endNew--
	}
	return start, endOld, endNew
}

// noteClaimOverlap records an agent that shares a claimed path as a short
// status note. It asks the daemon's who list for the kinds rather than inferring
// one from the author id, because a second human also writes above the agent
// base.
func (a *App) noteClaimOverlap(c clientConn, path string, overlaps []control.ClaimOverlap) {
	if len(overlaps) == 0 {
		return
	}
	who, err := c.Do(control.Request{Op: "who"})
	if err != nil || !who.OK {
		return
	}
	kinds := make(map[uint8]control.Kind, len(who.Participants))
	for _, p := range who.Participants {
		kinds[p.ID] = p.Kind
	}
	for _, o := range overlaps {
		if kinds[o.Author] != control.KindAgent {
			continue
		}
		name := o.Identity
		if name == "" {
			name = "an agent"
		}
		a.noteClient(agentOverlapNote(name, path))
		return
	}
}

// agentOverlapNote is the short status for an agent that shares a claimed file.
func agentOverlapNote(name, path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	return "agent " + name + " has claimed " + path
}

// noteClient queues a one-line status for the event thread.
func (a *App) noteClient(note string) {
	a.clientMu.Lock()
	a.clientNote = note
	a.clientMu.Unlock()
	a.host.Post(ui.Wake{})
}

// warnClientEdit queues a forward refusal as the status and asks the event
// thread to refresh the pane from the daemon, so local text the daemon never
// saw does not survive the refusal.
func (a *App) warnClientEdit(path, note string) {
	a.clientMu.Lock()
	a.clientNote = note
	if path != "" {
		seen := false
		for _, p := range a.clientRefetch {
			if p == path {
				seen = true
				break
			}
		}
		if !seen {
			a.clientRefetch = append(a.clientRefetch, path)
		}
	}
	a.clientMu.Unlock()
	a.host.Post(ui.Wake{})
}

// saveRemote saves the daemon buffer for an attached client. The daemon save is
// the one that writes: it refuses while proposals await the user, and that
// refusal is the status here, with the snapshot copy left alone.
func (a *App) saveRemote(p *editor.Pane, then func(saved bool)) {
	// An unnamed buffer has no daemon path to name. Sending an empty one would
	// let the daemon resolve it to the buffer the user is looking at -- an
	// unrelated file -- save that, and then the read-back below would put that
	// file's text over this pane. saveNow refuses before reaching here; this
	// keeps the wire invariant even if a later caller forgets.
	if p.File.Path == "" {
		a.status = "attach: this buffer has no path; an unnamed buffer has no daemon file to save"
		report(then, false)
		return
	}
	res, err := a.sendDecision("save", p.File.Path, 0)
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !res.OK {
		a.status = res.Err
		report(then, false)
		return
	}
	if !a.refetchClient(p) {
		report(then, false)
		return
	}
	a.status = "saved " + p.File.Name()
	report(then, true)
}

// saveAsRemote is the attached form of saveAs. The client owns no bytes and its
// own filesystem is not the daemon's, so a path must be resolved in the daemon's
// workspace: the prompt is seeded at the visible (daemon) root, and a relative
// answer is joined to that root exactly as the local save-as joins to the local
// one. The write itself is a daemon sequence, because only the daemon can create
// the file.
func (a *App) saveAsRemote(p *editor.Pane, then func(saved bool)) {
	root := a.visible.Primary()
	a.askPath("Save as", root+string(filepath.Separator), func(answer string, ok bool) {
		if !ok || answer == "" {
			a.status = "save cancelled"
			report(then, false)
			return
		}
		path := answer
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		a.writeRemoteAs(p, path, then)
	})
}

// writeRemoteAs writes the pane's typed text to path on the daemon. The client
// cannot stat the daemon's disk, so an open without create asks whether the path
// is already held; a held path raises the same overwrite question the local
// save-as asks, and only its answer proceeds to the write.
func (a *App) writeRemoteAs(p *editor.Pane, path string, then func(saved bool)) {
	c := a.decideClient()
	if c == nil {
		a.status = "attach: not connected to a daemon"
		report(then, false)
		return
	}
	probe, err := c.Do(control.Request{Op: "open", Path: path})
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !probe.OK {
		// Not a buffer and not a file on the daemon: the path is new.
		a.writeRemoteText(c, p, path, then)
		return
	}
	a.confirm("File exists", filepath.Base(path)+" already exists. Overwrite?",
		[]string{prompt.Overwrite, prompt.Cancel}, func(ans string, ok bool) {
			if !ok || ans != prompt.Overwrite {
				a.status = "save cancelled"
				report(then, false)
				return
			}
			a.writeRemoteText(c, p, path, then)
		})
}

// writeRemoteText creates or reuses the daemon buffer at path, replaces its
// whole text with the pane's typed text, and saves. The open carries create so
// the daemon claims the path for this human writer, and the version read that
// follows satisfies the read-before-write gate; the replacement is the whole
// buffer, so a new file and an overwritten one take the same path.
func (a *App) writeRemoteText(c clientConn, p *editor.Pane, path string, then func(saved bool)) {
	text := p.File.Text()
	opened, err := c.Do(control.Request{Op: "open", Path: path, Create: true})
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !opened.OK {
		a.status = opened.Err
		report(then, false)
		return
	}
	ver, err := c.Do(control.Request{Op: "version", Path: path})
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !ver.OK {
		a.status = ver.Err
		report(then, false)
		return
	}
	base := opened.Version
	res, err := c.Do(control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: ver.Bytes, Text: text}}})
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !res.OK {
		a.status = res.Err
		report(then, false)
		return
	}
	saved, err := c.Do(control.Request{Op: "save", Path: path})
	if err != nil {
		a.status = "attach: " + err.Error()
		report(then, false)
		return
	}
	if !saved.OK {
		a.status = saved.Err
		report(then, false)
		return
	}
	a.adoptRemotePath(p, path, saved.Version, text)
	a.status = "saved " + p.File.Name()
	report(then, true)
}

// adoptRemotePath names a client pane that has just been saved to a daemon path.
// The pane keeps the typed text; the edit tracker is rekeyed from the unnamed
// buffer to the path at the version the save produced, so the next local edit
// rebases on the saved text, and the path joins the owned set so the view
// persists it.
func (a *App) adoptRemotePath(p *editor.Pane, path string, version uint64, text string) {
	p.File.SetPath(path)
	a.clientMu.Lock()
	if a.clientEdits == nil {
		a.clientEdits = map[string]*clientEdit{}
	}
	delete(a.clientEdits, "")
	a.clientEdits[path] = &clientEdit{version: version, synced: text}
	a.clientMu.Unlock()
	a.markClientOwned(path)
	a.saveClientView()
	if a.Explorer != nil && a.Explorer.Tree != nil {
		a.Explorer.Tree.Refresh()
	}
}

// openRemote opens a workspace path on the client by snapshot alone. The
// daemon snapshot goes through findOrLoad, which loads a closed file headlessly:
// the daemon gains no visible tab, so a file opened on the phone does not
// appear on the laptop. A path the daemon refuses — outside the workspace,
// binary, too large, an encoding it cannot read — is its error and adds no tab.
func (a *App) openRemote(path string, focus bool) {
	res, err := a.sendDecision("snapshot", path, 0)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	f, err := a.decodeSnapshot(path, res)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	a.clearClientClosed(path)
	p := a.installClientFile(clientFile{path: path, file: f, deleted: snapshotDeleted(res, path)})
	if p == nil {
		return
	}
	a.markClientOwned(path)
	a.saveClientView()
	if focus {
		a.Tabs.Focus(p)
		a.focus = FocusEditor
	}
	a.status = fileWarning(p.File)
}

// adoptPending loads any host path with a pending change set that the client
// does not already show, so an agent proposal in a file the phone never opened
// is visible. It is additive to the restored owned set and the re-derived
// mirror. A path the user closed is skipped only while the daemon still matches
// the facts recorded at close, so a new proposal reopens it. An adopted path
// joins the owned set, so it is persisted and stays after the set is decided
// rather than dropping.
func (a *App) adoptPending(c clientConn, byPath map[string]control.Buffer, vers map[string]bufferMark, owned map[string]bool) []clientFile {
	props, err := c.Do(control.Request{Op: "proposals"})
	if err != nil || !props.OK {
		return nil
	}
	var files []clientFile
	var removals []control.Proposal
	for _, pr := range props.Proposals {
		if pr.Path == "" {
			continue
		}
		if pr.Kind == "delete" || pr.Kind == "rmdir" || pr.Kind == "publish" {
			// A removal or a publish is a workspace-level fact, not text, so it
			// never becomes a snapshot: queue it for the event thread, which
			// owns the pending maps.
			removals = append(removals, pr)
			continue
		}
		if pr.Kind != "set" {
			continue
		}
		if owned[pr.Path] {
			continue
		}
		if a.clientTabSnoozed(pr.Path, byPath[pr.Path]) {
			continue
		}
		f, v, err := a.fetchSnapshot(c, pr.Path)
		if err != nil {
			// The set may have been decided between the rollup and the fetch;
			// skip it rather than failing the watch.
			continue
		}
		// The proposal makes the path owned for good: the tab is the review,
		// and it stays until the user closes it rather than dropping when the
		// set is decided.
		a.markClientOwned(pr.Path)
		owned[pr.Path] = true
		vers[pr.Path] = markOf(v, byPath[pr.Path])
		files = append(files, clientFile{path: pr.Path, file: f, adopted: true})
	}
	a.stageClientRemovals(removals)
	return files
}

// stageClientRemovals queues daemon workspace proposals -- a deletion, a
// dir-removal or a publish -- for the event thread. adoptPending runs on the
// watch goroutine, and pendingDeletions, pendingDirRemovals and
// pendingPublishes belong to the event thread, which reads them every frame;
// the mirror is therefore a handoff the way a snapshot is. A (kind, path)
// already queued is not appended again, and the Wake makes drainClient collect
// the queue even on a cycle that rebuilt no pane.
func (a *App) stageClientRemovals(removals []control.Proposal) {
	if len(removals) == 0 {
		return
	}
	added := false
	a.clientMu.Lock()
	for _, pr := range removals {
		dup := false
		for _, q := range a.clientRemovals {
			if q.Kind == pr.Kind && q.Path == pr.Path {
				dup = true
				break
			}
		}
		if !dup {
			a.clientRemovals = append(a.clientRemovals, pr)
			added = true
		}
	}
	a.clientMu.Unlock()
	if added {
		a.host.Post(ui.Wake{})
	}
}

// mirrorRemoval installs one daemon workspace proposal -- a deletion, a
// dir-removal or a publish -- into the client pending maps. It runs on the
// event thread and is idempotent: a key already pending keeps its original
// author, so a watch cycle that repeats an undecided proposal cannot duplicate
// it.
func (a *App) mirrorRemoval(pr control.Proposal) {
	switch pr.Kind {
	case "delete":
		if a.pendingDeletions == nil {
			a.pendingDeletions = map[string]control.Deletion{}
		}
		if _, ok := a.pendingDeletions[pr.Path]; ok {
			return
		}
		a.pendingDeletions[pr.Path] = control.Deletion{Path: pr.Path, Author: pr.Author}
		// A proposal for the file already on screen raises the gate now, the
		// way ProposeDeletion does on the daemon; without clearing the tracked
		// pane the once-per-focus guard would suppress it.
		if p := a.openDeletionPane(pr.Path); p != nil && p == a.Tabs.Active() {
			a.deletionPromptPane = nil
		}
	case "rmdir":
		if a.pendingDirRemovals == nil {
			a.pendingDirRemovals = map[string]control.DirRemoval{}
		}
		if _, ok := a.pendingDirRemovals[pr.Path]; ok {
			return
		}
		a.pendingDirRemovals[pr.Path] = control.DirRemoval{Path: pr.Path, Author: pr.Author}
	case "publish":
		// A publish is a pinned outward step, and only the daemon holds the
		// pins: the client mirrors the wave name and proposer so the waiting
		// list can show it, and forwards the decision rather than running it.
		if a.pendingPublishes == nil {
			a.pendingPublishes = map[string]intent.Publish{}
		}
		if _, ok := a.pendingPublishes[pr.Path]; ok {
			return
		}
		a.pendingPublishes[pr.Path] = intent.Publish{Name: pr.Path, Author: pr.Author}
	}
}

// dropClientRemoval discards a queued removal the event thread has already
// answered, so a watch cycle that staged the proposal just before the decision
// cannot re-add it after the daemon cleared it.
func (a *App) dropClientRemoval(kind, path string) {
	a.clientMu.Lock()
	out := a.clientRemovals[:0]
	for _, pr := range a.clientRemovals {
		if pr.Kind == kind && pr.Path == path {
			continue
		}
		out = append(out, pr)
	}
	a.clientRemovals = out
	a.clientMu.Unlock()
}

// markClientOwned records that the client shows a tab for path, in the durable
// owned set the saved view persists. A client open and a mirrored daemon tab
// both join it, and only a user close removes one.
func (a *App) markClientOwned(path string) {
	if path == "" {
		return
	}
	a.clientTabMu.Lock()
	if a.clientOwned == nil {
		a.clientOwned = map[string]bool{}
	}
	a.clientOwned[path] = true
	a.clientTabMu.Unlock()
}

// markClientClosed records a local close with the daemon facts in view and
// forgets the tab. The mark is a snooze: the watch does not re-add the path
// while the daemon buffer still matches, and re-adds it once the version or
// review state moves. The install step re-checks the mark so a snapshot fetched
// before the close is dropped rather than re-adding the tab.
func (a *App) markClientClosed(path string, f *editor.File) {
	if path == "" {
		return
	}
	mark := markFromFile(f)
	a.clientTabMu.Lock()
	delete(a.clientOwned, path)
	if a.clientClosed == nil {
		a.clientClosed = map[string]bufferMark{}
	}
	a.clientClosed[path] = mark
	a.clientTabMu.Unlock()
}

// clearClientClosed drops the closed mark for a path the user is opening again.
func (a *App) clearClientClosed(path string) {
	if path == "" {
		return
	}
	a.clientTabMu.Lock()
	delete(a.clientClosed, path)
	a.clientTabMu.Unlock()
}

// clientTabClosed reports whether the path carries a local close mark, spent or
// not. It is the test and persistence view; the watch uses clientTabSnoozed so
// a mark whose daemon facts have moved is cleared and the path is re-added.
func (a *App) clientTabClosed(path string) bool {
	a.clientTabMu.Lock()
	defer a.clientTabMu.Unlock()
	_, ok := a.clientClosed[path]
	return ok
}

// clientTabSnoozed reports whether path was closed on the client and the daemon
// buffer still matches the facts recorded at close. A mark that no longer
// matches is spent and cleared, so the caller proceeds to re-add the path:
// that is what makes a later proposal, decision or edit un-snooze it.
func (a *App) clientTabSnoozed(path string, b control.Buffer) bool {
	a.clientTabMu.Lock()
	defer a.clientTabMu.Unlock()
	m, ok := a.clientClosed[path]
	if !ok {
		return false
	}
	if !m.stillClosed(b) {
		delete(a.clientClosed, path)
		return false
	}
	return true
}

// clientTabSnoozedFile is clientTabSnoozed against a fetched snapshot, for the
// install guard: an install whose facts still match the mark is a stale
// snapshot fetched before the close, while one the daemon has moved past
// belongs to a later change and is installed.
func (a *App) clientTabSnoozedFile(path string, f *editor.File) bool {
	a.clientTabMu.Lock()
	defer a.clientTabMu.Unlock()
	m, ok := a.clientClosed[path]
	if !ok {
		return false
	}
	if !m.stillClosedMark(markFromFile(f)) {
		delete(a.clientClosed, path)
		return false
	}
	return true
}

// clientView is the client owned tab set persisted per workspace and client
// key: the paths it shows and the daemon facts each locally closed path was
// closed at. The documents come from the daemon on attach; the mirror of the
// daemon's unsaved tabs is re-derived at attach and on every watch, never
// saved, so a clean daemon tab can never be re-adopted from the view.
type clientView struct {
	// Owned is the persistent membership. A path enters when the client opens
	// it or mirrors a daemon tab with unsaved work, and leaves only when the
	// user closes the tab there.
	Owned []string `json:"owned,omitempty"`
	// Open is the older flat shape's union of owned and mirrored paths. It is
	// read only to migrate a view written before the split.
	Open   []string    `json:"open,omitempty"`
	Closed closedMarks `json:"closed"`
}

// ownedForAttach is the owned set to restore on attach. A view written by this
// version names it in Owned. The older flat shape persisted every daemon tab in
// Open, so an Open entry is kept only when the daemon buffer still has unsaved
// work and would be mirrored again; the stale clean entries drop instead of
// being re-adopted as client-owned tabs, which is the leak the split fixes.
func (v clientView) ownedForAttach(byPath map[string]control.Buffer) []string {
	if v.Owned != nil || len(v.Open) == 0 {
		return v.Owned
	}
	var out []string
	for _, path := range v.Open {
		if hasUnsavedWork(byPath[path]) {
			out = append(out, path)
		}
	}
	return out
}

// closedMark is one persisted close: the path and the daemon facts recorded at
// close time. legacy marks an entry read from the older []string form, which
// carried no facts; it never snoozes, so the path is re-added on the next
// attach.
type closedMark struct {
	Path       string `json:"path"`
	Version    uint64 `json:"version,omitempty"`
	Pending    uint64 `json:"pending,omitempty"`
	Moved      uint64 `json:"moved,omitempty"`
	Superseded uint64 `json:"superseded,omitempty"`

	legacy bool
}

// mark is the bufferMark the entry records.
func (c closedMark) mark() bufferMark {
	return bufferMark{
		version:    c.Version,
		pending:    c.Pending,
		moved:      c.Moved,
		superseded: c.Superseded,
	}
}

// closedMarks marshals as a list of objects, and reads the older list-of-paths
// form as legacy marks so a saved view from before marks keeps loading.
type closedMarks []closedMark

func (cs *closedMarks) UnmarshalJSON(b []byte) error {
	var objs []closedMark
	if err := json.Unmarshal(b, &objs); err == nil {
		*cs = objs
		return nil
	}
	var paths []string
	if err := json.Unmarshal(b, &paths); err != nil {
		return err
	}
	out := make(closedMarks, 0, len(paths))
	for _, p := range paths {
		out = append(out, closedMark{Path: p, legacy: true})
	}
	*cs = out
	return nil
}

// readClientView returns the saved view for this client key. ok is false when
// there is none, which is the first attach.
func (a *App) readClientView() (clientView, bool) {
	if a.state == nil || a.attachKey == "" {
		return clientView{}, false
	}
	all, err := a.state.Settings(store.ScopeClient)
	if err != nil {
		return clientView{}, false
	}
	raw, ok := all[a.attachKey]
	if !ok {
		return clientView{}, false
	}
	var v clientView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return clientView{}, false
	}
	return v, true
}

// saveClientView records the current tab set and closed set for this client
// key, in the workspace state store beside the editor session. It is called on
// the event thread, where those sets change.
func (a *App) saveClientView() {
	if a.state == nil || a.attachKey == "" {
		return
	}
	a.clientTabMu.Lock()
	v := clientView{
		Owned:  make([]string, 0, len(a.clientOwned)),
		Closed: make(closedMarks, 0, len(a.clientClosed)),
	}
	for path := range a.clientOwned {
		v.Owned = append(v.Owned, path)
	}
	for path, m := range a.clientClosed {
		v.Closed = append(v.Closed, closedMark{
			Path:       path,
			Version:    m.version,
			Pending:    m.pending,
			Moved:      m.moved,
			Superseded: m.superseded,
		})
	}
	a.clientTabMu.Unlock()
	sort.Strings(v.Owned)
	sort.Slice(v.Closed, func(i, j int) bool { return v.Closed[i].Path < v.Closed[j].Path })
	blob, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = a.state.SetSetting(store.ScopeClient, a.attachKey, string(blob))
}

// decideRemote sends one decision to the daemon and re-fetches the buffer, so
// an attached client never mutates its snapshot copy. A host refusal is the
// status and leaves the local tab untouched; a success goes through
// applyClientFile, which keeps the cursor.
func (a *App) decideRemote(p *editor.Pane, group uint64, accept bool) bool {
	op := "reject"
	if accept {
		op = "accept"
	}
	res, err := a.sendDecision(op, p.File.Path, group)
	if err != nil {
		a.status = "attach: " + err.Error()
		return false
	}
	if !res.OK {
		a.status = res.Err
		return false
	}
	if !a.refetchClient(p) {
		return false
	}
	a.status = decidedStatus(accept, group)
	return true
}

// clearRemote clears a rejected set on the daemon. clear is claim-gated on the
// wire, so the decision connection claims the path first, exactly as the CLI
// does; the claim is the same author the clear verb then satisfies.
func (a *App) clearRemote(p *editor.Pane, group uint64) {
	path := p.File.Path
	res, err := a.sendDecision("claim", path, 0)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	res, err = a.sendDecision("clear", path, group)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	if !a.refetchClient(p) {
		return
	}
	a.status = fmt.Sprintf("cleared rejected change set %d", group)
}

// refetchClient replaces a tab file with a fresh snapshot of the same path. It
// is the decision path read-back, so the tab shows the daemon state a decision
// produced rather than a local mutation the next wake would overwrite.
func (a *App) refetchClient(p *editor.Pane) bool {
	if p == nil {
		return false
	}
	if a.clientEditDirty(p.File.Path) {
		// An unforwarded local edit would be lost by the replacement; the next
		// watch install refreshes the pane once it is clean.
		a.status = "attach: local edit pending; refresh skipped"
		return false
	}
	c := a.decideClient()
	if c == nil {
		a.status = "attach: not connected to a daemon"
		return false
	}
	f, _, err := a.fetchSnapshot(c, p.File.Path)
	if err != nil {
		a.status = "attach: " + err.Error()
		return false
	}
	a.applyClientFile(p, f)
	return true
}

// sendDecision sends one verb on the decision connection. group is ignored for
// verbs that carry none, and claim carries the path as its operand list.
func (a *App) sendDecision(op, path string, group uint64) (control.Response, error) {
	c := a.decideClient()
	if c == nil {
		return control.Response{}, errors.New("not attached to a daemon")
	}
	req := control.Request{Op: op}
	if op == "claim" {
		req.Paths = []string{path}
	} else {
		req.Path = path
		req.Group = group
	}
	return c.Do(req)
}

// sendIntent sends an intention command on the decision connection. It is how
// an attached client decides a publish: the pins and the hook live on the
// daemon, so a viewer forwards approve or withdraw rather than running the
// outward step itself.
func (a *App) sendIntent(cmd intent.Command) (control.Response, error) {
	c := a.decideClient()
	if c == nil {
		return control.Response{}, errors.New("not attached to a daemon")
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return control.Response{}, err
	}
	return c.Do(control.Request{Op: "intent", HookJSON: string(payload)})
}

// sendRemovalDecision sends the human answer for a pending removal on the
// decision connection. Both answers are the person's own gesture, so neither
// carries an author: the serve loop stamps the connection's own durable human,
// and the daemon admits a human to retract any pending removal. That is what
// lets an attached client's Withdraw retract an agent's proposal without
// forging the proposer's id. A transport failure or a daemon refusal is
// returned; the caller shows it as the status, as saveRemote does.
func (a *App) sendRemovalDecision(op, path string, approve bool) (control.Response, error) {
	c := a.decideClient()
	if c == nil {
		return control.Response{}, errors.New("not attached to a daemon")
	}
	req := control.Request{Op: op, Path: path, Approve: approve, Withdraw: !approve}
	return c.Do(req)
}
