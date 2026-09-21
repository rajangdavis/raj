package app

import (
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/keys"
	"raj/internal/ui"
)

// runClient runs a client-side decision to completion while pumping the daemon
// event thread, which is this test goroutine. A decision blocks on the daemon
// answer, so it cannot run on the same goroutine that would give it.
func runClient(t *testing.T, ch *clientHarness, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-done:
			return
		case <-deadline:
			t.Fatal("client action never finished")
		default:
			ch.srv.drain()
			time.Sleep(time.Millisecond)
		}
	}
}

// serverStates is every change set state in the daemon, by group.
func serverStates(srv *harness) map[uint64]string {
	out := map[uint64]string{}
	for _, g := range srv.Pane().File.Session().Groups() {
		out[g.ID] = g.State.String()
	}
	return out
}

// proposeAt makes a proposed change set on the daemon at [start,end) and
// returns its group id.
func proposeAt(t *testing.T, srv *harness, start, end int, text string) uint64 {
	t.Helper()
	c := srv.dial(t)
	base := c.do(srv, control.Request{Op: "text"}).Version
	if r := c.do(srv, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: start, End: end, Text: text}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	return srv.Pane().File.Session().LastGroup()
}

// A single accept in client mode goes to the daemon: the set is accepted
// there and the client tab is refetched, not mutated locally. Without the
// proxy the client would call AcceptGroup on its snapshot copy and the daemon
// would still hold the proposal.
func TestClientAcceptProxiesToDaemon(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	p.Cursors.Set(0, 0)
	runClient(t, ch, func() { ch.cli.reviewProposed(true) })

	if got := serverStates(ch.srv)[id]; got != "accepted" {
		t.Errorf("daemon group %d = %q, want accepted", id, got)
	}
	if pending := p.File.Session().Pending(); len(pending) != 0 {
		t.Errorf("client still holds the proposal: %+v", pending)
	}
	ch.cli.drain()
}

// A reject proxies the same way.
func TestClientRejectProxiesToDaemon(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	id := ch.srv.Pane().File.Session().LastGroup()
	p.Cursors.Set(0, 0)
	runClient(t, ch, func() { ch.cli.reviewProposed(false) })

	if got := serverStates(ch.srv)[id]; got != "rejected" {
		t.Errorf("daemon group %d = %q, want rejected", id, got)
	}
	if pending := p.File.Session().Pending(); len(pending) != 0 {
		t.Errorf("client still holds the proposal: %+v", pending)
	}
	ch.cli.drain()
}

// A clear proxies too, and it claims the path on the decision connection
// first because the wire clear is claim-gated. Without the claim the daemon
// refuses the clear before it reverses anything.
func TestClientClearProxiesWithClaim(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	id := ch.srv.Pane().File.Session().LastGroup()
	p.Cursors.Set(0, 0)
	// Reject first: clear only reaches a rejected set, and the reject refetch
	// is what gives the client a rejected copy to find at the caret.
	runClient(t, ch, func() { ch.cli.reviewProposed(false) })
	if got := serverStates(ch.srv)[id]; got != "rejected" {
		t.Fatalf("setup: daemon group %d = %q, want rejected", id, got)
	}
	runClient(t, ch, func() { ch.cli.clearRejected() })

	if text := ch.srv.Pane().File.Text(); strings.Contains(text, "// proposed") {
		t.Errorf("daemon text still holds the cleared bytes: %q", text)
	}
	if text := p.File.Text(); strings.Contains(text, "// proposed") {
		t.Errorf("client text still holds the cleared bytes: %q", text)
	}
	if !strings.Contains(ch.cli.status, "cleared") {
		t.Errorf("status = %q, want a clear confirmation", ch.cli.status)
	}
	ch.cli.drain()
}

// A bulk accept sends one verb for every visible set. Without the per-set
// proxy only the set under the caret would move and the rest would stay
// proposed on the daemon.
func TestClientBulkAcceptProxiesEverySet(t *testing.T) {
	srv := controlHarness(t, "one\ntwo\nthree\nfour\n")
	// The later line first, so the second proposal offsets are settled before
	// the insertion above them shifts the text.
	at := srv.Pane().File.LineStart(3)
	lower := proposeAt(t, srv, at, at, "P2\n")
	upper := proposeAt(t, srv, 0, 0, "P1\n")
	ch := attachClient(t, srv)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	// A caret away from every set makes the chord the bulk form.
	off := p.File.OffsetAt(2, 0)
	p.Cursors.Set(off, off)
	runClient(t, ch, func() { ch.cli.reviewProposed(true) })

	states := serverStates(ch.srv)
	for _, id := range []uint64{lower, upper} {
		if states[id] != "accepted" {
			t.Errorf("daemon group %d = %q, want accepted", id, states[id])
		}
	}
	if pending := p.File.Session().Pending(); len(pending) != 0 {
		t.Errorf("client still holds proposals: %+v", pending)
	}
	ch.cli.drain()
}

