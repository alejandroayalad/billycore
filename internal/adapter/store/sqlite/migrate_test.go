package sqlite

import (
	"database/sql"
	"strings"
	"testing"
)

func TestLoadMigrationsAreOrderedAndNumbered(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded: the schema did not compile into the binary")
	}
	for i, m := range migrations {
		if m.version != i+1 {
			// A gap means user_version can land on a number no migration owns.
			t.Errorf("migration %s has version %d, want %d: versions are contiguous from 1", m.name, m.version, i+1)
		}
		if strings.TrimSpace(m.sql) == "" {
			t.Errorf("migration %s is empty", m.name)
		}
	}
}

func TestVersionOfRejectsBadNames(t *testing.T) {
	for _, name := range []string{"initial.sql", "abc_initial.sql", "000_initial.sql", "-1_initial.sql"} {
		if _, err := versionOf(name); err == nil {
			t.Errorf("versionOf(%q) accepted a name it cannot order", name)
		}
	}
}

// Forward-only means an older binary refuses a newer database rather than
// querying columns it does not know about.
func TestMigrateRefusesANewerDatabase(t *testing.T) {
	path := testDBPath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	err = Migrate(db)
	if err == nil {
		t.Fatal("Migrate accepted a database from a newer BillyCore")
	}
	if !strings.Contains(err.Error(), "newer schema") {
		t.Errorf("error does not say why: %v", err)
	}
}

// A migration that fails leaves nothing behind, and BillyCore does not start.
func TestApplyMigrationRollsBackOnFailure(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Read the version Open left rather than hard-coding one: this test is
	// about atomicity, and it should not need editing every time a real
	// migration is added.
	var before int
	if err := db.QueryRow("PRAGMA user_version").Scan(&before); err != nil {
		t.Fatalf("user_version: %v", err)
	}

	bad := migration{
		version: before + 1,
		name:    "999_broken.sql",
		sql:     "CREATE TABLE later_table (id TEXT) STRICT;\nTHIS IS NOT SQL;",
	}
	if err := applyMigration(db, bad); err == nil {
		t.Fatal("applyMigration reported success on invalid SQL")
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'later_table'`).Scan(&name); err == nil {
		t.Error("the first statement of a failed migration survived: migrations are not atomic")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != before {
		t.Errorf("user_version = %d after a failed migration, want %d", version, before)
	}
}

// The real upgrade path: a database already holding Evidence moves from 001 to
// 002 without touching a row of it.
//
// This is not hypothetical. `~/.billy/billy.db` holds 1,044 Evidence rows
// written under migration 001, and the next start of any BillyCore binary
// applies 002 to it.
func TestMigrateUpgradesAnExistingDatabaseInPlace(t *testing.T) {
	path := testDBPath(t)

	// A database at version 1 only, as M1 left it.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if err := applyMigration(db, migrations[0]); err != nil {
		t.Fatalf("apply 001: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO evidence
		(id, source_id, source_type, source_reference, raw_content, observed_at, created_at, processing_stage)
		VALUES ('e1', 's1', 'GMAIL', 'ref-1', x'6869', '2026-08-25T00:00:00.000Z', '2026-08-25T00:00:00.000Z', 'RECEIVED')`); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	db.Close()

	// Upgrade.
	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v1 database: %v", err)
	}
	defer upgraded.Close()

	var version int
	if err := upgraded.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d", version, len(migrations))
	}

	// The Evidence is untouched, content included. Evidence is immutable, and a
	// migration is not an exception to that.
	var stage, reference string
	var raw []byte
	if err := upgraded.QueryRow(
		`SELECT processing_stage, source_reference, raw_content FROM evidence WHERE id = 'e1'`,
	).Scan(&stage, &reference, &raw); err != nil {
		t.Fatalf("the existing Evidence did not survive the upgrade: %v", err)
	}
	if stage != "RECEIVED" || reference != "ref-1" || string(raw) != "hi" {
		t.Errorf("evidence changed: stage %q, reference %q, content %q", stage, reference, string(raw))
	}

	var count int
	if err := upgraded.QueryRow(`SELECT count(*) FROM claims`).Scan(&count); err != nil {
		t.Errorf("claims table missing after the upgrade: %v", err)
	}
	if count != 0 {
		t.Errorf("the upgrade invented %d claims", count)
	}
}
