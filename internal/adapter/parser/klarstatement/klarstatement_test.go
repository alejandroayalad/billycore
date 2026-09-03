package klarstatement_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/klarstatement"
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
	fields := interpret(t, page(
		period(),
		word(45, 137, "02/06/2026"),
		word(104, 137, "als"),
		word(346, 137, "$14.00"),
		word(506, 137, "$0.37"),
	)).Fields[0]
	assertInt(t, fields, domain.FieldAmountMinor, 1400, domain.High)
	assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
	assertText(t, fields, domain.FieldDirection, "OUTFLOW", domain.High)
	assertText(t, fields, domain.FieldCounterparty, "als", domain.Medium)
	assertText(t, fields, domain.FieldOccurredAt, "2026-06-02T06:00:00.000Z", domain.High)
}

func TestAnAbonoRowIsAnInflow(t *testing.T) {
	fields := interpret(t, page(
		period(),
		word(45, 155, "08/06/2026"),
		word(104, 155, "Payment"),
		word(144, 155, "to"),
		word(154, 155, "Customers"),
		word(410, 155, "$1,100.00"),
		word(499, 155, "$1,100.37"),
	)).Fields[0]
	assertInt(t, fields, domain.FieldAmountMinor, 110000, domain.High)
	assertText(t, fields, domain.FieldDirection, "INFLOW", domain.High)
	assertText(t, fields, domain.FieldCounterparty, "Payment to Customers", domain.Medium)
}

func TestACardRowClaimsTheMerchantWithoutTheMask(t *testing.T) {
	fields := interpret(t, page(
		period(),
		word(45, 174, "09/06/2026"),
		word(104, 174, "La"),
		word(116, 174, "Creperia"),
		word(154, 174, "Altabrisa"),
		word(192, 174, "************2099"),
		word(342, 174, "$100.00"),
		word(498, 174, "$1,000.37"),
	)).Fields[0]
	assertText(t, fields, domain.FieldMerchant, "La Creperia Altabrisa", domain.Medium)
	if _, ok := fields[domain.FieldCounterparty]; ok {
		t.Error("a card row claimed a counterparty")
	}
}

func TestADateOutsideThePeriodIsSkipped(t *testing.T) {
	reading := interpret(t, page(
		period(),
		word(45, 439, "27/01/2025"),
		word(104, 439, "Fondo"),
		word(345, 439, "$36.33"),
		word(45, 137, "02/06/2026"),
		word(104, 137, "als"),
		word(346, 137, "$14.00"),
	))
	if len(reading.Fields) != 1 {
		t.Fatalf("claims = %d, want 1 Principal row", len(reading.Fields))
	}
	if reading.SkippedRows != 1 {
		t.Errorf("skipped = %d, want 1", reading.SkippedRows)
	}
}

func interpret(t *testing.T, coordinates []byte) app.Reading {
	t.Helper()
	reading, err := klarstatement.New(fakeExtractor{coordinates: coordinates}).Interpret(context.Background(), []byte("%PDF-test"))
	if err != nil {
		t.Fatal(err)
	}
	return reading
}

func assertInt(t *testing.T, fields map[domain.FieldName]domain.ClaimField, name domain.FieldName, want int64, conf domain.Confidence) {
	t.Helper()
	field, ok := fields[name]
	if !ok {
		t.Fatalf("missing %s", name)
	}
	if field.Int() != want || field.Confidence() != conf {
		t.Errorf("%s = %d %s, want %d %s", name, field.Int(), field.Confidence(), want, conf)
	}
}

func assertText(t *testing.T, fields map[domain.FieldName]domain.ClaimField, name domain.FieldName, want string, conf domain.Confidence) {
	t.Helper()
	field, ok := fields[name]
	if !ok {
		t.Fatalf("missing %s", name)
	}
	if field.Text() != want || field.Confidence() != conf {
		t.Errorf("%s = %q %s, want %q %s", name, field.Text(), field.Confidence(), want, conf)
	}
}

func period() string {
	return word(200, 80, "01 de junio - 30 de junio, 2026")
}

func page(parts ...string) []byte {
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

func word(x, y float64, text string) string {
	return fmt.Sprintf(
		`<block xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<line xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<word xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s</word>`+
			`</line></block>`,
		x, y, x+40, y+8, x, y, x+40, y+8, x, y, x+40, y+8, text)
}