// A host refusal is the status and leaves the snapshot copy untouched: the
// bogus group never reaches the local session. Without the refusal branch a
// failed proxy would fall through to a local mutation.
func TestClientRefusedDecisionSurfacesHostError(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	before := len(p.File.Session().Pending())
	var ok bool
	runClient(t, ch, func() { ok = ch.cli.decideRemote(p, 999999, false) })

	if ok {
		t.Error("a reject of a group the daemon does not hold succeeded")
	}
	if !strings.Contains(ch.cli.status, "no change set") {
		t.Errorf("status = %q, want the daemon refusal", ch.cli.status)
	}
	if got := len(p.File.Session().Pending()); got != before {
		t.Errorf("refusal changed the client state: %d pending, want %d", got, before)
	}
}

// A client buffer refuses document edits in every mode. Leaving Review with
// the real toggle must not create a local change the next wake would discard,
// and the refusal must say so rather than silently dropping the key.
func TestClientRefusesTypingOutsideReview(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	want := p.File.Text()
	// Leave Review: the ordinary editor would now accept typing.
	ch.cli.toggleReview()
	if ch.cli.mode != ModeEdit {
		t.Fatalf("mode = %v, want Edit after the toggle", ch.cli.mode)
	}
	ch.cli.typeText("Z")

	if p.File.Text() != want {
		t.Errorf("client mode allowed an edit: %q", p.File.Text())
	}
	if !strings.Contains(ch.cli.status, "read-only") {
		t.Errorf("status = %q, want a read-only note", ch.cli.status)
	}
}

// The read-only gate is shared, not just the typing path: Paste, Cut,
// undo/redo and the find bar replace all are refused in client mode too, each
// with the note. Without the shared readOnly check these reach the pane and
// change the snapshot copy.
func TestClientRefusesEveryEditGesture(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	ch.cli.toggleReview()
	if ch.cli.mode != ModeEdit {
		t.Fatalf("mode = %v, want Edit after the toggle", ch.cli.mode)
	}

	gestures := []struct {
		name string
		run  func()
	}{
		{"typing", func() { ch.cli.typeText("Z") }},
		{"paste", func() { ch.cli.Handle(ui.Paste{Text: "Z"}); ch.cli.drain() }},
		{"cut", func() { ch.cli.handleKeyAction(keys.Cut) }},
		{"undo", func() { ch.cli.handleKeyAction(keys.Undo) }},
		{"redo", func() { ch.cli.handleKeyAction(keys.Redo) }},
		{"find replace all", func() {
			ch.cli.handleKeyAction(keys.FindInFile)
			ch.cli.handleKeyAction(keys.LineBelow)
		}},
	}
	for _, g := range gestures {
		t.Run(g.name, func(t *testing.T) {
			before := p.File.Text()
			ch.cli.status = ""
			g.run()
			if got := p.File.Text(); got != before {
				t.Errorf("client mode allowed %s: %q", g.name, got)
			}
			if !strings.Contains(ch.cli.status, "read-only") {
				t.Errorf("%s status = %q, want a read-only note", g.name, ch.cli.status)
			}
		})
	}
}

// In client mode the decision proxies to the daemon and refetches; the advance
// then lands on the next set in the refreshed snapshot, not a stale offset.
// Without advancing after the refetch the client stays on the decided set.
func TestClientAcceptAdvancesAfterRefetch(t *testing.T) {
	srv := controlHarness(t, "aaa\nbbb\nccc\n")
	first := proposeAt(t, srv, 0, 3, "AAA")
	second := proposeAt(t, srv, 8, 11, "CCC")
	ch := attachClient(t, srv)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	p.Cursors.Set(p.File.OffsetAt(0, 0), p.File.OffsetAt(0, 0))

	runClient(t, ch, func() { ch.cli.reviewProposed(true) })

	states := serverStates(ch.srv)
	if states[first] != "accepted" {
		t.Errorf("daemon group %d = %q, want accepted", first, states[first])
	}
	if states[second] != "proposed" {
		t.Errorf("daemon group %d = %q, want still proposed", second, states[second])
	}
	if line, _ := p.File.LineCol(p.Cursors.Primary().Head); line != 2 {
		t.Errorf("client caret line = %d, want the refreshed next set on line 2", line)
	}
	if !strings.Contains(ch.cli.status, "proposal 1 of 1") {
		t.Errorf("status = %q, want the new position", ch.cli.status)
	}
}

