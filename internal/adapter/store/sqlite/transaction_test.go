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

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

var builtAt = time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)

func newTransactionTestRepo(t *testing.T) (*TransactionRepository, *ClaimRepository, *EvidenceQueue, *EvidenceRepository, *sql.DB) {
	t.Helper()
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewTransactionRepository(db), NewClaimRepository(db), NewEvidenceQueue(db), NewEvidenceRepository(db), db
}

// extractedArtifact moves one artifact through ingestion and extraction. The
// reconciliation tests below then start at an Evidence row at stage EXTRACTED
// with one ACTIVE Claim.
func extractedArtifact(t *testing.T, claims *ClaimRepository, evidence *EvidenceRepository, evidenceID, reference, claimID string) domain.Claim {
	t.Helper()
	storeEvidence(t, evidence, evidenceID, reference)
	claim := newTransactionalClaim(t, claimID, evidenceID)
	in := interpretationOf(t, "interp-"+evidenceID, evidenceID, "", claim)
	created, err := claims.Save(context.Background(), in, claimedAt)
	if err != nil || !created {
		t.Fatalf("Save interpretation: created=%v err=%v", created, err)
	}
	return claim
}

// one puts a Transaction and the id of its Claim into the set that Save takes.
func one(tx domain.Transaction, sourceClaimID string) []app.BuiltTransaction {
	return []app.BuiltTransaction{{Transaction: tx, SourceClaimID: sourceClaimID}}
}

// newTransactionalClaim is the interpretation of an outflow receipt. It holds
// each field that a Transaction needs, and newTestClaim in claim_test.go
// omits the direction.
func newTransactionalClaim(t *testing.T, id, evidenceID string) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	occurred, err := domain.NewTimeField(observed, domain.High)
	if err != nil {
		t.Fatalf("NewTimeField: %v", err)
	}
	text := func(v string, c domain.Confidence) domain.ClaimField {
		f, err := domain.NewTextField(v, c)
		if err != nil {
			t.Fatalf("NewTextField: %v", err)
		}
		return f
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID},
		map[domain.FieldName]domain.ClaimField{
			domain.FieldAmountMinor:     amount,
			domain.FieldCurrency:        text("MXN", domain.Low),
			domain.FieldMerchant:        text("HSBC beneficiary", domain.Medium),
			domain.FieldDirection:       text("OUTFLOW", domain.High),
			domain.FieldFinancialStatus: text("SETTLED", domain.High),
			domain.FieldOccurredAt:      occurred,
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

func newTestTransaction(t *testing.T, id, evidenceID string) domain.Transaction {
	t.Helper()
	money, err := domain.NewMoney(100000, "MXN")
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID:                  id,
		Money:               money,
		Merchant:            "HSBC beneficiary",
		Direction:           domain.Outflow,
		FinancialStatus:     domain.StatusSettled,
		ReconciliationState: domain.Unreconciled,
		State:               domain.TransactionActive,
		OccurredAt:          observed,
		EvidenceIDs:         []string{evidenceID},
		CreatedAt:           builtAt,
	})
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	return tx
}

