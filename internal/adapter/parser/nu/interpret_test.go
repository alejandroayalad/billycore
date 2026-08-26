package nu_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// interpretFixture runs the interpreter over one stored fixture, wrapped as a
// whole RFC 822 message the way the corpus stores it.
func interpretFixture(t *testing.T, fixture, subject string) map[domain.FieldName]domain.ClaimField {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	raw := []byte("Subject: " + subject + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + string(body))

	fields, err := nu.Interpreter{}.Interpret(raw)
	if err != nil {
		t.Fatalf("Interpret(%s): %v", fixture, err)
	}
	return fields
}

func text(t *testing.T, fields map[domain.FieldName]domain.ClaimField, name domain.FieldName) (string, domain.Confidence) {
	t.Helper()
	f, ok := fields[name]
	if !ok {
		t.Fatalf("no %s field", name)
	}
	return f.Text(), f.Confidence()
}

// D34's rule, on the template that exercises every branch of it.
func TestConfidenceFollowsHowTheArtifactYieldedTheValue(t *testing.T) {
	fields := interpretFixture(t, "outflow_rich.html", "Tu transferencia fue exitosa")

	amount, ok := fields[domain.FieldAmountMinor]
	if !ok {
		t.Fatal("no amount_minor field")
	}
	if amount.Int() != 100000 || amount.Confidence() != domain.High {
		t.Errorf("amount_minor = %d at %s, want 100000 at HIGH (labelled `Monto:`)", amount.Int(), amount.Confidence())
	}

	// LOW, and it is the whole of D34's answer to D30: no artifact anywhere in
	// the corpus states a currency. Billy asserts MXN and says plainly that it
	// inferred it.
	if v, c := text(t, fields, domain.FieldCurrency); v != "MXN" || c != domain.Low {
		t.Errorf("currency = %q at %s, want MXN at LOW", v, c)
	}
	if v, c := text(t, fields, domain.FieldDirection); v != "OUTFLOW" || c != domain.High {
		t.Errorf("direction = %q at %s, want OUTFLOW at HIGH", v, c)
	}
	// `Nombre:` labelled it.
	if v, c := text(t, fields, domain.FieldMerchant); v != "Persona Dos" || c != domain.High {
		t.Errorf("merchant = %q at %s, want a labelled name at HIGH", v, c)
	}
	// `Fecha:` and `Hora:` labelled it.
	if _, c := text(t, fields, domain.FieldOccurredAt); c != domain.High {
		t.Errorf("occurred_at confidence = %s, want HIGH", c)
	}
	// D35: SETTLED, HIGH because this artifact also carries `Estatus: Completada`.
	if v, c := text(t, fields, domain.FieldFinancialStatus); v != "SETTLED" || c != domain.High {
		t.Errorf("financial_status = %q at %s, want SETTLED at HIGH", v, c)
	}
	// D36.
	if v, c := text(t, fields, domain.FieldTrackingKey); v != "NUTESTTRACKINGKEY00000000000" || c != domain.High {
		t.Errorf("tracking_key = %q at %s", v, c)
	}
}

// The lean layout carries the same facts read more weakly: a sentence rather
// than a label, and no `Estatus:` line.
func TestAProseReadingIsMediumNotHigh(t *testing.T) {
	fields := interpretFixture(t, "outflow_legacy.html", "Tu transferencia fue exitosa")

	if v, c := text(t, fields, domain.FieldMerchant); v != "Persona Tres" || c != domain.Medium {
		t.Errorf("merchant = %q at %s, want a prose-read name at MEDIUM", v, c)
	}
	// D35: still SETTLED — the subject line is the assertion — but MEDIUM,
	// because nothing on the page says `Completada`.
	if v, c := text(t, fields, domain.FieldFinancialStatus); v != "SETTLED" || c != domain.Medium {
		t.Errorf("financial_status = %q at %s, want SETTLED at MEDIUM", v, c)
	}
	if _, ok := fields[domain.FieldTrackingKey]; ok {
		t.Error("the lean layout carries no tracking key; Billy claimed one anyway")
	}
}

// The card payment is the template that tests absence, which is the thing this
// slice is most able to get quietly wrong.
func TestTheCardPaymentClaimsNoDateAndNoMerchant(t *testing.T) {
	fields := interpretFixture(t, "card_payment.html", "¡Recibimos tu pago!")

	// D33: no row. Not midnight, not the ingestion time.
	if _, ok := fields[domain.FieldOccurredAt]; ok {
		t.Error("a card payment carries no body date; Billy claimed one anyway")
	}
	// Its "counterparty" is the user's own card product, which is not a
	// counterparty at all.
	if _, ok := fields[domain.FieldMerchant]; ok {
		t.Error("a card payment has no merchant; Billy claimed one anyway")
	}
	// D35: an inflow or card receipt states nothing about settlement.
	if _, ok := fields[domain.FieldFinancialStatus]; ok {
		t.Error("a card payment states no financial status; Billy claimed one anyway")
	}
	// The amount is read positionally, not from a label.
	amount, ok := fields[domain.FieldAmountMinor]
	if !ok {
		t.Fatal("no amount_minor field")
	}
	if amount.Confidence() != domain.Medium {
		t.Errorf("amount confidence = %s, want MEDIUM (a bare line, not a label)", amount.Confidence())
	}
	if v, c := text(t, fields, domain.FieldDirection); v != "OUTFLOW" || c != domain.High {
		t.Errorf("direction = %q at %s, want OUTFLOW at HIGH (D31)", v, c)
	}
}

