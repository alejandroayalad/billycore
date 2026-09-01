package nustatement

import (
	"fmt"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// A descriptor row names its own kind in the description column. The sign gives
// the direction, as everywhere. The party is the merchant or the counterparty,
// or neither, and it is MEDIUM because only the column places it (D58). The
// detail block, where one exists, stays unread (D61).

// party is the other side of a descriptor row, and the field that holds it.
type party struct {
	field domain.FieldName // FieldMerchant, FieldCounterparty, or "" for none
	text  string
}

// descriptorShapes are the rows read from the description alone. The matchers do
// not overlap, so the order does not matter.
var descriptorShapes = []func(string) (party, bool){
	suffixParty(" Devolución", domain.FieldMerchant),       // a merchant refund
	suffixParty(" Ajuste realizado", domain.FieldMerchant), // a merchant adjustment
	prefixParty("Pago de servicio - ", domain.FieldMerchant),
	cajero, // a cash withdrawal; the operator is the counterparty
	fixed("Pago a tu tarjeta de crédito Nu"),
	fixed("Bonificación por beneficio de Nu"),
	fixed("Compensación de retraso SPEI"),
	fixed("Depósito en punto de venta"),
}

// descriptor reads one row whose description names its kind, and reports if the
// row is one.
func descriptor(row bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error) {
	description := strings.Join(row.Description, " ")
	for _, match := range descriptorShapes {
		p, ok := match(description)
		if !ok {
			continue
		}
		money, direction, err := movement(row.Amount)
		if err != nil {
			return nil, true, err
		}
		// A descriptor row states a day and no time of day (D64).
		when, err := occurredAt(row.Date, "")
		if err != nil {
			return nil, true, err
		}

		b := builder{out: map[domain.FieldName]domain.ClaimField{}}
		b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
		b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
		b.text(domain.FieldDirection, string(direction), domain.High)
		if p.field != "" {
			b.text(p.field, p.text, domain.Medium)
		}
		b.moment(domain.FieldOccurredAt, when, domain.High)
		if b.err != nil {
			return nil, true, fmt.Errorf("nustatement: %s: %w", templateChanged, b.err)
		}
		return b.out, true, nil
	}
	return nil, false, nil
}

// suffixParty reads a row whose description ends with suffix. The party is the
// text before it, such as the merchant of a `Devolución`.
func suffixParty(suffix string, field domain.FieldName) func(string) (party, bool) {
	return func(description string) (party, bool) {
		name, ok := strings.CutSuffix(description, suffix)
		if !ok || name == "" {
			return party{}, false
		}
		return party{field: field, text: name}, true
	}
}

// prefixParty reads a row whose description starts with prefix. The party is the
// text after it, such as the merchant of a service payment.
func prefixParty(prefix string, field domain.FieldName) func(string) (party, bool) {
	return func(description string) (party, bool) {
		name, ok := strings.CutPrefix(description, prefix)
		if !ok || name == "" {
			return party{}, false
		}
		return party{field: field, text: name}, true
	}
}

// cajero reads a cash withdrawal. The operator sits between the two labels and
// is an institution, so it is the counterparty and not a merchant (D61).
func cajero(description string) (party, bool) {
	const prefix, suffix = "Cajero ", " Retiro de efectivo"
	if !strings.HasPrefix(description, prefix) || !strings.HasSuffix(description, suffix) {
		return party{}, false
	}
	operator := strings.TrimSuffix(strings.TrimPrefix(description, prefix), suffix)
	if operator == "" {
		return party{}, false
	}
	return party{field: domain.FieldCounterparty, text: operator}, true
}

// fixed reads a row whose whole description is one known phrase and names no
// party: a card payment, a Nu benefit, a SPEI compensation, a point-of-sale
// deposit.
func fixed(phrase string) func(string) (party, bool) {
	return func(description string) (party, bool) {
		return party{}, description == phrase
	}
}
