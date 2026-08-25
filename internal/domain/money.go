package domain

import (
	"errors"
	"fmt"
)

// Money is an amount in a currency's minor unit.
//
// Never a float, never signed. Direction carries sign and lives on the
// Transaction, not here. See DOMAIN.md §3.
type Money struct {
	minor    int64
	currency Currency
}

var ErrNegativeAmount = errors.New("money: amount must be non-negative; direction carries sign")

// NewMoney builds a Money from a non-negative amount in minor units.
func NewMoney(minor int64, currency Currency) (Money, error) {
	if minor < 0 {
		return Money{}, ErrNegativeAmount
	}
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: currency}, nil
}

// Minor returns the amount in the currency's minor unit.
func (m Money) Minor() int64 { return m.minor }

// Currency returns the currency code.
func (m Money) Currency() Currency { return m.currency }

// IsZero reports whether the amount is zero. A zero Money is also the type's
// zero value, which carries no currency.
func (m Money) IsZero() bool { return m.minor == 0 }

// Equal reports whether two amounts are the same amount in the same currency.
func (m Money) Equal(other Money) bool {
	return m.minor == other.minor && m.currency == other.currency
}

// Add returns the sum. Amounts in different currencies do not add.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("money: cannot add %s to %s", other.currency, m.currency)
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

// String renders the raw minor amount and the code — never a decimal.
//
// Rendering a decimal needs the currency's minor-unit exponent, and where that
// table comes from is API.md open question 8. Until it is answered, nothing in
// the domain pretends to know it.
func (m Money) String() string { return fmt.Sprintf("%d %s", m.minor, m.currency) }
