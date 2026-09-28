// Package hooks holds the pure hooks domain: how a stored definition is parsed
// and validated, whether a caller may run one, the in-memory gate that decides
// when it may start, and the registry of runs already in flight.
//
// Nothing here touches the store, the control surface or git, and the package
// imports only the standard library and its own builtin leaf registry, so the
// policy layer stays decoupled from the machinery that carries it.
// internal/control and internal/app import it to carry the policy to the
// surface; nothing here imports them back.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"raj/internal/hooks/builtin"
)

// DefaultTimeout is the run timeout a zero Raw.TimeoutMS means. The runner
// applies it, not Parse: Parse keeps zero as "the runner's default" so the
// policy that chooses one lives with the code that enforces it.
const DefaultTimeout = 2 * time.Minute

// Trigger names when a hook runs. v0 ships agent only.
type Trigger uint8

const (
	TriggerAgent Trigger = iota
)

// String names the trigger. A value this build does not know renders as its
// number, so a trigger added by a later version stays distinguishable rather
// than reading as "agent".
func (t Trigger) String() string {
	switch t {
	case TriggerAgent:
		return "agent"
	default:
		return fmt.Sprintf("trigger(%d)", uint8(t))
	}
}

// Tree names where a hook's command runs. It is a closed set: a value the
// runner cannot act on exactly is refused rather than guessed at.
type Tree string

const (
	// TreeProjected runs against a scratch tree materialised from the live
	// composition. It is the default and the v0 behaviour.
	TreeProjected Tree = "projected"
	// TreeWorkspace runs against the saved workspace root itself, and only
	// when it is ready: nothing unsaved and no pending change set to miss.
	TreeWorkspace Tree = "workspace"
)

// Raw is one stored hook as the store carries it, before validation.
type Raw struct {
	Name       string
	Action     string // JSON: an argv array, {"shell": "..."} or {"builtin": "...", "args": {...}}
	Trigger    string
	Tree       string
	Agent      bool
	CooldownMS int
	TimeoutMS  int
	MayWrite   bool
	Detach     bool
	Enabled    bool
}

// Hook is a validated definition. Exactly one of Argv, Shell or Builtin is set.
type Hook struct {
	Name          string
	Argv          []string
	Shell         string
	Builtin       string
	BuiltinArgs   map[string]any
	BuiltinPolicy builtin.Policy
	Trigger       Trigger
	Tree          Tree
	Agent         bool
	Cooldown      time.Duration
	Timeout       time.Duration
	MayWrite      bool
	Detach        bool
	Enabled       bool
}

// Parse validates one raw hook into a definition. Every refusal is an error: a
// hook is policy that runs on the host, so a field the runner cannot act on
// exactly is refused rather than guessed at. Durations are whole milliseconds.
func Parse(r Raw) (Hook, error) {
	if r.Name == "" {
		return Hook{}, errors.New("hook name must not be empty")
	}
	act, err := parseAction(r.Action)
	if err != nil {
		return Hook{}, err
	}
	if act.builtin != "" && r.Detach {
		return Hook{}, errors.New("a builtin action cannot be detached")
	}
	trigger, err := parseTrigger(r.Trigger)
	if err != nil {
		return Hook{}, err
	}
	tree, err := parseTree(r.Tree)
	if err != nil {
		return Hook{}, err
	}
	if r.CooldownMS < 0 {
		return Hook{}, fmt.Errorf("cooldown %dms must not be negative", r.CooldownMS)
	}
	if r.TimeoutMS < 0 {
		return Hook{}, fmt.Errorf("timeout %dms must not be negative", r.TimeoutMS)
	}
	return Hook{
		Name:          r.Name,
		Argv:          act.argv,
		Shell:         act.shell,
		Builtin:       act.builtin,
		BuiltinArgs:   act.args,
		BuiltinPolicy: act.policy,
		Trigger:       trigger,
		Tree:          tree,
		Agent:         r.Agent,
		Cooldown:      time.Duration(r.CooldownMS) * time.Millisecond,
		Timeout:       time.Duration(r.TimeoutMS) * time.Millisecond,
		MayWrite:      r.MayWrite,
		Detach:        r.Detach,
		Enabled:       r.Enabled,
	}, nil
}

// action is one parsed Action: exactly one of argv, shell or builtin is set.
type action struct {
	argv    []string
	shell   string
	builtin string
	args    map[string]any
	policy  builtin.Policy
}

// errActionKind is the one refusal for an object that is not exactly one action
// kind, so the no-kind and two-kind cases read the same.
var errActionKind = errors.New(`action object must have exactly one non-empty "shell" or "builtin" key`)

