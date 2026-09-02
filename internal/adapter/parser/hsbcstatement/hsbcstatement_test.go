package hsbcstatement_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/hsbcstatement"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

type fakeExtractor struct {
	coordinates []byte
	err         error
}

func (f fakeExtractor) Extract(ctx context.Context, _ []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.coordinates, f.err
}

func TestACargoRowIsAnOutflow(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("22", "CGO o", "08045209", "5868712", "$ 300.00", ""),
	))
	if len(reading.Fields) != 1 {
		t.Fatalf("claims = %d, want 1", len(reading.Fields))
	}
	fields := reading.Fields[0]
	assertInt(t, fields, domain.FieldAmountMinor, 30000, domain.High)
	assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
	assertText(t, fields, domain.FieldDirection, "OUTFLOW", domain.High)
	assertText(t, fields, domain.FieldCounterparty, "o", domain.Medium)
	assertText(t, fields, domain.FieldOccurredAt, "2026-06-22T06:00:00.000Z", domain.High)
}

func TestAnAbonoRowIsAnInflow(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("30", "NETNM DEPOSITO DE NOMINA", "14594619", "41234", "", "$ 17,844.58"),
	))
	fields := reading.Fields[0]
	assertInt(t, fields, domain.FieldAmountMinor, 1784458, domain.High)
	assertText(t, fields, domain.FieldDirection, "INFLOW", domain.High)
	if _, ok := fields[domain.FieldCounterparty]; ok {
		t.Error("payroll claimed a counterparty")
	}
	if _, ok := fields[domain.FieldMerchant]; ok {
		t.Error("payroll claimed a merchant")
	}
}

func TestACardRowClaimsTheMerchant(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("16", "6197411852 MAG2105031W3 MERPAGO*ROBERTO MX", "11771645", "8257", "$ 65.00", ""),
	))
	assertText(t, reading.Fields[0], domain.FieldMerchant, "6197411852 MAG2105031W3 MERPAGO*ROBERTO MX", domain.Medium)
	if _, ok := reading.Fields[0][domain.FieldCounterparty]; ok {
		t.Error("a card row claimed a counterparty")
	}
}

func TestADepositWithNoPartyClaimsNone(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("22", "G2026062069085813842882560 2420652", "08045211", "1006277", "", "$ 300.00"),
	))
	if _, ok := reading.Fields[0][domain.FieldMerchant]; ok {
		t.Error("the G20 deposit claimed a merchant")
	}
	if _, ok := reading.Fields[0][domain.FieldCounterparty]; ok {
		t.Error("the G20 deposit claimed a counterparty")
	}
}

func TestTheAnnexEnrichesAUniqueLedgerRow(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("22", "CGO o", "08045209", "5868712", "$ 300.00", ""),
		hsbcAnnex("SPEI´s Enviados durant", "22/06/2026", "14:59:42", "$ 300.00", "HSB58687", "12"),
	))
	fields := reading.Fields[0]
	assertText(t, fields, domain.FieldTrackingKey, "HSBC5868712", domain.High)
	assertText(t, fields, domain.FieldOccurredAt, "2026-06-22T20:59:42.000Z", domain.High)
	assertText(t, fields, domain.FieldCounterparty, "o", domain.Medium)
}

func TestACollisionStaysUnenriched(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("30", "CGO a", "1", "1", "$ 1,000.00", ""),
		hsbcRow("30", "CGO b", "2", "2", "$ 1,000.00", ""),
		hsbcRow("30", "CGO c", "3", "3", "$ 1,000.00", ""),
		hsbcAnnex("SPEI´s Enviados durant", "30/06/2026", "15:18:32", "$ 1,000.00", "HSBC7977", "01"),
	))
	if len(reading.Fields) != 3 {
		t.Fatalf("claims = %d, want 3", len(reading.Fields))
	}
	for i, fields := range reading.Fields {
		if _, ok := fields[domain.FieldTrackingKey]; ok {
			t.Errorf("row %d received a tracking key", i)
		}
		assertText(t, fields, domain.FieldOccurredAt, "2026-06-30T06:00:00.000Z", domain.High)
	}
}

func TestAnAnnexDateOutsideThePeriodDoesNotJoin(t *testing.T) {
	reading := interpret(t, ledger(
		hsbcPeriod(),
		hsbcRow("22", "CGO o", "08045209", "5868712", "$ 1,000.00", ""),
		hsbcAnnex("SPEI´s Enviados durant", "01/07/2026", "19:33:17", "$ 1,000.00", "HSBC0969", "72"),
	))
	if _, ok := reading.Fields[0][domain.FieldTrackingKey]; ok {
		t.Error("a July annex row enriched a June ledger row")
	}
}

