package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var created = time.Date(2026, 8, 16, 23, 44, 0, 0, time.UTC)

func TestNewClaim(t *testing.T) {
	c, err := domain.NewClaim("claim-1", domain.ClaimProposed,
		[]string{"evidence-1"}, transferFields(t), created)
	if err != nil {
		t.Fatalf("NewClaim: %v", err)
	}

	if c.ID() != "claim-1" || c.State() != domain.ClaimProposed {
		t.Errorf("id/state = %q/%q", c.ID(), c.State())
	}
	if c.SupersededBy() != "" {
		t.Errorf("a new claim is superseded by %q", c.SupersededBy())
	}
	if !c.CreatedAt().Equal(created) || !c.UpdatedAt().Equal(created) {
		t.Errorf("timestamps = %v / %v", c.CreatedAt(), c.UpdatedAt())
	}

	money, ok := c.Money()
	if !ok {
		t.Fatal("Money() reports no amount")
	}
	if money.Minor() != 100000 || money.Currency() != "MXN" {
		t.Errorf("Money() = %v", money)
	}
}

// The distinction DOMAIN.md §7 insists on: no field at all is not the same as a
// field held weakly.
func TestFieldAbsenceIsNotLowConfidence(t *testing.T) {
	c := mustClaim(t, transferFields(t))

	if _, ok := c.Field(domain.FieldAccountIdentifier); ok {
		t.Error("a field Billy never claimed is present")
	}

	fields := transferFields(t)
	fields[domain.FieldAccountIdentifier] = mustText(t, "1234", domain.Low)
	weak := mustClaim(t, fields)

	got, ok := weak.Field(domain.FieldAccountIdentifier)
	if !ok {
		t.Fatal("a claimed field is absent")
	}
	if got.Text() != "1234" || got.Confidence() != domain.Low {
		t.Errorf("field = %q at %s", got.Text(), got.Confidence())
	}
}

// An empty string is the most convincing fake value there is.
func TestEmptyTextIsAbsenceNotAValue(t *testing.T) {
	if _, err := domain.NewTextField("", domain.High); err == nil {
		t.Fatal("NewTextField accepted an empty value")
	}
}

func TestNewClaimRejects(t *testing.T) {
	valid := transferFields(t)

	tests := []struct {
		name      string
		id        string
		state     domain.ClaimState
		evidence  []string
		fields    map[domain.FieldName]domain.ClaimField
		createdAt time.Time
		is        error
	}{
		{name: "no id", id: "", state: domain.ClaimProposed,
			evidence: []string{"e1"}, fields: valid, createdAt: created, is: domain.ErrClaimNoID},
		{name: "no provenance", id: "c1", state: domain.ClaimProposed,
			evidence: nil, fields: valid, createdAt: created, is: domain.ErrClaimNoProvenance},
		{name: "empty provenance id", id: "c1", state: domain.ClaimProposed,
			evidence: []string{""}, fields: valid, createdAt: created, is: domain.ErrClaimNoProvenance},
		{name: "no fields", id: "c1", state: domain.ClaimProposed,
			evidence: []string{"e1"}, fields: nil, createdAt: created, is: domain.ErrClaimNoFields},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewClaim(tt.id, tt.state, tt.evidence, tt.fields, tt.createdAt)
			if !errors.Is(err, tt.is) {
				t.Errorf("got %v, want %v", err, tt.is)
			}
		})
	}

	// Everything else that must not become a Claim.
	others := []struct {
		name   string
		state  domain.ClaimState
		fields map[domain.FieldName]domain.ClaimField
		at     time.Time
	}{
		{name: "unknown state", state: "MAYBE", fields: valid, at: created},
		{name: "born superseded", state: domain.ClaimSuperseded, fields: valid, at: created},
		{name: "no created_at", state: domain.ClaimProposed, fields: valid},
		{name: "unknown field name", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				"clave_de_rastreo": mustText(t, "NU3AG", domain.High),
			}},
		{name: "amount as text", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldAmountMinor: mustText(t, "100000", domain.High),
				domain.FieldCurrency:    mustText(t, "MXN", domain.High),
			}},
		{name: "currency as an integer", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldCurrency: mustInt(t, 484, domain.High),
			}},
		{name: "amount without currency", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldAmountMinor: mustInt(t, 100000, domain.High),
			}},
		{name: "negative amount", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldAmountMinor: mustInt(t, -1, domain.High),
				domain.FieldCurrency:    mustText(t, "MXN", domain.High),
			}},
		{name: "nonsense currency", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldAmountMinor: mustInt(t, 1, domain.High),
				domain.FieldCurrency:    mustText(t, "pesos", domain.High),
			}},
		{name: "nonsense direction", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldDirection: mustText(t, "SIDEWAYS", domain.High),
			}},
		{name: "nonsense financial status", state: domain.ClaimProposed, at: created,
			fields: map[domain.FieldName]domain.ClaimField{
				domain.FieldFinancialStatus: mustText(t, "REFUNDED", domain.High),
			}},
	}

	for _, tt := range others {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := domain.NewClaim("c1", tt.state, []string{"e1"}, tt.fields, tt.at); err == nil {
				t.Fatalf("NewClaim accepted it, returning %+v", got)
			}
		})
	}
}

// Confidence is validated wherever it appears, including through the field
// constructors.
func TestConfidenceMustBeKnown(t *testing.T) {
	if _, err := domain.NewTextField("MXN", "PRETTY_SURE"); err == nil {
		t.Error("NewTextField accepted an unknown confidence")
	}
	if _, err := domain.NewIntField(1, ""); err == nil {
		t.Error("NewIntField accepted an empty confidence")
	}
}

