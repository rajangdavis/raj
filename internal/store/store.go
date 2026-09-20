// Package store persists a workspace's session, per-file cursor positions and
// settings in a single SQLite database.
//
// The database path is the caller's to choose. Open takes it as an argument,
// and the app passes the workspace's file inside its XDG state directory; this
// package has no opinion about where that is.
//
// The schema is versioned and migrated forward on Open; a database written by
// a newer build is refused rather than downgraded. The session is an opaque
// JSON blob here — the app marshals session.State before calling PutSession —
// because this package owns storage only, not the shape of what is stored.
//
// The pure-Go SQLite driver is named only in driver.go, which imports it for
// its side effect. Every other file speaks database/sql, so the package reads
// as plain stdlib and type-checks without the module downloaded.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Settings scopes. They are a closed set: the methods below refuse a scope
// that is not one of these, so a typo like "workspce" is reported rather than
// silently read or written as a scope of its own. A new scope is added here
// and nowhere else.
const (
	// ScopeUser is the per-user settings scope, shared across workspaces.
	ScopeUser = "user"
	// ScopeWorkspace is the per-workspace settings scope.
	ScopeWorkspace = "workspace"
	// ScopeClient is the per-client view scope: each client key (an attach
	// name, or the profile) stores its own tab view of this workspace here,
	// separate from the editor session and its settings.
	ScopeClient = "client"
)

// Position is a remembered cursor for one file: Cursor is the byte offset of
// the primary cursor and Top the first visible line. Updated is a unix time for
// diagnostics and tests, not part of a position's identity.
type Position struct {
	Path    string
	Cursor  int
	Top     int
	Updated int64
}

// Boundary errors. The methods wrap these with the operation and the offending
// value, so a caller can test for the class with errors.Is.
var (
	errEmptyPath    = errors.New("empty path")
	errEmptyScope   = errors.New("empty scope")
	errUnknownScope = errors.New("unknown scope")
	errEmptyKey     = errors.New("empty key")
	errNegative     = errors.New("negative position")
	errEmptyJournal = errors.New("empty journal snapshot")
)

// validScope reports whether scope names one of the settings scopes. The set is
// closed: see the scope constants.
func validScope(scope string) bool {
	return scope == ScopeUser || scope == ScopeWorkspace || scope == ScopeClient
}

// scopeError refuses a scope the settings methods do not know. An empty scope
// keeps its own error so a missing scope and a mistyped one read differently.
func scopeError(op, scope string) error {
	if scope == "" {
		return fmt.Errorf("store: %s: %w", op, errEmptyScope)
	}
	return fmt.Errorf("store: %s %s: %w", op, scope, errUnknownScope)
}

// Store is one workspace database. There is no package-level state: every
// handle hangs off the value and Close releases it.
type Store struct {
	db        *sql.DB
	closeOnce sync.Once
}

// Open opens the database at path, creating the parent directory and the file
// if needed, migrating the schema to the current version, and enabling WAL.
//
// Two Stores may open the same path: WAL lets a reader run alongside the one
// writer, the busy timeout makes the loser of a migration race wait rather
// than fail, and every migration statement is idempotent.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: open: %w", errEmptyPath)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection makes the DSN pragmas authoritative for every statement
	// and turns intra-process contention into an ordinary serialised queue; the
	// busy timeout covers the cross-process case.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database on its first call and does nothing at all after.
// It is idempotent, and a later call reports nil rather than the first call's
// error: by then there is nothing left to fail.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if cerr := s.db.Close(); cerr != nil {
			err = fmt.Errorf("store: close: %w", cerr)
		}
	})
	return err
}

// PutSession stores the session blob in the single row id=1, replacing any
// previous value. The blob is opaque here.
func (s *Store) PutSession(blob []byte) error {
	if _, err := s.db.Exec(upsertSession, blob, time.Now().Unix()); err != nil {
		return fmt.Errorf("store: put session: %w", err)
	}
	return nil
}

