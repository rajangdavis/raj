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
	"regexp"
	"sort"
	"strconv"
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

// ParamKind is a declared parameter's value domain. A parameter's kind is
// fixed when the hook is authored; a caller supplies only a value from it.
type ParamKind uint8

const (
	// ParamEnum is a closed set of allowed values, written enum(a,b,c).
	ParamEnum ParamKind = iota
	// ParamString is any value matching a regular expression, written
	// string(<pattern>).
	ParamString
	// ParamUint is an unsigned decimal integer, written uint.
	ParamUint
)

// String names the kind as it appears in a declaration.
func (k ParamKind) String() string {
	switch k {
	case ParamEnum:
		return "enum"
	case ParamString:
		return "string"
	case ParamUint:
		return "uint"
	default:
		return fmt.Sprintf("param(%d)", uint8(k))
	}
}

// Param is one declared parameter: a name and the closed domain a value must
// come from. Default and HasDefault make a parameter optional: a run that does
// not name it uses Default, so an argument-less caller such as a gate keeps
// working. A parameter without a default is required.
type Param struct {
	Name       string
	Kind       ParamKind
	Enum       []string // ParamEnum: the allowed values, in declaration order
	Regex      string   // ParamString: the pattern text
	Default    string   // the value a run picks when it names none
	HasDefault bool
	Decl       string // the declaration as written, echoed in a refusal
}

// Raw is one stored hook as the store carries it, before validation.
type Raw struct {
	Name   string
	Action string // JSON: an argv array, {"shell": "..."} or {"builtin": "...", "args": {...}}
	// Params is the JSON array of declared parameters, one declaration per
	// entry: NAME=enum(a,b,c), NAME=string(<regex>) or NAME=uint, each with an
	// optional =default suffix that makes it optional at run time. Empty or
	// "[]" means the hook takes none.
	Params     string
	Trigger    string
	Tree       string
	Agent      bool
	CooldownMS int
	TimeoutMS  int
	MayWrite   bool
	Detach     bool
	Enabled    bool
}

// Step is one action of a composite hook: a builtin leaf or a shell action.
// Exactly one of Builtin and Shell is set. Name labels the step and becomes its
// environment variable, RAJ_STEP_<normalised name>_OUT; Policy is the step
// leaf's registered policy, and it is what makes the transitive gate work.
type Step struct {
	Name    string
	Builtin string
	Args    map[string]any
	Shell   string
	Policy  builtin.Policy
}

// Hook is a validated definition. Exactly one of Argv, Shell, Builtin or a
// non-empty Steps is set.
type Hook struct {
	Name        string
	Argv        []string
	Shell       string
	Builtin     string
	BuiltinArgs map[string]any
	// Steps is the ordered steps of a composite action. BuiltinPolicy carries
	// the composite's effective policy: HumanOnly when any step's leaf is, so
	// Admit refuses the whole chain to an agent on the strength of one step.
	Steps         []Step
	BuiltinPolicy builtin.Policy
	Trigger       Trigger
	Tree          Tree
	Agent         bool
	Cooldown      time.Duration
	Timeout       time.Duration
	MayWrite      bool
	Detach        bool
	Enabled       bool
	// Params are the hook's declared parameters, in declaration order; empty
	// means the hook takes none. A parameter with a default is optional at run
	// time, so a name-only caller such as an automatic gate keeps working.
	Params []Param
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
	params, err := parseParams(r.Params)
	if err != nil {
		return Hook{}, err
	}
	if (act.builtin != "" || len(act.steps) > 0) && r.Detach {
		return Hook{}, errors.New("an in-process action cannot be detached")
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
		Steps:         act.steps,
		BuiltinPolicy: act.policy,
		Trigger:       trigger,
		Tree:          tree,
		Agent:         r.Agent,
		Cooldown:      time.Duration(r.CooldownMS) * time.Millisecond,
		Timeout:       time.Duration(r.TimeoutMS) * time.Millisecond,
		MayWrite:      r.MayWrite,
		Detach:        r.Detach,
		Enabled:       r.Enabled,
		Params:        params,
	}, nil
}

// action is one parsed Action: exactly one of argv, shell, builtin or steps
// is set.
type action struct {
	argv    []string
	shell   string
	builtin string
	args    map[string]any
	steps   []Step
	policy  builtin.Policy
}

// errActionKind is the one refusal for an object that is not exactly one action
// kind, so the no-kind and two-kind cases read the same.
var errActionKind = errors.New(`action object must have exactly one non-empty "shell", "builtin" or "steps" key`)

