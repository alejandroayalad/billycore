package nu

import (
	"fmt"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// parseTransferOut reads `Tu transferencia fue exitosa` — money the user sent.
//
// One subject, two layouts. Nu changed the template on 2026-07-22, and both are
// in the corpus:
//
//   - lean, 338 artifacts, 2024-01-25 to 2026-07-21: Monto, Fecha, Hora, and
//     the beneficiary written into a sentence. Encoded iso-8859-1.
//   - rich, 16 artifacts, 2026-07-23 onward: adds Folio, Nombre, Entidad,
//     Tarjeta de débito, Estatus, Concepto, Número de referencia and the SPEI
//     Clave de rastreo, each on its own labelled line.
//
// CONTEXT.md §3.1's field table describes the rich layout as if it were all
// 354. It is not — though the MVP month is entirely rich, which is why the
// mismatch has not bitten yet.
//
// The two share a parser rather than being split, because they agree on
// everything the lean one carries: the rich fields are additive, and every one
// of them is read as optional. A split would duplicate the required half to
// express a difference that is already expressed by absence.
func parseTransferOut(text string) (parser.Extraction, error) {
	money, err := amount(text, "Monto:")
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateTransferOut, err)
	}
	when, err := occurredAt(text)
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %s: %w", TemplateTransferOut, err)
	}

	out := parser.Extraction{
		Template:   TemplateTransferOut,
		Direction:  domain.Outflow,
		Amount:     money,
		OccurredAt: when,

		Concept:          optional(text, "Concepto:"),
		Reference:        optional(text, "Número de referencia:"),
		Folio:            optional(text, "Folio:"),
		TrackingKey:      optional(text, "Clave de rastreo:"),
		Status:           optional(text, "Estatus:"),
		CounterpartyCard: optional(text, "Tarjeta de débito:"),
	}

	// The beneficiary. The rich layout labels it; the lean one writes it into
	// "…la transferencia que hiciste a la cuenta de <name> en <bank> fue
	// exitosa." Both are read, and the labelled one wins where it exists,
	// because a label is a stronger anchor than a sentence Nu may rephrase.
	if name, found := labelled(text, "Nombre:"); found {
		out.Counterparty = name
		out.CounterpartyLabelled = true
		out.CounterpartyInstitution = optional(text, "Entidad:")
	} else if name, found := between(text, "a la cuenta de ", " en "); found {
		out.Counterparty = name
		if bank, found := between(text, " en ", " fue exitosa"); found {
			out.CounterpartyInstitution = bank
		}
	}

	if err := missing(TemplateTransferOut, map[string]string{
		"counterparty": out.Counterparty,
	}); err != nil {
		return parser.Extraction{}, err
	}
	return out, nil
}
