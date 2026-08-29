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
