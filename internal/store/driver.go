package store

// modernc.org/sqlite is a pure-Go SQLite, so CGO_ENABLED=0 and a cross-compile
// to GOOS=linux both still work. It is imported for its side effect of
// registering the "sqlite" driver with database/sql, and this is the only file
// in the package that names it: every other file type-checks against the
// stdlib interfaces even before the module is downloaded by `go mod tidy`.
import _ "modernc.org/sqlite"