// A refused decision is the status and never advances: a stale client copy
// whose set the daemon already rejected stays on that set.
func TestClientRefusedDecisionDoesNotAdvance(t *testing.T) {
	ch := newClientHarness(t) // one proposed set on the daemon
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	if !ch.srv.Pane().File.RejectGroup(id) {
		t.Fatal("setup: daemon reject failed")
	}
	before := p.Cursors.Primary().Head

	runClient(t, ch, func() { ch.cli.reviewProposed(false) })

	if got := p.Cursors.Primary().Head; got != before {
		t.Errorf("a refused reject advanced the caret from %d to %d", before, got)
	}
	if !strings.Contains(ch.cli.status, "already rejected") {
		t.Errorf("status = %q, want the daemon refusal", ch.cli.status)
	}
}

// A client reject stays on the set, matching the local rule: only accept
// advances. Without the accept-only rule the client would move to the next set.
func TestClientRejectStaysOnTheSet(t *testing.T) {
	srv := controlHarness(t, "aaa\nbbb\nccc\n")
	first := proposeAt(t, srv, 0, 3, "AAA")
	second := proposeAt(t, srv, 8, 11, "CCC")
	ch := attachClient(t, srv)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	p.Cursors.Set(p.File.OffsetAt(0, 0), p.File.OffsetAt(0, 0))

	runClient(t, ch, func() { ch.cli.reviewProposed(false) })

	states := serverStates(ch.srv)
	if states[first] != "rejected" {
		t.Errorf("daemon group %d = %q, want rejected", first, states[first])
	}
	if states[second] != "proposed" {
		t.Errorf("daemon group %d = %q, want still proposed", second, states[second])
	}
	if line, _ := p.File.LineCol(p.Cursors.Primary().Head); line != 0 {
		t.Errorf("client caret line = %d, want it left on the rejected set", line)
	}
}

// A refused drawer decision leaves the selection where it was: the workflow
// jump is for a decision that actually emptied the pending sets.
func TestClientPhoneDrawerRefusedDecisionKeepsSelection(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	id := proposeAt(t, srv, 0, 0, "// x\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	// The daemon rejects it out from under the client copy, so the client
	// reject is refused.
	if !ch.srv.Pane().File.RejectGroup(id) {
		t.Fatal("setup: daemon reject failed")
	}
	openPhoneDrawer(ch.cli)
	rej := -1
	for i, b := range ch.cli.drawerPanel {
		if b.action == keys.RejectProposed {
			rej = i
		}
	}
	if rej < 0 {
		t.Fatalf("no reject button: %+v", ch.cli.drawerPanel)
	}
	ch.cli.drawerSel = rej

	runClient(t, ch, func() { ch.cli.drawerDispatch(keys.RejectProposed) })
	ch.cli.drain()

	if ch.cli.drawerSel != rej {
		t.Errorf("a refused decision moved the selection to %d, want %d", ch.cli.drawerSel, rej)
	}
	if got := ch.cli.drawerPanel[ch.cli.drawerSel].action; got != keys.RejectProposed {
		t.Errorf("selection = %v, want reject unchanged", got)
	}
	if !strings.Contains(ch.cli.status, "already rejected") {
		t.Errorf("status = %q, want the refusal", ch.cli.status)
	}
}

// driveClientWatch gives the daemon a tick so controlTick bumps the watch
// generation, then pumps both event threads until cond holds. A decision made
// straight on the daemon session does not go through a control verb, so the
// tick is what wakes the parked watch; the loop then carries the fetch and the
// install. It fails when cond never holds, which is the stale snapshot this
// file guards: a client that only watched the version never refreshes.
func driveClientWatch(t *testing.T, ch *clientHarness, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("the client never refreshed after the daemon-side change")
		default:
		}
		ch.srv.Handle(ui.Tick{})
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	// Settle any install already queued before the caller measures.
	ch.srv.drain()
	ch.cli.drain()
}

