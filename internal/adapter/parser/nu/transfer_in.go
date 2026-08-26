package nu

import (
	"fmt"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// parseTransferIn reads `¡Recibiste una transferencia!` — money the user
// received. 345 artifacts, one layout throughout.
//
// It carries Monto, Fecha and Hora on labelled lines and nothing else: no
// folio, no Clave de rastreo, no institution. That absence is the fact that
// sinks CONTEXT.md §3.1's hope of using the SPEI tracking key as the
// reconciliation backbone — the key never appears on the receiving side, so the
// two halves of a transfer between the user's own accounts cannot be matched by
// it.
//
// The sender is written into a sentence: "<name> hizo una transferencia a tu
// Cuenta Nu por:".
func parseTransferIn(text string) (parser.Extraction, error) {
	money, err := amount(text, "Monto:")
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateTransferIn, err)
	}
	when, err := occurredAt(text)
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateTransferIn, err)
	}

	sender, _ := before(text, " hizo una transferencia a tu")

	out := parser.Extraction{
		Template:     TemplateTransferIn,
		Direction:    domain.Inflow,
		Amount:       money,
		OccurredAt:   when,
		Counterparty: sender,
	}
	if err := missing(TemplateTransferIn, map[string]string{
		"counterparty": out.Counterparty,
	}); err != nil {
		return parser.Extraction{}, err
	}
	return out, nil
}
