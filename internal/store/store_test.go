package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustOpen opens a store and closes it when the test ends. Close is idempotent,
// so a test that closes explicitly is still fine.
func mustOpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestOpenCreatesDatabaseAndSchema covers the first contract: Open makes the
// file and the tables, records the current version and turns on WAL.
func TestOpenCreatesDatabaseAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s := mustOpen(t, path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}

	var version int
	if err := s.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}

	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}

// TestReopenSeesPersistedState covers durability: everything written by one
// Store is visible to the next without a migration getting in the way.
func TestReopenSeesPersistedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	first := mustOpen(t, path)
	if err := first.PutSession([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := first.SetPosition("/work/a.go", 42, 7); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	if err := first.SetSetting(ScopeUser, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := mustOpen(t, path)
	blob, ok, err := second.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if !ok || string(blob) != `{"version":1}` {
		t.Fatalf("Session = %q, %v; want the stored blob", blob, ok)
	}

	cursor, top, ok, err := second.Position("/work/a.go")
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if !ok || cursor != 42 || top != 7 {
		t.Fatalf("Position = (%d, %d, %v); want (42, 7, true)", cursor, top, ok)
	}

	settings, err := second.Settings(ScopeUser)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("Settings(user) = %v; want theme=dark", settings)
	}
}

// TestSessionRoundTrip covers the absent case, byte fidelity, replacement and
// the empty-but-present blob.
func TestSessionRoundTrip(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	blob, ok, err := s.Session()
	if err != nil {
		t.Fatalf("Session on a fresh store: %v", err)
	}
	if ok {
		t.Fatalf("fresh store reports a session (%q), want none", blob)
	}

	want := []byte(`{"tabs":[{"path":"/work/a.go"}],"active":0}`)
	if err := s.PutSession(want); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	got, ok, err := s.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("Session = %q, %v; want %q, true", got, ok, want)
	}

	replacement := []byte(`{"tabs":[]}`)
	if err := s.PutSession(replacement); err != nil {
		t.Fatalf("PutSession (replace): %v", err)
	}
	got, ok, err = s.Session()
	if err != nil {
		t.Fatalf("Session after replace: %v", err)
	}
	if !ok || !bytes.Equal(got, replacement) {
		t.Fatalf("Session after replace = %q, %v; want %q, true", got, ok, replacement)
	}

	// An empty blob is a present session, not an absent one: the caller's JSON
	// is opaque and zero-length is still a value.
	if err := s.PutSession([]byte{}); err != nil {
		t.Fatalf("PutSession (empty): %v", err)
	}
	got, ok, err = s.Session()
	if err != nil {
		t.Fatalf("Session after empty: %v", err)
	}
	if !ok || len(got) != 0 {
		t.Fatalf("Session after empty = %q, %v; want empty, true", got, ok)
	}
}

// TestPositions covers lookup, upsert, the whole-map read and the absent case.
func TestPositions(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, _, ok, err := s.Position("/work/missing.go"); err != nil || ok {
		t.Fatalf("Position on a fresh store = ok %v, err %v; want false, nil", ok, err)
	}

	if err := s.SetPosition("/work/a.go", 10, 2); err != nil {
		t.Fatalf("SetPosition a: %v", err)
	}
	if err := s.SetPosition("/work/b.go", 20, 3); err != nil {
		t.Fatalf("SetPosition b: %v", err)
	}
	// The same path updates its row rather than adding one.
	if err := s.SetPosition("/work/a.go", 11, 4); err != nil {
		t.Fatalf("SetPosition a (update): %v", err)
	}

	cursor, top, ok, err := s.Position("/work/a.go")
	if err != nil {
		t.Fatalf("Position a: %v", err)
	}
	if !ok || cursor != 11 || top != 4 {
		t.Fatalf("Position a = (%d, %d, %v); want (11, 4, true)", cursor, top, ok)
	}

	all, err := s.Positions()
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Positions has %d entries, want 2: %v", len(all), all)
	}
	if got := all["/work/a.go"]; got.Cursor != 11 || got.Top != 4 {
		t.Fatalf("/work/a.go = %+v; want cursor 11 top 4", got)
	}
	if got := all["/work/b.go"]; got.Cursor != 20 || got.Top != 3 {
		t.Fatalf("/work/b.go = %+v; want cursor 20 top 3", got)
	}
	for _, p := range all {
		if p.Updated == 0 {
			t.Fatalf("position %q has no updated time", p.Path)
		}
	}
}

