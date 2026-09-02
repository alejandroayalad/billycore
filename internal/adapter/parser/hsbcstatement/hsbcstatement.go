// Package hsbcstatement reads the HSBC México Cuenta Flexible Simple statement.
//
// The ledger is the Claim. The SPEI annex enriches a ledger row when amount,
// calendar day and direction identify exactly one row (D74). Package bbox
// gives the ledger rows; this package reads their shape.
package hsbcstatement

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	_ "time/tzdata"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/hsbcenc"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// Currency is what an HSBC Flex statement is denominated in (D30, D50).
const Currency = domain.Currency("MXN")

var zone = func() *time.Location {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		panic("hsbcstatement: America/Mexico_City is unavailable: " + err.Error())
	}
	return loc
}()

var periodRE = regexp.MustCompile(`(\d{2}/\d{2}/\d{4})\s+al\s+(\d{2}/\d{2}/\d{4})`)

// Interpreter turns one statement PDF into the fields of each Claim.
type Interpreter struct {
	extractor app.PDFCoordinateExtractor
	geometry  bbox.HSBCGeometry
}

func New(extractor app.PDFCoordinateExtractor) Interpreter {
	return Interpreter{extractor: extractor, geometry: bbox.HSBCStatementV1()}
}

func (i Interpreter) Interpret(ctx context.Context, raw []byte) (app.Reading, error) {
	coordinates, err := i.extractor.Extract(ctx, raw)
	if err != nil {
		return app.Reading{}, classify(fmt.Errorf("%w: %w", errExtraction, err))
	}
	document, err := bbox.Parse(coordinates)
	if err != nil {
		return app.Reading{}, classify(fmt.Errorf("%w: %w", errCoordinates, err))
	}
	if err := decodeIfPresent(raw, document); err != nil {
		return app.Reading{}, classify(err)
	}

	start, end, err := periodOf(document)
	if err != nil {
		return app.Reading{}, classify(err)
	}

	rows := i.geometry.LedgerRows(document)
	reading := app.Reading{}
	claims := make([]map[domain.FieldName]domain.ClaimField, 0, len(rows))
	for _, row := range rows {
		fields, err := readLedger(row, start, end)
		if err != nil {
			return app.Reading{}, classify(err)
		}
		claims = append(claims, fields)
	}
	if err := enrich(claims, document); err != nil {
		return app.Reading{}, classify(err)
	}
	reading.Fields = claims
	if len(reading.Fields) == 0 {
		return app.Reading{}, app.ErrNoInterpretation
	}
	return reading, nil
}

func decodeIfPresent(raw []byte, document *bbox.Document) error {
	table, err := hsbcenc.FromPDF(raw)
	if errors.Is(err, hsbcenc.ErrNoTable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errEncoding, err)
	}
	table.DecodeDocument(document)
	return nil
}

func periodOf(doc *bbox.Document) (time.Time, time.Time, error) {
	for _, page := range doc.Pages {
		for _, b := range page.Blocks {
			for _, line := range b.Lines {
				if m := periodRE.FindStringSubmatch(line.Text()); m != nil {
					start, err := parser.ParseWall(m[1])
					if err != nil {
						return time.Time{}, time.Time{}, err
					}
					end, err := parser.ParseWall(m[2])
					if err != nil {
						return time.Time{}, time.Time{}, err
					}
					return start.In(zone), end.In(zone), nil
				}
			}
		}
	}
	return time.Time{}, time.Time{}, fmt.Errorf("hsbcstatement: %s: the cover states no period", templateChanged)
}

func dateOn(day string, start, end time.Time) (time.Time, error) {
	n, err := strconv.Atoi(day)
	if err != nil || n < 1 || n > 31 {
		return time.Time{}, fmt.Errorf("hsbcstatement: %s: day %q", templateChanged, day)
	}
	for cursor := start; !cursor.After(end); cursor = cursor.Add(24 * time.Hour) {
		if cursor.Day() == n {
			return time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, zone), nil
		}
	}
	return time.Time{}, fmt.Errorf("hsbcstatement: %s: day %s is outside the period", templateChanged, day)
}

func occurredAt(day time.Time, clock string) (time.Time, error) {
	if clock == "" {
		return day.UTC(), nil
	}
	hour, minute, second, err := parser.ParseClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	wall := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, second, 0, zone)
	return wall.UTC(), nil
}

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

var _ app.Interpreter = Interpreter{}
