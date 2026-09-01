package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// storeSpeiTransaction stores one ACTIVE UNRECONCILED Transaction whose Claim
// carries a tracking key, so the match pass can pair it with another.
func storeSpeiTransaction(t *testing.T, transactions *TransactionRepository, claims *ClaimRepository,
	evidence *EvidenceRepository, id, trackingKey string, direction domain.TransactionDirection, counterparty string) {
	t.Helper()
	evidenceID, claimID := "ev-"+id, "claim-"+id
	storeEvidence(t, evidence, evidenceID, "msg-"+id)
	claim := speiClaim(t, claimID, evidenceID, trackingKey, direction, counterparty)
	in := interpretationOf(t, "interp-"+evidenceID, evidenceID, "", claim)
	if created, err := claims.Save(context.Background(), in, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("Save interpretation: created=%v err=%v", created, err)
	}

	money, err := domain.NewMoney(100000, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	draft := domain.TransactionDraft{
		ID: id, Money: money, Counterparty: counterparty, Direction: direction,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: observed, EvidenceIDs: []string{evidenceID}, CreatedAt: builtAt,
	}
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := transactions.Save(context.Background(), one(tx, claimID), builtAt); err != nil || !created {
		t.Fatalf("Save %s: created=%v err=%v", id, created, err)
	}
}

func speiClaim(t *testing.T, id, evidenceID, trackingKey string, direction domain.TransactionDirection, counterparty string) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatal(err)
	}
	occurred, err := domain.NewTimeField(observed, domain.High)
	if err != nil {
		t.Fatal(err)
	}
	text := func(v string, c domain.Confidence) domain.ClaimField {
		f, err := domain.NewTextField(v, c)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	fields := map[domain.FieldName]domain.ClaimField{
		domain.FieldAmountMinor:     amount,
		domain.FieldCurrency:        text("MXN", domain.Low),
		domain.FieldDirection:       text(string(direction), domain.High),
		domain.FieldFinancialStatus: text("SETTLED", domain.High),
		domain.FieldOccurredAt:      occurred,
		domain.FieldTrackingKey:     text(trackingKey, domain.High),
	}
	if counterparty != "" {
		fields[domain.FieldCounterparty] = text(counterparty, domain.High)
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID}, fields, claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	active, err := proposed.Activate(claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

// provenanceOf reads the Evidence ids a Transaction holds, sorted. A survivor
// carries the ids of both merged Sources, so the two-source fact is kept (D70).
func provenanceOf(t *testing.T, db *sql.DB, id string) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT evidence_id FROM transaction_evidence WHERE transaction_id = ? ORDER BY evidence_id`, id)
	if err != nil {
		t.Fatalf("provenanceOf %s: %v", id, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var evidenceID string
		if err := rows.Scan(&evidenceID); err != nil {
			t.Fatalf("provenanceOf %s: scan: %v", id, err)
		}
		ids = append(ids, evidenceID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("provenanceOf %s: %v", id, err)
	}
	return ids
}

// The whole slice, end to end: two Transactions that share a tracking key merge
// into one; the survivor carries both Evidence ids; the other is SUPERSEDED and
// points at the survivor; the merged row leaves the list and the totals but
// stays in history (D36, D70).
func TestTrackingKeyMatchMergesTwoTransactions(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-email", "NU3AG95K", domain.Outflow, "Ada Lovelace")
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-stmt", "NU3AG95K", domain.Outflow, "Ada Lovelace")

	result, err := NewReconciliationRepository(db).UnreconciledWithTrackingKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("tracking-keyed = %d, want 2", len(result))
	}

	m := app.NewMatcher(NewReconciliationRepository(db))
	res, err := m.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 1 || res.NoMatch != 0 {
		t.Fatalf("result = %+v", res)
	}

	// tx-email < tx-stmt, so the email is the survivor.
	assertState(t, db, "tx-email", "ACTIVE", "RECONCILED", "")
	assertState(t, db, "tx-stmt", "SUPERSEDED", "RECONCILED", "tx-email")

	// The survivor carries both provenances.
	if got := provenanceOf(t, db, "tx-email"); len(got) != 2 {
		t.Errorf("survivor provenance = %v, want ev-tx-email and ev-tx-stmt", got)
	}

	// The merged row is gone from the list and both from the totals it inflated.
	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transactions) != 1 || page.Transactions[0].Transaction.ID() != "tx-email" {
		t.Fatalf("list = %+v, want only tx-email", page)
	}
	totals, err := transactions.Totals(context.Background(), app.TransactionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if totals.ExpenseCount != 1 || totals.ExpenseMinor != 100000 {
		t.Errorf("expense totals = %d over %d rows, want 100000 over 1", totals.ExpenseMinor, totals.ExpenseCount)
	}

	// The superseded row stays in history.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE id = 'tx-stmt'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("superseded row count = %d, want 1 (history is kept)", count)
	}

	// The candidate records the canonical pair and the outcome.
	var left, right, status string
	if err := db.QueryRow(`SELECT left_ref, right_ref, status FROM reconciliation_candidate`).Scan(&left, &right, &status); err != nil {
		t.Fatal(err)
	}
	if left != "tx-email" || right != "tx-stmt" || status != "MATCH" {
		t.Errorf("candidate = (%s, %s, %s), want (tx-email, tx-stmt, MATCH)", left, right, status)
	}
}

// Re-running the pass merges nothing new: the constraint on the candidate pair
// is the idempotency, and the merged rows are no longer selectable (D70).
func TestReconciliationIsIdempotent(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-email", "NU3AG95K", domain.Outflow, "Ada Lovelace")
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-stmt", "NU3AG95K", domain.Outflow, "Ada Lovelace")

	m := app.NewMatcher(NewReconciliationRepository(db))
	if _, err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := m.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.CandidatesRecorded != 0 || second.Merged != 0 {
		t.Fatalf("second pass = %+v, want nothing new", second)
	}

	var candidates, superseded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_candidate`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE transaction_state = 'SUPERSEDED'`).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 || superseded != 1 {
		t.Errorf("candidates=%d superseded=%d, want 1 and 1", candidates, superseded)
	}
}

// Opposite directions with one tracking key are NO_MATCH, and neither row moves
// (DOMAIN.md §6).
func TestOppositeDirectionsNeverMerge(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-out", "NU3AG95K", domain.Outflow, "Ada")
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-in", "NU3AG95K", domain.Inflow, "Ada")

	res, err := app.NewMatcher(NewReconciliationRepository(db)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.NoMatch != 1 {
		t.Fatalf("result = %+v", res)
	}
	assertState(t, db, "tx-out", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-in", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "NO_MATCH")
}

// An internal movement never reconciles with an external one, even on a shared
// tracking key (D55).
func TestInternalAndExternalNeverMerge(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-self", "NU3AG95K", domain.Outflow, domain.CounterpartySelf)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-out", "NU3AG95K", domain.Outflow, "Ada")

	res, err := app.NewMatcher(NewReconciliationRepository(db)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.NoMatch != 1 {
		t.Fatalf("result = %+v", res)
	}
	assertState(t, db, "tx-self", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-out", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "NO_MATCH")
}

// The store records an AMBIGUOUS candidate and merges nothing: the critical rule
// that ambiguous pairs are never auto-merged holds at the persistence layer
// (DOMAIN.md §6). The weak-signal producer that reaches AMBIGUOUS is a later
// slice; this proves the write path already refuses to merge it.
func TestAmbiguousCandidateIsRecordedButNotMerged(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-a", "KEYA", domain.Outflow, "Ada")
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-b", "KEYB", domain.Outflow, "Ada")

	created, err := NewReconciliationRepository(db).Reconcile(context.Background(), app.ReconcileDecision{
		CandidateID: "cand-1", LeftID: "tx-a", RightID: "tx-b", Outcome: domain.Ambiguous,
	}, builtAt)
	if err != nil || !created {
		t.Fatalf("Reconcile: created=%v err=%v", created, err)
	}
	assertState(t, db, "tx-a", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-b", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "AMBIGUOUS")
}

// assertState reads back one Transaction's lifecycle columns (D49, D70). A
// survivor stays ACTIVE and turns RECONCILED; the loser turns SUPERSEDED and
// points at the survivor. wantSupersededBy is empty for a row that still stands.
func assertState(t *testing.T, db *sql.DB, id, wantState, wantRecon, wantSupersededBy string) {
	t.Helper()
	var state, recon string
	var supersededBy sql.NullString
	if err := db.QueryRow(
		`SELECT transaction_state, reconciliation_state, superseded_by_transaction_id
		 FROM transactions WHERE id = ?`, id).Scan(&state, &recon, &supersededBy); err != nil {
		t.Fatalf("assertState %s: %v", id, err)
	}
	if state != wantState || recon != wantRecon || supersededBy.String != wantSupersededBy {
		t.Errorf("%s = (%s, %s, %q), want (%s, %s, %q)",
			id, state, recon, supersededBy.String, wantState, wantRecon, wantSupersededBy)
	}
}

// assertCandidateStatus reads back the one candidate the pass wrote and checks
// its outcome (D69). The tests that call it record exactly one pair.
func assertCandidateStatus(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM reconciliation_candidate`).Scan(&status); err != nil {
		t.Fatalf("assertCandidateStatus: %v", err)
	}
	if status != want {
		t.Errorf("candidate status = %s, want %s", status, want)
	}
}

