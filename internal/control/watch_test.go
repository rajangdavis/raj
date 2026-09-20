package control

import "testing"

// watchRegister hands back a cancel that removes the watcher, which is what
// lets a disconnected client's parked watch be forgotten rather than kept.
func TestWatchRegisterCleansUp(t *testing.T) {
	s := &Server{}
	id, ch, cancel := s.watchRegister()
	if id == 0 || ch == nil {
		t.Fatalf("register = %d, %v", id, ch)
	}
	s.watchMu.Lock()
	n := len(s.watchers)
	s.watchMu.Unlock()
	if n != 1 {
		t.Fatalf("watchers = %d, want 1", n)
	}
	cancel()
	s.watchMu.Lock()
	n = len(s.watchers)
	s.watchMu.Unlock()
	if n != 0 {
		t.Fatalf("after cancel watchers = %d, want 0", n)
	}
}

// BumpGen wakes every parked watcher once, and a repeated generation wakes
// nobody, so a client cannot be spun by re-storing the same number.
func TestBumpGenFansOut(t *testing.T) {
	s := &Server{}
	_, a, cancelA := s.watchRegister()
	_, b, cancelB := s.watchRegister()
	defer cancelA()
	defer cancelB()

	s.BumpGen(1)
	for name, ch := range map[string]chan struct{}{"a": a, "b": b} {
		select {
		case <-ch:
		default:
			t.Errorf("watcher %s did not wake", name)
		}
	}
	s.BumpGen(1)
	select {
	case <-a:
		t.Error("a repeated generation woke a watcher")
	default:
	}
}
