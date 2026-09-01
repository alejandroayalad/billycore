package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

var matchNow = time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

// fakeReconRepo scripts the pairs and records what Reconcile was asked to do,
// so the Matcher's decisions are testable without a database.
type fakeReconRepo struct {
	keyed        []app.TrackingKeyed
	unreconciled []app.SourcedTransaction
	decisions    []app.ReconcileDecision
	seen         map[string]bool // canonical pair -> already recorded
}

func (f *fakeReconRepo) UnreconciledWithTrackingKey(context.Context) ([]app.TrackingKeyed, error) {
	return f.keyed, nil
}

func (f *fakeReconRepo) UnreconciledTransactions(context.Context) ([]app.SourcedTransaction, error) {
	return f.unreconciled, nil
}

// srcTxs wraps Transactions as coming from one Source, so a weak fixture reads
// as a single-Source set unless a test spreads it across Sources.
func srcTxs(source string, txs ...domain.Transaction) []app.SourcedTransaction {
	out := make([]app.SourcedTransaction, 0, len(txs))
	for _, tx := range txs {
		out = append(out, app.SourcedTransaction{Transaction: tx, SourceID: source})
	}
	return out
}

func (f *fakeReconRepo) Reconcile(_ context.Context, d app.ReconcileDecision, _ time.Time) (bool, error) {
	f.decisions = append(f.decisions, d)
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	pair := d.LeftID + "|" + d.RightID
	if f.seen[pair] {
		return false, nil // the constraint would reject the second write
	}
	f.seen[pair] = true
	return true, nil
}

