package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// List returns ACTIVE Transactions in the order required by API.md section 7.
func (r *TransactionRepository) List(ctx context.Context, q app.TransactionQuery) (app.TransactionPage, error) {
	query, args := transactionListSQL(q)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return app.TransactionPage{}, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	items := make([]app.ListedTransaction, 0, q.Limit+1)
	for rows.Next() {
		item, err := scanListedTransaction(rows)
		if err != nil {
			return app.TransactionPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.TransactionPage{}, fmt.Errorf("list transactions: %w", err)
	}

	page := app.TransactionPage{Transactions: items}
	if len(items) > q.Limit {
		page.HasMore = true
		page.Transactions = items[:q.Limit]
	}
	return page, nil
}

func transactionListSQL(q app.TransactionQuery) (string, []any) {
	var where []string
	args := []any{string(domain.TransactionActive)}
	where = append(where, "t.transaction_state = ?")
	if q.From != nil {
		where, args = append(where, "t.occurred_at >= ?"), append(args, formatTime(*q.From))
	}
	if q.To != nil {
		where, args = append(where, "t.occurred_at < ?"), append(args, formatTime(*q.To))
	}
	where, args = appendSet(where, args, "t.direction", stringsOf(q.Directions))
	where, args = appendSet(where, args, "t.financial_status", stringsOf(q.FinancialStatuses))
	where, args = appendSet(where, args, "t.reconciliation_state", stringsOf(q.ReconciliationStates))
	if q.Currency != "" {
		where, args = append(where, "t.currency = ?"), append(args, string(q.Currency))
	}
	if q.After != nil {
		where = append(where, "(t.occurred_at < ? OR (t.occurred_at = ? AND t.id < ?))")
		at := formatTime(q.After.OccurredAt)
		args = append(args, at, at, q.After.ID)
	}

	args = append(args, q.Limit+1)
	return `SELECT
		t.id, t.amount_minor, t.currency, t.merchant, t.counterparty,
		t.account_identifier,
		t.direction, t.financial_status, t.reconciliation_state,
		t.transaction_state, t.occurred_at, t.created_at,
		(SELECT cf.confidence FROM claim_transaction ct
		 JOIN claim_fields cf ON cf.claim_id = ct.claim_id
		 WHERE ct.transaction_id = t.id AND cf.field_name = 'merchant'
		 ORDER BY ct.claim_id LIMIT 1),
		(SELECT cf.confidence FROM claim_transaction ct
		 JOIN claim_fields cf ON cf.claim_id = ct.claim_id
		 WHERE ct.transaction_id = t.id AND cf.field_name = 'counterparty'
		 ORDER BY ct.claim_id LIMIT 1),
		(SELECT cf.confidence FROM claim_transaction ct
		 JOIN claim_fields cf ON cf.claim_id = ct.claim_id
		 WHERE ct.transaction_id = t.id AND cf.field_name = 'account_identifier'
		 ORDER BY ct.claim_id LIMIT 1),
		(SELECT json_group_array(te.evidence_id) FROM transaction_evidence te
		 WHERE te.transaction_id = t.id)
	FROM transactions t
	WHERE ` + strings.Join(where, " AND ") + `
	ORDER BY t.occurred_at DESC, t.id DESC
	LIMIT ?`, args
}

func appendSet(where []string, args []any, column string, values []string) ([]string, []any) {
	if len(values) == 0 {
		return where, args
	}
	marks := make([]string, len(values))
	for i, value := range values {
		marks[i], args = "?", append(args, value)
	}
	return append(where, column+" IN ("+strings.Join(marks, ",")+")"), args
}

func stringsOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func scanListedTransaction(rows *sql.Rows) (app.ListedTransaction, error) {
	var id, direction, status, reconciliation, state, occurredText, createdText, evidenceJSON string
	var amount sql.NullInt64
	var currency, merchant, counterparty, account sql.NullString
	var merchantConfidence, counterpartyConfidence, accountConfidence sql.NullString
	if err := rows.Scan(&id, &amount, &currency, &merchant, &counterparty, &account,
		&direction, &status, &reconciliation, &state, &occurredText, &createdText,
		&merchantConfidence, &counterpartyConfidence, &accountConfidence,
		&evidenceJSON); err != nil {
		return app.ListedTransaction{}, fmt.Errorf("list transactions: scan: %w", err)
	}

	var money domain.Money
	var err error
	if amount.Valid != currency.Valid {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: incomplete money", id)
	}
	if amount.Valid {
		money, err = domain.NewMoney(amount.Int64, domain.Currency(currency.String))
		if err != nil {
			return app.ListedTransaction{}, fmt.Errorf("list transaction %s: %w", id, err)
		}
	}
	occurredAt, err := time.Parse(timeLayout, occurredText)
	if err != nil {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: occurred_at: %w", id, err)
	}
	createdAt, err := time.Parse(timeLayout, createdText)
	if err != nil {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: created_at: %w", id, err)
	}
	var evidenceIDs []string
	if err := json.Unmarshal([]byte(evidenceJSON), &evidenceIDs); err != nil {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: provenance: %w", id, err)
	}
	if merchant.Valid != merchantConfidence.Valid || account.Valid != accountConfidence.Valid ||
		counterparty.Valid != counterpartyConfidence.Valid {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: a supported field has no confidence", id)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Merchant: merchant.String, Counterparty: counterparty.String,
		AccountIdentifier: account.String,
		Direction:         domain.TransactionDirection(direction), FinancialStatus: domain.FinancialStatus(status),
		ReconciliationState: domain.ReconciliationState(reconciliation), State: domain.TransactionState(state),
		OccurredAt: occurredAt, EvidenceIDs: evidenceIDs, CreatedAt: createdAt,
	})
	if err != nil {
		return app.ListedTransaction{}, fmt.Errorf("list transaction %s: %w", id, err)
	}
	item := app.ListedTransaction{Transaction: tx}
	if merchantConfidence.Valid {
		item.MerchantConfidence = domain.Confidence(merchantConfidence.String)
		if err := item.MerchantConfidence.Validate(); err != nil {
			return app.ListedTransaction{}, fmt.Errorf("list transaction %s: merchant confidence: %w", id, err)
		}
	}
	if counterpartyConfidence.Valid {
		item.CounterpartyConfidence = domain.Confidence(counterpartyConfidence.String)
		if err := item.CounterpartyConfidence.Validate(); err != nil {
			return app.ListedTransaction{}, fmt.Errorf("list transaction %s: counterparty confidence: %w", id, err)
		}
	}
	if accountConfidence.Valid {
		item.AccountConfidence = domain.Confidence(accountConfidence.String)
		if err := item.AccountConfidence.Validate(); err != nil {
			return app.ListedTransaction{}, fmt.Errorf("list transaction %s: account confidence: %w", id, err)
		}
	}
	return item, nil
}

