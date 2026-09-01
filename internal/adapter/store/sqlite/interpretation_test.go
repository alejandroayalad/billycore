package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// resetForExtraction is the mechanism from D48 in one statement. It returns the
// artifact to stage RECEIVED and changes nothing else. It is here and not a
// queue method, because D48 leaves the trigger to a later slice.
func resetForExtraction(t *testing.T, db *sql.DB, evidenceID string) {
	t.Helper()
	if _, err := db.Exec(`
		UPDATE evidence SET processing_stage = 'RECEIVED', locked_until = NULL WHERE id = ?`,
		evidenceID,
	); err != nil {
		t.Fatalf("reset evidence %s: %v", evidenceID, err)
	}
}

func scanOne[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

// The complete re-extraction loop. A corrected parser reads an artifact again,
// its reading replaces the old one, and Billy retires what the old one
// supported (D47, D48, D49). The Evidence does not change: only a stage column
// and a pointer move.
func TestReExtractionSupersedesTheActiveInterpretation(t *testing.T) {
	transactions, claims, queue, evidence, db := newTransactionTestRepo(t)
	ctx := context.Background()

	stored := storeEvidence(t, evidence, "ev-1", "msg-1")
	first := interpretationOf(t, "interp-1", "ev-1", "", newTransactionalClaim(t, "claim-1", "ev-1"))
	if created, err := claims.Save(ctx, first, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("first Save: created=%v err=%v", created, err)
	}
	if created, err := transactions.Save(ctx, one(newTestTransaction(t, "tx-1", "ev-1"), "claim-1"), builtAt); err != nil || !created {
		t.Fatalf("Save transaction: created=%v err=%v", created, err)
	}

	// D48: the artifact returns to stage RECEIVED, and nothing else moves.
	resetForExtraction(t, db, "ev-1")

	pending, err := queue.ClaimForExtraction(ctx, 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(pending))
	}
	// The queue returns what Billy believes now. The pass needs it to name the
	// reading that it replaces.
	if pending[0].ActiveInterpretationID != "interp-1" {
		t.Fatalf("active interpretation = %q, want interp-1", pending[0].ActiveInterpretationID)
	}

	second := interpretationOf(t, "interp-2", "ev-1", "interp-1", newTransactionalClaim(t, "claim-2", "ev-1"))
	if created, err := claims.Save(ctx, second, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("second Save: created=%v err=%v", created, err)
	}

	// What Billy believes now.
	if got := scanOne[string](t, db,
		`SELECT interpretation_id FROM evidence_active_interpretation WHERE evidence_id = 'ev-1'`); got != "interp-2" {
		t.Errorf("active interpretation = %q, want interp-2", got)
	}
	// What Billy believed before — recorded, not inferred (D47).
	if got := scanOne[string](t, db,
		`SELECT superseded_by_interpretation_id FROM interpretations WHERE id = 'interp-1'`); got != "interp-2" {
		t.Errorf("lineage from interp-1 = %q, want interp-2", got)
	}

	if got := scanOne[string](t, db, `SELECT state FROM claims WHERE id = 'claim-1'`); got != "SUPERSEDED" {
		t.Errorf("old claim state = %q, want SUPERSEDED", got)
	}
	if got := scanOne[string](t, db, `SELECT state FROM claims WHERE id = 'claim-2'`); got != "ACTIVE" {
		t.Errorf("new claim state = %q, want ACTIVE", got)
	}
	// Billy keeps the old Claim and does not delete it.
	if got := scanOne[int](t, db, `SELECT count(*) FROM claims`); got != 2 {
		t.Errorf("claims = %d, want 2 — the superseded one is retained", got)
	}

	// D49: the Transaction from the old Claim stops being current. The column
	// transaction_state reports this, and not reconciliation_state.
	if got := scanOne[string](t, db, `SELECT transaction_state FROM transactions WHERE id = 'tx-1'`); got != "SUPERSEDED" {
		t.Errorf("transaction_state = %q, want SUPERSEDED — otherwise the corpus double-counts", got)
	}
	if got := scanOne[string](t, db, `SELECT reconciliation_state FROM transactions WHERE id = 'tx-1'`); got != "UNRECONCILED" {
		t.Errorf("reconciliation_state = %q, want UNRECONCILED — superseding is not a reconciliation", got)
	}
	// No code fills the successor pointer yet. The replacement Transaction does
	// not exist, because claim-2 has not reached reconciliation (D49).
	var successor sql.NullString
	if err := db.QueryRow(
		`SELECT superseded_by_transaction_id FROM transactions WHERE id = 'tx-1'`).Scan(&successor); err != nil {
		t.Fatalf("read successor: %v", err)
	}
	if successor.Valid {
		t.Errorf("superseded_by_transaction_id = %q; there is no honest successor to name yet", successor.String)
	}

	// The artifact is at stage EXTRACTED again, and its bytes did not change.
	if got := scanOne[string](t, db, `SELECT processing_stage FROM evidence WHERE id = 'ev-1'`); got != "EXTRACTED" {
		t.Errorf("stage = %q, want EXTRACTED", got)
	}
	readBack, err := evidence.GetByID(ctx, "ev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(readBack.RawContent()) != string(stored.RawContent()) {
		t.Error("the artifact changed across a re-extraction; Evidence is immutable (D7, D10)")
	}
}

