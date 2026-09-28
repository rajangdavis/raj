package hooks

import (
	"strings"
	"testing"
	"time"
)

// gateNow is a fixed base the tests advance by hand; the Gate never reads the
// clock itself, so every case is deterministic.
var gateNow = time.Unix(1_700_000_000, 0)

// TestGateDefaults pins the zero Options behaviour: the default floor and the
// default per-revision cap. Precondition: two fresh gates and a fixed clock.
// It fails if NewGate leaves floor or cap at zero, which would let a hook run
// back to back and without bound.
func TestGateDefaults(t *testing.T) {
	if DefaultFloor != 2*time.Second {
		t.Fatalf("DefaultFloor = %v; want 2s", DefaultFloor)
	}
	if DefaultPerRevision != 3 {
		t.Fatalf("DefaultPerRevision = %d; want 3", DefaultPerRevision)
	}

	// The default floor refuses just inside it and allows exactly at it.
	g := NewGate(Options{})
	g.Begin("h", 1, gateNow)
	g.End("h")
	if _, ok, _ := g.Allow("h", 1, gateNow.Add(DefaultFloor-time.Millisecond)); ok {
		t.Fatalf("Allow just inside the default floor was allowed; want refused")
	}
	if _, ok, _ := g.Allow("h", 1, gateNow.Add(DefaultFloor)); !ok {
		t.Fatalf("Allow at the default floor was refused; want allowed")
	}

	// The default cap stops the run after DefaultPerRevision in one revision.
	g2 := NewGate(Options{})
	at := gateNow
	for i := 0; i < DefaultPerRevision; i++ {
		if _, ok, reason := g2.Allow("h", 1, at); !ok {
			t.Fatalf("run %d under the default cap was refused: %s; want allowed", i, reason)
		}
		g2.Begin("h", 1, at)
		g2.End("h")
		at = at.Add(DefaultFloor)
	}
	if _, ok, reason := g2.Allow("h", 1, at); ok {
		t.Fatalf("run past the default cap was allowed; want refused (%s)", reason)
	}
}

// TestGateCooldown covers the floor refusal, its retryAfter, and the recovery
// once now reaches the floor. Precondition: a gate with the default floor and
// one recorded run. It fails if a run just inside the floor is allowed, if
// retryAfter is not the remaining time or is not positive, or if the floor
// never lifts.
func TestGateCooldown(t *testing.T) {
	g := NewGate(Options{})
	if _, ok, reason := g.Allow("h", 1, gateNow); !ok {
		t.Fatalf("first Allow refused: %s", reason)
	}
	g.Begin("h", 1, gateNow)
	g.End("h")

	ra, ok, reason := g.Allow("h", 1, gateNow.Add(500*time.Millisecond))
	if ok {
		t.Fatalf("Allow inside the floor was allowed; want refused")
	}
	if want := DefaultFloor - 500*time.Millisecond; ra != want {
		t.Fatalf("retryAfter = %v; want %v", ra, want)
	}
	if ra <= 0 {
		t.Fatalf("retryAfter = %v; want positive for a cooldown refusal", ra)
	}
	if !strings.Contains(reason, "cooldown") {
		t.Fatalf("reason = %q; want it to say cooldown", reason)
	}
	if _, ok, _ := g.Allow("h", 1, gateNow.Add(DefaultFloor)); !ok {
		t.Fatalf("Allow at exactly the floor was refused; want allowed")
	}
}

// TestGatePerRevisionCap covers the per-revision cap and that its refusal
// carries no retryAfter (only the cooldown does). Precondition: a gate capped
// at two runs with a one-second floor. It fails if a third run in the same
// revision is allowed or if the cap refusal reports a retryAfter.
func TestGatePerRevisionCap(t *testing.T) {
	g := NewGate(Options{Floor: time.Second, PerRevision: 2})
	at := gateNow
	for i := 0; i < 2; i++ {
		if _, ok, reason := g.Allow("h", 1, at); !ok {
			t.Fatalf("run %d refused: %s; want allowed", i, reason)
		}
		g.Begin("h", 1, at)
		g.End("h")
		at = at.Add(time.Second)
	}
	ra, ok, reason := g.Allow("h", 1, at)
	if ok {
		t.Fatalf("run past the cap was allowed; want refused")
	}
	if ra != 0 {
		t.Fatalf("retryAfter = %v for a cap refusal; want 0", ra)
	}
	if !strings.Contains(reason, "cap") {
		t.Fatalf("reason = %q; want it to say cap", reason)
	}
}

