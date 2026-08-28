package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

var (
	reconciledAt = time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)
	eventTime    = time.Date(2026, 8, 16, 23, 44, 0, 0, time.UTC)
	seenAt       = time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
)

// --- fakes ------------------------------------------------------------------
//
// The whole pass runs here with no database, no clock and no randomness, which
// is the property ARCHITECTURE.md §4 exists to buy and D19 exists to spend.

type fakeReconcileQueue struct {
	pending    []app.PendingReconciliation
	reconciled []string
	released   []string
	failures   []recordedFailure
	claimErr   error
	advanceErr error
}

func (q *fakeReconcileQueue) ClaimForReconciliation(_ context.Context, limit int, _, _ time.Time) ([]app.PendingReconciliation, error) {
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if limit < len(q.pending) {
		return q.pending[:limit], nil
	}
	return q.pending, nil
}

func (q *fakeReconcileQueue) MarkReconciled(_ context.Context, evidenceID string, _ time.Time) error {
	if q.advanceErr != nil {
		return q.advanceErr
	}
	q.reconciled = append(q.reconciled, evidenceID)
	return nil
}

func (q *fakeReconcileQueue) Release(_ context.Context, evidenceID string, _ time.Time) error {
	q.released = append(q.released, evidenceID)
	return nil
}

func (q *fakeReconcileQueue) RecordFailure(_ context.Context, evidenceID, reason string, retryAt time.Time) error {
	q.failures = append(q.failures, recordedFailure{evidenceID, reason, retryAt})
	return nil
}

type savedTransaction struct {
	transaction   domain.Transaction
	sourceClaimID string
}

type fakeTransactions struct {
	saved []savedTransaction
	err   error

	// occupied mimics `claim_transaction`'s primary key: a Claim that already
	// has a Transaction cannot get a second one (D41).
	occupied map[string]bool
}

func (r *fakeTransactions) Save(_ context.Context, built []app.BuiltTransaction, _ time.Time) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	if r.occupied == nil {
		r.occupied = map[string]bool{}
	}
	// The set is all-or-nothing: one taken slot means the artifact was already
	// built, and nothing is written.
	for _, b := range built {
		if r.occupied[b.SourceClaimID] {
			return false, nil
		}
	}
	for _, b := range built {
		r.occupied[b.SourceClaimID] = true
		r.saved = append(r.saved, savedTransaction{b.Transaction, b.SourceClaimID})
	}
	return true, nil
}

func newReconciler(q *fakeReconcileQueue, r *fakeTransactions) *app.Reconciler {
	ids := 0
	return &app.Reconciler{
		Queue:        q,
		Transactions: r,
		NewID: func() (string, error) {
			ids++
			return "tx-" + string(rune('a'+ids-1)), nil
		},
		Now: func() time.Time { return reconciledAt },
	}
}

// --- claim builders ---------------------------------------------------------

func claimField(t *testing.T, v string, c domain.Confidence) domain.ClaimField {
	t.Helper()
	f, err := domain.NewTextField(v, c)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	return f
}

// activeClaim builds the interpretation of an outflow receipt: the richest of
// the four Nu templates, and the one 354 artifacts produce.
func activeClaim(t *testing.T, id, evidenceID string, extra map[domain.FieldName]domain.ClaimField) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	occurred, err := domain.NewTimeField(eventTime, domain.High)
	if err != nil {
		t.Fatalf("NewTimeField: %v", err)
	}
	fields := map[domain.FieldName]domain.ClaimField{
		domain.FieldAmountMinor:     amount,
		domain.FieldCurrency:        claimField(t, "MXN", domain.Low),
		domain.FieldMerchant:        claimField(t, "HSBC beneficiary", domain.Medium),
		domain.FieldDirection:       claimField(t, "OUTFLOW", domain.High),
		domain.FieldFinancialStatus: claimField(t, "SETTLED", domain.High),
		domain.FieldOccurredAt:      occurred,
	}
	for name, field := range extra {
		fields[name] = field
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID}, fields, extractedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	active, err := proposed.Activate(extractedAt)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return active
}

// claimWithout drops one field, which is how the corpus's real gaps are shaped:
// no financial_status on 101 Claims, no occurred_at on 90.
func claimWithout(t *testing.T, id, evidenceID string, drop ...domain.FieldName) domain.Claim {
	t.Helper()
	full := activeClaim(t, id, evidenceID, nil)
	fields := map[domain.FieldName]domain.ClaimField{}
	for _, name := range full.FieldNames() {
		f, _ := full.Field(name)
		fields[name] = f
	}
	for _, name := range drop {
		delete(fields, name)
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID}, fields, extractedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	active, err := proposed.Activate(extractedAt)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return active
}

