package domain

import "fmt"

// TransactionDirection is which way money moved (DOMAIN.md §3).
//
// It exists because Money does not encode sign: Billy represents an 800 MXN
// payment as Money(800 MXN) plus OUTFLOW, never as Money(-800 MXN), so the same
// information is never stored twice and no amount can contradict its own
// direction.
//
// There is no UNKNOWN member. DATA_MODEL.md §2 forbids representing missing
// information as a fake value, so a direction Billy has not established is an
// absent one — the empty TransactionDirection, which Validate rejects.
type TransactionDirection string

const (
	Inflow  TransactionDirection = "INFLOW"
	Outflow TransactionDirection = "OUTFLOW"
)

func (d TransactionDirection) Validate() error {
	switch d {
	case Inflow, Outflow:
		return nil
	default:
		return fmt.Errorf("transaction direction: %q is neither INFLOW nor OUTFLOW", string(d))
	}
}

func (d TransactionDirection) String() string { return string(d) }