func trackingKeyed(t *testing.T, id, key string, dir domain.TransactionDirection, counterparty, evidenceID string) app.TrackingKeyed {
	t.Helper()
	money, err := domain.NewMoney(100000, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Direction: dir, Counterparty: counterparty,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: matchNow, EvidenceIDs: []string{evidenceID}, CreatedAt: matchNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app.TrackingKeyed{Transaction: tx, TrackingKey: key}
}

func newMatcher(repo app.ReconciliationRepository) *app.Matcher {
	m := app.NewMatcher(repo)
	n := 0
	m.NewID = func() (string, error) { n++; return "cand-" + string(rune('a'+n)), nil }
	m.Now = func() time.Time { return matchNow }
	return m
}

// Two Transactions with one tracking key are a MATCH, and the smaller id is the
// survivor that absorbs the other's Evidence (D36, D70).
func TestMatcherMergesOnASharedTrackingKey(t *testing.T) {
	repo := &fakeReconRepo{keyed: []app.TrackingKeyed{
		trackingKeyed(t, "tx-stmt", "KEY1", domain.Outflow, "Ada Lovelace", "ev-stmt"),
		trackingKeyed(t, "tx-email", "KEY1", domain.Outflow, "Ada Lovelace", "ev-email"),
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 1 || result.NoMatch != 0 || result.CandidatesRecorded != 1 {
		t.Fatalf("result = %+v", result)
	}
	d := repo.decisions[0]
	if d.LeftID != "tx-email" || d.RightID != "tx-stmt" {
		t.Errorf("pair = (%s, %s), want canonical (tx-email, tx-stmt)", d.LeftID, d.RightID)
	}
	if d.Outcome != domain.Match {
		t.Errorf("outcome = %s, want MATCH", d.Outcome)
	}
	if d.Survivor.ID() != "tx-email" || d.Superseded.ID() != "tx-stmt" {
		t.Errorf("survivor=%s superseded=%s, want survivor tx-email", d.Survivor.ID(), d.Superseded.ID())
	}
	if got := d.Survivor.EvidenceIDs(); len(got) != 2 || got[0] != "ev-email" || got[1] != "ev-stmt" {
		t.Errorf("survivor provenance = %v, want both", got)
	}
	if d.Superseded.State() != domain.TransactionSuperseded ||
		d.Superseded.SupersededByTransactionID() != "tx-email" {
		t.Errorf("superseded = %+v", d.Superseded)
	}
}

// Opposite directions share no event, so the same tracking key still does not
// merge them (DOMAIN.md §6).
func TestMatcherRefusesOppositeDirections(t *testing.T) {
	repo := &fakeReconRepo{keyed: []app.TrackingKeyed{
		trackingKeyed(t, "tx-a", "KEY1", domain.Outflow, "Ada", "ev-a"),
		trackingKeyed(t, "tx-b", "KEY1", domain.Inflow, "Ada", "ev-b"),
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.NoMatch != 1 {
		t.Fatalf("result = %+v", result)
	}
	if repo.decisions[0].Outcome != domain.NoMatch {
		t.Errorf("outcome = %s, want NO_MATCH", repo.decisions[0].Outcome)
	}
}

// An internal movement never reconciles with an external one (D55).
func TestMatcherRefusesInternalAgainstExternal(t *testing.T) {
	repo := &fakeReconRepo{keyed: []app.TrackingKeyed{
		trackingKeyed(t, "tx-a", "KEY1", domain.Outflow, domain.CounterpartySelf, "ev-a"),
		trackingKeyed(t, "tx-b", "KEY1", domain.Outflow, "Ada", "ev-b"),
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.NoMatch != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// A group of three observations of one movement collapses onto the smallest id.
func TestMatcherCollapsesAGroupOntoTheSmallestID(t *testing.T) {
	repo := &fakeReconRepo{keyed: []app.TrackingKeyed{
		trackingKeyed(t, "tx-c", "KEY1", domain.Outflow, "Ada", "ev-c"),
		trackingKeyed(t, "tx-a", "KEY1", domain.Outflow, "Ada", "ev-a"),
		trackingKeyed(t, "tx-b", "KEY1", domain.Outflow, "Ada", "ev-b"),
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 2 {
		t.Fatalf("merged = %d, want 2", result.Merged)
	}
	for _, d := range repo.decisions {
		if d.Survivor.ID() != "tx-a" {
			t.Errorf("survivor = %s, want tx-a", d.Survivor.ID())
		}
	}
}

// A pass whose candidate already exists records nothing new: the constraint,
// modelled by the fake, makes the re-run a no-op (D70).
func TestMatcherIsIdempotent(t *testing.T) {
	keyed := []app.TrackingKeyed{
		trackingKeyed(t, "tx-a", "KEY1", domain.Outflow, "Ada", "ev-a"),
		trackingKeyed(t, "tx-b", "KEY1", domain.Outflow, "Ada", "ev-b"),
	}
	repo := &fakeReconRepo{keyed: keyed}
	m := newMatcher(repo)
	if _, err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The store would drop these to SUPERSEDED/RECONCILED; the fake still holds
	// them, so a second sweep proves the constraint alone stops a re-merge.
	second, err := m.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Merged != 0 || second.CandidatesRecorded != 0 {
		t.Fatalf("second sweep = %+v, want nothing new", second)
	}
}

// weakTx builds an ACTIVE UNRECONCILED Transaction for the weak sweep. A zero
// minor with an empty currency is no Money, which the block skips (D71).
func weakTx(t *testing.T, id string, minor int64, currency string, dir domain.TransactionDirection, counterparty string) domain.Transaction {
	t.Helper()
	var money domain.Money
	if currency != "" {
		var err error
		if money, err = domain.NewMoney(minor, domain.Currency(currency)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Direction: dir, Counterparty: counterparty,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: matchNow, EvidenceIDs: []string{"ev-" + id}, CreatedAt: matchNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// Two Transactions in one amount block, with no shared tracking key, are
// AMBIGUOUS and never merge: the critical rule of DOMAIN.md §6 (D71).
func TestMatcherWeakSameAmountIsAmbiguous(t *testing.T) {
	repo := &fakeReconRepo{unreconciled: srcTxs("gmail",
		weakTx(t, "tx-b", 100000, "MXN", domain.Outflow, "Ada"),
		weakTx(t, "tx-a", 100000, "MXN", domain.Outflow, "Ada"),
	)}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.NoMatch != 0 || result.Ambiguous != 1 {
		t.Fatalf("result = %+v, want one AMBIGUOUS", result)
	}
	d := repo.decisions[0]
	if d.LeftID != "tx-a" || d.RightID != "tx-b" || d.Outcome != domain.Ambiguous {
		t.Errorf("decision = %+v, want canonical AMBIGUOUS pair", d)
	}
	if d.Survivor.ID() != "" || d.Superseded.ID() != "" {
		t.Errorf("AMBIGUOUS built a merge: %+v", d)
	}
}

// An internal movement against an external one in one amount block is NO_MATCH,
// even with no tracking key (D55).
func TestMatcherWeakInternalVsExternalIsNoMatch(t *testing.T) {
	repo := &fakeReconRepo{unreconciled: srcTxs("gmail",
		weakTx(t, "tx-self", 100000, "MXN", domain.Outflow, domain.CounterpartySelf),
		weakTx(t, "tx-ext", 100000, "MXN", domain.Outflow, "Ada"),
	)}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.NoMatch != 1 || result.Ambiguous != 0 {
		t.Fatalf("result = %+v, want one NO_MATCH", result)
	}
}

// Different amounts or directions fall in different blocks and never pair, so no
// candidate is written for a pair that plainly differs (D71). Currency is a
// third component of the key; only MXN is supported today (D50), so a
// cross-currency block cannot be built from a valid Transaction.
func TestMatcherWeakDifferentBlocksNeverPair(t *testing.T) {
	repo := &fakeReconRepo{unreconciled: srcTxs("gmail",
		weakTx(t, "tx-a", 100000, "MXN", domain.Outflow, "Ada"),
		weakTx(t, "tx-b", 200000, "MXN", domain.Outflow, "Ada"), // other amount
		weakTx(t, "tx-d", 100000, "MXN", domain.Inflow, "Ada"),  // other direction
	)}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidatesRecorded != 0 {
		t.Fatalf("result = %+v, want no candidate", result)
	}
}

// A Transaction with no Money has no amount to block on, so it is left out of
// the weak sweep (D71).
func TestMatcherWeakSkipsTransactionWithoutMoney(t *testing.T) {
	repo := &fakeReconRepo{unreconciled: srcTxs("gmail",
		weakTx(t, "tx-a", 0, "", domain.Outflow, "Ada"),
		weakTx(t, "tx-b", 0, "", domain.Outflow, "Ada"),
	)}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidatesRecorded != 0 {
		t.Fatalf("result = %+v, want no candidate", result)
	}
}

// compositeTx builds a Transaction for the composite sweep: it carries a merchant
// and a real occurred_at, the two fields the composite key rests on (D72).
func compositeTx(t *testing.T, id string, minor int64, dir domain.TransactionDirection, merchant string, occurredAt time.Time) domain.Transaction {
	t.Helper()
	money, err := domain.NewMoney(minor, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Direction: dir, Merchant: merchant,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: occurredAt, EvidenceIDs: []string{"ev-" + id}, CreatedAt: matchNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// A cross-Source pair that shares the exact composite key merges (D72). The
// email time is UTC and the statement time is local; UTC−6 puts both on the same
// day, so the key holds and the smaller id survives.
func TestMatcherCompositeMergesUniqueCrossSourcePair(t *testing.T) {
	emailAt := time.Date(2026, 6, 27, 0, 22, 0, 0, time.UTC)     // 27th in UTC
	stmtAt := time.Date(2026, 6, 26, 18, 22, 0, 0, mxTestMinus6) // 26th local, same instant
	repo := &fakeReconRepo{unreconciled: []app.SourcedTransaction{
		{Transaction: compositeTx(t, "tx-stmt", 100000, domain.Outflow, "Plomero", stmtAt), SourceID: "statement"},
		{Transaction: compositeTx(t, "tx-email", 100000, domain.Outflow, "plomero ", emailAt), SourceID: "gmail"},
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 1 || result.Ambiguous != 0 || result.NoMatch != 0 {
		t.Fatalf("result = %+v, want one composite MATCH", result)
	}
	d := repo.decisions[0]
	if d.Outcome != domain.Match || d.Basis != "composite" {
		t.Errorf("decision outcome=%s basis=%s, want MATCH/composite", d.Outcome, d.Basis)
	}
	if d.Survivor.ID() != "tx-email" || d.Superseded.ID() != "tx-stmt" {
		t.Errorf("survivor=%s superseded=%s, want survivor tx-email", d.Survivor.ID(), d.Superseded.ID())
	}
}

// Same-Source look-alikes never composite-merge: two same-day, same-merchant
// payments from one Source are two events, not one, so they stay AMBIGUOUS (D72).
func TestMatcherCompositeKeepsSameSourceApart(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	repo := &fakeReconRepo{unreconciled: []app.SourcedTransaction{
		{Transaction: compositeTx(t, "tx-a", 50000, domain.Outflow, "Oxxo", at), SourceID: "gmail"},
		{Transaction: compositeTx(t, "tx-b", 50000, domain.Outflow, "Oxxo", at), SourceID: "gmail"},
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.Ambiguous != 1 {
		t.Fatalf("result = %+v, want no merge and one AMBIGUOUS", result)
	}
}

// A composite key held by three Transactions is not unique on each side, so none
// merge and they stay AMBIGUOUS (D72).
func TestMatcherCompositeRefusesAmbiguousTriple(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	repo := &fakeReconRepo{unreconciled: []app.SourcedTransaction{
		{Transaction: compositeTx(t, "tx-a", 50000, domain.Outflow, "Oxxo", at), SourceID: "statement"},
		{Transaction: compositeTx(t, "tx-b", 50000, domain.Outflow, "Oxxo", at), SourceID: "gmail"},
		{Transaction: compositeTx(t, "tx-c", 50000, domain.Outflow, "Oxxo", at), SourceID: "gmail"},
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 {
		t.Fatalf("result = %+v, want no merge on a non-unique triple", result)
	}
}

// A different merchant breaks the composite key even across Sources, so the pair
// stays AMBIGUOUS rather than merging (D72).
func TestMatcherCompositeRefusesDifferentMerchant(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	repo := &fakeReconRepo{unreconciled: []app.SourcedTransaction{
		{Transaction: compositeTx(t, "tx-a", 50000, domain.Outflow, "Oxxo", at), SourceID: "statement"},
		{Transaction: compositeTx(t, "tx-b", 50000, domain.Outflow, "Seven", at), SourceID: "gmail"},
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 0 || result.Ambiguous != 1 {
		t.Fatalf("result = %+v, want no merge and one AMBIGUOUS", result)
	}
}

var mxTestMinus6 = time.FixedZone("UTC-6", -6*60*60)

// partyTx builds a Transaction that names the other party in merchant OR in
// counterparty, so a test can mimic how an email and a statement differ (D72).
func partyTx(t *testing.T, id string, minor int64, dir domain.TransactionDirection, merchant, counterparty string, at time.Time) domain.Transaction {
	t.Helper()
	money, err := domain.NewMoney(minor, "MXN")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewTransaction(domain.TransactionDraft{
		ID: id, Money: money, Direction: dir, Merchant: merchant, Counterparty: counterparty,
		FinancialStatus: domain.StatusSettled, ReconciliationState: domain.Unreconciled,
		State: domain.TransactionActive, OccurredAt: at, EvidenceIDs: []string{"ev-" + id}, CreatedAt: matchNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// The email names the party in merchant and the statement in counterparty; the
// composite party reads across both fields, so the pair still merges (D72).
func TestMatcherCompositeUnifiesMerchantAndCounterparty(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	repo := &fakeReconRepo{unreconciled: []app.SourcedTransaction{
		{Transaction: partyTx(t, "tx-email", 100000, domain.Outflow, "Plomero", "", at), SourceID: "gmail"},
		{Transaction: partyTx(t, "tx-stmt", 100000, domain.Outflow, "", "plomero", at), SourceID: "statement"},
	}}
	result, err := newMatcher(repo).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Merged != 1 || result.Ambiguous != 0 {
		t.Fatalf("result = %+v, want one composite MATCH", result)
	}
	if repo.decisions[0].Basis != "composite" {
		t.Errorf("basis = %s, want composite", repo.decisions[0].Basis)
	}
}
