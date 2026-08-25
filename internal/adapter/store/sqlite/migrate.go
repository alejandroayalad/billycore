package sqlite

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// migration is one numbered, forward-only schema change.
type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every migration newer than PRAGMA user_version, in order.
//
// DATA_MODEL.md §8: numbered, forward-only, one transaction each, and BillyCore
// does not start if one fails. There are no down migrations — reversing a schema
// change on a database holding the only copy of someone's financial history is
// not a feature, it is a way to lose it.
func Migrate(db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if n := len(migrations); n > 0 && current > migrations[n-1].version {
		// The file was written by a newer BillyCore. Running an older binary
		// against it would query columns that may not mean what it thinks.
		return fmt.Errorf("database is at migration %d but this binary only knows %d: refusing to run against a newer schema",
			current, migrations[n-1].version)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and its version bump in a single
// transaction. A migration that half-applied would leave the database in a
// state no version number describes.
func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	// PRAGMA takes no placeholders; the value is an int parsed from a filename
	// in the embedded FS, not input.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("migration %s: set user_version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}

// loadMigrations reads the embedded migrations, ordered by version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var migrations []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := versionOf(e.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			// Two migrations claiming one version means one of them silently
			// never runs, depending on sort order.
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := fs.ReadFile(migrationFS, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: e.Name(), sql: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// versionOf reads the leading number of a migration filename: 001_initial.sql
// is version 1.
func versionOf(name string) (int, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf("migration %q is not named <version>_<description>.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		// user_version starts at 0, so 0 is not an available version number.
		return 0, fmt.Errorf("migration %q does not start with a positive version number", name)
	}
	return version, nil
}
