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
// The same numbers extraction uses, chosen the same way: the batch is a lock
// bound rather than a memory bound — a pass that claimed all 800 rows and then
// crashed would hold every one of them locked until the lease lapsed — and the
// lease is sized for the crash rather than for the work. Building a Transaction
// from an already-validated Claim is arithmetic; it is nothing like parsing.
const (
	DefaultReconcileBatch = 100
	DefaultReconcileLease = time.Minute
)

// classTransaction — the Claim's fields did not make a valid Transaction.
//
// It is the Transaction-shaped sibling of classClaim, and it means the same
// kind of thing: the domain rejected Billy's own construction, which is a bug
// in the mapping rather than a bad artifact. A Claim asserting an amount and no
// direction lands here.
const classTransaction = "TRANSACTION"

// ReconcileResult is the summary of one reconciliation pass.
type ReconcileResult struct {
	EvidenceProcessed   int
	TransactionsCreated int

	// Claimless counts artifacts that reached EXTRACTED carrying no Claim and
	// will never carry a Transaction — 244 of the 1,044 stored artifacts. They
	// are advanced, not failed: extraction looked and the answer was that there
	// is no financial event here (D44).
	Claimless int

	// Skipped counts Claims that already had a Transaction, so this pass built
	// none. It is the reconciliation half of the number M1's second sync
	// printed, and it means the same thing: the work was already done, and
	// doing it again changed nothing.
	Skipped int

	// DatedFromEvidence counts Transactions whose occurred_at came from
	// DATA_MODEL.md §4.5's fallback rather than from the artifact — the Claim
	// stated no event time, so the Transaction took the earliest observed_at of
	// its supporting Evidence.
	//
	// It is a subset of TransactionsCreated, not a separate outcome. The count
	// is here because the difference matters to anyone reading the table: a
	// fallback date is when Billy *received* the artifact, which for the 90 Nu
	// card payments is minutes after the payment and could in principle be days.
	// Expected to be 90 against the live corpus.
	DatedFromEvidence int

	// Failed counts Claims that were attempted and produced no Transaction
	// because something went wrong. A pass with failures still succeeds: one
	// bad row must not stop the pipeline.
	Failed int

	CompletedAt time.Time
}

// Reconciler turns ACTIVE Claims into Transactions.
//
// It is the third stage of ARCHITECTURE.md §5: it reads the interpretation
// Billy currently holds of an artifact and records the financial event that
// interpretation describes, advancing EXTRACTED → RECONCILED.
//
// **It parses nothing.** A Claim has already been through
// domain.NewClaim — every value in it was validated when it was written, and
// none of the raw artifact reaches this pass. That is why, unlike Extractor,
// there is no per-row panic recovery here: the hostile input is two stages
// upstream and was never let through. A panic in this pass would be a bug in
// Billy's own arithmetic, and swallowing it would hide it.
//
// Like Extractor and Ingestor, it owns no clock and no randomness. Both are
// injected, so the whole use case runs in a test with neither.
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

