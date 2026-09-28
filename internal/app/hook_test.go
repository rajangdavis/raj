package app

import (
	"strings"
	"testing"

	"raj/internal/control"
)

// TestHostHooksCRUD drives the app host's hook surface over a real workspace
// store. Precondition: newHarnessAt opens the store the package TestMain points
// at a temp XDG_STATE_HOME, so a.state is a real *store.Store. Without the four
// host methods the BufferHost interface is unsatisfied; without the store
// conversion the round-trip fails; without validation the invalid-row refusals
// fail.
func TestHostHooksCRUD(t *testing.T) {
	h := hostOf(newHarnessAt(t, t.TempDir()).App)

	rows, err := h.Hooks()
	if err != nil {
		t.Fatalf("Hooks on a fresh store: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("Hooks on a fresh store = %v, want an empty non-nil slice", rows)
	}

	valid := control.HookRow{Name: "check", Action: `["go","test","./..."]`, Trigger: "agent",
		CooldownMS: 2000, TimeoutMS: 30000, Enabled: true}
	if err := h.PutHook(valid); err != nil {
		t.Fatalf("PutHook valid: %v", err)
	}
	// An invalid action and an unknown trigger are both refused before the
	// store is written.
	if err := h.PutHook(control.HookRow{Name: "bad", Action: "not json", Trigger: "agent"}); err == nil {
		t.Error("PutHook accepted an invalid action")
	}
	if err := h.PutHook(control.HookRow{Name: "notrigger", Action: `["true"]`}); err == nil {
		t.Error("PutHook accepted an unknown trigger")
	}

	rows, err = h.Hooks()
	if err != nil {
		t.Fatalf("Hooks: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "check" || rows[0].Action != valid.Action ||
		rows[0].CooldownMS != 2000 || rows[0].TimeoutMS != 30000 || !rows[0].Enabled {
		t.Fatalf("Hooks = %+v, want the valid row only", rows)
	}

	// Insert order does not decide output order: the store sorts by name.
	for _, name := range []string{"zeta", "alpha"} {
		if err := h.PutHook(control.HookRow{Name: name, Action: `["true"]`, Trigger: "agent", Enabled: true}); err != nil {
			t.Fatalf("PutHook %s: %v", name, err)
		}
	}
	rows, err = h.Hooks()
	if err != nil {
		t.Fatalf("Hooks sorted: %v", err)
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	if got := strings.Join(names, ","); got != "alpha,check,zeta" {
		t.Fatalf("Hooks order = %s, want alpha,check,zeta", got)
	}

	// enable/disable flip the stored row and refuse an absent name.
	if err := h.SetHookEnabled("check", false); err != nil {
		t.Fatalf("SetHookEnabled false: %v", err)
	}
	rows, _ = h.Hooks()
	if rows[1].Enabled {
		t.Error("SetHookEnabled(false) left the row enabled")
	}
	if err := h.SetHookEnabled("check", true); err != nil {
		t.Fatalf("SetHookEnabled true: %v", err)
	}
	rows, _ = h.Hooks()
	if !rows[1].Enabled {
		t.Error("SetHookEnabled(true) left the row disabled")
	}
	if err := h.SetHookEnabled("absent", true); err == nil {
		t.Error("SetHookEnabled accepted an absent name")
	}

	// Delete removes the row; a second delete is a no-op.
	if err := h.DeleteHook("check"); err != nil {
		t.Fatalf("DeleteHook: %v", err)
	}
	rows, _ = h.Hooks()
	if len(rows) != 2 {
		t.Errorf("after delete = %+v, want two rows", rows)
	}
	if err := h.DeleteHook("check"); err != nil {
		t.Errorf("DeleteHook absent: %v", err)
	}
}

// TestHostHooksCarryTreeAndDetach pins the two L1/L2 fields through the app
// host and the real store. The control package's tests use memHost, which
// keeps whatever it is handed, so a conversion here that drops a field passed
// every control test while `raj hook add cycle --tree workspace --detach`
// stored a projected, attached hook. An empty tree reads back as projected.
func TestHostHooksCarryTreeAndDetach(t *testing.T) {
	h := hostOf(newHarnessAt(t, t.TempDir()).App)

	cycle := control.HookRow{Name: "cycle", Action: `["sh","scripts/raj-cycle.sh"]`, Trigger: "agent",
		Tree: "workspace", Agent: true, TimeoutMS: 1800000, MayWrite: true, Detach: true, Enabled: true}
	if err := h.PutHook(cycle); err != nil {
		t.Fatalf("PutHook cycle: %v", err)
	}
	if err := h.PutHook(control.HookRow{Name: "plain", Action: `["true"]`, Trigger: "agent", Enabled: true}); err != nil {
		t.Fatalf("PutHook plain: %v", err)
	}
	rows, err := h.Hooks()
	if err != nil {
		t.Fatalf("Hooks: %v", err)
	}
	byName := map[string]control.HookRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if got := byName["cycle"]; got.Tree != "workspace" || !got.Detach || !got.MayWrite {
		t.Errorf("cycle read back as tree=%q detach=%t may_write=%t, want workspace/true/true", got.Tree, got.Detach, got.MayWrite)
	}
	if got := byName["plain"]; got.Tree != "projected" || got.Detach {
		t.Errorf("plain read back as tree=%q detach=%t, want projected/false", got.Tree, got.Detach)
	}
}