// A decision delivered over the control connection wakes the watchers from
// drainControl itself, without waiting for the next idle tick: a client that
// did not make the decision still clears. Without the drainControl bump the
// watch stays parked until some unrelated ui.Tick arrives.
func TestClientRefreshesWithoutAnIdleTick(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := p.File.Session().LastGroup()
	c := ch.srv.dial(t)
	if r := c.do(ch.srv, control.Request{Op: "accept", Path: p.File.Path, Group: id}); !r.OK {
		t.Fatalf("accept = %+v", r)
	}
	// Pump both event loops, but never deliver the daemon a ui.Tick.
	deadline := time.After(3 * time.Second)
	for len(p.File.Session().Pending()) != 0 {
		select {
		case <-deadline:
			t.Fatalf("client did not refresh without an idle tick; pending = %d", len(p.File.Session().Pending()))
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A decision made on the daemon, not through this client, still refreshes the
// client copy. Accept drops Pending without moving the version, so a client
// that only compared versions kept showing a proposal the daemon had already
// accepted.
func TestClientRefreshesOnDaemonAccept(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	// The fixture must hold the set on both sides, or the refresh proves
	// nothing.
	if pending := ch.srv.Pane().File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the daemon holds no proposal")
	}
	if pending := p.File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the client did not carry the proposal")
	}
	// Accept straight on the daemon session: no client call, so nothing
	// refetches explicitly.
	ch.srv.Pane().File.AcceptGroup(id)
	driveClientWatch(t, ch, func() bool { return len(p.File.Session().Pending()) == 0 })

	if got := serverStates(ch.srv)[id]; got != "accepted" {
		t.Errorf("daemon group %d = %q, want accepted", id, got)
	}
	if !strings.Contains(p.File.Text(), "// proposed") {
		t.Errorf("client text lost the accepted bytes: %q", p.File.Text())
	}
}

// A daemon-side reject refreshes the client the same way: the set leaves the
// pending list without a version move.
func TestClientRefreshesOnDaemonReject(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	if pending := ch.srv.Pane().File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the daemon holds no proposal")
	}
	if pending := p.File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the client did not carry the proposal")
	}
	if !ch.srv.Pane().File.RejectGroup(id) {
		t.Fatal("setup: daemon reject failed")
	}
	driveClientWatch(t, ch, func() bool { return len(p.File.Session().Pending()) == 0 })

	if got := serverStates(ch.srv)[id]; got != "rejected" {
		t.Errorf("daemon group %d = %q, want rejected", id, got)
	}
}

// A daemon-side clear takes the rejected bytes out of the view, and the client
// must drop them too rather than keep a snapshot the old version gate stopped
// following.
func TestClientRefreshesOnDaemonClear(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	if pending := ch.srv.Pane().File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the daemon holds no proposal")
	}
	if pending := p.File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the client did not carry the proposal")
	}
	// Reject first: clear only reaches a rejected set. The client catches up
	// on the reject before the clear becomes a visible text change.
	if !ch.srv.Pane().File.RejectGroup(id) {
		t.Fatal("setup: daemon reject failed")
	}
	driveClientWatch(t, ch, func() bool { return len(p.File.Session().Pending()) == 0 })
	if !strings.Contains(p.File.Text(), "// proposed") {
		t.Fatalf("setup: the rejected bytes left the client view early: %q", p.File.Text())
	}
	if ok, _ := ch.srv.Pane().File.ClearGroup(id); !ok {
		t.Fatal("setup: daemon clear failed")
	}
	driveClientWatch(t, ch, func() bool { return !strings.Contains(p.File.Text(), "// proposed") })
}

// A daemon-side save is the approval: it lands the pending set and clears
// Dirty, and the client must follow. Dirty flips without a version move, so
// the version-only mark used to leave the client holding a proposal that no
// longer existed on the daemon.
func TestClientRefreshesOnDaemonSave(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	id := ch.srv.Pane().File.Session().LastGroup()
	if pending := ch.srv.Pane().File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the daemon holds no proposal")
	}
	if pending := p.File.Session().Pending(); len(pending) == 0 {
		t.Fatal("setup: the client did not carry the proposal")
	}
	if !ch.srv.Pane().File.ViewDirty() {
		t.Fatal("setup: the daemon buffer is not dirty before the save")
	}
	// No client call: the save runs straight on the daemon session.
	if err := ch.srv.Pane().File.Save(); err != nil {
		t.Fatalf("daemon save = %v", err)
	}
	if ch.srv.Pane().File.ViewDirty() {
		t.Error("the daemon buffer is still dirty after a save")
	}
	driveClientWatch(t, ch, func() bool { return len(p.File.Session().Pending()) == 0 })

	if got := serverStates(ch.srv)[id]; got != "accepted" {
		t.Errorf("daemon group %d = %q, want accepted", id, got)
	}
}
