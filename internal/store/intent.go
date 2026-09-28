package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Errors the intention methods refuse by name.
var (
	errEmptyIntentionName = errors.New("empty intention name")
	errEmptyExportTree    = errors.New("empty export tree")
	errEmptyExportCommit  = errors.New("empty export commit")
)

// IntentionMember is one membership entry as stored: a group id qualified by
// the workspace-relative path of the buffer that numbers it. The pair is what
// makes an id unambiguous across buffers. A legacy row stored a bare id, which
// decodes as a member with an empty path.
type IntentionMember struct {
	Path string `json:"path"`
	ID   uint64 `json:"id"`
}

// IntentionRow is one stored intention. Members is the JSON encoding of the
// qualified membership. BaseSHA and Created are the deterministic-export
// metadata: the pinned base commit and the pinned creation time.
type IntentionRow struct {
	Name    string
	Owner   string
	Base    string
	Task    string
	Members []IntentionMember
	State   string
	BaseSHA string
	Created int64
}

// PutIntention stores row, replacing any row of the same name. An empty name is
// refused.
func (s *Store) PutIntention(row IntentionRow) error {
	if row.Name == "" {
		return fmt.Errorf("store: put intention: %w", errEmptyIntentionName)
	}
	members, err := json.Marshal(row.Members)
	if err != nil {
		return fmt.Errorf("store: put intention %s: %w", row.Name, err)
	}
	if _, err := s.db.Exec(upsertIntention, row.Name, row.Owner, row.Base, row.Task, string(members),
		row.State, row.BaseSHA, row.Created); err != nil {
		return fmt.Errorf("store: put intention %s: %w", row.Name, err)
	}
	return nil
}

// Intention returns the row for name and whether one is present. An empty name
// is refused.
func (s *Store) Intention(name string) (IntentionRow, bool, error) {
	if name == "" {
		return IntentionRow{}, false, fmt.Errorf("store: get intention: %w", errEmptyIntentionName)
	}
	var row IntentionRow
	var members string
	switch err := s.db.QueryRow(selectIntention, name).Scan(
		&row.Name, &row.Owner, &row.Base, &row.Task, &members, &row.State, &row.BaseSHA, &row.Created,
	); err {
	case nil:
		row.Members = decodeMembers(members)
		return row, true, nil
	case sql.ErrNoRows:
		return IntentionRow{}, false, nil
	default:
		return IntentionRow{}, false, fmt.Errorf("store: get intention %s: %w", name, err)
	}
}

// Intentions returns every intention, sorted by name. It is never nil.
func (s *Store) Intentions() ([]IntentionRow, error) {
	rows, err := s.db.Query(selectIntentions)
	if err != nil {
		return nil, fmt.Errorf("store: list intentions: %w", err)
	}
	defer rows.Close()

	out := make([]IntentionRow, 0)
	for rows.Next() {
		var row IntentionRow
		var members string
		if err := rows.Scan(&row.Name, &row.Owner, &row.Base, &row.Task, &members,
			&row.State, &row.BaseSHA, &row.Created); err != nil {
			return nil, fmt.Errorf("store: scan intention: %w", err)
		}
		row.Members = decodeMembers(members)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list intentions: %w", err)
	}
	return out, nil
}

// DeleteIntention removes the row for name. Deleting an absent name is not an
// error; an empty name is refused.
func (s *Store) DeleteIntention(name string) error {
	if name == "" {
		return fmt.Errorf("store: delete intention: %w", errEmptyIntentionName)
	}
	if _, err := s.db.Exec(deleteIntention, name); err != nil {
		return fmt.Errorf("store: delete intention %s: %w", name, err)
	}
	return nil
}

// ExportRecord is one export row: the intention and member groups that produced
// a commit, and the commit, parent, base and tree it produced. Groups is the
// JSON encoding of the qualified membership. It is one row per export.
type ExportRecord struct {
	ID        int64
	Intention string
	Groups    []IntentionMember
	CommitSHA string
	ParentSHA string
	BaseSHA   string
	TreeSHA   string
	Time      int64
}

