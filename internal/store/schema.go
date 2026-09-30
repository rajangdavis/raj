package store

// schemaVersion is the version this build writes. Open migrates a database
// forward to it and refuses one that is newer, so an older binary never
// silently downgrades state written by a newer one.
const schemaVersion = 10

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

// hookTreeProjected is the hooks.tree default: a row written before the v3 -> v4
// column, or one stored without a choice, runs against the projected scratch
// tree. It repeats the hooks domain's TreeProjected value because the store
// keeps its columns as plain text and does not import the policy package.
const hookTreeProjected = "projected"

// hookParamsDefault is the hooks.params default: the JSON empty declaration
// array. A row written before the v9 -> v10 column takes no parameters, which
// is the behaviour every hook had before declared parameters existed.
const hookParamsDefault = "[]"

// migrations holds one statement list per version step: migrations[v] moves a
// database from version v to v+1, applied in a transaction with the version
// update. Every statement is idempotent: table creation uses CREATE ... IF NOT
// EXISTS, and an ADD COLUMN whose column is already present is skipped by
// applyMigration, so re-running a step after a crash or a lost race is
// harmless.
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
	// v2 -> v3: the workspace's hook definitions. One row per named hook: the
	// stored action JSON, its trigger, whether agents may call it, the cooldown
	// and timeout in milliseconds, the write guard and the enabled flag. The
	// contents of action and trigger are the hooks service's to validate.
	{
		`CREATE TABLE IF NOT EXISTS hooks (
			name        TEXT PRIMARY KEY,
			action      TEXT NOT NULL,
			trigger     TEXT NOT NULL,
			agent       INTEGER NOT NULL,
			cooldown_ms INTEGER NOT NULL,
			timeout_ms  INTEGER NOT NULL,
			may_write   INTEGER NOT NULL,
			enabled     INTEGER NOT NULL,
			updated     INTEGER NOT NULL
		)`,
	},
	// v3 -> v4: where a hook runs and whether it outlives its request.
	// projected (the default) keeps the v0 scratch-tree behaviour and workspace
	// runs against the saved workspace root; detach (default off) starts the run
	// in its own session. The columns are added to the existing table, and the
	// defaults backfill every row written before this step, so a stored hook
	// needs no rewrite.
	{
		`ALTER TABLE hooks ADD COLUMN tree TEXT NOT NULL DEFAULT 'projected'`,
		`ALTER TABLE hooks ADD COLUMN detach INTEGER NOT NULL DEFAULT 0`,
	},
	// v4 -> v5: the durable mail queue. One row per undelivered message,
	// keyed by the recipient's durable identity rather than its author id,
	// which a restart re-seeds. delivered_ms is zero while unread and a unix
	// time once the recipient's next parked recv confirms the batch, so the
	// marker closes the crash window between hand-off and confirmation
	// instead of the row being deleted early. The index is on the recipient
	// because every start and every join loads that identity's rows.
	{
		`CREATE TABLE IF NOT EXISTS mail (
			id           INTEGER PRIMARY KEY,
			to_identity  TEXT NOT NULL,
			from_author  INTEGER,
			from_key     TEXT,
			from_name    TEXT,
			text         TEXT NOT NULL,
			created_ms   INTEGER,
			delivered_ms INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS mail_to_identity ON mail (to_identity)`,
	},
	// v5 -> v6: intentions and their export records. An intention is a named,
	// owned selection of groups over a base; the export table is one row per
	// export, joining the intention and its groups to a commit, parent, base and
	// tree. members and groups_json are JSON arrays of group ids, so the store
	// keeps its columns plain text and owns no domain type.
	{
		`CREATE TABLE IF NOT EXISTS intentions (
			name       TEXT PRIMARY KEY,
			owner      TEXT NOT NULL,
			base       TEXT NOT NULL,
			members    TEXT NOT NULL,
			state      TEXT NOT NULL,
			base_sha   TEXT NOT NULL,
			created_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS intent_exports (
			id                INTEGER PRIMARY KEY,
			intention         TEXT NOT NULL,
			groups_json       TEXT NOT NULL,
			commit_sha        TEXT NOT NULL,
			parent_commit_sha TEXT NOT NULL,
			base_commit_sha   TEXT NOT NULL,
			tree_sha          TEXT NOT NULL,
			exported_ms       INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS intent_exports_intention ON intent_exports (intention)`,
	},
	// v6 -> v7: the task an intention was created for (D6). It is stored so an
	// export that passes no --task still carries it in the note; the default
	// backfills every intention written before this step.
	{
		`ALTER TABLE intentions ADD COLUMN task TEXT NOT NULL DEFAULT ''`,
	},
	// v7 -> v8: intention membership becomes qualified. A member is a group id
	// plus the workspace-relative path of the buffer that numbers it, because an
	// id is unique only within one session. The columns are JSON text: the old
	// array-of-ids is rewritten to an array of {path:"",id:N} objects, and the
	// Go decoder still reads a bare array, so a row that was never rewritten
	// stays loadable. An empty path is a legacy member resolved by id.
	{
		`UPDATE intentions
		 SET members = (
			SELECT json_group_array(json_object('path', '', 'id', value))
			FROM json_each(intentions.members)
		 )
		 WHERE json_valid(members)
		   AND json_type(members) = 'array'
		   AND json_array_length(members) > 0
		   AND json_type(members, '$[0]') = 'integer'`,
		`UPDATE intent_exports
		 SET groups_json = (
			SELECT json_group_array(json_object('path', '', 'id', value))
			FROM json_each(intent_exports.groups_json)
		 )
		 WHERE json_valid(groups_json)
		   AND json_type(groups_json) = 'array'
		   AND json_array_length(groups_json) > 0
		   AND json_type(groups_json, '$[0]') = 'integer'`,
	},
	// v8 -> v9: retained as a no-op. v9 once added the intent_publishes table;
	// a publish's result now lives only in the response, so this build stops
	// creating and using the table. The version stays 9 rather than stepping
	// back, so a database a build with the table already stamped v9 is read as
	// current rather than refused; the table, if it exists, is left in place and
	// is never read or written again.
	nil,
	// v9 -> v10: a hook's declared parameters. One JSON array of declarations
	// per row; the default '[]' backfills every row written before this step,
	// so an existing hook takes no parameters and behaves exactly as it did.
	// The array is decoded and validated by internal/hooks, not here, because
	// the store keeps its columns as plain text.
	{
		`ALTER TABLE hooks ADD COLUMN params TEXT NOT NULL DEFAULT '[]'`,
	},
}

// The statements behind the Store methods, one named constant each, so the
// methods read as verbs. The hook statements live in hooks.go next to the
// type they serve.
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