// Totals sums income and spending over the Transactions the query selects. An
// internal movement is the user's own money and counts as neither (D55); the
// exclusion uses the reserved counterparty value, passed as a parameter so the
// domain owns it and no literal lives in this SQL (D65).
func (r *TransactionRepository) Totals(ctx context.Context, q app.TransactionQuery) (app.TransactionTotals, error) {
	// Totals are single-currency: mixing minor units of two currencies is
	// meaningless. BillyCore supports one currency today (D50), so the default
	// is MXN where the query names none.
	currency := q.Currency
	if currency == "" {
		currency = domain.Currency("MXN")
	}
	totals := app.TransactionTotals{Currency: currency}

	where := []string{"t.transaction_state = ?", "t.currency = ?", "t.amount_minor IS NOT NULL"}
	args := []any{string(domain.TransactionActive), string(currency)}
	if q.From != nil {
		where, args = append(where, "t.occurred_at >= ?"), append(args, formatTime(*q.From))
	}
	if q.To != nil {
		where, args = append(where, "t.occurred_at < ?"), append(args, formatTime(*q.To))
	}

	// Income and spending, excluding the internal movement.
	external := append([]string{"(t.counterparty IS NULL OR t.counterparty <> ?)"}, where...)
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.direction, COALESCE(SUM(t.amount_minor), 0), COUNT(*)
		FROM transactions t
		WHERE `+strings.Join(external, " AND ")+`
		GROUP BY t.direction`,
		append([]any{domain.CounterpartySelf}, args...)...)
	if err != nil {
		return app.TransactionTotals{}, fmt.Errorf("transaction totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var direction string
		var sum int64
		var count int
		if err := rows.Scan(&direction, &sum, &count); err != nil {
			return app.TransactionTotals{}, fmt.Errorf("transaction totals: scan: %w", err)
		}
		switch domain.TransactionDirection(direction) {
		case domain.Inflow:
			totals.IncomeMinor, totals.IncomeCount = sum, count
		case domain.Outflow:
			totals.ExpenseMinor, totals.ExpenseCount = sum, count
		}
	}
	if err := rows.Err(); err != nil {
		return app.TransactionTotals{}, fmt.Errorf("transaction totals: %w", err)
	}

	// The internal movements that were left out, so the reader can say so.
	internal := append([]string{"t.counterparty = ?"}, where...)
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactions t WHERE `+strings.Join(internal, " AND "),
		append([]any{domain.CounterpartySelf}, args...)...,
	).Scan(&totals.ExcludedInternal); err != nil {
		return app.TransactionTotals{}, fmt.Errorf("transaction totals: internal: %w", err)
	}
	return totals, nil
}
