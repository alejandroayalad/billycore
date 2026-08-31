package nustatement_test

import (
	"errors"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/nustatement"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// The descriptor shapes read a row from its description column. Direction comes
// from the sign, the party is MEDIUM because only the column places it (D58),
// and a no-party row names neither a merchant nor a counterparty.
//
// The redacted fixtures do not carry these Spanish words, so the test is
// synthetic and the real statements are validated separately. The counts held:
// READ 422 to 463 across the three statements.
func TestDescriptorShapes(t *testing.T) {
	cases := []struct {
		name         string
		description  string
		amount       string
		direction    string
		minor        int64
		merchant     string
		counterparty string
		internal     bool
	}{
		{"card payment", "Pago a tu tarjeta de crédito Nu", "-$379.00", "OUTFLOW", 37900, "", "", false},
		{"devolución", "KSK*VID ONLYFANS Devolución", "+$20.00", "INFLOW", 2000, "KSK*VID ONLYFANS", "", false},
		{"ajuste", "APPLE.COM/BILL Ajuste realizado", "+$109.00", "INFLOW", 10900, "APPLE.COM/BILL", "", false},
		{"bonificación", "Bonificación por beneficio de Nu", "+$38.28", "INFLOW", 3828, "", "", false},
		{"compensación", "Compensación de retraso SPEI", "+$0.01", "INFLOW", 1, "", "", false},
		{"pago de servicio", "Pago de servicio - Totalplay", "-$909.00", "OUTFLOW", 90900, "Totalplay", "", false},
		{"cajero", "Cajero BANCOMER S.A. Retiro de efectivo", "-$338.28", "OUTFLOW", 33828, "", "BANCOMER S.A.", false},
		{"punto de venta", "Depósito en punto de venta", "+$483.00", "INFLOW", 48300, "", "", false},
		{"descongelamos", "Descongelamos saldo de tu Cajita: pau", "+$6,007.91", "INFLOW", 600791, "", domain.CounterpartySelf, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reading := interpret(t, statement([3]string{"31 MAY 2026", tc.description, tc.amount}))
			if len(reading.Fields) != 1 {
				t.Fatalf("read %d rows, want 1", len(reading.Fields))
			}
			fields := reading.Fields[0]

			assertInt(t, fields, domain.FieldAmountMinor, tc.minor, domain.High)
			assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
			assertText(t, fields, domain.FieldDirection, tc.direction, domain.High)
			assertText(t, fields, domain.FieldOccurredAt, "2026-05-31T06:00:00.000Z", domain.High)

			if tc.merchant != "" {
				assertText(t, fields, domain.FieldMerchant, tc.merchant, domain.Medium)
			} else if _, ok := fields[domain.FieldMerchant]; ok {
				t.Errorf("row claimed a merchant, want none")
			}
			if tc.counterparty != "" {
				conf := domain.Medium
				if tc.internal {
					conf = domain.High // a fact about the shape, not a reading (D62)
				}
				assertText(t, fields, domain.FieldCounterparty, tc.counterparty, conf)
			} else if _, ok := fields[domain.FieldCounterparty]; ok {
				t.Errorf("row claimed a counterparty, want none")
			}
		})
	}
}

// The descriptor shapes do not read a Compra or a SPEI row. A card purchase
// still yields a merchant, not a no-party descriptor row.
func TestDescriptorDoesNotStealACompra(t *testing.T) {
	reading := interpret(t, statement([3]string{"31 MAY 2026", "PANADERIA65 Compra", "-$100.00"}))
	fields := compraOf(t, reading, "PANADERIA65")
	assertText(t, fields, domain.FieldMerchant, "PANADERIA65", domain.Medium)
}

// A Devolución names its merchant. The suffix with nothing before it reads no
// merchant, so it is not a row.
func TestABareDevolucionSuffixIsNotAShape(t *testing.T) {
	extractor := fakeExtractor{coordinates: statement([3]string{"31 MAY 2026", "Devolución", "+$20.00"})}
	if _, err := nustatement.New(extractor).Interpret(t.Context(), []byte("%PDF-1.7")); !errors.Is(err, app.ErrNoInterpretation) {
		t.Fatalf("err = %v, want app.ErrNoInterpretation", err)
	}
}
