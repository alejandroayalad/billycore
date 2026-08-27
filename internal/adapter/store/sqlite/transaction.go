package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// stageReconciled is where reconciliation leaves Evidence (ARCHITECTURE.md §5).
// It is the end of the pipeline: RECEIVED → EXTRACTED → RECONCILED.
const stageReconciled = "RECONCILED"

// eventTransactionCreated is the event name from DOMAIN.md §8.
const eventTransactionCreated = "TransactionCreated"

// TransactionRepository stores Transactions.
type TransactionRepository struct {
	db *sql.DB
}

func NewTransactionRepository(db *sql.DB) *TransactionRepository {
	return &TransactionRepository{db: db}
}

// Save writes a Transaction, its provenance, its TransactionCreated event, the
// slot that makes it idempotent, and the Evidence stage advance — all in one
// transaction — and reports whether a Transaction was created.
//
// One commit, for the reason ClaimRepository.Save gives and D25 argued first: an
// event describing a change that did not commit is a lie about the domain, and
// Evidence advanced to RECONCILED whose Transaction rolled back is a worse one —
// an artifact the pipeline will never look at again, with nothing to show for
// it, and this time the thing missing is a row of money.
//
// **A false with a nil error means this Claim already has a Transaction**, and
// this one was not written. Same contract as EvidenceRepository.Insert and
// ClaimRepository.Save, from the same place: `claim_transaction`'s primary key
// is what makes re-running reconciliation idempotent, not a check this function
// remembered to perform (D41, migration 004).
//
// `now` is passed in rather than read from the clock, so the caller owns time.
func (r *TransactionRepository) Save(ctx context.Context, t domain.Transaction, sourceClaimID string, now time.Time) (bool, error) {
	if sourceClaimID == "" {
		// Without it there is no slot to claim, and without a slot there is no
		// idempotency — a second pass would write a second row of money.
		return false, fmt.Errorf("save transaction %s: the originating claim is required", t.ID())
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("save transaction: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Money's two halves are written together or not at all. The domain has
	// already guaranteed the pair; nullableAmount is what keeps "no amount"
	// from arriving as a zero that reads like one.
	var amountMinor, currency any
	if money, ok := t.Money(); ok {
		amountMinor, currency = money.Minor(), string(money.Currency())
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transactions (
			id, amount_minor, currency, merchant, account_identifier,
			direction, financial_status, reconciliation_state,
			occurred_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID(), amountMinor, currency,
		nullableText(t.Merchant()), nullableText(t.AccountIdentifier()),
		string(t.Direction()), string(t.FinancialStatus()), string(t.ReconciliationState()),
		formatTime(t.OccurredAt()), formatTime(t.CreatedAt()), formatTime(t.UpdatedAt()),
	); err != nil {
		// The id is safe to log; the merchant is a person's name and the amount
		// is someone's money (SECURITY.md §10).
		return false, fmt.Errorf("save transaction %s: %w", t.ID(), err)
	}

	// Provenance first among the children: a Transaction without it is invalid
	// (DOMAIN.md §4), and the foreign key to evidence is what makes that
	// unfakeable rather than merely asserted.
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

	// The constraint. Claiming the slot is what makes this Save the one that
	// counts, and losing the race is an ordinary outcome rather than an error:
	// the Claim already has a Transaction, and a second identical one is not
	// additional knowledge — it is the same money counted twice.
	//
	// ON CONFLICT DO NOTHING rather than catching a violation, so idempotency
	// never depends on matching the text of a driver's error message.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO claim_transaction (claim_id, transaction_id)
		VALUES (?, ?)
		ON CONFLICT (claim_id) DO NOTHING`,
		sourceClaimID, t.ID(),
	)
	if err != nil {
		return false, fmt.Errorf("save transaction %s: claim the slot for claim %s: %w", t.ID(), sourceClaimID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("save transaction %s: rows affected: %w", t.ID(), err)
	}
	if affected == 0 {
		// Another pass got there first. Nothing written here survives: the
		// deferred Rollback discards the Transaction, its provenance and its
		// event together, so there is no half-recorded financial event and no
		// event describing one.
		return false, nil
	}

	// The stage advance, guarded on the row still being at EXTRACTED so that a
	// row already advanced by another pass is not silently re-advanced. The
	// lock is dropped in the same statement, because work that committed is
	// work nobody needs to retry, and last_error is cleared with it — a row
	// that failed on Monday and succeeded on Tuesday must not keep Monday's
	// diagnostic beside a perfectly good Transaction.
	for _, evidenceID := range t.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			UPDATE evidence
			SET processing_stage = ?, locked_until = NULL, last_error = NULL
			WHERE id = ? AND processing_stage = ?`,
			stageReconciled, evidenceID, stageExtracted,
		); err != nil {
			return false, fmt.Errorf("save transaction %s: advance evidence %s: %w", t.ID(), evidenceID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("save transaction %s: commit: %w", t.ID(), err)
	}
	return true, nil
}

// appendTransactionCreated writes the event described in DOMAIN.md §8: Billy
// now believes a financial event exists.
//
// The payload carries ids, not amounts. It is a fact about a Transaction, not a
// second copy of one — a consumer that wants the money reads the Transaction,
// and the event log does not become a place someone's finances leak from
// (SECURITY.md §10).
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
