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
	usd, _ := NewMoney(100, "USD")
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

func TestStringDoesNotRenderDecimal(t *testing.T) {
	m, _ := NewMoney(89993, "MXN")
	if got := m.String(); got != "89993 MXN" {
		t.Fatalf("got %q; the minor-unit exponent is API.md open question 8", got)
	}
}
