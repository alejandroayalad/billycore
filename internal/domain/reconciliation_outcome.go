package domain

import "fmt"

// ReconciliationOutcome is what BillyCore decided about a candidate pair
// (DOMAIN.md §6). It is the status a reconciliation_candidate records (D69).
//
// It is separate from ReconciliationState: the outcome is about one pair of
// observations; the state is about one Transaction. A MATCH produces a
// RECONCILED survivor, but the two words answer different questions.
type ReconciliationOutcome string

const (
	// Match means the observations are safely the same event, so they merge
	// (D70). AMBIGUOUS never reaches here: it is never auto-merged.
	Match ReconciliationOutcome = "MATCH"

	// NoMatch means a contradiction or enough evidence proves them different
	// events — opposite direction, or internal against external (D55).
	NoMatch ReconciliationOutcome = "NO_MATCH"

	// Ambiguous means BillyCore cannot safely decide. The pair stays separate
	// until new Evidence, BillyAgent, or a human resolves it (DOMAIN.md §6).
	Ambiguous ReconciliationOutcome = "AMBIGUOUS"
)

// Validate rejects an outcome that is not one of the three above.
func (o ReconciliationOutcome) Validate() error {
	switch o {
	case Match, NoMatch, Ambiguous:
		return nil
	default:
		return fmt.Errorf("reconciliation outcome: %q is not a known outcome", string(o))
	}
}

func (o ReconciliationOutcome) String() string { return string(o) }