// The compare-and-swap separates a planned replacement from a lost race, and no
// column records intent. Two passes both expect interp-1. One pass swaps it,
// and the other writes nothing.
func TestAReplacementThatNamesTheWrongPredecessorWritesNothing(t *testing.T) {
	_, claims, _, evidence, db := newTransactionTestRepo(t)
	ctx := context.Background()

	storeEvidence(t, evidence, "ev-1", "msg-1")
	if created, err := claims.Save(ctx, oneClaim(t, "interp-1", "claim-1", "ev-1"), testProfile, claimedAt); err != nil || !created {
		t.Fatalf("first Save: created=%v err=%v", created, err)
	}
	resetForExtraction(t, db, "ev-1")

	// The winner.
	winner := interpretationOf(t, "interp-2", "ev-1", "interp-1", newTestClaim(t, "claim-2", "ev-1"))
	if created, err := claims.Save(ctx, winner, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("winning Save: created=%v err=%v", created, err)
	}

	// The loser read the same interpretation before the winner committed.
	loser := interpretationOf(t, "interp-3", "ev-1", "interp-1", newTestClaim(t, "claim-3", "ev-1"))
	created, err := claims.Save(ctx, loser, testProfile, claimedAt)
	if err != nil {
		t.Fatalf("losing Save: %v — losing the race is an outcome, not an error", err)
	}
	if created {
		t.Error("two replacements of one interpretation both committed")
	}

	if got := scanOne[string](t, db,
		`SELECT interpretation_id FROM evidence_active_interpretation WHERE evidence_id = 'ev-1'`); got != "interp-2" {
		t.Errorf("active interpretation = %q, want interp-2", got)
	}
	// Nothing from the loser stays: no interpretation row, no Claim, and no
	// event for an activation that did not happen.
	if got := scanOne[int](t, db, `SELECT count(*) FROM interpretations WHERE id = 'interp-3'`); got != 0 {
		t.Errorf("the losing interpretation left %d rows behind", got)
	}
	if got := scanOne[int](t, db, `SELECT count(*) FROM claims WHERE id = 'claim-3'`); got != 0 {
		t.Errorf("the losing claim left %d rows behind", got)
	}
	if got := scanOne[int](t, db,
		`SELECT count(*) FROM domain_event WHERE payload LIKE '%claim-3%'`); got != 0 {
		t.Errorf("the losing claim left %d events behind", got)
	}
}

