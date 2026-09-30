package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooksMigrationFromV2 covers the forward step: a database written at v2
// gains the hooks table and moves to the current version, and the rows written
// before the migration survive it. Precondition: a raw v2 database holding a session and
// a journal row. It fails if migrations[2] is missing or not appended, because
// Open then refuses v2 or leaves the version there and PutHook has no table.
func TestHooksMigrationFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(createSchema); err != nil {
		t.Fatalf("create schema table: %v", err)
	}
	if _, err := raw.Exec(insertSchemaVersion); err != nil {
		t.Fatalf("initialise version: %v", err)
	}
	for _, step := range [][]string{migrations[0], migrations[1]} {
		for _, stmt := range step {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("build v2: %v", err)
			}
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 2); err != nil {
		t.Fatalf("record v2: %v", err)
	}
	if _, err := raw.Exec(upsertSession, []byte("seed"), 1); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	sum := sha256.Sum256([]byte("{}"))
	if _, err := raw.Exec(upsertJournal, "/w/a.go", []byte("{}"), 1, "utf-8", sum[:], 1); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s := mustOpen(t, path)
	var version int
	if err := s.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("migrated version = %d, want %d", version, schemaVersion)
	}
	if blob, ok, err := s.Session(); err != nil || !ok || string(blob) != "seed" {
		t.Fatalf("Session after migration = %q, %v, %v; want seed, true, nil", blob, ok, err)
	}
	entry, ok, err := s.Journal("/w/a.go")
	if err != nil || !ok {
		t.Fatalf("Journal after migration = ok %v, err %v; want the seeded row", ok, err)
	}
	if !bytes.Equal(entry.Snapshot, []byte("{}")) {
		t.Fatalf("Journal snapshot after migration = %q; want {}", entry.Snapshot)
	}

	// The new table exists and a hook round-trips through it.
	if err := s.PutHook(Hook{
		Name:       "check",
		Action:     `["bash","scripts/check.sh"]`,
		Trigger:    "agent",
		Agent:      true,
		CooldownMS: 2000,
		TimeoutMS:  30000,
		MayWrite:   false,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("PutHook after migration: %v", err)
	}
	got, ok, err := s.Hook("check")
	if err != nil || !ok {
		t.Fatalf("Hook after migration = ok %v, err %v", ok, err)
	}
	if got.Action != `["bash","scripts/check.sh"]` || got.Trigger != "agent" || got.Tree != hookTreeProjected || !got.Agent || !got.Enabled {
		t.Fatalf("Hook after migration = %+v; want the stored check hook", got)
	}
}

// TestHooksMigrationFromV3 covers the forward step into the tree column: a
// database written at v3 keeps its hook rows, gains hooks.tree, and moves to
// the current version with every existing row reading the projected default.
// Precondition: a raw v3 database with one hook row written before the column
// existed. It fails if migrations[3] is missing or not appended, because Open
// then leaves the version at v3 and the tree column is absent.
func TestHooksMigrationFromV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(createSchema); err != nil {
		t.Fatalf("create schema table: %v", err)
	}
	if _, err := raw.Exec(insertSchemaVersion); err != nil {
		t.Fatalf("initialise version: %v", err)
	}
	for _, step := range [][]string{migrations[0], migrations[1], migrations[2]} {
		for _, stmt := range step {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("build v3: %v", err)
			}
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 3); err != nil {
		t.Fatalf("record v3: %v", err)
	}
	// The row is written without hooks.tree, exactly as a v3 build would.
	if _, err := raw.Exec(`INSERT INTO hooks (name, action, trigger, agent, cooldown_ms, timeout_ms, may_write, enabled, updated)
		VALUES ('check', '["true"]', 'agent', 1, 0, 0, 0, 1, 1)`); err != nil {
		t.Fatalf("seed v3 hook: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s := mustOpen(t, path)
	var version int
	if err := s.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("migrated version = %d, want %d", version, schemaVersion)
	}
	got, ok, err := s.Hook("check")
	if err != nil || !ok {
		t.Fatalf("Hook after migration = ok %v, err %v; want the seeded row", ok, err)
	}
	if got.Tree != hookTreeProjected {
		t.Fatalf("migrated hook tree = %q, want %q", got.Tree, hookTreeProjected)
	}
	if got.Action != `["true"]` || got.Detach || !got.Agent || !got.Enabled {
		t.Fatalf("Hook after migration = %+v; want the seeded row intact with detach off", got)
	}

	// The new column round-trips a workspace hook through the store.
	if err := s.PutHook(Hook{Name: "ws", Action: `["true"]`, Trigger: "agent", Tree: "workspace", Detach: true, Enabled: true}); err != nil {
		t.Fatalf("PutHook workspace after migration: %v", err)
	}
	ws, ok, err := s.Hook("ws")
	if err != nil || !ok || ws.Tree != "workspace" || !ws.Detach {
		t.Fatalf("workspace hook after migration = %+v, ok %v, err %v; want tree workspace and detach on", ws, ok, err)
	}
}

