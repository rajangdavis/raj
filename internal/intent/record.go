package intent

import (
	"time"

	"raj/internal/store"
)

// Record is one export row: the intention and member groups that produced a
// commit, and the commit, parent, base and tree it produced. It is bookkeeping
// for the export, not a provenance record: provenance lives in the raj journal,
// and this row only joins an intention to the object ids it wrote.
type Record struct {
	Intention string
	Groups    []Member
	CommitSHA string
	ParentSHA string
	BaseSHA   string
	TreeSHA   string
	Time      time.Time
}

// SaveRecord writes rec through the workspace store. The store owns the table;
// this is the one place the two worlds are joined.
func SaveRecord(s *store.Store, rec Record) error {
	groups := make([]store.IntentionMember, 0, len(rec.Groups))
	for _, m := range rec.Groups {
		groups = append(groups, store.IntentionMember{ID: m.ID, Path: m.Path})
	}
	return s.PutExport(store.ExportRecord{
		Intention: rec.Intention,
		Groups:    groups,
		CommitSHA: rec.CommitSHA,
		ParentSHA: rec.ParentSHA,
		BaseSHA:   rec.BaseSHA,
		TreeSHA:   rec.TreeSHA,
		Time:      rec.Time.UnixMilli(),
	})
}
