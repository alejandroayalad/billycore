// Package klarstatement reads the Klar México Cuenta a la Vista statement.
//
// The Principal ledger is the Claim. Cargo is OUTFLOW and abono is INFLOW.
// The document prints no tracking key. Interest and investment tables are
// other shapes and are skipped (D59, D77).
package klarstatement

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const Currency = domain.Currency("MXN")

var zone = func() *time.Location {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		panic("klarstatement: America/Mexico_City is unavailable: " + err.Error())
	}
	return loc
}()

var (
	periodRE = regexp.MustCompile(`(?i)(\d{1,2})\s+de\s+([a-záéíóúñ]+)\s+-\s+(\d{1,2})\s+de\s+([a-záéíóúñ]+),\s+(\d{4})`)
	cardMask = regexp.MustCompile(`\*{6,}\d{4}`)
	months   = map[string]time.Month{
		"enero": 1, "febrero": 2, "marzo": 3, "abril": 4, "mayo": 5, "junio": 6,
		"julio": 7, "agosto": 8, "septiembre": 9, "octubre": 10, "noviembre": 11, "diciembre": 12,
	}
)

type Interpreter struct {
	extractor app.PDFCoordinateExtractor
	geometry  bbox.KlarGeometry
}

func New(extractor app.PDFCoordinateExtractor) Interpreter {
	return Interpreter{extractor: extractor, geometry: bbox.KlarStatementV1()}
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
	start, end, err := periodOf(document)
	if err != nil {
		return app.Reading{}, classify(err)
	}

	reading := app.Reading{}
	for _, row := range i.geometry.LedgerRows(document) {
		fields, err := readLedger(row, start, end)
		if err != nil {
			return app.Reading{}, classify(err)
		}
		if fields == nil {
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

func periodOf(doc *bbox.Document) (time.Time, time.Time, error) {
	for _, page := range doc.Pages {
		for _, b := range page.Blocks {
			for _, line := range b.Lines {
				if m := periodRE.FindStringSubmatch(strings.ToLower(line.Text())); m != nil {
					start, err := spanishDay(m[1], m[2], m[5])
					if err != nil {
						return time.Time{}, time.Time{}, err
					}
					end, err := spanishDay(m[3], m[4], m[5])
					if err != nil {
						return time.Time{}, time.Time{}, err
					}
					return start, end, nil
				}
			}
		}
	}
	return time.Time{}, time.Time{}, fmt.Errorf("klarstatement: %s: the cover states no period", templateChanged)
}

func spanishDay(day, month, year string) (time.Time, error) {
	m, ok := months[month]
	if !ok {
		return time.Time{}, fmt.Errorf("klarstatement: %s: month %q", templateChanged, month)
	}
	d, err := strconv.Atoi(day)
	if err != nil {
		return time.Time{}, fmt.Errorf("klarstatement: %s: day %q", templateChanged, day)
	}
	y, err := strconv.Atoi(year)
	if err != nil {
		return time.Time{}, fmt.Errorf("klarstatement: %s: year %q", templateChanged, year)
	}
	return time.Date(y, m, d, 0, 0, 0, 0, zone), nil
}

func readLedger(row bbox.Row, start, end time.Time) (map[domain.FieldName]domain.ClaimField, error) {
	when, err := dateOn(row.Date)
	if err != nil {
		return nil, err
	}
	if when.Before(start) || when.After(end) {
		return nil, nil
	}
	money, direction, err := movement(row)
	if err != nil {
		return nil, err
	}

	b := builder{out: map[domain.FieldName]domain.ClaimField{}}
	b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
	b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
	b.text(domain.FieldDirection, string(direction), domain.High)
	b.moment(domain.FieldOccurredAt, when.UTC(), domain.High)
	describe(row, &b)
	if b.err != nil {
		return nil, fmt.Errorf("klarstatement: %s: %w", templateChanged, b.err)
	}
	return b.out, nil
}

func dateOn(raw string) (time.Time, error) {
	wall, err := time.ParseInLocation("02/01/2006", raw, zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("klarstatement: %s: date %q", templateChanged, raw)
	}
	return time.Date(wall.Year(), wall.Month(), wall.Day(), 0, 0, 0, 0, zone), nil
}

func movement(row bbox.Row) (domain.Money, domain.TransactionDirection, error) {
	switch {
	case klarMoney(row.Cargo) && klarMoney(row.Abono):
		return domain.Money{}, "", fmt.Errorf("klarstatement: %s: a row states cargo and abono", templateChanged)
	case klarMoney(row.Cargo):
		money, err := parser.ParseMoney(row.Cargo, Currency)
		if err != nil {
			return domain.Money{}, "", err
		}
		if money.Minor() <= 0 {
			return domain.Money{}, "", fmt.Errorf("klarstatement: a movement of nothing is not one")
		}
		return money, domain.Outflow, nil
	case klarMoney(row.Abono):
		money, err := parser.ParseMoney(row.Abono, Currency)
		if err != nil {
			return domain.Money{}, "", err
		}
		if money.Minor() <= 0 {
			return domain.Money{}, "", fmt.Errorf("klarstatement: a movement of nothing is not one")
		}
		return money, domain.Inflow, nil
	default:
		return domain.Money{}, "", fmt.Errorf("klarstatement: %s: a row states no amount", templateChanged)
	}
}

func klarMoney(s string) bool {
	return s != "" && s != "$0.00"
}

func describe(row bbox.Row, b *builder) {
	joined := strings.Join(strings.Fields(strings.Join(row.Description, " ")), " ")
	card := cardMask.MatchString(joined)
	desc := strings.TrimSpace(cardMask.ReplaceAllString(joined, ""))
	if desc == "" {
		return
	}
	if card {
		b.text(domain.FieldMerchant, desc, domain.Medium)
		return
	}
	b.text(domain.FieldCounterparty, desc, domain.Medium)
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
