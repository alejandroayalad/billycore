package parser_test

import (
	"errors"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const MXN = domain.Currency("MXN")

func TestParseMoneyAccepts(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"$1,000.00", 100000},   // CONTEXT.md §3.1's worked example
		{"$299.00", 29900},      // the inflow fixture
		{"$1,120.25", 112025},   // the card payment fixture
		{"$909.00", 90900},      // the service payment fixture
		{"$0.50", 50},           // a leading zero is a real amount
		{"$0.00", 0},            // and so is nothing at all
		{"$5.99", 599},          // no thousands separator
		{"$12,345.67", 1234567}, // two groups
		{"$1,234,567.89", 123456789},
		{"1,000.00", 100000},      // the dollar sign is optional
		{"  $1,000.00  ", 100000}, // callers slice on a label, so trim
		{"$ 1,000.00", 100000},
		{"$899.93", 89993}, // 899.93*100 is 89992.99999999999 as a float
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parser.ParseMoney(tt.in, MXN)
			if err != nil {
				t.Fatalf("ParseMoney(%q): %v", tt.in, err)
			}
			if got.Minor() != tt.want {
				t.Errorf("ParseMoney(%q) = %d minor, want %d", tt.in, got.Minor(), tt.want)
			}
			if got.Currency() != MXN {
				t.Errorf("ParseMoney(%q) currency = %q, want MXN", tt.in, got.Currency())
			}
		})
	}
}

func TestParseMoneyRefuses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		is   error
	}{
		{"empty", "", parser.ErrNotMoney},
		{"sign only", "$", parser.ErrNotMoney},
		{"prose", "Completada", parser.ErrUnsupportedFmt},
		{"no fraction, needs API.md Q8", "$300", parser.ErrUnsupportedFmt},
		{"no fraction, zero", "$0", parser.ErrUnsupportedFmt},
		{"one fraction digit", "$1.5", parser.ErrUnsupportedFmt},
		{"three fraction digits", "$1.500", parser.ErrUnsupportedFmt},
		{"trailing sentence period", "$50.00.", parser.ErrUnsupportedFmt},
		{"two decimal points", "$1.000.00", parser.ErrUnsupportedFmt},
		{"mis-grouped", "$1,23.00", parser.ErrNotMoney},
		{"four-digit group", "$1,2345.00", parser.ErrNotMoney},
		{"leading comma", "$,100.00", parser.ErrNotMoney},
		{"trailing comma before point", "$1,.00", parser.ErrNotMoney},
		{"negative", "-$1.00", parser.ErrNotMoney},
		{"amount with trailing text", "$1.00 MXN", parser.ErrUnsupportedFmt},
		{"letters in a group", "$1,O00.00", parser.ErrNotMoney},
		{"overflowing integer part", "$99,999,999,999,999,999,999.99", parser.ErrNotMoney},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.ParseMoney(tt.in, MXN)
			if err == nil {
				t.Fatalf("ParseMoney(%q) = %v, want an error", tt.in, got)
			}
			if !errors.Is(err, tt.is) {
				t.Errorf("ParseMoney(%q) error = %v, want %v", tt.in, err, tt.is)
			}
		})
	}
}

// A Money with no currency is not a Money. The domain constructor already says
// so; this pins that ParseMoney routes through it rather than around it.
func TestParseMoneyRequiresACurrency(t *testing.T) {
	if _, err := parser.ParseMoney("$1.00", domain.Currency("")); err == nil {
		t.Fatal("ParseMoney accepted an empty currency")
	}
	if _, err := parser.ParseMoney("$1.00", domain.Currency("mxn")); err == nil {
		t.Fatal("ParseMoney accepted a lowercase currency code")
	}
}
