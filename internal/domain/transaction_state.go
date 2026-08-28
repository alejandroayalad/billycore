package domain

import "fmt"

// TransactionState tells you if this Transaction is Billy's current record of
// the event (D49). It is separate from ReconciliationState and from
// FinancialStatus, because each column answers one question. See D49 for why
// SUPERSEDED is not a reconciliation state.
type TransactionState string

const (
	// TransactionActive is Billy's current record of the event. Every
	// Transaction starts here, and only ACTIVE rows are shown.
	TransactionActive TransactionState = "ACTIVE"

	// TransactionSuperseded means a better interpretation of the same Evidence
	// replaced the Claim that this Transaction comes from (D47, D48). Billy
	// keeps the row (DATA_MODEL.md §6).
	TransactionSuperseded TransactionState = "SUPERSEDED"
)

// Validate rejects a state that is not in the list above. There is no UNKNOWN
// member: a Transaction is current, or it is replaced.
func (s TransactionState) Validate() error {
	switch s {
	case TransactionActive, TransactionSuperseded:
		return nil
	default:
		return fmt.Errorf("transaction state: %q is not a known state", string(s))
	}
}

func (s TransactionState) String() string { return string(s) }
