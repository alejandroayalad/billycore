package sqlite

import (
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

	bad := migration{
		version: 2,
		name:    "002_broken.sql",
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
	if version != 1 {
		t.Errorf("user_version = %d after a failed migration, want 1", version)
	}
}