// parseAction splits a stored Action into its one kind. The array form is a
// non-empty argv whose every element is a non-empty string. The object form is
// exactly one of {"shell": "..."}, {"builtin": "<name>", "args": {...}} and
// {"steps": [...]}: a non-empty shell, a builtin name this build's registry
// knows plus an optional JSON object of args, or a non-empty ordered step list
// parseSteps validates. Anything else is refused: invalid JSON, an empty array,
// a non-string or empty element, an object with no kind or with two, an extra
// key, an empty shell, an unknown builtin name, args that are not an object, a
// malformed step, and any top-level scalar.
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
		if raw, ok := obj["steps"]; ok {
			if len(obj) != 1 {
				return action{}, errActionKind
			}
			steps, err := parseSteps(raw)
			if err != nil {
				return action{}, err
			}
			return action{steps: steps, policy: compositePolicy(steps)}, nil
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

// StepOutVar is the environment variable a step's output is exported under:
// RAJ_STEP_ + the step name normalised by NormaliseStepName, + "_OUT".
func StepOutVar(name string) string {
	return "RAJ_STEP_" + NormaliseStepName(name) + "_OUT"
}

// StepOutFileVar is the environment variable a spilled step's output file path
// is exported under: the step's OUT variable with an "_FILE" suffix.
func StepOutFileVar(name string) string {
	return "RAJ_STEP_" + NormaliseStepName(name) + "_OUT_FILE"
}

// NormaliseStepName maps a step name to the variable fragment it exports:
// uppercase, with every character that is not an ASCII letter or digit turned
// into an underscore. Two names that normalise alike would write the same
// variable, so Parse refuses the pair rather than letting one shadow another.
func NormaliseStepName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteByte(byte(r - 'a' + 'A'))
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteByte(byte(r))
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ParamEnvVar is the environment variable a resolved parameter is delivered
// under: RAJ_PARAM_ + the name upper-cased with every character that is not an
// ASCII letter or digit turned into an underscore.
func ParamEnvVar(name string) string {
	return "RAJ_PARAM_" + NormaliseStepName(name)
}

// ParamValue is one resolved parameter: the name and value a run supplied or
// inherited from its default, and the environment variable it was delivered
// under. The run record carries these so a run is auditable and a later pin can
// pin the values it ran with.
type ParamValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Env   string `json:"env"`
}

// ParamEnv renders resolved parameters as the "KEY=value" entries os/exec
// takes, so one slice serves a process environment and a builtin leaf's
// in-process channel alike.
func ParamEnv(vals []ParamValue) []string {
	env := make([]string, 0, len(vals))
	for _, v := range vals {
		env = append(env, v.Env+"="+v.Value)
	}
	return env
}

// ResolveParams validates the NAME=value assignments a caller supplied against
// the declared parameters and returns every parameter's resolved value in
// declaration order: the supplied value, or the declared default for a
// parameter the caller omitted. An unknown name, a duplicate, a missing
// required parameter and a value outside its domain are refusals; a caller
// must not run the action when one is returned.
func (h Hook) ResolveParams(supplied []string) ([]ParamValue, error) {
	given := make(map[string]string, len(supplied))
	for _, a := range supplied {
		name, value, ok := strings.Cut(a, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("hook %q: parameter %q must be NAME=value", h.Name, a)
		}
		declared := h.param(name)
		if declared == nil {
			return nil, fmt.Errorf("hook %q has no parameter %q", h.Name, name)
		}
		if _, dup := given[declared.Name]; dup {
			return nil, fmt.Errorf("hook %q: parameter %q given more than once", h.Name, declared.Name)
		}
		if err := validateParamValue(*declared, value); err != nil {
			return nil, fmt.Errorf("hook %q: parameter %q: %w (declared %s)", h.Name, declared.Name, err, declared.Decl)
		}
		given[declared.Name] = value
	}
	out := make([]ParamValue, 0, len(h.Params))
	for _, p := range h.Params {
		value, ok := given[p.Name]
		if !ok {
			if !p.HasDefault {
				return nil, fmt.Errorf("hook %q requires parameter %q (declared %s; add a default to make it optional)", h.Name, p.Name, p.Decl)
			}
			value = p.Default
		}
		out = append(out, ParamValue{Name: p.Name, Value: value, Env: ParamEnvVar(p.Name)})
	}
	return out, nil
}

// param finds the declared parameter named name, or nil.
func (h Hook) param(name string) *Param {
	for i := range h.Params {
		if h.Params[i].Name == name {
			return &h.Params[i]
		}
	}
	return nil
}

