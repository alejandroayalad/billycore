package parser

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// Errors from ParseMoney, split on a mechanical rule rather than a judgement:
// ErrUnsupportedFmt means the text has no decimal point, or does not carry
// exactly two digits after the first one; ErrNotMoney means everything else.
//
// The split earns its keep in one case, and it is the case that matters. An
// amount written without its cents — "$300" — is a template change worth
// noticing, not a line of prose that failed to be a number, and it arrives
// under its own error so a caller can tell the two apart.
var (
	ErrNotMoney       = errors.New("parser: not an amount")
	ErrUnsupportedFmt = errors.New("parser: unsupported amount format")
)

// ParseMoney converts a Nu amount such as "$1,000.00" into Money.
//
// The conversion is integer arithmetic end to end. Nothing here is ever a
// float: 899.93 * 100 is 89992.99999999999 in IEEE 754, and a cent lost in the
// hundredth artifact is exactly the kind of quiet wrongness this layer exists
// to prevent. The digits either side of the decimal point are read as separate
// integers and combined.
//
// The accepted shape, and only it:
//
//	$1,000.00   $999.00   $0.50
//
// An optional '$', digit groups separated by commas — the first of one to
// three digits, the rest of exactly three — then a '.' and exactly two
// fraction digits.
//
// # Why the fraction is mandatory
//
// Scaling "$300" to minor units requires knowing that MXN has two of them, and
// where that exponent comes from is API.md open question 8, which is open
// (D21: stop, do not code around). Requiring the two digits means the exponent
// is read off the text rather than assumed, so this function needs no currency
// table. Measured 2026-08-25, all 710 labelled `Monto:` values across the
// corpus carry exactly two fraction digits, so nothing Billy needs is refused
// by this rule today. If a template ever drops them, the answer is a decision
// about the exponent, not a looser parser.
//
// Currency is the caller's, because no Nu email states one: "MXN" and "pesos"
// appear zero times in 1,044 artifacts. Inferring it from the '$' sign is a
// judgement about the Source, and it belongs with the template parser that
// knows which Source it is reading — not here.
func ParseMoney(s string, currency domain.Currency) (domain.Money, error) {
	digits := strings.TrimSpace(s)
	digits = strings.TrimPrefix(digits, "$")
	digits = strings.TrimSpace(digits)
	if digits == "" {
		return domain.Money{}, fmt.Errorf("%w: %q", ErrNotMoney, s)
	}

	point := strings.IndexByte(digits, '.')
	if point < 0 {
		return domain.Money{}, fmt.Errorf("%w: %q has no fraction; see API.md Q8", ErrUnsupportedFmt, s)
	}
	whole, frac := digits[:point], digits[point+1:]

	if len(frac) != 2 || !allDigits(frac) {
		return domain.Money{}, fmt.Errorf("%w: %q must have exactly two fraction digits", ErrUnsupportedFmt, s)
	}

	units, err := parseGrouped(whole)
	if err != nil {
		return domain.Money{}, fmt.Errorf("%w: %q: %v", ErrNotMoney, s, err)
	}
	cents := int64(frac[0]-'0')*10 + int64(frac[1]-'0')

	if units > (math.MaxInt64-cents)/100 {
		return domain.Money{}, fmt.Errorf("%w: %q overflows int64 minor units", ErrUnsupportedFmt, s)
	}
	return domain.NewMoney(units*100+cents, currency)
}

// parseGrouped reads the integer part, enforcing comma grouping rather than
// merely tolerating it. "$1,23.00" is not a thousand-something with a stray
// comma; it is a shape Billy has not seen, and reading it as 123 would be a
// guess.
func parseGrouped(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("no integer part")
	}
	groups := strings.Split(s, ",")
	for i, g := range groups {
		switch {
		case !allDigits(g):
			return 0, fmt.Errorf("group %q is not digits", g)
		case i == 0 && (len(g) < 1 || len(g) > 3):
			return 0, fmt.Errorf("leading group %q must be one to three digits", g)
		case i > 0 && len(g) != 3:
			return 0, fmt.Errorf("group %q must be exactly three digits", g)
		}
	}

	var n int64
	for _, g := range groups {
		for i := 0; i < len(g); i++ {
			d := int64(g[i] - '0')
			if n > (math.MaxInt64-d)/10 {
				return 0, errors.New("integer part overflows int64")
			}
			n = n*10 + d
		}
	}
	return n, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
