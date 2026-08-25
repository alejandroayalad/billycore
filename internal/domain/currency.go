package domain

import "fmt"

// Currency is an ISO 4217 alphabetic code, such as MXN or USD.
//
// There is no currency table and no minor-unit exponent here. DATA_MODEL.md §9
// records that currency is stored as a code; API.md open question 8 has not yet
// decided where the exponent comes from. Validation is therefore shape only.
type Currency string

// Validate reports whether the code is three uppercase ASCII letters.
func (c Currency) Validate() error {
	if len(c) != 3 {
		return fmt.Errorf("currency: %q must be three letters", string(c))
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return fmt.Errorf("currency: %q must be uppercase A-Z", string(c))
		}
	}
	return nil
}

func (c Currency) String() string { return string(c) }
