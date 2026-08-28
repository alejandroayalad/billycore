package sqlite

import (
	"database/sql"
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
	if userVersion != 5 {
		t.Errorf("user_version = %d, want 5 after migration 005", userVersion)
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
// This test asserted the absence of `transactions` and `transaction_evidence`
// through migrations 002 and 003. It now asserts their presence, because
// migration 004 arrived with `internal/app/reconcile.go` — the code that writes
// to them. The rule did not change; the code caught up with the document.
//
// What it still asserts is the rule itself, against the next table to be
// described and not yet written: `reconciliation_candidate`, whose columns
// DATA_MODEL.md §10 Q1 cannot even name until the domain settles what a
// candidate references.
func TestMigrationsCreateNoUnusedTables(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"reconciliation_candidate"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err == nil {
			t.Errorf("table %s exists, and nothing writes to it yet", table)
		}
	}
}

// Migration 004 creates what the Transaction aggregate needs: the row, its
// provenance to Evidence, and the slot that makes building one idempotent.
//
// It exercises the tables rather than only looking them up in sqlite_master. A
// schema nobody has inserted into is a schema nobody has tested, and the two
// facts worth proving here are physical: Money's halves are nullable together,
// and `claim_transaction`'s primary key rejects a second Transaction for one
// Claim (D41).
func TestMigration004CreatesTheTransactionTables(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"transactions", "transaction_evidence", "claim_transaction"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
	for _, index := range []string{
		"idx_transactions_cursor",
		"idx_transaction_evidence_by_evidence",
		"idx_claim_transaction_by_transaction",
	} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name); err != nil {
			t.Errorf("index %s missing: %v", index, err)
		}
	}

	// A Transaction with no amount is a legitimate row (DATA_MODEL.md §4.5).
	// The domain forbids half a Money; the schema deliberately does not, which
	// is what this asserts — the invariant lives in one place (D6), and it is
	// not here.
	if _, err := db.Exec(`
		INSERT INTO transactions (
			id, amount_minor, currency, merchant, account_identifier,
			direction, financial_status, reconciliation_state,
			occurred_at, created_at, updated_at
		) VALUES ('tx-1', NULL, NULL, NULL, NULL, 'OUTFLOW', 'UNKNOWN', 'UNRECONCILED',
			'2026-08-16T23:44:00.000Z', '2026-08-26T12:00:00.000Z', '2026-08-26T12:00:00.000Z')`,
	); err != nil {
		t.Fatalf("a Transaction without Money is valid storage: %v", err)
	}

	// The slot is the natural key. A second Transaction built from one Claim is
	// the duplicate that puts the same money in the table twice, and the
	// primary key is what refuses it — not a code path that remembered to look.
	mustExec(t, db, `INSERT INTO evidence (id, source_id, source_type, source_reference, content_type, raw_content, observed_at, created_at, processing_stage)
		VALUES ('ev-1', 'gmail_primary', 'EMAIL', 'msg-1', 'message/rfc822', x'00', '2026-08-16T23:44:00.000Z', '2026-08-26T12:00:00.000Z', 'EXTRACTED')`)
	mustExec(t, db, `INSERT INTO claims (id, state, created_at, updated_at)
		VALUES ('claim-1', 'ACTIVE', '2026-08-26T12:00:00.000Z', '2026-08-26T12:00:00.000Z')`)
	mustExec(t, db, `INSERT INTO transaction_evidence (transaction_id, evidence_id) VALUES ('tx-1', 'ev-1')`)
	mustExec(t, db, `INSERT INTO claim_transaction (claim_id, transaction_id) VALUES ('claim-1', 'tx-1')`)

	mustExec(t, db, `
		INSERT INTO transactions (id, direction, financial_status, reconciliation_state, occurred_at, created_at, updated_at)
		VALUES ('tx-2', 'OUTFLOW', 'UNKNOWN', 'UNRECONCILED',
			'2026-08-16T23:44:00.000Z', '2026-08-26T12:00:00.000Z', '2026-08-26T12:00:00.000Z')`)
	if _, err := db.Exec(`INSERT INTO claim_transaction (claim_id, transaction_id) VALUES ('claim-1', 'tx-2')`); err == nil {
		t.Error("a second Transaction for one Claim was accepted; claim_transaction's primary key is the whole idempotency (D41)")
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec: %v", err)
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

// Migration 005 moves the constraint one level higher. The artifact points at
// one interpretation, and that interpretation holds one or many Claims (D46,
// D47).
func TestMigration005CreatesTheActiveInterpretationConstraint(t *testing.T) {
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
	for _, id := range []string{"i-1", "i-2"} {
		if _, err := db.Exec(`INSERT INTO interpretations (id, evidence_id, created_at)
			VALUES (?, 'ev-1', '2026-08-26T00:00:00.000Z')`, id); err != nil {
			t.Fatalf("seed interpretation %s: %v", id, err)
		}
	}

	if _, err := db.Exec(`INSERT INTO evidence_active_interpretation (evidence_id, interpretation_id) VALUES ('ev-1', 'i-1')`); err != nil {
		t.Fatalf("first active interpretation: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO evidence_active_interpretation (evidence_id, interpretation_id) VALUES ('ev-1', 'i-2')`); err == nil {
		t.Error("the database accepted two active interpretations for one artifact")
	}

	// The pointer cannot name an interpretation or an artifact that does not
	// exist.
	if _, err := db.Exec(`INSERT INTO evidence_active_interpretation (evidence_id, interpretation_id) VALUES ('ev-missing', 'i-2')`); err == nil {
		t.Error("the database accepted an active interpretation for evidence that does not exist")
	}

	// Many Claims in one interpretation. Migration 003 could not do this.
	for _, id := range []string{"c-1", "c-2"} {
		if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
			VALUES (?, 'ACTIVE', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z')`, id); err != nil {
			t.Fatalf("seed claim %s: %v", id, err)
		}
		if _, err := db.Exec(`INSERT INTO interpretation_claims (interpretation_id, claim_id) VALUES ('i-1', ?)`, id); err != nil {
			t.Fatalf("membership for %s: %v — one interpretation must hold many claims", id, err)
		}
	}

	// The table from 003 is gone, so a query that still reads it fails.
	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'evidence_active_claim'`).Scan(&name); err == nil {
		t.Error("evidence_active_claim survived migration 005")
	}
}

// The lineage that D47 needs: what Billy believes now, and what Billy believed
// before.
func TestMigration005RecordsInterpretationLineage(t *testing.T) {
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
	for _, id := range []string{"i-1", "i-2"} {
		if _, err := db.Exec(`INSERT INTO interpretations (id, evidence_id, created_at)
			VALUES (?, 'ev-1', '2026-08-26T00:00:00.000Z')`, id); err != nil {
			t.Fatalf("seed interpretation %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`UPDATE interpretations SET superseded_by_interpretation_id = 'i-2' WHERE id = 'i-1'`); err != nil {
		t.Fatalf("record lineage: %v", err)
	}
	// The value must be an interpretation that exists, and not any string.
	if _, err := db.Exec(`UPDATE interpretations SET superseded_by_interpretation_id = 'i-missing' WHERE id = 'i-1'`); err == nil {
		t.Error("the database accepted lineage pointing at an interpretation that does not exist")
	}
}

// D49 — a Transaction's own lifecycle, separate from its reconciliation state.
func TestMigration005AddsTheTransactionLifecycle(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, column := range []string{"transaction_state", "superseded_by_transaction_id"} {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('transactions') WHERE name = ?`, column).Scan(&n); err != nil {
			t.Fatalf("read table info: %v", err)
		}
		if n != 1 {
			t.Errorf("transactions has no %s column", column)
		}
	}

	// reconciliation_state keeps its three values. The two columns answer
	// different questions, which is the argument in D49.
	var notNull int
	if err := db.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('transactions') WHERE name = 'transaction_state'`).Scan(&notNull); err != nil {
		t.Fatalf("read table info: %v", err)
	}
	if notNull != 1 {
		t.Error("transaction_state is nullable; a row that is neither current nor superseded is not a state")
	}
}
