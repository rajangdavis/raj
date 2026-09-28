package hooks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"raj/internal/hooks/builtin"
)

// testLeaf is the builtin a parse test registers: it echoes its args. The
// registry is package state, so one init registers it for every test in the
// binary.
type testLeaf struct{ name string }

func (l testLeaf) Name() string { return l.name }

func (l testLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	return builtin.Result{Output: "ok"}, nil
}

func init() {
	if err := builtin.Register("test.echo", testLeaf{name: "test.echo"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("test.humanonly", testLeaf{name: "test.humanonly"}, builtin.HumanOnly); err != nil {
		panic(err)
	}
}

// TestParseAccepts walks the shapes Parse must accept: an argv array, the
// {"shell":...} object, surrounding whitespace, and the zero durations.
// Precondition: none, Parse is pure. It fails if an accepted shape is refused,
// if milliseconds are scaled wrong, or if Parse substitutes DefaultTimeout for
// a zero TimeoutMS (the runner does that, not Parse).
func TestParseAccepts(t *testing.T) {
	tests := []struct {
		name string
		raw  Raw
		want Hook
	}{
		{
			name: "argv array",
			raw: Raw{Name: "check", Action: `["bash","scripts/check.sh"]`, Trigger: "agent",
				Agent: true, CooldownMS: 2000, TimeoutMS: 30000, MayWrite: true, Enabled: true},
			want: Hook{Name: "check", Argv: []string{"bash", "scripts/check.sh"}, Trigger: TriggerAgent,
				Tree: TreeProjected, Agent: true, Cooldown: 2 * time.Second, Timeout: 30 * time.Second, MayWrite: true, Enabled: true},
		},
		{
			name: "shell object",
			raw:  Raw{Name: "notify", Action: `{"shell":"echo done"}`, Trigger: "agent", Enabled: true},
			want: Hook{Name: "notify", Shell: "echo done", Trigger: TriggerAgent, Tree: TreeProjected, Enabled: true},
		},
		{
			name: "builtin object",
			raw:  Raw{Name: "leaf", Action: `{"builtin":"test.echo","args":{"n":1}}`, Trigger: "agent", Enabled: true},
			want: Hook{Name: "leaf", Builtin: "test.echo", BuiltinArgs: map[string]any{"n": float64(1)},
				Trigger: TriggerAgent, Tree: TreeProjected, Enabled: true},
		},
		{
			name: "builtin human-only leaf",
			raw:  Raw{Name: "hleaf", Action: `{"builtin":"test.humanonly"}`, Trigger: "agent", Enabled: true},
			want: Hook{Name: "hleaf", Builtin: "test.humanonly", BuiltinPolicy: builtin.HumanOnly,
				Trigger: TriggerAgent, Tree: TreeProjected, Enabled: true},
		},
		{
			name: "surrounding whitespace",
			raw:  Raw{Name: "ws", Action: "  [\"true\"]\n", Trigger: "agent", Enabled: true},
			want: Hook{Name: "ws", Argv: []string{"true"}, Trigger: TriggerAgent, Tree: TreeProjected, Enabled: true},
		},
		{
			name: "zero durations stay zero",
			raw:  Raw{Name: "zero", Action: `["true"]`, Trigger: "agent", Enabled: true},
			want: Hook{Name: "zero", Argv: []string{"true"}, Trigger: TriggerAgent, Tree: TreeProjected, Enabled: true,
				Cooldown: 0, Timeout: 0},
		},
		{
			name: "workspace tree",
			raw:  Raw{Name: "ws-tree", Action: `["true"]`, Trigger: "agent", Tree: "workspace", Enabled: true},
			want: Hook{Name: "ws-tree", Argv: []string{"true"}, Trigger: TriggerAgent, Tree: TreeWorkspace, Enabled: true},
		},
		{
			name: "detached",
			raw:  Raw{Name: "cycle", Action: `["true"]`, Trigger: "agent", Detach: true, Enabled: true},
			want: Hook{Name: "cycle", Argv: []string{"true"}, Trigger: TriggerAgent, Tree: TreeProjected, Detach: true, Enabled: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse(%+v) error: %v; want accept", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse(%+v) = %+v; want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestTriggerString pins the one trigger v0 ships and the fallback for a value
// a later version might add. Precondition: none. It fails if String reports a
// future trigger as "agent", which would make an unknown trigger
// indistinguishable from the one that exists.
func TestTriggerString(t *testing.T) {
	if got := TriggerAgent.String(); got != "agent" {
		t.Fatalf("TriggerAgent.String() = %q; want agent", got)
	}
	if got := Trigger(9).String(); got == "" || got == "agent" {
		t.Fatalf("Trigger(9).String() = %q; want a non-empty non-agent label", got)
	}
}

// TestDefaultTimeout pins the runner default that Parse deliberately does not
// apply. Precondition: none. It fails if the constant drifts or if Parse
// starts clamping a zero TimeoutMS, which would take the choice away from the
// runner.
func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 2*time.Minute {
		t.Fatalf("DefaultTimeout = %v; want 2m", DefaultTimeout)
	}
	h, err := Parse(Raw{Name: "t", Action: `["true"]`, Trigger: "agent", Enabled: true})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if h.Timeout != 0 {
		t.Fatalf("Parse Timeout = %v; want 0 so the runner applies DefaultTimeout", h.Timeout)
	}
}

// TestParseRefuses is the refusal table. Precondition: none. It fails if a
// malformed row is accepted — each is policy that would run on the host — if
// the refusal does not return the zero Hook, or if a message stops naming the
// offending field or trigger, which is what makes a stored row fixable.
func TestParseRefuses(t *testing.T) {
	agent := func(action string) Raw {
		return Raw{Name: "x", Action: action, Trigger: "agent"}
	}
	tests := []struct {
		name    string
		raw     Raw
		wantSub string
	}{
		{"empty name", Raw{Action: `["true"]`, Trigger: "agent"}, "name"},
		{"empty action", Raw{Name: "x", Trigger: "agent"}, "action"},
		{"invalid json array", agent(`["a"`), "action"},
		{"junk action", agent("not json"), "action"},
		{"empty array", agent(`[]`), "empty"},
		{"array non-string element", agent(`["a",1]`), "action"},
		{"array empty element", agent(`["a",""]`), "empty"},
		{"array null element", agent(`["a",null]`), "empty"},
		{"object neither key", agent(`{}`), "exactly one"},
		// shell plus argv is two keys, so it is refused for the same reason as
		// any extra key. This case predates the tightening and still passes.
		{"object both keys", agent(`{"shell":"a","argv":["b"]}`), "exactly one"},
		{"object empty shell", agent(`{"shell":""}`), "shell"},
		{"object null shell", agent(`{"shell":null}`), "exactly one"},
		{"object empty argv", agent(`{"argv":[]}`), "empty"},
		{"object empty argv element", agent(`{"argv":["a",""]}`), "empty"},
		// {"argv":...} was an accepted object shape; the spec action is only a
		// bare argv array or {"shell":...}, so it is refused now. Precondition:
		// a well-formed non-empty argv object. Without the change Parse accepts
		// it and returns no error, so this case fails on err == nil.
		{"object argv", agent(`{"argv":["go","test"]}`), "exactly one"},
		// The object has "shell" plus an unknown extra key. Precondition: two
		// keys present. Without the change the struct decode ignored the unknown
		// key and accepted, so it fails on err == nil.
		{"object shell unknown key", agent(`{"shell":"echo done","timeout":5}`), "exactly one"},
		{"object builtin empty", agent(`{"builtin":""}`), "builtin"},
		{"object builtin unknown", agent(`{"builtin":"no.such.leaf"}`), "no.such.leaf"},
		{"object builtin null", agent(`{"builtin":null}`), "builtin"},
		{"object builtin and shell", agent(`{"builtin":"test.echo","shell":"x"}`), "exactly one"},
		{"object builtin extra key", agent(`{"builtin":"test.echo","timeout":5}`), "exactly one"},
		{"object builtin args scalar", agent(`{"builtin":"test.echo","args":5}`), "args"},
		// A shell that is whitespace only is refused as empty. Precondition:
		// {"shell":"   "}. Before the change only "" was refused, so Parse
		// accepted whitespace and this case fails on err == nil.
		{"object whitespace shell", agent(`{"shell":"   "}`), "shell"},
		{"top-level string", agent(`"true"`), "array"},
		{"top-level number", agent(`42`), "array"},
		{"top-level bool", agent(`true`), "array"},
		{"top-level null", agent(`null`), "array"},
		{"trigger empty", Raw{Name: "x", Action: `["true"]`}, "trigger"},
		{"trigger unknown", Raw{Name: "x", Action: `["true"]`, Trigger: "save"}, `"save"`},
		{"negative cooldown", Raw{Name: "x", Action: `["true"]`, Trigger: "agent", CooldownMS: -1}, "negative"},
		{"negative timeout", Raw{Name: "x", Action: `["true"]`, Trigger: "agent", TimeoutMS: -1}, "negative"},
		{"unknown tree", Raw{Name: "x", Action: `["true"]`, Trigger: "agent", Tree: "elsewhere"}, "tree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err == nil {
				t.Fatalf("Parse(%+v) = %+v, nil; want an error", tt.raw, got)
			}
			if got.Name != "" || got.Argv != nil || got.Shell != "" || got.Builtin != "" || got.BuiltinArgs != nil {
				t.Fatalf("Parse(%+v) returned %+v alongside an error; want the zero Hook", tt.raw, got)
			}
			if tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Parse(%+v) error = %q; want it to contain %q", tt.raw, err, tt.wantSub)
			}
		})
	}
}

// TestNewSetSkipsInvalidRows is the resilience contract: one bad row is
// skipped and named, the rest survive. Precondition: four rows, two valid and
// two malformed, supplied out of name order. It fails if a malformed row
// aborts the whole set, if a malformed row is admitted, if the errors do not
// name their hook, or if All is not sorted.
func TestNewSetSkipsInvalidRows(t *testing.T) {
	raws := []Raw{
		{Name: "beta", Action: `["true"]`, Trigger: "agent", Enabled: true},
		{Name: "bad-action", Action: `[]`, Trigger: "agent"},
		{Name: "alpha", Action: `{"shell":"echo"}`, Trigger: "agent", Enabled: true},
		{Name: "bad-trigger", Action: `["true"]`, Trigger: "commit"},
	}
	set, errs := NewSet(raws)
	if len(errs) != 2 {
		t.Fatalf("NewSet errors = %v; want one per invalid row (2)", errs)
	}
	for _, want := range []string{"bad-action", "bad-trigger"} {
		found := false
		for _, err := range errs {
			if strings.Contains(err.Error(), want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("NewSet errors = %v; want one naming %q", errs, want)
		}
	}

	all := set.All()
	names := make([]string, len(all))
	for i, h := range all {
		names[i] = h.Name
	}
	if strings.Join(names, ",") != "alpha,beta" {
		t.Fatalf("All names = %v; want alpha, beta", names)
	}
	if h, ok := set.Get("alpha"); !ok || h.Shell != "echo" || h.Trigger != TriggerAgent {
		t.Fatalf("Get(alpha) = %+v, %v; want the shell hook", h, ok)
	}
	if _, ok := set.Get("missing"); ok {
		t.Fatalf("Get(missing) reported a hook")
	}
	if _, ok := set.Get("bad-action"); ok {
		t.Fatalf("Get(bad-action) reported a hook; the invalid row must be skipped")
	}
}

// TestNewSetEmpty covers the no-hooks workspace: no errors and a non-nil empty
// All, so a caller can range over it without a guard.
func TestNewSetEmpty(t *testing.T) {
	set, errs := NewSet(nil)
	if len(errs) != 0 {
		t.Fatalf("NewSet(nil) errors = %v; want none", errs)
	}
	if all := set.All(); all == nil || len(all) != 0 {
		t.Fatalf("All on an empty set = %v; want an empty non-nil slice", all)
	}
}

// TestAdmit exercises each sentinel and the caller asymmetry. Precondition: a
// set with an agent-callable enabled hook, a human-only enabled hook (Agent
// false) and a disabled hook. It fails if Admit conflates the refusals, if the
// human is blocked from an Agent=false hook, or if an agent caller is allowed
// one, and if a refusal stops naming the hook.
func TestAdmit(t *testing.T) {
	set, errs := NewSet([]Raw{
		{Name: "shared", Action: `["true"]`, Trigger: "agent", Agent: true, Enabled: true},
		{Name: "human", Action: `["true"]`, Trigger: "agent", Agent: false, Enabled: true},
		{Name: "off", Action: `["true"]`, Trigger: "agent", Agent: true, Enabled: false},
	})
	if len(errs) != 0 {
		t.Fatalf("NewSet errors = %v; want none", errs)
	}
	tests := []struct {
		name        string
		hook        string
		callerAgent bool
		wantErr     error
	}{
		{"agent hook, agent caller", "shared", true, nil},
		{"agent hook, human caller", "shared", false, nil},
		{"human-only hook, human caller", "human", false, nil},
		{"human-only hook, agent caller", "human", true, ErrNotAgentCallable},
		{"disabled hook, human caller", "off", false, ErrDisabled},
		{"disabled hook, agent caller", "off", true, ErrDisabled},
		{"missing hook, human caller", "missing", false, ErrNoHook},
		{"missing hook, agent caller", "missing", true, ErrNoHook},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := set.Admit(tt.hook, tt.callerAgent)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Admit(%q, %v) error: %v; want admit", tt.hook, tt.callerAgent, err)
				}
				if got.Name != tt.hook {
					t.Fatalf("Admit(%q, %v) = %+v; want the hook named %q", tt.hook, tt.callerAgent, got, tt.hook)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Admit(%q, %v) error = %v; want %v", tt.hook, tt.callerAgent, err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.hook) {
				t.Fatalf("Admit(%q, %v) error = %q; want it to name the hook", tt.hook, tt.callerAgent, err)
			}
		})
	}
}