// parseParams validates a stored declaration list. Empty or null is a hook
// that takes no parameters, which is every row written before the field
// existed, so it parses as none rather than an error.
func parseParams(text string) ([]Param, error) {
	s := strings.TrimSpace(text)
	if s == "" || s == "null" {
		return nil, nil
	}
	var decls []string
	if err := json.Unmarshal([]byte(s), &decls); err != nil {
		return nil, fmt.Errorf("params must be a JSON array of declarations: %w", err)
	}
	if len(decls) == 0 {
		return nil, nil
	}
	params := make([]Param, 0, len(decls))
	seen := make(map[string]string, len(decls))
	for i, decl := range decls {
		p, err := parseParamDecl(decl)
		if err != nil {
			return nil, fmt.Errorf("param[%d]: %w", i, err)
		}
		norm := NormaliseStepName(p.Name)
		if prev, dup := seen[norm]; dup {
			return nil, fmt.Errorf("params %q and %q both export %s", prev, p.Name, ParamEnvVar(p.Name))
		}
		seen[norm] = p.Name
		params = append(params, p)
	}
	return params, nil
}

// parseParamDecl parses one declaration: NAME=enum(a,b,c), NAME=string(<re>),
// NAME=uint, each with an optional "=<default>" suffix that makes the
// parameter optional. The type body is scanned to its balanced closing paren
// rather than cut at the first one, so a string pattern may contain groups.
func parseParamDecl(decl string) (Param, error) {
	text := strings.TrimSpace(decl)
	if text == "" {
		return Param{}, errors.New("declaration must not be empty")
	}
	name, rest, ok := strings.Cut(text, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return Param{}, fmt.Errorf("declaration %q must be NAME=type", decl)
	}
	if !validParamName(name) {
		return Param{}, fmt.Errorf("declaration %q: name %q must be letters, digits, _ or -", decl, name)
	}
	p := Param{Name: name, Decl: text}
	var body, tail string
	switch {
	case rest == "uint" || strings.HasPrefix(rest, "uint="):
		p.Kind = ParamUint
		tail = rest[len("uint"):]
	case strings.HasPrefix(rest, "enum("):
		p.Kind = ParamEnum
		var found bool
		body, tail, found = scanParamBody(rest[len("enum("):])
		if !found {
			return Param{}, fmt.Errorf("declaration %q: missing )", decl)
		}
		for _, m := range strings.Split(body, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				return Param{}, fmt.Errorf("declaration %q: enum must not have an empty value", decl)
			}
			p.Enum = append(p.Enum, m)
		}
	case strings.HasPrefix(rest, "string("):
		p.Kind = ParamString
		var found bool
		body, tail, found = scanParamBody(rest[len("string("):])
		if !found {
			return Param{}, fmt.Errorf("declaration %q: missing )", decl)
		}
		if strings.TrimSpace(body) == "" {
			return Param{}, fmt.Errorf("declaration %q: string pattern must not be empty", decl)
		}
		if _, err := regexp.Compile(body); err != nil {
			return Param{}, fmt.Errorf("declaration %q: pattern does not compile: %v", decl, err)
		}
		p.Regex = body
	default:
		return Param{}, fmt.Errorf("declaration %q: type must be enum(...), string(...) or uint", decl)
	}
	if tail != "" {
		if !strings.HasPrefix(tail, "=") {
			return Param{}, fmt.Errorf("declaration %q: unexpected %q after the type", decl, tail)
		}
		p.Default, p.HasDefault = tail[1:], true
	}
	if p.HasDefault {
		if err := validateParamValue(p, p.Default); err != nil {
			return Param{}, fmt.Errorf("declaration %q: default %w", decl, err)
		}
	}
	return p, nil
}

// scanParamBody finds the ) that closes a param type body, honouring balanced
// parentheses and a regular expression character class, so a pattern may carry
// its own groups. s starts just after the opening paren; tail is what follows
// the closing one, empty or a leading =default.
func scanParamBody(s string) (body, tail string, ok bool) {
	depth := 1
	inClass := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++ // an escaped byte is a literal, a close paren included
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			// A class may open with ^ and then a literal ].
			j := i + 1
			if j < len(s) && s[j] == '^' {
				j++
			}
			if j < len(s) && s[j] == ']' {
				i = j
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}

// validParamName reports whether a declaration name is one the env mapping can
// carry predictably: letters, digits, _ and -.
func validParamName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validateParamValue reports whether v is in p's declared domain. The error
// names the domain so a refusal can echo the declaration.
func validateParamValue(p Param, v string) error {
	switch p.Kind {
	case ParamEnum:
		for _, m := range p.Enum {
			if m == v {
				return nil
			}
		}
		return fmt.Errorf("value %q is not one of %s", v, p.domain())
	case ParamString:
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return fmt.Errorf("pattern %q does not compile: %v", p.Regex, err)
		}
		if !re.MatchString(v) {
			return fmt.Errorf("value %q does not match %s", v, p.domain())
		}
		return nil
	case ParamUint:
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			return fmt.Errorf("value %q is not a uint", v)
		}
		return nil
	default:
		return fmt.Errorf("value %q has an unknown type", v)
	}
}