// TestSettingsScopesAreIsolated is the scope rule from the schema: a user key
// and a workspace key with the same name are different rows, and delete only
// touches its own scope.
func TestSettingsScopesAreIsolated(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if err := s.SetSetting(ScopeUser, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting user/theme: %v", err)
	}
	if err := s.SetSetting(ScopeWorkspace, "theme", "light"); err != nil {
		t.Fatalf("SetSetting workspace/theme: %v", err)
	}
	if err := s.SetSetting(ScopeUser, "wrap", "on"); err != nil {
		t.Fatalf("SetSetting user/wrap: %v", err)
	}

	user, err := s.Settings(ScopeUser)
	if err != nil {
		t.Fatalf("Settings(user): %v", err)
	}
	if len(user) != 2 || user["theme"] != "dark" || user["wrap"] != "on" {
		t.Fatalf("Settings(user) = %v; want theme=dark wrap=on", user)
	}

	workspace, err := s.Settings(ScopeWorkspace)
	if err != nil {
		t.Fatalf("Settings(workspace): %v", err)
	}
	if len(workspace) != 1 || workspace["theme"] != "light" {
		t.Fatalf("Settings(workspace) = %v; want only theme=light", workspace)
	}

	// Overwrite lands in the same scope.
	if err := s.SetSetting(ScopeUser, "theme", "solarized"); err != nil {
		t.Fatalf("SetSetting user/theme (overwrite): %v", err)
	}
	user, err = s.Settings(ScopeUser)
	if err != nil {
		t.Fatalf("Settings(user) after overwrite: %v", err)
	}
	if user["theme"] != "solarized" {
		t.Fatalf("user theme = %q, want solarized", user["theme"])
	}

	// Deleting is scoped: the user row goes, the workspace row stays.
	if err := s.DeleteSetting(ScopeUser, "theme"); err != nil {
		t.Fatalf("DeleteSetting user/theme: %v", err)
	}
	user, err = s.Settings(ScopeUser)
	if err != nil {
		t.Fatalf("Settings(user) after delete: %v", err)
	}
	if _, ok := user["theme"]; ok {
		t.Fatalf("user theme survived delete: %v", user)
	}
	workspace, err = s.Settings(ScopeWorkspace)
	if err != nil {
		t.Fatalf("Settings(workspace) after user delete: %v", err)
	}
	if workspace["theme"] != "light" {
		t.Fatalf("workspace theme = %q after a user-scope delete, want light", workspace["theme"])
	}

	// Deleting an absent key is a no-op, not an error.
	if err := s.DeleteSetting(ScopeUser, "absent"); err != nil {
		t.Fatalf("DeleteSetting absent: %v", err)
	}
}

// TestUnknownScopeIsRefused pins the scope check: a typo must be an error, not
// a silent read or write against a scope that will never be looked at again.
func TestUnknownScopeIsRefused(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, err := s.Settings("workspce"); !errors.Is(err, errUnknownScope) {
		t.Fatalf("Settings(workspce) error = %v, want unknown scope", err)
	}
	if err := s.SetSetting("workspce", "theme", "dark"); !errors.Is(err, errUnknownScope) {
		t.Fatalf("SetSetting(workspce) error = %v, want unknown scope", err)
	}
	if err := s.DeleteSetting("workspce", "theme"); !errors.Is(err, errUnknownScope) {
		t.Fatalf("DeleteSetting(workspce) error = %v, want unknown scope", err)
	}
	// The refused write left nothing behind under a real scope.
	user, err := s.Settings(ScopeUser)
	if err != nil {
		t.Fatalf("Settings(user): %v", err)
	}
	if len(user) != 0 {
		t.Fatalf("a refused scope wrote user settings: %v", user)
	}
}

// TestSchemaVersionStableAcrossReopen covers idempotence: reopening never
// advances the version again or re-runs a step that would fail on existing
// tables.
func TestSchemaVersionStableAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 3; i++ {
		s := mustOpen(t, path)
		var version int
		if err := s.db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
			t.Fatalf("open %d: read schema version: %v", i, err)
		}
		if version != schemaVersion {
			t.Fatalf("open %d: schema version = %d, want %d", i, version, schemaVersion)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("open %d: close: %v", i, err)
		}
	}
}

