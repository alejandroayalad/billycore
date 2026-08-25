package sqlite

import "embed"

// migrationFS holds the schema, compiled into the binary.
//
// DATA_MODEL.md §8: migrations are embedded, numbered, forward-only, and run at
// startup. Embedding is what makes D4 true — one binary, no migration step the
// user runs, and no way for the schema on disk to disagree with the code that
// queries it.
//
//go:embed migrations/*.sql
var migrationFS embed.FS
