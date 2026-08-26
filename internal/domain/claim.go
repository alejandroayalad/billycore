package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ClaimState is where a Claim stands in its lifecycle (DOMAIN.md §5).
//
// ACTIVE does not mean objectively true. It means this is the interpretation
// Billy currently uses, which is a weaker and more honest thing to say.
type ClaimState string

const (
	ClaimProposed   ClaimState = "PROPOSED"
	ClaimActive     ClaimState = "ACTIVE"
	ClaimSuperseded ClaimState = "SUPERSEDED"
	ClaimRejected   ClaimState = "REJECTED"
)

func (s ClaimState) Validate() error {
	switch s {
	case ClaimProposed, ClaimActive, ClaimSuperseded, ClaimRejected:
		return nil
	default:
		return fmt.Errorf("claim state: %q is not a known state", string(s))
	}
}

func (s ClaimState) String() string { return string(s) }

// FieldName names one interpreted value inside a Claim.
//
// The set is closed, and it is the vocabulary DATA_MODEL.md §4.4 writes down.
// A closed set means a typo is a rejected Claim rather than a column nothing
// ever reads, and it means the answer to "what can Billy claim about a
// movement?" is in one place.
//
// It is deliberately *not* extended here to cover the identifiers Nu's
// templates carry — folio, clave de rastreo, concepto — or the event time. Each
// of those is a vocabulary decision, and inventing one in passing inside an
// implementation is the exact thing D21 exists to prevent.
type FieldName string

const (
	FieldAmountMinor       FieldName = "amount_minor"
	FieldCurrency          FieldName = "currency"
	FieldMerchant          FieldName = "merchant"
	FieldAccountIdentifier FieldName = "account_identifier"
	FieldDirection         FieldName = "direction"
	FieldFinancialStatus   FieldName = "financial_status"
)

// fieldIsInt records the physical shape of each field, mirroring the
// value_int / value_text split in DATA_MODEL.md §4.4. Storing an amount as text
// would satisfy that table's CHECK constraint and still be wrong.
var fieldIsInt = map[FieldName]bool{
	FieldAmountMinor:       true,
	FieldCurrency:          false,
	FieldMerchant:          false,
	FieldAccountIdentifier: false,
	FieldDirection:         false,
	FieldFinancialStatus:   false,
}

func (n FieldName) Validate() error {
	if _, known := fieldIsInt[n]; !known {
		return fmt.Errorf("claim field: %q is not a field Billy claims", string(n))
	}
	return nil
}

func (n FieldName) String() string { return string(n) }

// ClaimField is one interpreted value and how well Billy supports it.
//
// A field holds an integer or a text value, never both and never neither —
// the same exclusivity DATA_MODEL.md §4.4 enforces physically, enforced here
// where it means something.
type ClaimField struct {
	isInt      bool
	intValue   int64
	textValue  string
	confidence Confidence
}

// NewIntField builds an integer-valued field, such as amount_minor.
func NewIntField(v int64, c Confidence) (ClaimField, error) {
	if err := c.Validate(); err != nil {
		return ClaimField{}, err
	}
	return ClaimField{isInt: true, intValue: v, confidence: c}, nil
}

// NewTextField builds a text-valued field.
//
// An empty string is rejected. DATA_MODEL.md §2 forbids representing missing
// information as a fake value, and "" is the most convincing fake there is: a
// merchant Billy could not read must have no field at all, so that "no
// merchant" stays distinguishable from "a merchant named nothing".
func NewTextField(v string, c Confidence) (ClaimField, error) {
	if err := c.Validate(); err != nil {
		return ClaimField{}, err
	}
	if v == "" {
		return ClaimField{}, errors.New("claim field: an empty text value is absence, not a value")
	}
	return ClaimField{isInt: false, textValue: v, confidence: c}, nil
}

func (f ClaimField) IsInt() bool            { return f.isInt }
func (f ClaimField) Int() int64             { return f.intValue }
func (f ClaimField) Text() string           { return f.textValue }
func (f ClaimField) Confidence() Confidence { return f.confidence }

var (
	ErrClaimNoID         = errors.New("claim: id is required")
	ErrClaimNoProvenance = errors.New("claim: a claim without provenance to Evidence is invalid")
	ErrClaimNoFields     = errors.New("claim: a claim that asserts nothing is not a claim")
)