func TestCoverFurnitureIsNotAMovement(t *testing.T) {
	_, err := hsbcstatement.New(fakeExtractor{coordinates: ledger(
		hsbcPeriod(),
		word(546.2, 262.45, "22"),
		word(533.2, 222.15, "$ 4,923.58"),
	)}).Interpret(t.Context(), []byte("%PDF-1.7"))
	if !errors.Is(err, app.ErrNoInterpretation) {
		t.Fatalf("err = %v, want ErrNoInterpretation", err)
	}
}

func TestAMissingPeriodFailsTheReading(t *testing.T) {
	_, err := hsbcstatement.New(fakeExtractor{coordinates: ledger(
		hsbcRow("22", "CGO o", "1", "2", "$ 300.00", ""),
	)}).Interpret(t.Context(), []byte("%PDF-1.7"))
	if err == nil {
		t.Fatal("expected an error")
	}
	var redacted app.Redacted
	if !errors.As(err, &redacted) {
		t.Fatalf("err %T does not redact", err)
	}
	if redacted.Redacted() != "the statement changed" {
		t.Errorf("class = %q", redacted.Redacted())
	}
}

func interpret(t *testing.T, coordinates []byte) app.Reading {
	t.Helper()
	reading, err := hsbcstatement.New(fakeExtractor{coordinates: coordinates}).Interpret(t.Context(), []byte("%PDF-1.7"))
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	return reading
}

func assertText(t *testing.T, fields map[domain.FieldName]domain.ClaimField, name domain.FieldName, want string, confidence domain.Confidence) {
	t.Helper()
	field, ok := fields[name]
	if !ok {
		t.Fatalf("no %s field", name)
	}
	if field.Text() != want {
		t.Errorf("%s = %q, want %q", name, field.Text(), want)
	}
	if field.Confidence() != confidence {
		t.Errorf("%s confidence = %s, want %s", name, field.Confidence(), confidence)
	}
}

func assertInt(t *testing.T, fields map[domain.FieldName]domain.ClaimField, name domain.FieldName, want int64, confidence domain.Confidence) {
	t.Helper()
	field, ok := fields[name]
	if !ok {
		t.Fatalf("no %s field", name)
	}
	if !field.IsInt() || field.Int() != want {
		t.Errorf("%s = %d, want %d", name, field.Int(), want)
	}
	if field.Confidence() != confidence {
		t.Errorf("%s confidence = %s, want %s", name, field.Confidence(), confidence)
	}
}

func ledger(parts ...string) []byte {
	yCursor = 500
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>`)
	b.WriteString(`<page width="612.000000" height="792.000000"><flow>`)
	for _, p := range parts {
		b.WriteString(p)
	}
	b.WriteString(`</flow></page></doc></body></html>`)
	return []byte(b.String())
}

func hsbcPeriod() string {
	return word(467.7, 271.85, "09/06/2026 al 30/06/2026")
}

func hsbcRow(day, desc, ref, serial, cargo, abono string) string {
	var b strings.Builder
	y := nextY()
	b.WriteString(word(43.2, y, day))
	b.WriteString(word(61.2, y, desc))
	b.WriteString(word(300.4, y, ref))
	if cargo != "" {
		b.WriteString(word(371.2, y, cargo))
	}
	if abono != "" {
		b.WriteString(word(441.8, y, abono))
	}
	b.WriteString(word(313.4, y+9.3, serial))
	return b.String()
}

func hsbcAnnex(header, date, clock, amount, key, wrap string) string {
	y := nextY() + 40
	var b strings.Builder
	b.WriteString(word(209.5, y, header))
	b.WriteString(word(38.8, y+20, date))
	b.WriteString(word(80.8, y+20, clock))
	b.WriteString(word(398.6, y+20, amount))
	b.WriteString(word(469.4, y+20, key))
	b.WriteString(word(469.4, y+29, wrap))
	return b.String()
}

func word(x, y float64, text string) string {
	return fmt.Sprintf(
		`<block xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<line xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<word xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s</word>`+
			`</line></block>`,
		x, y, x+40, y+8, x, y, x+40, y+8, x, y, x+40, y+8, xmlEscape(text))
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var yCursor = 500.0

func nextY() float64 {
	yCursor += 20
	return yCursor
}
