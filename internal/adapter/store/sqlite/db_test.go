package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

func testDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "billy.db")
}

func TestOpenAppliesPragmasAndSchema(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys, busyTimeout, userVersion int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if userVersion != 1 {
		t.Errorf("user_version = %d, want 1 after migration 001", userVersion)
	}

	for _, table := range []string{"evidence", "domain_event"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing after migration 001: %v", table, err)
		}
	}

	var index string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_evidence_pipeline_scan'`).Scan(&index)
	if err != nil {
		t.Errorf("idx_evidence_pipeline_scan missing: %v", err)
	}
}

// Migration 001 creates only what M1 writes to. A table appearing early is a
// schema nobody has exercised, and DATA_MODEL.md §4 is not a build list.
func TestMigration001CreatesNoUnusedTables(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"claims", "claim_evidence", "claim_fields", "transactions", "transaction_evidence"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err == nil {
			t.Errorf("table %s exists: migration 001 is M1 only", table)
		}
	}
}

// Startup runs migrations every time. The second run must be a no-op rather
// than an error, or BillyCore only starts once.
func TestOpenIsIdempotent(t *testing.T) {
	path := testDBPath(t)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO domain_event (type, payload, occurred_at) VALUES ('Test', '{}', '2026-08-25T00:00:00Z')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var events int
	if err := db2.QueryRow(`SELECT count(*) FROM domain_event`).Scan(&events); err != nil {
		t.Fatalf("count: %v", err)
	}
	if events != 1 {
		t.Errorf("domain_event has %d rows, want 1: reopening must not re-run migrations", events)
	}
}

// D15 makes file permissions the only thing protecting this data.
func TestOpenKeepsDatabaseFilesPrivate(t *testing.T) {
	path := testDBPath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Force a WAL file to exist by writing something.
	if _, err := db.Exec(`INSERT INTO domain_event (type, payload, occurred_at) VALUES ('Test', '{}', '2026-08-25T00:00:00Z')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := ensureSidecarModes(path); err != nil {
		t.Fatalf("ensureSidecarModes: %v", err)
	}

	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			if suffix == "" {
				t.Fatalf("stat %s: %v", path, err)
			}
			continue // no -wal yet; nothing to leak
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is mode %04o, want 0600", filepath.Base(path+suffix), perm)
		}
	}
}

// A file widened by a backup-and-restore is tightened on the next open rather
// than being trusted because it already existed.
func TestOpenTightensAWidenedFile(t *testing.T) {
	path := testDBPath(t)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}
}
