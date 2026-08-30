package nustatement

import (
	"fmt"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// internalShapes are the rows that move money between accounts of one user
// (D55, D63). A Cajita is a savings pot of the same user, and the backing money
// is money the user already holds.
//
// The sign belongs to the shape. A statement prints each internal movement two
// times: once in `Detalle de movimientos en tu cuenta` with the sign below, and
// once in the section of the pot with the same words and the other sign. The
// two rows are one movement seen from two sides, so only the account row is
// read. Measured on the May, June and July 2026 statements: 84 movements
// printed as 168 rows.
var internalShapes = []struct {
	description string
	sign        domain.TransactionDirection
}{
	{"Retiro de Cajita:", domain.Inflow},
	{"Depósito en Cajita:", domain.Outflow},
	{"Retirado de tu dinero de respaldo", domain.Inflow},
}

// internalMovement reads one movement between accounts of one user, and reports
// if the row is one.
//
// The counterparty is the reserved value, because the user is on both sides
// (D62). No merchant is claimed: a Cajita is not a place where something was
// bought (D61).
//
// The cover page also prints `Dinero de respaldo` as a balance. It carries no
// date and no sign, so the row assembler makes no row from it (D63).
func internalMovement(row bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error) {
	description := strings.Join(row.Description, " ")
	stated, named := internalSign(description)
	if !named {
		return nil, false, nil
	}

	money, direction, err := movement(row.Amount)
	if err != nil {
		return nil, true, err
	}
	// The mirror row of the pot. It states the same movement from the other
	// side, and reading it as well would record the money two times.
	if direction != stated {
		return nil, false, nil
	}
	// The row states a day and no time of day (D64).
	when, err := occurredAt(row.Date, "")
	if err != nil {
		return nil, true, err
	}

	b := builder{out: map[domain.FieldName]domain.ClaimField{}}
	b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
	b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
	b.text(domain.FieldDirection, string(direction), domain.High)

	// HIGH, and not read from the document. The shape of the row is the whole
	// support for the value, and the shape is not a guess.
	b.text(domain.FieldCounterparty, domain.CounterpartySelf, domain.High)

	b.moment(domain.FieldOccurredAt, when, domain.High)
	if b.err != nil {
		return nil, true, fmt.Errorf("nustatement: %s: %w", templateChanged, b.err)
	}
	return b.out, true, nil
}

// internalSign reports the direction that one internal shape carries in the
// account section, and if the description names such a shape. The name of a
// Cajita is chosen by the user and varies for each row, so the match is a
// prefix.
func internalSign(description string) (domain.TransactionDirection, bool) {
	for _, shape := range internalShapes {
		if strings.HasPrefix(description, shape.description) {
			return shape.sign, true
		}
	}
	return "", false
}
