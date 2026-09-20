package store

// schemaVersion is the version this build writes. Open migrates a database
// forward to it and refuses one that is newer, so an older binary never
// silently downgrades state written by a newer one.
const schemaVersion = 2

// createSchema and its companions are idempotent so that two Opens racing on a
// fresh database both succeed: the loser either sees the table already made or
// inserts nothing.
const createSchema = `
CREATE TABLE IF NOT EXISTS schema (
	id      INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
)`

const insertSchemaVersion = `INSERT OR IGNORE INTO schema (id, version) VALUES (1, 0)`

const selectSchemaVersion = `SELECT version FROM schema WHERE id = 1`

const updateSchemaVersion = `UPDATE schema SET version = ?`

// migrations holds one statement list per version step: migrations[v] moves a
// database from version v to v+1, applied in a transaction with the version
// update. Every statement is CREATE ... IF NOT EXISTS, so re-running a
// migration after a crash or a lost race is harmless.
var migrations = [][]string{
	// v0 -> v1: the session blob, per-file positions and scoped settings.
	{
		`CREATE TABLE IF NOT EXISTS session (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			blob    BLOB NOT NULL,
			updated INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS positions (
			path    TEXT PRIMARY KEY,
			cursor  INTEGER NOT NULL,
			top     INTEGER NOT NULL,
			updated INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			scope   TEXT NOT NULL,
			key     TEXT NOT NULL,
			value   TEXT NOT NULL,
			updated INTEGER NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
	},
	// v1 -> v2: the per-buffer journal. One row per dirty local buffer, keyed by
	// its path: the exact session snapshot, the version whose agreed composition
	// matches disk, the encoding the bytes were read in, and the digest of those
	// bytes. A save deletes the row; disk is the state then.
	{
		`CREATE TABLE IF NOT EXISTS journal (
			path     TEXT PRIMARY KEY,
			snapshot BLOB NOT NULL,
			saved    INTEGER NOT NULL,
			encoding TEXT NOT NULL,
			digest   BLOB NOT NULL,
			updated  INTEGER NOT NULL
		)`,
	},
}

// The statements behind the Store methods, one named constant each, so the
// shape of the storage lives in one file and the methods read as verbs.
const upsertSession = `
INSERT INTO session (id, blob, updated) VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET blob = excluded.blob, updated = excluded.updated`

const selectSession = `SELECT blob FROM session WHERE id = 1`

const upsertPosition = `
INSERT INTO positions (path, cursor, top, updated) VALUES (?, ?, ?, ?)
ON CONFLICT (path) DO UPDATE
SET cursor = excluded.cursor, top = excluded.top, updated = excluded.updated`

const selectPosition = `SELECT path, cursor, top, updated FROM positions WHERE path = ?`

const selectPositions = `SELECT path, cursor, top, updated FROM positions ORDER BY path`

const selectSettings = `SELECT key, value FROM settings WHERE scope = ? ORDER BY key`

const upsertSetting = `
INSERT INTO settings (scope, key, value, updated) VALUES (?, ?, ?, ?)
ON CONFLICT (scope, key) DO UPDATE
SET value = excluded.value, updated = excluded.updated`

const deleteSetting = `DELETE FROM settings WHERE scope = ? AND key = ?`

const upsertJournal = `
INSERT INTO journal (path, snapshot, saved, encoding, digest, updated)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (path) DO UPDATE SET
	snapshot = excluded.snapshot,
	saved    = excluded.saved,
	encoding = excluded.encoding,
	digest   = excluded.digest,
	updated  = excluded.updated`

const selectJournal = `SELECT path, snapshot, saved, encoding, digest, updated FROM journal WHERE path = ?`

const deleteJournal = `DELETE FROM journal WHERE path = ?`