func work(claim domain.Claim, evidenceID string) app.PendingReconciliation {
	return app.PendingReconciliation{
		EvidenceID: evidenceID,
		ObservedAt: seenAt,
		Claims:     []domain.Claim{claim},
		Attempts:   1,
	}
}

// --- tests ------------------------------------------------------------------

func TestAnActiveClaimBecomesATransactionWithProvenance(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(activeClaim(t, "claim-a", "ev-1", nil), "ev-1"),
	}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsCreated != 1 || result.EvidenceProcessed != 1 {
		t.Fatalf("result = %+v, want one Transaction from one artifact", result)
	}
	if len(transactions.saved) != 1 {
		t.Fatalf("saved %d transactions, want 1", len(transactions.saved))
	}

	saved := transactions.saved[0]
	if saved.sourceClaimID != "claim-a" {
		t.Errorf("source claim = %q, want claim-a — the slot keys on it (D41)", saved.sourceClaimID)
	}
	tx := saved.transaction
	// DOMAIN.md §4's one hard invariant, carried through the whole pass.
	if ids := tx.EvidenceIDs(); len(ids) != 1 || ids[0] != "ev-1" {
		t.Errorf("provenance = %v, want [ev-1]", ids)
	}
	money, ok := tx.Money()
	if !ok || money.Minor() != 100000 || money.Currency() != "MXN" {
		t.Errorf("money = %s (present=%v), want 100000 MXN", money, ok)
	}
	if tx.Direction() != domain.Outflow {
		t.Errorf("direction = %s, want OUTFLOW", tx.Direction())
	}
	if tx.FinancialStatus() != domain.StatusSettled {
		t.Errorf("status = %s, want SETTLED", tx.FinancialStatus())
	}
	// Every Transaction is born UNRECONCILED. One Claim makes one Transaction
	// (D42); whether two describe the same event is DOMAIN.md §6's question,
	// and nothing has asked it yet.
	if tx.ReconciliationState() != domain.Unreconciled {
		t.Errorf("reconciliation state = %s, want UNRECONCILED", tx.ReconciliationState())
	}
	if !tx.OccurredAt().Equal(eventTime) {
		t.Errorf("occurred_at = %s, want the time the artifact stated %s", tx.OccurredAt(), eventTime)
	}
	// The stage advance rides inside Save's transaction, so the pass must not
	// also advance the row itself — that would move it outside the commit that
	// wrote the money.
	if len(queue.reconciled) != 0 {
		t.Errorf("the pass advanced %v separately; Save owns that for rows it wrote", queue.reconciled)
	}
}

// D43. 101 of 800 Claims assert no financial_status: the card payments and the
// service payments state nothing about settlement. UNKNOWN is a domain value
// meaning "Billy looked and cannot tell" (DOMAIN.md §5), not a gap dressed up.
func TestAClaimWithNoFinancialStatusBecomesUnknown(t *testing.T) {
	claim := claimWithout(t, "claim-a", "ev-1", domain.FieldFinancialStatus)
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}
	transactions := &fakeTransactions{}

	if _, err := newReconciler(queue, transactions).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(transactions.saved) != 1 {
		t.Fatalf("saved %d transactions, want 1", len(transactions.saved))
	}
	if got := transactions.saved[0].transaction.FinancialStatus(); got != domain.StatusUnknown {
		t.Errorf("status = %s, want UNKNOWN", got)
	}
}

// D44. 244 of the 1,044 artifacts carry no Claim and never will. Stage is
// pipeline position, not a claim about the data (ARCHITECTURE.md §5): they are
// finished, and leaving them at EXTRACTED would have every later pass re-read
// them for the life of the database.
func TestAnArtifactWithNoClaimAdvancesAndProducesNothing(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		{EvidenceID: "ev-1", ObservedAt: seenAt, Attempts: 1},
	}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Claimless != 1 || result.TransactionsCreated != 0 || result.Failed != 0 {
		t.Errorf("result = %+v, want one claimless row and no failure", result)
	}
	if len(queue.reconciled) != 1 || queue.reconciled[0] != "ev-1" {
		t.Errorf("advanced %v, want [ev-1]", queue.reconciled)
	}
	if len(transactions.saved) != 0 {
		t.Error("an artifact with no interpretation produced a Transaction")
	}
}