// The shape of a statement, and the reason for this migration: one artifact,
// one reading, many movements.
func TestAnInterpretationOfManyClaimsSurvivesTheRoundTrip(t *testing.T) {
	transactions, claims, queue, evidence, db := newTransactionTestRepo(t)
	ctx := context.Background()

	storeEvidence(t, evidence, "ev-1", "msg-1")
	in := interpretationOf(t, "interp-1", "ev-1", "",
		newTransactionalClaim(t, "claim-1", "ev-1"),
		newTransactionalClaim(t, "claim-2", "ev-1"),
		newTransactionalClaim(t, "claim-3", "ev-1"))
	if created, err := claims.Save(ctx, in, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}

	if got := scanOne[int](t, db, `SELECT count(*) FROM claims`); got != 3 {
		t.Errorf("claims = %d, want 3", got)
	}
	if got := scanOne[int](t, db,
		`SELECT count(*) FROM interpretation_claims WHERE interpretation_id = 'interp-1'`); got != 3 {
		t.Errorf("membership rows = %d, want 3", got)
	}
	// One activation event for each Claim, because Billy uses each of them.
	if got := scanOne[int](t, db,
		`SELECT count(*) FROM domain_event WHERE type = ?`, eventClaimActivated); got != 3 {
		t.Errorf("ClaimActivated events = %d, want 3", got)
	}
	// One stage advance for the artifact, for any number of movements.
	if got := scanOne[string](t, db, `SELECT processing_stage FROM evidence WHERE id = 'ev-1'`); got != "EXTRACTED" {
		t.Errorf("stage = %q, want EXTRACTED", got)
	}

	pending, err := queue.ClaimForReconciliation(ctx, 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(pending) != 1 || len(pending[0].Claims) != 3 {
		t.Fatalf("claimed %d rows carrying %d claims, want 1 row of 3", len(pending), len(pending[0].Claims))
	}

	built := one(newTestTransaction(t, "tx-1", "ev-1"), "claim-1")
	built = append(built, one(newTestTransaction(t, "tx-2", "ev-1"), "claim-2")...)
	built = append(built, one(newTestTransaction(t, "tx-3", "ev-1"), "claim-3")...)
	if created, err := transactions.Save(ctx, built, builtAt); err != nil || !created {
		t.Fatalf("Save transactions: created=%v err=%v", created, err)
	}
	if got := scanOne[int](t, db, `SELECT count(*) FROM transactions`); got != 3 {
		t.Errorf("transactions = %d, want 3 — one per movement (D42)", got)
	}
	// The stage advances one time, and only after the complete set commits. An
	// advance for each Transaction would stop the movements after the first.
	if got := scanOne[string](t, db, `SELECT processing_stage FROM evidence WHERE id = 'ev-1'`); got != "RECONCILED" {
		t.Errorf("stage = %q, want RECONCILED", got)
	}
}

// One movement that does not commit fails the complete reading (D46). A table
// with 38 rows instead of 40 is worse than a failure that somebody can see.
func TestOneUnwritableClaimRollsBackTheWholeInterpretation(t *testing.T) {
	_, claims, _, evidence, db := newTransactionTestRepo(t)
	ctx := context.Background()

	storeEvidence(t, evidence, "ev-1", "msg-1")
	// The second Claim collides on the primary key of claims. This is the
	// simplest way to make one member of a set fail.
	if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
		VALUES ('claim-2', 'ACTIVE', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed the collision: %v", err)
	}

	in := interpretationOf(t, "interp-1", "ev-1", "",
		newTransactionalClaim(t, "claim-1", "ev-1"),
		newTransactionalClaim(t, "claim-2", "ev-1"))
	if _, err := claims.Save(ctx, in, testProfile, claimedAt); err == nil {
		t.Fatal("a set with an unwritable member was accepted")
	}

	if got := scanOne[int](t, db, `SELECT count(*) FROM claims WHERE id = 'claim-1'`); got != 0 {
		t.Error("the store committed the good part of a failed reading")
	}
	if got := scanOne[int](t, db, `SELECT count(*) FROM interpretations`); got != 0 {
		t.Error("a failed reading left an interpretation row behind")
	}
	if got := scanOne[int](t, db,
		`SELECT count(*) FROM evidence_active_interpretation WHERE evidence_id = 'ev-1'`); got != 0 {
		t.Error("a failed reading became the active interpretation")
	}
	// The artifact stays retryable, which is the purpose of a complete failure.
	if got := scanOne[string](t, db, `SELECT processing_stage FROM evidence WHERE id = 'ev-1'`); got != "RECEIVED" {
		t.Errorf("stage = %q, want RECEIVED — a failed reading must not advance the row", got)
	}
}

// Migration 005 carries the pointers from 003 forward. A re-extraction cannot
// recover them, because the Claims do not record which one Billy used.
func TestMigration005CarriesTheActiveClaimPointersForward(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	db, err := sql.Open("sqlite", testDBPath(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// A database at migration 004, with one artifact interpreted.
	for _, m := range migrations {
		if m.version > 4 {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO evidence (
		id, source_id, source_type, source_reference, observed_at, created_at, processing_stage
	) VALUES ('ev-1', 's', 'GMAIL', 'r-1', '2026-08-26T00:00:00.000Z', '2026-08-26T00:00:00.000Z', 'EXTRACTED')`); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO claims (id, state, created_at, updated_at)
		VALUES ('c-1', 'ACTIVE', '2026-08-26T01:02:03.000Z', '2026-08-26T01:02:03.000Z')`); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO evidence_active_claim (evidence_id, claim_id) VALUES ('ev-1', 'c-1')`); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Billy uses the same Claim as before, now as the only member of its own
	// interpretation.
	interpretationID := scanOne[string](t, db,
		`SELECT interpretation_id FROM evidence_active_interpretation WHERE evidence_id = 'ev-1'`)
	if got := scanOne[string](t, db,
		`SELECT claim_id FROM interpretation_claims WHERE interpretation_id = ?`, interpretationID); got != "c-1" {
		t.Errorf("carried-forward claim = %q, want c-1", got)
	}
	// The reading has the date of the Claim, and not the date of this
	// migration.
	if got := scanOne[string](t, db,
		`SELECT created_at FROM interpretations WHERE id = ?`, interpretationID); got != "2026-08-26T01:02:03.000Z" {
		t.Errorf("created_at = %q, want the claim's own 2026-08-26T01:02:03.000Z", got)
	}
	if len(interpretationID) != 36 {
		t.Errorf("interpretation id %q is not the 36-character UUID shape internal/id writes", interpretationID)
	}
	// The rows from before 005 are still current, because they come from Claims
	// that are still active.
	if got := scanOne[int](t, db,
		`SELECT count(*) FROM transactions WHERE transaction_state != 'ACTIVE'`); got != 0 {
		t.Errorf("%d pre-existing transactions were not carried forward as ACTIVE", got)
	}
}
