package domain_test

import (
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var mergedAt = time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

// The survivor of a merge keeps both provenances and turns RECONCILED, and it
// stays ACTIVE so it is still shown (D70).
func TestReconciledWithUnionsProvenanceAndStaysActive(t *testing.T) {
	draft := validDraft(t)
	draft.EvidenceIDs = []string{"ev-email"}
	survivor, err := domain.NewTransaction(draft)
	if err != nil {
		t.Fatal(err)
	}

	merged, err := survivor.ReconciledWith([]string{"ev-statement", "ev-email"}, mergedAt)
	if err != nil {
		t.Fatalf("ReconciledWith: %v", err)
	}
	if merged.ReconciliationState() != domain.Reconciled {
		t.Errorf("reconciliation state = %s, want RECONCILED", merged.ReconciliationState())
	}
	if merged.State() != domain.TransactionActive {
		t.Errorf("state = %s, want ACTIVE", merged.State())
	}
	got := merged.EvidenceIDs()
	if len(got) != 2 || got[0] != "ev-email" || got[1] != "ev-statement" {
		t.Errorf("provenance = %v, want both, sorted, once each", got)
	}
	if !merged.UpdatedAt().Equal(mergedAt) {
		t.Errorf("updated_at = %s, want %s", merged.UpdatedAt(), mergedAt)
	}
}

// Only an ACTIVE Transaction can be a survivor: a superseded row absorbing
// another would be a defect in the caller.
func TestReconciledWithRejectsANonActiveSurvivor(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	retired, err := tx.SupersededByTransaction("tx-2", mergedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retired.ReconciledWith([]string{"ev-x"}, mergedAt); err == nil {
		t.Error("a superseded transaction absorbed another; want an error")
	}
}

// The loser turns SUPERSEDED and points at the survivor, and it also turns
// RECONCILED — it was determined to be the same event (D70).
func TestSupersededByTransactionRetiresAndPoints(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	retired, err := tx.SupersededByTransaction("tx-survivor", mergedAt)
	if err != nil {
		t.Fatalf("SupersededByTransaction: %v", err)
	}
	if retired.State() != domain.TransactionSuperseded {
		t.Errorf("state = %s, want SUPERSEDED", retired.State())
	}
	if retired.ReconciliationState() != domain.Reconciled {
		t.Errorf("reconciliation state = %s, want RECONCILED", retired.ReconciliationState())
	}
	if retired.SupersededByTransactionID() != "tx-survivor" {
		t.Errorf("superseded_by = %q, want tx-survivor", retired.SupersededByTransactionID())
	}
}

// A Transaction cannot supersede itself, and a survivor must be named.
func TestSupersededByTransactionRejectsSelfAndEmpty(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.SupersededByTransaction(tx.ID(), mergedAt); err == nil {
		t.Error("a transaction superseded itself; want an error")
	}
	if _, err := tx.SupersededByTransaction("", mergedAt); err == nil {
		t.Error("a superseded transaction named no survivor; want an error")
	}
}

// An ACTIVE Transaction never carries a survivor pointer.
func TestNewTransactionHasNoSupersededPointer(t *testing.T) {
	tx, err := domain.NewTransaction(validDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	if tx.SupersededByTransactionID() != "" {
		t.Errorf("superseded_by = %q, want empty", tx.SupersededByTransactionID())
	}
}

func TestReconciliationOutcomeValidate(t *testing.T) {
	for _, ok := range []domain.ReconciliationOutcome{domain.Match, domain.NoMatch, domain.Ambiguous} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	if err := domain.ReconciliationOutcome("MAYBE").Validate(); err == nil {
		t.Error("MAYBE accepted; want an error")
	}
}