// Claim is an interpretation derived from Evidence (DOMAIN.md §1).
//
// **A Claim is not financial fact.** It may be incomplete, uncertain,
// superseded or rejected, and every one of those is an ordinary outcome rather
// than an error.
//
// Its one hard invariant is provenance: every Claim traces back to the Evidence
// it came from, and a Claim without it is invalid (DOMAIN.md §4). That is what
// makes a wrong number in a table answerable — the artifact that produced it is
// still there, byte for byte, and still immutable.
//
// Claims evolve by replacement, never by edit. Every state change returns a new
// Claim value; historical Claims are preserved.
type Claim struct {
	id           string
	state        ClaimState
	supersededBy string
	evidenceIDs  []string
	fields       map[FieldName]ClaimField
	createdAt    time.Time
	updatedAt    time.Time
}

// NewClaim records an interpretation, enforcing every invariant a Claim carries.
//
// This is the one place "is this a valid Claim?" is answered, whether the Claim
// came from a template parser or arrived over the wire at POST /v1/claims
// (D11, D12). An HTTP handler that validated separately would be a second
// answer to the same question, free to drift from this one.
//
// The id and the timestamps are supplied rather than generated: a UUID reads
// from the operating system and a clock is I/O, and the domain performs
// neither (D6).
func NewClaim(
	id string,
	state ClaimState,
	evidenceIDs []string,
	fields map[FieldName]ClaimField,
	createdAt time.Time,
) (Claim, error) {
	if id == "" {
		return Claim{}, ErrClaimNoID
	}
	if err := state.Validate(); err != nil {
		return Claim{}, err
	}
	// A Claim may not begin life superseded: something has to have replaced it,
	// and at construction nothing has.
	if state == ClaimSuperseded {
		return Claim{}, errors.New("claim: a claim is superseded by another claim, never born that way")
	}
	if createdAt.IsZero() {
		return Claim{}, errors.New("claim: created_at is required")
	}

	provenance, err := cleanProvenance(evidenceIDs)
	if err != nil {
		return Claim{}, err
	}
	copied, err := validateFields(fields)
	if err != nil {
		return Claim{}, err
	}

	return Claim{
		id:          id,
		state:       state,
		evidenceIDs: provenance,
		fields:      copied,
		createdAt:   createdAt.UTC(),
		updatedAt:   createdAt.UTC(),
	}, nil
}

func cleanProvenance(ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, ErrClaimNoProvenance
		}
		if seen[id] {
			continue // the same artifact cited twice supports a Claim once
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, ErrClaimNoProvenance
	}
	sort.Strings(out)
	return out, nil
}

func validateFields(fields map[FieldName]ClaimField) (map[FieldName]ClaimField, error) {
	if len(fields) == 0 {
		return nil, ErrClaimNoFields
	}

	out := make(map[FieldName]ClaimField, len(fields))
	for name, field := range fields {
		if err := name.Validate(); err != nil {
			return nil, err
		}
		if err := field.confidence.Validate(); err != nil {
			return nil, fmt.Errorf("claim field %q: %w", name, err)
		}
		if field.isInt != fieldIsInt[name] {
			return nil, fmt.Errorf("claim field %q: wrong value shape; it is %s",
				name, valueShape(fieldIsInt[name]))
		}
		if !field.isInt && field.textValue == "" {
			return nil, fmt.Errorf("claim field %q: an empty text value is absence, not a value", name)
		}
		if err := validateFieldValue(name, field); err != nil {
			return nil, err
		}
		out[name] = field
	}

	// Money is a pair. DOMAIN.md §4: currency is required wherever Money
	// exists, and an amount without one is not a smaller fact — it is a
	// meaningless one.
	_, hasAmount := out[FieldAmountMinor]
	_, hasCurrency := out[FieldCurrency]
	if hasAmount && !hasCurrency {
		return nil, errors.New("claim: amount_minor without currency is not an amount")
	}

	return out, nil
}

// validateFieldValue applies each field's own domain rules, so that an
// interpretation cannot enter Billy claiming a direction of "SIDEWAYS".
func validateFieldValue(name FieldName, f ClaimField) error {
	switch name {
	case FieldAmountMinor:
		if f.intValue < 0 {
			return fmt.Errorf("claim field %q: %w", name, ErrNegativeAmount)
		}
	case FieldCurrency:
		if err := Currency(f.textValue).Validate(); err != nil {
			return fmt.Errorf("claim field %q: %w", name, err)
		}
	case FieldDirection:
		if err := TransactionDirection(f.textValue).Validate(); err != nil {
			return fmt.Errorf("claim field %q: %w", name, err)
		}
	case FieldFinancialStatus:
		if err := FinancialStatus(f.textValue).Validate(); err != nil {
			return fmt.Errorf("claim field %q: %w", name, err)
		}
	}
	return nil
}

