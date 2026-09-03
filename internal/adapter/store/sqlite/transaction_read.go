package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// List returns ACTIVE Transactions in the order required by API.md section 7.
func (r *TransactionRepository) List(ctx context.Context, q app.TransactionQuery) (app.TransactionPage, error) {
	query, args := r.transactionListSQL(q)
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

func (r *TransactionRepository) transactionListSQL(q app.TransactionQuery) (string, []any) {
	cte, args := r.ownAccountTransferSQL()
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

// ownAccountTransferSQL is the movement D75 and D78 leave out of earned and
// spent. Identity is a tracking key or an owned alias. Amount, currency and
// UTC day ±1 pick the pair. A collision is AMBIGUOUS and stays in the totals.
func (r *TransactionRepository) ownAccountTransferSQL() (string, []any) {
	if r.household.Empty() {
		return `WITH own_account_transfer AS (SELECT CAST(NULL AS TEXT) AS id WHERE 0) `, nil
	}

	sourceSQL, sourceArgs := sqlNamedValues("source_id", "account_id", sortedPairs(r.household.SourceAccount))
	aliasSQL, aliasArgs := sqlNamedValues("alias", "account_id", sortedPairs(r.household.AliasAccount))
	args := append(append([]any{}, sourceArgs...), aliasArgs...)
	args = append(args, string(domain.FieldTrackingKey), string(domain.TransactionActive),
		string(domain.Outflow), string(domain.Inflow))

	return `WITH source_account(source_id, account_id) AS (` + sourceSQL + `),
	alias_account(alias, account_id) AS (` + aliasSQL + `),
	movement AS (
		SELECT t.id, t.direction, t.amount_minor, t.currency, t.occurred_at,
			sa.account_id AS home,
			CASE
				WHEN ca.account_id IS NOT NULL AND ma.account_id IS NOT NULL
					AND ca.account_id <> ma.account_id THEN NULL
				ELSE COALESCE(ca.account_id, ma.account_id)
			END AS pointed,
			(SELECT CASE
				WHEN cf.value_text LIKE 'HSB%' AND cf.value_text NOT LIKE 'HSBC%'
				THEN 'HSBC' || substr(cf.value_text, 4)
				ELSE cf.value_text
			END
			FROM claim_transaction ct
			JOIN claim_fields cf ON cf.claim_id = ct.claim_id AND cf.field_name = ?
			WHERE ct.transaction_id = t.id
				AND cf.value_text IS NOT NULL AND length(cf.value_text) > 0
			LIMIT 1) AS key
		FROM transactions t
		JOIN transaction_evidence te ON te.transaction_id = t.id
		JOIN evidence e ON e.id = te.evidence_id
		JOIN source_account sa ON sa.source_id = e.source_id
		LEFT JOIN alias_account ca ON ca.alias = t.counterparty
		LEFT JOIN alias_account ma ON ma.alias = t.merchant
		WHERE t.transaction_state = ?
	), candidate AS (
		SELECT DISTINCT o.id AS out_id, i.id AS in_id
		FROM movement o
		JOIN movement i
			ON o.direction = ?
			AND i.direction = ?
			AND o.home <> i.home
			AND o.amount_minor = i.amount_minor
			AND o.currency = i.currency
			AND abs(julianday(substr(o.occurred_at, 1, 10)) - julianday(substr(i.occurred_at, 1, 10))) <= 1
			AND (
				(o.key IS NOT NULL AND i.key IS NOT NULL AND o.key = i.key)
				OR o.pointed = i.home
				OR i.pointed = o.home
			)
	), unique_pair AS (
		SELECT c.out_id, c.in_id
		FROM candidate c
		WHERE (SELECT COUNT(*) FROM candidate x WHERE x.out_id = c.out_id) = 1
			AND (SELECT COUNT(*) FROM candidate x WHERE x.in_id = c.in_id) = 1
	), own_account_transfer AS (
		SELECT out_id AS id FROM unique_pair
		UNION
		SELECT in_id AS id FROM unique_pair
	) `, args
}

func sortedPairs(m map[string]string) [][2]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, m[k]})
	}
	return out
}

func sqlNamedValues(colA, colB string, rows [][2]string) (string, []any) {
	if len(rows) == 0 {
		return fmt.Sprintf("SELECT CAST(NULL AS TEXT) AS %s, CAST(NULL AS TEXT) AS %s WHERE 0", colA, colB), nil
	}
	var b strings.Builder
	args := make([]any, 0, len(rows)*2)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?)")
		args = append(args, row[0], row[1])
	}
	return "VALUES " + b.String(), args
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
// D78); Cajita uses the reserved counterparty, and an owned-account pair uses
// tracking key or alias first, then amount, currency and time.
func (r *TransactionRepository) Totals(ctx context.Context, q app.TransactionQuery) (app.TransactionTotals, error) {
	// Totals are single-currency: mixing minor units of two currencies is
	// meaningless. BillyCore supports one currency today (D50), so the default
	// is MXN where the query names none.
	currency := q.Currency
	if currency == "" {
		currency = domain.Currency("MXN")
	}
	totals := app.TransactionTotals{Currency: currency}

	cte, cteArgs := r.ownAccountTransferSQL()
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

	// Income and spending, excluding Cajita (D55) and own-account transfers (D75, D78).
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