// D41's whole point: re-running reconciliation must not put the same money in
// the table twice. The fake enforces it as the primary key does — by refusing
// the second insert, not by the pass remembering to look.
func TestReRunningReconciliationCreatesNoSecondTransaction(t *testing.T) {
	claim := activeClaim(t, "claim-a", "ev-1", nil)
	transactions := &fakeTransactions{}

	first := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}
	if result, err := newReconciler(first, transactions).Run(context.Background()); err != nil || result.TransactionsCreated != 1 {
		t.Fatalf("first pass: result=%+v err=%v", result, err)
	}

	second := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}
	result, err := newReconciler(second, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if result.TransactionsCreated != 0 || result.Skipped != 1 {
		t.Errorf("result = %+v, want nothing created and one skipped", result)
	}
	if len(transactions.saved) != 1 {
		t.Errorf("%d transactions exist for one Claim; the money is in the table twice", len(transactions.saved))
	}
	// The row still needs advancing: it *has* been reconciled, just not by this
	// pass. Leaving it at EXTRACTED would make it immortal work.
	if len(second.reconciled) != 1 || second.reconciled[0] != "ev-1" {
		t.Errorf("advanced %v, want [ev-1]", second.reconciled)
	}
}

// DATA_MODEL.md §4.5's fallback. A Claim that states no event time still makes
// a Transaction, dated from the Evidence Billy holds — the 90 `¡Recibimos tu
// pago!` card payments, whose bodies carry no date at all.
func TestAClaimWithNoOccurredAtIsDatedFromItsEvidence(t *testing.T) {
	claim := claimWithout(t, "claim-a", "ev-1", domain.FieldOccurredAt)
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TransactionsCreated != 1 || result.Failed != 0 {
		t.Errorf("result = %+v, want one Transaction and no failure", result)
	}
	if result.DatedFromEvidence != 1 {
		t.Errorf("DatedFromEvidence = %d, want 1 — the count is how the fallback stays visible", result.DatedFromEvidence)
	}
	if len(queue.failures) != 0 {
		t.Errorf("recorded %v; an artifact with no body date is not a failure", queue.failures)
	}

	got := transactions.saved[0].transaction.OccurredAt()
	if !got.Equal(seenAt) {
		t.Errorf("occurred_at = %s, want the Evidence's observed_at %s", got, seenAt)
	}
	// Not the ingestion time and not midnight — the two plausible wrong answers
	// D33 put occurred_at in the Claim vocabulary to keep distinguishable.
	if got.Equal(reconciledAt) {
		t.Error("occurred_at took the clock, not the Evidence")
	}
}

// A Claim that *does* state a time keeps it, and is not counted as derived.
// The fallback must not quietly overwrite the 710 artifacts that carry a date.
func TestAStatedOccurredAtIsNeverReplacedByTheFallback(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(activeClaim(t, "claim-a", "ev-1", nil), "ev-1"),
	}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.DatedFromEvidence != 0 {
		t.Errorf("DatedFromEvidence = %d, want 0", result.DatedFromEvidence)
	}
	if got := transactions.saved[0].transaction.OccurredAt(); !got.Equal(eventTime) {
		t.Errorf("occurred_at = %s, want the time the artifact stated %s", got, eventTime)
	}
}

// The fallback substitutes a real timestamp or nothing at all. A zero
// observed_at would be a bug in the queue — `evidence.observed_at` is NOT NULL —
// and the row must fail visibly rather than acquire a date in the year 1.
func TestTheFallbackDoesNotSubstituteTheZeroTime(t *testing.T) {
	claim := claimWithout(t, "claim-a", "ev-1", domain.FieldOccurredAt)
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		{EvidenceID: "ev-1", Claims: []domain.Claim{claim}, Attempts: 1},
	}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Failed != 1 || result.TransactionsCreated != 0 {
		t.Errorf("result = %+v, want one failure and no Transaction", result)
	}
	if len(transactions.saved) != 0 {
		t.Error("a Transaction was written with no usable date")
	}
}

// A Claim that cannot make a valid Transaction is a bug in Billy's own mapping,
// not a bad artifact — and it is still recorded on its own row rather than
// stopping the pass.
func TestAClaimThatCannotMakeATransactionIsRecordedAndDoesNotAdvance(t *testing.T) {
	claim := claimWithout(t, "claim-a", "ev-1", domain.FieldDirection)
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v, want one failure", result)
	}
	if len(queue.failures) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(queue.failures))
	}
	if got := queue.failures[0].reason; got != "TRANSACTION" {
		t.Errorf("reason = %q, want the class alone", got)
	}
	if len(queue.reconciled) != 0 {
		t.Error("a failed row advanced; retry is not a stage (DATA_MODEL.md §7)")
	}
}