// TestGateRevisionResetsCount covers the count being per revision. Precondition:
// a gate capped at two, filled at revision 1. It fails if revision 2 inherits
// revision 1's count, which would refuse a run the policy separately allows.
func TestGateRevisionResetsCount(t *testing.T) {
	g := NewGate(Options{PerRevision: 2})
	at := gateNow
	for i := 0; i < 2; i++ {
		if _, ok, reason := g.Allow("h", 1, at); !ok {
			t.Fatalf("revision 1 run %d refused: %s; want allowed", i, reason)
		}
		g.Begin("h", 1, at)
		g.End("h")
		at = at.Add(DefaultFloor)
	}
	if _, ok, _ := g.Allow("h", 1, at); ok {
		t.Fatalf("revision 1 past the cap was allowed; want refused")
	}
	if _, ok, reason := g.Allow("h", 2, at); !ok {
		t.Fatalf("revision 2 after the cap was refused: %s; want its own count", reason)
	}
	g.Begin("h", 2, at)
	g.End("h")
	at = at.Add(DefaultFloor)
	if _, ok, reason := g.Allow("h", 2, at); !ok {
		t.Fatalf("revision 2 second run was refused: %s; want within its cap", reason)
	}
}

// TestGateInFlight covers the in-flight refusal and its empty retryAfter.
// Precondition: a run begun and not ended. It fails if a second run starts
// while one is in flight, even well past the cooldown, or if the refusal
// offers a retryAfter (the retry is the pending rerun, not a wait).
func TestGateInFlight(t *testing.T) {
	g := NewGate(Options{})
	g.Begin("h", 1, gateNow)
	ra, ok, reason := g.Allow("h", 1, gateNow.Add(time.Hour))
	if ok {
		t.Fatalf("Allow while in flight was allowed; want refused")
	}
	if ra != 0 {
		t.Fatalf("retryAfter = %v while in flight; want 0", ra)
	}
	if !strings.Contains(reason, "flight") {
		t.Fatalf("reason = %q; want it to say in flight", reason)
	}
	g.End("h")
	if _, ok, _ := g.Allow("h", 1, gateNow.Add(time.Hour)); !ok {
		t.Fatalf("Allow after End was refused; want allowed once the cooldown has passed")
	}
}

// TestGateRequestCoalescing covers at most one pending rerun per hook and its
// interaction with Begin and End. Precondition: a gate with no run in flight.
// It fails if more than one request is queued, if a request outside a run is
// accepted, if Begin does not clear the pending flag it is starting, or if End
// drops a pending rerun before the runner can take it.
func TestGateRequestCoalescing(t *testing.T) {
	g := NewGate(Options{})
	if g.Request("h") {
		t.Fatalf("Request on a hook not in flight was accepted; want false")
	}
	g.Begin("h", 1, gateNow)
	if !g.Request("h") {
		t.Fatalf("first Request in flight was refused; want accepted")
	}
	if g.Request("h") {
		t.Fatalf("second Request was accepted; want at most one pending rerun")
	}
	if !g.TakePending("h") {
		t.Fatalf("TakePending = false; want the queued rerun")
	}
	if g.TakePending("h") {
		t.Fatalf("TakePending consumed the same rerun twice")
	}
	if !g.Request("h") {
		t.Fatalf("Request after consuming was refused; want a fresh pending rerun")
	}
	// Begin of the rerun clears the pending flag it is starting.
	g.Begin("h", 1, gateNow)
	if g.TakePending("h") {
		t.Fatalf("TakePending after Begin = true; Begin must clear a pending rerun")
	}
	// A pending rerun queued while in flight survives End so the runner can
	// take it once the run it belongs to has ended.
	if !g.Request("h") {
		t.Fatalf("Request after Begin was refused; want accepted")
	}
	g.End("h")
	if !g.TakePending("h") {
		t.Fatalf("TakePending after End = false; a pending rerun must survive End")
	}
}

// TestGateRefusalsDoNotMutate covers checks being queries. Precondition: a
// hook at its per-revision cap. It fails if a refused Allow resets the count,
// which would let repeated probes eventually allow a run the policy forbids.
func TestGateRefusalsDoNotMutate(t *testing.T) {
	g := NewGate(Options{Floor: time.Second, PerRevision: 1})
	g.Begin("h", 1, gateNow)
	g.End("h")
	at := gateNow.Add(time.Second)
	if _, ok, _ := g.Allow("h", 1, at); ok {
		t.Fatalf("Allow at the cap was allowed; want refused")
	}
	if _, ok, _ := g.Allow("h", 1, at); ok {
		t.Fatalf("second Allow at the cap was allowed; a refusal must not reset the count")
	}
}
