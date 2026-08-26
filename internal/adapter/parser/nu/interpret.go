package nu

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// Interpreter turns a Nu artifact into the fields of a Claim.
//
// It implements app.Interpreter, and it is the one place the confidence rule of
// D34 is applied. That rule is about *how the artifact yielded a value*, not
// about what the value is:
//
//	HIGH    the artifact states it on a labelled line
//	MEDIUM  the artifact implies it — read from prose, from position, or from
//	        the template's own identity
//	LOW     no artifact states it at all; Billy inferred it from the Source
//
// Which is why this lives beside the template parsers rather than in
// internal/app: only the code that did the reading knows which of those
// happened, and the use case above must not re-derive it by guessing the layout
// from which other fields happen to be present.
type Interpreter struct{}

// Interpret reads one stored artifact and reports what Billy believes about it.
//
// It returns app.ErrNoInterpretation for the 244 artifacts no template
// recognises, which is an ordinary outcome and not a failure.
func (Interpreter) Interpret(raw []byte) (map[domain.FieldName]domain.ClaimField, error) {
	extraction, err := Parse(raw)
	if errors.Is(err, parser.ErrNoTemplate) {
		// Translated at the boundary. The use case knows "nothing recognised
		// this"; it does not know that recognising means matching an email
		// subject line.
		return nil, app.ErrNoInterpretation
	}
	if err != nil {
		return nil, classify(err)
	}
	fields, err := fieldsOf(extraction)
	if err != nil {
		return nil, classify(err)
	}
	return fields, nil
}

// interpretError carries a failure together with a rendering of it that holds
// no artifact.
//
// It exists because a parser error is not safe to store or log. ParseMoney
// renders the text it choked on, and that text is a substring of the email:
//
//	parser: not an amount: "Monto: $1,0O0.00"
//
// The quoted half is exactly what SECURITY.md §10 forbids putting in last_error.
// Error() keeps the full text for a caller debugging in front of the artifact;
// Redacted() is what the pipeline stores, and the pipeline never calls Error().
type interpretError struct {
	class string
	err   error
}

func (e interpretError) Error() string    { return e.err.Error() }
func (e interpretError) Unwrap() error    { return e.err }
func (e interpretError) Redacted() string { return e.class }

// classify names the failure without repeating what caused it.
//
// Every value parser in this package wraps a sentinel, which is what makes this
// possible: the classification reads the error's identity rather than its text,
// so it cannot accidentally carry the input along with the reason. An error
// matching nothing gets the vaguest class rather than its own message — the
// unknown case is the one most likely to be carrying something.
func classify(err error) error {
	switch {
	case errors.Is(err, parser.ErrNotMoney), errors.Is(err, parser.ErrUnsupportedFmt):
		return interpretError{class: "the amount did not parse", err: err}
	case errors.Is(err, parser.ErrNotDate):
		return interpretError{class: "the timestamp did not parse", err: err}
	case errors.Is(err, parser.ErrNoHTMLPart):
		return interpretError{class: "the artifact has no html part", err: err}
	case errors.Is(err, parser.ErrTooLarge):
		return interpretError{class: "the artifact exceeds the size cap", err: err}
	case strings.Contains(err.Error(), templateChanged):
		// The drift case, and the one worth naming: a recognised template that
		// stopped carrying a field it always carried. Matched on our own
		// constant rather than a sentinel because the parsers report it
		// per-field; the class stored is fixed text either way, so no part of
		// the artifact travels with it.
		return interpretError{class: "the template changed", err: err}
	default:
		return interpretError{class: "the artifact could not be read", err: err}
	}
}