// Session returns the stored session blob and whether one is present. A fresh
// database reports (nil, false, nil).
func (s *Store) Session() ([]byte, bool, error) {
	var blob []byte
	switch err := s.db.QueryRow(selectSession).Scan(&blob); err {
	case nil:
		return blob, true, nil
	case sql.ErrNoRows:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("store: get session: %w", err)
	}
}

// SetPosition records the cursor and top line for path, replacing any previous
// value. An empty path or a negative offset is refused.
func (s *Store) SetPosition(path string, cursor, top int) error {
	if path == "" {
		return fmt.Errorf("store: set position: %w", errEmptyPath)
	}
	if cursor < 0 || top < 0 {
		return fmt.Errorf("store: set position %s: %w", path, errNegative)
	}
	if _, err := s.db.Exec(upsertPosition, path, cursor, top, time.Now().Unix()); err != nil {
		return fmt.Errorf("store: set position %s: %w", path, err)
	}
	return nil
}

// Position returns the recorded cursor and top line for path, and whether one
// is present. An empty path is refused.
func (s *Store) Position(path string) (cursor, top int, ok bool, err error) {
	if path == "" {
		return 0, 0, false, fmt.Errorf("store: get position: %w", errEmptyPath)
	}
	var p Position
	switch rowErr := s.db.QueryRow(selectPosition, path).Scan(&p.Path, &p.Cursor, &p.Top, &p.Updated); rowErr {
	case nil:
		return p.Cursor, p.Top, true, nil
	case sql.ErrNoRows:
		return 0, 0, false, nil
	default:
		return 0, 0, false, fmt.Errorf("store: get position %s: %w", path, rowErr)
	}
}

// Positions returns every recorded position, keyed by path. It is never nil,
// so a caller can range over it without a nil check.
func (s *Store) Positions() (map[string]Position, error) {
	rows, err := s.db.Query(selectPositions)
	if err != nil {
		return nil, fmt.Errorf("store: list positions: %w", err)
	}
	defer rows.Close()

	out := make(map[string]Position)
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.Path, &p.Cursor, &p.Top, &p.Updated); err != nil {
			return nil, fmt.Errorf("store: scan position: %w", err)
		}
		out[p.Path] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list positions: %w", err)
	}
	return out, nil
}

// Settings returns every key/value pair in scope. A scope outside the known
// pair is refused. The result is never nil.
func (s *Store) Settings(scope string) (map[string]string, error) {
	if !validScope(scope) {
		return nil, scopeError("settings", scope)
	}
	rows, err := s.db.Query(selectSettings, scope)
	if err != nil {
		return nil, fmt.Errorf("store: settings %s: %w", scope, err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store: scan setting: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: settings %s: %w", scope, err)
	}
	return out, nil
}

// SetSetting stores a value in scope. A scope outside the known pair or an
// empty key is refused; the value itself may be empty.
func (s *Store) SetSetting(scope, key, value string) error {
	if !validScope(scope) {
		return scopeError("set setting", scope)
	}
	if key == "" {
		return fmt.Errorf("store: set setting: %w", errEmptyKey)
	}
	if _, err := s.db.Exec(upsertSetting, scope, key, value, time.Now().Unix()); err != nil {
		return fmt.Errorf("store: set setting %s/%s: %w", scope, key, err)
	}
	return nil
}

// DeleteSetting removes a key from scope. Deleting a key that is not present
// is not an error. A scope outside the known pair or an empty key is refused.
func (s *Store) DeleteSetting(scope, key string) error {
	if !validScope(scope) {
		return scopeError("delete setting", scope)
	}
	if key == "" {
		return fmt.Errorf("store: delete setting: %w", errEmptyKey)
	}
	if _, err := s.db.Exec(deleteSetting, scope, key); err != nil {
		return fmt.Errorf("store: delete setting %s/%s: %w", scope, key, err)
	}
	return nil
}

// pragmas are applied through the DSN rather than a post-open Exec: database/sql
// may hand out a fresh connection at any time, and an Exec would reach only the
// one it happened to borrow.
var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
}