func valueShape(isInt bool) string {
	if isInt {
		return "an integer field"
	}
	return "a text field"
}

func (c Claim) ID() string           { return c.id }
func (c Claim) State() ClaimState    { return c.state }
func (c Claim) CreatedAt() time.Time { return c.createdAt }
func (c Claim) UpdatedAt() time.Time { return c.updatedAt }

// SupersededBy is the id of the Claim that replaced this one, empty unless the
// state is SUPERSEDED.
func (c Claim) SupersededBy() string { return c.supersededBy }

// EvidenceIDs returns a copy, sorted. Handing out the slice would let a caller
// rewrite a recorded Claim's provenance through the returned reference.
func (c Claim) EvidenceIDs() []string {
	out := make([]string, len(c.evidenceIDs))
	copy(out, c.evidenceIDs)
	return out
}

// Field returns one interpreted value. The boolean is the difference between
// "Billy has no belief about this" and "Billy believes it weakly" — see
// DOMAIN.md §7, which insists those stay distinguishable.
func (c Claim) Field(name FieldName) (ClaimField, bool) {
	f, ok := c.fields[name]
	return f, ok
}

// FieldNames returns the fields this Claim asserts, sorted.
func (c Claim) FieldNames() []FieldName {
	out := make([]FieldName, 0, len(c.fields))
	for name := range c.fields {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Money assembles the amount and currency fields into Money, reporting whether
// the Claim asserts one at all. The pair is guaranteed by the constructor, so a
// Claim can never yield an amount that has lost its currency.
func (c Claim) Money() (Money, bool) {
	amount, hasAmount := c.fields[FieldAmountMinor]
	currency, hasCurrency := c.fields[FieldCurrency]
	if !hasAmount || !hasCurrency {
		return Money{}, false
	}
	money, err := NewMoney(amount.intValue, Currency(currency.textValue))
	if err != nil {
		return Money{}, false // unreachable: the constructor validated both
	}
	return money, true
}

// --- lifecycle --------------------------------------------------------------
//
// DOMAIN.md §5's states, and the transitions between them:
//
//	PROPOSED ──▶ ACTIVE ──▶ SUPERSEDED
//	    │           │
//	    └──▶ REJECTED ◀┘
//
// SUPERSEDED and REJECTED are terminal. A Claim is never deleted and never
// edited in place, so every method here returns a new value.

// Activate makes this Billy's usable interpretation.
func (c Claim) Activate(at time.Time) (Claim, error) {
	if c.state != ClaimProposed {
		return Claim{}, fmt.Errorf("claim: cannot activate a %s claim", c.state)
	}
	return c.transition(ClaimActive, "", at)
}

// Reject records that an interpretation turned out not to be usable. It is
// available from PROPOSED and from ACTIVE, because Billy may have been using an
// interpretation before discovering it was wrong.
func (c Claim) Reject(at time.Time) (Claim, error) {
	if c.state != ClaimProposed && c.state != ClaimActive {
		return Claim{}, fmt.Errorf("claim: cannot reject a %s claim", c.state)
	}
	return c.transition(ClaimRejected, "", at)
}

// SupersededByClaim replaces this Claim with a better one, keeping the pointer
// from old to new that DATA_MODEL.md §4.2 stores as superseded_by_claim_id.
//
// Only an ACTIVE Claim can be superseded: a PROPOSED one was never used, and
// replacing it is not a correction to Billy's history.
func (c Claim) SupersededByClaim(newClaimID string, at time.Time) (Claim, error) {
	if c.state != ClaimActive {
		return Claim{}, fmt.Errorf("claim: cannot supersede a %s claim", c.state)
	}
	if newClaimID == "" {
		return Claim{}, errors.New("claim: superseding claim id is required")
	}
	if newClaimID == c.id {
		return Claim{}, errors.New("claim: a claim cannot supersede itself")
	}
	return c.transition(ClaimSuperseded, newClaimID, at)
}

func (c Claim) transition(to ClaimState, supersededBy string, at time.Time) (Claim, error) {
	if at.IsZero() {
		return Claim{}, errors.New("claim: a state change needs the time it happened")
	}
	if at.Before(c.createdAt) {
		return Claim{}, errors.New("claim: a state change cannot predate the claim")
	}
	next := c
	next.state = to
	next.supersededBy = supersededBy
	next.updatedAt = at.UTC()
	next.evidenceIDs = c.EvidenceIDs()
	next.fields = make(map[FieldName]ClaimField, len(c.fields))
	for k, v := range c.fields {
		next.fields[k] = v
	}
	return next, nil
}