// One commit writes the Transaction, its provenance, its event, its slot and
// the stage advance. All five, or none of them.
func TestSaveWritesTheTransactionItsProvenanceAndItsEvent(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	claim := extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")

	created, err := transactions.Save(context.Background(), one(newTestTransaction(t, "tx-1", "ev-1"), claim.ID()), builtAt)
	if err != nil || !created {
		t.Fatalf("Save: created=%v err=%v", created, err)
	}

	var amount int64
	var currency, merchant, direction, status, state, occurredAt string
	var account sql.NullString
	err = db.QueryRow(`
		SELECT amount_minor, currency, merchant, account_identifier,
		       direction, financial_status, reconciliation_state, occurred_at
		FROM transactions WHERE id = 'tx-1'`,
	).Scan(&amount, &currency, &merchant, &account, &direction, &status, &state, &occurredAt)
	if err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if amount != 100000 || currency != "MXN" {
		t.Errorf("money = %d %s, want 100000 MXN", amount, currency)
	}
	if direction != "OUTFLOW" || status != "SETTLED" || state != "UNRECONCILED" {
		t.Errorf("direction/status/state = %s/%s/%s", direction, status, state)
	}
	// DATA_MODEL.md §2: absence is NULL, never a stand-in value. No Nu template
	// states the user's own account.
	if account.Valid {
		t.Errorf("account_identifier = %q, want NULL", account.String)
	}
	if occurredAt != formatTime(observed) {
		t.Errorf("occurred_at = %q, want %q", occurredAt, formatTime(observed))
	}

	var provenance int
	if err := db.QueryRow(`SELECT count(*) FROM transaction_evidence WHERE transaction_id = 'tx-1' AND evidence_id = 'ev-1'`).Scan(&provenance); err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if provenance != 1 {
		t.Error("a Transaction without provenance to Evidence is invalid (DOMAIN.md §4)")
	}

	var slot string
	if err := db.QueryRow(`SELECT transaction_id FROM claim_transaction WHERE claim_id = 'claim-1'`).Scan(&slot); err != nil {
		t.Fatalf("read slot: %v", err)
	}
	if slot != "tx-1" {
		t.Errorf("slot = %q, want tx-1", slot)
	}

	// DOMAIN.md §8 — TransactionCreated, in the same commit (D25).
	var payload string
	if err := db.QueryRow(`SELECT payload FROM domain_event WHERE type = 'TransactionCreated'`).Scan(&payload); err != nil {
		t.Fatalf("read event: %v", err)
	}
	var event struct {
		TransactionID string   `json:"transactionId"`
		EvidenceIDs   []string `json:"evidenceIds"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	if event.TransactionID != "tx-1" || len(event.EvidenceIDs) != 1 || event.EvidenceIDs[0] != "ev-1" {
		t.Errorf("event = %+v", event)
	}

	// The stage advance rides in the same transaction, for the reason the Claim's
	// does: a row marked RECONCILED whose Transaction rolled back is an artifact
	// Billy never revisits with nothing to show for it.
	if stage := readStage(t, db, "ev-1"); stage != stageReconciled {
		t.Errorf("stage = %q, want RECONCILED", stage)
	}
}

// SECURITY.md §10 — the event log is a record of facts about Transactions, not
// a second copy of someone's finances.
func TestTheTransactionEventCarriesNoMoney(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	claim := extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")
	if _, err := transactions.Save(context.Background(), one(newTestTransaction(t, "tx-1", "ev-1"), claim.ID()), builtAt); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var payload string
	if err := db.QueryRow(`SELECT payload FROM domain_event WHERE type = 'TransactionCreated'`).Scan(&payload); err != nil {
		t.Fatalf("read event: %v", err)
	}
	for _, leaked := range []string{"100000", "MXN", "HSBC beneficiary", "OUTFLOW"} {
		if strings.Contains(payload, leaked) {
			t.Errorf("payload %q carries %q", payload, leaked)
		}
	}
}

// D41. The second Save for one Claim writes nothing and says so, and it does
// that because the primary key refuses it — not because Save looked first.
func TestASecondTransactionForOneClaimIsRefused(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	claim := extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")
	ctx := context.Background()

	if created, err := transactions.Save(ctx, one(newTestTransaction(t, "tx-1", "ev-1"), claim.ID()), builtAt); err != nil || !created {
		t.Fatalf("first Save: created=%v err=%v", created, err)
	}
	created, err := transactions.Save(ctx, one(newTestTransaction(t, "tx-2", "ev-1"), claim.ID()), builtAt)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if created {
		t.Error("a second Transaction was created for one Claim")
	}

	// And nothing of the losing attempt survives: no orphan row, no provenance
	// link, no event describing a Transaction that does not exist.
	assertCount(t, db, `SELECT count(*) FROM transactions`, 1)
	assertCount(t, db, `SELECT count(*) FROM transaction_evidence`, 1)
	assertCount(t, db, `SELECT count(*) FROM domain_event WHERE type = 'TransactionCreated'`, 1)
}

// The race D41 closes needs no crash: a pass that runs longer than its lease
// and a second pass that claims the same row will both build. Eight goroutines
// is the same shape migration 003's test uses against the Claim slot.
func TestConcurrentPassesProduceExactlyOneTransaction(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	claim := extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")

	var wg sync.WaitGroup
	createdCount := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := newTestTransaction(t, fmt.Sprintf("tx-%d", i), "ev-1")
			created, err := transactions.Save(context.Background(), one(tx, claim.ID()), builtAt)
			if err != nil {
				t.Errorf("Save: %v", err)
				return
			}
			createdCount <- created
		}(i)
	}
	wg.Wait()
	close(createdCount)

	wins := 0
	for created := range createdCount {
		if created {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d passes reported creating a Transaction, want exactly 1", wins)
	}
	assertCount(t, db, `SELECT count(*) FROM transactions`, 1)
	assertCount(t, db, `SELECT count(*) FROM domain_event WHERE type = 'TransactionCreated'`, 1)
}

// Without the originating Claim there is no slot to take, and without a slot
// there is no idempotency. Refusing is better than writing a row nothing can
// deduplicate.
func TestSaveRefusesATransactionWithNoOriginatingClaim(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")

	if _, err := transactions.Save(context.Background(), one(newTestTransaction(t, "tx-1", "ev-1"), ""), builtAt); err == nil {
		t.Error("Save accepted a Transaction with no originating Claim")
	}
	assertCount(t, db, `SELECT count(*) FROM transactions`, 0)
}

// The queue's reconciliation end: it claims rows at EXTRACTED, hands back the
// ACTIVE Claim rehydrated through the domain, and returns observed_at for the
// fallback DATA_MODEL.md §4.5 requires.
func TestClaimForReconciliationReturnsTheActiveClaim(t *testing.T) {
	_, claims, queue, evidence, _ := newTransactionTestRepo(t)
	extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")

	pending, err := queue.ClaimForReconciliation(context.Background(), 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(pending))
	}
	work := pending[0]
	if work.EvidenceID != "ev-1" || len(work.Claims) != 1 {
		t.Fatalf("work = %+v, want ev-1 with one Claim", work)
	}
	claim := work.Claims[0]
	if claim.ID() != "claim-1" || claim.State() != domain.ClaimActive {
		t.Errorf("claim = %s/%s, want claim-1/ACTIVE", claim.ID(), claim.State())
	}
	if !work.ObservedAt.Equal(observed) {
		t.Errorf("observed_at = %s, want %s — DATA_MODEL.md §4.5's fallback needs it", work.ObservedAt, observed)
	}
	// The store reads the Claim through the domain constructor, so the values
	// do not change.
	money, ok := claim.Money()
	if !ok || money.Minor() != 100000 || money.Currency() != "MXN" {
		t.Errorf("money = %s (present=%v)", money, ok)
	}
	if got, ok := claim.OccurredAt(); !ok || !got.Equal(observed) {
		t.Errorf("occurred_at = %s (present=%v)", got, ok)
	}
	if field, ok := claim.Field(domain.FieldMerchant); !ok || field.Confidence() != domain.Medium {
		t.Errorf("merchant confidence did not survive rehydration: %v/%v", field.Confidence(), ok)
	}
}

// D44. An artifact that produced no Claim is still handed out — it has to leave
// the queue, and filtering it here would leave it at EXTRACTED forever.
func TestClaimForReconciliationReturnsRowsWithNoClaim(t *testing.T) {
	_, _, queue, evidence, db := newTransactionTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")
	// Extraction found no financial event and advanced the row on its own.
	if err := queue.MarkExtracted(context.Background(), "ev-1", claimedAt); err != nil {
		t.Fatalf("MarkExtracted: %v", err)
	}

	pending, err := queue.ClaimForReconciliation(context.Background(), 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(pending) != 1 || len(pending[0].Claims) != 0 {
		t.Fatalf("pending = %+v, want one row with no Claim", pending)
	}

	if err := queue.MarkReconciled(context.Background(), "ev-1", builtAt); err != nil {
		t.Fatalf("MarkReconciled: %v", err)
	}
	if stage := readStage(t, db, "ev-1"); stage != stageReconciled {
		t.Errorf("stage = %q, want RECONCILED", stage)
	}
}

// A row still at RECEIVED is not reconciliation's work, and a locked row is
// somebody else's.
func TestClaimForReconciliationSkipsRowsThatAreNotItsToTake(t *testing.T) {
	_, claims, queue, evidence, _ := newTransactionTestRepo(t)
	storeEvidence(t, evidence, "ev-received", "msg-1")
	extractedArtifact(t, claims, evidence, "ev-locked", "msg-2", "claim-2")
	extractedArtifact(t, claims, evidence, "ev-free", "msg-3", "claim-3")

	first, err := queue.ClaimForReconciliation(context.Background(), 1, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("claimed %d, want 1", len(first))
	}

	second, err := queue.ClaimForReconciliation(context.Background(), 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("claimed %d, want the one row left at EXTRACTED and unlocked", len(second))
	}
	if second[0].EvidenceID == first[0].EvidenceID {
		t.Error("a locked row was claimed twice")
	}
	for _, work := range append(first, second...) {
		if work.EvidenceID == "ev-received" {
			t.Error("a row still at RECEIVED was claimed for reconciliation")
		}
	}
}

// MarkReconciled is guarded on the row still being at EXTRACTED, so a row some
// other pass already advanced is left alone rather than re-advanced.
func TestMarkReconciledLeavesRowsAtOtherStagesAlone(t *testing.T) {
	_, _, queue, evidence, db := newTransactionTestRepo(t)
	storeEvidence(t, evidence, "ev-1", "msg-1")

	if err := queue.MarkReconciled(context.Background(), "ev-1", builtAt); err != nil {
		t.Fatalf("MarkReconciled: %v", err)
	}
	if stage := readStage(t, db, "ev-1"); stage != stageReceived {
		t.Errorf("stage = %q, want RECEIVED — a row at another stage is not this pass's to advance", stage)
	}
}

// The whole pipeline, end to end, twice — the M1 shape of the proof: the second
// pass creates nothing and changes nothing.
func TestTheSecondReconciliationPassCreatesNothing(t *testing.T) {
	transactions, claims, queue, evidence, db := newTransactionTestRepo(t)
	claim := extractedArtifact(t, claims, evidence, "ev-1", "msg-1", "claim-1")
	ctx := context.Background()

	if created, err := transactions.Save(ctx, one(newTestTransaction(t, "tx-1", "ev-1"), claim.ID()), builtAt); err != nil || !created {
		t.Fatalf("first Save: created=%v err=%v", created, err)
	}

	// The row is at RECONCILED, so a second pass does not even claim it.
	pending, err := queue.ClaimForReconciliation(ctx, 10, builtAt, builtAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimForReconciliation: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("claimed %d rows, want none — the work is done", len(pending))
	}
	assertCount(t, db, `SELECT count(*) FROM transactions`, 1)
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", query, got, want)
	}
}
