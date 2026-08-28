package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var (
	txOccurred = time.Date(2026, 8, 16, 23, 44, 0, 0, time.UTC)
	txCreated  = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
)

func mustMoney(t *testing.T, minor int64, currency domain.Currency) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(minor, currency)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	return m
}

// validDraft is the shape everything below varies one field of, so each test
// says only what it is about.
func validDraft(t *testing.T) domain.TransactionDraft {
	t.Helper()
	return domain.TransactionDraft{
		ID:                  "tx-1",
		Money:               mustMoney(t, 100000, "MXN"),
		Merchant:            "HSBC beneficiary",
		Direction:           domain.Outflow,
		FinancialStatus:     domain.StatusSettled,
		ReconciliationState: domain.Unreconciled,
		State:               domain.TransactionActive,
		OccurredAt:          txOccurred,
		EvidenceIDs:         []string{"ev-1"},
		CreatedAt:           txCreated,
	}
}

func TestNewTransactionKeepsWhatItWasGiven(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	money, ok := tx.Money()
	if !ok {
		t.Fatal("Money reported absent")
	}
	if money.Minor() != 100000 || money.Currency() != "MXN" {
		t.Errorf("money = %s, want 100000 MXN", money)
	}
	if tx.Direction() != domain.Outflow {
		t.Errorf("direction = %s, want OUTFLOW", tx.Direction())
	}
	if tx.ReconciliationState() != domain.Unreconciled {
		t.Errorf("reconciliation state = %s, want UNRECONCILED", tx.ReconciliationState())
	}
	if !tx.OccurredAt().Equal(txOccurred) {
		t.Errorf("occurred_at = %s, want %s", tx.OccurredAt(), txOccurred)
	}
	// created_at and updated_at are one instant at construction, as they are on
	// a Claim: the Transaction really was created and nothing has changed it.
	if !tx.CreatedAt().Equal(tx.UpdatedAt()) {
		t.Errorf("created_at %s and updated_at %s differ at construction", tx.CreatedAt(), tx.UpdatedAt())
	}
}

// DOMAIN.md §4: every Transaction traces back to at least one piece of
// Evidence. This is the aggregate's one hard invariant, and it is the
// difference between a number Billy can answer for and a number Billy made up.
func TestATransactionWithoutProvenanceIsInvalid(t *testing.T) {
	for _, name := range []string{"nil", "empty slice", "empty string"} {
		draft := validDraft(t)
		switch name {
		case "nil":
			draft.EvidenceIDs = nil
		case "empty slice":
			draft.EvidenceIDs = []string{}
		case "empty string":
			draft.EvidenceIDs = []string{""}
		}
		if _, err := domain.NewTransaction(draft); !errors.Is(err, domain.ErrTransactionNoProvenance) {
			t.Errorf("%s: err = %v, want ErrTransactionNoProvenance", name, err)
		}
	}
}

func TestProvenanceIsDeduplicatedAndSorted(t *testing.T) {
	draft := validDraft(t)
	draft.EvidenceIDs = []string{"ev-2", "ev-1", "ev-2"}
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	got := tx.EvidenceIDs()
	if len(got) != 2 || got[0] != "ev-1" || got[1] != "ev-2" {
		t.Errorf("evidence ids = %v, want [ev-1 ev-2]", got)
	}
}

// Handing out the slice would let a caller rewrite a recorded Transaction's
// provenance through the returned reference.
func TestEvidenceIDsCannotBeRewrittenThroughTheAccessor(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	tx.EvidenceIDs()[0] = "ev-tampered"
	if got := tx.EvidenceIDs()[0]; got != "ev-1" {
		t.Errorf("provenance became %q through the accessor", got)
	}
}

// DATA_MODEL.md §4.5: a Transaction may exist without Money where the Evidence
// does not support an amount. The boolean is what keeps that different from an
// amount of zero (DATA_MODEL.md §2).
func TestATransactionMayCarryNoMoney(t *testing.T) {
	draft := validDraft(t)
	draft.Money = domain.Money{}
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	if _, ok := tx.Money(); ok {
		t.Error("Money reported present on a Transaction that carries none")
	}

	draft.Money = mustMoney(t, 0, "MXN")
	zero, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	money, ok := zero.Money()
	if !ok {
		t.Fatal("a zero amount is an amount, and it went missing")
	}
	if money.Minor() != 0 || money.Currency() != "MXN" {
		t.Errorf("money = %s, want 0 MXN", money)
	}
}