// TestAdmitWrongTrigger covers the sentinel Parse cannot produce in v0: every
// parsed trigger is "agent", so the only way to a non-agent Hook is to build
// the Set directly. Precondition: a Set holding one enabled hook with an
// unknown trigger value. It fails if Admit admits a hook whose trigger it does
// not recognise, which is the branch a future trigger would take.
// TestAdmitBuiltinPolicy pins that a builtin leaf's registered policy -- not
// the leaf and not the row's agent flag -- decides whether an agent may run it.
// A HumanOnly leaf is refused to an agent even when the row opts in with Agent
// true; the human still passes, and an AgentDefault leaf passes to an agent.
//
// Precondition: test.echo registered AgentDefault and test.humanonly
// HumanOnly, and rows that set Agent true. Without the policy on the parsed
// hook the HumanOnly row is admitted, which is the bug this pins.
func TestAdmitBuiltinPolicy(t *testing.T) {
	set, errs := NewSet([]Raw{
		{Name: "read", Action: `{"builtin":"test.echo"}`, Trigger: "agent", Agent: true, Enabled: true},
		{Name: "object", Action: `{"builtin":"test.humanonly"}`, Trigger: "agent", Agent: true, Enabled: true},
	})
	if len(errs) != 0 {
		t.Fatalf("NewSet errors = %v; want none", errs)
	}
	if _, err := set.Admit("object", true); !errors.Is(err, ErrHumanOnlyLeaf) {
		t.Fatalf("Admit(human-only leaf, agent) error = %v; want ErrHumanOnlyLeaf", err)
	}
	if got, err := set.Admit("object", false); err != nil {
		t.Fatalf("Admit(human-only leaf, human) error = %v; want admit", err)
	} else if got.Name != "object" {
		t.Fatalf("Admit(human-only leaf, human) = %+v; want the hook", got)
	}
	if _, err := set.Admit("read", true); err != nil {
		t.Fatalf("Admit(agent-default leaf, agent) error = %v; want admit", err)
	}
}

func TestAdmitWrongTrigger(t *testing.T) {
	set := &Set{hooks: map[string]Hook{
		"scheduled": {Name: "scheduled", Trigger: Trigger(9), Enabled: true},
	}}
	if _, err := set.Admit("scheduled", false); !errors.Is(err, ErrWrongTrigger) {
		t.Fatalf("Admit(unknown trigger) error = %v; want ErrWrongTrigger", err)
	}
}
