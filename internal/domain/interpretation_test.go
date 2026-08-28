package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var interpretedAt = time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)

func activeClaim(t *testing.T, id, evidenceID string) domain.Claim {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	currency, err := domain.NewTextField("MXN", domain.Low)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	proposed, err := domain.NewClaim(id, domain.ClaimProposed, []string{evidenceID},
		map[domain.FieldName]domain.ClaimField{
			domain.FieldAmountMinor: amount,
			domain.FieldCurrency:    currency,
		}, interpretedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	active, err := proposed.Activate(interpretedAt)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return active
}

// The shape that D46 needs: one artifact, one reading, many movements. An email
// gives one Claim, and a statement gives many.
func TestAnInterpretationHoldsManyClaims(t *testing.T) {
	claims := []domain.Claim{
		activeClaim(t, "claim-1", "ev-1"),
		activeClaim(t, "claim-2", "ev-1"),
		activeClaim(t, "claim-3", "ev-1"),
	}
	in, err := domain.NewInterpretation("interp-1", "ev-1", claims, "", interpretedAt)
	if err != nil {
		t.Fatalf("NewInterpretation: %v", err)
	}
	if got := in.ClaimIDs(); len(got) != 3 {
		t.Errorf("claim ids = %v, want three", got)
	}
	if in.EvidenceID() != "ev-1" {
		t.Errorf("evidence = %q, want ev-1", in.EvidenceID())
	}
	if in.Supersedes() != "" {
		t.Errorf("a first reading supersedes %q, want nothing", in.Supersedes())
	}
}

// Claims returns a copy. If it returned the slice, a caller could change a
// recorded reading through the reference.
func TestInterpretationClaimsCannotBeRewrittenThroughTheAccessor(t *testing.T) {
	in, err := domain.NewInterpretation("interp-1", "ev-1",
		[]domain.Claim{activeClaim(t, "claim-1", "ev-1")}, "", interpretedAt)
	if err != nil {
		t.Fatalf("NewInterpretation: %v", err)
	}
	in.Claims()[0] = activeClaim(t, "claim-rewritten", "ev-1")
	if got := in.ClaimIDs(); got[0] != "claim-1" {
		t.Errorf("claim = %q after writing through the accessor, want claim-1", got[0])
	}
}

// The lineage that D47 records, old to new. It is a field, and not an order
// that somebody computes from timestamps.
func TestAnInterpretationNamesTheOneItReplaces(t *testing.T) {
	in, err := domain.NewInterpretation("interp-2", "ev-1",
		[]domain.Claim{activeClaim(t, "claim-2", "ev-1")}, "interp-1", interpretedAt)
	if err != nil {
		t.Fatalf("NewInterpretation: %v", err)
	}
	if in.Supersedes() != "interp-1" {
		t.Errorf("supersedes = %q, want interp-1", in.Supersedes())
	}
}

func TestAnEmptyInterpretationIsRefused(t *testing.T) {
	// "No template recognises this" is an answer about the artifact, and the
	// pipeline records it with MarkExtracted. A reading with no Claims is a
	// different thing, and it is not a reading.
	_, err := domain.NewInterpretation("interp-1", "ev-1", nil, "", interpretedAt)
	if !errors.Is(err, domain.ErrInterpretationNoClaims) {
		t.Errorf("err = %v, want ErrInterpretationNoClaims", err)
	}
}

func TestAnInterpretationRequiresItsIdentityAndItsArtifact(t *testing.T) {
	claims := []domain.Claim{activeClaim(t, "claim-1", "ev-1")}
	if _, err := domain.NewInterpretation("", "ev-1", claims, "", interpretedAt); !errors.Is(err, domain.ErrInterpretationNoID) {
		t.Errorf("no id: err = %v, want ErrInterpretationNoID", err)
	}
	if _, err := domain.NewInterpretation("interp-1", "", claims, "", interpretedAt); !errors.Is(err, domain.ErrInterpretationNoEvidence) {
		t.Errorf("no evidence: err = %v, want ErrInterpretationNoEvidence", err)
	}
	if _, err := domain.NewInterpretation("interp-1", "ev-1", claims, "", time.Time{}); err == nil {
		t.Error("an interpretation with no created_at was accepted")
	}
	if _, err := domain.NewInterpretation("interp-1", "ev-1", claims, "interp-1", interpretedAt); err == nil {
		t.Error("an interpretation was allowed to supersede itself")
	}
}

// Billy activates a set, so each member is ACTIVE (D46). A PROPOSED Claim is an
// offer from outside, and it takes no slot.
func TestAnInterpretationRefusesAClaimThatIsNotActive(t *testing.T) {
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	currency, err := domain.NewTextField("MXN", domain.Low)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	proposed, err := domain.NewClaim("claim-1", domain.ClaimProposed, []string{"ev-1"},
		map[domain.FieldName]domain.ClaimField{
			domain.FieldAmountMinor: amount,
			domain.FieldCurrency:    currency,
		}, interpretedAt)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}
	if _, err := domain.NewInterpretation("interp-1", "ev-1", []domain.Claim{proposed}, "", interpretedAt); err == nil {
		t.Error("a PROPOSED claim was accepted into an interpretation")
	}
}

// The check that the database cannot make. A foreign key shows that the Claim
// exists. It does not show that the Claim is about this artifact.
func TestAnInterpretationRefusesAClaimAboutAnotherArtifact(t *testing.T) {
	claims := []domain.Claim{activeClaim(t, "claim-1", "ev-other")}
	if _, err := domain.NewInterpretation("interp-1", "ev-1", claims, "", interpretedAt); err == nil {
		t.Error("a claim resting on another artifact was accepted")
	}
}

func TestAnInterpretationRefusesTheSameClaimTwice(t *testing.T) {
	claim := activeClaim(t, "claim-1", "ev-1")
	if _, err := domain.NewInterpretation("interp-1", "ev-1", []domain.Claim{claim, claim}, "", interpretedAt); err == nil {
		t.Error("one claim was accepted twice into one interpretation")
	}
}

// D49 — the lifecycle has a closed list of values, and an empty string is not
// one of them. A caller that forgets the field must not look like a caller that
// made a decision.
func TestTransactionStateIsRequiredAndClosed(t *testing.T) {
	for _, state := range []domain.TransactionState{domain.TransactionActive, domain.TransactionSuperseded} {
		if err := state.Validate(); err != nil {
			t.Errorf("%s was rejected: %v", state, err)
		}
	}
	for _, state := range []domain.TransactionState{"", "RETIRED", "active", "UNKNOWN"} {
		if err := domain.TransactionState(state).Validate(); err == nil {
			t.Errorf("%q was accepted as a transaction state", state)
		}
	}
}
