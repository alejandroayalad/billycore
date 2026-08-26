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
	if userVersion != 3 {
		t.Errorf("user_version = %d, want 3 after migration 003", userVersion)
	}

	for _, table := range []string{"evidence", "domain_event"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing after migrations: %v", table, err)
		}
	}

	var index string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_evidence_pipeline_scan'`).Scan(&index)
	if err != nil {
		t.Errorf("idx_evidence_pipeline_scan missing: %v", err)
	}
}

// A table is created when code writes to it, not when a document describes it.
// A table appearing early is a schema nobody has exercised, and
// DATA_MODEL.md §4 is not a build list.
//
// 002 brought the claim tables because M2 writes to them. `transactions` and
// `transaction_evidence` are specified in §4.5 and §4.6 and still have no code
// behind them, so they must still be absent.
func TestMigrationsCreateNoUnusedTables(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"transactions", "transaction_evidence"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err == nil {
			t.Errorf("table %s exists, and nothing writes to it yet", table)
		}
	}
}

// Migration 002 creates what the Claim aggregate needs, with the physical
// exclusivity DATA_MODEL.md §4.4 requires of a field's value.
func TestMigration002CreatesTheClaimTables(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"claims", "claim_evidence", "claim_fields"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
	for _, index := range []string{"idx_claim_evidence_by_evidence"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name); err != nil {
			t.Errorf("index %s missing: %v", index, err)
		}
	}

	if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
		VALUES ('c1', 'ACTIVE', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	// A field holds an integer or text, never both and never neither.
	both := `INSERT INTO claim_fields (claim_id, field_name, value_int, value_text, confidence)
		VALUES ('c1', 'amount_minor', 100000, 'MXN', 'HIGH')`
	if _, err := db.Exec(both); err == nil {
		t.Error("claim_fields accepted a row with both a value_int and a value_text")
	}
	neither := `INSERT INTO claim_fields (claim_id, field_name, confidence)
		VALUES ('c1', 'merchant', 'HIGH')`
	if _, err := db.Exec(neither); err == nil {
		t.Error("claim_fields accepted a row with no value at all")
	}
	ok := `INSERT INTO claim_fields (claim_id, field_name, value_int, confidence)
		VALUES ('c1', 'amount_minor', 100000, 'HIGH')`
	if _, err := db.Exec(ok); err != nil {
		t.Errorf("claim_fields rejected a valid row: %v", err)
	}
}

// Provenance must not be deletable out from under a Claim. DATA_MODEL.md §6:
// nothing supporting a financial fact disappears because something else was
// removed.
func TestClaimProvenanceRestrictsDeletes(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO evidence
		(id, source_id, source_type, source_reference, observed_at, created_at, processing_stage)
		VALUES ('e1', 's1', 'GMAIL', 'ref-1', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z', 'RECEIVED')`); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
		VALUES ('c1', 'ACTIVE', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO claim_evidence (claim_id, evidence_id) VALUES ('c1', 'e1')`); err != nil {
		t.Fatalf("insert provenance: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM evidence WHERE id = 'e1'`); err == nil {
		t.Error("Evidence supporting a Claim was deleted")
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE id = 'c1'`); err == nil {
		t.Error("a Claim with provenance was deleted")
	}

	// A claim_fields row is likewise not orphanable.
	if _, err := db.Exec(`INSERT INTO claim_fields (claim_id, field_name, value_text, confidence)
		VALUES ('c1', 'currency', 'MXN', 'HIGH')`); err != nil {
		t.Fatalf("insert field: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE id = 'c1'`); err == nil {
		t.Error("a Claim with fields was deleted")
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

// Migration 003 creates the constraint that makes extraction idempotent. The
// primary key is the whole point: it is what a second pass collides with.
func TestMigration003CreatesTheActiveClaimConstraint(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO evidence (
		id, source_id, source_type, source_reference, observed_at, created_at, processing_stage
	) VALUES ('ev-1', 's', 'GMAIL', 'r-1', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z', 'RECEIVED')`); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	for _, id := range []string{"c-1", "c-2"} {
		if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
			VALUES (?, 'ACTIVE', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z')`, id); err != nil {
			t.Fatalf("seed claim %s: %v", id, err)
		}
	}

	if _, err := db.Exec(`INSERT INTO evidence_active_claim (evidence_id, claim_id) VALUES ('ev-1', 'c-1')`); err != nil {
		t.Fatalf("first active claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO evidence_active_claim (evidence_id, claim_id) VALUES ('ev-1', 'c-2')`); err == nil {
		t.Error("the database accepted two active claims for one artifact")
	}

	// Provenance is a real reference, not a promise: the pointer cannot name a
	// Claim or an artifact that does not exist.
	if _, err := db.Exec(`INSERT INTO evidence_active_claim (evidence_id, claim_id) VALUES ('ev-missing', 'c-2')`); err == nil {
		t.Error("the database accepted an active claim for evidence that does not exist")
	}
}
