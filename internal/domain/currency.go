package domain

import "fmt"

// Currency is an ISO 4217 alphabetic code, such as MXN.
//
// BillyCore supports a fixed set of currencies and knows the minor-unit
// exponent of each (D50). A code outside the set is not a currency Billy can
// render, so Validate rejects it.
type Currency string

// supportedCurrencies maps each supported code to its ISO 4217 minor-unit
// exponent: the number of decimal places. MXN divides by 100.
//
// To support a currency, add it here with its exponent. Nothing else changes.
var supportedCurrencies = map[Currency]int{
	"MXN": 2,
}

// Validate reports if the code is a currency that BillyCore supports.
//
// The shape check comes first, so a malformed code reports its shape and not
// its absence from the table.
func (c Currency) Validate() error {
	if len(c) != 3 {
		return fmt.Errorf("currency: %q must be three letters", string(c))
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return fmt.Errorf("currency: %q must be uppercase A-Z", string(c))
		}
	}
	if _, ok := supportedCurrencies[c]; !ok {
		return fmt.Errorf("currency: %q is not a currency BillyCore supports", string(c))
	}
	return nil
}

// Exponent returns the number of decimal places of the currency, and if the
// currency is supported. A caller that holds a validated Money always gets
// true.
func (c Currency) Exponent() (int, bool) {
	exp, ok := supportedCurrencies[c]
	return exp, ok
}

func (c Currency) String() string { return string(c) }