// SECURITY.md §10. `last_error` is built from a closed vocabulary and never
// from err.Error(), and the error here carries a Claim id — which is safe — but
// nothing from the artifact must ever reach the column.
func TestTheRecordedReasonCarriesNothingFromTheClaim(t *testing.T) {
	claim := claimWithout(t, "claim-a", "ev-1", domain.FieldDirection)
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{work(claim, "ev-1")}}

	if _, err := newReconciler(queue, &fakeTransactions{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reason := queue.failures[0].reason
	for _, leaked := range []string{"HSBC beneficiary", "MXN", "100000", "claim-a"} {
		if strings.Contains(reason, leaked) {
			t.Errorf("reason %q carries %q out of the interpretation", reason, leaked)
		}
	}
}

// One bad row does not stop the pass. SECURITY.md §7 requires it of extraction
// and the same argument holds one stage later: the rows behind a failure are
// still holding locks.
func TestOneUnbuildableClaimDoesNotStopThePass(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(claimWithout(t, "claim-a", "ev-1", domain.FieldDirection), "ev-1"),
		work(activeClaim(t, "claim-b", "ev-2", nil), "ev-2"),
		{EvidenceID: "ev-3", ObservedAt: seenAt},
	}}
	transactions := &fakeTransactions{}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.EvidenceProcessed != 3 || result.Failed != 1 || result.TransactionsCreated != 1 || result.Claimless != 1 {
		t.Errorf("result = %+v, want 3 processed: 1 failed, 1 created, 1 claimless", result)
	}
}

// A store that cannot be written to ends nothing. The row keeps its Claim and
// its Evidence, and a later pass tries again.
func TestAStoreFailureIsRecordedAgainstTheRow(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(activeClaim(t, "claim-a", "ev-1", nil), "ev-1"),
	}}
	transactions := &fakeTransactions{err: errors.New("disk is gone")}

	result, err := newReconciler(queue, transactions).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v, want one failure", result)
	}
	if len(queue.failures) != 1 || queue.failures[0].reason != "STORE" {
		t.Errorf("failures = %+v, want one STORE", queue.failures)
	}
	if len(queue.reconciled) != 0 {
		t.Error("the row advanced despite nothing being written")
	}
}

// The queue being unreachable is the one thing that ends a pass.
func TestAnUnreachableQueueEndsThePass(t *testing.T) {
	queue := &fakeReconcileQueue{claimErr: errors.New("database is locked")}
	if _, err := newReconciler(queue, &fakeTransactions{}).Run(context.Background()); err == nil {
		t.Error("Run succeeded with an unreachable queue")
	}
}

// Rows claimed and never attempted are handed back, rather than sitting out
// their lease for a failure that did not happen.
func TestShutdownReleasesTheReconciliationRowsItNeverAttempted(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(activeClaim(t, "claim-a", "ev-1", nil), "ev-1"),
		work(activeClaim(t, "claim-b", "ev-2", nil), "ev-2"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newReconciler(queue, &fakeTransactions{}).Run(ctx); err == nil {
		t.Error("Run reported success on a cancelled context")
	}
	if len(queue.released) != 2 {
		t.Errorf("released %v, want both rows", queue.released)
	}
}

// D19 and ARCHITECTURE.md §4: the use case owns no clock and no randomness, so
// the whole pass runs with neither. A Reconciler built by hand with both
// injected has no path to time.Now or crypto/rand.
func TestReconciliationRunsWithoutAClockOrRandomnessOfItsOwn(t *testing.T) {
	queue := &fakeReconcileQueue{pending: []app.PendingReconciliation{
		work(activeClaim(t, "claim-a", "ev-1", nil), "ev-1"),
	}}
	transactions := &fakeTransactions{}
	r := newReconciler(queue, transactions)

	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.CompletedAt.Equal(reconciledAt) {
		t.Errorf("completed_at = %s, want the injected clock %s", result.CompletedAt, reconciledAt)
	}
	if got := transactions.saved[0].transaction.ID(); got != "tx-a" {
		t.Errorf("transaction id = %q, want the injected generator's", got)
	}
	if !transactions.saved[0].transaction.CreatedAt().Equal(reconciledAt) {
		t.Errorf("created_at = %s, want the injected clock", transactions.saved[0].transaction.CreatedAt())
	}
}

// The batch bounds one pass, for the reason extraction's does: a pass that
// claimed everything and then crashed would hold every row locked until the
// lease lapsed.
func TestTheBatchSizeBoundsOneReconciliationPass(t *testing.T) {
	var pending []app.PendingReconciliation
	for i := 0; i < 5; i++ {
		id := string(rune('1' + i))
		pending = append(pending, work(activeClaim(t, "claim-"+id, "ev-"+id, nil), "ev-"+id))
	}
	queue := &fakeReconcileQueue{pending: pending}
	r := newReconciler(queue, &fakeTransactions{})
	r.Batch = 2

	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.EvidenceProcessed != 2 {
		t.Errorf("processed %d, want the batch of 2", result.EvidenceProcessed)
	}
}
