package domain

import "fmt"

// ReconciliationState is whether Billy has decided this Transaction stands
// alone (DOMAIN.md §5).
//
// It is kept deliberately separate from FinancialStatus. One answers "have two
// observations been determined to be the same event?"; the other answers "what
// happened to the money?". A single column carrying both would make
// `SETTLED` and `RECONCILED` alternatives to one another, which they are not —
// a settled payment may be unreconciled, and a reconciled one may still be
// pending.
//
// There is no UNKNOWN member. Every Transaction Billy writes has been through
// reconciliation or has not, and the one that has not is UNRECONCILED — a
// definite state rather than a gap.
type ReconciliationState string

const (
	// Unreconciled is where every Transaction is born: it exists, and nothing
	// has yet been determined about whether some other observation describes
	// the same event.
	Unreconciled ReconciliationState = "UNRECONCILED"

	// Reconciled means two observations were determined to be one event
	// (DOMAIN.md §6).
	Reconciled ReconciliationState = "RECONCILED"

	// Conflicted means the Evidence disagrees with itself. What puts a
	// Transaction here and how it leaves is DOMAIN.md open question 18, and
	// nothing sets it yet.
	Conflicted ReconciliationState = "CONFLICTED"
)

func (s ReconciliationState) Validate() error {
	switch s {
	case Unreconciled, Reconciled, Conflicted:
		return nil
	default:
		return fmt.Errorf("reconciliation state: %q is not a known state", string(s))
	}
}

func (s ReconciliationState) String() string { return string(s) }
