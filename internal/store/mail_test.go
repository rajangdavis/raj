package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestMailRoundTrip covers insert, the undelivered read, delivery marking and
// the prune that removes confirmed rows while leaving unread ones.
func TestMailRoundTrip(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if rows, err := s.LoadUndeliveredMail("raj-a"); err != nil || len(rows) != 0 {
		t.Fatalf("LoadUndeliveredMail on a fresh store = %v, %v; want none", rows, err)
	}

	first, err := s.InsertMail(Mail{ToIdentity: "raj-a", FromAuthor: 3, FromKey: "raj-b", FromName: "peer", Text: "one"})
	if err != nil {
		t.Fatalf("InsertMail: %v", err)
	}
	second, err := s.InsertMail(Mail{ToIdentity: "raj-a", FromAuthor: 1, Text: "two"})
	if err != nil {
		t.Fatalf("InsertMail: %v", err)
	}
	if _, err := s.InsertMail(Mail{ToIdentity: "raj-b", Text: "other"}); err != nil {
		t.Fatalf("InsertMail raj-b: %v", err)
	}

	// Oldest first, and the durable reply target survives the round trip even
	// though its author id is only a display number.
	rows, err := s.LoadUndeliveredMail("raj-a")
	if err != nil {
		t.Fatalf("LoadUndeliveredMail: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != first || rows[0].Text != "one" || rows[1].ID != second || rows[1].Text != "two" {
		t.Fatalf("LoadUndeliveredMail = %+v; want first then second", rows)
	}
	if rows[0].FromKey != "raj-b" || rows[0].FromName != "peer" {
		t.Errorf("reply target = %q/%q, want raj-b/peer", rows[0].FromKey, rows[0].FromName)
	}

	if other, err := s.LoadUndeliveredMail("raj-b"); err != nil || len(other) != 1 || other[0].Text != "other" {
		t.Fatalf("raj-b mail = %+v, %v; want one message", other, err)
	}

	if err := s.MarkMailDelivered([]int64{first}); err != nil {
		t.Fatalf("MarkMailDelivered: %v", err)
	}
	rows, err = s.LoadUndeliveredMail("raj-a")
	if err != nil || len(rows) != 1 || rows[0].ID != second {
		t.Fatalf("after delivery = %+v, %v; want only the second", rows, err)
	}

	if err := s.PruneDeliveredMail(); err != nil {
		t.Fatalf("PruneDeliveredMail: %v", err)
	}
	rows, err = s.LoadUndeliveredMail("raj-a")
	if err != nil || len(rows) != 1 || rows[0].ID != second {
		t.Fatalf("after prune = %+v, %v; want only the second", rows, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM mail`).Scan(&count); err != nil {
		t.Fatalf("count mail: %v", err)
	}
	if count != 2 {
		t.Fatalf("mail rows = %d, want 2 (the unread pair)", count)
	}
}

// TestMailBoundaries pins the refusals: a row without a recipient or text can
// never be delivered, and an empty delivery list is a harmless no-op.
func TestMailBoundaries(t *testing.T) {
	s := mustOpen(t, filepath.Join(t.TempDir(), "state.db"))

	if _, err := s.InsertMail(Mail{Text: "x"}); !errors.Is(err, errEmptyIdentity) {
		t.Fatalf("InsertMail without an identity error = %v, want empty identity", err)
	}
	if _, err := s.InsertMail(Mail{ToIdentity: "raj-a"}); !errors.Is(err, errEmptyMailText) {
		t.Fatalf("InsertMail without text error = %v, want empty mail text", err)
	}
	if _, err := s.LoadUndeliveredMail(""); !errors.Is(err, errEmptyIdentity) {
		t.Fatalf("LoadUndeliveredMail empty identity error = %v, want empty identity", err)
	}
	if err := s.MarkMailDelivered(nil); err != nil {
		t.Fatalf("MarkMailDelivered(nil) = %v, want nil", err)
	}
}

// TestMailMigrationFromV4 covers the forward step: a database written at v4
// gains the mail table and its index and moves to the current version, with
// rows written before the migration still present.
func TestMailMigrationFromV4(t *testing.T) {
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
	// Build v4 by applying the first four steps by hand, exactly as a build
	// that predates this one would have left it.
	for v := 0; v < 4; v++ {
		for _, stmt := range migrations[v] {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("build v%d: %v", v+1, err)
			}
		}
	}
	if _, err := raw.Exec(updateSchemaVersion, 4); err != nil {
		t.Fatalf("record v4: %v", err)
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
	// The mail table is usable after the step and the recipient index exists.
	id, err := s.InsertMail(Mail{ToIdentity: "raj-a", Text: "after migration"})
	if err != nil {
		t.Fatalf("InsertMail after migration: %v", err)
	}
	if rows, err := s.LoadUndeliveredMail("raj-a"); err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("LoadUndeliveredMail after migration = %+v, %v; want the inserted row", rows, err)
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'mail_to_identity'`).Scan(&name); err != nil {
		t.Fatalf("mail index missing after migration: %v", err)
	}
}