// TestHooksMigrationFromV3IsIdempotent covers the one step SQLite cannot write
// as CREATE ... IF NOT EXISTS. With the tree column already present and the
// recorded version rewound to v3, the next Open re-runs the ALTER, must ignore
// the duplicate column, and still record the new version. Precondition: a v3
// database migrated once. Without the guard the second Open fails on
// "duplicate column name" and leaves the version at 3.
func TestHooksMigrationFromV3IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(createSchema); err != nil {
		t.Fatalf("create schema table: %v", err)
	}
	if _, err := raw.Exec(insertSchemaVersion); err != nil {
		t.Fatalf("initialise version: %v", err)
	}
	for _, step := range [][]string{migrations[0], migrations[1], migrations[2]} {
		for _, stmt := range step {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("build v3: %v", err)
			}
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 3); err != nil {
		t.Fatalf("record v3: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// The first Open adds hooks.tree and records v4.
	first := mustOpen(t, path)
	if err := first.PutHook(Hook{Name: "check", Action: `["true"]`, Trigger: "agent", Enabled: true}); err != nil {
		t.Fatalf("PutHook: %v", err)
	}
	// Rewind the recorded version so the next Open runs the ALTER again with
	// the column already there, exactly as a lost migration race would.
	if _, err := first.db.Exec(updateSchemaVersion, 3); err != nil {
		t.Fatalf("rewind version: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second := mustOpen(t, path)
	var version int
	if err := second.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		t.Fatalf("read version after re-run: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("version after re-run = %d, want %d", version, schemaVersion)
	}
	got, ok, err := second.Hook("check")
	if err != nil || !ok || got.Tree != hookTreeProjected {
		t.Fatalf("Hook after re-run = %+v, ok %v, err %v; want the row with the projected default", got, ok, err)
	}
}

// TestHooksCRUD covers the hook row: the full-field round-trip including both
// booleans and the millisecond fields, the upsert changing a field, name
// ordering, the absent cases, deletion and the boundary refusals.
// Precondition: a fresh store. It fails if the columns are not persisted or
// ordered by name, if Updated is not stamped internally, or if the refusals
// are absent.
func TestHooksCRUD(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, ok, err := s.Hook("missing"); err != nil || ok {
		t.Fatalf("Hook on a fresh store = ok %v, err %v; want false, nil", ok, err)
	}
	all, err := s.Hooks()
	if err != nil {
		t.Fatalf("Hooks on a fresh store: %v", err)
	}
	if all == nil || len(all) != 0 {
		t.Fatalf("Hooks on a fresh store = %v; want an empty non-nil slice", all)
	}

	want := Hook{
		Name:       "check",
		Action:     `["go","test","./..."]`,
		Trigger:    "agent",
		Tree:       "workspace",
		Agent:      true,
		Detach:     true,
		CooldownMS: 2000,
		TimeoutMS:  30000,
		MayWrite:   false,
		Enabled:    true,
		Updated:    1, // ignored: the store stamps its own unix time
	}
	if err := s.PutHook(want); err != nil {
		t.Fatalf("PutHook: %v", err)
	}
	got, ok, err := s.Hook("check")
	if err != nil || !ok {
		t.Fatalf("Hook after PutHook = ok %v, err %v", ok, err)
	}
	if got.Name != want.Name || got.Action != want.Action || got.Trigger != want.Trigger ||
		got.Tree != want.Tree || got.Agent != want.Agent || got.Detach != want.Detach || got.CooldownMS != want.CooldownMS ||
		got.TimeoutMS != want.TimeoutMS || got.MayWrite != want.MayWrite || got.Enabled != want.Enabled {
		t.Fatalf("Hook = %+v; want %+v", got, want)
	}
	if got.Updated <= want.Updated {
		t.Fatalf("Hook Updated = %d; want the store's own stamp, not the caller's %d", got.Updated, want.Updated)
	}

	// A second put upserts the row rather than adding one and changes a field.
	want.Action = `{"shell":"echo hi"}`
	want.Tree = hookTreeProjected
	want.Detach = false
	want.Enabled = false
	if err := s.PutHook(want); err != nil {
		t.Fatalf("PutHook (upsert): %v", err)
	}
	got, ok, err = s.Hook("check")
	if err != nil || !ok {
		t.Fatalf("Hook after upsert = ok %v, err %v", ok, err)
	}
	if got.Action != want.Action || got.Tree != want.Tree || got.Detach != want.Detach || got.Enabled {
		t.Fatalf("Hook after upsert = %+v; want action %q, tree %q and detach %t and enabled false", got, want.Action, want.Tree, want.Detach)
	}
	all, err = s.Hooks()
	if err != nil {
		t.Fatalf("Hooks after upsert: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Hooks after upsert has %d rows, want 1: %v", len(all), all)
	}

	// Hooks is ordered by name whatever the insert order.
	for _, name := range []string{"zeta", "alpha"} {
		if err := s.PutHook(Hook{Name: name, Action: `["true"]`, Trigger: "agent"}); err != nil {
			t.Fatalf("PutHook %s: %v", name, err)
		}
	}
	all, err = s.Hooks()
	if err != nil {
		t.Fatalf("Hooks: %v", err)
	}
	names := make([]string, len(all))
	for i, h := range all {
		names[i] = h.Name
	}
	if strings.Join(names, ",") != "alpha,check,zeta" {
		t.Fatalf("Hooks order = %v; want alpha, check, zeta", names)
	}

	// Delete removes the row; a missing row is a no-op.
	if err := s.DeleteHook("check"); err != nil {
		t.Fatalf("DeleteHook: %v", err)
	}
	if _, ok, err := s.Hook("check"); err != nil || ok {
		t.Fatalf("Hook after delete = ok %v, err %v; want false, nil", ok, err)
	}
	if err := s.DeleteHook("absent"); err != nil {
		t.Fatalf("DeleteHook absent: %v", err)
	}

	// Boundary refusals never reach SQL.
	if err := s.PutHook(Hook{Action: `["true"]`}); !errors.Is(err, errEmptyHookName) {
		t.Fatalf("PutHook empty name error = %v, want empty hook name", err)
	}
	if err := s.PutHook(Hook{Name: "check"}); !errors.Is(err, errEmptyHookAction) {
		t.Fatalf("PutHook empty action error = %v, want empty hook action", err)
	}
	if _, ok, err := s.Hook(""); !errors.Is(err, errEmptyHookName) || ok {
		t.Fatalf("Hook empty name = ok %v, err %v; want empty hook name", ok, err)
	}
	if err := s.DeleteHook(""); !errors.Is(err, errEmptyHookName) {
		t.Fatalf("DeleteHook empty name error = %v, want empty hook name", err)
	}
}

// TestHooksParamsMigrationFromV9 covers the v9 -> v10 step: a database written
// at v9 gains hooks.params, an existing hook row reads the empty declaration
// default, and a row authored with parameters round-trips. Precondition: a raw
// v9 database with one hook row written before the column existed. It fails if
// migrations[9] is missing or the params column is not backfilled.
func TestHooksParamsMigrationFromV9(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(createSchema); err != nil {
		t.Fatalf("create schema table: %v", err)
	}
	if _, err := raw.Exec(insertSchemaVersion); err != nil {
		t.Fatalf("initialise version: %v", err)
	}
	for _, step := range migrations[:9] {
		for _, stmt := range step {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("build v9: %v", err)
			}
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 9); err != nil {
		t.Fatalf("record v9: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO hooks (name, action, trigger, tree, agent, cooldown_ms, timeout_ms, may_write, detach, enabled, updated)
		VALUES ('check', '["true"]', 'agent', 'projected', 1, 0, 0, 0, 0, 1, 1)`); err != nil {
		t.Fatalf("seed v9 hook: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s := mustOpen(t, path)
	var version int
	if err := s.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("migrated version = %d, want %d", version, schemaVersion)
	}
	got, ok, err := s.Hook("check")
	if err != nil || !ok {
		t.Fatalf("Hook after migration = ok %v, err %v; want the seeded row", ok, err)
	}
	if got.Params != hookParamsDefault {
		t.Fatalf("migrated hook params = %q, want %q", got.Params, hookParamsDefault)
	}

	// A declared list round-trips through the new column.
	const decls = `["PHASE=enum(check,race)=check"]`
	if err := s.PutHook(Hook{Name: "cycle", Action: `["true"]`, Params: decls, Trigger: "agent", Enabled: true}); err != nil {
		t.Fatalf("PutHook with params: %v", err)
	}
	cy, ok, err := s.Hook("cycle")
	if err != nil || !ok || cy.Params != decls {
		t.Fatalf("hook with params = %+v, ok %v, err %v; want params %s", cy, ok, err, decls)
	}
	// An empty Params is stored as the empty-array default, not an empty string.
	if err := s.PutHook(Hook{Name: "plain", Action: `["true"]`, Trigger: "agent"}); err != nil {
		t.Fatalf("PutHook plain: %v", err)
	}
	if pl, _, _ := s.Hook("plain"); pl.Params != hookParamsDefault {
		t.Fatalf("plain hook params = %q; want %q", pl.Params, hookParamsDefault)
	}
}