func TestClaimLifecycle(t *testing.T) {
	later := created.Add(time.Hour)

	t.Run("proposed to active to superseded", func(t *testing.T) {
		c := mustClaim(t, transferFields(t))

		active, err := c.Activate(later)
		if err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if active.State() != domain.ClaimActive || !active.UpdatedAt().Equal(later) {
			t.Errorf("active = %s at %v", active.State(), active.UpdatedAt())
		}
		if c.State() != domain.ClaimProposed {
			t.Error("Activate mutated the receiver; a Claim is a value")
		}

		old, err := active.SupersededByClaim("claim-2", later)
		if err != nil {
			t.Fatalf("SupersededByClaim: %v", err)
		}
		if old.State() != domain.ClaimSuperseded || old.SupersededBy() != "claim-2" {
			t.Errorf("superseded = %s by %q", old.State(), old.SupersededBy())
		}
		// Provenance and fields survive supersession: the whole point of
		// keeping the old Claim is that it stays readable.
		if len(old.EvidenceIDs()) != 1 || len(old.FieldNames()) != len(c.FieldNames()) {
			t.Error("supersession lost provenance or fields")
		}
	})

	t.Run("rejection from proposed and from active", func(t *testing.T) {
		c := mustClaim(t, transferFields(t))
		if _, err := c.Reject(later); err != nil {
			t.Errorf("reject a proposed claim: %v", err)
		}
		active, _ := c.Activate(later)
		if _, err := active.Reject(later); err != nil {
			t.Errorf("reject an active claim: %v", err)
		}
	})

	t.Run("terminal states are terminal", func(t *testing.T) {
		c := mustClaim(t, transferFields(t))
		active, _ := c.Activate(later)
		superseded, _ := active.SupersededByClaim("claim-2", later)
		rejected, _ := c.Reject(later)

		for _, tc := range []struct {
			name string
			err  error
		}{
			{"activate an active claim", second(active.Activate(later))},
			{"activate a superseded claim", second(superseded.Activate(later))},
			{"activate a rejected claim", second(rejected.Activate(later))},
			{"reject a superseded claim", second(superseded.Reject(later))},
			{"reject a rejected claim", second(rejected.Reject(later))},
			{"supersede a proposed claim", second(c.SupersededByClaim("x", later))},
			{"supersede a superseded claim", second(superseded.SupersededByClaim("x", later))},
			{"supersede a rejected claim", second(rejected.SupersededByClaim("x", later))},
		} {
			if tc.err == nil {
				t.Errorf("%s was allowed", tc.name)
			}
		}
	})

	t.Run("supersession needs another claim", func(t *testing.T) {
		active, _ := mustClaim(t, transferFields(t)).Activate(later)
		if _, err := active.SupersededByClaim("", later); err == nil {
			t.Error("superseded by nothing")
		}
		if _, err := active.SupersededByClaim(active.ID(), later); err == nil {
			t.Error("a claim superseded itself")
		}
	})

	t.Run("a state change cannot predate the claim", func(t *testing.T) {
		c := mustClaim(t, transferFields(t))
		if _, err := c.Activate(created.Add(-time.Second)); err == nil {
			t.Error("activated before it existed")
		}
		if _, err := c.Activate(time.Time{}); err == nil {
			t.Error("activated at the zero time")
		}
	})
}

// Provenance is copied on the way out, so a caller cannot rewrite what a
// recorded Claim rests on.
func TestProvenanceIsNotSharedWithCallers(t *testing.T) {
	c, err := domain.NewClaim("c1", domain.ClaimProposed,
		[]string{"e2", "e1", "e1"}, transferFields(t), created)
	if err != nil {
		t.Fatal(err)
	}

	ids := c.EvidenceIDs()
	if len(ids) != 2 || ids[0] != "e1" || ids[1] != "e2" {
		t.Fatalf("EvidenceIDs() = %v, want the duplicate collapsed and sorted", ids)
	}
	ids[0] = "tampered"
	if again := c.EvidenceIDs(); again[0] != "e1" {
		t.Error("a caller rewrote the claim's provenance")
	}
}

// --- helpers ----------------------------------------------------------------

func transferFields(t *testing.T) map[domain.FieldName]domain.ClaimField {
	t.Helper()
	return map[domain.FieldName]domain.ClaimField{
		domain.FieldAmountMinor:     mustInt(t, 100000, domain.High),
		domain.FieldCurrency:        mustText(t, "MXN", domain.High),
		domain.FieldDirection:       mustText(t, "OUTFLOW", domain.High),
		domain.FieldMerchant:        mustText(t, "Persona Dos", domain.Medium),
		domain.FieldFinancialStatus: mustText(t, "SETTLED", domain.Medium),
	}
}

func mustClaim(t *testing.T, fields map[domain.FieldName]domain.ClaimField) domain.Claim {
	t.Helper()
	c, err := domain.NewClaim("claim-1", domain.ClaimProposed, []string{"evidence-1"}, fields, created)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustInt(t *testing.T, v int64, c domain.Confidence) domain.ClaimField {
	t.Helper()
	f, err := domain.NewIntField(v, c)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustText(t *testing.T, v string, c domain.Confidence) domain.ClaimField {
	t.Helper()
	f, err := domain.NewTextField(v, c)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func second(_ domain.Claim, err error) error { return err }
