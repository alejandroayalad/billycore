package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

type stubTransactionReader struct {
	page   app.TransactionPage
	totals app.TransactionTotals
	err    error
	query  app.TransactionQuery
}

func (s *stubTransactionReader) List(_ context.Context, query app.TransactionQuery) (app.TransactionPage, error) {
	s.query = query
	return s.page, s.err
}

func (s *stubTransactionReader) Totals(_ context.Context, query app.TransactionQuery) (app.TransactionTotals, error) {
	s.query = query
	return s.totals, s.err
}

func apiTransaction(t *testing.T, id string, at time.Time) domain.Transaction {
	t.Helper()
	money, err := domain.NewMoney(89993, domain.Currency("MXN"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Merchant: "A merchant", Direction: domain.Outflow,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: at, EvidenceIDs: []string{"ev-1"}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func transactionHandler(t *testing.T, reader app.TransactionReader) http.Handler {
	t.Helper()
	return NewServer(nil, newStubRepo(), reader, nil, nil, stubPinger{}, nil).Handler(testToken)
}

func TestListTransactionsReturnsTheDocumentedRepresentation(t *testing.T) {
	at := time.Date(2026, 8, 23, 13, 58, 0, 0, time.UTC)
	reader := &stubTransactionReader{page: app.TransactionPage{
		Transactions: []app.ListedTransaction{{
			Transaction: apiTransaction(t, "tx-1", at), MerchantConfidence: domain.Medium,
		}},
		HasMore: true,
	}}
	w := request(t, transactionHandler(t, reader), http.MethodGet,
		"/v1/transactions?limit=1", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	got := decode[transactionListResponse](t, w)
	if len(got.Data) != 1 || got.NextCursor == nil {
		t.Fatalf("response = %+v", got)
	}
	tx := got.Data[0]
	if tx.ID != "tx-1" || tx.Amount.AmountMinor != 89993 || tx.Amount.Currency != "MXN" {
		t.Errorf("transaction = %+v", tx)
	}
	if tx.Merchant == nil || tx.Merchant.Value != "A merchant" || tx.Merchant.Confidence != "MEDIUM" {
		t.Errorf("merchant = %+v", tx.Merchant)
	}
	if tx.Account != nil || len(tx.Relationships) != 0 {
		t.Errorf("account/relationships = %+v/%+v", tx.Account, tx.Relationships)
	}
	// A purchase names no counterparty and is not internal.
	if tx.Counterparty != nil || tx.Internal {
		t.Errorf("counterparty/internal = %+v/%v", tx.Counterparty, tx.Internal)
	}
}

// An internal movement carries the reserved counterparty and reports internal.
// The API sends the raw value; the human readers translate it (D62, D65).
func TestAnInternalMovementIsMarkedInTheResponse(t *testing.T) {
	at := time.Date(2026, 5, 31, 6, 0, 0, 0, time.UTC)
	money, err := domain.NewMoney(70771, domain.Currency("MXN"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: "tx-int", Money: money, Counterparty: domain.CounterpartySelf, Direction: domain.Inflow,
		FinancialStatus: domain.StatusUnknown, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: at, EvidenceIDs: []string{"ev-1"}, CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &stubTransactionReader{page: app.TransactionPage{Transactions: []app.ListedTransaction{
		{Transaction: tx, CounterpartyConfidence: domain.High},
	}}}
	w := request(t, transactionHandler(t, reader), http.MethodGet, "/v1/transactions", "Bearer "+testToken)
	got := decode[transactionListResponse](t, w)
	if len(got.Data) != 1 {
		t.Fatalf("response = %+v", got)
	}
	item := got.Data[0]
	if item.Counterparty == nil || item.Counterparty.Value != domain.CounterpartySelf || item.Counterparty.Confidence != "HIGH" {
		t.Errorf("counterparty = %+v", item.Counterparty)
	}
	if !item.Internal {
		t.Error("internal = false, want true")
	}
	if item.Merchant != nil {
		t.Errorf("an internal movement has a merchant: %+v", item.Merchant)
	}
}

// A SPEI between two of the user's accounts is internal on the wire (D75),
// even when the counterparty is still the printed name.
func TestAnOwnAccountTransferIsMarkedInternal(t *testing.T) {
	at := time.Date(2026, 7, 31, 19, 35, 0, 0, time.UTC)
	tx := apiTransaction(t, "tx-xfer", at)
	reader := &stubTransactionReader{page: app.TransactionPage{Transactions: []app.ListedTransaction{
		{Transaction: tx, OwnAccountTransfer: true},
	}}}
	w := request(t, transactionHandler(t, reader), http.MethodGet, "/v1/transactions", "Bearer "+testToken)
	got := decode[transactionListResponse](t, w)
	if len(got.Data) != 1 {
		t.Fatalf("response = %+v", got)
	}
	if !got.Data[0].Internal {
		t.Error("internal = false, want true for an own-account transfer")
	}
}

func TestListTransactionsParsesFiltersAndCursor(t *testing.T) {
	reader := &stubTransactionReader{}
	cursor := encodeTransactionCursor(app.TransactionCursor{OccurredAt: observed, ID: "tx-2"})
	path := "/v1/transactions?from=2026-08-01T00:00:00Z&to=2026-09-01T00:00:00Z" +
		"&direction=INFLOW&direction=OUTFLOW&financial_status=SETTLED" +
		"&reconciliation_state=UNRECONCILED&currency=MXN&limit=25&cursor=" + cursor
	w := request(t, transactionHandler(t, reader), http.MethodGet, path, "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	q := reader.query
	if q.Limit != 25 || len(q.Directions) != 2 || len(q.FinancialStatuses) != 1 || q.After.ID != "tx-2" {
		t.Errorf("query = %+v", q)
	}
}

func TestListTransactionsRejectsInvalidParameters(t *testing.T) {
	handler := transactionHandler(t, &stubTransactionReader{})
	for _, path := range []string{
		"/v1/transactions?limit=0",
		"/v1/transactions?from=yesterday",
		"/v1/transactions?direction=SIDEWAYS",
		"/v1/transactions?cursor=not-a-cursor",
	} {
		w := request(t, handler, http.MethodGet, path, "Bearer "+testToken)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

func TestListTransactionsReportsStorageFailure(t *testing.T) {
	handler := transactionHandler(t, &stubTransactionReader{err: errors.New("database is locked")})
	w := request(t, handler, http.MethodGet, "/v1/transactions", "Bearer "+testToken)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// The summary endpoint returns income and spending with the internal movements
// excluded and counted (D55).
func TestTransactionSummaryReturnsTotals(t *testing.T) {
	reader := &stubTransactionReader{totals: app.TransactionTotals{
		Currency: domain.Currency("MXN"), IncomeMinor: 120000, ExpenseMinor: 150000,
		IncomeCount: 3, ExpenseCount: 5, ExcludedInternal: 4,
	}}
	w := request(t, transactionHandler(t, reader), http.MethodGet,
		"/v1/transactions/summary?from=2026-08-01T00:00:00Z", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	got := decode[transactionTotalsResponse](t, w)
	if got.Currency != "MXN" || got.Income.AmountMinor != 120000 || got.Income.Count != 3 {
		t.Errorf("income = %+v", got.Income)
	}
	if got.Expense.AmountMinor != 150000 || got.NetMinor != -30000 || got.ExcludedInternal != 4 {
		t.Errorf("totals = %+v", got)
	}
	if reader.query.From == nil {
		t.Error("the from filter did not reach the reader")
	}
}
