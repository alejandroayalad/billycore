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
	cte, args := ownAccountTransferSQL()
	var where []string
	where, args = append(where, "t.transaction_state = ?"), append(args, string(domain.TransactionActive))
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
	return cte + `SELECT
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
		 WHERE te.transaction_id = t.id),
		EXISTS (SELECT 1 FROM own_account_transfer x WHERE x.id = t.id)
	FROM transactions t
	WHERE ` + strings.Join(where, " AND ") + `
	ORDER BY t.occurred_at DESC, t.id DESC
	LIMIT ?`, args
}

// ownAccountTransferSQL is the movement D75 and D76 leave out of earned and
// spent. A keyed SPEI across two Sources is both sides (D75). An inflow whose
// counterparty is a name those keyed SPEI already proved is the user is also
// the user's own money (D76). The other ledger joins only when the amount and
// calendar day are unique; a collision stays unmatched (D59, D21).
func ownAccountTransferSQL() (string, []any) {
	return `WITH tracking_keyed AS (
		SELECT ct.transaction_id, e.source_id, t.direction,
			CASE
				WHEN cf.value_text LIKE 'HSB%' AND cf.value_text NOT LIKE 'HSBC%'
				THEN 'HSBC' || substr(cf.value_text, 4)
				ELSE cf.value_text
			END AS key
		FROM claim_transaction ct
		JOIN claim_fields cf ON cf.claim_id = ct.claim_id AND cf.field_name = ?
		JOIN transactions t ON t.id = ct.transaction_id AND t.transaction_state = ?
		JOIN transaction_evidence te ON te.transaction_id = t.id
		JOIN evidence e ON e.id = te.evidence_id
		WHERE cf.value_text IS NOT NULL AND length(cf.value_text) > 0
	), keyed_transfer AS (
		SELECT DISTINCT a.transaction_id AS id
		FROM tracking_keyed a
		JOIN tracking_keyed b
			ON a.key = b.key
			AND a.source_id <> b.source_id
			AND a.direction <> b.direction
	), self_name AS (
		SELECT DISTINCT t.counterparty AS name
		FROM keyed_transfer k
		JOIN transactions t ON t.id = k.id
		WHERE t.direction = ?
			AND t.counterparty IS NOT NULL
			AND t.counterparty <> ?
			AND length(t.counterparty) > 0
	), self_named_inflow AS (
		SELECT t.id, t.amount_minor, t.occurred_at
		FROM transactions t
		JOIN self_name s ON s.name = t.counterparty
		WHERE t.direction = ?
			AND t.transaction_state = ?
	), candidate AS (
		SELECT DISTINCT o.id AS out_id, i.id AS in_id
		FROM transactions o
		JOIN transaction_evidence teo ON teo.transaction_id = o.id
		JOIN evidence eo ON eo.id = teo.evidence_id
		JOIN self_named_inflow i ON i.amount_minor = o.amount_minor
		JOIN transaction_evidence tei ON tei.transaction_id = i.id
		JOIN evidence ei ON ei.id = tei.evidence_id
		WHERE o.direction = ?
			AND o.transaction_state = ?
			AND eo.source_id <> ei.source_id
			AND abs(julianday(substr(o.occurred_at, 1, 10)) - julianday(substr(i.occurred_at, 1, 10))) <= 1
	), unique_other_side AS (
		SELECT c.out_id AS id
		FROM candidate c
		WHERE (SELECT COUNT(*) FROM candidate x WHERE x.out_id = c.out_id) = 1
			AND (SELECT COUNT(*) FROM candidate x WHERE x.in_id = c.in_id) = 1
	), own_account_transfer AS (
		SELECT id FROM keyed_transfer
		UNION
		SELECT id FROM self_named_inflow
		UNION
		SELECT id FROM unique_other_side
	) `, []any{
			string(domain.FieldTrackingKey), string(domain.TransactionActive),
			string(domain.Inflow), domain.CounterpartySelf,
			string(domain.Inflow), string(domain.TransactionActive),
			string(domain.Outflow), string(domain.TransactionActive),
		}
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
	var ownAccountTransfer bool
	if err := rows.Scan(&id, &amount, &currency, &merchant, &counterparty, &account,
		&direction, &status, &reconciliation, &state, &occurredText, &createdText,
		&merchantConfidence, &counterpartyConfidence, &accountConfidence,
		&evidenceJSON, &ownAccountTransfer); err != nil {
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
	item := app.ListedTransaction{Transaction: tx, OwnAccountTransfer: ownAccountTransfer}
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
// internal movement is the user's own money and counts as neither (D55, D75,
// D76); Cajita uses the reserved counterparty, a keyed SPEI uses the tracking
// key, and a self-named inflow uses the name those SPEI already proved.
func (r *TransactionRepository) Totals(ctx context.Context, q app.TransactionQuery) (app.TransactionTotals, error) {
	// Totals are single-currency: mixing minor units of two currencies is
	// meaningless. BillyCore supports one currency today (D50), so the default
	// is MXN where the query names none.
	currency := q.Currency
	if currency == "" {
		currency = domain.Currency("MXN")
	}
	totals := app.TransactionTotals{Currency: currency}

	cte, cteArgs := ownAccountTransferSQL()
	where := []string{"t.transaction_state = ?", "t.currency = ?", "t.amount_minor IS NOT NULL"}
	filterArgs := []any{string(domain.TransactionActive), string(currency)}
	if q.From != nil {
		where, filterArgs = append(where, "t.occurred_at >= ?"), append(filterArgs, formatTime(*q.From))
	}
	if q.To != nil {
		where, filterArgs = append(where, "t.occurred_at < ?"), append(filterArgs, formatTime(*q.To))
	}

	bind := func(head any) []any {
		out := append(append([]any{}, cteArgs...), head)
		return append(out, filterArgs...)
	}

	// Income and spending, excluding Cajita (D55) and own-account SPEI (D75, D76).
	external := append([]string{
		"(t.counterparty IS NULL OR t.counterparty <> ?)",
		"t.id NOT IN (SELECT id FROM own_account_transfer)",
	}, where...)
	rows, err := r.db.QueryContext(ctx, cte+`
		SELECT t.direction, COALESCE(SUM(t.amount_minor), 0), COUNT(*)
		FROM transactions t
		WHERE `+strings.Join(external, " AND ")+`
		GROUP BY t.direction`,
		bind(domain.CounterpartySelf)...)
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

	// Cajita rows and own-account SPEI that were left out, so the reader can say so.
	internal := append([]string{"(t.counterparty = ? OR t.id IN (SELECT id FROM own_account_transfer))"}, where...)
	if err := r.db.QueryRowContext(ctx, cte+`
		SELECT COUNT(*) FROM transactions t WHERE `+strings.Join(internal, " AND "),
		bind(domain.CounterpartySelf)...,
	).Scan(&totals.ExcludedInternal); err != nil {
		return app.TransactionTotals{}, fmt.Errorf("transaction totals: internal: %w", err)
	}
	return totals, nil
}
