package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrTransactionNoID         = errors.New("transaction: id is required")
	ErrTransactionNoProvenance = errors.New("transaction: a transaction without provenance to Evidence is invalid")
	ErrTransactionNoOccurredAt = errors.New("transaction: occurred_at is required")
)

// TransactionDraft is the raw material NewTransaction validates.
//
// A struct rather than eleven positional parameters, because three of them are
// optional and four are strings: `NewTransaction(id, money, "", "", ...)` is a
// call nobody can read and everybody can transpose. The field names are the
// documentation, and a caller that forgets one gets a zero value the
// constructor rejects rather than a neighbour's value silently shifted along.
type TransactionDraft struct {
	ID string

	// Money is optional, and its absence is the zero Money — a value with no
	// currency. A Transaction may exist without an amount where the Evidence
	// does not support one (DATA_MODEL.md §4.5); it may never exist with an
	// amount that has lost its currency (DOMAIN.md §4).
	Money Money

	// Merchant is the counterparty descriptor exactly as the artifact gave it.
	// Empty means Billy read no counterparty, not a counterparty named nothing.
	Merchant string

	// AccountIdentifier is the account or card the Evidence referenced. Empty
	// means absent. Nothing populates it today: no Nu template states the
	// user's own account, and the `Tarjeta de débito: ••••7662` line is the
	// counterparty's (CONTEXT.md §3.1).
	AccountIdentifier string

	Direction           TransactionDirection
	FinancialStatus     FinancialStatus
	ReconciliationState ReconciliationState

	// State tells you if this is Billy's current record of the event (D49).
	// The pipeline builds every Transaction as ACTIVE. It is required, so a
	// caller that forgets it gets an error here.
	State TransactionState

	// OccurredAt is when the event happened, and it is required. Where the
	// artifact stated no time, the caller has already substituted the earliest
	// observed_at of the supporting Evidence — DATA_MODEL.md §4.5's fallback,
	// computed before the write. The domain does not apply it: a Transaction
	// cannot see Evidence timestamps, and a constructor that invented a time
	// would be the one place a fabricated fact could enter unchallenged.
	OccurredAt time.Time

	// EvidenceIDs is the provenance. At least one is required.
	EvidenceIDs []string

	CreatedAt time.Time
}

// Transaction is Billy's current representation of a financial event
// (DOMAIN.md §1, §3).
//
// It is an aggregate root, independent of Evidence and Claim. It has identity
// because Billy tracks the same financial event over time as more Evidence
// arrives — a pending authorization and a later settlement are one Transaction,
// not two.
//
// Its one hard invariant is provenance: every Transaction traces back to at
// least one piece of Evidence, and one without it is invalid (DOMAIN.md §4).
// That is the difference between a number Billy can answer for and a number
// Billy made up.
//
// Values, never edits in place — the same discipline Claim keeps.
type Transaction struct {
	id                  string
	money               Money
	hasMoney            bool
	merchant            string
	accountIdentifier   string
	direction           TransactionDirection
	financialStatus     FinancialStatus
	reconciliationState ReconciliationState
	state               TransactionState
	occurredAt          time.Time
	evidenceIDs         []string
	createdAt           time.Time
	updatedAt           time.Time
}

