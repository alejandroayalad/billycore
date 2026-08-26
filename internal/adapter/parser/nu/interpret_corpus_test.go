package nu_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// TestInterpretCorpus builds a Claim from every stored artifact.
//
// Read-only, aggregate invariants, and a clean skip where the database is
// absent — the same contract as the two corpus tests beside it. It never
// writes: the Claims are constructed and thrown away, which is enough to prove
// that the 800 the parsers recognise all pass their own constructor.
//
// It is the test that would catch this slice being quietly wrong. A confidence
// applied to the wrong field, a merchant on a card payment, an occurred_at
// invented for an artifact that carried no date — none of those fail a unit
// test on five fixtures, and all of them show up as a count that moved here.
func TestInterpretCorpus(t *testing.T) {
	db := openCorpus(t)
	defer db.Close()

	rows, err := db.Query(`SELECT id, raw_content FROM evidence ORDER BY observed_at`)
	if err != nil {
		t.Fatalf("query corpus: %v", err)
	}
	defer rows.Close()

	var (
		artifacts, claims, none int
		withProvenance          int
		perField                = map[domain.FieldName]int{}
		perConfidence           = map[domain.Confidence]int{}
		at                      = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	)

	for rows.Next() {
		var evidenceID string
		var raw []byte
		if err := rows.Scan(&evidenceID, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		artifacts++

		fields, err := nu.Interpreter{}.Interpret(raw)
		if errors.Is(err, app.ErrNoInterpretation) {
			none++
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", evidenceID, err)
			continue
		}
		claims++

		// Every Claim is valid by its own constructor, and every one of them
		// carries provenance — the one invariant DOMAIN.md §4 calls hard.
		proposed, err := domain.NewClaim(evidenceID, domain.ClaimProposed, []string{evidenceID}, fields, at)
		if err != nil {
			t.Errorf("%s: not a valid claim: %v", evidenceID, err)
			continue
		}
		active, err := proposed.Activate(at)
		if err != nil {
			t.Errorf("%s: cannot activate: %v", evidenceID, err)
			continue
		}
		if provenance := active.EvidenceIDs(); len(provenance) == 1 && provenance[0] == evidenceID {
			withProvenance++
		} else {
			t.Errorf("%s: provenance = %v", evidenceID, provenance)
		}

		// Money is a pair, guaranteed by the constructor. Assert it anyway:
		// this is the number that ends up in the table.
		money, ok := active.Money()
		if !ok || money.Minor() <= 0 || money.Currency() != nu.Currency {
			t.Errorf("%s: money = %v (ok=%v)", evidenceID, money, ok)
		}

		for name := range fields {
			perField[name]++
			perConfidence[fields[name].Confidence()]++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate corpus: %v", err)
	}

	t.Logf("artifacts %d · claims %d · no interpretation %d", artifacts, claims, none)
	for _, name := range []domain.FieldName{
		domain.FieldAmountMinor, domain.FieldCurrency, domain.FieldDirection,
		domain.FieldMerchant, domain.FieldOccurredAt, domain.FieldFinancialStatus,
		domain.FieldTrackingKey, domain.FieldAccountIdentifier,
	} {
		t.Logf("  %-20s %4d", name, perField[name])
	}
	t.Logf("  confidence: HIGH %d · MEDIUM %d · LOW %d",
		perConfidence[domain.High], perConfidence[domain.Medium], perConfidence[domain.Low])

	// Measured 2026-08-25, and the counts this slice must not move.
	assertCount(t, "artifacts", artifacts, 1044)
	assertCount(t, "claims", claims, 800)
	assertCount(t, "artifacts producing no claim", none, 244)
	assertCount(t, "claims with provenance", withProvenance, 800)

	// Every Claim asserts an amount, a currency and a direction.
	assertCount(t, "amount_minor", perField[domain.FieldAmountMinor], 800)
	assertCount(t, "currency", perField[domain.FieldCurrency], 800)
	assertCount(t, "direction", perField[domain.FieldDirection], 800)

	// 710 artifacts carry a body timestamp; the 90 card payments do not, and
	// they have no row rather than a midnight (D33).
	assertCount(t, "occurred_at", perField[domain.FieldOccurredAt], 710)

	// Every template but the card payment names a counterparty: 800 - 90.
	assertCount(t, "merchant", perField[domain.FieldMerchant], 710)

	// D35 and D40: SETTLED on both transfer receipts — 354 outflows and 345
	// inflows — and on neither the card payment nor the service payment.
	assertCount(t, "financial_status", perField[domain.FieldFinancialStatus], 699)

	// D36: the tracking key rides on the rich outflow layout alone.
	assertCount(t, "tracking_key", perField[domain.FieldTrackingKey], 16)

	// Never, on any template (DOMAIN.md §6, CONTEXT.md §3.1).
	assertCount(t, "account_identifier", perField[domain.FieldAccountIdentifier], 0)

	// D34: currency is LOW on every Claim and nothing else is, so the count of
	// LOW fields is exactly the count of Claims.
	assertCount(t, "LOW fields", perConfidence[domain.Low], 800)
}
