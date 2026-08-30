package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
	"github.com/alejandroayalad/billycore/internal/id"
)

// DefaultReconcileBatch and DefaultReconcileLease size one reconciliation pass.
//
// They are the numbers that extraction uses, for the same reasons. The batch is
// a lock bound, and the lease is sized for a stopped process.
const (
	DefaultReconcileBatch = 100
	DefaultReconcileLease = time.Minute
)

// classTransaction means that the fields of the Claim did not make a valid
// Transaction. The domain rejected Billy's own construction, so this is a
// defect in the mapping and not a bad artifact.
const classTransaction = "TRANSACTION"

// ReconcileResult is the summary of one reconciliation pass.
type ReconcileResult struct {
	EvidenceProcessed   int
	TransactionsCreated int

	// Claimless counts the artifacts that reached EXTRACTED with no Claim and
	// that will never carry a Transaction. The pass advances them and does not
	// fail them (D44).
	Claimless int

	// Skipped counts the artifacts whose Claims already had Transactions, so
	// this pass built none. It means that the work was already done.
	Skipped int

	// DatedFromEvidence counts the Transactions whose occurred_at came from the
	// fallback in DATA_MODEL.md §4.5. A fallback date is when Billy received
	// the artifact, not when the event happened. Expect 90 for the corpus.
	DatedFromEvidence int

	// Failed counts the artifacts that the pass attempted and that produced no
	// Transaction because of an error. A pass with failures still succeeds.
	Failed int

	CompletedAt time.Time
}

// Reconciler turns the Claims of an active interpretation into Transactions. It
// is the third stage of ARCHITECTURE.md §5 and advances a row from EXTRACTED to
// RECONCILED, with one Transaction for each Claim (D42). It parses nothing, so
// it recovers no panic: no part of the raw artifact reaches this pass.
type Reconciler struct {
	Queue        ReconcileQueue
	Transactions TransactionRepository

	NewID func() (string, error)
	Now   func() time.Time

	Batch int
	Lease time.Duration
}

func NewReconciler(queue ReconcileQueue, transactions TransactionRepository) *Reconciler {
	return &Reconciler{
		Queue:        queue,
		Transactions: transactions,
		NewID:        id.New,
		Now:          func() time.Time { return time.Now().UTC() },
		Batch:        DefaultReconcileBatch,
		Lease:        DefaultReconcileLease,
	}
}

// Run claims one batch of Evidence at stage EXTRACTED and builds Transactions
// from the Claims that each row holds. It returns at the end of the batch, and
// the caller decides if it runs again. One failed row does not stop the pass.
func (r *Reconciler) Run(ctx context.Context) (ReconcileResult, error) {
	now := r.Now().UTC()
	pending, err := r.Queue.ClaimForReconciliation(ctx, r.batch(), now, now.Add(r.lease()))
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("reconcile: claim evidence: %w", err)
	}

	result := ReconcileResult{}
	for i, work := range pending {
		if err := ctx.Err(); err != nil {
			// Shutting down. Rows claimed and not yet attempted are released
			// rather than left to sit out their lease — they were never tried,
			// so there is no failure to record and nothing to back off from.
			r.releaseUnattempted(ctx, pending[i:])
			return result, err
		}
		outcome, created, dated := r.reconcileOne(ctx, work)
		switch outcome {
		case outcomeTransacted:
			result.TransactionsCreated += created
			result.DatedFromEvidence += dated
		case outcomeClaimless:
			result.Claimless++
		case outcomeAlreadyBuilt:
			result.Skipped++
		case outcomeBuildFailed:
			result.Failed++
		}
		result.EvidenceProcessed++
	}

	result.CompletedAt = r.Now().UTC()
	return result, nil
}

// reconcileOutcome is what happened to one row. Only an unavailable queue ends
// a pass.
type reconcileOutcome int

const (
	outcomeTransacted reconcileOutcome = iota
	outcomeClaimless
	outcomeAlreadyBuilt
	outcomeBuildFailed
)

// reconcileOne builds each Transaction that the interpretation of one row
// describes. It reports how many it wrote, and how many used the date fallback.
// It returns no error, because each failure belongs on the row. It builds the
// complete interpretation or none of it: one bad Claim fails the artifact.
func (r *Reconciler) reconcileOne(ctx context.Context, work PendingReconciliation) (reconcileOutcome, int, int) {
	if len(work.Claims) == 0 {
		// Extraction found no financial event. The row leaves the pipeline
		// although it carries nothing (D44). If it stayed at EXTRACTED, each
		// later pass would read all 244 again.
		if err := r.Queue.MarkReconciled(ctx, work.EvidenceID, r.Now()); err != nil {
			return r.fail(ctx, work, classStore, err), 0, 0
		}
		return outcomeClaimless, 0, 0
	}

	built := make([]BuiltTransaction, 0, len(work.Claims))
	dated := 0
	for _, claim := range work.Claims {
		transaction, derived, err := r.buildTransaction(claim, work.ObservedAt)
		if err != nil {
			return r.fail(ctx, work, classTransaction, err), 0, 0
		}
		if derived {
			dated++
		}
		built = append(built, BuiltTransaction{Transaction: transaction, SourceClaimID: claim.ID()})
	}

	created, err := r.Transactions.Save(ctx, built, r.Now())
	if err != nil {
		return r.fail(ctx, work, classStore, err), 0, 0
	}
	if !created {
		// These Claims already have their Transactions. Another pass reached
		// the artifact first, or the work was already done. The store wrote
		// nothing, and the row still advances.
		if err := r.Queue.MarkReconciled(ctx, work.EvidenceID, r.Now()); err != nil {
			return r.fail(ctx, work, classStore, err), 0, 0
		}
		return outcomeAlreadyBuilt, 0, 0
	}
	return outcomeTransacted, len(built), dated
}