func TestTheServicePaymentTimestampIsMedium(t *testing.T) {
	fields := interpretFixture(t, "service_payment.html", "Tu comprobante de pago de servicio")

	// Unlabelled prose, found by being the first line that parses whole as a
	// timestamp.
	if _, c := text(t, fields, domain.FieldOccurredAt); c != domain.Medium {
		t.Errorf("occurred_at confidence = %s, want MEDIUM", c)
	}
	// `Empresa a la cual se realizará el pago:` labelled it — and `Nombre:` on
	// this template is the payer, which must not have been read instead.
	if v, c := text(t, fields, domain.FieldMerchant); v != "Servicio De Prueba" || c != domain.High {
		t.Errorf("merchant = %q at %s", v, c)
	}
	if _, ok := fields[domain.FieldFinancialStatus]; ok {
		t.Error("a service payment states no financial status; Billy claimed one anyway")
	}
}

func TestTheInflowClaimsNoStatusAndNoTrackingKey(t *testing.T) {
	fields := interpretFixture(t, "inflow.html", "¡Recibiste una transferencia!")

	if v, c := text(t, fields, domain.FieldDirection); v != "INFLOW" || c != domain.High {
		t.Errorf("direction = %q at %s", v, c)
	}
	// The sender is written into a sentence.
	if _, c := text(t, fields, domain.FieldMerchant); c != domain.Medium {
		t.Errorf("merchant confidence = %s, want MEDIUM", c)
	}
	if _, ok := fields[domain.FieldFinancialStatus]; ok {
		t.Error("an inflow receipt states no financial status; Billy claimed one anyway")
	}
	// The key never appears on the receiving side, which is why it cannot be
	// the reconciliation backbone CONTEXT.md §3.1 hoped for.
	if _, ok := fields[domain.FieldTrackingKey]; ok {
		t.Error("an inflow carries no tracking key; Billy claimed one anyway")
	}
}

// Nu's artifacts carry account-shaped values, and every one of them belongs to
// the other party. Claiming one as the user's would not merely be wrong — it
// would block correct reconciliation later (DOMAIN.md §6, CONTEXT.md §3.1).
func TestNoTemplateClaimsAnAccountIdentifier(t *testing.T) {
	for _, tc := range []struct{ fixture, subject string }{
		{"outflow_rich.html", "Tu transferencia fue exitosa"},
		{"outflow_legacy.html", "Tu transferencia fue exitosa"},
		{"inflow.html", "¡Recibiste una transferencia!"},
		{"card_payment.html", "¡Recibimos tu pago!"},
		{"service_payment.html", "Tu comprobante de pago de servicio"},
	} {
		fields := interpretFixture(t, tc.fixture, tc.subject)
		if _, ok := fields[domain.FieldAccountIdentifier]; ok {
			t.Errorf("%s: claimed an account_identifier", tc.fixture)
		}
	}
}

// The 244 that carry no financial event are an ordinary outcome, and the use
// case has to tell them apart from a failure without knowing what a subject
// line is.
func TestAnUnrecognisedArtifactReportsNoInterpretation(t *testing.T) {
	raw := []byte("Subject: Tu estado de cuenta ya está disponible\r\n\r\n<html><body>nothing financial</body></html>")
	_, err := nu.Interpreter{}.Interpret(raw)
	if !errors.Is(err, app.ErrNoInterpretation) {
		t.Fatalf("err = %v, want app.ErrNoInterpretation", err)
	}
}

// The leak this guards against is real and specific: ParseMoney renders the
// text it choked on, so `err.Error()` on a malformed artifact contains a piece
// of the email. Whatever the pipeline stores must not (SECURITY.md §10).
func TestAFailureDescribesItselfWithoutTheArtifact(t *testing.T) {
	beneficiary := "Persona Dos"
	body := `<html><body>
		<p>Monto: $1,0O0.00</p>
		<p>Fecha: 16/AGO/2026</p>
		<p>Hora: 17:44</p>
		<p>Nombre: ` + beneficiary + `</p>
		<p>Tarjeta de d&eacute;bito: ` + "••••7662" + `</p>
	</body></html>`
	raw := []byte("Subject: Tu transferencia fue exitosa\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + body)

	_, err := nu.Interpreter{}.Interpret(raw)
	if err == nil {
		t.Fatal("a malformed amount parsed")
	}

	// The full error still carries the input, which is what makes it useful in
	// front of the artifact and unsafe to store.
	if !strings.Contains(err.Error(), "1,0O0.00") {
		t.Errorf("the full error lost its detail: %v", err)
	}

	var safe app.Redacted
	if !errors.As(err, &safe) {
		t.Fatalf("error does not describe itself safely: %T", err)
	}
	reason := safe.Redacted()
	for _, leak := range []string{"1,0O0.00", beneficiary, "7662", "Monto", "$"} {
		if strings.Contains(reason, leak) {
			t.Errorf("redacted reason %q carries %q from the artifact", reason, leak)
		}
	}
	if reason != "the amount did not parse" {
		t.Errorf("reason = %q", reason)
	}
}

// Template drift is the failure worth telling apart from a malformed artifact:
// it is the one that means Nu changed something and a parser needs writing.
func TestTemplateDriftIsClassifiedAsSuch(t *testing.T) {
	raw := []byte("Subject: ¡Recibiste una transferencia!\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<html><body><p>Monto: $500.00</p><p>Fecha: 18 AGO 2026</p><p>Hora: 10:00</p></body></html>`)

	_, err := nu.Interpreter{}.Interpret(raw)
	if err == nil {
		t.Fatal("an inflow with no sender parsed")
	}
	var safe app.Redacted
	if !errors.As(err, &safe) {
		t.Fatalf("error does not describe itself safely: %T", err)
	}
	if got := safe.Redacted(); got != "the template changed" {
		t.Errorf("reason = %q, want the drift class", got)
	}
}
