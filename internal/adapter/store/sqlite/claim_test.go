package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var claimedAt = time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

func newClaimTestRepo(t *testing.T) (*ClaimRepository, *EvidenceQueue, *EvidenceRepository, *sql.DB) {
	t.Helper()
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewClaimRepository(db), NewEvidenceQueue(db), NewEvidenceRepository(db), db
}

// storeEvidence puts one artifact at RECEIVED, the way ingestion leaves it.
func storeEvidence(t *testing.T, repo *EvidenceRepository, id, reference string) domain.Evidence {
	t.Helper()
	e := newTestEvidence(t, id, reference)
	created, err := repo.Insert(context.Background(), e, ingestedAt)
	if err != nil || !created {
		t.Fatalf("Insert: created=%v err=%v", created, err)
	}
	return e
}

func newTestClaim(t *testing.T, id, evidenceID string) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	currency, err := domain.NewTextField("MXN", domain.Low)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	merchant, err := domain.NewTextField("HSBC beneficiary", domain.Medium)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	occurred, err := domain.NewTimeField(observed, domain.High)
	if err != nil {
		t.Fatalf("NewTimeField: %v", err)
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID},
		map[domain.FieldName]domain.ClaimField{
			domain.FieldAmountMinor: amount,
			domain.FieldCurrency:    currency,
			domain.FieldMerchant:    merchant,
			domain.FieldOccurredAt:  occurred,
		}, claimedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	active, err := proposed.Activate(claimedAt)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return active
}

