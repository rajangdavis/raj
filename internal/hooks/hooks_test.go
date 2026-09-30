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
	// The intent leaves are H4's; the intention-diff composite test registers
	// their names as AgentDefault stubs so the definition's shape and gate can
	// be pinned before the app-side leaves land.
	if err := builtin.Register("test.intent.resolve", testLeaf{name: "test.intent.resolve"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("test.intent.materialise", testLeaf{name: "test.intent.materialise"}, builtin.AgentDefault); err != nil {
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

// TestParseComposite pins the steps action form: an ordered list of named
// builtin steps parses into Hook.Steps, no single-action field is set, the
// order is kept, and the effective policy starts AgentDefault.
func TestParseComposite(t *testing.T) {
	h, err := Parse(Raw{Name: "git-context", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"status","builtin":"test.echo"},
			{"name":"diff","builtin":"test.echo","args":{"who":"d"}},
			{"name":"log","builtin":"test.echo","args":{"count":20}}]}`})
	if err != nil {
		t.Fatalf("Parse(composite): %v", err)
	}
	if h.Argv != nil || h.Shell != "" || h.Builtin != "" || h.BuiltinArgs != nil {
		t.Fatalf("composite set a single-action field: %+v", h)
	}
	if len(h.Steps) != 3 {
		t.Fatalf("Steps = %+v; want three", h.Steps)
	}
	for i, want := range []string{"STATUS", "DIFF", "LOG"} {
		if got := NormaliseStepName(h.Steps[i].Name); got != want {
			t.Errorf("step %d normalises to %q; want %q", i, got, want)
		}
	}
	if h.Steps[2].Args["count"] != float64(20) {
		t.Errorf("log args = %+v; want count 20", h.Steps[2].Args)
	}
	if h.BuiltinPolicy != builtin.AgentDefault {
		t.Errorf("BuiltinPolicy = %v; want AgentDefault", h.BuiltinPolicy)
	}
}

// TestCompositePolicyIsTransitive pins the gate's leaf half: a composite whose
// steps are all AgentDefault leaves is agent-callable; one that names a
// HumanOnly leaf anywhere in the chain carries HumanOnly and Admit refuses it
// to an agent however the row's Agent flag reads.
func TestCompositePolicyIsTransitive(t *testing.T) {
	set, errs := NewSet([]Raw{
		{Name: "reads", Trigger: "agent", Agent: true, Enabled: true,
			Action: `{"steps":[{"name":"a","builtin":"test.echo"},{"name":"b","builtin":"test.echo"}]}`},
		{Name: "mixed", Trigger: "agent", Agent: true, Enabled: true,
			Action: `{"steps":[{"name":"a","builtin":"test.echo"},{"name":"b","builtin":"test.humanonly"}]}`},
	})
	if len(errs) != 0 {
		t.Fatalf("NewSet errors: %v", errs)
	}
	if _, err := set.Admit("reads", true); err != nil {
		t.Fatalf("agent on an AgentDefault composite: %v; want admit", err)
	}
	if _, err := set.Admit("mixed", true); !errors.Is(err, ErrHumanOnlyLeaf) {
		t.Fatalf("agent on a composite with a HumanOnly step = %v; want ErrHumanOnlyLeaf", err)
	}
	if _, err := set.Admit("mixed", false); err != nil {
		t.Fatalf("human on a composite with a HumanOnly step: %v; want admit", err)
	}
}

// TestStepNameCollisionRejected pins the load-time collision refusal: two step
// names that normalise to the same variable are refused, naming the variable,
// because one would otherwise shadow the other's RAJ_STEP output.
func TestStepNameCollisionRejected(t *testing.T) {
	_, err := Parse(Raw{Name: "c", Trigger: "agent", Enabled: true,
		Action: `{"steps":[{"name":"git-status","builtin":"test.echo"},{"name":"git_status","builtin":"test.echo"}]}`})
	if err == nil {
		t.Fatal("Parse accepted two step names that collide after normalisation")
	}
	if !strings.Contains(err.Error(), "GIT_STATUS") {
		t.Fatalf("collision refusal = %q; want it to name the colliding variable", err)
	}
}

// TestCompositeIntentionDiff pins the intention-diff composite's shape and gate.
// The intent leaves it chains are H4's; this test registers their names as
// AgentDefault stubs only long enough to prove the definition parses, keeps its
// order, and stays agent-callable. The runtime leaves land with the app-side
// intent projection, deferred from this item.
func TestCompositeIntentionDiff(t *testing.T) {
	h, err := Parse(Raw{Name: "intention-diff", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"resolve","builtin":"test.intent.resolve"},
			{"name":"materialise","builtin":"test.intent.materialise"},
			{"name":"diff","builtin":"test.echo","args":{"base":""}}]}`})
	if err != nil {
		t.Fatalf("Parse(intention-diff): %v", err)
	}
	if len(h.Steps) != 3 || h.Steps[0].Name != "resolve" ||
		h.Steps[1].Name != "materialise" || h.Steps[2].Name != "diff" {
		t.Fatalf("intention-diff steps = %+v; want resolve, materialise, diff", h.Steps)
	}
	if h.BuiltinPolicy != builtin.AgentDefault {
		t.Fatalf("intention-diff policy = %v; want AgentDefault so an agent may run it", h.BuiltinPolicy)
	}
}