// dsn names the database for modernc.org/sqlite. The path is made a URI so
// spaces and '?' survive; the pragmas are appended literally because the driver
// matches their parenthesised form.
func dsn(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=" + strings.Join(pragmas, "&_pragma=")
}

// migrate brings the database to schemaVersion. It creates the schema table,
// reads the stored version, and applies each forward step in its own
// transaction; a version newer than this build supports is refused rather than
// downgraded.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(createSchema); err != nil {
		return fmt.Errorf("store: create schema table: %w", err)
	}
	if _, err := db.Exec(insertSchemaVersion); err != nil {
		return fmt.Errorf("store: initialise schema version: %w", err)
	}

	var version int
	if err := db.QueryRow(selectSchemaVersion).Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("store: schema version %d is newer than this build supports (%d): refusing to downgrade", version, schemaVersion)
	}
	for v := version; v < schemaVersion; v++ {
		if v >= len(migrations) {
			return fmt.Errorf("store: no migration from version %d (current %d)", v, schemaVersion)
		}
		if err := applyMigration(db, v); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs the step from version from to from+1 and records the new
// version in the same transaction, so a crash leaves the database at the old
// version with no partial step committed.
func applyMigration(db *sql.DB, from int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate v%d: begin: %w", from, err)
	}
	for _, stmt := range migrations[from] {
		if _, err := tx.Exec(stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migrate v%d: %w", from, err)
		}
	}
	if _, err := tx.Exec(updateSchemaVersion, from+1); err != nil {
		tx.Rollback()
		return fmt.Errorf("store: migrate v%d: record version: %w", from, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate v%d: commit: %w", from, err)
	}
	return nil
}

// JournalEntry is one dirty buffer's persisted session: the encoded
// piecetable snapshot, the version whose agreed composition matches the disk,
// the file encoding the bytes were read in, and the SHA-256 of those bytes.
// Updated is a unix time for diagnostics, not part of the entry's identity.
type JournalEntry struct {
	Path     string
	Snapshot []byte
	Saved    uint64
	Encoding []byte
	Digest   []byte
	Updated  int64
}

// PutJournal stores entry, replacing any row for the same path. An empty path
// or an empty snapshot is refused: neither can describe a buffer.
func (s *Store) PutJournal(entry JournalEntry) error {
	if entry.Path == "" {
		return fmt.Errorf("store: put journal: %w", errEmptyPath)
	}
	if len(entry.Snapshot) == 0 {
		return fmt.Errorf("store: put journal %s: %w", entry.Path, errEmptyJournal)
	}
	if _, err := s.db.Exec(upsertJournal, entry.Path, entry.Snapshot, int64(entry.Saved),
		string(entry.Encoding), entry.Digest, time.Now().Unix()); err != nil {
		return fmt.Errorf("store: put journal %s: %w", entry.Path, err)
	}
	return nil
}

// Journal returns the row for path and whether one is present.
func (s *Store) Journal(path string) (JournalEntry, bool, error) {
	if path == "" {
		return JournalEntry{}, false, fmt.Errorf("store: get journal: %w", errEmptyPath)
	}
	var e JournalEntry
	var saved int64
	switch err := s.db.QueryRow(selectJournal, path).Scan(
		&e.Path, &e.Snapshot, &saved, &e.Encoding, &e.Digest, &e.Updated,
	); err {
	case nil:
		e.Saved = uint64(saved)
		return e, true, nil
	case sql.ErrNoRows:
		return JournalEntry{}, false, nil
	default:
		return JournalEntry{}, false, fmt.Errorf("store: get journal %s: %w", path, err)
	}
}

// DeleteJournal removes the row for path. A missing row is not an error: the
// caller is asserting that disk is now the state, and that is already true.
func (s *Store) DeleteJournal(path string) error {
	if path == "" {
		return fmt.Errorf("store: delete journal: %w", errEmptyPath)
	}
	if _, err := s.db.Exec(deleteJournal, path); err != nil {
		return fmt.Errorf("store: delete journal %s: %w", path, err)
	}
	return nil
}
