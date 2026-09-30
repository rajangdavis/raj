package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/fslist"
	"raj/internal/hooks"
	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/search"
	"raj/internal/session"
	"raj/internal/store"
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

// StartControl begins listening on one address.
//
// addr is a socket path or `tcp://host:port`. It is the one-address form of
// StartControlAddrs; the editor passes its resolved socket and TCP address
// together through that, so both are up at once.
func (a *App) StartControl(addr string) error {
	return a.StartControlAddrs([]string{addr})
}

// StartControlAddrs begins listening on each address at once, over one queue
// and one registry. The CLI uses it to keep the Unix socket — where a local
// script reads the token and where the filesystem authorises — listening
// alongside a TCP port for a driver that does not share the filesystem. A
// single address is the common case and behaves exactly as StartControl did.
//
// An empty entry means the default socket, so a caller that has a flag can
// pass "" rather than resolve the convention itself.
func (a *App) StartControlAddrs(addrs []string) error {
	if a.standalone {
		// A standalone editor is private: it serves no control listener even
		// when an address was named on the command line.
		return nil
	}
	if len(addrs) == 0 {
		addrs = []string{""}
	}
	clean := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr == "" {
			addr = control.DefaultPath()
		}
		clean = append(clean, addr)
	}
	srv, err := control.ListenAll(clean, func() { a.host.Post(wakeEvent()) })
	if err != nil {
		return err
	}
	a.control = srv
	// Point the server at the persistent run directory before any connection
	// can ask for a detached run. Best-effort: no state directory leaves
	// detached runs unable to start, not the editor unable to run.
	srv.SetHookDir(a.hookRunsDir())
	// The durable mail queue is per workspace and lives in the store: a message
	// sent while the editor is down is replayed when it restarts and that
	// identity parks. A nil store leaves the mailbox in memory, as it was.
	if a.state != nil {
		srv.Mail.SetStore(mailStore{a.state}, srv.Participants)
		srv.Mail.Prune()
	}
	a.seedParticipants()
	srv.Mail.Replay()
	return nil
}

// hookRunsDir is where detached hook runs write their logs, alongside the
// journal and trash in the workspace state directory. It is empty when there is
// no root set, which leaves detached runs refused rather than logging nowhere.
func (a *App) hookRunsDir() string {
	if a.roots.Len() == 0 {
		return ""
	}
	return filepath.Join(session.StateDirForRoots(a.roots.All()), "hook-runs")
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

// ControlPaths is every address the listener is bound to, primary first, for a
// session listening on both a socket and a port at once. Nil when there is no
// listener.
func (a *App) ControlPaths() []string {
	if a.control == nil {
		return nil
	}
	return a.control.Paths()
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
		return fmt.Errorf("no control listener")
	}
	return a.control.Send(to, text)
}

// notifySaved tells every connected driver that a buffer reached disk.
//
// It posts an editor notice (AuthorOriginal), not a message from the user, so
// "from the user" always means a person typed it.
//
// Best-effort by design: the user asked for the save, and a driver whose
// mailbox is full or that went away between the listing and the send must not
// turn that into a failed save. Errors are dropped for the same reason. With no
// control listener there is nothing to tell, which is the ordinary case for a
// client, which serves none.
//
// The path is the whole message: a driver knows what it asked for and needs to
// know which save landed. It is a helper rather than an inline loop so both
// save paths — App.write and host.Save — announce in one place.
func (a *App) notifySaved(path string) {
	if a.control == nil {
		return
	}
	for _, d := range a.control.Drivers() {
		if !d.Connected {
			continue
		}
		// PostNotice, not Tell: a save is the editor's own notice (author 0).
		_ = a.control.PostNotice(d.ID, "saved "+path)
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

// controlTick advances the review generation clients watch whenever the
// workspace review surface moves. It runs on the event thread, so the version
// reads it makes are the model's own. Doing it here rather than at every
// mutation site means no verb — apply, accept, reject, clear, withdraw or a
// removal — can change what a client is looking at and forget to wake a
// watcher.
func (a *App) controlTick() {
	if a.control == nil {
		return
	}
	sum := a.reviewGeneration()
	if sum == a.controlHash {
		return
	}
	a.controlHash = sum
	a.controlGen++
	a.control.BumpGen(a.controlGen)
}

// reviewGeneration folds the whole workspace review surface into one value:
// every open tab's and headless buffer's path, session version, decision
// generation and view-dirty flag, plus every pending workspace removal's path
// and author. Paths are included so a rename or a removal moves the generation
// even when no session
// version does; the decision generation is included so an accept, reject,
// clear or withdraw — decisions that move the composition without moving the
// session version — moves it too; view-dirty is included so a bare save that
// only clears dirty moves it, which neither of the other two does; the
// deleted-on-disk mark is included so a file removed outside the editor moves
// the generation and wakes the parked watchers — a deletion changes no version,
// decision or dirty bit, and an attached client cannot stat the daemon's disk,
// so without it the mark would never be fetched. This is the value clients
// watch, and it will carry a workspace identity when multi-root lands.
//
// It runs on the event thread on every tick, so it stays a cheap fold: ViewDirty
// caches on the session version and decision generation, so an unchanged buffer
// costs two comparisons, and it never calls Proposals or DiffPending. The
// removal keys are sorted so Go's map iteration order cannot change the hash.
func (a *App) reviewGeneration() uint64 {
	h := fnv.New64a()
	write := func(path string, version, decisions uint64, dirty, deleted bool) {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%t\x00%t\n", path, version, decisions, dirty, deleted)
	}
	for _, p := range a.Tabs.All() {
		write(p.File.Path, uint64(p.File.Session().Version()), p.File.DecisionGeneration(), p.File.ViewDirty(), p.DiskDeleted())
	}
	for _, p := range a.headless {
		write(p.File.Path, uint64(p.File.Session().Version()), p.File.DecisionGeneration(), p.File.ViewDirty(), p.DiskDeleted())
	}
	deletions := make([]string, 0, len(a.pendingDeletions))
	for path := range a.pendingDeletions {
		deletions = append(deletions, path)
	}
	sort.Strings(deletions)
	for _, path := range deletions {
		d := a.pendingDeletions[path]
		fmt.Fprintf(h, "delete\x00%s\x00%d\n", path, d.Author)
	}
	dirRemovals := make([]string, 0, len(a.pendingDirRemovals))
	for path := range a.pendingDirRemovals {
		dirRemovals = append(dirRemovals, path)
	}
	sort.Strings(dirRemovals)
	for _, path := range dirRemovals {
		d := a.pendingDirRemovals[path]
		fmt.Fprintf(h, "rmdir\x00%s\x00%d\n", path, d.Author)
	}
	return h.Sum64()
}

// drainJoins persists the author row of every participant that joined since the
// last wake. A join runs on a connection goroutine, where it only queued the
// id; the journal write belongs here, on the thread that owns every log writer,
// and reuses recordAuthor's change check so a repeat hello appends nothing.
func (a *App) drainJoins() {
	if a.control == nil {
		return
	}
	for _, id := range a.control.TakeJoins() {
		a.recordJoin(id)
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
		// The hook Gate is per server, and the guard is the event thread's seam
		// to it: dispatchHook's run admission needs it on the goroutine that
		// owns the model.
		a.guard.HookGate = a.control.HookGate
	}
	for _, p := range a.control.Take() {
		res := control.Dispatch(a.guard, p.Req)
		// Every answer carries the current generation, so a client can take a
		// snapshot and arm a watch from the same number without a second round
		// trip. A watch wake overwrites its own Gen with the moment it woke.
		res.Gen = a.controlGen
		a.notifySuperseded(p.Req, res)
		// Queue the answer rather than sending it here: the client must not
		// learn the request was applied before the frame that shows it exists.
		// Draw flushes the queue once the frame has been presented.
		a.controlReplies = append(a.controlReplies, controlReply{pending: p, res: res})
	}
	// A control-driven change wakes the parked watchers now rather than on the
	// next idle tick, so a decision on one client reaches the others at the
	// same beat its own author sees it. The idle tick still calls controlTick,
	// which is what catches a change made outside a control request.
	// The roster's waiting state is a property of the document — a quiet
	// participant holding a pending proposal — so push the authors that hold
	// one into the server's registry after every batch, where connection
	// goroutines read it lock-guarded.
	a.syncWaiting()
	a.controlTick()
}

// controlReply is one parked request's answer, held from the event handler
// until the frame that reflects the request has been presented.
type controlReply struct {
	pending *control.Pending
	res     control.Response
}

// flushControlReplies delivers, in order, the answers drainControl queued.
// Draw calls it at its end, after the frame is on the host, so a control reply
// follows the frame that reflects the effect it acknowledges. This is an
// ordering guarantee only: each reply is still sent exactly once, and no verb
// behaves differently beyond one frame of latency.
func (a *App) flushControlReplies() {
	if len(a.controlReplies) == 0 {
		return
	}
	replies := a.controlReplies
	a.controlReplies = nil
	for _, r := range replies {
		r.pending.Reply(r.res)
	}
}

// notifySuperseded tells the author of each Proposed set that a landed apply or
// patch moved over it. The editing caller already carries the warning in its
// reply; this is the other half of the spec's reconciliation: the occupying
// author is notified through the mailbox, never summoned, because a gone driver
// cannot be woken and its box keeps until it returns.
//
// Best-effort by construction. The write has already landed, so a full mailbox
// or a connection that went away must not turn the notice into a failed write,
// and the path runs on the event thread where Post never blocks.
func (a *App) notifySuperseded(req control.Request, res control.Response) {
	if a.control == nil || len(res.Warnings) == 0 {
		return
	}
	if req.Op != "apply" && req.Op != "patch" {
		return
	}
	for _, w := range res.Warnings {
		if w.Author == req.Author {
			continue
		}
		// The notice carries no path. The server only has req.Path in the
		// editor's spelling, and the recipient is another writer whose view of
		// the tree is not known here, so an absolute path would be one it
		// cannot resolve; a pathless request has none to name anyway. The set,
		// span and author are what the recipient acts on.
		_ = a.control.PostNotice(w.Author, fmt.Sprintf(
			"change set %d (bytes %d..%d) was landed over by author %d",
			w.Group, w.Start, w.End, req.Author))
	}
}

// host implements control.BufferHost. It is a distinct type rather than methods
// on App so that what the socket can reach is a listed surface: adding a verb
// means adding a method here, not finding that one already worked.
type host struct{ a *App }

func hostOf(a *App) control.BufferHost { return host{a} }

// Projection composes the live buffers under policy, keyed by absolute editor
// path. It maps the control policy onto the piecetable policy App.Project takes,
// so the wire-facing policy never leaks into the composition primitive.
// ProjectionWithProposed is the display composition, its deferral included;
// ProjectionVerifying is the verification composition, with proposed deletions
// applied.
func (h host) Projection(policy control.ProjectionPolicy) map[string][]byte {
	switch policy {
	case control.ProjectionWithProposed:
		return h.a.Project(piecetable.AcceptedAndProposed)
	case control.ProjectionVerifying:
		return h.a.Project(piecetable.AcceptedAndProposedApplied)
	}
	return h.a.Project(piecetable.AcceptedOnly)
}

func (h host) Root() string { return h.a.visible.Primary() }

// Roots is the whole visible workspace root set. The Guard validates a path
// against every root when the host exposes this, and falls back to Root for a
// host that does not. Root stays for the readers that still need exactly one.
// Both read the visible set, so an attach client validates against the daemon's
// workspace rather than the directory it was launched in.
func (h host) Roots() []string { return h.a.visible.All() }

// isAgent asks the registry rather than comparing the id to a constant, so a
// second human's text is not mistaken for an agent's.
func (h host) isAgent(id uint8) bool {
	if h.a.control == nil || h.a.control.Participants == nil {
		return piecetable.Author(id).IsAgent()
	}
	return h.a.control.Participants.IsAgent(id)
}

func (h host) Buffers() []control.Buffer {
	// A headless buffer is listed too, marked so a caller can tell a tab from a
	// buffer it can read but cannot see.
	panes := append(append([]*editor.Pane{}, h.a.Tabs.All()...), h.a.headless...)
	out := make([]control.Buffer, 0, len(panes))
	for _, p := range panes {
		// A read-only view is a local artifact of a diff pane, keyed on a
		// synthetic .raj-seam path that names nothing a socket client could
		// read. It is not a file's text, so it is not a buffer on the wire.
		if p.File.IsReadOnly() {
			continue
		}
		sess := p.File.Session()

		b := control.Buffer{
			Path:     p.File.Path,
			Version:  uint64(sess.Version()),
			Dirty:    p.File.ViewDirty(),
			Bytes:    p.File.Len(),
			Lines:    p.File.Lines(),
			Active:   p == h.a.Tabs.Active(),
			Headless: h.a.isHeadless(p),
			// The file behind the buffer is gone. It is the same fact the tab
			// mark and the save question carry, so a reader with no screen can
			// tell a buffer that is only unsaved from one whose file no longer
			// exists.
			Deleted: p.File.DeletedOnDisk(),
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
		// An invalid set is invisible to Pending: it is Proposed with no
		// surviving hunk to accept, so the count a driver needs is the invalid
		// sets a save will dispose deliberately rather than silently. With no
		// decisions there is nothing invalid, and the walk is skipped.
		if sess.HasDecisions() {
			invalid := 0
			for _, g := range sess.Groups() {
				if g.State == piecetable.Proposed && g.Invalid {
					invalid++
				}
			}
			if invalid > 0 {
				b.Superseded = invalid
			}
		}
		out = append(out, b)
	}
	return out
}

// Screen returns the drawn screen the screen verb reads: the visible rows top
// to bottom, and the caret's 1-based line and column in cells. It reads the
// screen the last frame left behind, which is honest because a control reply
// is flushed only after the frame it acknowledges (see flushControlReplies).
// Rows come from ui.Screen.Row, so trailing spaces are already trimmed, and a
// caret that is not drawn reports zero, which the wire reads as absent.
func (h host) Screen() ([]string, int, int) {
	s := h.a.screen
	if s == nil {
		return nil, 0, 0
	}
	_, rows := s.Size()
	out := make([]string, 0, rows)
	for y := 0; y < rows; y++ {
		out = append(out, s.Row(y))
	}
	line, col := 0, 0
	if s.CursorShown {
		line, col = s.CursorY+1, s.CursorX+1
	}
	return out, line, col
}

// canonicalPat}

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
		path = filepath.Join(h.a.visible.Primary(), path)
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
	// A headless buffer is found by the same comparison as a tab; the only
	// difference between them is presentation.
	if p, ok := h.a.findHeadless(want); ok {
		return p, nil
	}
	return nil, fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
}

// findOrLoad is find for the inspection verbs: a path already loaded is found,
// and one the workspace can read that is not is loaded headlessly and served,
// with no tab. It is deliberately not find itself, so a write or a close does
// not load a file the caller never asked to touch. A path that is missing,
// outside the workspace, unreadable, binary or oversized stays the same "no
// open buffer" error find gave, so a typo is still a typo.
func (h host) findOrLoad(path string) (*editor.Pane, error) {
	if p, err := h.find(path); err == nil {
		return p, nil
	}
	if path == "" {
		return nil, control.ErrNoBuffer
	}
	p, err := h.a.loadHeadless(h.canonicalPath(path))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
	}
	return p, nil
}

// Resolve maps an empty path to the buffer the user is looking at, and loads a
// path the workspace can read headlessly. It is the seam the Guard resolves
// every verb through, so a request against a file nobody has opened reaches a
// loaded buffer rather than an error; whether that buffer gets a tab is the
// business of the verb, not of resolution.
func (h host) Resolve(path string) (string, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return "", err
	}
	return p.File.Path, nil
}

