package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// eventTransactionReconciled is the event name from DOMAIN.md §8.
const eventTransactionReconciled = "TransactionReconciled"

// ReconciliationRepository reads the Transactions a match pass compares and
// records the candidate it decides (DATA_MODEL.md §9, D69).
type ReconciliationRepository struct {
	db *sql.DB
}

func NewReconciliationRepository(db *sql.DB) *ReconciliationRepository {
	return &ReconciliationRepository{db: db}
}

// UnreconciledWithTrackingKey returns each ACTIVE UNRECONCILED Transaction that
// carries a tracking key, reached through its Claim (D36, D66). A tracking key
// lives in claim_fields, so this joins claim_transaction back to it. One row for
// each Transaction, even where more than one Claim states the key.
func (r *ReconciliationRepository) UnreconciledWithTrackingKey(ctx context.Context) ([]app.TrackingKeyed, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			t.id, t.amount_minor, t.currency, t.merchant, t.counterparty,
			t.account_identifier, t.direction, t.financial_status,
			t.reconciliation_state, t.transaction_state,
			t.occurred_at, t.created_at,
			cf.value_text,
			(SELECT json_group_array(te.evidence_id) FROM transaction_evidence te
			 WHERE te.transaction_id = t.id)
		FROM transactions t
		JOIN claim_transaction ct ON ct.transaction_id = t.id
		JOIN claim_fields cf ON cf.claim_id = ct.claim_id AND cf.field_name = ?
		WHERE t.transaction_state = ? AND t.reconciliation_state = ?
		ORDER BY t.id`,
		string(domain.FieldTrackingKey), string(domain.TransactionActive), string(domain.Unreconciled),
	)
	if err != nil {
		return nil, fmt.Errorf("tracking-keyed transactions: %w", err)
	}
	defer rows.Close()

	// Dedupe by id: a Transaction with two tracking-key Claims would arrive
	// twice, and a pair of one Transaction with itself is not a pair.
	seen := map[string]bool{}
	var out []app.TrackingKeyed
	for rows.Next() {
		tx, key, err := scanTrackingKeyed(rows)
		if err != nil {
			return nil, err
		}
		if seen[tx.ID()] {
			continue
		}
		seen[tx.ID()] = true
		out = append(out, app.TrackingKeyed{Transaction: tx, TrackingKey: key})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tracking-keyed transactions: %w", err)
	}
	return out, nil
}

func scanTrackingKeyed(rows *sql.Rows) (domain.Transaction, string, error) {
	var id, direction, status, reconciliation, state, occurredText, createdText string
	var key, evidenceJSON string
	var amount sql.NullInt64
	var currency, merchant, counterparty, account sql.NullString
	if err := rows.Scan(&id, &amount, &currency, &merchant, &counterparty, &account,
		&direction, &status, &reconciliation, &state, &occurredText, &createdText,
		&key, &evidenceJSON); err != nil {
		return domain.Transaction{}, "", fmt.Errorf("tracking-keyed: scan: %w", err)
	}
	tx, err := reconstructTransaction(id, amount, currency, merchant, counterparty, account,
		direction, status, reconciliation, state, occurredText, createdText, evidenceJSON)
	if err != nil {
		return domain.Transaction{}, "", err
	}
	return tx, key, nil
}

// UnreconciledTransactions returns every ACTIVE UNRECONCILED Transaction with its
// Source, so the weak sweep blocks them by amount and applies the composite key
// across Sources (D71, D72). The tracking-key pass runs first, so a Transaction
// that already merged is SUPERSEDED or RECONCILED and does not appear here. A
// pre-merge Transaction has one Evidence, so one Source; MIN keeps it stable.
func (r *ReconciliationRepository) UnreconciledTransactions(ctx context.Context) ([]app.SourcedTransaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			t.id, t.amount_minor, t.currency, t.merchant, t.counterparty,
			t.account_identifier, t.direction, t.financial_status,
			t.reconciliation_state, t.transaction_state,
			t.occurred_at, t.created_at,
			(SELECT json_group_array(te.evidence_id) FROM transaction_evidence te
			 WHERE te.transaction_id = t.id),
			(SELECT MIN(e.source_id) FROM transaction_evidence te
			 JOIN evidence e ON e.id = te.evidence_id
			 WHERE te.transaction_id = t.id)
		FROM transactions t
		WHERE t.transaction_state = ? AND t.reconciliation_state = ?
		ORDER BY t.id`,
		string(domain.TransactionActive), string(domain.Unreconciled),
	)
	if err != nil {
		return nil, fmt.Errorf("unreconciled transactions: %w", err)
	}
	defer rows.Close()

	var out []app.SourcedTransaction
	for rows.Next() {
		var id, direction, status, reconciliation, state, occurredText, createdText string
		var evidenceJSON string
		var amount sql.NullInt64
		var currency, merchant, counterparty, account, sourceID sql.NullString
		if err := rows.Scan(&id, &amount, &currency, &merchant, &counterparty, &account,
			&direction, &status, &reconciliation, &state, &occurredText, &createdText,
			&evidenceJSON, &sourceID); err != nil {
			return nil, fmt.Errorf("unreconciled transactions: scan: %w", err)
		}
		tx, err := reconstructTransaction(id, amount, currency, merchant, counterparty, account,
			direction, status, reconciliation, state, occurredText, createdText, evidenceJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, app.SourcedTransaction{Transaction: tx, SourceID: sourceID.String})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unreconciled transactions: %w", err)
	}
	return out, nil
}

