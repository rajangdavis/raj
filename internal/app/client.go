package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/piecetable"
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
	// remove closes the tab for path instead of installing a file: the daemon
	// no longer lists it as an open tab (it was closed, or it is now headless),
	// so the client must not keep showing it.
	remove bool
	// adopted marks a file that changed the client view membership — a host
	// proposal the client took on, or a daemon tab it newly mirrors — so the
	// event thread persists the view once it installs.
	adopted bool
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
	}
}

// matches reports whether the fetched mark still agrees with the current
// daemon facts for the same buffer.
func (m bufferMark) matches(b control.Buffer) bool {
	return m.version == b.Version && m.dirty == b.Dirty &&
		m.pending == uint64(b.Pending) && m.moved == uint64(b.Moved) &&
		m.superseded == uint64(b.Superseded)
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

// StartClient connects to the daemon this app was launched to attach to, loads
// its open documents as tabs, enters Review and arms a watch. It runs before
// Run; the connection and the buffer marks then belong to the watch goroutine,
// so the client lock serialises its requests. A failure at any step is a status
// line, not a crash: an attach with no daemon says why and leaves an empty
// editor rather than taking the terminal down.
func (a *App) StartClient() {
	// The dot starts red and turns green only once the daemon answers: an
	// attach that fails before it ever connects must not look healthy.
	a.clientMu.Lock()
	a.clientDown = true
	a.clientMu.Unlock()

	addr, err := control.Locate(a.attachAddr, a.root)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	c, err := dialControl(addr)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if _, err := c.ResolveRoots(a.root); err != nil {
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
	vers := make(map[string]bufferMark, len(res.Buffers))
	a.clientMirrored = make(map[string]bool, len(res.Buffers))
	a.clientClosed = make(map[string]bufferMark)
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
	// The saved view restores tab order and the client own closed marks, but it
	// no longer decides membership: the daemon real tabs are mirrored below
	// whether or not the saved view named them, so a tab the daemon opened
	// while the client was away appears on attach.
	if saved, ok := a.readClientView(); ok {
		for _, cm := range saved.Closed {
			if cm.legacy || cm.Path == "" {
				// An old string entry carries no facts to match, so the path is
				// re-added rather than staying closed forever.
				continue
			}
			a.clientClosed[cm.Path] = cm.mark()
		}
		for _, path := range saved.Open {
			if _, seen := a.clientMirrored[path]; seen {
				continue
			}
			b, isTab := byPath[path]
			if a.clientTabSnoozed(path, b) {
				continue
			}
			f, v, err := a.fetchSnapshot(c, path)
			if err != nil {
				dropped = append(dropped, path)
				continue
			}
			vers[path] = markOf(v, b)
			// A saved path the daemon also shows as a real tab is mirrored, so
			// the reconcile drops it when the daemon closes it; one the daemon
			// only has headless stays client-owned.
			a.clientMirrored[path] = isTab && !b.Headless
			files = append(files, clientFile{path: path, file: f})
		}
	}
	// Mirror the daemon real tabs. A headless buffer is one an agent loaded to
	// read; showing it would open a file nobody has on screen in the editor
	// that owns the workspace.
	for _, b := range res.Buffers {
		if b.Path == "" || b.Headless {
			continue
		}
		if _, seen := a.clientMirrored[b.Path]; seen {
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
		a.clientMirrored[b.Path] = true
		files = append(files, clientFile{path: b.Path, file: f})
	}
	// Adoption is what attach means: a host path with a pending change set that
	// the client does not show becomes a tab, so a proposal in a file the client
	// never opened is visible.
	files = append(files, a.adoptPending(c, byPath, vers, a.clientMirrored)...)
	for _, cf := range files {
		a.installClientFile(cf)
	}
	a.saveClientView()
	// A second connection carries decisions, so the event thread never waits on
	// the parked watch for the client lock. It is a separate author, which is
	// why clear claims the path on this connection before it clears.
	decide, err := dialControl(addr)
	if err != nil {
		c.Close()
		a.status = "attach: " + err.Error()
		return
	}
	if _, err := decide.ResolveRoots(a.root); err != nil {
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
	// A client is a viewer: Review is the only mode in which an edit cannot
	// reach the daemon document, and it is where a watcher proposal is meant
	// to be read.
	a.EnterReview()
	if a.Tabs.Active() == nil {
		// An attach with nothing to show says so rather than presenting an
		// empty editor as if it were the daemon's workspace.
		a.status = "attach: the daemon has no open tabs"
	}
	if len(dropped) > 0 {
		a.status = "attach: dropped " + strings.Join(dropped, ", ")
	}
	a.setClientConnected(true)
	safe.Go(func() {
		defer close(a.clientDone)
		a.watchLoop(c, res.Gen, vers)
	})
}

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
	// The client owns its tabs, and the daemon decides what that means: the
	// reconcile syncs the paths the client shows and mirrors any daemon real tab
	// it does not yet have. A client-loaded path is headless in the daemon, so a
	// headless filter would drop it; an agent headless read is simply not in the
	// owned set.
	a.clientTabMu.Lock()
	owned := make(map[string]bool, len(a.clientMirrored))
	for path, mirrored := range a.clientMirrored {
		owned[path] = mirrored
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
	for path, mirrored := range owned {
		b, ok := byPath[path]
		if !ok {
			// The daemon dropped it. A mirrored tab is one the laptop closed;
			// a client-loaded buffer may only have been evicted from the
			// headless registry, so it stays.
			if mirrored {
				a.unmarkClientTab(path)
				delete(vers, path)
				files = append(files, clientFile{path: path, remove: true})
			}
			continue
		}
		if a.clientTabSnoozed(path, b) {
			// Closed after this cycle copied the set: skip the fetch. A stale
			// fetch is dropped by the install re-check, which is the durable
			// guard; this one also saves the round trip.
			continue
		}
		if mirrored && b.Headless {
			// The daemon no longer shows it as a real tab.
			a.unmarkClientTab(path)
			delete(vers, path)
			files = append(files, clientFile{path: path, remove: true})
			continue
		}
		if !mirrored && !b.Headless {
			// The daemon opened this path as a real tab, so it becomes a
			// mirror: a later daemon close removes it.
			a.markClientAdopted(path, true)
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
	// A daemon tab opened since the last wake is mirrored, so it appears with
	// no relaunch. A path the user closed is skipped only while the daemon
	// facts still match the close mark; a later proposal, decision or edit
	// clears the mark and the path is re-added.
	for _, b := range res.Buffers {
		if b.Path == "" || b.Headless {
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
		a.markClientAdopted(b.Path, true)
		owned[b.Path] = true
		files = append(files, clientFile{path: b.Path, file: f, adopted: true})
	}
	// Adopt host buffers that have picked up a pending change set since the
	// last wake, so a proposal in a file the client never opened is visible.
	files = append(files, a.adoptPending(c, byPath, vers, owned)...)
	if len(files) > 0 {
		a.clientMu.Lock()
		a.clientFiles = append(a.clientFiles, files...)
		a.clientMu.Unlock()
		a.host.Post(ui.Wake{})
	}
	return nil
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
	addr, err := control.Locate(a.attachAddr, a.root)
	if err != nil {
		return nil, err
	}
	c, err := dialControl(addr)
	if err != nil {
		return nil, err
	}
	if _, err := c.ResolveRoots(a.root); err != nil {
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
	for path := range vers {
		delete(vers, path)
	}
	if err := a.syncClient(c, res, vers); err != nil {
		return 0, false
	}
	return res.Gen, true
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
	lost := a.clientLost
	a.clientLost = ""
	a.clientMu.Unlock()
	if lost != "" {
		a.status = lost
	}
	adopted := false
	for _, cf := range files {
		if cf.adopted {
			adopted = true
		}
		a.installClientFile(cf)
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
// buffer the client has not seen. A removal closes the tab instead, for a path
// the daemon no longer has open.
func (a *App) installClientFile(cf clientFile) *editor.Pane {
	if cf.remove {
		a.removeClientTab(cf.path)
		return nil
	}
	// The user may have closed this tab after the snapshot was fetched. The
	// close mark is the guard the watch cannot race: an install that still
	// agrees with the facts recorded at close is a stale fetch and is dropped,
	// while one the daemon has moved past belongs to a later change and is
	// installed.
	if a.clientTabSnoozedFile(cf.path, cf.file) {
		return nil
	}
	for _, p := range a.Tabs.All() {
		if p.File.Path == cf.path {
			a.applyClientFile(p, cf.file)
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
	if prev != nil {
		// A buffer opened on the daemon after the attach is announced by
		// adding the tab, not by stealing the tab the user is reading.
		a.Tabs.Focus(prev)
	}
	return p
}

// applyClientFile swaps a tab file for a newer snapshot while keeping what the
// viewer was looking at: the cursor line and column, and the viewport scrolled
// by the same number of lines the cursor moved.
func (a *App) applyClientFile(p *editor.Pane, f *editor.File) {
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	top := p.Viewport.Top

	f.SetDark(a.host.Theme().Dark())
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
	p.FollowCursor()
}

// removeClientTab closes the tab for a path the daemon no longer lists as open.
// It is a reconcile, not a user close, so it never asks about unsaved work: the
// client copy is the daemon, and the daemon has already moved on.
func (a *App) removeClientTab(path string) {
	// The watch already unmarked the path; persist the pruned view so a
	// reattach does not resurrect a tab the daemon dropped.
	a.saveClientView()
	for i, p := range a.Tabs.All() {
		if p.File.Path != path {
			continue
		}
		a.closeDoc(p)
		a.Tabs.CloseIndex(i)
		a.refreshProblems()
		if a.Tabs.Active() == nil {
			a.status = "attach: no open tabs"
		}
		return
	}
}

// saveRemote saves the daemon buffer for an attached client. The daemon save is
// the one that writes: it refuses while proposals await the user, and that
// refusal is the status here, with the snapshot copy left alone.
func (a *App) saveRemote(p *editor.Pane, then func(saved bool)) {
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
	p := a.installClientFile(clientFile{path: path, file: f})
	if p == nil {
		return
	}
	a.markClientTab(path)
	a.saveClientView()
	if focus {
		a.Tabs.Focus(p)
		a.focus = FocusEditor
	}
	a.status = fileWarning(p.File)
}

// adoptPending loads any host path with a pending change set that the client
// does not already show, so an agent proposal in a file the phone never opened
// is visible. It is additive to both the first-attach mirror and a saved view.
// A path the user closed is skipped only while the daemon still matches the
// facts recorded at close, so a new proposal reopens it. An adopted path joins
// the owned set so the reconcile keeps syncing it, and it is never auto-removed
// when the proposals are decided: the tab stays a normal client tab.
func (a *App) adoptPending(c clientConn, byPath map[string]control.Buffer, vers map[string]bufferMark, owned map[string]bool) []clientFile {
	props, err := c.Do(control.Request{Op: "proposals"})
	if err != nil || !props.OK {
		return nil
	}
	var files []clientFile
	for _, pr := range props.Proposals {
		if pr.Kind != "set" || pr.Path == "" {
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
		// A host real tab is mirrored, so the reconcile removes it if the host
		// closes it; a host-headless buffer is client-owned, so it is not
		// dropped by the headless rule.
		mirrored := !byPath[pr.Path].Headless
		a.markClientAdopted(pr.Path, mirrored)
		owned[pr.Path] = mirrored
		vers[pr.Path] = markOf(v, byPath[pr.Path])
		files = append(files, clientFile{path: pr.Path, file: f, adopted: true})
	}
	return files
}

// markClientAdopted records a path the client took on because the host has a
// pending change set there. It joins the owned set so the reconcile keeps
// syncing it; mirrored is true when the host shows it as a real tab.
func (a *App) markClientAdopted(path string, mirrored bool) {
	if path == "" {
		return
	}
	a.clientTabMu.Lock()
	if a.clientMirrored == nil {
		a.clientMirrored = map[string]bool{}
	}
	a.clientMirrored[path] = mirrored
	a.clientTabMu.Unlock()
}

// markClientTab records that the client shows a tab for path. The watch reads
// the set to decide what to sync, so a client-loaded headless buffer keeps
// updating even though the daemon does not list it as a real tab.
func (a *App) markClientTab(path string) {
	if path == "" {
		return
	}
	a.clientTabMu.Lock()
	if a.clientMirrored == nil {
		a.clientMirrored = map[string]bool{}
	}
	a.clientMirrored[path] = false
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
	delete(a.clientMirrored, path)
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

// clientView is the client own tab set persisted per workspace and client key:
// the paths it shows and the daemon facts each locally closed path was closed
// at. The documents come from the daemon on attach.
type clientView struct {
	Open   []string    `json:"open"`
	Closed closedMarks `json:"closed"`
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
		Open:   make([]string, 0, len(a.clientMirrored)),
		Closed: make(closedMarks, 0, len(a.clientClosed)),
	}
	for path := range a.clientMirrored {
		v.Open = append(v.Open, path)
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
	sort.Strings(v.Open)
	sort.Slice(v.Closed, func(i, j int) bool { return v.Closed[i].Path < v.Closed[j].Path })
	blob, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = a.state.SetSetting(store.ScopeClient, a.attachKey, string(blob))
}

// unmarkClientTab forgets a client tab. Closing is local: the daemon buffer and
// any daemon tab are untouched.
func (a *App) unmarkClientTab(path string) {
	if path == "" {
		return
	}
	a.clientTabMu.Lock()
	delete(a.clientMirrored, path)
	a.clientTabMu.Unlock()
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