// Run claims one batch of Evidence at EXTRACTED and builds Transactions from
// the Claims it holds.
//
// It returns when the batch is exhausted rather than looping until the queue is
// empty, for the reason Extractor.Run gives: the caller decides whether to run
// again, which keeps "how much work happens" a scheduling question rather than
// something buried in here.
//
// A failing row does not stop the pass. The error Run returns is reserved for
// what genuinely ends one — the queue being unreachable, or the context being
// cancelled.
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
		switch r.reconcileOne(ctx, work) {
		case outcomeTransacted:
			result.TransactionsCreated++
		case outcomeTransactedFromEvidenceDate:
			result.TransactionsCreated++
			result.DatedFromEvidence++
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

// reconcileOutcome is what happened to one row. Only the queue being
// unreachable ends a pass; everything here belongs on its own row.
type reconcileOutcome int

const (
	outcomeTransacted reconcileOutcome = iota

	// outcomeTransactedFromEvidenceDate is outcomeTransacted with the
	// occurred_at fallback applied. It is a separate outcome only so the count
	// survives to the result; both mean a Transaction was written.
	outcomeTransactedFromEvidenceDate

	outcomeClaimless
	outcomeAlreadyBuilt
	outcomeBuildFailed
)

// reconcileOne builds the Transaction for a single row and records what
// happened to it.
//
// It returns no error, for the reason extractOne does not: every failure it can
// encounter belongs on the row rather than to the caller, including a failure
// to record the failure. At that point the queue is unreachable, the lease
// lapses on its own, and the row returns to a later pass with its Evidence and
// its Claim both still there.
func (r *Reconciler) reconcileOne(ctx context.Context, work PendingReconciliation) reconcileOutcome {
	if !work.HasClaim {
		// Extraction looked and found no financial event. The row is finished
		// with the pipeline even though it carries nothing (D44); leaving it at
		// EXTRACTED would have every later pass re-read all 244 of them.
		if err := r.Queue.MarkReconciled(ctx, work.EvidenceID, r.Now()); err != nil {
			return r.fail(ctx, work, classStore, err)
		}
		return outcomeClaimless
	}

	transaction, derived, err := r.buildTransaction(work)
	if err != nil {
		return r.fail(ctx, work, classTransaction, err)
	}

	created, err := r.Transactions.Save(ctx, transaction, work.Claim.ID(), r.Now())
	if err != nil {
		return r.fail(ctx, work, classStore, err)
	}
	if !created {
		// This Claim already has a Transaction — another pass reached it first,
		// or this one is a re-run over work already done. Nothing was written,
		// so nothing rolled back, and the row still needs advancing: it *has*
		// been reconciled, just not by this pass.
		if err := r.Queue.MarkReconciled(ctx, work.EvidenceID, r.Now()); err != nil {
			return r.fail(ctx, work, classStore, err)
		}
		return outcomeAlreadyBuilt
	}
	if derived {
		return outcomeTransactedFromEvidenceDate
	}
	return outcomeTransacted
}

// buildTransaction maps one ACTIVE Claim onto the Transaction aggregate, and
// reports whether its date came from the fallback rather than from the artifact.
//
// It reads the Claim's fields and hands them to domain.NewTransaction, which is
// the only thing that decides whether they make a valid Transaction. Nothing is
// invented here: a field the Claim does not assert becomes an absent value, not
// a plausible-looking default — with the two exceptions below, and both are
// exceptions DATA_MODEL.md writes down rather than conveniences taken here.
func (r *Reconciler) buildTransaction(work PendingReconciliation) (domain.Transaction, bool, error) {
	claim := work.Claim

	occurredAt, derived := occurredAtFor(claim, work.ObservedAt)

	// Money is optional and comes as a validated pair or not at all — Claim
	// guarantees an amount never loses its currency, so this cannot produce
	// half of one. A Claim asserting no amount yields the zero Money, which is
	// how TransactionDraft spells absence.
	money, _ := claim.Money()

	// UNKNOWN where the Claim asserts no status (D43). 101 of 800 Claims assert
	// none: the card payments and the service payments state nothing about
	// settlement. DOMAIN.md §5 makes UNKNOWN a legitimate state precisely for
	// this — Evidence often does not reveal whether an event is an
	// authorization or a settlement — so recording it is a fact rather than a
	// gap dressed up as one.
	status := domain.StatusUnknown
	if stated := textField(claim, domain.FieldFinancialStatus); stated != "" {
		status = domain.FinancialStatus(stated)
	}

	transactionID, err := r.NewID()
	if err != nil {
		return domain.Transaction{}, false, fmt.Errorf("reconcile: generate transaction id: %w", err)
	}

	transaction, err := domain.NewTransaction(domain.TransactionDraft{
		ID:                transactionID,
		Money:             money,
		Merchant:          textField(claim, domain.FieldMerchant),
		AccountIdentifier: textField(claim, domain.FieldAccountIdentifier),
		Direction:         domain.TransactionDirection(textField(claim, domain.FieldDirection)),
		FinancialStatus:   status,

		// Every Transaction is born UNRECONCILED. One Claim makes one
		// Transaction (D42); whether two of them describe the same event is
		// DOMAIN.md §6's question, and nothing has asked it yet.
		ReconciliationState: domain.Unreconciled,

		OccurredAt:  occurredAt,
		EvidenceIDs: claim.EvidenceIDs(),
		CreatedAt:   r.Now().UTC(),
	})
	if err != nil {
		return domain.Transaction{}, false, fmt.Errorf("reconcile: claim %s did not yield a valid transaction: %w", claim.ID(), err)
	}
	return transaction, derived, nil
}

// occurredAtFor answers when the event happened, and whether the answer came
// from the artifact or from the fallback.
//
// DATA_MODEL.md §4.5: `occurred_at` is always populated. Where Billy knows the
// event time it uses that time; where it does not, it uses the earliest
// observed_at of the supporting Evidence. **The fallback is computed here,
// before the write, and never inside a SQL query** — that is what makes the
// column trustworthy as the cursor `GET /v1/transactions` paginates on, with no
// COALESCE anywhere and no query that has to remember to apply the same rule.
//
// It fires for 90 of the 800 Claims: every `¡Recibimos tu pago!` card payment,
// whose body carries no date at all. D33 put occurred_at in the Claim
// vocabulary precisely so that absence would still be absence at this point —
// those Claims have no field rather than a midnight, and this is the one place
// that decides what to do about it.
//
// **The substitution is honest but lossy**, and worth naming: observed_at is
// when Gmail received the notification, not when the payment happened. For Nu's
// card payments those are minutes apart; for a Source that batches, they need
// not be. Billy has no better answer, and DATA_MODEL.md §4.5 chose a stable
// value over a null. Nothing in the schema records which of the two a given row
// got — that would be a column §4.5 does not have, and adding one is a
// decision, not an implementation detail. The count lives in ReconcileResult
// instead.
//
// "Earliest" is not yet a choice between values: D38 gives one ACTIVE Claim per
// artifact and every Claim today rests on exactly one, so there is exactly one
// observed_at to take. It becomes a real minimum when a Claim may draw on
// several artifacts — DOMAIN.md open question 13, still open — and the port
// hands over one row's timestamp because that is all one row has.
func occurredAtFor(claim domain.Claim, observedAt time.Time) (time.Time, bool) {
	if stated, ok := claim.OccurredAt(); ok {
		return stated, false
	}
	// A zero observedAt is not substituted for silently. It would be a bug in
	// the queue — `evidence.observed_at` is NOT NULL — and domain.NewTransaction
	// rejects the zero time, so the row fails visibly instead of acquiring a
	// date in the year 1.
	return observedAt, true
}

// textField reads one text-valued field, or "" where the Claim does not assert
// it. Empty is absence everywhere in this codebase — NewTextField rejects it as
// a value — so one return suffices.
func textField(c domain.Claim, name domain.FieldName) string {
	f, ok := c.Field(name)
	if !ok {
		return ""
	}
	return f.Text()
}

// fail records one failed attempt against its row.
//
// The stage does not move. Retry is not a processing stage (DATA_MODEL.md §7):
// the artifact stays at EXTRACTED with its Claim intact, and attempts,
// last_error and locked_until carry everything about the retry.
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

// releaseUnattempted hands back rows claimed by a pass that is shutting down.
//
// Detached from the cancelled context on purpose: the whole point is to run
// these statements after the caller has given up, and inheriting the
// cancellation would make every one of them fail and strand the rows for the
// rest of their lease.
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
