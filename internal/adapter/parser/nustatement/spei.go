package nustatement

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// The two openings of a SPEI detail block. Nu writes the direction in words,
// and the sign of the amount column states it a second time.
const (
	speiDeposit  = "Depósito SPEI,"
	speiTransfer = "Transferencia SPEI,"
)

// The phrases a SPEI detail states around the name of the other party. The
// parenthetical belongs to Nu and is not part of the name.
const (
	receivedFrom = "Del cliente "
	sentTo       = "Al cliente "
	unverified   = "(Dato no verificado por esta institución)"
)

// The two labelled values of a SPEI detail. The document names each one, so
// D34 rates both HIGH and no geometry is inferred (D58).
var (
	speiClock       = regexp.MustCompile(`Hora: (\d{1,2}:\d{2}:\d{2})`)
	speiTrackingKey = regexp.MustCompile(`Clave de rastreo ([^,]+)`)
)

// spei reads one SPEI movement, and reports if the row is one.
//
// The detail block below the row carries the fields. The whole block is joined
// first, because poppler can split one visual line into two elements that share
// a y — package bbox already puts them in reading order.
//
// The CLABE, the concept and the reference stay unread. Each one is a
// vocabulary decision of its own, and the artifact keeps them for a later
// reading (D48, D61).
func spei(row bbox.Row) (map[domain.FieldName]domain.ClaimField, bool, error) {
	detail := strings.Join(row.Detail, " ")

	var marker string
	var stated domain.TransactionDirection
	switch {
	case strings.HasPrefix(detail, speiDeposit):
		marker, stated = receivedFrom, domain.Inflow
	case strings.HasPrefix(detail, speiTransfer):
		marker, stated = sentTo, domain.Outflow
	default:
		return nil, false, nil
	}

	money, direction, err := movement(row.Amount)
	if err != nil {
		return nil, true, err
	}
	// Two facts state the direction, and they must agree. A deposit that leaves
	// the account is drift in the document and not a movement.
	if direction != stated {
		return nil, true, fmt.Errorf("nustatement: %s: the detail states %s and the amount column states %s",
			templateChanged, stated, direction)
	}
	counterparty, err := speiCounterparty(detail, marker)
	if err != nil {
		return nil, true, err
	}
	when, err := occurredAt(row.Date, clockOf(detail))
	if err != nil {
		return nil, true, err
	}

	b := builder{out: map[domain.FieldName]domain.ClaimField{}}
	b.integer(domain.FieldAmountMinor, money.Minor(), domain.High)
	b.text(domain.FieldCurrency, string(money.Currency()), domain.Low)
	b.text(domain.FieldDirection, string(direction), domain.High)
	b.text(domain.FieldCounterparty, counterparty, domain.High)
	b.moment(domain.FieldOccurredAt, when, domain.High)

	// The key is optional. A movement without one is still a movement, so the
	// field is absent rather than empty — the same rule occurred_at follows
	// where an artifact states no time (D33).
	if key := trackingKey(detail); key != "" {
		b.text(domain.FieldTrackingKey, key, domain.High)
	}
	if b.err != nil {
		return nil, true, fmt.Errorf("nustatement: %s: %w", templateChanged, b.err)
	}
	return b.out, true, nil
}

// speiCounterparty reads the person or the institution the detail names (D61).
// It is never a merchant: nothing was bought.
func speiCounterparty(detail, marker string) (string, error) {
	i := strings.Index(detail, marker)
	if i < 0 {
		return "", fmt.Errorf("nustatement: %s: the detail names no %q", templateChanged, strings.TrimSpace(marker))
	}
	rest := detail[i+len(marker):]
	j := strings.Index(rest, unverified)
	if j < 0 {
		return "", fmt.Errorf("nustatement: %s: the name of the other party has no end", templateChanged)
	}
	name := strings.TrimSpace(rest[:j])
	if name == "" {
		return "", fmt.Errorf("nustatement: %s: the detail names nobody", templateChanged)
	}
	return name, nil
}

// trackingKey reads the `Clave de rastreo` verbatim, or "" where the detail
// states none. Billy does not validate the shape of another system's
// identifier (D36).
func trackingKey(detail string) string {
	m := speiTrackingKey.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// clockOf reads the `Hora:` value of a detail block, or "" where there is none.
func clockOf(detail string) string {
	m := speiClock.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	return m[1]
}
