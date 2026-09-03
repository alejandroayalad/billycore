package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// stageReconciled is where reconciliation leaves Evidence (ARCHITECTURE.md §5).
// It is the end of the pipeline: RECEIVED → EXTRACTED → RECONCILED.
const stageReconciled = "RECONCILED"

// eventTransactionCreated is the event name from DOMAIN.md §8.
const eventTransactionCreated = "TransactionCreated"

// TransactionRepository stores Transactions.
type TransactionRepository struct {
	db        *sql.DB
	household app.Household
}

func NewTransactionRepository(db *sql.DB) *TransactionRepository {
	return &TransactionRepository{db: db}
}

// WithHousehold sets the owned accounts that List and Totals use (D78).
func (r *TransactionRepository) WithHousehold(h app.Household) *TransactionRepository {
	r.household = h
	return r
}

// Save writes each Transaction of one artifact in one database transaction: the
// rows, their provenance, one event for each, the slot for each Claim, and the
// stage advance. The set is the unit, because the stage advance is in this
// commit. A false with a nil error means that one Claim already has a
// Transaction, and claim_transaction makes that idempotent (D41).
func (r *TransactionRepository) Save(ctx context.Context, built []app.BuiltTransaction, now time.Time) (bool, error) {
	if len(built) == 0 {
		// An artifact with no Claims does not reach this code, because
		// MarkReconciled advances it (D44). An empty set would commit a stage
		// advance for work that does not exist.
		return false, errors.New("save transactions: nothing to save")
	}
	for _, b := range built {
		if b.SourceClaimID == "" {
			// Without the Claim id there is no slot, and without a slot there
			// is no idempotency. A second pass would write a second row of
			// money.
			return false, fmt.Errorf("save transaction %s: the originating claim is required", b.Transaction.ID())
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("save transactions: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	for _, b := range built {
		took, err := insertTransaction(ctx, tx, b, now)
		if err != nil {
			return false, err
		}
		if !took {
			// Another pass wrote first. The deferred Rollback discards each
			// Transaction, its provenance and its event.
			return false, nil
		}
	}

	// The stage advance. The guard on EXTRACTED prevents a second advance, and
	// the same statement drops the lock and clears last_error. This code reads
	// the artifacts from the Transactions: a parameter would let a caller
	// advance a row that this commit did not write about.
	for _, evidenceID := range evidenceOf(built) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE evidence
			SET processing_stage = ?, locked_until = NULL, last_error = NULL
			WHERE id = ? AND processing_stage = ?`,
			stageReconciled, evidenceID, stageExtracted,
		); err != nil {
			return false, fmt.Errorf("save transactions: advance evidence %s: %w", evidenceID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("save transactions: commit: %w", err)
	}
	return true, nil
}

// insertTransaction writes one Transaction, its provenance and its event. It
// takes the slot that prevents a second write, and reports if it took it.
func insertTransaction(ctx context.Context, tx *sql.Tx, b app.BuiltTransaction, now time.Time) (bool, error) {
	t := b.Transaction

	// Write both parts of Money, or neither part. The domain guarantees the
	// pair. A nil pair keeps "no amount" different from an amount of zero.
	var amountMinor, currency any
	if money, ok := t.Money(); ok {
		amountMinor, currency = money.Minor(), string(money.Currency())
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transactions (
			id, amount_minor, currency, merchant, counterparty, account_identifier,
			direction, financial_status, reconciliation_state, transaction_state,
			occurred_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID(), amountMinor, currency,
		nullableText(t.Merchant()), nullableText(t.Counterparty()),
		nullableText(t.AccountIdentifier()),
		string(t.Direction()), string(t.FinancialStatus()), string(t.ReconciliationState()),
		string(t.State()),
		formatTime(t.OccurredAt()), formatTime(t.CreatedAt()), formatTime(t.UpdatedAt()),
	); err != nil {
		// The id is safe to log. The merchant can be the name of a person, and
		// the amount is the money of a person (SECURITY.md §10).
		return false, fmt.Errorf("save transaction %s: %w", t.ID(), err)
	}

	// Write the provenance first. A Transaction without provenance is invalid
	// (DOMAIN.md §4), and the foreign key to evidence enforces that.
	for _, evidenceID := range t.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transaction_evidence (transaction_id, evidence_id) VALUES (?, ?)`,
			t.ID(), evidenceID,
		); err != nil {
			return false, fmt.Errorf("save transaction %s: provenance: %w", t.ID(), err)
		}
	}

	if err := appendTransactionCreated(ctx, tx, t, now); err != nil {
		return false, err
	}

	// The constraint. The pass that takes the slot is the pass that counts, and
	// to lose is an ordinary result: a second Transaction for one Claim is the
	// same money two times. ON CONFLICT DO NOTHING keeps idempotency
	// independent of the text of a driver error message.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO claim_transaction (claim_id, transaction_id)
		VALUES (?, ?)
		ON CONFLICT (claim_id) DO NOTHING`,
		b.SourceClaimID, t.ID(),
	)
	if err != nil {
		return false, fmt.Errorf("save transaction %s: claim the slot for claim %s: %w", t.ID(), b.SourceClaimID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("save transaction %s: rows affected: %w", t.ID(), err)
	}
	return affected == 1, nil
}

// evidenceOf returns the artifacts that a set of Transactions rests on. It
// removes duplicates and keeps a stable order.
func evidenceOf(built []app.BuiltTransaction) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range built {
		for _, evidenceID := range b.Transaction.EvidenceIDs() {
			if seen[evidenceID] {
				continue
			}
			seen[evidenceID] = true
			out = append(out, evidenceID)
		}
	}
	return out
}

// appendTransactionCreated writes the event in DOMAIN.md §8. The payload holds
// ids and no amounts, so the event log does not hold the finances of a person
// (SECURITY.md §10). A reader that needs the money reads the Transaction.
func appendTransactionCreated(ctx context.Context, tx *sql.Tx, t domain.Transaction, now time.Time) error {
	payload, err := json.Marshal(struct {
		TransactionID string   `json:"transactionId"`
		EvidenceIDs   []string `json:"evidenceIds"`
	}{
		TransactionID: t.ID(),
		EvidenceIDs:   t.EvidenceIDs(),
	})
	if err != nil {
		return fmt.Errorf("%s: marshal payload: %w", eventTransactionCreated, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO domain_event (type, payload, occurred_at) VALUES (?, ?, ?)`,
		eventTransactionCreated, string(payload), formatTime(now),
	); err != nil {
		return fmt.Errorf("%s: %w", eventTransactionCreated, err)
	}
	return nil
}
