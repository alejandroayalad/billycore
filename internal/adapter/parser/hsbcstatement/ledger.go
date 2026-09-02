package hsbcstatement

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const (
	cgoPrefix  = "CGO"
	payroll    = "NETNM DEPOSITO DE NOMINA"
	cardPrefix = 10
)

func readLedger(row bbox.Row, start, end time.Time) (map[domain.FieldName]domain.ClaimField, error) {
	money, direction, err := movement(row)
	if err != nil {
		return nil, err
	}
	when, err := dateOn(row.Date, start, end)
	if err != nil {
		return nil, err
	}
	at, err := occurredAt(when, "")
	if err != nil {
		return nil, err
	}

	b := builder{out: map[domain.FieldName]domain.ClaimField{}}
	b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
	b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
	b.text(domain.FieldDirection, string(direction), domain.High)
	b.moment(domain.FieldOccurredAt, at, domain.High)
	describe(row, &b)
	if b.err != nil {
		return nil, fmt.Errorf("hsbcstatement: %s: %w", templateChanged, b.err)
	}
	return b.out, nil
}

func movement(row bbox.Row) (domain.Money, domain.TransactionDirection, error) {
	switch {
	case row.Cargo != "" && row.Abono != "":
		return domain.Money{}, "", fmt.Errorf("hsbcstatement: %s: a row states cargo and abono", templateChanged)
	case row.Cargo != "":
		money, err := parser.ParseMoney(row.Cargo, Currency)
		if err != nil {
			return domain.Money{}, "", err
		}
		if money.Minor() <= 0 {
			return domain.Money{}, "", fmt.Errorf("hsbcstatement: a movement of nothing is not one")
		}
		return money, domain.Outflow, nil
	case row.Abono != "":
		money, err := parser.ParseMoney(row.Abono, Currency)
		if err != nil {
			return domain.Money{}, "", err
		}
		if money.Minor() <= 0 {
			return domain.Money{}, "", fmt.Errorf("hsbcstatement: a movement of nothing is not one")
		}
		return money, domain.Inflow, nil
	default:
		return domain.Money{}, "", fmt.Errorf("hsbcstatement: %s: a row states no amount", templateChanged)
	}
}

func describe(row bbox.Row, b *builder) {
	desc := strings.Join(strings.Fields(strings.Join(row.Description, " ")), " ")
	switch {
	case desc == "" || desc == payroll || strings.HasPrefix(desc, "G20"):
		return
	case strings.HasPrefix(desc, cgoPrefix):
		rest := strings.TrimSpace(strings.TrimPrefix(desc, cgoPrefix))
		if rest != "" {
			b.text(domain.FieldCounterparty, rest, domain.Medium)
		}
	case isCard(desc):
		b.text(domain.FieldMerchant, desc, domain.Medium)
	}
}

func isCard(desc string) bool {
	digits := 0
	for _, r := range desc {
		if r >= '0' && r <= '9' {
			digits++
			if digits >= cardPrefix {
				return true
			}
			continue
		}
		if unicode.IsSpace(r) {
			continue
		}
		return false
	}
	return digits >= cardPrefix
}