// DocSnapshot hands a client the whole document for one buffer: the encoded
// session, its version, the file encoding and the buffer's own path. The pane is
// found or loaded like any read, but no read is marked — a snapshot is what a
// client renders itself from, not evidence of a write.
func (h host) DocSnapshot(path string) ([]byte, uint64, []byte, string, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return nil, 0, nil, "", err
	}
	sess := p.File.Session()
	snap, err := sess.SnapshotState()
	if err != nil {
		return nil, 0, nil, "", err
	}
	encoded, err := snap.Encode()
	if err != nil {
		return nil, 0, nil, "", err
	}
	encJSON, err := json.Marshal(p.File.Enc)
	if err != nil {
		return nil, 0, nil, "", err
	}
	return encoded, uint64(sess.Version()), encJSON, p.File.Path, nil
}

func (h host) Open(path string, create bool) (uint64, bool, error) {
	// A path names a file on disk, and a file can be spelled several ways —
	// through a symlink, through .., through the tab's own form. Reopening one
	// that is already open reuses its tab rather than stacking a duplicate, and
	// commits a provisional preview. The identity comparison is by file, not by
	// string. Canonicalising first is the step every other verb takes, so a
	// relative path opens the buffer its absolute spelling names rather than one
	// keyed on the process's working directory. None of it moves the user's
	// active tab or focus: revealing a file over the socket is not a request to
	// interrupt.
	path = h.canonicalPath(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		for _, p := range h.a.Tabs.All() {
			if sameFile(resolved, p.File.Path) {
				// A socket open is an explicit request to show the file, so a
				// provisional preview becomes a real tab. It does not move the
				// user's active tab or focus: the tab is there to be found.
				h.a.Tabs.Promote(p)
				return uint64(p.File.Session().Version()), false, nil
			}
		}
	}
	// A headless buffer is already loaded, and open is the request to show it:
	// announce rather than load a second copy under the same path. This also
	// covers -create, which skips the stat below and would otherwise make a
	// duplicate for a file an agent had only inspected. The reveal is quiet, so
	// opening over the socket does not move the user, and it commits a preview
	// the same way Tabs.Open does.
	if p, err := h.find(path); err == nil {
		h.a.announceIfHeadlessQuiet(p)
		h.a.Tabs.Promote(p)
		return uint64(p.File.Session().Version()), false, nil
	}
	// Without -create, open reaches only something that already exists: a
	// buffer already open (which may have been created earlier and never
	// written, so it is not on disk) or a file raj can read. A path that is
	// neither is a typo, and making an empty buffer for it is how a misspelled
	// name later becomes a file. -create is the caller saying it means to make
	// one.
	if !create {
		if _, err := os.Stat(path); err != nil {
			return 0, false, fmt.Errorf("no open buffer or file at %s; pass --create to make a new buffer", path)
		}
	}
	h.a.openFileQuiet(path)
	p, err := h.find(path)
	if err != nil {
		// OpenFile reports its own refusals to the user; the caller gets the
		// same answer rather than a success for a tab that is not there.
		return 0, false, fmt.Errorf("could not open %s", path)
	}
	// A tab appeared where none existed, so this call made a buffer rather
	// than focusing one: created is what tells the driver the difference.
	return uint64(p.File.Session().Version()), true, nil
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
// a missing column means the margin. The line is a session (file) line, not a
// display row: a caller names a place in the document, and a fold must not
// renumber it. The map is used only to place the viewport, so a fold above the
// target still scrolls to the row that draws the position.
func (h host) Goto(path string, line, col int) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	// A caret move is only meaningful on screen, so a headless buffer is
	// announced first: goto is a request to look at a place in a file.
	h.a.announceIfHeadless(p)
	if line <= 0 {
		line, _ = p.File.LineCol(p.Cursors.Primary().Head)
		line++
	}
	if col <= 0 {
		col = 1
	}
	placeCaret(p, p.File.OffsetAt(line-1, col-1))
	// A cursor and viewport move is view state the session records, so persist
	// it the same way the editor's own goto does.
	h.a.TouchSession()
	return nil
}

// Reveal puts a buffer in front of the user and, through the control server,
// in front of every attached client: it loads the path if it is not open,
// gives it a tab and focuses it, and places the caret at the span start. It
// shares placeCaret with Goto so the two caret paths cannot drift, and it
// publishes the reveal on the control server so each parked watch carries the
// path and span to the client that is watching.
func (h host) Reveal(path string, start, end int) error {
	p, err := h.findOrLoad(path)
	if err != nil {
		return err
	}
	// A reveal is a request to look, so it announces a headless buffer and
	// focuses the tab either way; goto stays quiet for an already-open buffer,
	// but a reveal whose whole point is to put a file in front of the user
	// must move the view.
	h.a.announce(p)
	off := 0
	if start >= 0 {
		off = start
	}
	placeCaret(p, off)
	h.a.TouchSession()
	if h.a.control != nil {
		h.a.controlGen = h.a.control.PublishReveal(control.Reveal{
			Path: p.File.Path, Start: start, End: end})
	}
	return nil
}

// placeCaret puts the cursor of p at a byte offset, clamps it into the
// document, and centres the viewport on the row that draws it. It is the one
// caret placement body Goto and Reveal share on the daemon, and an attached
// client calls it through revealClientFile, so the two sides cannot drift.
func placeCaret(p *editor.Pane, off int) {
	if off < 0 {
		off = 0
	}
	if max := p.File.Len(); off > max {
		off = max
	}
	p.Cursors.Set(off, off)
	// The caret is placed file-true; the viewport centres on the row that draws
	// the offset, which a fold above it shifts away from the session line. A
	// line a fold hides maps to its fold row, so the caret is still shown.
	row, _ := p.DispPos(off)
	p.Viewport.Center(row, p.DisplayLines())
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
	// A refused save is protected the same way unsaved changes are: the
	// buffer cannot be written as it stands, so a socket close must not take a
	// view that happens to match disk as permission to drop it.
	if p.File.ViewDirty() || p.SaveRefused() {
		return fmt.Errorf("%s has unsaved changes; save or reject them first", p.File.Path)
	}
	// A headless buffer has no tab to remove; dropping it from the registry is
	// the whole close. The dirty guard above is what keeps it safe, and a
	// headless buffer is always clean because a proposal announces first.
	if h.a.dropHeadless(p) {
		return nil
	}
	for i, q := range h.a.Tabs.All() {
		if q == p {
			// The UI close path (closeTabAt) remembers the position and runs
			// closeDoc before removing a tab, forgetting the journal, the LSP
			// document and the diagnostics. A socket close skipped that,
			// leaving the journal behind to be reopened as a tab on the next
			// start.
			h.a.rememberPosition(p)
			h.a.closeDoc(p)
			h.a.Tabs.CloseIndex(i)
			// The tab set is the session, so a socket close must persist the
			// same way the UI closeTabAt does; without this the tab reappears
			// on the next start.
			h.a.TouchSession()
			return nil
		}
	}
	return fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
}

