package app_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"

	_ "modernc.org/sqlite"
)

// TestReconcileCorpus runs the reconciliation pass over every stored artifact.
//
// Same contract as the four corpus tests in the parser packages: **read-only**,
// aggregate invariants, and a clean skip where the database is absent. It never
// writes — not to ~/.billy/billy.db and not anywhere else. The Claims are built
// in memory from the stored bytes and the Transactions are handed to a fake
// repository, which is enough to prove that all 800 Claims the parsers produce
// survive domain.NewTransaction.
//
// It lives in `app_test` rather than in `app`, which is what lets it import the
// Nu parser without internal/app depending on an adapter. The dependency arrow
// ARCHITECTURE.md §4 draws is untouched: this is a black-box test of the
// pipeline, not a use case reaching for an implementation.
//
// It is the test that would catch this slice being quietly wrong. A Claim that
// makes no valid Transaction, a status defaulted where one was stated, an
// occurred_at invented for an artifact that carried no date — none of those
// fail a unit test on a handful of fixtures, and all of them show up as a count
// that moved here.
func TestReconcileCorpus(t *testing.T) {
	db := openCorpus(t)
	defer db.Close()

	rows, err := db.Query(`SELECT id, raw_content, observed_at FROM evidence ORDER BY observed_at`)
	if err != nil {
		t.Fatalf("query corpus: %v", err)
	}
	defer rows.Close()

	var (
		pending   []app.PendingReconciliation
		artifacts int
		at        = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	)
	for rows.Next() {
		var evidenceID, observedAt string
		var raw []byte
		if err := rows.Scan(&evidenceID, &raw, &observedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		artifacts++

		observed, err := time.Parse(time.RFC3339, observedAt)
		if err != nil {
			t.Fatalf("%s: observed_at: %v", evidenceID, err)
		}
		item := app.PendingReconciliation{EvidenceID: evidenceID, ObservedAt: observed, Attempts: 1}

		fields, err := nu.Interpreter{}.Interpret(raw)
		switch {
		case errors.Is(err, app.ErrNoInterpretation):
			// No Claim. The row still has to leave the queue (D44).
		case err != nil:
			t.Errorf("%s: %v", evidenceID, err)
			continue
		default:
			proposed, err := domain.NewClaim("claim-"+evidenceID, domain.ClaimProposed, []string{evidenceID}, fields, at)
			if err != nil {
				t.Errorf("%s: not a valid claim: %v", evidenceID, err)
				continue
			}
			active, err := proposed.Activate(at)
			if err != nil {
				t.Errorf("%s: cannot activate: %v", evidenceID, err)
				continue
			}
			item.Claim, item.HasClaim = active, true
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate corpus: %v", err)
	}

	queue := &fakeReconcileQueue{pending: pending}
	transactions := &fakeTransactions{}
	reconciler := newReconciler(queue, transactions)
	// One pass over the whole corpus. The batch is a lock bound in production;
	// here there are no locks and the point is to see every artifact.
	reconciler.Batch = len(pending)
	// Ids have to be unique across 800 Transactions, and newReconciler's
	// single-rune generator is not. This is the only thing injected differently
	// from the unit tests — still no clock, still no crypto/rand.
	next := 0
	reconciler.NewID = func() (string, error) {
		next++
		return "tx-" + string(rune('0'+next/1000%10)) + string(rune('0'+next/100%10)) +
			string(rune('0'+next/10%10)) + string(rune('0'+next%10)), nil
	}

	result, err := reconciler.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("artifacts %d · transactions %d · dated from evidence %d · claimless %d · failed %d",
		result.EvidenceProcessed, result.TransactionsCreated, result.DatedFromEvidence,
		result.Claimless, result.Failed)

	// Measured 2026-08-25 and unchanged by this slice.
	assertCorpusCount(t, "artifacts", artifacts, 1044)
	assertCorpusCount(t, "artifacts processed", result.EvidenceProcessed, 1044)

	// **Nothing fails.** A Claim that does not make a Transaction is a bug in
	// this mapping, not a bad artifact, and there are none.
	assertCorpusCount(t, "failures", result.Failed, 0)

	// D44: the 244 that produced no Claim advance carrying nothing.
	assertCorpusCount(t, "claimless artifacts", result.Claimless, 244)
	assertCorpusCount(t, "claimless artifacts advanced", len(queue.reconciled), 244)

	// **Every Claim becomes a Transaction.** 710 of the 800 state an event time;
	// the 90 `¡Recibimos tu pago!` card payments state none and take
	// DATA_MODEL.md §4.5's fallback to their Evidence's observed_at, computed
	// before the write.
	assertCorpusCount(t, "transactions", result.TransactionsCreated, 800)
	assertCorpusCount(t, "transactions saved", len(transactions.saved), 800)
	assertCorpusCount(t, "dated from evidence", result.DatedFromEvidence, 90)

	// The aggregate invariants, on every Transaction that was built.
	var (
		withMoney, withMerchant, unknownStatus, settled int
		byDirection                                     = map[domain.TransactionDirection]int{}
		observedByEvidence                              = map[string]time.Time{}
		claimByID                                       = map[string]domain.Claim{}
		datedFromEvidence                               int
	)
	for _, item := range pending {
		observedByEvidence[item.EvidenceID] = item.ObservedAt
		if item.HasClaim {
			claimByID[item.Claim.ID()] = item.Claim
		}
	}
	for _, saved := range transactions.saved {
		tx := saved.transaction

		// DOMAIN.md §4's hard invariant: provenance to at least one piece of
		// Evidence, and it is the artifact the Claim came from.
		provenance := tx.EvidenceIDs()
		if len(provenance) != 1 || "claim-"+provenance[0] != saved.sourceClaimID {
			t.Errorf("%s: provenance = %v, source claim = %s", tx.ID(), provenance, saved.sourceClaimID)
		}
		// Every Transaction is born UNRECONCILED (D42).
		if tx.ReconciliationState() != domain.Unreconciled {
			t.Errorf("%s: reconciliation state = %s", tx.ID(), tx.ReconciliationState())
		}
		// occurred_at is always populated (DATA_MODEL.md §4.5) — never the zero
		// time, and never midnight standing in for a date nobody read.
		if tx.OccurredAt().IsZero() {
			t.Errorf("%s: occurred_at is the zero time", tx.ID())
		}
		// Verify the date against its provenance, not by comparing the two
		// possible values: a stated event time may legitimately equal the
		// Evidence timestamp.
		observed := observedByEvidence[provenance[0]]
		claim, found := claimByID[saved.sourceClaimID]
		if !found {
			t.Errorf("%s: source Claim %s was not in the corpus input", tx.ID(), saved.sourceClaimID)
			continue
		}
		if stated, ok := claim.OccurredAt(); ok {
			if !tx.OccurredAt().Equal(stated) {
				t.Errorf("%s: occurred_at %s replaced the Claim's stated time %s",
					tx.ID(), tx.OccurredAt(), stated)
			}
		} else {
			datedFromEvidence++
			if !tx.OccurredAt().Equal(observed) {
				t.Errorf("%s: occurred_at %s did not take its Evidence time %s",
					tx.ID(), tx.OccurredAt(), observed)
			}
		}
		// The event time can never postdate Billy receiving the notification.
		// A Transaction dated after its own Evidence would mean a parser read a
		// date out of the wrong field, and it is the shape a timezone error
		// takes: D29 chose America/Mexico_City, and a six-hour slip the wrong
		// way shows up here rather than in a fixture.
		if tx.OccurredAt().After(observed) {
			t.Errorf("%s: occurred_at %s is after its Evidence was observed at %s",
				tx.ID(), tx.OccurredAt(), observed)
		}
		if money, ok := tx.Money(); ok {
			withMoney++
			// Money is unsigned; direction carries the sign (DOMAIN.md §3).
			if money.Minor() < 0 {
				t.Errorf("%s: negative amount %s", tx.ID(), money)
			}
			if money.Currency() != nu.Currency {
				t.Errorf("%s: currency = %s, want %s (D30)", tx.ID(), money.Currency(), nu.Currency)
			}
		}
		if tx.Merchant() != "" {
			withMerchant++
		}
		// No Nu template states the user's own account (CONTEXT.md §3.1). A
		// value here would be the counterparty's card poisoning the account
		// signal DOMAIN.md §6 relies on.
		if tx.AccountIdentifier() != "" {
			t.Errorf("%s: account_identifier = %q, and no template states one", tx.ID(), tx.AccountIdentifier())
		}
		switch tx.FinancialStatus() {
		case domain.StatusUnknown:
			unknownStatus++
		case domain.StatusSettled:
			settled++
		default:
			t.Errorf("%s: financial status = %s, and no template claims one", tx.ID(), tx.FinancialStatus())
		}
		byDirection[tx.Direction()]++
	}

	t.Logf("money %d · merchant %d · INFLOW %d · OUTFLOW %d · SETTLED %d · UNKNOWN %d",
		withMoney, withMerchant, byDirection[domain.Inflow], byDirection[domain.Outflow], settled, unknownStatus)

	// Every Claim asserts an amount and a currency, so every Transaction has
	// Money.
	assertCorpusCount(t, "transactions with money", withMoney, 800)

	// Every template but the card payment names a counterparty: 800 - 90.
	assertCorpusCount(t, "transactions with a merchant", withMerchant, 710)

	// D35 and D40: both transfer receipts are SETTLED — 354 outflows and 345
	// inflows. The 90 card payments and 11 service payments claim no status and
	// take UNKNOWN (D43).
	assertCorpusCount(t, "SETTLED", settled, 699)
	assertCorpusCount(t, "UNKNOWN", unknownStatus, 101)

	// D31: the card payment is an OUTFLOW. 354 transfers + 90 card payments +
	// 11 service payments.
	assertCorpusCount(t, "INFLOW", byDirection[domain.Inflow], 345)
	assertCorpusCount(t, "OUTFLOW", byDirection[domain.Outflow], 455)

	// Counted independently of ReconcileResult from the Claim's field presence,
	// while the loop above verifies that each branch produced the right value.
	assertCorpusCount(t, "dated from evidence, recounted", datedFromEvidence, 90)
}

// TestReconcilingTheCorpusTwiceCreatesNothingTheSecondTime is the M1 shape of
// the proof, one stage further along: 1,044 discovered, 0 created, 1,044
// skipped. Idempotency by constraint (D41), against the real corpus.
func TestReconcilingTheCorpusTwiceCreatesNothingTheSecondTime(t *testing.T) {
	db := openCorpus(t)
	defer db.Close()

	rows, err := db.Query(`SELECT id, raw_content, observed_at FROM evidence ORDER BY observed_at LIMIT 200`)
	if err != nil {
		t.Fatalf("query corpus: %v", err)
	}
	defer rows.Close()

	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	var pending []app.PendingReconciliation
	for rows.Next() {
		var evidenceID, observedAt string
		var raw []byte
		if err := rows.Scan(&evidenceID, &raw, &observedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		observed, _ := time.Parse(time.RFC3339, observedAt)
		item := app.PendingReconciliation{EvidenceID: evidenceID, ObservedAt: observed, Attempts: 1}
		fields, err := nu.Interpreter{}.Interpret(raw)
		if err == nil {
			proposed, err := domain.NewClaim("claim-"+evidenceID, domain.ClaimProposed, []string{evidenceID}, fields, at)
			if err != nil {
				continue
			}
			active, err := proposed.Activate(at)
			if err != nil {
				continue
			}
			item.Claim, item.HasClaim = active, true
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate corpus: %v", err)
	}

	transactions := &fakeTransactions{}
	run := func() app.ReconcileResult {
		t.Helper()
		queue := &fakeReconcileQueue{pending: pending}
		r := newReconciler(queue, transactions)
		r.Batch = len(pending)
		next := 0
		r.NewID = func() (string, error) {
			next++
			return "tx-" + string(rune('0'+next/1000%10)) + string(rune('0'+next/100%10)) +
				string(rune('0'+next/10%10)) + string(rune('0'+next%10)), nil
		}
		result, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return result
	}

	first := run()
	second := run()

	t.Logf("first: created %d · second: created %d skipped %d",
		first.TransactionsCreated, second.TransactionsCreated, second.Skipped)

	if first.TransactionsCreated == 0 {
		t.Fatal("the first pass created nothing; there is nothing to prove idempotent")
	}
	if second.TransactionsCreated != 0 {
		t.Errorf("the second pass created %d transactions; the money is in the table twice", second.TransactionsCreated)
	}
	if second.Skipped != first.TransactionsCreated {
		t.Errorf("second pass skipped %d, want the %d the first created", second.Skipped, first.TransactionsCreated)
	}
	if len(transactions.saved) != first.TransactionsCreated {
		t.Errorf("%d transactions exist after two passes, want %d", len(transactions.saved), first.TransactionsCreated)
	}
}

// openCorpus opens the real database read-only, and skips cleanly when it is
// not there.
//
// mode=ro and query_only(1) are belt and braces: the first refuses to open the
// file for writing, the second refuses to execute a statement that would write.
// This test reads someone's actual financial history, and BILLYCORE_CORPUS_DB
// is how it is pointed somewhere else.
//
// Deliberately duplicated from the parser packages' own helper rather than
// exported from one of them: a test helper shared across packages has to live
// in non-test code, and a function whose only job is to open the author's
// database does not belong in the shipped binary.
func openCorpus(t *testing.T) *sql.DB {
	t.Helper()
	path := os.Getenv("BILLYCORE_CORPUS_DB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory, so no corpus: %v", err)
		}
		path = filepath.Join(home, ".billy", "billy.db")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no corpus at %s; set BILLYCORE_CORPUS_DB to run this test", path)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	return db
}

func assertCorpusCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}
