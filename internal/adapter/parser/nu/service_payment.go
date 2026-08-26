package nu

import (
	"fmt"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// parseServicePayment reads `Tu comprobante de pago de servicio` — a utility
// bill paid out of the user's Nu account. 11 artifacts.
//
// Its labels are the richest of the four and its trap is the sharpest:
// `Nombre:` here is the *payer*, the user, where on the outflow receipt the
// identical label is the beneficiary. The counterparty on this template is
// `Empresa a la cual se realizará el pago:`, and reading `Nombre:` as the
// counterparty would silently attribute every utility payment to the user
// themselves. One parser per template exists for exactly this (D11).
//
// The timestamp is unlabelled prose — "18 jul 2026 - 10:03:51" alone on a line.
func parseServicePayment(text string) (parser.Extraction, error) {
	money, err := amount(text, "Monto:")
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateServicePayment, err)
	}

	// The date is found by parsing rather than by position: the first line the
	// date parser accepts whole is it. Anchoring on the heading above it would
	// bind to a sentence Nu can reword — "Aquí está tu comprobante de tu pago
	// de pago de servicio" is already a typo Nu may one day fix — and anchoring
	// on a line number would bind to a layout that is not stable. A line that
	// is nothing but a timestamp is unambiguous on its own.
	wall, found := firstTimestampLine(text)
	if !found {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %s — no timestamp line", TemplateServicePayment, templateChanged)
	}

	// `Costo extra:` is deliberately not read. It is a fee, and a fee is a
	// second movement of money, not a property of this one — modelling it is a
	// domain question this layer does not open.
	out := parser.Extraction{
		Template:       TemplateServicePayment,
		Direction:      domain.Outflow,
		Amount:         money,
		OccurredAt:     wall.In(zone).UTC(),
		Counterparty:   optional(text, "Empresa a la cual se realizará el pago:"),
		ServiceAccount: optional(text, "Número de cuenta:"),
		OperationCode:  optional(text, "Código de operación:"),
	}
	if err := missing(TemplateServicePayment, map[string]string{
		"counterparty": out.Counterparty,
	}); err != nil {
		return parser.Extraction{}, err
	}
	return out, nil
}

// firstTimestampLine returns the first line that is entirely a date and time.
func firstTimestampLine(text string) (parser.Wall, bool) {
	for _, line := range strings.Split(text, "\n") {
		wall, err := parser.ParseWall(line)
		if err == nil && wall.HasTime {
			return wall, true
		}
	}
	return parser.Wall{}, false
}