// The weak sweep, end to end: two external Transactions in one amount block with
// no shared tracking key are AMBIGUOUS. Nothing merges, both stay in the list
// and the totals, and a re-run records nothing new (D71).
func TestWeakSignalPairIsAmbiguousAndNeverMerges(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-a", "KEYA", domain.Outflow, "Ada")
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-b", "KEYB", domain.Outflow, "Ada")

	m := app.NewMatcher(NewReconciliationRepository(db))
	res, err := m.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.NoMatch != 0 || res.Ambiguous != 1 {
		t.Fatalf("result = %+v, want one AMBIGUOUS", res)
	}
	assertState(t, db, "tx-a", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-b", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "AMBIGUOUS")

	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transactions) != 2 {
		t.Fatalf("list = %d rows, want 2 (nothing merged)", len(page.Transactions))
	}
	totals, err := transactions.Totals(context.Background(), app.TransactionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if totals.ExpenseCount != 2 || totals.ExpenseMinor != 200000 {
		t.Errorf("expense totals = %d over %d rows, want 200000 over 2", totals.ExpenseMinor, totals.ExpenseCount)
	}

	second, err := m.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.CandidatesRecorded != 0 {
		t.Fatalf("second pass = %+v, want nothing new", second)
	}
}

// An internal movement against an external one in one amount block is NO_MATCH
// through the weak sweep, with no shared tracking key (D55, D71).
func TestWeakSignalInternalVsExternalIsNoMatch(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-self", "KEYA", domain.Outflow, domain.CounterpartySelf)
	storeSpeiTransaction(t, transactions, claims, evidence, "tx-ext", "KEYB", domain.Outflow, "Ada")

	res, err := app.NewMatcher(NewReconciliationRepository(db)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.NoMatch != 1 || res.Ambiguous != 0 {
		t.Fatalf("result = %+v, want one NO_MATCH", res)
	}
	assertState(t, db, "tx-self", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-ext", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "NO_MATCH")
}

// storeSourcedSpei stores one ACTIVE UNRECONCILED Transaction from a named
// Source, with a merchant and a distinct tracking key. Distinct keys keep the
// tracking-key sweep from pairing them, so the composite sweep decides (D72).
func storeSourcedSpei(t *testing.T, transactions *TransactionRepository, claims *ClaimRepository,
	evidence *EvidenceRepository, id, sourceID string, sourceType domain.SourceType, trackingKey, merchant string) {
	t.Helper()
	evidenceID, claimID := "ev-"+id, "claim-"+id
	e, err := domain.NewEvidence(evidenceID, sourceID, sourceType, "ref-"+id,
		"message/rfc822", []byte("From: nu@nu.com.mx\r\n\r\nmovimiento"), observed)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := evidence.Insert(context.Background(), e, testProfile, ingestedAt); err != nil || !created {
		t.Fatalf("Insert %s: created=%v err=%v", id, created, err)
	}
	claim := speiClaim(t, claimID, evidenceID, trackingKey, domain.Outflow, "Ada")
	in := interpretationOf(t, "interp-"+evidenceID, evidenceID, "", claim)
	if created, err := claims.Save(context.Background(), in, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("Save interpretation %s: created=%v err=%v", id, created, err)
	}

	money, err := domain.NewMoney(100000, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Merchant: merchant, Direction: domain.Outflow,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: observed, EvidenceIDs: []string{evidenceID}, CreatedAt: builtAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := transactions.Save(context.Background(), one(tx, claimID), builtAt); err != nil || !created {
		t.Fatalf("Save %s: created=%v err=%v", id, created, err)
	}
}

// The composite sweep, end to end: an email and a statement of one movement,
// with no shared tracking key, merge on the exact composite key. The survivor
// carries both Evidence ids and the event's basis is composite (D72).
func TestCompositeMatchMergesAcrossSources(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSourcedSpei(t, transactions, claims, evidence, "tx-email", "gmail_primary", domain.SourceGmail, "KEYEMAIL", "Plomero")
	storeSourcedSpei(t, transactions, claims, evidence, "tx-stmt", "statement_jun", domain.SourceBankStatement, "KEYSTMT", "Plomero")

	res, err := app.NewMatcher(NewReconciliationRepository(db)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 1 || res.Ambiguous != 0 || res.NoMatch != 0 {
		t.Fatalf("result = %+v, want one composite MATCH", res)
	}
	assertState(t, db, "tx-email", "ACTIVE", "RECONCILED", "")
	assertState(t, db, "tx-stmt", "SUPERSEDED", "RECONCILED", "tx-email")
	if got := provenanceOf(t, db, "tx-email"); len(got) != 2 {
		t.Errorf("survivor provenance = %v, want both Sources", got)
	}
	var basis string
	if err := db.QueryRow(
		`SELECT json_extract(payload,'$.basis') FROM domain_event WHERE type='TransactionReconciled'`).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "composite" {
		t.Errorf("event basis = %q, want composite", basis)
	}
}

// Two same-Source look-alikes with no shared tracking key never composite-merge:
// they stay AMBIGUOUS, because one Source seeing two is two events (D72).
func TestCompositeKeepsSameSourceApart(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeSourcedSpei(t, transactions, claims, evidence, "tx-a", "gmail_primary", domain.SourceGmail, "KEYA", "Oxxo")
	storeSourcedSpei(t, transactions, claims, evidence, "tx-b", "gmail_primary", domain.SourceGmail, "KEYB", "Oxxo")

	res, err := app.NewMatcher(NewReconciliationRepository(db)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.Ambiguous != 1 {
		t.Fatalf("result = %+v, want no merge and one AMBIGUOUS", res)
	}
	assertState(t, db, "tx-a", "ACTIVE", "UNRECONCILED", "")
	assertState(t, db, "tx-b", "ACTIVE", "UNRECONCILED", "")
	assertCandidateStatus(t, db, "AMBIGUOUS")
}
