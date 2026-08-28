package domain

import "testing"

func TestNewMoneyRejectsNegative(t *testing.T) {
	if _, err := NewMoney(-1, "MXN"); err == nil {
		t.Fatal("negative amount was accepted; direction carries sign (DOMAIN.md §3)")
	}
}

func TestNewMoneyRejectsBadCurrency(t *testing.T) {
	for _, code := range []Currency{"", "mx", "MXNN", "mxn", "M2N"} {
		if _, err := NewMoney(100, code); err == nil {
			t.Errorf("currency %q was accepted", code)
		}
	}
}

func TestNewMoneyAccepts(t *testing.T) {
	m, err := NewMoney(89993, "MXN")
	if err != nil {
		t.Fatalf("valid Money rejected: %v", err)
	}
	if m.Minor() != 89993 || m.Currency() != "MXN" {
		t.Fatalf("got %s, want 89993 MXN", m)
	}
}

func TestAddRejectsMixedCurrency(t *testing.T) {
	mxn, _ := NewMoney(100, "MXN")
	// Built directly, because NewMoney refuses USD: BillyCore supports one
	// currency today (D50). The invariant is about two currencies, not about
	// which two, so it must stay tested while the table holds one entry.
	usd := Money{minor: 100, currency: "USD"}
	if _, err := mxn.Add(usd); err == nil {
		t.Fatal("added USD to MXN")
	}
}

func TestAddSameCurrency(t *testing.T) {
	a, _ := NewMoney(100, "MXN")
	b, _ := NewMoney(250, "MXN")
	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want, _ := NewMoney(350, "MXN"); !sum.Equal(want) {
		t.Fatalf("got %s, want %s", sum, want)
	}
}

func TestStringRendersTheRawMinorAmount(t *testing.T) {
	m, _ := NewMoney(89993, "MXN")
	if got := m.String(); got != "89993 MXN" {
		t.Fatalf("got %q, want the exact stored integer for a log", got)
	}
}

// D50 — the exponent is known now, so a person can read the amount.
func TestDecimalRendersWithTheCurrencyExponent(t *testing.T) {
	for _, tc := range []struct {
		minor int64
		want  string
	}{
		{89993, "899.93 MXN"},
		{100000, "1000.00 MXN"},
		{5, "0.05 MXN"},
		{1, "0.01 MXN"},
		{0, "0.00 MXN"},
	} {
		m, err := NewMoney(tc.minor, "MXN")
		if err != nil {
			t.Fatalf("NewMoney(%d): %v", tc.minor, err)
		}
		if got := m.Decimal(); got != tc.want {
			t.Errorf("Decimal(%d) = %q, want %q", tc.minor, got, tc.want)
		}
	}
}

// The set is closed (D50). A currency Billy cannot render must not reach a
// column, because a stored amount nothing can display is a fact Billy cannot
// answer for.
func TestCurrencyIsAClosedSet(t *testing.T) {
	if err := Currency("MXN").Validate(); err != nil {
		t.Errorf("MXN was rejected: %v", err)
	}
	for _, code := range []Currency{"USD", "EUR", "JPY", "XXX"} {
		if err := code.Validate(); err == nil {
			t.Errorf("%s was accepted; BillyCore does not support it", code)
		}
	}
	// A malformed code reports its shape, not its absence from the table.
	for _, code := range []Currency{"", "mx", "mxn", "MXNN"} {
		if err := code.Validate(); err == nil {
			t.Errorf("%q was accepted as a currency", code)
		}
	}
	if _, ok := Currency("MXN").Exponent(); !ok {
		t.Error("MXN has no exponent")
	}
	if _, ok := Currency("USD").Exponent(); ok {
		t.Error("an unsupported currency reported an exponent")
	}
}
