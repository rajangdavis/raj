package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

// Code actions. A server answers textDocument/codeAction with the fixes and
// refactors that apply to a range: a quick fix carries the diagnostic it
// repairs, a refactor carries an edit, and a source action can carry a command
// instead. The result is the protocol's most heterogeneous, so decoding here is
// deliberately permissive about which of the three shapes an entry takes and
// strict about meaning: an entry that can be neither applied nor executed is
// still kept, because refusing it out loud is better than dropping it silently.

// Command is a server command: an opaque identifier the server understands and
// the arguments to pass it. A Command can be a code action's whole result (the
// legacy shape) or a step a CodeAction names after its edit has been applied.
type Command struct {
	Title     string            `json:"title"`
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

// A CodeAction's edit is a WorkspaceEdit, which rename.go already defines and
// decodes: textDocument/rename and textDocument/codeAction return the same
// structure, so one decoder owns both. The merge of changes and
// documentChanges, the deterministic order, and the resource-operation refusal
// all live there; a code action that asks for a file operation is refused whole
// by the caller for the same reason a rename is.

// CodeAction is one action a server offers for a range. Which of Edit, Command
// and Data is set decides how the action is carried out, and all three can be
// absent for a server that expects a codeAction/resolve round-trip;
// NeedsResolve names that case so the caller resolves it first.
type CodeAction struct {
	Title string
	Kind  string
	// Diagnostics are the problems this action is meant to fix, as the server
	// names them, so an action's own context can be carried back to a resolve.
	Diagnostics []Diagnostic
	// IsPreferred is the server's ranking hint. It is kept for a caller that
	// wants to surface it; raj does not apply a preferred fix automatically.
	IsPreferred bool
	Edit        *WorkspaceEdit
	Command     *Command
	// Data is the server's opaque payload for a later codeAction/resolve. It is
	// kept so a resolve-only action can be sent back whole and, until then,
	// named rather than mistaken for an empty one.
	Data json.RawMessage
}

// NeedsResolve reports whether the action carries nothing raj can act on: no
// edit to apply and no command to run. It is the codeAction/resolve case, so
// the caller resolves it through codeAction/resolve before applying anything.
func (c CodeAction) NeedsResolve() bool {
	return c.Edit == nil && c.Command == nil
}

// CodeActionResolve reports whether the server advertised the resolve half of
// code actions. The provider is an options object or a boolean, so it stays raw
// and only the one option a caller needs is read here; a server that advertised
// a bare true asked for no resolves and a resolve request to it is a
// method-not-found rather than a feature.
func (c ServerCapabilities) CodeActionResolve() bool {
	if !Supports(c.CodeActionProvider) {
		return false
	}
	var opts struct {
		ResolveProvider bool `json:"resolveProvider"`
	}
	if json.Unmarshal(c.CodeActionProvider, &opts) != nil {
		return false
	}
	return opts.ResolveProvider
}

// RequestCodeActions asks for the actions that apply to a range, with the
// diagnostics the range is about in context.
//
// context is how a server decides which quick fixes are relevant -- the
// diagnostics that intersect the range, not the whole file's set -- so it is a
// parameter rather than read from the server's own publish: the editor holds
// the set it last showed, and that is the set the request is about.
func RequestCodeActions(ctx context.Context, c *Conn, path string, r Range, diags []Diagnostic) ([]CodeAction, error) {
	if c == nil {
		return nil, ErrClosed
	}
	if diags == nil {
		diags = []Diagnostic{}
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"range":        r,
		"context":      map[string]any{"diagnostics": diags},
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/codeAction", params, &raw); err != nil {
		return nil, err
	}
	return decodeCodeActions(raw), nil
}

// ResolveCodeAction fills in an action through codeAction/resolve.
//
// The action is sent back whole — its title, kind, diagnostics, preferred flag
// and opaque data — because the server keyed its deferred work on one or more
// of them, and a resolve that dropped the data could not identify the action it
// is meant to complete. An edit is deliberately not echoed: this request is
// only made for an action that carries neither an edit nor a command, so there
// is nothing to send, and the app-side WorkspaceEdit has no stable wire
// encoding here.
//
// A reply that carries no usable action is returned with the input action's
// data intact rather than turned into an error, so the caller refuses it by
// name; a malformed reply leaves the input action unchanged, because a failed
// resolve is not a reason to forget what the action was.
func ResolveCodeAction(ctx context.Context, c *Conn, action CodeAction) (CodeAction, error) {
	if c == nil {
		return CodeAction{}, ErrClosed
	}
	params := map[string]any{"title": action.Title}
	if action.Kind != "" {
		params["kind"] = action.Kind
	}
	if len(action.Diagnostics) > 0 {
		params["diagnostics"] = action.Diagnostics
	}
	if action.IsPreferred {
		params["isPreferred"] = true
	}
	if len(action.Data) > 0 {
		params["data"] = action.Data
	}
	if action.Command != nil {
		params["command"] = action.Command
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "codeAction/resolve", params, &raw); err != nil {
		return CodeAction{}, err
	}
	out, ok := decodeCodeAction(raw)
	if !ok {
		return action, nil
	}
	return out, nil
}

// ExecuteCommand runs a server command. The result is usually null and is
// discarded: a command acts on the workspace, and anything it wants to put in a
// buffer arrives as a workspace/applyEdit the editor does not yet answer.
func ExecuteCommand(ctx context.Context, c *Conn, cmd Command) error {
	if c == nil {
		return ErrClosed
	}
	if cmd.Command == "" {
		return nil
	}
	params := map[string]any{"command": cmd.Command}
	if len(cmd.Arguments) > 0 {
		args := make([]json.RawMessage, len(cmd.Arguments))
		copy(args, cmd.Arguments)
		params["arguments"] = args
	}
	return c.Call(ctx, "workspace/executeCommand", params, nil)
}

// decodeCodeActions reads the result, which is an array of CodeAction |
// Command, or null.
func decodeCodeActions(raw json.RawMessage) []CodeAction {
	if isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []CodeAction
	for _, it := range items {
		if act, ok := decodeCodeAction(it); ok {
			out = append(out, act)
		}
	}
	return out
}

// wireCodeAction is the union of the CodeAction literal and the Command
// literal. A Command literal has command as a string at the top level; a
// CodeAction literal has command as an object (or omits it), and may also carry
// an edit or data.
type wireCodeAction struct {
	Title       string            `json:"title"`
	Kind        string            `json:"kind"`
	Diagnostics []Diagnostic      `json:"diagnostics"`
	IsPreferred bool              `json:"isPreferred"`
	Edit        json.RawMessage   `json:"edit"`
	Command     json.RawMessage   `json:"command"`
	Arguments   []json.RawMessage `json:"arguments"`
	Data        json.RawMessage   `json:"data"`
}

// decodeCodeAction reads one result entry. A title is required in both literal
// shapes; an entry without one names nothing to show, so it is dropped rather
// than listed as a blank row.
func decodeCodeAction(raw json.RawMessage) (CodeAction, bool) {
	if isNull(raw) {
		return CodeAction{}, false
	}
	var w wireCodeAction
	if json.Unmarshal(raw, &w) != nil {
		return CodeAction{}, false
	}
	title := strings.TrimSpace(w.Title)
	if title == "" {
		return CodeAction{}, false
	}
	act := CodeAction{
		Title:       title,
		Kind:        w.Kind,
		Diagnostics: w.Diagnostics,
		IsPreferred: w.IsPreferred,
		Edit:        decodeWorkspaceEdit(w.Edit),
		Data:        w.Data,
	}
	act.Command = decodeCommand(w.Command, title, w.Arguments)
	return act, true
}

// decodeCommand reads a command from either literal shape. A JSON string is the
// legacy Command literal, where command and arguments sit at the entry's top
// level; an object is a CodeAction's nested command. Anything else is no
// command, and the action is then an edit, a resolve, or nothing.
func decodeCommand(raw json.RawMessage, title string, topArgs []json.RawMessage) *Command {
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		if name == "" {
			return nil
		}
		return &Command{Title: title, Command: name, Arguments: topArgs}
	}
	var w struct {
		Title     string            `json:"title"`
		Command   string            `json:"command"`
		Arguments []json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &w) != nil || w.Command == "" {
		return nil
	}
	if w.Title == "" {
		w.Title = title
	}
	return &Command{Title: w.Title, Command: w.Command, Arguments: w.Arguments}
}

// positionLess orders two positions in document order.
func positionLess(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// rangesOverlap reports whether two ranges have any position in common. A
// zero-width range therefore counts when it sits inside the other, which is the
// case that matters: a caret with nothing selected still belongs to the
// diagnostic it is on.
func rangesOverlap(a, b Range) bool {
	return !positionLess(b.End, a.Start) && !positionLess(a.End, b.Start)
}

// Intersecting returns the diagnostics whose ranges overlap r, in the order
// given. It is the codeAction context's selection rule: the whole published set
// is the help for the request, and the problems that touch the requested range
// are the ones the actions should be about.
func Intersecting(diags []Diagnostic, r Range) []Diagnostic {
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		if rangesOverlap(d.Range, r) {
			out = append(out, d)
		}
	}
	return out
}