// The whole point of D25 applied to Claims: the row, its provenance, its
// fields, the event and the stage advance either all happen or none do.
func TestSaveWritesTheClaimItsFieldsItsProvenanceAndItsEvent(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	claim := newTestClaim(t, "claim-1", "ev-1")

	if created, err := claims.Save(context.Background(), claim, claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}

	var state, createdAt, updatedAt string
	var supersededBy sql.NullString
	if err := db.QueryRow(
		`SELECT state, superseded_by_claim_id, created_at, updated_at FROM claims WHERE id = ?`,
		"claim-1").Scan(&state, &supersededBy, &createdAt, &updatedAt); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	if state != "ACTIVE" {
		t.Errorf("state = %q, want ACTIVE", state)
	}
	if supersededBy.Valid {
		t.Errorf("superseded_by_claim_id = %q on a fresh claim", supersededBy.String)
	}

	var evidenceID string
	if err := db.QueryRow(`SELECT evidence_id FROM claim_evidence WHERE claim_id = ?`,
		"claim-1").Scan(&evidenceID); err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if evidenceID != "ev-1" {
		t.Errorf("provenance = %q, want ev-1", evidenceID)
	}

	fields := readFields(t, db, "claim-1")
	if len(fields) != 4 {
		t.Fatalf("wrote %d fields, want 4: %v", len(fields), fields)
	}
	// The int/text split is physical, and an amount stored as text would
	// satisfy the CHECK and still be wrong (DATA_MODEL.md §4.4).
	if got := fields["amount_minor"]; got.valueInt != 100000 || got.valueText.Valid {
		t.Errorf("amount_minor = %+v", got)
	}
	if got := fields["currency"]; got.valueText.String != "MXN" || got.valueInt != 0 || got.confidence != "LOW" {
		t.Errorf("currency = %+v", got)
	}
	if got := fields["merchant"]; got.confidence != "MEDIUM" {
		t.Errorf("merchant confidence = %q, want MEDIUM", got.confidence)
	}

	// DOMAIN.md §8: one ClaimActivated, carrying ids and no interpreted values.
	var payload string
	if err := db.QueryRow(
		`SELECT payload FROM domain_event WHERE type = ?`, eventClaimActivated).Scan(&payload); err != nil {
		t.Fatalf("read event: %v", err)
	}
	var event struct {
		ClaimID     string   `json:"claimId"`
		EvidenceIDs []string `json:"evidenceIds"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.ClaimID != "claim-1" || len(event.EvidenceIDs) != 1 || event.EvidenceIDs[0] != "ev-1" {
		t.Errorf("event = %+v", event)
	}
	// SECURITY.md §10: a fact about an interpretation, not a copy of one.
	if strings.Contains(payload, "HSBC") || strings.Contains(payload, "100000") {
		t.Errorf("event payload carries interpreted values: %s", payload)
	}
}

// The Evidence advances in the same transaction as the Claim. A row marked
// EXTRACTED whose Claim rolled back is an artifact Billy never revisits and has
// nothing to show for.
func TestSaveAdvancesTheEvidenceItInterpreted(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	if stage := readStage(t, db, "ev-1"); stage != stageReceived {
		t.Fatalf("stage before = %q, want RECEIVED", stage)
	}
	if created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}
	if stage := readStage(t, db, "ev-1"); stage != stageExtracted {
		t.Errorf("stage after = %q, want EXTRACTED", stage)
	}
}

// Provenance is a foreign key, not a promise. A Claim citing Evidence that does
// not exist must not land, and nothing of it may survive the attempt.
func TestAClaimCitingEvidenceThatDoesNotExistIsRejectedWhole(t *testing.T) {
	claims, _, _, db := newClaimTestRepo(t)

	_, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-missing"), claimedAt)
	if err == nil {
		t.Fatal("Save accepted a claim with no such Evidence")
	}
	for _, table := range []string{"claims", "claim_evidence", "claim_fields", "domain_event"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after a rolled-back save", table, n)
		}
	}
}

// Evidence is immutable. Extraction moves the pipeline columns and touches
// nothing else (DATA_MODEL.md §7).
func TestExtractionDoesNotRewriteTheArtifact(t *testing.T) {
	claims, _, evidence, _ := newClaimTestRepo(t)
	stored := storeEvidence(t, evidence, "ev-1", "msg-1")

	if created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}
	after, err := evidence.GetByID(context.Background(), "ev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(after.RawContent()) != string(stored.RawContent()) {
		t.Error("raw_content changed")
	}
	if !after.ObservedAt().Equal(stored.ObservedAt()) {
		t.Errorf("observed_at changed: %v -> %v", stored.ObservedAt(), after.ObservedAt())
	}
	if after.SourceReference() != stored.SourceReference() {
		t.Errorf("source_reference changed: %q -> %q", stored.SourceReference(), after.SourceReference())
	}
}

// --- the queue --------------------------------------------------------------

func TestClaimForExtractionLocksTheRowsItHandsOut(t *testing.T) {
	_, queue, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	storeEvidence(t, evidence, "ev-2", "msg-2")

	lockedUntil := claimedAt.Add(time.Minute)
	first, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt, lockedUntil)
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("claimed %d rows, want 2", len(first))
	}
	if len(first[0].RawContent) == 0 {
		t.Error("claimed a row with no content to interpret")
	}

	// A second pass arriving inside the lease finds nothing to do.
	second, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt.Add(time.Second), lockedUntil)
	if err != nil {
		t.Fatalf("second ClaimForExtraction: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("a second pass claimed %d already-locked rows", len(second))
	}

	// A lapsed lease is claimable again — which is what makes a killed process
	// recoverable without anyone releasing anything by hand.
	third, err := queue.ClaimForExtraction(context.Background(), 10, lockedUntil.Add(time.Second), lockedUntil.Add(time.Minute))
	if err != nil {
		t.Fatalf("third ClaimForExtraction: %v", err)
	}
	if len(third) != 2 {
		t.Errorf("after the lease lapsed, claimed %d rows, want 2", len(third))
	}
	if readStage(t, db, "ev-1") != stageReceived {
		t.Error("claiming a row advanced its stage; only extraction does that")
	}
}

func TestMarkExtractedAdvancesAndReleaseDoesNot(t *testing.T) {
	_, queue, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	storeEvidence(t, evidence, "ev-2", "msg-2")

	if _, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt, claimedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if err := queue.MarkExtracted(context.Background(), "ev-1", claimedAt); err != nil {
		t.Fatalf("MarkExtracted: %v", err)
	}
	if err := queue.Release(context.Background(), "ev-2", claimedAt); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if got := readStage(t, db, "ev-1"); got != stageExtracted {
		t.Errorf("ev-1 stage = %q, want EXTRACTED", got)
	}
	// Retry is not a stage: a released row stays exactly where it was.
	if got := readStage(t, db, "ev-2"); got != stageReceived {
		t.Errorf("ev-2 stage = %q, want RECEIVED", got)
	}
	// Released means claimable again immediately, not at the end of the lease.
	again, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt, claimedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(again) != 1 || again[0].ID != "ev-2" {
		t.Errorf("re-claimed %d rows, want just ev-2", len(again))
	}
}

// Migration 003's tables are not this slice's business, and there is a test
// asserting their absence. This is the other half of that: nothing here creates
// them by accident.
func TestExtractionCreatesNoTransactionTables(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	if created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}
	for _, table := range []string{"transactions", "transaction_evidence"} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err == nil {
			t.Errorf("%s exists; it belongs to migration 003", table)
		}
	}
}

// attempts increments when the row is handed out, not when something fails.
// A parser that takes the whole process down never reaches code that could
// record a failure, and a count of handled failures would leave that row at
// zero forever.
func TestClaimingARowCountsTheAttempt(t *testing.T) {
	_, queue, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	if got := readAttempts(t, db, "ev-1"); got != 0 {
		t.Fatalf("attempts before = %d, want 0", got)
	}
	claimed, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt, claimedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("claimed = %+v, want one row reporting attempts=1", claimed)
	}
	if got := readAttempts(t, db, "ev-1"); got != 1 {
		t.Errorf("attempts after = %d, want 1", got)
	}

	// A second hand-out after the lease lapses counts again, and the row
	// reports the running total rather than restarting.
	later := claimedAt.Add(2 * time.Minute)
	again, err := queue.ClaimForExtraction(context.Background(), 10, later, later.Add(time.Minute))
	if err != nil {
		t.Fatalf("second ClaimForExtraction: %v", err)
	}
	if len(again) != 1 || again[0].Attempts != 2 {
		t.Errorf("second claim = %+v, want attempts=2", again)
	}
}

// DATA_MODEL.md §7: retry is not a processing stage. A failure moves last_error
// and locked_until, and nothing else.
func TestRecordFailureBacksOffWithoutMovingTheStage(t *testing.T) {
	_, queue, evidence, db := newClaimTestRepo(t)
	stored := storeEvidence(t, evidence, "ev-1", "msg-1")
	if _, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt, claimedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}

	retryAt := claimedAt.Add(time.Hour)
	if err := queue.RecordFailure(context.Background(), "ev-1", "INTERPRET: the amount did not parse", retryAt); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	if got := readStage(t, db, "ev-1"); got != stageReceived {
		t.Errorf("stage = %q, want RECEIVED — a failure is not a stage", got)
	}
	if got := readLastError(t, db, "ev-1"); got != "INTERPRET: the amount did not parse" {
		t.Errorf("last_error = %q", got)
	}

	// Backed off: not claimable now, claimable once the backoff expires.
	soon, err := queue.ClaimForExtraction(context.Background(), 10, claimedAt.Add(time.Minute), claimedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(soon) != 0 {
		t.Errorf("a backed-off row was claimed %d times too early", len(soon))
	}
	after, err := queue.ClaimForExtraction(context.Background(), 10, retryAt.Add(time.Second), retryAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForExtraction: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("after the backoff, claimed %d rows, want 1", len(after))
	}

	// The artifact itself is untouched. Evidence is immutable, and a failed
	// parse leaves it exactly as received (SECURITY.md §7).
	back, err := evidence.GetByID(context.Background(), "ev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(back.RawContent()) != string(stored.RawContent()) {
		t.Error("raw_content changed on a failed extraction")
	}
}

// A row that failed on Monday and succeeded on Tuesday must not keep Monday's
// diagnostic: a stale last_error beside a good Claim reads as a live problem.
func TestASuccessfulExtractionClearsAnEarlierError(t *testing.T) {
	claims, queue, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	storeEvidence(t, evidence, "ev-2", "msg-2")

	if err := queue.RecordFailure(context.Background(), "ev-1", "INTERPRET: the template changed", claimedAt); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := queue.RecordFailure(context.Background(), "ev-2", "INTERPRET: the template changed", claimedAt); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	if created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}
	if err := queue.MarkExtracted(context.Background(), "ev-2", claimedAt); err != nil {
		t.Fatalf("MarkExtracted: %v", err)
	}

	for _, id := range []string{"ev-1", "ev-2"} {
		if got := readLastError(t, db, id); got != "" {
			t.Errorf("%s kept last_error = %q after succeeding", id, got)
		}
	}
}

// --- helpers ----------------------------------------------------------------

type storedField struct {
	valueInt   int64
	valueText  sql.NullString
	confidence string
}

func readFields(t *testing.T, db *sql.DB, claimID string) map[string]storedField {
	t.Helper()
	rows, err := db.Query(
		`SELECT field_name, coalesce(value_int, 0), value_text, confidence FROM claim_fields WHERE claim_id = ?`,
		claimID)
	if err != nil {
		t.Fatalf("read fields: %v", err)
	}
	defer rows.Close()

	out := map[string]storedField{}
	for rows.Next() {
		var name string
		var f storedField
		if err := rows.Scan(&name, &f.valueInt, &f.valueText, &f.confidence); err != nil {
			t.Fatalf("scan field: %v", err)
		}
		out[name] = f
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fields: %v", err)
	}
	return out
}

func readAttempts(t *testing.T, db *sql.DB, evidenceID string) int {
	t.Helper()
	var attempts int
	if err := db.QueryRow(`SELECT attempts FROM evidence WHERE id = ?`, evidenceID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	return attempts
}

func readLastError(t *testing.T, db *sql.DB, evidenceID string) string {
	t.Helper()
	var lastError sql.NullString
	if err := db.QueryRow(`SELECT last_error FROM evidence WHERE id = ?`, evidenceID).Scan(&lastError); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	return lastError.String
}

func readStage(t *testing.T, db *sql.DB, evidenceID string) string {
	t.Helper()
	var stage string
	if err := db.QueryRow(`SELECT processing_stage FROM evidence WHERE id = ?`, evidenceID).Scan(&stage); err != nil {
		t.Fatalf("read stage: %v", err)
	}
	return stage
}

// The property M1 has and extraction did not: re-running writes nothing, and it
// is a constraint that says so rather than a check somebody remembered.
func TestASecondActiveClaimForOneArtifactIsRefused(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt)
	if err != nil || !created {
		t.Fatalf("first Save: created=%v err=%v", created, err)
	}

	// A different Claim id, the same artifact — what a second pass produces
	// after a lease lapses, since ids come from crypto/rand.
	created, err = claims.Save(context.Background(), newTestClaim(t, "claim-2", "ev-1"), claimedAt)
	if err != nil {
		t.Fatalf("second Save: %v — losing the race is an outcome, not an error", err)
	}
	if created {
		t.Error("a second active claim was created for one artifact")
	}

	// Nothing of the losing Claim survives. A half-written interpretation — the
	// claim row without its fields, or an event describing a Claim that is not
	// there — would be worse than the duplicate it was preventing.
	var claimCount int
	if err := db.QueryRow(`SELECT count(*) FROM claims`).Scan(&claimCount); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claimCount != 1 {
		t.Errorf("claims = %d, want 1", claimCount)
	}
	for _, table := range []string{"claim_evidence", "claim_fields", "domain_event"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE 1`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		var forLoser int
		switch table {
		case "domain_event":
			if err := db.QueryRow(
				`SELECT count(*) FROM domain_event WHERE payload LIKE '%claim-2%'`).Scan(&forLoser); err != nil {
				t.Fatalf("count events: %v", err)
			}
		default:
			if err := db.QueryRow(
				`SELECT count(*) FROM ` + table + ` WHERE claim_id = 'claim-2'`).Scan(&forLoser); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
		}
		if forLoser != 0 {
			t.Errorf("%s holds %d rows for the rolled-back claim", table, forLoser)
		}
	}

	// The pointer still names the winner.
	var activeClaim string
	if err := db.QueryRow(
		`SELECT claim_id FROM evidence_active_claim WHERE evidence_id = ?`, "ev-1").Scan(&activeClaim); err != nil {
		t.Fatalf("read active claim: %v", err)
	}
	if activeClaim != "claim-1" {
		t.Errorf("active claim = %q, want claim-1", activeClaim)
	}
}

