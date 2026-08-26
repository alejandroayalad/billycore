package nu

import (
	"fmt"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// parseCardPayment reads `¡Recibimos tu pago!` — a payment landing on the
// user's own Nu credit card. 90 artifacts.
//
// It is the sparsest of the four. There are no labels at all: the amount stands
// alone on its own line under "Recibimos el pago que hiciste a tu <product>
// por:", and there is no date anywhere in the body.
//
// # What this parser deliberately does not do
//
// It does not supply a date. DATA_MODEL.md §4.5 already specifies the fallback
// to the Source's delivery timestamp, and applying it here would mean this
// layer inventing a timestamp the artifact does not contain. OccurredAt stays
// zero and the caller applies the rule.
//
// The direction is OUTFLOW (D31): this is a debit paying down what the user
// owes on the card, so money left an account they hold. It is stated from the
// paying side, which is the only side this artifact describes — nothing here
// names the account the money came from.
//
// The same event seen from the card's side is a credit, and when the card
// statement arrives as a second Source it will say so. Collapsing the two into
// one movement is reconciliation's problem (DOMAIN.md §6), and D31 records why
// it is a sharp one: amount and time will match while direction contradicts,
// which is the shape §6 treats as grounds to block a match.
func parseCardPayment(text string) (parser.Extraction, error) {
	product, found := between(text, "el pago que hiciste a tu ", " por")
	if !found {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %s — no payment sentence", TemplateCardPayment, templateChanged)
	}

	// The amount is the first bare `$` line after the sentence that introduces
	// it. It is read positionally relative to that sentence rather than by line
	// index: the footer of this template carries "$0.00" and "$400.00" in a
	// regulatory disclosure, and a scan from the top of the document would find
	// those on some artifacts.
	raw, found := amountAfter(text, "el pago que hiciste a tu ")
	if !found {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %s — no amount below the payment sentence", TemplateCardPayment, templateChanged)
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
		Direction:    domain.Outflow,
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
