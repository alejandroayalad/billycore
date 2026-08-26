package domain

import "fmt"

// FinancialStatus is what Billy knows about where a movement stands
// (DOMAIN.md §3, §5).
//
// UNKNOWN is a real member here, unlike the absent direction in
// TransactionDirection, and the difference is deliberate: Evidence often does
// not reveal whether an event is an authorization or a settlement, so "Billy
// looked and cannot tell" is a fact worth recording rather than a gap.
//
// There is no REFUNDED. A refund is a second Transaction related to the first
// (DOMAIN.md §4), which keeps the original intact and lets a partial refund
// work the same way as a full one.
//
// There is no CONFIRMED either. DOMAIN.md §5 argues that Evidence provenance,
// active Claims, reconciliation state, financial status and confidence together
// say more than a boolean would.
type FinancialStatus string

const (
	StatusUnknown  FinancialStatus = "UNKNOWN"
	StatusPending  FinancialStatus = "PENDING"
	StatusSettled  FinancialStatus = "SETTLED"
	StatusReversed FinancialStatus = "REVERSED"
)

func (s FinancialStatus) Validate() error {
	switch s {
	case StatusUnknown, StatusPending, StatusSettled, StatusReversed:
		return nil
	default:
		return fmt.Errorf("financial status: %q is not a known status", string(s))
	}
}

func (s FinancialStatus) String() string { return string(s) }

// Confidence is how well Billy supports a value it is asserting
// (DOMAIN.md §7).
//
// Qualitative on purpose. A Claim-level 0.87342 is false precision and is not
// expressive enough besides: different fields of one interpretation carry
// different certainty, which is why confidence lives on the field.
//
// Confidence is not the same as absence. "account = ****1234, LOW" means Billy
// has a belief with weak support; "no account field at all" means Billy has no
// belief. Those must stay different, and they do, because a field Billy cannot
// support has no ClaimField rather than a low-confidence empty one.
type Confidence string

const (
	High   Confidence = "HIGH"
	Medium Confidence = "MEDIUM"
	Low    Confidence = "LOW"
)

func (c Confidence) Validate() error {
	switch c {
	case High, Medium, Low:
		return nil
	default:
		return fmt.Errorf("confidence: %q is not HIGH, MEDIUM or LOW", string(c))
	}
}

func (c Confidence) String() string { return string(c) }
