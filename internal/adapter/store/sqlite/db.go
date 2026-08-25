// Package sqlite opens BillyCore's database and applies its schema.
//
// It is an adapter: nothing here knows what Evidence means (D6). See
// ARCHITECTURE.md §7 for why SQLite, and DATA_MODEL.md §4 for the pragmas.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// BusyTimeout is how long a connection waits for a lock before returning
// SQLITE_BUSY.
//
// ARCHITECTURE.md §7 requires a busy timeout and DATA_MODEL.md §4 declines to
// invent a value. Five seconds is the conventional default: long enough that a
// concurrent writer in a single-user daemon never surfaces as an error, short
// enough that a genuinely stuck lock is still reported rather than hung on.
const BusyTimeout = "5000"

// Open opens the database, applies the pragmas, and migrates it forward.
//
// It returns an error rather than a half-configured handle: a database that is
// open but not migrated is worse than one that never opened, because the first
// query is what discovers the problem.
func Open(path string) (*sql.DB, error) {
	// Create the file at 0600 before SQLite can create it at whatever the umask
	// allows. D15 makes file permissions the only protection this data has —
	// there is no encryption at rest — so the mode is not a detail.
	if err := ensureFileMode(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := verifyPragmas(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	// WAL mode creates two more files holding the same financial data, and they
	// are created by SQLite, after the open, with the default mode.
	if err := ensureSidecarModes(path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dsn carries the pragmas on the connection string rather than issuing them
// after opening.
//
// foreign_keys and busy_timeout are per-connection (DATA_MODEL.md §4), and
// database/sql opens connections whenever the pool decides to — one that skipped
// these would enforce no foreign keys and fail instantly under contention. The
// DSN is the only place that applies to every connection, including the ones
// opened later.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout("+BusyTimeout+")")
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

// verifyPragmas reads back what dsn asked for.
//
// An unrecognized DSN parameter is not an error the driver reports; it is a
// setting that silently did not happen. Foreign keys being off is invisible
// until the day a delete removes provenance, so it is checked at startup
// instead.
func verifyPragmas(db *sql.DB) error {
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys is off: refusing to run without referential integrity")
	}
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode pragma: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("journal_mode is %q, want wal", journalMode)
	}
	return nil
}

func ensureFileMode(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open database file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	// An existing file may predate this rule, or have been restored from a
	// backup by a copy that widened it.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("cannot set 0600 on %s: %w", path, err)
	}
	return nil
}

// ensureSidecarModes tightens the WAL and shared-memory files.
//
// A -wal file holds committed rows that have not been checkpointed yet: it is
// the same financial history as billy.db, and leaving it world-readable would
// undo the mode on the database itself.
func ensureSidecarModes(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		p := path + suffix
		if _, err := os.Stat(p); err != nil {
			continue // not created yet; it inherits from billy.db when it is
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return fmt.Errorf("cannot set 0600 on %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}