// The duplicate this prevents does not need a crash. Two passes overlap when a
// lease lapses under a slow one, both interpret the same artifact, and both
// try to write. Exactly one may win.
func TestTwoOverlappingPassesProduceOneClaim(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	const passes = 8
	results := make(chan bool, passes)
	errs := make(chan error, passes)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < passes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			created, err := claims.Save(context.Background(),
				newTestClaim(t, fmt.Sprintf("claim-%d", i), "ev-1"), claimedAt)
			if err != nil {
				errs <- err
				return
			}
			results <- created
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Errorf("Save: %v", err)
	}
	won := 0
	for created := range results {
		if created {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d passes created a claim, want exactly 1", won)
	}

	var claimCount int
	if err := db.QueryRow(`SELECT count(*) FROM claims`).Scan(&claimCount); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claimCount != 1 {
		t.Errorf("claims = %d for one artifact, want 1", claimCount)
	}
	// One artifact, one interpretation, one activation event.
	var events int
	if err := db.QueryRow(
		`SELECT count(*) FROM domain_event WHERE type = ?`, eventClaimActivated).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d ClaimActivated events, want 1", events)
	}
}

// A PROPOSED Claim takes no active slot, which is what keeps the proposal
// endpoints of D11/D12 working: an outside proposer may offer a competing
// interpretation of an artifact Billy has already interpreted.
func TestAProposedClaimDoesNotTakeTheActiveSlot(t *testing.T) {
	claims, _, evidence, db := newClaimTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	if created, err := claims.Save(context.Background(), newTestClaim(t, "claim-1", "ev-1"), claimedAt); err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}

	proposed := newProposedClaim(t, "claim-2", "ev-1")
	created, err := claims.Save(context.Background(), proposed, claimedAt)
	if err != nil {
		t.Fatalf("Save proposed: %v", err)
	}
	if !created {
		t.Error("a proposed claim was refused; it competes for no slot")
	}

	var claimCount int
	if err := db.QueryRow(`SELECT count(*) FROM claims`).Scan(&claimCount); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claimCount != 2 {
		t.Errorf("claims = %d, want 2 — one active, one proposed", claimCount)
	}
	// And no ClaimActivated for it: nothing became anyone's interpretation.
	var events int
	if err := db.QueryRow(
		`SELECT count(*) FROM domain_event WHERE type = ?`, eventClaimActivated).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d ClaimActivated events, want 1", events)
	}
}

func newProposedClaim(t *testing.T, id, evidenceID string) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	currency, err := domain.NewTextField("MXN", domain.Low)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	c, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID},
		map[domain.FieldName]domain.ClaimField{
			domain.FieldAmountMinor: amount,
			domain.FieldCurrency:    currency,
		}, claimedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	return c
}
