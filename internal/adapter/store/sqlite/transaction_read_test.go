package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

func readableTransaction(t *testing.T, id, evidenceID string, minor int64, direction domain.TransactionDirection, at time.Time) domain.Transaction {
	t.Helper()
	money, err := domain.NewMoney(minor, domain.Currency("MXN"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Merchant: "HSBC beneficiary", Direction: direction,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: at, EvidenceIDs: []string{evidenceID}, CreatedAt: builtAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func storeReadableTransaction(t *testing.T, transactions *TransactionRepository, claims *ClaimRepository,
	evidence *EvidenceRepository, id string, at time.Time, minor int64, direction domain.TransactionDirection) {
	t.Helper()
	evidenceID, claimID := "ev-"+id, "claim-"+id
	claim := extractedArtifact(t, claims, evidence, evidenceID, "msg-"+id, claimID)
	created, err := transactions.Save(context.Background(), one(
		readableTransaction(t, id, evidenceID, minor, direction, at), claim.ID()), builtAt)
	if err != nil || !created {
		t.Fatalf("Save %s: created=%v err=%v", id, created, err)
	}
}

func TestListTransactionsOrdersFiltersAndPaginates(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	day2 := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-a", day2, 100, domain.Outflow)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-b", day2, 200, domain.Inflow)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-c", day3, 300, domain.Outflow)

	first, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || len(first.Transactions) != 2 || first.Transactions[0].Transaction.ID() != "tx-c" || first.Transactions[1].Transaction.ID() != "tx-b" {
		t.Fatalf("first page = %+v", first)
	}
	if first.Transactions[1].MerchantConfidence != domain.Medium {
		t.Errorf("merchant confidence = %s, want MEDIUM", first.Transactions[1].MerchantConfidence)
	}

	second, err := transactions.List(context.Background(), app.TransactionQuery{
		Limit: 2, After: &app.TransactionCursor{OccurredAt: day2, ID: "tx-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || len(second.Transactions) != 1 || second.Transactions[0].Transaction.ID() != "tx-a" {
		t.Fatalf("second page = %+v", second)
	}

	to := day3
	filtered, err := transactions.List(context.Background(), app.TransactionQuery{
		To: &to, Directions: []domain.TransactionDirection{domain.Outflow}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Transactions) != 1 || filtered.Transactions[0].Transaction.ID() != "tx-a" {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestListTransactionsReturnsOnlyActiveRows(t *testing.T) {
	transactions, claims, _, evidence, db := newTransactionTestRepo(t)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-active", observed, 100, domain.Outflow)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-old", observed, 200, domain.Outflow)
	if _, err := db.Exec(`UPDATE transactions SET transaction_state = 'SUPERSEDED' WHERE id = 'tx-old'`); err != nil {
		t.Fatal(err)
	}

	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transactions) != 1 || page.Transactions[0].Transaction.ID() != "tx-active" {
		t.Fatalf("page = %+v", page)
	}
}

// The counterparty survives the write and the read, and it is not the merchant
// (D61). Migration 008 added the column; a Transaction that lost it here would
// discard a field the SPEI reading works to produce.
func TestACounterpartySurvivesTheRoundTrip(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	storeCounterpartyTransaction(t, transactions, claims, evidence, "tx-spei", "VIAJE74 COMIDA75 ALVAREZ76")

	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transactions) != 1 {
		t.Fatalf("page = %+v", page)
	}
	item := page.Transactions[0]
	if got := item.Transaction.Counterparty(); got != "VIAJE74 COMIDA75 ALVAREZ76" {
		t.Errorf("counterparty = %q", got)
	}
	if item.CounterpartyConfidence != domain.High {
		t.Errorf("counterparty confidence = %s, want HIGH", item.CounterpartyConfidence)
	}
	// The two fields do not overwrite each other.
	if got := item.Transaction.Merchant(); got != "HSBC beneficiary" {
		t.Errorf("merchant = %q", got)
	}
}

// Most movements name no counterparty, and the column is NULL for each of them.
// Absence must stay absence: no email template yields one today.
func TestATransactionWithNoCounterpartyReloadsWithout(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	storeReadableTransaction(t, transactions, claims, evidence, "tx-plain", observed, 100, domain.Outflow)

	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Transactions) != 1 {
		t.Fatalf("page = %+v", page)
	}
	item := page.Transactions[0]
	if got := item.Transaction.Counterparty(); got != "" {
		t.Errorf("counterparty = %q, want absent", got)
	}
	if item.CounterpartyConfidence != "" {
		t.Errorf("counterparty confidence = %s, want none", item.CounterpartyConfidence)
	}
	if got := item.Transaction.Merchant(); got != "HSBC beneficiary" {
		t.Errorf("merchant = %q", got)
	}
}

// storeCounterpartyTransaction writes one Transaction that names a counterparty,
// with the Claim field that supports it.
func storeCounterpartyTransaction(t *testing.T, transactions *TransactionRepository, claims *ClaimRepository,
	evidence *EvidenceRepository, id, counterparty string) {
	t.Helper()
	evidenceID, claimID := "ev-"+id, "claim-"+id
	storeEvidence(t, evidence, evidenceID, "msg-"+id)
	claim := claimWithCounterparty(t, claimID, evidenceID, counterparty)
	in := interpretationOf(t, "interp-"+evidenceID, evidenceID, "", claim)
	if created, err := claims.Save(context.Background(), in, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("Save interpretation: created=%v err=%v", created, err)
	}

	tx := readableTransaction(t, id, evidenceID, 100, domain.Outflow, observed)
	draft := domain.TransactionDraft{
		ID: tx.ID(), Merchant: tx.Merchant(), Counterparty: counterparty,
		Direction: tx.Direction(), FinancialStatus: tx.FinancialStatus(),
		ReconciliationState: tx.ReconciliationState(), State: tx.State(),
		OccurredAt: tx.OccurredAt(), EvidenceIDs: tx.EvidenceIDs(), CreatedAt: builtAt,
	}
	if money, ok := tx.Money(); ok {
		draft.Money = money
	}
	withCounterparty, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	if created, err := transactions.Save(context.Background(), one(withCounterparty, claim.ID()), builtAt); err != nil || !created {
		t.Fatalf("Save %s: created=%v err=%v", id, created, err)
	}
}

// claimWithCounterparty is newTransactionalClaim plus the counterparty field.
// The reading names the person, so D34 rates it HIGH.
func claimWithCounterparty(t *testing.T, id, evidenceID, counterparty string) domain.Claim {
	t.Helper()
	base := newTransactionalClaim(t, id, evidenceID)
	fields := map[domain.FieldName]domain.ClaimField{}
	for _, name := range base.FieldNames() {
		field, _ := base.Field(name)
		fields[name] = field
	}
	value, err := domain.NewTextField(counterparty, domain.High)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	fields[domain.FieldCounterparty] = value

	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID}, fields, claimedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	active, err := proposed.Activate(claimedAt)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return active
}

// Totals sum income and spending and leave the internal movement out of both
// (D55). The internal movement is still counted, so a reader can say so.
func TestTotalsExcludeInternalMovements(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	day := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	storeReadableTransaction(t, transactions, claims, evidence, "in-1", day, 30000, domain.Inflow)
	storeReadableTransaction(t, transactions, claims, evidence, "out-1", day, 50000, domain.Outflow)
	// An internal outflow of the user's own money. It must touch no total.
	storeCounterpartyTransaction(t, transactions, claims, evidence, "internal-1", domain.CounterpartySelf)

	totals, err := transactions.Totals(context.Background(), app.TransactionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if totals.IncomeMinor != 30000 || totals.IncomeCount != 1 {
		t.Errorf("income = %d (%d), want 30000 (1)", totals.IncomeMinor, totals.IncomeCount)
	}
	if totals.ExpenseMinor != 50000 || totals.ExpenseCount != 1 {
		t.Errorf("expense = %d (%d), want 50000 (1): the internal outflow must not count", totals.ExpenseMinor, totals.ExpenseCount)
	}
	if totals.ExcludedInternal != 1 {
		t.Errorf("excluded internal = %d, want 1", totals.ExcludedInternal)
	}
	if totals.Currency != domain.Currency("MXN") {
		t.Errorf("currency = %s, want MXN", totals.Currency)
	}
}

// A SPEI that leaves one of the user's accounts and arrives in another is the
// same money, not income and not spending (D75). The two rows stay; only the
// totals drop them. HSB and HSBC are one key: Nu still prints the wrap.
func TestTotalsExcludeOwnAccountTransfers(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	day := time.Date(2026, 7, 31, 19, 35, 0, 0, time.UTC)
	storeSourcedKeyed(t, transactions, claims, evidence, "tx-hsbc", "hsbc_statements", domain.SourceBankStatement,
		"HSBC825834", domain.Outflow, 500000, "o", day)
	storeSourcedKeyed(t, transactions, claims, evidence, "tx-nu", "nu_statements", domain.SourceBankStatement,
		"HSB825834", domain.Inflow, 500000, "ALEJANDRO", day)
	storeSourcedKeyed(t, transactions, claims, evidence, "tx-pay", "hsbc_statements", domain.SourceBankStatement,
		"PAYROLL1", domain.Inflow, 1784458, "", day)

	totals, err := transactions.Totals(context.Background(), app.TransactionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if totals.IncomeMinor != 1784458 || totals.IncomeCount != 1 {
		t.Errorf("income = %d (%d), want payroll only 1784458 (1)", totals.IncomeMinor, totals.IncomeCount)
	}
	if totals.ExpenseMinor != 0 || totals.ExpenseCount != 0 {
		t.Errorf("expense = %d (%d), want none: the Flex outflow is a transfer", totals.ExpenseMinor, totals.ExpenseCount)
	}
	if totals.ExcludedInternal != 2 {
		t.Errorf("excluded internal = %d, want 2 transfer rows", totals.ExcludedInternal)
	}

	page, err := transactions.List(context.Background(), app.TransactionQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	flag := map[string]bool{}
	for _, item := range page.Transactions {
		flag[item.Transaction.ID()] = item.OwnAccountTransfer
	}
	if !flag["tx-hsbc"] || !flag["tx-nu"] {
		t.Errorf("transfer flags = %v, want both SPEI sides true", flag)
	}
	if flag["tx-pay"] {
		t.Error("payroll was flagged as an own-account transfer")
	}
}

// storeSourcedKeyed writes one Transaction from a named Source with a tracking
// key, so D75 can pair it with the other side.
func storeSourcedKeyed(t *testing.T, transactions *TransactionRepository, claims *ClaimRepository,
	evidence *EvidenceRepository, id, sourceID string, sourceType domain.SourceType,
	trackingKey string, direction domain.TransactionDirection, minor int64, counterparty string, at time.Time) {
	t.Helper()
	evidenceID, claimID := "ev-"+id, "claim-"+id
	e, err := domain.NewEvidence(evidenceID, sourceID, sourceType, "ref-"+id,
		"application/pdf", []byte("%PDF-test"), at)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := evidence.Insert(context.Background(), e, testProfile, ingestedAt); err != nil || !created {
		t.Fatalf("Insert %s: created=%v err=%v", id, created, err)
	}

	amount, err := domain.NewIntField(minor, domain.High)
	if err != nil {
		t.Fatal(err)
	}
	occurred, err := domain.NewTimeField(at, domain.High)
	if err != nil {
		t.Fatal(err)
	}
	text := func(v string, c domain.Confidence) domain.ClaimField {
		t.Helper()
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
	proposed, err := domain.NewClaim(claimID, domain.ClaimProposed, []string{evidenceID}, fields, claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := proposed.Activate(claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	in := interpretationOf(t, "interp-"+evidenceID, evidenceID, "", claim)
	if created, err := claims.Save(context.Background(), in, testProfile, claimedAt); err != nil || !created {
		t.Fatalf("Save interpretation %s: created=%v err=%v", id, created, err)
	}

	money, err := domain.NewMoney(minor, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	draft := domain.TransactionDraft{
		ID: id, Money: money, Counterparty: counterparty, Direction: direction,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: at, EvidenceIDs: []string{evidenceID}, CreatedAt: builtAt,
	}
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := transactions.Save(context.Background(), one(tx, claimID), builtAt); err != nil || !created {
		t.Fatalf("Save %s: created=%v err=%v", id, created, err)
	}
}

// The from and to window filters the totals, as it filters the list.
func TestTotalsHonourTheWindow(t *testing.T) {
	transactions, claims, _, evidence, _ := newTransactionTestRepo(t)
	inside := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	outside := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	storeReadableTransaction(t, transactions, claims, evidence, "in-window", inside, 20000, domain.Outflow)
	storeReadableTransaction(t, transactions, claims, evidence, "out-window", outside, 99999, domain.Outflow)

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	totals, err := transactions.Totals(context.Background(), app.TransactionQuery{From: &from})
	if err != nil {
		t.Fatal(err)
	}
	if totals.ExpenseMinor != 20000 {
		t.Errorf("expense = %d, want 20000: the July row is outside the window", totals.ExpenseMinor)
	}
}