// domain renders the value domain for a refusal: enum(...), string(...) or
// uint, without the name or default.
func (p Param) domain() string {
	switch p.Kind {
	case ParamEnum:
		return "enum(" + strings.Join(p.Enum, ",") + ")"
	case ParamString:
		return "string(" + p.Regex + ")"
	default:
		return "uint"
	}
}

// compositePolicy is a composite's effective leaf policy: HumanOnly when any
// step's leaf is, so a chain can never be more agent-callable than its most
// restricted part.
func compositePolicy(steps []Step) builtin.Policy {
	for _, s := range steps {
		if s.Policy == builtin.HumanOnly {
			return builtin.HumanOnly
		}
	}
	return builtin.AgentDefault
}

// parseSteps validates a composite's ordered step list. It refuses an empty
// list, a malformed step, and two step names whose normalised forms collide.
func parseSteps(raw json.RawMessage) ([]Step, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("action steps must be a JSON array of step objects")
	}
	if len(list) == 0 {
		return nil, errors.New("action steps must not be empty")
	}
	steps := make([]Step, 0, len(list))
	seen := make(map[string]string, len(list))
	for i, item := range list {
		step, err := parseStep(item)
		if err != nil {
			return nil, fmt.Errorf("action steps[%d]: %w", i, err)
		}
		norm := NormaliseStepName(step.Name)
		if prev, dup := seen[norm]; dup {
			return nil, fmt.Errorf("action steps[%d]: step names %q and %q both export RAJ_STEP_%s_OUT", i, prev, step.Name, norm)
		}
		seen[norm] = step.Name
		steps = append(steps, step)
	}
	return steps, nil
}

// parseStep validates one composite step: a name, and exactly one of a
// registered builtin leaf or a non-empty shell string. args is builtin-only.
func parseStep(raw json.RawMessage) (Step, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return Step{}, errors.New("step must be a JSON object")
	}
	for k := range obj {
		switch k {
		case "name", "builtin", "args", "shell":
		default:
			return Step{}, fmt.Errorf("step has unknown key %q", k)
		}
	}
	nameRaw, ok := obj["name"]
	if !ok {
		return Step{}, errors.New(`step needs a "name"`)
	}
	var name string
	if err := json.Unmarshal(nameRaw, &name); err != nil {
		return Step{}, errors.New("step name must be a string")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Step{}, errors.New("step name must not be empty")
	}
	step := Step{Name: name}
	hasBuiltin := false
	if raw, ok := obj["builtin"]; ok {
		var leaf *string
		if err := json.Unmarshal(raw, &leaf); err != nil {
			return Step{}, fmt.Errorf("step %q: builtin must be a string", name)
		}
		if leaf == nil || strings.TrimSpace(*leaf) == "" {
			return Step{}, fmt.Errorf("step %q: builtin must not be empty", name)
		}
		_, policy, found := builtin.Lookup(*leaf)
		if !found {
			return Step{}, fmt.Errorf("step %q: builtin %q is not registered", name, *leaf)
		}
		step.Builtin, step.Policy, hasBuiltin = *leaf, policy, true
	}
	hasShell := false
	if raw, ok := obj["shell"]; ok {
		var shell *string
		if err := json.Unmarshal(raw, &shell); err != nil {
			return Step{}, fmt.Errorf("step %q: shell must be a string", name)
		}
		if shell == nil || strings.TrimSpace(*shell) == "" {
			return Step{}, fmt.Errorf("step %q: shell must not be empty", name)
		}
		step.Shell, hasShell = *shell, true
	}
	if hasBuiltin == hasShell {
		return Step{}, fmt.Errorf(`step %q: needs exactly one of "builtin" or "shell"`, name)
	}
	if raw, ok := obj["args"]; ok {
		if !hasBuiltin {
			return Step{}, fmt.Errorf(`step %q: "args" is only for a builtin step`, name)
		}
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil || args == nil {
			return Step{}, fmt.Errorf("step %q: args must be a JSON object", name)
		}
		step.Args = args
	}
	return step, nil
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