// parseAction splits a stored Action into its one kind. The array form is a
// non-empty argv whose every element is a non-empty string. The object form is
// exactly one of {"shell": "..."} and {"builtin": "<name>", "args": {...}}: a
// non-empty shell, or a builtin name this build's registry knows plus an
// optional JSON object of args. Anything else is refused: invalid JSON, an
// empty array, a non-string or empty element, an object with no kind or with
// two, an extra key, an empty shell, an unknown builtin name, args that are not
// an object, and any top-level scalar.
func parseAction(text string) (action, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return action{}, errors.New("action must not be empty")
	}
	switch s[0] {
	case '[':
		var argv []string
		if err := json.Unmarshal([]byte(s), &argv); err != nil {
			return action{}, fmt.Errorf("action: %w", err)
		}
		if err := checkArgv(argv); err != nil {
			return action{}, err
		}
		return action{argv: argv}, nil
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return action{}, fmt.Errorf("action: %w", err)
		}
		if raw, ok := obj["shell"]; ok {
			if len(obj) != 1 {
				return action{}, errActionKind
			}
			var shell *string
			if err := json.Unmarshal(raw, &shell); err != nil {
				return action{}, fmt.Errorf("action shell must be a string: %w", err)
			}
			if shell == nil {
				return action{}, errActionKind
			}
			if strings.TrimSpace(*shell) == "" {
				return action{}, errors.New("action shell must not be empty")
			}
			return action{shell: *shell}, nil
		}
		raw, ok := obj["builtin"]
		if !ok {
			return action{}, errActionKind
		}
		for k := range obj {
			if k != "builtin" && k != "args" {
				return action{}, errActionKind
			}
		}
		var name *string
		if err := json.Unmarshal(raw, &name); err != nil {
			return action{}, fmt.Errorf("action builtin must be a string: %w", err)
		}
		if name == nil || strings.TrimSpace(*name) == "" {
			return action{}, errors.New("action builtin must not be empty")
		}
		_, policy, ok := builtin.Lookup(*name)
		if !ok {
			return action{}, fmt.Errorf("action builtin %q is not registered", *name)
		}
		var args map[string]any
		if rawArgs, ok := obj["args"]; ok && string(rawArgs) != "null" {
			if err := json.Unmarshal(rawArgs, &args); err != nil || args == nil {
				return action{}, errors.New("action builtin args must be a JSON object")
			}
		}
		return action{builtin: *name, args: args, policy: policy}, nil
	default:
		return action{}, errors.New("action must be a JSON array of strings or an object")
	}
}

// checkArgv refuses an empty argv and any empty element. A non-string element
// never reaches here: the JSON decode into []string already refused it.
func checkArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("action argv must not be empty")
	}
	for i, a := range argv {
		if a == "" {
			return fmt.Errorf("action argv[%d] must not be empty", i)
		}
	}
	return nil
}

// parseTrigger maps a stored trigger name to a Trigger. Only "agent" exists in
// v0, and the offending string is named so a bad row is fixable.
func parseTrigger(s string) (Trigger, error) {
	switch s {
	case "agent":
		return TriggerAgent, nil
	default:
		return 0, fmt.Errorf("unknown trigger %q", s)
	}
}

// parseTree maps a stored tree name to a Tree. An empty value is the default a
// row written before the field existed carries, so it parses as projected; any
// other unknown value is refused and named, so a bad row is fixable rather than
// silently run somewhere the author did not choose.
func parseTree(s string) (Tree, error) {
	switch s {
	case "", string(TreeProjected):
		return TreeProjected, nil
	case string(TreeWorkspace):
		return TreeWorkspace, nil
	default:
		return "", fmt.Errorf("unknown tree %q", s)
	}
}

// Set is the validated hooks of one workspace, keyed by name.
type Set struct {
	hooks map[string]Hook
}

// NewSet validates raws. Invalid rows are skipped and returned as errors, one
// per row and each naming its hook, so one bad row does not brick the
// workspace. A later valid row whose name an earlier valid row used replaces
// it, matching the store's key-by-name model.
func NewSet(raws []Raw) (*Set, []error) {
	s := &Set{hooks: make(map[string]Hook, len(raws))}
	var errs []error
	for _, r := range raws {
		h, err := Parse(r)
		if err != nil {
			errs = append(errs, fmt.Errorf("hook %q: %w", r.Name, err))
			continue
		}
		s.hooks[h.Name] = h
	}
	return s, errs
}

// Get returns the hook named name and whether it is present.
func (s *Set) Get(name string) (Hook, bool) {
	h, ok := s.hooks[name]
	return h, ok
}

// All returns every valid hook, sorted by name. It is never nil.
func (s *Set) All() []Hook {
	out := make([]Hook, 0, len(s.hooks))
	for _, h := range s.hooks {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// The sentinel refusals Admit returns, each matched with errors.Is. They are
// separate values so a caller can tell "there is no such hook" from "there is
// one and its policy refuses", and decide whether to report or to create it.
var (
	ErrNoHook           = errors.New("no such hook")
	ErrDisabled         = errors.New("hook is disabled")
	ErrWrongTrigger     = errors.New("hook does not have the agent trigger")
	ErrNotAgentCallable = errors.New("hook is not callable by an agent")
	ErrHumanOnlyLeaf    = errors.New("hook action is human-only and not callable by an agent")
)

// Admit reports whether name may run for a caller of the given kind. The hook
// must exist, be enabled and carry TriggerAgent; when callerAgent is true the
// hook's Agent flag must be set, and a builtin action whose leaf registered
// HumanOnly is refused whatever that flag says. The policy lives on the leaf's
// registration, never on the leaf, so a leaf cannot admit itself. The human
// (callerAgent false) may run any enabled agent-trigger hook. The returned
// error wraps the hook name and one of the sentinels above.
func (s *Set) Admit(name string, callerAgent bool) (Hook, error) {
	h, ok := s.hooks[name]
	if !ok {
		return Hook{}, fmt.Errorf("%w: %q", ErrNoHook, name)
	}
	if !h.Enabled {
		return Hook{}, fmt.Errorf("%w: %q", ErrDisabled, name)
	}
	if h.Trigger != TriggerAgent {
		return Hook{}, fmt.Errorf("%w: %q", ErrWrongTrigger, name)
	}
	if callerAgent && h.BuiltinPolicy == builtin.HumanOnly {
		return Hook{}, fmt.Errorf("%w: %q", ErrHumanOnlyLeaf, name)
	}
	if callerAgent && !h.Agent {
		return Hook{}, fmt.Errorf("%w: %q", ErrNotAgentCallable, name)
	}
	return h, nil
}
