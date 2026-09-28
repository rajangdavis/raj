package store

import (
	"fmt"
	"strings"
	"time"
)

// Mail is one queued message addressed to a durable identity. It is the
// mailbox's durable half: the in-memory Mailbox carries the live copy, and a
// row here is what lets an editor restart hand a message to a participant that
// had not read it yet.
//
// FromAuthor is the sender's author id at the time the message was written. It
// is a number a restart re-seeds, so it is carried for display only; FromKey
// and FromName are the durable reply target and are what make a replayed
// message addressable after its sender has gone.
type Mail struct {
	ID          int64
	ToIdentity  string
	FromAuthor  int
	FromKey     string
	FromName    string
	Text        string
	CreatedMS   int64
	DeliveredMS int64
}

// InsertMail stores one undelivered message for m.ToIdentity and returns its
// row id. The recipient identity and the text are required: a row with neither
// cannot be delivered or read. CreatedMS is stamped here when the caller
// leaves it zero.
func (s *Store) InsertMail(m Mail) (int64, error) {
	if m.ToIdentity == "" {
		return 0, fmt.Errorf("store: insert mail: %w", errEmptyIdentity)
	}
	if m.Text == "" {
		return 0, fmt.Errorf("store: insert mail for %s: %w", m.ToIdentity, errEmptyMailText)
	}
	created := m.CreatedMS
	if created == 0 {
		created = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(insertMail, m.ToIdentity, m.FromAuthor, m.FromKey, m.FromName, m.Text, created)
	if err != nil {
		return 0, fmt.Errorf("store: insert mail for %s: %w", m.ToIdentity, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert mail for %s: %w", m.ToIdentity, err)
	}
	return id, nil
}

// MarkMailDelivered stamps the rows ids as delivered at the current time. An
// empty list is a no-op, so a caller with nothing to confirm does not build an
// empty IN clause.
func (s *Store) MarkMailDelivered(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, time.Now().UnixMilli())
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args = append(args, id)
		placeholders[i] = "?"
	}
	stmt := fmt.Sprintf(markMailDelivered, strings.Join(placeholders, ", "))
	if _, err := s.db.Exec(stmt, args...); err != nil {
		return fmt.Errorf("store: mark mail delivered: %w", err)
	}
	return nil
}

// LoadUndeliveredMail returns identity's unread messages, oldest first by
// write time and then row id. It is never nil, so a caller can range over it
// without a nil check. An empty identity is refused.
func (s *Store) LoadUndeliveredMail(identity string) ([]Mail, error) {
	if identity == "" {
		return nil, fmt.Errorf("store: load mail: %w", errEmptyIdentity)
	}
	rows, err := s.db.Query(selectUndeliveredMail, identity)
	if err != nil {
		return nil, fmt.Errorf("store: load mail for %s: %w", identity, err)
	}
	defer rows.Close()

	out := make([]Mail, 0)
	for rows.Next() {
		var m Mail
		if err := rows.Scan(&m.ID, &m.FromAuthor, &m.FromKey, &m.FromName, &m.Text, &m.CreatedMS, &m.DeliveredMS); err != nil {
			return nil, fmt.Errorf("store: scan mail: %w", err)
		}
		m.ToIdentity = identity
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: load mail for %s: %w", identity, err)
	}
	return out, nil
}

// PruneDeliveredMail removes every row a recipient has already confirmed. It
// runs at start, so the delivered marker can close the crash window between a
// hand-off and its confirmation without keeping the rows forever.
func (s *Store) PruneDeliveredMail() error {
	if _, err := s.db.Exec(pruneDeliveredMail); err != nil {
		return fmt.Errorf("store: prune delivered mail: %w", err)
	}
	return nil
}

// The mail statements, one named constant each, mirroring the schema
// constants. The delivered predicate is `!= 0` rather than `> 0` so a row
// stamped by a clock that went backwards is still pruned.
const insertMail = `
INSERT INTO mail (to_identity, from_author, from_key, from_name, text, created_ms, delivered_ms)
VALUES (?, ?, ?, ?, ?, ?, 0)`

const markMailDelivered = `UPDATE mail SET delivered_ms = ? WHERE id IN (%s)`

const selectUndeliveredMail = `
SELECT id, from_author, from_key, from_name, text, created_ms, delivered_ms
FROM mail WHERE to_identity = ? AND delivered_ms = 0
ORDER BY created_ms, id`

const pruneDeliveredMail = `DELETE FROM mail WHERE delivered_ms != 0`