// TestSchemaTableIsASingleton pins the bootstrap row: reopening must not add a
// second version row. INSERT OR IGNORE needs a uniqueness constraint to ignore
// anything, and without one every Open appended another row.
func TestSchemaTableIsASingleton(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 3; i++ {
		s := mustOpen(t, path)
		var rows int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema`).Scan(&rows); err != nil {
			t.Fatalf("open %d: count schema rows: %v", i, err)
		}
		if rows != 1 {
			t.Fatalf("open %d: schema has %d rows, want 1", i, rows)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("open %d: close: %v", i, err)
		}
	}
}

// TestTooNewSchemaIsRefused covers the refusal: a database from a future build
// must not be opened and downgraded.
func TestTooNewSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s := mustOpen(t, path)

	// Simulate a database written by a newer build.
	if _, err := s.db.Exec(updateSchemaVersion, schemaVersion+1); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	future, err := Open(path)
	if err == nil {
		future.Close()
		t.Fatal("Open accepted a schema version newer than this build")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open error = %v; want a refusal naming the newer version", err)
	}
}

// TestCloseIsIdempotent covers the second Close: it must be a no-op.
func TestCloseIsIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// And a third, to prove later calls stay nil rather than replaying an
	// earlier result.
	if err := s.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}

// TestBoundaryValidation pins the refusals: empty identity values and negative
// offsets never reach SQL.
func TestBoundaryValidation(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, err := Open(""); !errors.Is(err, errEmptyPath) {
		t.Fatalf("Open(%q) error = %v, want empty path", "", err)
	}
	if err := s.SetPosition("", 0, 0); !errors.Is(err, errEmptyPath) {
		t.Fatalf("SetPosition empty path error = %v, want empty path", err)
	}
	if err := s.SetPosition("/work/a.go", -1, 0); !errors.Is(err, errNegative) {
		t.Fatalf("SetPosition negative cursor error = %v, want negative position", err)
	}
	if err := s.SetPosition("/work/a.go", 0, -1); !errors.Is(err, errNegative) {
		t.Fatalf("SetPosition negative top error = %v, want negative position", err)
	}
	if _, _, _, err := s.Position(""); !errors.Is(err, errEmptyPath) {
		t.Fatalf("Position empty path error = %v, want empty path", err)
	}
	if _, err := s.Settings(""); !errors.Is(err, errEmptyScope) {
		t.Fatalf("Settings empty scope error = %v, want empty scope", err)
	}
	if err := s.SetSetting("", "k", "v"); !errors.Is(err, errEmptyScope) {
		t.Fatalf("SetSetting empty scope error = %v, want empty scope", err)
	}
	if err := s.SetSetting(ScopeUser, "", "v"); !errors.Is(err, errEmptyKey) {
		t.Fatalf("SetSetting empty key error = %v, want empty key", err)
	}
	if err := s.DeleteSetting(ScopeUser, ""); !errors.Is(err, errEmptyKey) {
		t.Fatalf("DeleteSetting empty key error = %v, want empty key", err)
	}
}

// TestTwoStoresOnOneDatabase covers the concurrency contract: two Stores on one
// path do not corrupt each other, and each sees the other's committed writes.
func TestTwoStoresOnOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	a := mustOpen(t, path)
	b := mustOpen(t, path)

	if err := a.PutSession([]byte("from-a")); err != nil {
		t.Fatalf("a.PutSession: %v", err)
	}
	blob, ok, err := b.Session()
	if err != nil {
		t.Fatalf("b.Session: %v", err)
	}
	if !ok || string(blob) != "from-a" {
		t.Fatalf("b.Session = %q, %v; want from-a, true", blob, ok)
	}

	if err := b.SetPosition("/work/a.go", 5, 1); err != nil {
		t.Fatalf("b.SetPosition: %v", err)
	}
	cursor, top, ok, err := a.Position("/work/a.go")
	if err != nil {
		t.Fatalf("a.Position: %v", err)
	}
	if !ok || cursor != 5 || top != 1 {
		t.Fatalf("a.Position = (%d, %d, %v); want (5, 1, true)", cursor, top, ok)
	}
}

// TestJournalRoundTrip covers the per-buffer row: upsert, byte fidelity of the
// snapshot and digest, the absent case, delete, and the refusals.
func TestJournalRoundTrip(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, ok, err := s.Journal("/work/a.go"); err != nil || ok {
		t.Fatalf("Journal on a fresh store = ok %v, err %v; want false, nil", ok, err)
	}
	want := JournalEntry{
		Path:     "/work/a.go",
		Snapshot: []byte(`{"version":1,"base":"aGVsbG8="}`),
		Saved:    7,
		Encoding: []byte(`{"Kind":0,"CRLF":true,"BOM":false,"Mixed":false}`),
		Digest:   bytes.Repeat([]byte{0xab}, 32),
	}
	if err := s.PutJournal(want); err != nil {
		t.Fatalf("PutJournal: %v", err)
	}
	got, ok, err := s.Journal("/work/a.go")
	if err != nil {
		t.Fatalf("Journal: %v", err)
	}
	if !ok {
		t.Fatal("Journal reports no row after PutJournal")
	}
	if got.Saved != want.Saved || !bytes.Equal(got.Snapshot, want.Snapshot) ||
		!bytes.Equal(got.Encoding, want.Encoding) || !bytes.Equal(got.Digest, want.Digest) {
		t.Fatalf("Journal = %+v; want %+v", got, want)
	}
	if got.Updated == 0 {
		t.Error("Journal did not stamp Updated")
	}

	// A second put replaces the row rather than adding one.
	want.Saved = 9
	want.Snapshot = []byte(`{"version":1,"base":"d29ybGQ="}`)
	if err := s.PutJournal(want); err != nil {
		t.Fatalf("PutJournal (replace): %v", err)
	}
	got, ok, err = s.Journal("/work/a.go")
	if err != nil || !ok {
		t.Fatalf("Journal after replace = ok %v, err %v", ok, err)
	}
	if got.Saved != 9 || !bytes.Equal(got.Snapshot, want.Snapshot) {
		t.Fatalf("Journal after replace = %+v; want saved 9 and the new snapshot", got)
	}

	if err := s.DeleteJournal("/work/a.go"); err != nil {
		t.Fatalf("DeleteJournal: %v", err)
	}
	if _, ok, err := s.Journal("/work/a.go"); err != nil || ok {
		t.Fatalf("Journal after delete = ok %v, err %v; want false, nil", ok, err)
	}
	// Deleting an absent row is a no-op.
	if err := s.DeleteJournal("/work/absent.go"); err != nil {
		t.Fatalf("DeleteJournal absent: %v", err)
	}

	if err := s.PutJournal(JournalEntry{Path: ""}); !errors.Is(err, errEmptyPath) {
		t.Fatalf("PutJournal empty path error = %v, want empty path", err)
	}
	if err := s.PutJournal(JournalEntry{Path: "/work/a.go"}); !errors.Is(err, errEmptyJournal) {
		t.Fatalf("PutJournal empty snapshot error = %v, want empty journal", err)
	}
	if _, _, err := s.Journal(""); !errors.Is(err, errEmptyPath) {
		t.Fatalf("Journal empty path error = %v, want empty path", err)
	}
	if err := s.DeleteJournal(""); !errors.Is(err, errEmptyPath) {
		t.Fatalf("DeleteJournal empty path error = %v, want empty path", err)
	}
}

// TestJournalMigrationFromV1 covers the forward step: a database written at v1
// gains the journal table and moves to v2, and the rows written before the
// migration survive it.
func TestJournalMigrationFromV1(t *testing.T) {
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
	for _, stmt := range migrations[0] {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("build v1: %v", err)
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 1); err != nil {
		t.Fatalf("record v1: %v", err)
	}
	if _, err := raw.Exec(upsertSession, []byte("seed"), 1); err != nil {
		t.Fatalf("seed session: %v", err)
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
	// Every row carries a digest: it is what the restore path checks the disk
	// bytes against, so the column is NOT NULL and an entry without one is
	// incomplete rather than legal.
	sum := sha256.Sum256([]byte("{}"))
	if err := s.PutJournal(JournalEntry{
		Path:     "/w/a.go",
		Snapshot: []byte("{}"),
		Saved:    1,
		Digest:   sum[:],
	}); err != nil {
		t.Fatalf("PutJournal after migration: %v", err)
	}
}