// TestParamEnvVar pins the environment mapping: the name upper-cased with every
// character that is not an ASCII letter or digit turned into an underscore.
// Precondition: none. It fails if the prefix or the normalisation drifts, which
// would silently move the variable an action reads.
func TestParamEnvVar(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"PHASE", "RAJ_PARAM_PHASE"},
		{"phase", "RAJ_PARAM_PHASE"},
		{"phase-name", "RAJ_PARAM_PHASE_NAME"},
		{"phase_name", "RAJ_PARAM_PHASE_NAME"},
		{"Attempt2", "RAJ_PARAM_ATTEMPT2"},
	} {
		if got := ParamEnvVar(tt.name); got != tt.want {
			t.Errorf("ParamEnvVar(%q) = %q; want %q", tt.name, got, tt.want)
		}
	}
}

// TestParseParamsAccepts walks the three declaration forms and the optional
// default each may carry, and pins the field every one fills. Precondition:
// none, Parse is pure. It fails if a form is refused, if the default is dropped
// or misread, or if the declaration order is not preserved.
func TestParseParamsAccepts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []Param
	}{
		{"enum required", `["PHASE=enum(check,race)"]`, []Param{
			{Name: "PHASE", Kind: ParamEnum, Enum: []string{"check", "race"}, Decl: "PHASE=enum(check,race)"}}},
		{"enum default", `["PHASE=enum(check,race)=check"]`, []Param{
			{Name: "PHASE", Kind: ParamEnum, Enum: []string{"check", "race"},
				Default: "check", HasDefault: true, Decl: "PHASE=enum(check,race)=check"}}},
		{"string required", `["TAG=string(^v[0-9]+$)"]`, []Param{
			{Name: "TAG", Kind: ParamString, Regex: "^v[0-9]+$", Decl: "TAG=string(^v[0-9]+$)"}}},
		{"string default with groups", `["TAG=string((a|b)+)=ab"]`, []Param{
			{Name: "TAG", Kind: ParamString, Regex: "(a|b)+",
				Default: "ab", HasDefault: true, Decl: "TAG=string((a|b)+)=ab"}}},
		{"uint required", `["N=uint"]`, []Param{
			{Name: "N", Kind: ParamUint, Decl: "N=uint"}}},
		{"uint default", `["N=uint=7"]`, []Param{
			{Name: "N", Kind: ParamUint, Default: "7", HasDefault: true, Decl: "N=uint=7"}}},
		{"order preserved", `["B=uint","A=enum(x,y)=x"]`, []Param{
			{Name: "B", Kind: ParamUint, Decl: "B=uint"},
			{Name: "A", Kind: ParamEnum, Enum: []string{"x", "y"},
				Default: "x", HasDefault: true, Decl: "A=enum(x,y)=x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := Parse(Raw{Name: "h", Action: `["true"]`, Params: tt.raw, Trigger: "agent", Enabled: true})
			if err != nil {
				t.Fatalf("Parse(params %s) error: %v; want accept", tt.raw, err)
			}
			if !reflect.DeepEqual(h.Params, tt.want) {
				t.Fatalf("Params = %+v; want %+v", h.Params, tt.want)
			}
		})
	}
}

// TestParseParamsEmpty pins backward compatibility: a row with no params field,
// an empty array or null all parse as a hook that takes no parameters, so every
// stored row and every existing caller behaves exactly as before.
func TestParseParamsEmpty(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", "  "} {
		h, err := Parse(Raw{Name: "h", Action: `["true"]`, Params: raw, Trigger: "agent", Enabled: true})
		if err != nil {
			t.Fatalf("Parse(Params %q) error: %v; want no parameters", raw, err)
		}
		if h.Params != nil {
			t.Fatalf("Parse(Params %q) = %+v; want none", raw, h.Params)
		}
	}
}