// reconstructTransaction rebuilds one Transaction from its stored columns. The
// domain validates the fields, so a bad row is an error and not a silent zero.
func reconstructTransaction(id string, amount sql.NullInt64, currency, merchant, counterparty,
	account sql.NullString, direction, status, reconciliation, state, occurredText, createdText,
	evidenceJSON string) (domain.Transaction, error) {
	var money domain.Money
	var err error
	if amount.Valid != currency.Valid {
		return domain.Transaction{}, fmt.Errorf("transaction %s: incomplete money", id)
	}
	if amount.Valid {
		money, err = domain.NewMoney(amount.Int64, domain.Currency(currency.String))
		if err != nil {
			return domain.Transaction{}, fmt.Errorf("transaction %s: %w", id, err)
		}
	}
	occurredAt, err := time.Parse(timeLayout, occurredText)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("transaction %s: occurred_at: %w", id, err)
	}
	createdAt, err := time.Parse(timeLayout, createdText)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("transaction %s: created_at: %w", id, err)
	}
	var evidenceIDs []string
	if err := json.Unmarshal([]byte(evidenceJSON), &evidenceIDs); err != nil {
		return domain.Transaction{}, fmt.Errorf("transaction %s: provenance: %w", id, err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Merchant: merchant.String, Counterparty: counterparty.String,
		AccountIdentifier: account.String,
		Direction:         domain.TransactionDirection(direction), FinancialStatus: domain.FinancialStatus(status),
		ReconciliationState: domain.ReconciliationState(reconciliation), State: domain.TransactionState(state),
		OccurredAt: occurredAt, EvidenceIDs: evidenceIDs, CreatedAt: createdAt,
	})
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("transaction %s: %w", id, err)
	}
	return tx, nil
}