// NewTransaction records a financial event, enforcing every invariant
// DOMAIN.md §4 puts on the Transaction aggregate:
//
//   - Money uses integer minor units — Money has no other representation.
//   - Currency is required wherever Money exists.
//   - Direction is explicit. There is no UNKNOWN direction.
//   - Money is unsigned. Direction carries the sign.
//   - Unsupported financial facts are forbidden.
//   - Provenance to at least one piece of Evidence.
//
// This is the one place "is this a valid Transaction?" is answered, whether it
// was built from a Claim by the reconciliation pass or arrives some other way
// later (D6: invariants live in domain constructors, not in handlers and not in
// database constraints).
//
// The id and the timestamps are supplied rather than generated, for the reason
// NewClaim gives: a UUID reads from the operating system and a clock is I/O,
// and the domain performs neither.
func NewTransaction(d TransactionDraft) (Transaction, error) {
	if d.ID == "" {
		return Transaction{}, ErrTransactionNoID
	}

	money, hasMoney, err := validateDraftMoney(d.Money)
	if err != nil {
		return Transaction{}, err
	}

	// Direction is not defaulted. TransactionDirection has no UNKNOWN member
	// on purpose, and money that moved without Billy knowing which way is not
	// a smaller fact — it is an unsupported one.
	if err := d.Direction.Validate(); err != nil {
		return Transaction{}, err
	}
	// FinancialStatus, by contrast, does have UNKNOWN, and the caller is
	// expected to pass it where the Claim asserted none (D43). It is still
	// validated rather than defaulted here: silently turning the empty string
	// into UNKNOWN would let a caller that forgot the field look exactly like
	// one that read the artifact and could not tell.
	if err := d.FinancialStatus.Validate(); err != nil {
		return Transaction{}, err
	}
	if err := d.ReconciliationState.Validate(); err != nil {
		return Transaction{}, err
	}
	// There is no default, for the same reason that FinancialStatus has none:
	// a zero value that becomes a real state hides a caller that forgot it.
	if err := d.State.Validate(); err != nil {
		return Transaction{}, err
	}

	if d.OccurredAt.IsZero() {
		return Transaction{}, ErrTransactionNoOccurredAt
	}
	if d.CreatedAt.IsZero() {
		return Transaction{}, errors.New("transaction: created_at is required")
	}

	provenance, err := cleanTransactionProvenance(d.EvidenceIDs)
	if err != nil {
		return Transaction{}, err
	}

	return Transaction{
		id:                  d.ID,
		money:               money,
		hasMoney:            hasMoney,
		merchant:            d.Merchant,
		accountIdentifier:   d.AccountIdentifier,
		direction:           d.Direction,
		financialStatus:     d.FinancialStatus,
		reconciliationState: d.ReconciliationState,
		state:               d.State,
		occurredAt:          d.OccurredAt.UTC(),
		evidenceIDs:         provenance,
		createdAt:           d.CreatedAt.UTC(),
		updatedAt:           d.CreatedAt.UTC(),
	}, nil
}

// validateDraftMoney separates "no amount" from "an amount", and rejects the
// third thing a struct literal can express: half of one.
//
// Money's own constructor guarantees the pair, but a zero-valued struct field
// bypasses it — `Money{minor: 800}` is writable inside this package and carries
// an amount with no currency. DATA_MODEL.md §4.5 is explicit that where
// amount_minor exists, currency must too, so that shape is caught here rather
// than reaching a column.
func validateDraftMoney(m Money) (Money, bool, error) {
	if m.Currency() == "" {
		if m.Minor() != 0 {
			return Money{}, false, errors.New("transaction: an amount without a currency is not an amount")
		}
		return Money{}, false, nil
	}
	// Re-run the constructor rather than trusting the value: it is the only
	// thing that rejects a negative amount, and a struct literal can hold one.
	validated, err := NewMoney(m.Minor(), m.Currency())
	if err != nil {
		return Money{}, false, fmt.Errorf("transaction: %w", err)
	}
	return validated, true, nil
}

func cleanTransactionProvenance(ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, ErrTransactionNoProvenance
		}
		if seen[id] {
			continue // the same artifact cited twice supports a Transaction once
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, ErrTransactionNoProvenance
	}
	sort.Strings(out)
	return out, nil
}

func (t Transaction) ID() string                               { return t.id }
func (t Transaction) Merchant() string                         { return t.merchant }
func (t Transaction) AccountIdentifier() string                { return t.accountIdentifier }
func (t Transaction) Direction() TransactionDirection          { return t.direction }
func (t Transaction) FinancialStatus() FinancialStatus         { return t.financialStatus }
func (t Transaction) ReconciliationState() ReconciliationState { return t.reconciliationState }
func (t Transaction) State() TransactionState                  { return t.state }
func (t Transaction) OccurredAt() time.Time                    { return t.occurredAt }
func (t Transaction) CreatedAt() time.Time                     { return t.createdAt }
func (t Transaction) UpdatedAt() time.Time                     { return t.updatedAt }

// Money returns the amount, and whether the Transaction carries one at all.
//
// The boolean is not decoration. A Transaction with no amount and a Transaction
// of zero are different facts, and DATA_MODEL.md §2 forbids collapsing the
// first into the second.
func (t Transaction) Money() (Money, bool) { return t.money, t.hasMoney }

// EvidenceIDs returns a copy, sorted. Handing out the slice would let a caller
// rewrite a recorded Transaction's provenance through the returned reference.
func (t Transaction) EvidenceIDs() []string {
	out := make([]string, len(t.evidenceIDs))
	copy(out, t.evidenceIDs)
	return out
}