// DOMAIN.md §4: currency is required wherever Money exists. Money's own
// constructor guarantees the pair, but a struct literal inside the domain
// package can hold half of one, and that is the shape this catches.
func TestAnAmountWithoutACurrencyIsRejected(t *testing.T) {
	draft := validDraft(t)
	draft.Money = domain.Money{}
	// There is no exported way to build half a Money, which is the point. The
	// nearest a caller can get is an amount whose currency failed validation,
	// and NewMoney refuses to return it at all.
	if _, err := domain.NewMoney(800, ""); err == nil {
		t.Error("NewMoney accepted an amount with no currency")
	}
	if _, err := domain.NewMoney(-1, "MXN"); !errors.Is(err, domain.ErrNegativeAmount) {
		t.Errorf("err = %v, want ErrNegativeAmount — direction carries the sign", err)
	}
}

// DOMAIN.md §3: there is no UNKNOWN direction. Money that moved without Billy
// knowing which way is not a smaller fact, it is an unsupported one.
func TestDirectionIsRequiredAndClosed(t *testing.T) {
	for _, direction := range []domain.TransactionDirection{"", "SIDEWAYS", "inflow"} {
		draft := validDraft(t)
		draft.Direction = direction
		if _, err := domain.NewTransaction(draft); err == nil {
			t.Errorf("direction %q was accepted", direction)
		}
	}
}

// UNKNOWN is a real member of FinancialStatus and a legitimate value for a
// Transaction (DOMAIN.md §5, D43). The empty string is not: a caller that
// forgot the field must not look like one that read the artifact and could not
// tell.
func TestFinancialStatusAcceptsUnknownAndRejectsAbsence(t *testing.T) {
	draft := validDraft(t)
	draft.FinancialStatus = domain.StatusUnknown
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("UNKNOWN is a legitimate status: %v", err)
	}
	if tx.FinancialStatus() != domain.StatusUnknown {
		t.Errorf("status = %s, want UNKNOWN", tx.FinancialStatus())
	}

	draft.FinancialStatus = ""
	if _, err := domain.NewTransaction(draft); err == nil {
		t.Error("an absent financial status was silently accepted; it must not default to UNKNOWN here")
	}
}

func TestReconciliationStateIsRequiredAndClosed(t *testing.T) {
	for _, state := range []domain.ReconciliationState{"", "PENDING", "reconciled"} {
		draft := validDraft(t)
		draft.ReconciliationState = state
		if _, err := domain.NewTransaction(draft); err == nil {
			t.Errorf("reconciliation state %q was accepted", state)
		}
	}
	for _, state := range []domain.ReconciliationState{domain.Unreconciled, domain.Reconciled, domain.Conflicted} {
		draft := validDraft(t)
		draft.ReconciliationState = state
		if _, err := domain.NewTransaction(draft); err != nil {
			t.Errorf("reconciliation state %q was rejected: %v", state, err)
		}
	}
}

// DATA_MODEL.md §4.5: occurred_at is always populated, and the fallback that
// fills it is computed before the write. The domain does not apply that
// fallback — it cannot see Evidence timestamps — so a draft without a time is
// a caller that skipped a step, not a Transaction with a gap.
func TestOccurredAtIsRequired(t *testing.T) {
	draft := validDraft(t)
	draft.OccurredAt = time.Time{}
	if _, err := domain.NewTransaction(draft); !errors.Is(err, domain.ErrTransactionNoOccurredAt) {
		t.Errorf("err = %v, want ErrTransactionNoOccurredAt", err)
	}
}

func TestTimestampsAreNormalisedToUTC(t *testing.T) {
	mexico := time.FixedZone("CST", -6*60*60)
	draft := validDraft(t)
	draft.OccurredAt = txOccurred.In(mexico)
	draft.CreatedAt = txCreated.In(mexico)

	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	if _, offset := tx.OccurredAt().Zone(); offset != 0 {
		t.Errorf("occurred_at kept a %d-second offset", offset)
	}
	if !tx.OccurredAt().Equal(txOccurred) {
		t.Errorf("occurred_at = %s, want the same instant %s", tx.OccurredAt(), txOccurred)
	}
}

func TestIDAndCreatedAtAreRequired(t *testing.T) {
	draft := validDraft(t)
	draft.ID = ""
	if _, err := domain.NewTransaction(draft); !errors.Is(err, domain.ErrTransactionNoID) {
		t.Errorf("err = %v, want ErrTransactionNoID", err)
	}
	draft = validDraft(t)
	draft.CreatedAt = time.Time{}
	if _, err := domain.NewTransaction(draft); err == nil {
		t.Error("a Transaction was created with no created_at")
	}
}

// Merchant and account are optional, and empty means Billy read none — not a
// counterparty named nothing. No Nu template states the user's own account, so
// account_identifier is absent on all 800 Claims the corpus produces.
func TestMerchantAndAccountAreOptional(t *testing.T) {
	draft := validDraft(t)
	draft.Merchant = ""
	draft.AccountIdentifier = ""
	tx, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	if tx.Merchant() != "" || tx.AccountIdentifier() != "" {
		t.Errorf("merchant %q / account %q, want both empty", tx.Merchant(), tx.AccountIdentifier())
	}
}
