// Package nustatement reads the Nu México account statement.
//
// The statement labels nothing, so the reading is geometric: pdftotext gives
// coordinates (D54), package bbox gives positioned rows, and this package reads
// the shape of one row. It handles the `Compra` shape, the SPEI shapes, and the
// movements between accounts of one user. Each other shape is skipped and
// counted (D59).
package nustatement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "time/tzdata" // D29: the zone must resolve on a host with no tz database

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// Currency is what a Nu statement is denominated in (D30, D50).
//
// It is a constant and not a parsed field. The page states
// `MONTO EN PESOS MEXICANOS` above the amount column, and this parser does not
// read that heading, so Billy infers the currency from the Source. D34 calls
// that inference LOW, and the field says so.
const Currency = domain.Currency("MXN")

// compraSuffix is the word that marks a card purchase. Nu writes it at the end
// of the description of the row.
const compraSuffix = " Compra"

// zone is the wall-clock zone of every Nu timestamp (D29). Mexico City is
// UTC-6, so reading a statement date as UTC moves each purchase one day back.
var zone = func() *time.Location {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		panic("nustatement: America/Mexico_City is unavailable: " + err.Error())
	}
	return loc
}()

// Interpreter turns one statement PDF into the fields of each Claim.
//
// It implements app.Interpreter. The extractor is a port, so this package runs
// no process of its own and a test reads a coordinate fixture directly.
type Interpreter struct {
	extractor app.PDFCoordinateExtractor
	geometry  bbox.Geometry
}

func New(extractor app.PDFCoordinateExtractor) Interpreter {
	return Interpreter{extractor: extractor, geometry: bbox.NuStatementV1()}
}

// Interpret reads one stored statement and reports what Billy believes about it.
//
// The context is the caller's, and it goes to the extractor without change. A
// running pdftotext must stop when the caller stops (D57).
//
// It returns app.ErrNoInterpretation when no row is recognised. That is an
// ordinary result and not a failure.
func (i Interpreter) Interpret(ctx context.Context, raw []byte) (app.Reading, error) {
	coordinates, err := i.extractor.Extract(ctx, raw)
	if err != nil {
		return app.Reading{}, classify(fmt.Errorf("%w: %w", errExtraction, err))
	}
	document, err := bbox.Parse(coordinates)
	if err != nil {
		return app.Reading{}, classify(fmt.Errorf("%w: %w", errCoordinates, err))
	}

	var reading app.Reading
	for _, row := range i.geometry.Rows(document) {
		fields, recognised, err := read(row)
		if err != nil {
			// The shape matched and the value did not parse. That is drift in
			// the document, and it fails the reading rather than becoming one
			// more skipped row: a skipped row says "Billy reads no such shape".
			return app.Reading{}, classify(err)
		}
		if !recognised {
			reading.SkippedRows++
			continue
		}
		reading.Fields = append(reading.Fields, fields)
	}
	if len(reading.Fields) == 0 {
		return app.Reading{}, app.ErrNoInterpretation
	}
	return reading, nil
}

// shape reads one row, and reports if the row is of its shape. A shape that
// recognises a row and cannot read a value returns the reason.
type shape func(bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error)

// shapes are the rows this parser reads, in the order it tries them. A row that
// no shape reads is skipped and counted (D59).
var shapes = []shape{compra, internalMovement, spei}

// read gives one row to each shape until one recognises it.
func read(row bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error) {
	for _, s := range shapes {
		fields, recognised, err := s(row)
		if recognised || err != nil {
			return fields, recognised, err
		}
	}
	return nil, false, nil
}

// compra reads one card purchase, and reports if the row is one.
//
// The confidence rule is D58. The date and the amount are HIGH: a column places
// each one, and a partner on an identical y confirms it, which held on 548 of
// 548 rows. The merchant is MEDIUM, because only its column places it.
//
// The exchange sub-line of a purchase abroad is never read here. bbox keeps it
// off the row, and the peso amount of the row is the whole record (D56, D60).
func compra(row bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error) {
	merchant, recognised := strings.CutSuffix(strings.Join(row.Description, " "), compraSuffix)
	if !recognised {
		return nil, false, nil
	}

	money, direction, err := movement(row.Amount)
	if err != nil {
		return nil, true, err
	}
	// A purchase row states a day and no time of day (D64).
	when, err := occurredAt(row.Date, "")
	if err != nil {
		return nil, true, err
	}

	b := builder{out: map[domain.FieldName]domain.ClaimField{}}
	b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
	b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
	b.text(domain.FieldDirection, string(direction), domain.High)
	b.text(domain.FieldMerchant, merchant, domain.Medium)
	b.moment(domain.FieldOccurredAt, when, domain.High)
	if b.err != nil {
		return nil, true, fmt.Errorf("nustatement: %s: %w", templateChanged, b.err)
	}
	return b.out, true, nil
}

// movement reads the amount column. The sign is the direction, and the Money
// that keeps it is unsigned (AGENTS.md §3 rule 2). Nothing here is a float:
// ParseMoney reads the two sides of the decimal point as integers.
func movement(amount string) (domain.Money, domain.TransactionDirection, error) {
	var direction domain.TransactionDirection
	switch {
	case strings.HasPrefix(amount, "-"):
		direction = domain.Outflow
	case strings.HasPrefix(amount, "+"):
		direction = domain.Inflow
	default:
		return domain.Money{}, "", fmt.Errorf("nustatement: %s: the amount states no direction", templateChanged)
	}
	money, err := parser.ParseMoney(amount[1:], Currency)
	if err != nil {
		return domain.Money{}, "", err
	}
	if money.Minor() <= 0 {
		return domain.Money{}, "", fmt.Errorf("nustatement: a movement of nothing is not one")
	}
	return money, direction, nil
}

// occurredAt reads the date column, and the time of day where the row states
// one. The date column states a day only, and midnight in Mexico City is the
// convention (D64), so the day of the instant is the day the statement printed
// for every reader in that zone. A SPEI detail states `Hora:`, and that makes
// the instant exact.
func occurredAt(date, clock string) (time.Time, error) {
	wall, err := parser.ParseWall(date)
	if err != nil {
		return time.Time{}, err
	}
	if clock != "" {
		hour, minute, second, err := parser.ParseClock(clock)
		if err != nil {
			return time.Time{}, err
		}
		wall = wall.WithClock(hour, minute, second)
	}
	return wall.In(zone).UTC(), nil
}

// Interpreter satisfies the port it was built for, checked at compile time.
var _ app.Interpreter = Interpreter{}

// builder collects fields and the first reason one could not be built. Each
// value here comes from a parser that already validated it, so a failure means
// this package stopped honouring its own contract.
type builder struct {
	out map[domain.FieldName]domain.ClaimField
	err error
}

func (b *builder) integer(name domain.FieldName, v int64, c domain.Confidence) {
	field, err := domain.NewIntField(v, c)
	b.put(name, field, err)
}

func (b *builder) text(name domain.FieldName, v string, c domain.Confidence) {
	field, err := domain.NewTextField(v, c)
	b.put(name, field, err)
}

func (b *builder) moment(name domain.FieldName, t time.Time, c domain.Confidence) {
	field, err := domain.NewTimeField(t, c)
	b.put(name, field, err)
}

func (b *builder) put(name domain.FieldName, field domain.ClaimField, err error) {
	if err != nil {
		b.err = errors.Join(b.err, fmt.Errorf("%s: %w", name, err))
		return
	}
	b.out[name] = field
}