// CloseDiscard removes a buffer without saving it, discarding unsaved changes
// and any pending change sets. It is the machine form of the editor's
// close-without-save: the file on disk is untouched, the buffer goes away, and
// closeDoc forgets the journal, the LSP document and the diagnostics the same
// way an ordinary close does. It deliberately skips the dirty guard Close
// keeps, which is the point — the caller has decided to drop the work.
func (h host) CloseDiscard(path string) (bool, error) {
	p, err := h.find(path)
	if err != nil {
		return false, err
	}
	// No dirty check: discarding the unsaved edits is the point, and the
	// journal closeDoc removes is what forgets any pending change sets, so a
	// proposal that was never accepted does not come back on reopen.
	if h.a.dropHeadless(p) {
		return h.a.noteDiscardRemainder(p.File.Path), nil
	}
	for i, q := range h.a.Tabs.All() {
		if q == p {
			name := p.File.Path
			h.a.rememberPosition(p)
			h.a.closeDoc(p)
			h.a.Tabs.CloseIndex(i)
			h.a.TouchSession()
			return h.a.noteDiscardRemainder(name), nil
		}
	}
	return false, fmt.Errorf("%w: %s", control.ErrNoBuffer, path)
}

// noteDiscardRemainder says, on the status line, when a discarded buffer still
// has a file on disk, and returns whether it does. close -discard drops the
// buffer and leaves the file exactly as it was, so a driver that recreated the
// content under a new name can otherwise leave the old name behind as a silent
// duplicate. The fact also rides close's wire answer as Response.Remains; the
// status line is the on-screen record for the person at the keyboard, and the
// return value is what the host reports to the caller. It is deliberately
// quiet for a buffer that never reached disk: nothing remains, and there is
// nothing to report.
func (a *App) noteDiscardRemainder(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	a.status = "closed " + filepath.Base(path) + "; the file is still on disk"
	return true
}

// Mkdir creates a directory, with any missing parents, under the workspace
// root. It is a filesystem change rather than a buffer one, so no buffer is
// loaded and the guard has already canonicalised the path and checked it
// in-root. os.MkdirAll makes an existing directory a no-op rather than an
// error, which is what a caller wants when it cannot cheaply know whether the
// directory it is about to write into already exists. The explorer lists the
// tree, so it is refreshed to show the new directory; creating a directory is
// not session state, and the save-as path that also makes one refreshes
// without touching the session either.
func (h host) Mkdir(path string) error {
	path = h.canonicalPath(path)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	h.a.Explorer.Tree.Refresh()
	return nil
}

// Rename moves a file within the workspace. A file nobody has open is an
// ordinary os.Rename. A file with an open clean buffer is carried: the rename
// happens first, then the pane's path follows the file, and everything keyed on
// the old path is forgotten.
//
// A buffer whose file is not on disk — open -create makes one before any bytes
// exist — has no file to move, so the rename is the pane's path alone and works
// even while the buffer is dirty: the work stays in the piece table and follows
// the name. Refusing this was what forced a driver that wanted a new name to
// close -discard and leave the old file behind as a duplicate.
//
// A dirty buffer over an existing file, or one holding a pending change set, is
// still refused and nothing is moved — there is no force path, because the
// on-disk bytes and the buffer would otherwise be reconciled by nobody.
func (h host) Rename(old, new string) error {
	old, new = h.canonicalPath(old), h.canonicalPath(new)
	p, err := h.find(old)
	if err != nil {
		// Not open: nothing follows the name but the file itself.
		if rerr := renameFile(old, new); rerr != nil {
			return rerr
		}
		h.a.Explorer.Tree.Refresh()
		return nil
	}
	if _, statErr := os.Stat(p.File.Path); statErr != nil {
		if !os.IsNotExist(statErr) {
			return statErr
		}
		// No file on disk: carry the pane's name without touching the
		// filesystem. closeDoc forgets everything keyed on the old path,
		// exactly as the ordinary carry below does, and SetPath is the
		// whole move.
		h.a.closeDoc(p)
		p.File.SetPath(new)
		h.a.Explorer.Tree.Refresh()
		h.a.TouchSession()
		return nil
	}
	if ok, why := deletionSafe(p); !ok {
		return fmt.Errorf("%s cannot be renamed: %s", p.File.Path, why)
	}
	if err := renameFile(old, new); err != nil {
		return err
	}
	// Forget everything keyed on the OLD path before the name moves. closeDoc
	// flushes and removes the old journal, tells the language server the old
	// document is gone, and clears the old diagnostics and inlays. The pane
	// itself is kept, so the piece table, version and session tab follow the
	// rename instead of the file being reopened under its new name.
	h.a.closeDoc(p)
	p.File.SetPath(new)
	h.a.Explorer.Tree.Refresh()
	h.a.TouchSession()
	return nil
}

// renameFile moves old to new on disk. A rename that changes only the case of
// the name can be a no-op on a case-insensitive filesystem — the kernel treats
// the two names as one and leaves the old spelling — so it goes through a
// temporary name in the same directory to force the change. Both hops stay in
// one directory, which keeps them on one filesystem and so renameable.
func renameFile(old, new string) error {
	if !strings.EqualFold(old, new) || old == new {
		return os.Rename(old, new)
	}
	tmp := new + ".raj-rename"
	if err := os.Rename(old, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, new); err != nil {
		// Put the file back rather than leaving it under the temporary name:
		// the first hop is the recoverable half, the second is the one that
		// failed.
		_ = os.Rename(tmp, old)
		return err
	}
	return nil
}

// ProposeDeletion records a pending deletion for path, proposed by author. It
// does not unlink anything: delete is a review primitive and the user decides
// whether the file goes. It is idempotent — a second proposal for the same
// path leaves the original author in place, so two agents racing to propose
// one removal do not rewrite who asked.
func (a *App) ProposeDeletion(path string, author uint8) error {
	if a.pendingDeletions == nil {
		a.pendingDeletions = map[string]control.Deletion{}
	}
	if _, ok := a.pendingDeletions[path]; ok {
		return nil
	}
	a.pendingDeletions[path] = control.Deletion{Path: path, Author: author}
	// A proposal for the file already on screen is raised now rather than at
	// the next focus: the gate must not wait for a focus change that may never
	// come. A path open only in the background waits for its turn, so the
	// question never lands over a file the user was not looking at.
	if p := a.openDeletionPane(path); p != nil && p == a.Tabs.Active() {
		a.deletionPromptPane = nil
		a.maybePromptDeletion()
	}
	return nil
}

// authorIsHuman reports whether id may retract any pending removal: the local
// keyboard row, or a durable joined human (an attached client). It shares the
// Guard's own predicate when the app has one, and falls back to the same id
// rule when it does not — the test harness builds an App with no guard — so the
// removal owner check and the Guard cannot drift apart.
func (a *App) authorIsHuman(id uint8) bool {
	if a.guard != nil {
		return a.guard.IsHuman(id)
	}
	return id != control.AuthorOriginal && id < control.FirstAgent
}

// WithdrawDeletion removes the pending deletion for path when author proposed
// it, or when author is a human: a person may retract any pending removal,
// while an agent may retract only its own. A path that is not pending is a
// no-op.
func (a *App) WithdrawDeletion(path string, author uint8) error {
	d, ok := a.pendingDeletions[path]
	if !ok {
		return nil
	}
	if d.Author != author && !a.authorIsHuman(author) {
		return fmt.Errorf("pending deletion of %s was proposed by author %d, not this writer", path, d.Author)
	}
	delete(a.pendingDeletions, path)
	return nil
}

// ApproveDeletion carries out the pending deletion for path: the human answer
// to a proposal, doing exactly what the prompt Remove forever answer does. The
// pending entry is looked up by sameFile as the prompt does, so a symlink
// spelling that reached the map still matches; a path with no pending proposal
// is refused by name rather than silently removing the file. The open pane, if
// any, is handed to removeDeleted so the buffer close, the pending clear, the
// tree refresh and the status line all run in the one removal path.
func (a *App) ApproveDeletion(path string) error {
	d, ok := a.pendingDeletionFor(path)
	if !ok {
		return fmt.Errorf("no pending deletion for %s", path)
	}
	a.removeDeleted(a.openDeletionPane(path), d.Path)
	return nil
}