// TestParseParamsRefuses is the malformed-declaration table. Precondition:
// none. It fails if a bad declaration is accepted, which would store a domain
// the runner cannot enforce, or if the refusal stops naming the declaration.
func TestParseParamsRefuses(t *testing.T) {
	tests := []struct {
		name    string
		params  string
		wantSub string
	}{
		{"bad type", `["X=int"]`, "type"},
		{"empty name", `["=enum(a)"]`, "NAME=type"},
		{"bad name", `["X Y=uint"]`, "name"},
		{"empty enum", `["X=enum()"]`, "empty"},
		{"empty enum member", `["X=enum(a,,b)"]`, "empty"},
		{"missing paren", `["X=enum(a"]`, "missing )"},
		{"invalid regex", `["X=string(a**b)"]`, "does not compile"},
		{"empty regex", `["X=string()"]`, "must not be empty"},
		{"duplicate name", `["X=uint","X=uint"]`, "both export"},
		{"colliding names", `["phase-name=uint","PHASE_NAME=uint"]`, "both export"},
		{"non-uint default", `["X=uint=abc"]`, "default"},
		{"enum default outside", `["X=enum(a,b)=c"]`, "default"},
		{"string default mismatch", `["X=string(^a$)=b"]`, "default"},
		{"not a json array", `X=uint`, "JSON array"},
		{"scalar json", `"X=uint"`, "JSON array"},
		{"trailing junk", `["X=uint=7junk"]`, "default"},
		{"empty declaration", `[""]`, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(Raw{Name: "h", Action: `["true"]`, Params: tt.params, Trigger: "agent", Enabled: true})
			if err == nil {
				t.Fatalf("Parse(Params %s) = %+v; want an error", tt.params, got)
			}
			if tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Parse(Params %s) error = %q; want it to contain %q", tt.params, err, tt.wantSub)
			}
		})
	}
}

// TestResolveParams pins the run-time resolution: every value comes from the
// declared domain, a missing optional parameter takes its default, an explicit
// value overrides it, and the output is in declaration order with the env name
// the action will read.
func TestResolveParams(t *testing.T) {
	h, err := Parse(Raw{Name: "cycle", Action: `["true"]`, Trigger: "agent", Enabled: true,
		Params: `["PHASE=enum(check,race)=check","TAG=string(^v[0-9]+$)","N=uint"]`})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := h.ResolveParams([]string{"PHASE=race", "TAG=v2", "N=07"})
	if err != nil {
		t.Fatalf("ResolveParams valid: %v", err)
	}
	want := []ParamValue{
		{Name: "PHASE", Value: "race", Env: "RAJ_PARAM_PHASE"},
		{Name: "TAG", Value: "v2", Env: "RAJ_PARAM_TAG"},
		{Name: "N", Value: "07", Env: "RAJ_PARAM_N"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveParams = %+v; want %+v", got, want)
	}
	// The omitted optional parameter takes its default.
	got, err = h.ResolveParams([]string{"TAG=v1", "N=3"})
	if err != nil {
		t.Fatalf("ResolveParams default: %v", err)
	}
	if got[0].Name != "PHASE" || got[0].Value != "check" {
		t.Fatalf("default PHASE = %+v; want check", got[0])
	}
	// An explicit value overrides the default.
	got, err = h.ResolveParams([]string{"PHASE=race", "TAG=v1", "N=3"})
	if err != nil {
		t.Fatalf("ResolveParams override: %v", err)
	}
	if got[0].Value != "race" {
		t.Fatalf("explicit PHASE = %q; want race", got[0].Value)
	}
	// ParamEnv renders the same values as process-environment entries.
	env := ParamEnv(got)
	if len(env) != 3 || env[0] != "RAJ_PARAM_PHASE=race" || env[2] != "RAJ_PARAM_N=3" {
		t.Fatalf("ParamEnv = %q; want the resolved entries", env)
	}
}

// TestResolveParamsRefuses pins every run-time refusal: an undeclared name, a
// value outside its domain, and a missing required parameter. Each must be an
// error so the caller never starts the action. The refusal echoes the
// declaration so the caller learns the domain it must choose from.
func TestResolveParamsRefuses(t *testing.T) {
	h, err := Parse(Raw{Name: "cycle", Action: `["true"]`, Trigger: "agent", Enabled: true,
		Params: `["PHASE=enum(check,race)=check","TAG=string(^v[0-9]+$)","N=uint"]`})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"undeclared name", []string{"TAG=v1", "N=1", "WHAT=x"}, "no parameter"},
		{"enum outside", []string{"PHASE=deploy", "TAG=v1", "N=1"}, "enum(check,race)"},
		{"regex mismatch", []string{"TAG=nope", "N=1"}, "string(^v[0-9]+$)"},
		{"uint not a number", []string{"TAG=v1", "N=-1"}, "uint"},
		{"missing required", []string{"PHASE=race"}, "requires parameter"},
		{"duplicate supplied", []string{"PHASE=check", "PHASE=race", "TAG=v1", "N=1"}, "more than once"},
		{"malformed assignment", []string{"PHASE"}, "NAME=value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.ResolveParams(tt.in)
			if err == nil {
				t.Fatalf("ResolveParams(%q) = %+v; want a refusal", tt.in, got)
			}
			if got != nil {
				t.Fatalf("ResolveParams(%q) returned %+v alongside an error; want none", tt.in, got)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolveParams(%q) error = %q; want it to contain %q", tt.in, err, tt.want)
			}
		})
	}
}
