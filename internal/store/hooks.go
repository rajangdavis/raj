package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Hook is one stored hook definition. Action is the stored JSON: an argv
// array, or {"shell": "..."}. Trigger, tree and action are validated by the
// hooks service, not here.
type Hook struct {
	Name       string
	Action     string
	Trigger    string
	Tree       string
	Agent      bool
	CooldownMS int
	TimeoutMS  int
	MayWrite   bool
	Detach     bool
	Enabled    bool
	// Params is the stored JSON array of declared parameters; "[]" means the
	// hook takes none. The hooks service validates its contents, not the store.
	Params  string
	Updated int64
}

// hookFlag maps a boolean hook field to the 0/1 its INTEGER column stores.
func hookFlag(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PutHook stores h, replacing any row with the same name. Updated is stamped
// here to the current unix time; the caller's Updated is ignored. An empty
// Name or an empty Action is refused: a hook with no action cannot run. The
// contents of Action, Trigger and Tree are the hooks service's to validate,
// not the store's. An empty Tree is stored as the projected default.
func (s *Store) PutHook(h Hook) error {
	if h.Name == "" {
		return fmt.Errorf("store: put hook: %w", errEmptyHookName)
	}
	if h.Action == "" {
		return fmt.Errorf("store: put hook %s: %w", h.Name, errEmptyHookAction)
	}
	tree := h.Tree
	if tree == "" {
		tree = hookTreeProjected
	}
	params := h.Params
	if params == "" {
		params = hookParamsDefault
	}
	if _, err := s.db.Exec(upsertHook, h.Name, h.Action, h.Trigger, tree, params, hookFlag(h.Agent),
		h.CooldownMS, h.TimeoutMS, hookFlag(h.MayWrite), hookFlag(h.Detach), hookFlag(h.Enabled), time.Now().Unix()); err != nil {
		return fmt.Errorf("store: put hook %s: %w", h.Name, err)
	}
	return nil
}

// Hook returns the row for name and whether one is present. A missing name
// reports the zero Hook and false; an empty name is refused.
func (s *Store) Hook(name string) (Hook, bool, error) {
	if name == "" {
		return Hook{}, false, fmt.Errorf("store: get hook: %w", errEmptyHookName)
	}
	var h Hook
	switch err := s.db.QueryRow(selectHook, name).Scan(
		&h.Name, &h.Action, &h.Trigger, &h.Tree, &h.Params, &h.Agent, &h.CooldownMS, &h.TimeoutMS,
		&h.MayWrite, &h.Detach, &h.Enabled, &h.Updated,
	); err {
	case nil:
		return h, true, nil
	case sql.ErrNoRows:
		return Hook{}, false, nil
	default:
		return Hook{}, false, fmt.Errorf("store: get hook %s: %w", name, err)
	}
}

// Hooks returns every hook, sorted by name. It is never nil, so a caller can
// range over it without a nil check.
func (s *Store) Hooks() ([]Hook, error) {
	rows, err := s.db.Query(selectHooks)
	if err != nil {
		return nil, fmt.Errorf("store: list hooks: %w", err)
	}
	defer rows.Close()

	out := make([]Hook, 0)
	for rows.Next() {
		var h Hook
		if err := rows.Scan(
			&h.Name, &h.Action, &h.Trigger, &h.Tree, &h.Params, &h.Agent, &h.CooldownMS, &h.TimeoutMS,
			&h.MayWrite, &h.Detach, &h.Enabled, &h.Updated,
		); err != nil {
			return nil, fmt.Errorf("store: scan hook: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list hooks: %w", err)
	}
	return out, nil
}

// DeleteHook removes the row for name. Deleting a name that is not present is
// not an error; an empty name is refused.
func (s *Store) DeleteHook(name string) error {
	if name == "" {
		return fmt.Errorf("store: delete hook: %w", errEmptyHookName)
	}
	if _, err := s.db.Exec(deleteHook, name); err != nil {
		return fmt.Errorf("store: delete hook %s: %w", name, err)
	}
	return nil
}

// The hook statements, one named constant each, mirroring the schema constants.
const upsertHook = `
INSERT INTO hooks (name, action, trigger, tree, params, agent, cooldown_ms, timeout_ms, may_write, detach, enabled, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET
	action      = excluded.action,
	trigger     = excluded.trigger,
	tree        = excluded.tree,
	params      = excluded.params,
	agent       = excluded.agent,
	cooldown_ms = excluded.cooldown_ms,
	timeout_ms  = excluded.timeout_ms,
	may_write   = excluded.may_write,
	detach      = excluded.detach,
	enabled     = excluded.enabled,
	updated     = excluded.updated`

const selectHook = `SELECT name, action, trigger, tree, params, agent, cooldown_ms, timeout_ms, may_write, detach, enabled, updated FROM hooks WHERE name = ?`

const selectHooks = `SELECT name, action, trigger, tree, params, agent, cooldown_ms, timeout_ms, may_write, detach, enabled, updated FROM hooks ORDER BY name`

const deleteHook = `DELETE FROM hooks WHERE name = ?`
