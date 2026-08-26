package nu

import (
	"fmt"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
)

// parseCardPayment reads `¡Recibimos tu pago!` — a payment landing on the
// user's own Nu credit card. 90 artifacts.
//
// It is the sparsest of the four. There are no labels at all: the amount stands
// alone on its own line under "Recibimos el pago que hiciste a tu <product>
// por:", and there is no date anywhere in the body.
//
// # Two things this parser deliberately does not do
//
// It does not supply a date. DATA_MODEL.md §4.5 already specifies the fallback
// to the Source's delivery timestamp, and applying it here would mean this
// layer inventing a timestamp the artifact does not contain. OccurredAt stays
// zero and the caller applies the rule.
//
// It does not set a Direction, and that is an open question rather than an
// oversight. This message is the user paying their own credit card: money
// leaves one account they own and lands on another. Whether that is an OUTFLOW,
// an INFLOW, both, or a transfer that should net to neither depends on which
// Account the Transaction is attributed to — which DOMAIN.md does not settle,
// and D21 says to stop at rather than pick. Direction is therefore absent, not
// guessed, and the 90 artifacts extract cleanly in every other respect.
func parseCardPayment(text string) (parser.Extraction, error) {
	product, found := between(text, "el pago que hiciste a tu ", " por")
	if !found {
		return parser.Extraction{}, fmt.Errorf("nu: %s: template changed — no payment sentence", TemplateCardPayment)
	}

	// The amount is the first bare `$` line after the sentence that introduces
	// it. It is read positionally relative to that sentence rather than by line
	// index: the footer of this template carries "$0.00" and "$400.00" in a
	// regulatory disclosure, and a scan from the top of the document would find
	// those on some artifacts.
	raw, found := amountAfter(text, "el pago que hiciste a tu ")
	if !found {
		return parser.Extraction{}, fmt.Errorf("nu: %s: template changed — no amount below the payment sentence", TemplateCardPayment)
	}
	money, err := parser.ParseMoney(raw, Currency)
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateCardPayment, err)
	}
	if money.Minor() <= 0 {
		return parser.Extraction{}, fmt.Errorf("nu: %s: amount is %d minor units", TemplateCardPayment, money.Minor())
	}

	return parser.Extraction{
		Template:     TemplateCardPayment,
		Amount:       money,
		Counterparty: product,
	}, nil
}

// amountAfter returns the first line at or below the marker that is nothing but
// an amount.
func amountAfter(text, marker string) (string, bool) {
	lines := strings.Split(text, "\n")
	from := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			from = i
			break
		}
	}
	if from < 0 {
		return "", false
	}
	for _, line := range lines[from:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "$") && !strings.ContainsAny(line, " \t") {
			return line, true
		}
	}
	return "", false
}