// fieldsOf applies D34, D35 and D36 to one extraction.
//
// A field Billy cannot support is *absent*, never present-and-empty and never
// an "UNKNOWN" placeholder. DOMAIN.md §7 requires "Billy has no belief" to stay
// distinguishable from "Billy believes it weakly", and DATA_MODEL.md §4.4 makes
// that physical: no belief, no row.
func fieldsOf(e parser.Extraction) (map[domain.FieldName]domain.ClaimField, error) {
	b := builder{out: map[domain.FieldName]domain.ClaimField{}}

	// amount_minor — labelled `Monto:` on three templates, and on the card
	// payment a bare `$` line anchored to the sentence above it. The value is
	// read just as exactly; the *anchor* is weaker, which is what MEDIUM says.
	b.integer(domain.FieldAmountMinor, e.Amount.Minor(), amountConfidence(e.Template))

	// currency — LOW, always (D34, closing D30's open item). MXN appears zero
	// times in 1,044 artifacts: this is not a field Billy read, it is an
	// inference about the Source, and the '$' glyph is not evidence because
	// many currencies use it. Billy asserts it, and says plainly that the
	// artifact did not.
	b.text(domain.FieldCurrency, string(e.Amount.Currency()), domain.Low)

	// direction — HIGH on all four. The template's identity settles it: a
	// `Tu transferencia fue exitosa` is an outflow because of what the message
	// *is*, not because of a field that might have been misread. The card
	// payment is HIGH too, on D31's reasoning rather than on a parsed value.
	b.text(domain.FieldDirection, string(e.Direction), domain.High)

	// merchant — the counterparty as the artifact wrote it, never normalised
	// (DOMAIN.md Q2 is open). HIGH where a label anchored it, MEDIUM where a
	// sentence did.
	//
	// The card payment is deliberately absent. Its "counterparty" is the user's
	// own card product — "Cuenta Nu" — which is not a counterparty at all, and
	// writing it as a merchant would put a plausible-looking wrong answer on 90
	// rows. No belief is the honest record.
	if e.Template != TemplateCardPayment {
		b.text(domain.FieldMerchant, e.Counterparty, labelConfidence(e.CounterpartyLabelled))
	}

	// occurred_at — HIGH where `Fecha:` and `Hora:` labelled it, MEDIUM for the
	// service payment, whose timestamp is unlabelled prose found by being the
	// first line that parses whole as one.
	//
	// Absent on all 90 card payments, which carry no body date. That absence is
	// the whole reason D33 put occurred_at in the vocabulary: DATA_MODEL.md
	// §4.5's fallback to the delivery timestamp fires when the Transaction is
	// built, and inventing a midnight here would rob it of the chance.
	if !e.OccurredAt.IsZero() {
		b.moment(domain.FieldOccurredAt, e.OccurredAt, occurredAtConfidence(e.Template))
	}

	// financial_status — SETTLED on the outflow receipt and nowhere else (D35).
	//
	// The subject line is the assertion: `Tu transferencia fue exitosa` says the
	// transfer succeeded, and a SPEI transfer that succeeded is final. HIGH on
	// the 16 rich-layout artifacts that also carry `Estatus: Completada`, MEDIUM
	// on the 338 where the belief rests on the subject alone.
	//
	// The other three templates get no row. An inflow receipt and a card
	// payment receipt state nothing about settlement, and DOMAIN.md §5's
	// UNKNOWN is what the Transaction defaults to without Billy claiming it.
	if e.Template == TemplateTransferOut {
		b.text(domain.FieldFinancialStatus, string(domain.StatusSettled), labelConfidence(e.Status != ""))
	}

	// tracking_key — the SPEI `Clave de rastreo` (D36), labelled, on 16
	// artifacts and never an inflow. Optional: most artifacts carry none, and
	// that is absence rather than drift.
	if e.TrackingKey != "" {
		b.text(domain.FieldTrackingKey, e.TrackingKey, domain.High)
	}

	// account_identifier is never claimed, on any template. Nu's artifacts do
	// carry account-shaped values — `Tarjeta de débito: ••••7662` and the
	// service payment's `Número de cuenta:` — and both belong to the *other*
	// party: one sits in the recipient block beside Nombre and Entidad, the
	// other is a utility contract number. DOMAIN.md §6 treats a known-account
	// contradiction as grounds to block reconciliation, so recording either as
	// the user's account would not merely be wrong — it would actively prevent
	// correct matches later (CONTEXT.md §3.1).

	if b.err != nil {
		return nil, fmt.Errorf("nu: %s: %w", e.Template, b.err)
	}
	return b.out, nil
}

// builder collects fields and the first reason one could not be built.
//
// Every call here passes a confidence that is a constant of this file, and a
// value a template parser already validated, so a failure means the parser
// changed and stopped honouring its own contract — a template that quietly
// dropped a field it always carried. That is worth an error rather than a
// silently shorter Claim, which is why nothing here ignores one.
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

// amountConfidence: labelled on three templates, positional on the fourth.
func amountConfidence(t parser.Template) domain.Confidence {
	if t == TemplateCardPayment {
		return domain.Medium
	}
	return domain.High
}

// occurredAtConfidence: `Fecha:`/`Hora:` are labels; the service payment's
// timestamp is a bare line.
func occurredAtConfidence(t parser.Template) domain.Confidence {
	if t == TemplateServicePayment {
		return domain.Medium
	}
	return domain.High
}

// labelConfidence is D34's central distinction in one line: a label the
// template would have to change to break, versus prose it can be reworded out
// of.
func labelConfidence(labelled bool) domain.Confidence {
	if labelled {
		return domain.High
	}
	return domain.Medium
}

// Interpreter satisfies the port it was built for, checked at compile time.
var _ app.Interpreter = Interpreter{}