// Deletions lists the workspace's pending deletions in stable path order, so
// the wire and a reader see the same order every time.
func (a *App) Deletions() []control.Deletion {
	if len(a.pendingDeletions) == 0 {
		return nil
	}
	out := make([]control.Deletion, 0, len(a.pendingDeletions))
	for _, d := range a.pendingDeletions {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ProposeDeletion, WithdrawDeletion and Deletions bridge the BufferHost to the
// app's workspace-level pending-deletion set. They are the only way the
// socket reaches that state, like every other method on host.
func (h host) ProposeDeletion(path string, author uint8) error {
	return h.a.ProposeDeletion(path, author)
}

func (h host) WithdrawDeletion(path string, author uint8) error {
	return h.a.WithdrawDeletion(path, author)
}

func (h host) ApproveDeletion(path string) error { return h.a.ApproveDeletion(path) }

func (h host) Deletions() []control.Deletion { return h.a.Deletions() }

// ProposeDirRemoval records a pending dir-removal for path, proposed by
// author. It removes nothing: rmdir is a review primitive and the user decides
// whether the subtree goes. Idempotent — a second proposal for the same path
// leaves the original author in place, so two agents racing to propose one
// removal do not rewrite who asked.
func (a *App) ProposeDirRemoval(path string, author uint8) error {
	if a.pendingDirRemovals == nil {
		a.pendingDirRemovals = map[string]control.DirRemoval{}
	}
	if _, ok := a.pendingDirRemovals[path]; ok {
		return nil
	}
	a.pendingDirRemovals[path] = control.DirRemoval{Path: path, Author: author}
	// A directory has no pane to focus, so the gate raises immediately rather
	// than waiting for a focus change that will never come. A question already
	// on screen is never interrupted; the check runs again on the next
	// proposal.
	if !a.Prompt.Open {
		a.promptDirRemoval(control.DirRemoval{Path: path, Author: author})
	}
	return nil
}

// WithdrawDirRemoval removes the pending dir-removal for path when author
// proposed it, or when author is a human: a person may retract any pending
// removal, while an agent may retract only its own. A path that is not pending
// is a no-op.
func (a *App) WithdrawDirRemoval(path string, author uint8) error {
	d, ok := a.pendingDirRemovals[path]
	if !ok {
		return nil
	}
	if d.Author != author && !a.authorIsHuman(author) {
		return fmt.Errorf("pending dir-removal of %s was proposed by author %d, not this writer", path, d.Author)
	}
	delete(a.pendingDirRemovals, path)
	return nil
}

// ApproveDirRemoval carries out the pending dir-removal for path: the human
// answer to a proposal, the rmdir analogue of ApproveDeletion. The pending
// entry is matched by sameFile as the review does, and a path with no pending
// proposal is refused by name. removeDirDeleted is the one removal path, so the
// buffers under the tree close, the pending entry clears and the tree refreshes.
func (a *App) ApproveDirRemoval(path string) error {
	d, ok := a.pendingDirRemovalFor(path)
	if !ok {
		return fmt.Errorf("no pending dir-removal for %s", path)
	}
	a.removeDirDeleted(d)
	return nil
}

// DirRemovals lists the workspace's pending dir-removals in stable path order,
// so the wire and a reader see the same order every time.
func (a *App) DirRemovals() []control.DirRemoval {
	if len(a.pendingDirRemovals) == 0 {
		return nil
	}
	out := make([]control.DirRemoval, 0, len(a.pendingDirRemovals))
	for _, d := range a.pendingDirRemovals {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Proposals is the unified pending surface: one flat tagged list over every
// open buffer's pending change sets and invalid sets, the pending file
// deletions and the pending dir-removals. It is read-only and ungated; each
// kind can still be listed on its own. A change set's span is the min start
// and max end across its rebased hunks, or -1/-1 when a later edit has moved
// every member past, so a caller knows to ask `diff` for the text. An invalid
// set is its own kind because Pending drops it: no hunk survives, so listing it
// as a "set" would invite an accept that cannot work.
func (a *App) Proposals() []control.Proposal {
	var out []control.Proposal
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	for _, p := range panes {
		sess := p.File.Session()
		at := map[uint64]int{}
		for _, g := range sess.Pending() {
			at[g.ID] = len(out)
			out = append(out, control.Proposal{
				Kind: "set", Path: p.File.Path, Author: uint8(g.Author), Group: g.ID,
				Size: g.Bytes, Start: -1, End: -1,
			})
		}
		for _, d := range sess.DiffPending() {
			i, ok := at[d.Group.ID]
			if !ok {
				continue
			}
			for _, hk := range d.Hunks {
				if out[i].Start < 0 || hk.Start < out[i].Start {
					out[i].Start = hk.Start
				}
				if hk.End > out[i].End {
					out[i].End = hk.End
				}
			}
		}
		// Pending drops an invalid set by design: no hunk survives, so there is
		// nothing to accept, and a driver asking "what is waiting" must still
		// see it rather than be told the file is clean while a save would
		// discard text. It rides the rollup under its own kind, so a filter for
		// kind "set" stays exactly the sets a decision can reach.
		for _, g := range sess.Groups() {
			if g.State != piecetable.Proposed || !g.Invalid {
				continue
			}
			out = append(out, control.Proposal{
				Kind: "invalid", Path: p.File.Path, Author: uint8(g.Author), Group: g.ID,
				Size: g.Bytes, Start: -1, End: -1,
			})
		}
	}
	for _, d := range a.Deletions() {
		out = append(out, control.Proposal{Kind: "delete", Path: d.Path, Author: d.Author, Size: fileBytes(d.Path), Start: -1, End: -1})
	}
	for _, d := range a.DirRemovals() {
		out = append(out, control.Proposal{Kind: "rmdir", Path: d.Path, Author: d.Author, Size: dirFileCount(d.Path), Start: -1, End: -1})
	}
	for name, pub := range a.pendingPublishes {
		out = append(out, control.Proposal{Kind: "publish", Path: name, Author: pub.Author, Start: -1, End: -1})
	}
	return out
}

// fileBytes is a file size on disk, or 0 when it cannot be read: the waiting
// list still lists a proposal whose file has gone rather than dropping it for a
// stat error.
func fileBytes(path string) int {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return 0
	}
	return int(fi.Size())
}

// dirFileCount is how many regular files a folder removal would take. A walk
// error skips the unreadable entry rather than failing the whole count.
func dirFileCount(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// ProposeDirRemoval, WithdrawDirRemoval and DirRemovals bridge the BufferHost
// to the app's workspace-level pending-dir-removal set, exactly as the
// deletion triple bridges pendingDeletions.
func (h host) ProposeDirRemoval(path string, author uint8) error {
	return h.a.ProposeDirRemoval(path, author)
}

func (h host) WithdrawDirRemoval(path string, author uint8) error {
	return h.a.WithdrawDirRemoval(path, author)
}

func (h host) ApproveDirRemoval(path string) error { return h.a.ApproveDirRemoval(path) }

func (h host) DirRemovals() []control.DirRemoval { return h.a.DirRemovals() }

func (h host) Proposals() []control.Proposal { return h.a.Proposals() }

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
func (h host) Read(path string, author uint8, start, end, lineStart, lineEnd int, annotated bool) ([]control.Span, []control.StateRun, uint64, error) {
	p, err := h.findOrLoad(path)
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

// Find searches one buffer and returns the first match's byte span together
// with the total match count. The matching is the editor's own: the same search
// engine the search pane and the workspace walk use, reached by rooting the
// walk at the one document, so a find cannot disagree with a search over the
// same text and there is no second matcher to drift. It is a read, so an
// unopened file is loaded headlessly and a buffer's unsaved text is what is
// searched, not the file on disk.
func (h host) Find(path string, author uint8, q control.SearchQuery) (control.SearchMatch, int, bool, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return control.SearchMatch{}, 0, false, err
	}
	name := p.File.Path
	version := uint64(p.File.Session().Version())
	out := control.SearchMatch{Path: name, Version: version}
	// The search engine walks a root and overlays open documents. Handing it
	// the file itself as the root, with the buffer's text as the one open
	// document, bounds the walk to exactly this file: a path that exists on
	// disk is visited once, and a buffer that has never been written has no
	// walk to do and is served from the snapshot. Either way the engine does
	// the matching and this host never re-implements it.
	res := search.RunDocs(context.Background(), name, search.Query{
		Text: q.Text, Regex: q.Regex, Case: q.Case, Word: q.Word,
	}, search.Docs{name: p.File.Text()})
	if res.Err != nil {
		return out, 0, false, res.Err
	}
	count := res.Total()
	if len(res.Matches) == 0 {
		return out, count, false, nil
	}
	m := res.Matches[0]
	out.Line, out.Col, out.Len = m.Line, m.Col, m.Len
	out.LineStart, out.LineEnd = m.LineStart, m.LineEnd
	out.ByteStart, out.ByteEnd = m.ByteStart, m.ByteEnd
	out.Text = m.Text
	return out, count, true, nil
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

func (h host) Version(path string, author uint8) (uint64, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return 0, err
	}
	return uint64(p.File.Session().Version()), nil
}

// Apply is the write surface, and the reason offsets rather than text matching:
// ApplyDiff rebases hunks written against base onto whatever the document is
// now, and rejects the ones it cannot place. Staleness is handled by replaying
// the journal, so there is no old_str to get wrong. A hunk that lands over
// another writer's Proposed span is allowed -- the advisory lease -- and the
// warning names the set it moved past; a Rejected span is still a conflict.
func (h host) Apply(path string, author uint8, base uint64, hunks []control.Hunk, task string) (uint64, []control.Conflict, []control.GroupOverlap, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, nil, nil, err
	}
	// A proposal the user has to decide about must be visible: a headless
	// buffer is announced before the change set lands, never left hidden. The
	// reveal is quiet, so the tab appears without moving the user.
	h.a.announceIfHeadlessQuiet(p)
	// A hunk's offsets are in the coordinates of base, not of the document as
	// it stands now, so the bounds check has to measure against the length the
	// base had. A base past the journal has no such length -- the session never
	// produced it -- so fall back to the current text and let ApplyDiff report
	// the conflicts instead of refusing a version it might still place.
	size := len(p.File.Text())
	if n, ok := p.File.Session().LengthAt(piecetable.Version(base)); ok {
		size = n
	}
	pt := make([]piecetable.Hunk, 0, len(hunks))
	for i, x := range hunks {
		if _, _, serr := resolveSpan(x.Start, x.End, size); serr != nil {
			return 0, nil, nil, fmt.Errorf("hunk %d: %w", i, serr)
		}
		pt = append(pt, piecetable.Hunk{Start: x.Start, End: x.End, Text: x.Text})
	}
	// The connection's own author id, not a blanket Agent: the tint and the
	// undo stacks are per-author, so two connected agents stay distinguishable
	// from each other as well as from the user, and cmd+z swallows neither.
	// Begin/End makes one diff one undo entry.
	before := p.File.Session().Version()
	p.File.Begin()
	conflicts, blocks := p.File.ApplyDiff(piecetable.Author(author), piecetable.Version(base), pt)
	p.File.End()
	// A set this call opened carries the connection's task, so a manifest built
	// later keys it by the work that produced it; a no-task connection records
	// nothing and reads empty.
	//
	// No-op hunks are skipped by ApplyDiff, so a batch of only no-ops commits
	// nothing and LastGroup would name whatever set came before. Only record
	// or mark a set this call actually opened: the version advances exactly
	// when at least one real hunk committed.
	if p.File.Session().Version() > before {
		p.File.Session().SetGroupTask(p.File.Session().LastGroup(), task)
		// An agent's change set is a proposal, not an edit: it is in the
		// document and visible, but marked as awaiting a decision. Marked here
		// rather than in the piece table because only this layer knows which
		// authors are agents — and a second human's edits must not be marked.
		if h.isAgent(author) {
			p.File.ProposeGroup(p.File.Session().LastGroup())
		}
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
			// Author and the span ride with the lease so a driver learns who
			// holds the text and where without a second groups call.
			Author: uint8(c.Author),
			Start:  c.Start,
			End:    c.End,
			Hunk:   control.Hunk{Start: c.Hunk.Start, End: c.Hunk.End, Text: c.Hunk.Text},
		})
	}
	var warnings []control.GroupOverlap
	for _, b := range blocks {
		warnings = append(warnings, control.GroupOverlap{
			Group: b.Group, Author: uint8(b.Author), Start: b.Start, End: b.End})
	}
	return uint64(p.File.Session().Version()), out, warnings, nil
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
func (h host) Patch(path string, author uint8, id uint64, newText string, task string) (uint64, []control.Conflict, []control.GroupOverlap, error) {
	snap, ok := h.a.snapshots[id]
	if !ok || snap.author != author {
		return 0, nil, nil, fmt.Errorf("no snapshot %d for this writer (snapshots are per-writer and evicted on restart)", id)
	}
	p, err := h.find(path)
	if err != nil {
		return 0, nil, nil, err
	}
	// A patch creates a proposal too, so it is announced by the same rule as
	// apply: work the user has to decide about is never hidden. Quiet, for the
	// same reason: the tab appears without moving the user.
	h.a.announceIfHeadlessQuiet(p)
	if snap.path != p.File.Path {
		return 0, nil, nil, fmt.Errorf("snapshot %d is of %s, not %s", id, snap.path, p.File.Path)
	}
	diffs := control.DiffLines(snap.text, newText)
	if len(diffs) == 0 {
		// The caller returned the text unchanged; there is no change set to
		// record, and no group to mark.
		return uint64(p.File.Session().Version()), nil, nil, nil
	}
	pt := make([]piecetable.Hunk, 0, len(diffs))
	for _, d := range diffs {
		pt = append(pt, piecetable.Hunk{Start: snap.start + d.Start, End: snap.start + d.End, Text: d.Text})
	}
	before := p.File.Session().Version()
	p.File.Begin()
	conflicts, blocks := p.File.ApplyDiff(piecetable.Author(author), piecetable.Version(snap.version), pt)
	p.File.End()
	// Like apply: a patch whose every hunk was refused commits nothing, so
	// LastGroup would name whatever set came before. Only record or mark a set
	// this call actually opened, and record the task it belongs to.
	if p.File.Session().Version() > before {
		p.File.Session().SetGroupTask(p.File.Session().LastGroup(), task)
		if h.isAgent(author) {
			p.File.ProposeGroup(p.File.Session().LastGroup())
		}
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
			// Author and the span ride with the lease so a driver learns who
			// holds the text and where without a second groups call.
			Author: uint8(c.Author),
			Start:  c.Start,
			End:    c.End,
			Hunk:   control.Hunk{Start: c.Hunk.Start, End: c.Hunk.End, Text: c.Hunk.Text},
		})
	}
	var warnings []control.GroupOverlap
	for _, b := range blocks {
		warnings = append(warnings, control.GroupOverlap{
			Group: b.Group, Author: uint8(b.Author), Start: b.Start, End: b.End})
	}
	return uint64(p.File.Session().Version()), out, warnings, nil
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
	// A recovered buffer can be headless and dirty, and an exec reads disk
	// rather than the buffer, so its disk bytes are stale for that path too.
	panes := append(append([]*editor.Pane{}, h.a.Tabs.All()...), h.a.headless...)
	for _, p := range panes {
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
//
// The version is recorded for every open tab, not only the dirty ones: a hit
// in a clean buffer still names the revision it was found in, so a caller can
// tell the buffer moved between the search and a later read.
func (h host) Snapshot() control.Searcher {
	docs := search.Docs{}
	versions := search.DocVersions{}
	// A recovered buffer can be headless and dirty; search must see its
	// unsaved text for the same reason it sees a tab's, or it returns disk
	// text that is no longer what the editor holds.
	panes := append(append([]*editor.Pane{}, h.a.Tabs.All()...), h.a.headless...)
	for _, p := range panes {
		if p.File.Path == "" {
			continue
		}
		versions[p.File.Path] = uint64(p.File.Session().Version())
		if p.File.ViewDirty() {
			docs[p.File.Path] = p.File.Text()
		}
	}
	return fslist.SnapshotSearcher{Roots: h.a.visible.All(), Docs: docs, Versions: versions}
}

// Ls lists the immediate children of a directory. It resolves a path to the
// workspace's canonical coordinates and leaves the listing itself to
// fslist.List, which carries no App state. With no path it lists the workspace
// roots when there are several, or the primary root's children when there is
// one.
func (h host) Ls(path string, all bool) ([]control.Entry, error) {
	if path == "" {
		// No path names the workspace itself. One root means its immediate
		// children, exactly as before; several means one top-level entry per
		// root, in supplied order, so every root is visible and none is
		// mistaken for the primary's contents.
		if h.a.visible.Len() > 1 {
			return fslist.RootEntries(h.a.visible.All()), nil
		}
		path = h.a.visible.Primary()
	}
	dir := h.canonicalPath(path)
	return fslist.List(dir, all, h.a.rootFor(dir), h.a.visible.All())
}

// Hooks lists the workspace's stored hooks, converted from the store rows and
// sorted by name (the store's own order). A nil store has none, so the answer
// is an empty non-nil slice rather than an error.
func (h host) Hooks() ([]control.HookRow, error) {
	if h.a.state == nil {
		return []control.HookRow{}, nil
	}
	stored, err := h.a.state.Hooks()
	if err != nil {
		return nil, err
	}
	out := make([]control.HookRow, 0, len(stored))
	for _, s := range stored {
		out = append(out, storeHookRow(s))
	}
	return out, nil
}

// PutHook validates row with the hooks domain and stores it. The validation is
// repeated here, not only in Dispatch, so a direct in-process caller cannot
// store a row the domain would refuse.
func (h host) PutHook(row control.HookRow) error {
	if h.a.state == nil {
		return errors.New("hooks: no workspace store to write to")
	}
	// Every field crosses, Tree and Detach included: a row that loses them here
	// is stored as a projected, attached hook whatever the author asked for.
	if _, err := hooks.Parse(hooks.Raw{
		Name: row.Name, Action: row.Action, Params: row.Params, Trigger: row.Trigger, Tree: row.Tree,
		Agent: row.Agent, CooldownMS: row.CooldownMS, TimeoutMS: row.TimeoutMS,
		MayWrite: row.MayWrite, Detach: row.Detach, Enabled: row.Enabled,
	}); err != nil {
		return err
	}
	return h.a.state.PutHook(store.Hook{
		Name: row.Name, Action: row.Action, Params: row.Params, Trigger: row.Trigger, Tree: row.Tree,
		Agent: row.Agent, CooldownMS: row.CooldownMS, TimeoutMS: row.TimeoutMS,
		MayWrite: row.MayWrite, Detach: row.Detach, Enabled: row.Enabled,
	})
}

// DeleteHook removes the named hook. Deleting an absent name is not an error.
func (h host) DeleteHook(name string) error {
	if h.a.state == nil {
		return errors.New("hooks: no workspace store to write to")
	}
	return h.a.state.DeleteHook(name)
}

// SetHookEnabled flips the named hook's enabled flag, refusing a name that is
// not stored rather than creating a disabled stub.
func (h host) SetHookEnabled(name string, enabled bool) error {
	if h.a.state == nil {
		return errors.New("hooks: no workspace store to write to")
	}
	row, ok, err := h.a.state.Hook(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no such hook %q", name)
	}
	row.Enabled = enabled
	return h.a.state.PutHook(row)
}

// storeHookRow converts one store row to the control surface's shape.
func storeHookRow(s store.Hook) control.HookRow {
	return control.HookRow{
		Name: s.Name, Action: s.Action, Params: s.Params, Trigger: s.Trigger, Tree: s.Tree,
		Agent: s.Agent, CooldownMS: s.CooldownMS, TimeoutMS: s.TimeoutMS,
		MayWrite: s.MayWrite, Detach: s.Detach, Enabled: s.Enabled,
	}
}

// mailStore adapts the workspace store's mail table to the control mailbox's
// durable hook. It lives here, in the app, because neither package may import
// the other: the control layer owns the message shape and the store owns the
// row.
type mailStore struct{ s *store.Store }

// InsertMail stores one message for a durable identity.
func (m mailStore) InsertMail(toIdentity string, from uint8, fromKey, fromName, text string, createdMS int64) (int64, error) {
	return m.s.InsertMail(store.Mail{
		ToIdentity: toIdentity,
		FromAuthor: int(from),
		FromKey:    fromKey,
		FromName:   fromName,
		Text:       text,
		CreatedMS:  createdMS,
	})
}

// MarkMailDelivered confirms the rows a recipient's next park handed over.
func (m mailStore) MarkMailDelivered(ids []int64) error {
	return m.s.MarkMailDelivered(ids)
}

// LoadUndeliveredMail reads one identity's unread mail back.
func (m mailStore) LoadUndeliveredMail(toIdentity string) ([]control.StoredMessage, error) {
	rows, err := m.s.LoadUndeliveredMail(toIdentity)
	if err != nil {
		return nil, err
	}
	out := make([]control.StoredMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, control.StoredMessage{
			ID: r.ID, From: uint8(r.FromAuthor),
			FromKey: r.FromKey, FromName: r.FromName, Text: r.Text,
		})
	}
	return out, nil
}

// PruneDeliveredMail drops rows a recipient already confirmed.
func (m mailStore) PruneDeliveredMail() error {
	return m.s.PruneDeliveredMail()
}

// Groups lists a buffer's change sets.
func (h host) Groups(path string) ([]control.Group, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	// One projection of the pending sets answers "how many hunks, how many
	// moved", so a caller does not need a second `diff` call just for the
	// counts. A set already decided is not in DiffPending, so project its own
	// members the same way; its counts would otherwise read zero and say the
	// set contributed nothing when it did.
	sess := p.File.Session()
	counts := map[uint64][2]int{}
	for _, d := range sess.DiffPending() {
		counts[d.Group.ID] = [2]int{len(d.Hunks), d.Moved}
	}
	overlaps := sess.PendingOverlaps()
	var out []control.Group
	for _, g := range sess.Groups() {
		// A set every live member of which has been reversed out is not a
		// change any more. Its op records stay in the journal -- the manifest
		// audits them -- but the listing a driver decides from must not carry
		// the tombstone, or a buffer accumulates the `0 ops, +0 bytes, 0
		// hunks` sets a clear left behind.
		if g.Ops == 0 {
			continue
		}
		c, ok := counts[g.ID]
		if !ok {
			// Accepted or rejected: not pending, but its members are still in
			// the journal, so the same projection applies.
			if d, found := sess.GroupDiff(g.ID); found {
				c = [2]int{len(d.Hunks), d.Moved}
			}
		}
		cg := control.Group{
			ID: g.ID, Path: p.File.Path, Author: uint8(g.Author),
			State: g.State.String(), Task: g.Task, Ops: g.Ops, Bytes: g.Bytes,
			First: uint64(g.First), Last: uint64(g.Last),
			Hunks: c[0], Moved: c[1],
			Invalid: g.Invalid, InvalidBy: groupCollider(g.InvalidBy),
		}
		if ov, ok := overlaps[g.ID]; ok {
			cg.Overlaps = groupOverlaps(ov)
		}
		out = append(out, cg)
	}
	return out, nil
}

// Diff hands the pending change sets back as old→new hunks. The session owns
// the rendering: the spans come from the same rebase walk that applies and
// reverses edits, so what a reviewer sees is where the change sits now, not
// where it was written.
func (h host) Diff(path string, author uint8) ([]control.DiffGroup, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	sess := p.File.Session()
	overlaps := sess.PendingOverlaps()
	var out []control.DiffGroup
	for _, g := range sess.DiffPending() {
		dg := control.DiffGroup{
			Group: control.Group{
				ID: g.Group.ID, Path: p.File.Path, Author: uint8(g.Group.Author),
				State: g.Group.State.String(), Ops: g.Group.Ops, Bytes: g.Group.Bytes,
				First: uint64(g.Group.First), Last: uint64(g.Group.Last),
				Invalid: g.Group.Invalid, InvalidBy: groupCollider(g.Group.InvalidBy),
			},
			Moved: g.Moved,
		}
		if ov, ok := overlaps[g.Group.ID]; ok {
			dg.Overlaps = groupOverlaps(ov)
		}
		for _, hk := range g.Hunks {
			line, endLine := diffLines(p.File, hk.Start, hk.End)
			dg.Hunks = append(dg.Hunks, control.DiffHunk{
				Start: hk.Start, End: hk.End, Line: line, EndLine: endLine,
				Old: hk.Old, New: hk.New})
		}
		for _, hk := range g.MovedHunks {
			dg.MovedHunks = append(dg.MovedHunks, control.DiffHunk{
				Start: -1, End: -1, Old: hk.Old, New: hk.New})
		}
		out = append(out, dg)
	}
	return out, nil
}

// groupOverlaps converts the session's overlap report into the wire shape. It
// returns nil for no overlaps, so `groups -json` omits the field entirely
// rather than carrying an empty list.
func groupOverlaps(ov []piecetable.Overlap) *control.GroupOverlaps {
	if len(ov) == 0 {
		return nil
	}
	sets := make([]control.GroupOverlap, 0, len(ov))
	for _, o := range ov {
		sets = append(sets, control.GroupOverlap{
			Group: o.Group, Author: uint8(o.Author), Start: o.Start, End: o.End,
		})
	}
	return &control.GroupOverlaps{Sets: sets}
}

// groupCollider converts one invalid set's collider report into the wire shape,
// or nil when the set has none. It is the single-set sibling of groupOverlaps.
func groupCollider(ov *piecetable.Overlap) *control.GroupOverlap {
	if ov == nil {
		return nil
	}
	return &control.GroupOverlap{
		Group: ov.Group, Author: uint8(ov.Author), Start: ov.Start, End: ov.End,
	}
}

// diffLines converts a hunk's byte span into 1-based line coordinates using
// the buffer's current line map, so a reviewer can name the hunk without a
// second read. A zero-width hunk sits on one line; otherwise the span covers
// up to the byte before End, the last line the replaced text held.
func diffLines(f *editor.File, start, end int) (line, endLine int) {
	line = f.LineOf(start) + 1
	e := end
	if e > start {
		e = end - 1
	}
	endLine = f.LineOf(e) + 1
	if endLine < line {
		endLine = line
	}
	return line, endLine
}

// Review returns a buffer's pending change sets and, unless listOnly, enters
// Review mode at the first one. It is the socket form of the cmd+r toggle and
// the proposals picker: the list is what `groups` shows filtered to the sets
// still awaiting a decision, and EnterReview is the same enter path the chord
// takes, so the two surfaces cannot drift. EnterReview acts on the active tab,
// so entering focuses the buffer the path names first: a `review [path]` for a
// background file would otherwise list that file's sets while the mode badge
// and the jump belonged to whatever was in front. A headless buffer is
// announced first, so there is a tab to focus.
func (h host) Review(path string, listOnly bool) ([]control.Group, error) {
	p, err := h.find(path)
	if err != nil {
		return nil, err
	}
	var out []control.Group
	for _, g := range p.File.Session().Pending() {
		out = append(out, control.Group{
			ID: g.ID, Path: p.File.Path, Author: uint8(g.Author),
			State: g.State.String(), Task: g.Task, Ops: g.Ops, Bytes: g.Bytes,
			First: uint64(g.First), Last: uint64(g.Last),
		})
	}
	if !listOnly {
		h.a.announceIfHeadless(p)
		h.a.Tabs.Focus(p)
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

// disposeInvalid drops every still-Proposed invalid change set in p and reports
// how many it disposed. An invalid set has no surviving hunk: accept and reject
// address nothing, and Pending has already let it go, so ClearGroup is the only
// gesture that reaches it, and for an invalid Proposed set that is a state-only
// reject. The bytes a save writes are unchanged either way -- an invalid run is
// in neither the agreed composition nor, once cleared, the edit composition --
// which is why a save counts the drop rather than refusing over it. A snapshot
// holds no decisions to move and is left alone.
func (a *App) disposeInvalid(p *editor.Pane) int {
	if p == nil || p.File.IsSnapshot() || !p.File.Session().HasDecisions() {
		return 0
	}
	n := 0
	// Groups() returns a copy, so the walk is stable while ClearGroup moves the
	// session's decisions.
	for _, g := range p.File.Session().Groups() {
		if g.State != piecetable.Proposed || !g.Invalid {
			continue
		}
		if ok, _ := p.File.ClearGroup(g.ID); ok {
			n++
		}
	}
	return n
}

// Clear disposes of a change set in one gesture: a Rejected set is reversed
// out as before, and an Invalid (superseded) Proposed set is dropped by
// marking it rejected, because no live member of it can be reversed. It is the
// only disposal that reaches an invalid set -- Pending drops it, so accept and
// reject address nothing, and the old rejected-only path refused it outright --
// which is the wedge this closes. It returns false for a set that is neither a
// rejected set nor an invalid one, and the refusal names the group because that
// is how the caller addressed it.
func (h host) Clear(path string, group uint64) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	defer h.a.flushJournal(p)
	ok, block := p.File.ClearGroup(group)
	if !ok {
		if block.Group != 0 {
			// The reversal wedged behind a live set. Name it in the same
			// owner-and-span shape a lease refusal uses, so the caller sees
			// what to reject or clear first instead of retrying blind.
			return &control.BlockError{
				Conflict: control.Conflict{
					Group:  block.Group,
					Author: uint8(block.Author),
					Start:  block.Start,
					End:    block.End,
				},
				Message: fmt.Sprintf("change set %d cannot be cleared: change set %d "+
					"(author %d, bytes %d..%d) overlaps it; reject or clear that set first",
					group, block.Group, block.Author, block.Start, block.End),
			}
		}
		return fmt.Errorf("change set %d cannot be cleared "+
			"(proposed sets with surviving text are decided with accept or reject; "+
			"an accepted set, or one already reversed, has nothing to clear)", group)
	}
	p.Cursors.Normalize()
	h.a.Explorer.Tree.MarkChanged(p.File.Path)
	return nil
}

// Revert discards the calling writer's own pieces: RevertAuthor reverses every
// op they wrote out of the document and records the reversal in the journal,
// so the record stays consistent rather than holding a second forward edit
// beside the work it undoes. It is claim-gated like Clear and, like Clear, can
// wedge behind a later live set, which it names in the same block shape.
//
// The authorization is the caller's own author and nothing else: no field on
// the wire names a peer, so a driver cannot reverse another writer's accepted
// text. Dropping a peer's work is the user's decision, taken through reject and
// clear.
func (h host) Revert(path string, author uint8) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	defer h.a.flushJournal(p)
	ok, block := p.File.RevertAuthor(piecetable.Author(author))
	if !ok {
		if block.Group != 0 {
			// The reversal wedged behind a live set, but sets earlier in the
			// walk were already dropped, so the document moved. Mirror that
			// before naming the blocker in the same owner-and-span shape a
			// lease refusal uses, so the caller sees what to clear first
			// instead of retrying blind.
			p.Cursors.Normalize()
			h.a.Explorer.Tree.MarkChanged(p.File.Path)
			return &control.BlockError{
				Conflict: control.Conflict{
					Group:  block.Group,
					Author: uint8(block.Author),
					Start:  block.Start,
					End:    block.End,
				},
				Message: fmt.Sprintf("author %d's pieces cannot be reverted: change set %d "+
					"(author %d, bytes %d..%d) overlaps them; reject or clear that set first",
					author, block.Group, block.Author, block.Start, block.End),
			}
		}
		return fmt.Errorf("author %d has no live pieces in %s to revert "+
			"(they may be gone, or a later edit has wedged them)", author, path)
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
// The refusal is about the buffer's state, not the caller. The save verb is
// the user's: the Guard's identity gate refuses an agent before this runs, so
// only the local keyboard or a joined human reaches here. What this method
// decides is whether the agreed composition can be written at all — a buffer
// with proposals pending waits for the user, whoever is asking.
//
// A save from here announces itself the same way App.write's does: connected
// drivers hear via App.notifySaved, and the live language server hears via
// App.lspSaved. It still does not share the rest of App.write's bookkeeping —
// the journal and AcceptPending have no meaning for a buffer whose proposed
// changes were refused above.
// saveRefusal converts an editor save refusal into the one wording both save
// paths use. A save the editor refused because the agreed composition would
// have dropped a superseded set gets an actionable message naming the set and
// the two gestures that resolve it, and the set ids ride along so a caller can
// route the decision. Any other error -- disk changed, an unencodable
// character, a composition the save_check guard refused -- is the editor's
// own, returned unchanged.
func saveRefusal(err error) (string, []uint64) {
	var refused *editor.UnsavedProposedError
	var check *editor.SaveCheckError
	switch {
	case errors.As(err, &refused):
	case errors.As(err, &check):
		// The composition guard's reason is the whole message; prefixing it
		// with "save failed" would say the same thing twice.
		return check.Error(), nil
	default:
		return "save failed: " + err.Error(), nil
	}
	msg := refused.Error() + "; save with proposed changes to accept them, or `clear` them to discard"
	ids := make([]uint64, len(refused.Groups))
	for i, g := range refused.Groups {
		ids[i] = g.ID
	}
	return msg, ids
}

// Reload takes the version on disk for a buffer, matching the editor Reload
// gesture. The socket has no human to ask, so it never prompts: a dirty buffer
// is reloaded and its unsaved changes are discarded, and the status line says
// so.
func (h host) Reload(path string) error {
	p, err := h.find(path)
	if err != nil {
		return err
	}
	dirty := p.File.ViewDirty()
	if err := h.a.reload(p, nil); err != nil {
		return err
	}
	if dirty {
		h.a.status = "reloaded " + p.File.Name() + " from disk (unsaved changes discarded)"
	}
	return nil
}

func (h host) Save(path string, force bool) (uint64, error) {
	p, err := h.find(path)
	if err != nil {
		return 0, err
	}
	if pending := p.File.Session().Pending(); len(pending) > 0 {
		return 0, fmt.Errorf("%d proposed change set(s) await the user's approval; "+
			"the edit is in the buffer and will reach disk when they save", len(pending))
	}
	// A save disposes the invalid sets it would otherwise drop, before the
	// write, and the count rides the status line below, so the drop is
	// deliberate rather than silent. An invalid set is not Pending, so no
	// review offered it; this is the gesture that reaches it.
	discarded := h.a.disposeInvalid(p)
	if discarded > 0 {
		h.a.flushJournal(p)
	}
	p.File.SaveCheck = h.a.settings.SaveCheck
	save := p.File.Save
	if force {
		// force is the prompt Overwrite: write over a file that changed on
		// disk. A snapshot still refuses, because its bytes belong to the
		// daemon, not to this disk.
		if p.File.IsSnapshot() {
			return 0, editor.ErrSnapshotReadOnly
		}
		save = p.File.SaveOver
	}
	if err := save(); err != nil {
		// A composition or encoding refusal is the guard refusal: the pane is
		// protected from a close even when its view matches disk, because the
		// accepted composition a later save would write is still the one this
		// refusal named. A disk conflict is not marked: the caller answers a
		// question about it, and a clean buffer loses nothing either way.
		if !errors.Is(err, editor.ErrDiskChanged) {
			p.MarkSaveRefused()
		}
		// A save that still refused over a set the disposal could not reach is
		// surfaced in the same actionable wording the user's own save shows;
		// anything else -- disk changed, an unencodable character -- is the
		// editor's own and is returned unchanged.
		if msg, _ := saveRefusal(err); msg != "save failed: "+err.Error() {
			return 0, errors.New(msg)
		}
		return 0, err
	}
	// The bytes reached disk; both announcements are best-effort, like App.write's.
	p.ClearSaveRefused()
	h.a.notifySaved(p.File.Path)
	h.a.lspSaved(p)
	if discarded > 0 {
		h.a.status = fmt.Sprintf("saved %s (discarded %d invalid change set(s))", p.File.Name(), discarded)
	}
	return uint64(p.File.Session().Version()), nil
}

// LSP prepares a blocking language-server request for a 1-based line and
// column. Every mode syncs the document and captures what the request needs so
// it runs off the event thread; diagnostics wears the one difference — it
// returns the published state and never waits here — but it still registers the
// queried buffer, because a server cannot publish for a document it has never
// been given. A nil caller with a non-nil error is a clean "no server" or "not
// ready" answer the driver can retry.
func (h host) LSP(path string, line, col int, mode string) (control.LSPCaller, error) {
	switch mode {
	case "hover", "definition", "declaration", "type-definition", "implementation",
		"references", "completion", "signature", "diagnostics", "document-symbols":
	default:
		return nil, fmt.Errorf("unknown lsp mode %q (want hover, definition, declaration, type-definition, implementation, references, completion, signature, diagnostics or document-symbols)", mode)
	}
	p, err := h.findOrLoad(path)
	if err != nil {
		return nil, err
	}
	lpath := h.a.docPath(p)
	if lpath == "" {
		return nil, fmt.Errorf("no path for this buffer")
	}
	if mode == "diagnostics" {
		// Diagnostics starts a server when none is running, so a first ask is
		// answered by a start rather than by "not started" forever. for_ returns
		// nil with a reason while it comes up — starting, missing, none or gave
		// up — and the caller retries; only a live server comes back non-nil,
		// and then the sync and bounded wait below apply. A diagnostic set is
		// what the server last sent, so this mode never blocks here on the
		// answer. The queried buffer is still registered with the live server
		// before the cache is read, so a control-path document the server has
		// never seen can be published for; that is a notification write, not a
		// request the event thread waits on.
		ls, st := h.a.servers.for_(lpath, func() { h.a.host.Post(ui.Wake{}) })
		if ls == nil {
			return lspCaller{mode: "diagnostics", status: lspStatus(st), detail: st.message(lpath)}, nil
		}
		// The published latch is not a freshness proof, and a versionless
		// publish cannot be dated by version at all: gopls omits the version
		// for a file at version 0, which is both the on-disk copy and an
		// unedited buffer. syncDoc notes the sync in the store, so status can
		// reject a versionless publish that predates the current text rather
		// than read it as clean.

		// for_ returns a server only alongside a live sync, so this branch is
		// unreachable today; guarding it keeps a nil dereference off the event
		// thread if that invariant ever changes.
		if ls.sync == nil {
			return lspCaller{
				mode:   "diagnostics",
				status: control.LSPStatusStarting,
				detail: "the language server is not ready",
			}, nil
		}
		// Register the queried buffer with the live server before reading the
		// publish: the reading the caller wants is for the text in front of it,
		// and a document the server has not opened can never be published for.
		// The wait for the server's answer happens off the event thread in Run.
		if !h.a.syncDoc(ls, p) {
			return nil, fmt.Errorf("could not synchronise the document with the server")
		}
		bufVersion := int(p.File.Session().Version())
		synced, _ := ls.sync.Version(lpath)
		status, detail := h.a.diags.status(lpath, synced, bufVersion)
		return lspCaller{
			mode:   "diagnostics",
			status: status,
			detail: detail,
			diags:  h.a.diags.forPath(lpath),
			store:  h.a.diags,
			path:   lpath,
			buf:    bufVersion,
		}, nil
	}
	// document-symbols is asked about the document, not a position, like
	// diagnostics; the server's whole-file outline has no caret.
	if mode != "document-symbols" && (line < 1 || col < 1) {
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
	switch mode {
	case "references":
		if msg := capabilityGap(ls.caps.Capabilities.ReferencesProvider, "references"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	case "declaration":
		if msg := capabilityGap(ls.caps.Capabilities.DeclarationProvider, "declaration"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	case "type-definition":
		if msg := capabilityGap(ls.caps.Capabilities.TypeDefinitionProvider, "type definition"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	case "implementation":
		if msg := capabilityGap(ls.caps.Capabilities.ImplementationProvider, "implementation"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	case "signature":
		if msg := capabilityGap(ls.caps.Capabilities.SignatureHelpProvider, "signature help"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	case "document-symbols":
		if msg := capabilityGap(ls.caps.Capabilities.DocumentSymbolProvider, "document symbols"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	}
	// A document-symbol request carries no position; the caller's path is the
	// whole subject, like a formatting request's.
	if mode == "document-symbols" {
		return lspCaller{mode: mode, conn: conn, path: lpath}, nil
	}
	pos := lsp.NewDocument(p.File.Text()).Position(off)
	return lspCaller{mode: mode, conn: conn, path: lpath, pos: pos}, nil
}

// LSPWorkspaceSymbols prepares a project-wide symbol query. It is the variant
// of host.LSP that takes a query instead of a position: the server's index is
// what matches names, so there is nothing to ask about a place. The path still
// names the document whose server should answer, and the capability gate is
// the human action's, in the same voice, so a server that never advertised
// workspaceSymbolProvider is told apart from one with nothing to say rather
// than asked a method it would answer method-not-found.
func (h host) LSPWorkspaceSymbols(path, query string) (control.LSPCaller, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return nil, err
	}
	lpath := h.a.docPath(p)
	if lpath == "" {
		return nil, fmt.Errorf("no path for this buffer")
	}
	ls, st := h.a.servers.for_(lpath, func() { h.a.host.Post(ui.Wake{}) })
	if ls == nil {
		if msg := st.message(lpath); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("language server not available")
	}
	if msg := ls.capabilityGapMethod(ls.caps.Capabilities.WorkspaceSymbolProvider, "workspace/symbol", "workspace symbols"); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}
	if !h.a.syncDoc(ls, p) {
		return nil, fmt.Errorf("could not synchronise the document with the server")
	}
	conn := ls.srv.Conn()
	if conn == nil {
		return nil, fmt.Errorf("language server not ready")
	}
	return lspCaller{mode: "symbols", conn: conn, query: query}, nil
}

// LSPInlayHints prepares a range-scoped inlay-hint request, the range variant
// of host.LSP. It follows the same shape — find or load the buffer, sync it so
// the server sees unsaved text, locate a live server — and returns a caller
// that runs the blocking request off the event thread.
//
// lineStart and lineEnd are 1-based inclusive lines, zero meaning the start or
// the end of the file. The server's positions come back in UTF-16, so the
// caller also carries a conversion to 1-based editor line and display column,
// pinned to a text snapshot taken here: the answer arrives on another
// goroutine, and the live buffer must not be read from there.
func (h host) LSPInlayHints(path string, lineStart, lineEnd int) (control.LSPCaller, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return nil, err
	}
	lpath := h.a.docPath(p)
	if lpath == "" {
		return nil, fmt.Errorf("no path for this buffer")
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

	lines := p.File.Lines()
	top, bottom := 0, lines
	if lineStart > 0 {
		top = lineStart - 1
	}
	if lineEnd > 0 {
		bottom = lineEnd
	}
	if top < 0 {
		top = 0
	}
	if top > lines {
		top = lines
	}
	if bottom > lines {
		bottom = lines
	}
	if bottom < top {
		bottom = top
	}
	rng := h.a.hintRange(p, top, bottom)

	// Snapshot the bytes and the column map now, so the conversion runs against
	// the text the range was measured on rather than a buffer the editor may
	// have moved past.
	text := p.File.Text()
	doc := lsp.NewDocument(text)
	cols := p.File.Cols
	toHint := func(hint lsp.InlayHint) control.LSPHint {
		off := doc.Offset(hint.Pos)
		line := doc.Position(off).Line
		start := doc.Offset(lsp.Position{Line: line, Character: 0})
		end := len(text)
		if line+1 < doc.Lines() {
			end = doc.Offset(lsp.Position{Line: line + 1, Character: 0}) - 1
		}
		if end < start {
			end = start
		}
		if off < start {
			off = start
		}
		if off > end {
			off = end
		}
		out := control.LSPHint{
			Line: line + 1, Col: cols.ColOf(text[start:end], off-start) + 1,
			Text: hint.Text, Kind: hint.Kind,
			PaddingLeft: hint.PaddingLeft, PaddingRight: hint.PaddingRight,
			Tooltip: hint.Tooltip,
		}
		for _, te := range hint.Edits {
			lo, hi := doc.Span(te.Range)
			out.Edits = append(out.Edits, control.LSPHintEdit{Start: lo, End: hi, Text: te.NewText})
		}
		return out
	}
	return lspCaller{mode: "inlay-hints", conn: conn, path: lpath, rng: rng, toHint: toHint}, nil
}

// LSPFormat prepares a whole-document or range formatting request. It is the
// formatting variant of host.LSP: the same find-or-load, capability gate and
// sync, with the buffer's own indent style built into the FormattingOptions on
// the event thread so the request goroutine never reads the live buffer.
//
// lineStart and lineEnd are 1-based inclusive lines, zero meaning the start or
// the end of the file; both zero asks about the whole document. The capability
// gate is per feature — the whole-document form reads documentFormattingProvider
// and the range form reads documentRangeFormattingProvider — so a server that
// advertises one and not the other is refused for the one it lacks rather than
// asked a method it would answer method-not-found.
func (h host) LSPFormat(path string, lineStart, lineEnd int) (control.LSPCaller, error) {
	p, err := h.findOrLoad(path)
	if err != nil {
		return nil, err
	}
	lpath := h.a.docPath(p)
	if lpath == "" {
		return nil, fmt.Errorf("no path for this buffer")
	}
	ls, st := h.a.servers.for_(lpath, func() { h.a.host.Post(ui.Wake{}) })
	if ls == nil {
		if msg := st.message(lpath); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("language server not available")
	}
	whole := lineStart <= 0 && lineEnd <= 0
	if whole {
		if msg := capabilityGap(ls.caps.Capabilities.DocumentFormattingProvider, "formatting"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	} else {
		if msg := capabilityGap(ls.caps.Capabilities.DocumentRangeFormattingProvider, "range formatting"); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	}
	if !h.a.syncDoc(ls, p) {
		return nil, fmt.Errorf("could not synchronise the document with the server")
	}
	conn := ls.srv.Conn()
	if conn == nil {
		return nil, fmt.Errorf("language server not ready")
	}
	opts := formatOptions(p.File.Indent)
	if whole {
		return lspCaller{mode: "format", conn: conn, path: lpath, opts: opts}, nil
	}
	lines := p.File.Lines()
	top, bottom := 0, lines
	if lineStart > 0 {
		top = lineStart - 1
	}
	if lineEnd > 0 {
		bottom = lineEnd
	}
	if top < 0 {
		top = 0
	}
	if top > lines {
		top = lines
	}
	if bottom > lines {
		bottom = lines
	}
	if bottom < top {
		bottom = top
	}
	// The same line-bounds-to-range conversion the inlay-hints request uses:
	// the range starts at the first selected line and ends at the start of the
	// line after the last, so a whole-line selection asks for whole lines.
	rng := h.a.hintRange(p, top, bottom)
	return lspCaller{mode: "range-format", conn: conn, path: lpath, opts: opts, rng: rng}, nil
}

// diagnosticsWait bounds how long the diagnostics caller waits for the publish
// that answers it. A server that has not spoken within this window gets the
// honest stale or unpublished status instead of holding the caller open; a
// publish already on its way normally lands well inside it.
const diagnosticsWait = 2 * time.Second

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
	// store, path and buf carry the diagnostics wait: the request registers the
	// buffer and returns the publish so far, and Run waits briefly for the next
	// one and judges it against the version that was registered. store is nil
	// when there is no live server to wait on.
	store *diagnostics
	buf   int
	// rng and toHint carry an inlay-hints answer: the range the request asked
	// about, and the conversion of one server hint into the 1-based editor
	// coordinates the driver reads. toHint is built on the event thread and
	// closes over a text snapshot, so Run never touches the live buffer.
	rng    lsp.Range
	toHint func(lsp.InlayHint) control.LSPHint
	// query is the workspace/symbol text for the symbols mode. It is the whole
	// request: the server matches it against its index, and an empty query asks
	// for everything the server has.
	query string
	// opts carries a formatting request's FormattingOptions, built on the event
	// thread from the buffer's indent style so the request goroutine never
	// reads the live buffer.
	opts lsp.FormattingOptions
}

func (c lspCaller) Run(ctx context.Context) ([]byte, error) {
	var out control.LSPResult
	switch c.mode {
	case "diagnostics":
		// A publish is asynchronous: the request registered the document just
		// now, and the server's reading may not have arrived by the time this
		// goroutine runs. Wait briefly for the next one rather than making the
		// caller poll, and only when the status says the answer is not a
		// reading of the current text.
		if c.status != control.LSPStatusOK && c.store != nil {
			if status, detail, items := c.store.reading(c.path, c.buf); status == control.LSPStatusOK {
				// A publish landed between the request and this goroutine; take
				// it instead of waiting for another.
				c.status, c.detail, c.diags = status, detail, items
			} else {
				ctx, cancel := context.WithTimeout(ctx, diagnosticsWait)
				woke := c.store.waitFor(ctx, c.path)
				cancel()
				if woke {
					c.status, c.detail, c.diags = c.store.reading(c.path, c.buf)
				} else if status, detail, items := c.store.reading(c.path, c.buf); status == control.LSPStatusOK {
					// A publish can land between the request's first reading
					// and waitFor's registration; take it rather than reporting
					// the older status after the wait.
					c.status, c.detail, c.diags = status, detail, items
				}
			}
		}
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
	case "definition", "declaration", "type-definition", "implementation", "references":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		locs, err := runLocationRequest(ctx, c.mode, c)
		if err != nil {
			return nil, err
		}
		out.Locations = lspLocations(locs)
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
	case "signature":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		help, err := lsp.RequestSignatureHelp(ctx, c.conn, c.path, c.pos)
		if err != nil {
			return nil, err
		}
		if help != nil {
			out.ActiveSignature = help.ActiveSignature
			out.ActiveParameter = help.ActiveParameter
			out.Signatures = make([]control.LSPSignature, 0, len(help.Signatures))
			for _, sig := range help.Signatures {
				cs := control.LSPSignature{
					Label:           sig.Label,
					Documentation:   sig.Documentation,
					ActiveParameter: sig.ActiveParameter,
				}
				for _, prm := range sig.Parameters {
					cs.Parameters = append(cs.Parameters, control.LSPParameter{
						Label: prm.Label, Start: prm.Start, End: prm.End, Offsets: prm.HasOffsets,
					})
				}
				out.Signatures = append(out.Signatures, cs)
			}
		}
		return json.Marshal(out)
	case "format", "range-format":
		// Formatting is a cold-server request like document symbols, so the
		// bound is the longer one. The FormattingOptions were built on the
		// event thread; the range form also carries the lines the driver named,
		// and the whole-document form leaves rng zero.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var edits []lsp.TextEdit
		var err error
		if c.mode == "range-format" {
			edits, err = lsp.RequestRangeFormatting(ctx, c.conn, c.path, c.rng, c.opts)
		} else {
			edits, err = lsp.RequestFormatting(ctx, c.conn, c.path, c.opts)
		}
		if err != nil {
			return nil, err
		}
		out.Edits = lspEdits(edits)
		return json.Marshal(out)
	case "inlay-hints":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		hints, err := lsp.RequestInlayHints(ctx, c.conn, c.path, c.rng)
		if err != nil {
			return nil, err
		}
		out.Hints = make([]control.LSPHint, 0, len(hints))
		for _, hint := range hints {
			out.Hints = append(out.Hints, c.toHint(hint))
		}
		return json.Marshal(out)
	case "document-symbols":
		// A file's whole outline can be large and a cold server may still be
		// parsing it, so the bound matches the workspace-symbol 5s rather than
		// the point-query 3s.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		syms, err := lsp.RequestDocumentSymbols(ctx, c.conn, c.path)
		if err != nil {
			return nil, err
		}
		out.DocumentSymbols = lspDocumentSymbols(syms)
		return json.Marshal(out)
	case "symbols":
		// A project-wide symbol query can outlast a hover: a cold gopls index
		// is the slow case, so the bound is the longer one.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		syms, err := lsp.RequestWorkspaceSymbols(ctx, c.conn, c.query)
		if err != nil {
			return nil, err
		}
		out.Symbols = lspWorkspaceSymbols(syms)
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("unknown lsp mode %q", c.mode)
}

// lspDocumentSymbols converts a document-symbol tree to the wire shape, with the
// server's 0-based positions made 1-based. The tree is preserved rather than
// flattened: a flat SymbolInformation reply is childless, and a driver that
// assumed a depth would misread one shape or the other. A symbol with no range
// keeps line 0, the same "no place" distinction LSPSymbol makes.
func lspDocumentSymbols(syms []lsp.DocumentSymbol) []control.LSPDocumentSymbol {
	out := make([]control.LSPDocumentSymbol, 0, len(syms))
	for _, s := range syms {
		cs := control.LSPDocumentSymbol{
			Name: s.Name, Detail: s.Detail, Kind: s.Kind.String(),
			Container: s.Container, Path: s.Path,
		}
		if s.HasRange {
			cs.Line = s.SelectionRange.Start.Line + 1
			cs.Col = s.SelectionRange.Start.Character + 1
		}
		cs.Children = lspDocumentSymbols(s.Children)
		out = append(out, cs)
	}
	return out
}

// runLocationRequest performs the blocking request for one of the location
// modes. Definition, declaration, type definition, implementation and
// references differ only in the method, so a new one is a case here rather
// than a fourth copy of the timeout-and-decode sequence.
func runLocationRequest(ctx context.Context, mode string, c lspCaller) ([]lsp.Location, error) {
	switch mode {
	case "definition":
		return lsp.RequestDefinition(ctx, c.conn, c.path, c.pos)
	case "declaration":
		return lsp.RequestDeclaration(ctx, c.conn, c.path, c.pos)
	case "type-definition":
		return lsp.RequestTypeDefinition(ctx, c.conn, c.path, c.pos)
	case "implementation":
		return lsp.RequestImplementation(ctx, c.conn, c.path, c.pos)
	case "references":
		return lsp.RequestReferences(ctx, c.conn, c.path, c.pos, true)
	}
	return nil, fmt.Errorf("unknown lsp mode %q", mode)
}

// lspEdits converts the lsp package's formatting edits to the wire shape, with
// the server's 0-based line and character made 1-based for the driver. End is
// exclusive, as the protocol's ranges are.
func lspEdits(edits []lsp.TextEdit) []control.LSPEdit {
	out := make([]control.LSPEdit, 0, len(edits))
	for _, e := range edits {
		out = append(out, control.LSPEdit{
			Line:    e.Range.Start.Line + 1,
			Col:     e.Range.Start.Character + 1,
			EndLine: e.Range.End.Line + 1,
			EndCol:  e.Range.End.Character + 1,
			Text:    e.NewText,
		})
	}
	return out
}

// lspLocations converts the lsp package's locations to the wire shape, with the
// server's 0-based line and character made 1-based for the driver.
func lspLocations(locs []lsp.Location) []control.LSPLocation {
	out := make([]control.LSPLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, control.LSPLocation{
			Path: l.Path, Line: l.Range.Start.Line + 1, Col: l.Range.Start.Character + 1,
		})
	}
	return out
}

// lspWorkspaceSymbols converts a workspace/symbol answer to the wire shape,
// with the server's 0-based positions made 1-based. A symbol the server named
// by file alone keeps line 0, the same "no place" distinction LSPSymbol makes.
func lspWorkspaceSymbols(syms []lsp.WorkspaceSymbol) []control.LSPSymbol {
	out := make([]control.LSPSymbol, 0, len(syms))
	for _, s := range syms {
		cs := control.LSPSymbol{
			Name: s.Name, Kind: s.Kind.String(),
			Container: s.Container, Path: s.Location.Path,
		}
		if s.HasRange {
			cs.Line = s.Location.Range.Start.Line + 1
			cs.Col = s.Location.Range.Start.Character + 1
		}
		out = append(out, cs)
	}
	return out
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
// It is pure so the rule can be tested without a live server. path names the
// document, for the unassociated detail. published is the store's latch,
// pubVersion the document version that publish applied to (nil when the server
// sent none), syncedVersion the version the server was last told about, and
// bufVersion the buffer's current version. items is the published list: a
// fresh list carrying gopls's "no package for this open file" diagnostic is
// not a reading of the text, because the server answered without associating
// the document with any package. A set is a real reading only when it was
// published for the text the buffer holds now: a publish that predates it, or
// a server that has not even been told about it, is stale rather than clean.
// A publish that carried no version cannot be dated here; freshnessLocked
// resolves that case first, so a stale versionless publish never reaches the
// unassociated rule.
func diagnosticsStatus(path string, published bool, pubVersion *int, syncedVersion, bufVersion int, items []lsp.Diagnostic) (status, detail string) {
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
	case hasNoPackageDiagnostic(items):
		// The server answered, but with no package association for the
		// document: its list carries the "no package" notice, not a reading of
		// the text, so an otherwise empty list must not read as clean.
		return control.LSPStatusUnassociated,
			"the language server has no package for " + path + ", so its diagnostics are not a reading of the text"
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

// syncWaiting tells the server which authors hold a pending proposal, so the
// roster can derive a quiet participant as waiting rather than idle. It runs
// on the event thread, which owns the pending set, and replaces the server's
// copy whole: a set accepted, rejected or cleared drops out.
func (a *App) syncWaiting() {
	if a.control == nil || a.control.Participants == nil {
		return
	}
	held := map[uint8]bool{}
	for _, p := range a.Proposals() {
		held[p.Author] = true
	}
	a.control.Participants.SetPending(held)
}