// buildTransaction maps one ACTIVE Claim onto the Transaction aggregate, and
// reports if the date came from the fallback. domain.NewTransaction decides if
// the fields make a valid Transaction. A field that the Claim does not state
// stays absent, except for the two cases below that DATA_MODEL.md specifies.
func (r *Reconciler) buildTransaction(claim domain.Claim, observedAt time.Time) (domain.Transaction, bool, error) {
	occurredAt, derived := occurredAtFor(claim, observedAt)

	// Money is optional and arrives as a validated pair, or not at all. Claim
	// guarantees that an amount keeps its currency. A Claim with no amount
	// gives the zero Money, which is how TransactionDraft shows absence.
	money, _ := claim.Money()

	// Use UNKNOWN where the Claim states no status (D43). 101 of 800 Claims
	// state none. DOMAIN.md §5 makes UNKNOWN a valid state, because Evidence
	// often does not show if an event is an authorization or a settlement.
	status := domain.StatusUnknown
	if stated := textField(claim, domain.FieldFinancialStatus); stated != "" {
		status = domain.FinancialStatus(stated)
	}

	transactionID, err := r.NewID()
	if err != nil {
		return domain.Transaction{}, false, fmt.Errorf("reconcile: generate transaction id: %w", err)
	}

	transaction, err := domain.NewTransaction(domain.TransactionDraft{
		ID:       transactionID,
		Money:    money,
		Merchant: textField(claim, domain.FieldMerchant),

		// The counterparty is its own field and never the merchant (D61). It
		// carries the reserved value where both sides are the user (D62), and
		// the Transaction keeps that value so a reader can leave the movement
		// out of the totals (D55).
		Counterparty: textField(claim, domain.FieldCounterparty),

		AccountIdentifier: textField(claim, domain.FieldAccountIdentifier),
		Direction:         domain.TransactionDirection(textField(claim, domain.FieldDirection)),
		FinancialStatus:   status,

		// Each Transaction starts UNRECONCILED. One Claim makes one Transaction
		// (D42). DOMAIN.md §6 decides if two of them describe one event, and no
		// code asks that question yet.
		ReconciliationState: domain.Unreconciled,

		// The Transaction starts ACTIVE. It is Billy's current record of the
		// event until a better reading of the artifact replaces it (D49).
		State: domain.TransactionActive,

		OccurredAt:  occurredAt,
		EvidenceIDs: claim.EvidenceIDs(),
		CreatedAt:   r.Now().UTC(),
	})
	if err != nil {
		return domain.Transaction{}, false, fmt.Errorf("reconcile: claim %s did not yield a valid transaction: %w", claim.ID(), err)
	}
	return transaction, derived, nil
}

// occurredAtFor reports when the event happened, and if that time came from the
// artifact or from the fallback. DATA_MODEL.md §4.5 requires a value, so Billy
// uses the earliest observed_at of the Evidence where the artifact states none.
// This code computes the fallback before the write and never in a query.
func occurredAtFor(claim domain.Claim, observedAt time.Time) (time.Time, bool) {
	if stated, ok := claim.OccurredAt(); ok {
		return stated, false
	}
	// A zero observedAt would be a defect in the queue, because
	// evidence.observed_at is NOT NULL. domain.NewTransaction rejects the zero
	// time, so the row fails and does not take a date in the year 1.
	return observedAt, true
}

// textField reads one text field, or "" if the Claim does not state it. An
// empty string always means absence, because NewTextField rejects it as a
// value.
func textField(c domain.Claim, name domain.FieldName) string {
	f, ok := c.Field(name)
	if !ok {
		return ""
	}
	return f.Text()
}

// fail records one failed attempt on its row.
//
// The stage does not move. Retry is not a processing stage (DATA_MODEL.md §7).
// The artifact stays at EXTRACTED with its Claims.
func (r *Reconciler) fail(ctx context.Context, work PendingReconciliation, class string, cause error) reconcileOutcome {
	retryAt := r.Now().UTC().Add(retryAfter(work.Attempts))
	if err := r.Queue.RecordFailure(ctx, work.EvidenceID, reason(class, cause), retryAt); err != nil {
		// Nothing further to do: the lease lapses on its own and the row
		// returns. Reported by id, never by content.
		slog.Error("could not record a reconciliation failure",
			"evidence", work.EvidenceID, "class", class, "error", err)
	}
	return outcomeBuildFailed
}

// releaseUnattempted returns the rows that a pass claimed during a shutdown.
//
// It uses a context that is detached from the cancelled one. A cancelled
// context would fail each statement and hold the rows until the lease ends.
func (r *Reconciler) releaseUnattempted(ctx context.Context, pending []PendingReconciliation) {
	detached := context.WithoutCancel(ctx)
	for _, work := range pending {
		if err := r.Queue.Release(detached, work.EvidenceID, r.Now()); err != nil {
			slog.Warn("could not release evidence on shutdown",
				"evidence", work.EvidenceID, "error", err)
		}
	}
}

func (r *Reconciler) batch() int {
	if r.Batch <= 0 {
		return DefaultReconcileBatch
	}
	return r.Batch
}

func (r *Reconciler) lease() time.Duration {
	if r.Lease <= 0 {
		return DefaultReconcileLease
	}
	return r.Lease
}