// PutExport appends rec. An export with no intention, commit or tree is
// refused: it would join nothing.
func (s *Store) PutExport(rec ExportRecord) error {
	if rec.Intention == "" {
		return fmt.Errorf("store: put export: %w", errEmptyIntentionName)
	}
	if rec.TreeSHA == "" {
		return fmt.Errorf("store: put export %s: %w", rec.Intention, errEmptyExportTree)
	}
	if rec.CommitSHA == "" {
		return fmt.Errorf("store: put export %s: %w", rec.Intention, errEmptyExportCommit)
	}
	groups, err := json.Marshal(rec.Groups)
	if err != nil {
		return fmt.Errorf("store: put export %s: %w", rec.Intention, err)
	}
	if _, err := s.db.Exec(insertExport, rec.Intention, string(groups),
		rec.CommitSHA, rec.ParentSHA, rec.BaseSHA, rec.TreeSHA, rec.Time); err != nil {
		return fmt.Errorf("store: put export %s: %w", rec.Intention, err)
	}
	return nil
}

// Exports returns an intention's exports, oldest first. It is never nil.
func (s *Store) Exports(intention string) ([]ExportRecord, error) {
	if intention == "" {
		return nil, fmt.Errorf("store: list exports: %w", errEmptyIntentionName)
	}
	rows, err := s.db.Query(selectExports, intention)
	if err != nil {
		return nil, fmt.Errorf("store: list exports %s: %w", intention, err)
	}
	defer rows.Close()

	out := make([]ExportRecord, 0)
	for rows.Next() {
		rec, err := scanExport(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan export: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list exports %s: %w", intention, err)
	}
	return out, nil
}

// LastExport returns an intention's most recent export and whether one is
// present. It is the chain head a later export parents on.
func (s *Store) LastExport(intention string) (ExportRecord, bool, error) {
	if intention == "" {
		return ExportRecord{}, false, fmt.Errorf("store: last export: %w", errEmptyIntentionName)
	}
	var rec ExportRecord
	var groups string
	switch err := s.db.QueryRow(selectLastExport, intention).Scan(
		&rec.ID, &rec.Intention, &groups, &rec.CommitSHA, &rec.ParentSHA,
		&rec.BaseSHA, &rec.TreeSHA, &rec.Time,
	); err {
	case nil:
		rec.Groups = decodeMembers(groups)
		return rec, true, nil
	case sql.ErrNoRows:
		return ExportRecord{}, false, nil
	default:
		return ExportRecord{}, false, fmt.Errorf("store: last export %s: %w", intention, err)
	}
}

// scanExport reads one SELECT row into an ExportRecord.
func scanExport(rows *sql.Rows) (ExportRecord, error) {
	var rec ExportRecord
	var groups string
	if err := rows.Scan(&rec.ID, &rec.Intention, &groups, &rec.CommitSHA, &rec.ParentSHA,
		&rec.BaseSHA, &rec.TreeSHA, &rec.Time); err != nil {
		return ExportRecord{}, err
	}
	rec.Groups = decodeMembers(groups)
	return rec, nil
}

// decodeMembers reads a JSON membership value. The current shape is an array of
// {path, id} objects; a legacy row stored an array of bare ids, which decodes
// as members with an empty path so an old row stays loadable. A malformed value
// reads as no members rather than failing a listing.
func decodeMembers(s string) []IntentionMember {
	if s == "" {
		return nil
	}
	var members []IntentionMember
	if err := json.Unmarshal([]byte(s), &members); err == nil {
		return members
	}
	var ids []uint64
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil
	}
	members = make([]IntentionMember, 0, len(ids))
	for _, id := range ids {
		members = append(members, IntentionMember{ID: id})
	}
	return members
}

// The intention and export statements, one named constant each, mirroring the
// schema constants.
const upsertIntention = `
INSERT INTO intentions (name, owner, base, task, members, state, base_sha, created_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET
	owner      = excluded.owner,
	base       = excluded.base,
	task       = excluded.task,
	members    = excluded.members,
	state      = excluded.state,
	base_sha   = excluded.base_sha,
	created_ms = excluded.created_ms`

const selectIntention = `
SELECT name, owner, base, task, members, state, base_sha, created_ms
FROM intentions WHERE name = ?`

const selectIntentions = `
SELECT name, owner, base, task, members, state, base_sha, created_ms
FROM intentions ORDER BY name`

const deleteIntention = `DELETE FROM intentions WHERE name = ?`

const insertExport = `
INSERT INTO intent_exports
	(intention, groups_json, commit_sha, parent_commit_sha, base_commit_sha, tree_sha, exported_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`

const selectExports = `
SELECT id, intention, groups_json, commit_sha, parent_commit_sha, base_commit_sha, tree_sha, exported_ms
FROM intent_exports WHERE intention = ? ORDER BY id`

const selectLastExport = `
SELECT id, intention, groups_json, commit_sha, parent_commit_sha, base_commit_sha, tree_sha, exported_ms
FROM intent_exports WHERE intention = ? ORDER BY id DESC LIMIT 1`
