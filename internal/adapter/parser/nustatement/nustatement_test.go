package nustatement_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/nustatement"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// The fixtures are the extractor's, and they are windows of the real May, June
// and July 2026 statements. A copy here would drift from them. See their
// README.md.
var fixtureDir = filepath.Join("..", "..", "extractor", "pdftotext", "testdata")

// fakeExtractor stands in for pdftotext. The reading above the process boundary
// is deterministic Go, so a test of it needs no Poppler (D51, D54).
type fakeExtractor struct {
	coordinates []byte
	err         error

	// capture records the context that the parser handed over. A running
	// pdftotext must stop when the caller stops (D57).
	capture func(context.Context)
}

func (f fakeExtractor) Extract(ctx context.Context, pdf []byte) ([]byte, error) {
	if f.capture != nil {
		f.capture(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.coordinates, f.err
}

// A Compra row states five things, and each one is read from the geometry.
func TestACompraRowYieldsItsFiveFields(t *testing.T) {
	fields := compraOf(t, readFixture(t, "nu_2026_05_bbox.xhtml"), "PANADERIA65 LIBRERIA66 FERRETERIA67")

	if len(fields) != 5 {
		t.Fatalf("the row yielded %d fields, want 5: %v", len(fields), fields)
	}
	// The amount is HIGH and the date is HIGH: a column places each one and a
	// partner on an identical y confirms it (D58). The merchant is MEDIUM,
	// because only its column places it.
	assertInt(t, fields, domain.FieldAmountMinor, 68669, domain.High)
	assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
	assertText(t, fields, domain.FieldDirection, "OUTFLOW", domain.High)
	assertText(t, fields, domain.FieldMerchant, "PANADERIA65 LIBRERIA66 FERRETERIA67", domain.Medium)
	// Midnight in Mexico City, which is UTC-6 (D29, D64). The statement states
	// the day and no time of day.
	assertText(t, fields, domain.FieldOccurredAt, "2026-05-31T06:00:00.000Z", domain.High)
}

// Money is unsigned, and the sign of the amount column carries the direction
// (AGENTS.md §3 rule 2).
func TestTheSignOfTheAmountIsTheDirection(t *testing.T) {
	reading := interpret(t, statement(
		[3]string{"31 MAY 2026", "PANADERIA65 Compra", "-$100.00"},
		[3]string{"31 MAY 2026", "LIBRERIA66 Compra", "+$250.50"},
	))

	out := compraOf(t, reading, "PANADERIA65")
	assertText(t, out, domain.FieldDirection, "OUTFLOW", domain.High)
	assertInt(t, out, domain.FieldAmountMinor, 10000, domain.High)

	in := compraOf(t, reading, "LIBRERIA66")
	assertText(t, in, domain.FieldDirection, "INFLOW", domain.High)
	assertInt(t, in, domain.FieldAmountMinor, 25050, domain.High)
}

// A purchase abroad prints its pesos on the row and its origin amount and rate
// on a sub-line below. Nu already converted, and BillyCore converts nothing
// (D56). No field names the origin amount or the rate, so none is recorded
// (D60), and the sub-line is never a row of its own.
func TestAForeignPurchaseRecordsTheSettledPesosOnly(t *testing.T) {
	reading := readFixture(t, "nu_2026_05_fx_bbox.xhtml")

	fields := compraOf(t, reading, "Huerta235.ibarra236 *Juarez237")
	assertInt(t, fields, domain.FieldAmountMinor, 38339, domain.High)
	if len(fields) != 5 {
		t.Fatalf("the foreign purchase yielded %d fields, want the same 5: %v", len(fields), fields)
	}

	// USD 20 at MXN 17.404 is 348.08, and neither number appears anywhere.
	for _, claim := range reading.Fields {
		for name, field := range claim {
			if field.IsInt() && (field.Int() == 2000 || field.Int() == 34808) {
				t.Errorf("%s carries the origin amount", name)
			}
			if !field.IsInt() && strings.Contains(field.Text(), "USD") {
				t.Errorf("%s carries %q", name, field.Text())
			}
		}
	}
}

// D59: a row of another shape is skipped, the reading says how many, and the
// rows that were read are still one interpretation.
func TestARowThatNoShapeReadsIsSkippedAndCounted(t *testing.T) {
	cases := map[string]struct{ read, skipped int }{
		// Two purchases, a SPEI deposit and a Cajita withdrawal.
		"nu_2026_05_bbox.xhtml": {read: 4, skipped: 0},
		// One purchase, a SPEI transfer and a Cajita withdrawal. One row of
		// another shape is skipped.
		"nu_2026_06_bbox.xhtml":    {read: 3, skipped: 1},
		"nu_2026_05_fx_bbox.xhtml": {read: 3, skipped: 1},
	}
	for fixture, want := range cases {
		t.Run(fixture, func(t *testing.T) {
			reading := readFixture(t, fixture)
			if len(reading.Fields) != want.read {
				t.Errorf("read %d rows, want %d", len(reading.Fields), want.read)
			}
			if reading.SkippedRows != want.skipped {
				t.Errorf("skipped %d rows, want %d", reading.SkippedRows, want.skipped)
			}
		})
	}
}

// A statement of rows no shape reads is an answer, not a failure. The use case
// knows "nothing recognised this" and not what a Compra is.
func TestAStatementWithNoReadableRowReportsNoInterpretation(t *testing.T) {
	extractor := fakeExtractor{coordinates: statement(
		[3]string{"31 MAY 2026", "Pago a tu tarjeta de crédito Nu", "-$100.00"},
		[3]string{"31 MAY 2026", "Larios580 saldo de tu Cajita: delgado79", "+$250.50"},
	)}
	_, err := nustatement.New(extractor).Interpret(t.Context(), []byte("%PDF-1.7"))
	if !errors.Is(err, app.ErrNoInterpretation) {
		t.Fatalf("err = %v, want app.ErrNoInterpretation", err)
	}
}

// A SPEI deposit is an inflow, and the detail block below the row names the
// other party, the tracking key and the time of day. The document labels each
// one, so D34 rates them HIGH and no geometry is inferred (D58).
//
// The detail of this row also has two elements on one y, which poppler produces
// where a horizontal gap splits a visual line. The name reads correctly only if
// the block is joined in reading order before it is parsed.
func TestASpeiDepositYieldsItsCounterpartyAndTrackingKey(t *testing.T) {
	fields := speiOf(t, readFixture(t, "nu_2026_05_bbox.xhtml"), "VIAJE74 COMIDA75 ALVAREZ76")

	assertText(t, fields, domain.FieldDirection, "INFLOW", domain.High)
	assertInt(t, fields, domain.FieldAmountMinor, 80881, domain.High)
	assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
	assertText(t, fields, domain.FieldCounterparty, "VIAJE74 COMIDA75 ALVAREZ76", domain.High)
	assertText(t, fields, domain.FieldTrackingKey, "SNTH84848484848484848484", domain.High)
	// 31 MAY 2026 and `Hora: 10:22:34` in Mexico City, which is UTC-6 (D29).
	assertText(t, fields, domain.FieldOccurredAt, "2026-05-31T16:22:34.000Z", domain.High)

	// A transfer is not a purchase, so no merchant is claimed (D61). The CLABE,
	// the concept and the reference stay unread (D48, D61).
	if _, ok := fields[domain.FieldMerchant]; ok {
		t.Error("a SPEI movement claimed a merchant")
	}
	if len(fields) != 6 {
		t.Fatalf("the row yielded %d fields, want 6: %v", len(fields), fields)
	}
	for name, field := range fields {
		if field.IsInt() {
			continue
		}
		for _, unread := range []string{"838383838383838383", "delgado79", "858585"} {
			if strings.Contains(field.Text(), unread) {
				t.Errorf("%s carries %q, which no field names", name, unread)
			}
		}
	}
}

// A SPEI transfer is an outflow, and it names the other party after `Al cliente`
// rather than after `Del cliente`.
func TestASpeiTransferYieldsItsCounterpartyAndTrackingKey(t *testing.T) {
	fields := speiOf(t, readFixture(t, "nu_2026_06_bbox.xhtml"), "navarro278")

	assertText(t, fields, domain.FieldDirection, "OUTFLOW", domain.High)
	assertInt(t, fields, domain.FieldAmountMinor, 61662, domain.High)
	assertText(t, fields, domain.FieldCounterparty, "navarro278", domain.High)
	assertText(t, fields, domain.FieldTrackingKey, "TIENDA364", domain.High)
	// 18 JUN 2026 and `Hora: 03:03:21` in Mexico City.
	assertText(t, fields, domain.FieldOccurredAt, "2026-06-18T09:03:21.000Z", domain.High)
}

// A detail block can open a page and belong to the last row of the page before
// it. The reading must attach it there, or the row loses every field the detail
// carries and the next row gains them.
func TestASpeiDetailOnTheNextPageBelongsToTheRowAbove(t *testing.T) {
	reading := readFixture(t, "nu_2026_07_bbox.xhtml")
	if len(reading.Fields) != 2 || reading.SkippedRows != 0 {
		t.Fatalf("read %d rows and skipped %d, want 2 and 0", len(reading.Fields), reading.SkippedRows)
	}

	// The row is the last one of page 1; its detail opens page 2.
	deposit := speiOf(t, reading, "GARZA82 DE HUERTA83 IBARRA84 JUAREZ85")
	assertText(t, deposit, domain.FieldDirection, "INFLOW", domain.High)
	assertInt(t, deposit, domain.FieldAmountMinor, 60607, domain.High)
	assertText(t, deposit, domain.FieldTrackingKey, "SNT0909090", domain.High)
	assertText(t, deposit, domain.FieldOccurredAt, "2026-07-31T18:48:36.000Z", domain.High)

	// The row and the detail below it on page 2. Mexico City is UTC-6, so an
	// evening movement of 31 July is an instant of 1 August.
	transfer := speiOf(t, reading, "cuota110 renta111")
	assertText(t, transfer, domain.FieldDirection, "OUTFLOW", domain.High)
	assertInt(t, transfer, domain.FieldAmountMinor, 121213, domain.High)
	assertText(t, transfer, domain.FieldTrackingKey, "CORTEZ116", domain.High)
	assertText(t, transfer, domain.FieldOccurredAt, "2026-08-01T00:54:18.000Z", domain.High)
}

// Almost every SPEI movement states a tracking key, and a movement without one
// is still a movement. Absence is a missing field and never an empty value —
// the rule occurred_at already follows (D33, DOMAIN.md §7).
func TestASpeiMovementWithNoTrackingKeyStillReads(t *testing.T) {
	fields := speiOf(t, interpret(t, statementWithDetail(
		[3]string{"31 MAY 2026", "ALVAREZ76 delgado79", "+$808.81"},
		detailLine{text: "Depósito SPEI, Hora: 10:22:34, Recibido de BBVA MEXICO. Del cliente"},
		detailLine{dy: 13.5, text: "ALVAREZ76 (Dato no verificado por esta institución), por concepto"},
		detailLine{dy: 27, text: "delgado79. De la cuenta 838383838383838383 clabe, Clave de"},
		detailLine{dy: 40.5, text: "referencia 858585"},
	)), "ALVAREZ76")

	if _, ok := fields[domain.FieldTrackingKey]; ok {
		t.Error("a movement with no `Clave de rastreo` claimed a tracking key")
	}
	assertText(t, fields, domain.FieldCounterparty, "ALVAREZ76", domain.High)
	assertText(t, fields, domain.FieldOccurredAt, "2026-05-31T16:22:34.000Z", domain.High)
}

// Poppler splits one visual line into two elements where a wide gap sits
// between them, and the two elements share a y. The name of the other party
// spans that split, so the block is joined in reading order and never in
// document order.
func TestADetailSplitAcrossOneYJoinsInReadingOrder(t *testing.T) {
	fields := speiOf(t, interpret(t, statementWithDetail(
		[3]string{"31 MAY 2026", "ALVAREZ76 delgado79", "+$808.81"},
		detailLine{text: "Depósito SPEI, Hora: 10:22:34, Recibido de BBVA MEXICO. Del cliente"},
		// The right-hand element arrives first, as it does in the document.
		detailLine{dy: 13.5, dx: 167.13, text: "(Dato no verificado por esta"},
		detailLine{dy: 13.5, text: "VIAJE74 COMIDA75 ALVAREZ76"},
		detailLine{dy: 27, text: "institución), Clave de rastreo SNTH8484, Clave de referencia 858585"},
	)), "VIAJE74 COMIDA75 ALVAREZ76")

	assertText(t, fields, domain.FieldCounterparty, "VIAJE74 COMIDA75 ALVAREZ76", domain.High)
	assertText(t, fields, domain.FieldTrackingKey, "SNTH8484", domain.High)
}

// A detail that says one direction and an amount column that says the other is
// drift in the document. Billy does not choose between them.
func TestASpeiDetailThatContradictsTheAmountColumnFails(t *testing.T) {
	extractor := fakeExtractor{coordinates: statementWithDetail(
		[3]string{"31 MAY 2026", "ALVAREZ76 delgado79", "-$808.81"},
		detailLine{text: "Depósito SPEI, Hora: 10:22:34, Recibido de BBVA MEXICO. Del cliente"},
		detailLine{dy: 13.5, text: "ALVAREZ76 (Dato no verificado por esta institución), por concepto x."},
	)}
	_, err := nustatement.New(extractor).Interpret(t.Context(), []byte("%PDF-1.7"))
	if err == nil {
		t.Fatal("a deposit that left the account was read")
	}
	var safe app.Redacted
	if !errors.As(err, &safe) {
		t.Fatalf("error does not describe itself safely: %T", err)
	}
	if safe.Redacted() != "the statement changed" {
		t.Errorf("reason = %q", safe.Redacted())
	}
}

// D55 and D63: a Cajita movement and a `dinero de respaldo` movement go between
// accounts of one user. The user is on both sides, so the counterparty is the
// reserved value and no merchant is claimed (D61, D62).
//
// The direction comes from the sign of the amount column, as it does for every
// other row.
func TestAnInternalMovementRecordsTheUserAsTheCounterparty(t *testing.T) {
	cases := []struct {
		description string
		amount      string
		direction   string
		minor       int64
	}{
		{"Retiro de Cajita: Mi primera Cajita", "+$707.71", "INFLOW", 70771},
		{"Retiro de Cajita: envio602", "+$4.47", "INFLOW", 447},
		{"Depósito en Cajita: Mi espino536", "-$7,474.75", "OUTFLOW", 747475},
		{"Retirado de tu dinero de respaldo", "+$8,585.86", "INFLOW", 858586},
	}
	for _, want := range cases {
		t.Run(want.description, func(t *testing.T) {
			reading := interpret(t, statement([3]string{"31 MAY 2026", want.description, want.amount}))
			if len(reading.Fields) != 1 {
				t.Fatalf("read %d rows, want 1", len(reading.Fields))
			}
			fields := reading.Fields[0]

			assertText(t, fields, domain.FieldCounterparty, domain.CounterpartySelf, domain.High)
			assertText(t, fields, domain.FieldDirection, want.direction, domain.High)
			assertInt(t, fields, domain.FieldAmountMinor, want.minor, domain.High)
			assertText(t, fields, domain.FieldCurrency, "MXN", domain.Low)
			assertText(t, fields, domain.FieldOccurredAt, "2026-05-31T06:00:00.000Z", domain.High)

			// A Cajita is not a place where something was bought.
			if _, ok := fields[domain.FieldMerchant]; ok {
				t.Error("an internal movement claimed a merchant")
			}
			if len(fields) != 5 {
				t.Fatalf("the row yielded %d fields, want 5: %v", len(fields), fields)
			}
		})
	}
}

// A statement prints each internal movement two times: once for the account and
// once for the pot, with the same words and the other sign. Only the account row
// is the movement. Reading the mirror row as well would record the money two
// times, in two directions, on one day.
func TestTheMirrorRowOfAnInternalMovementIsNotRead(t *testing.T) {
	reading := interpret(t, statement(
		[3]string{"24 JUN 2026", "Retiro de Cajita: Mi primera Cajita", "+$404.41"},
		// The same three movements as the section of the pot states them.
		[3]string{"24 JUN 2026", "Retiro de Cajita: Mi primera Cajita", "-$404.41"},
		[3]string{"23 JUN 2026", "Depósito en Cajita: Mi primera Cajita", "+$42.43"},
		[3]string{"22 JUN 2026", "Retirado de tu dinero de respaldo", "-$63.64"},
	))

	if len(reading.Fields) != 1 {
		t.Fatalf("read %d rows, want 1: the three mirror rows are one movement seen twice", len(reading.Fields))
	}
	if reading.SkippedRows != 3 {
		t.Errorf("skipped %d rows, want 3", reading.SkippedRows)
	}
	assertText(t, reading.Fields[0], domain.FieldDirection, "INFLOW", domain.High)
	assertInt(t, reading.Fields[0], domain.FieldAmountMinor, 40441, domain.High)
}

// The cover page prints `Dinero de respaldo` beside a balance, with no date and
// no sign. It is a summary figure and never a movement (D63).
func TestTheBackingMoneySummaryIsNotAMovement(t *testing.T) {
	extractor := fakeExtractor{coordinates: statement(
		[3]string{"", "Dinero de respaldo", "$1,234.56"},
	)}
	_, err := nustatement.New(extractor).Interpret(t.Context(), []byte("%PDF-1.7"))
	if !errors.Is(err, app.ErrNoInterpretation) {
		t.Fatalf("err = %v, want app.ErrNoInterpretation", err)
	}
}

// A purchase and a SPEI movement read exactly as they did before this shape
// family joined the parser.
func TestTheInternalShapesChangeNoOtherReading(t *testing.T) {
	reading := readFixture(t, "nu_2026_05_bbox.xhtml")

	compra := compraOf(t, reading, "PANADERIA65 LIBRERIA66 FERRETERIA67")
	assertInt(t, compra, domain.FieldAmountMinor, 68669, domain.High)
	assertText(t, compra, domain.FieldOccurredAt, "2026-05-31T06:00:00.000Z", domain.High)
	if _, ok := compra[domain.FieldCounterparty]; ok {
		t.Error("a purchase claimed a counterparty")
	}

	spei := speiOf(t, reading, "VIAJE74 COMIDA75 ALVAREZ76")
	assertText(t, spei, domain.FieldTrackingKey, "SNTH84848484848484848484", domain.High)
	assertText(t, spei, domain.FieldOccurredAt, "2026-05-31T16:22:34.000Z", domain.High)
}

// The parser never makes a context of its own. A shutdown has to stop a running
// pdftotext, and it can only do that through the caller's context (D57).
func TestTheCallersContextReachesTheExtractor(t *testing.T) {
	var captured context.Context
	extractor := fakeExtractor{
		coordinates: fixture(t, "nu_2026_05_bbox.xhtml"),
		capture:     func(ctx context.Context) { captured = ctx },
	}

	ctx, cancel := context.WithCancel(t.Context())
	if _, err := nustatement.New(extractor).Interpret(ctx, []byte("%PDF-1.7")); err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if captured == nil {
		t.Fatal("the extractor received no context")
	}
	cancel()
	if captured.Err() == nil {
		t.Fatal("cancelling the caller did not cancel the context the extractor received")
	}
}

// A cancelled caller stops the reading rather than producing a partial one.
func TestACancelledCallerStopsTheReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	extractor := fakeExtractor{coordinates: fixture(t, "nu_2026_05_bbox.xhtml")}
	if _, err := nustatement.New(extractor).Interpret(ctx, []byte("%PDF-1.7")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Money is integer minor units end to end. 899.93 * 100 is 89992.99999999999 in
// IEEE 754, so a float anywhere in the path loses a centavo here.
func TestMoneyNeverPassesThroughAFloat(t *testing.T) {
	reading := interpret(t, statement(
		[3]string{"31 MAY 2026", "PANADERIA65 Compra", "-$899.93"},
		[3]string{"31 MAY 2026", "LIBRERIA66 Compra", "-$1,234.56"},
	))
	assertInt(t, compraOf(t, reading, "PANADERIA65"), domain.FieldAmountMinor, 89993, domain.High)
	assertInt(t, compraOf(t, reading, "LIBRERIA66"), domain.FieldAmountMinor, 123456, domain.High)
}

// The leak this guards against is real: ParseMoney renders the text it choked
// on, and that text is a line of the statement. What the pipeline stores must
// not carry it (SECURITY.md §10).
//
// The amount here has the shape of the column and a group of two digits, so it
// reaches the parser and fails there.
func TestAFailureDescribesItselfWithoutTheStatement(t *testing.T) {
	merchant := "PANADERIA65"
	extractor := fakeExtractor{coordinates: statement(
		[3]string{"31 MAY 2026", merchant + " Compra", "-$1,23.45"},
	)}
	_, err := nustatement.New(extractor).Interpret(t.Context(), []byte("%PDF-1.7"))
	if err == nil {
		t.Fatal("a malformed amount parsed")
	}

	var safe app.Redacted
	if !errors.As(err, &safe) {
		t.Fatalf("error does not describe itself safely: %T", err)
	}
	reason := safe.Redacted()
	for _, leak := range []string{"1,23.45", merchant, "$"} {
		if strings.Contains(reason, leak) {
			t.Errorf("redacted reason %q carries %q from the statement", reason, leak)
		}
	}
	if reason != "the amount did not parse" {
		t.Errorf("reason = %q", reason)
	}
}

func readFixture(t *testing.T, name string) app.Reading {
	t.Helper()
	return interpret(t, fixture(t, name))
}

func interpret(t *testing.T, coordinates []byte) app.Reading {
	t.Helper()
	reading, err := nustatement.New(fakeExtractor{coordinates: coordinates}).Interpret(t.Context(), []byte("%PDF-1.7"))
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	return reading
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// compraOf returns the field set of the purchase at one merchant. The reading
// is in page then y order, and naming the merchant says which row is meant.
func compraOf(t *testing.T, reading app.Reading, merchant string) map[domain.FieldName]domain.ClaimField {
	t.Helper()
	for _, fields := range reading.Fields {
		if field, ok := fields[domain.FieldMerchant]; ok && field.Text() == merchant {
			return fields
		}
	}
	t.Fatalf("no claim for merchant %q", merchant)
	return nil
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

// statement builds a coordinate document from date, description and amount
// triples. The columns are the measured ones: a date left of x=130, a
// description between the columns, and an amount right of x=480. The date and
// the amount share one y, which is what makes the pair a row (D54).
func statement(rows ...[3]string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>`)
	b.WriteString(`<page width="595.000000" height="842.000000"><flow>`)
	y := 150.0
	for _, row := range rows {
		writeBlock(&b, 56.00, y, row[0])
		// The description of a row sits about one point off its y, as it does
		// in every fixture.
		writeBlock(&b, 135.84, y-1, row[1])
		writeBlock(&b, 494.64, y, row[2])
		y += 26.09
	}
	b.WriteString(`</flow></page></doc></body></html>`)
	return []byte(b.String())
}

func writeBlock(b *strings.Builder, x, y float64, text string) {
	box := func(tag string, extra string) {
		fmt.Fprintf(b, `<%s xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s`, tag, x, y, x+100, y+10, extra)
	}
	box("block", "")
	box("line", "")
	for _, word := range strings.Fields(text) {
		fmt.Fprintf(b, `<word xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s</word>`, x, y, x+10, y+10, word)
	}
	b.WriteString(`</line></block>`)
}

// speiOf returns the field set of the movement with one counterparty. A SPEI
// row claims no merchant, so compraOf cannot find it.
func speiOf(t *testing.T, reading app.Reading, counterparty string) map[domain.FieldName]domain.ClaimField {
	t.Helper()
	for _, fields := range reading.Fields {
		if field, ok := fields[domain.FieldCounterparty]; ok && field.Text() == counterparty {
			return fields
		}
	}
	t.Fatalf("no claim for counterparty %q", counterparty)
	return nil
}

// detailLine is one line of a detail block, placed relative to the start of the
// block. dx is the offset from the left edge of the description column, which is
// where poppler puts the second element of a split line.
type detailLine struct {
	dy, dx float64
	text   string
}

// statementWithDetail builds one row and the detail block below it. The block
// starts at the left edge of the description column and sits below the row,
// which is what makes it a detail and not a description (D54).
func statementWithDetail(row [3]string, detail ...detailLine) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>`)
	b.WriteString(`<page width="595.000000" height="842.000000"><flow>`)
	const y = 150.0
	writeBlock(&b, 56.00, y, row[0])
	writeBlock(&b, 135.84, y-1, row[1])
	writeBlock(&b, 494.64, y, row[2])
	writeDetail(&b, 135.84, y+21, detail)
	b.WriteString(`</flow></page></doc></body></html>`)
	return []byte(b.String())
}

func writeDetail(b *strings.Builder, x, y float64, lines []detailLine) {
	fmt.Fprintf(b, `<block xMin="%f" yMin="%f" xMax="%f" yMax="%f">`, x, y, x+340, y+13.5*float64(len(lines)))
	for _, line := range lines {
		lx, ly := x+line.dx, y+line.dy
		fmt.Fprintf(b, `<line xMin="%f" yMin="%f" xMax="%f" yMax="%f">`, lx, ly, lx+170, ly+10)
		for _, word := range strings.Fields(line.text) {
			fmt.Fprintf(b, `<word xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s</word>`, lx, ly, lx+10, ly+10, word)
		}
		b.WriteString(`</line>`)
	}
	b.WriteString(`</block>`)
}