// Reconcile records one candidate and, on a MATCH, merges the pair (D70). The
// candidate's UNIQUE pair is the idempotency: a re-run inserts nothing, so it
// merges nothing. It reports whether it wrote a new candidate.
func (r *ReconciliationRepository) Reconcile(ctx context.Context, d app.ReconcileDecision, now time.Time) (bool, error) {
	if err := d.Outcome.Validate(); err != nil {
		return false, fmt.Errorf("reconcile: %w", err)
	}
	if d.LeftID == "" || d.RightID == "" || d.LeftID >= d.RightID {
		return false, fmt.Errorf("reconcile: candidate pair is not canonical: %q, %q", d.LeftID, d.RightID)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("reconcile: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// The candidate is the slot. ON CONFLICT keeps idempotency independent of a
	// driver's error text, the way claim_transaction does in 004.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO reconciliation_candidate (id, left_ref, right_ref, status, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (left_ref, right_ref) DO NOTHING`,
		d.CandidateID, d.LeftID, d.RightID, string(d.Outcome), formatTime(now),
	)
	if err != nil {
		return false, fmt.Errorf("reconcile: record candidate: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reconcile: rows affected: %w", err)
	}
	if affected == 0 {
		return false, nil
	}

	if d.Outcome == domain.Match {
		if err := merge(ctx, tx, d, now); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("reconcile: commit: %w", err)
	}
	return true, nil
}

// merge retires the loser into the survivor and unions the provenance (D70). The
// ACTIVE guard on each UPDATE is a second lock behind the candidate's UNIQUE
// pair, so a superseded row is never retired twice.
func merge(ctx context.Context, tx *sql.Tx, d app.ReconcileDecision, now time.Time) error {
	survivor, superseded := d.Survivor, d.Superseded

	if _, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET reconciliation_state = ?, updated_at = ?
		WHERE id = ? AND transaction_state = ?`,
		string(survivor.ReconciliationState()), formatTime(survivor.UpdatedAt()),
		survivor.ID(), string(domain.TransactionActive),
	); err != nil {
		return fmt.Errorf("reconcile: mark survivor %s: %w", survivor.ID(), err)
	}

	// Union the loser's Evidence onto the survivor. The primary key ignores the
	// ids the survivor already holds, so only the other Source's is added, and
	// the two-source fact is kept (D70).
	for _, evidenceID := range survivor.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transaction_evidence (transaction_id, evidence_id)
			VALUES (?, ?) ON CONFLICT DO NOTHING`,
			survivor.ID(), evidenceID,
		); err != nil {
			return fmt.Errorf("reconcile: union provenance onto %s: %w", survivor.ID(), err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET transaction_state = ?, superseded_by_transaction_id = ?,
			reconciliation_state = ?, updated_at = ?
		WHERE id = ? AND transaction_state = ?`,
		string(domain.TransactionSuperseded), survivor.ID(),
		string(superseded.ReconciliationState()), formatTime(superseded.UpdatedAt()),
		superseded.ID(), string(domain.TransactionActive),
	); err != nil {
		return fmt.Errorf("reconcile: supersede %s: %w", superseded.ID(), err)
	}

	return appendTransactionReconciled(ctx, tx, survivor.ID(), superseded.ID(), d.Basis, now)
}

// appendTransactionReconciled writes the event in DOMAIN.md §8. The payload
// holds ids and the basis, and no amounts: the event log does not hold the
// finances of a person (SECURITY.md §10). An empty basis is the tracking key,
// the first signal a merge had (D70); the composite key names itself (D72).
func appendTransactionReconciled(ctx context.Context, tx *sql.Tx, survivorID, supersededID, basis string, now time.Time) error {
	if basis == "" {
		basis = string(domain.FieldTrackingKey)
	}
	payload, err := json.Marshal(struct {
		SurvivingTransactionID  string `json:"survivingTransactionId"`
		SupersededTransactionID string `json:"supersededTransactionId"`
		Basis                   string `json:"basis"`
	}{
		SurvivingTransactionID:  survivorID,
		SupersededTransactionID: supersededID,
		Basis:                   basis,
	})
	if err != nil {
		return fmt.Errorf("%s: marshal payload: %w", eventTransactionReconciled, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO domain_event (type, payload, occurred_at) VALUES (?, ?, ?)`,
		eventTransactionReconciled, string(payload), formatTime(now),
	); err != nil {
		return fmt.Errorf("%s: %w", eventTransactionReconciled, err)
	}
	return nil
}
