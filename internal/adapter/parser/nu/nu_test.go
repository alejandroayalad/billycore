package nu_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"
	"github.com/alejandroayalad/billycore/internal/domain"
)

func TestParseTemplates(t *testing.T) {
	tests := []struct {
		fixture string
		subject string
		want    parser.Extraction
	}{
		{
			fixture: "outflow_rich.html",
			subject: "Tu transferencia fue exitosa",
			want: parser.Extraction{
				Template:  nu.TemplateTransferOut,
				Direction: domain.Outflow,
				Amount:    mxn(t, 100000),
				// 17:44 in Mexico City on 16 August is 23:44 UTC.
				OccurredAt:              time.Date(2026, 8, 16, 23, 44, 0, 0, time.UTC),
				Counterparty:            "Persona Dos",
				CounterpartyInstitution: "HSBC",
				CounterpartyCard:        "••••0000",
				Concept:                 "Transferencia",
				Reference:               "010101",
				Folio:                   "TESTFOLIO1",
				TrackingKey:             "NUTESTTRACKINGKEY00000000000",
				Status:                  "Completada",
			},
		},
		{
			fixture: "outflow_legacy.html",
			subject: "Tu transferencia fue exitosa",
			want: parser.Extraction{
				Template:  nu.TemplateTransferOut,
				Direction: domain.Outflow,
				Amount:    mxn(t, 49800),
				// 21:16 on 20 July is 03:16 UTC the following day — the case
				// that makes D29 worth having.
				OccurredAt:              time.Date(2026, 7, 21, 3, 16, 0, 0, time.UTC),
				Counterparty:            "Persona Tres",
				CounterpartyInstitution: "NU MEXICO",
			},
		},
		{
			fixture: "inflow.html",
			subject: "¡Recibiste una transferencia!",
			want: parser.Extraction{
				Template:     nu.TemplateTransferIn,
				Direction:    domain.Inflow,
				Amount:       mxn(t, 29900),
				OccurredAt:   time.Date(2026, 8, 19, 0, 54, 0, 0, time.UTC),
				Counterparty: "PERSONA UNO DE PRUEBA",
			},
		},
		{
			fixture: "card_payment.html",
			subject: "¡Recibimos tu pago!",
			want: parser.Extraction{
				Template: nu.TemplateCardPayment,
				Amount:   mxn(t, 112025),
				// No Direction and no OccurredAt, both deliberately: see
				// card_payment.go.
				Counterparty: "Tarjeta Garantizada Nu",
			},
		},
		{
			fixture: "service_payment.html",
			subject: "Tu comprobante de pago de servicio",
			want: parser.Extraction{
				Template:       nu.TemplateServicePayment,
				Direction:      domain.Outflow,
				Amount:         mxn(t, 90900),
				OccurredAt:     time.Date(2026, 7, 18, 16, 3, 51, 0, time.UTC),
				Counterparty:   "Servicio De Prueba",
				ServiceAccount: "0000000000",
				OperationCode:  "00000000-0000-4000-8000-000000000000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, err := nu.Parse(message(t, tt.subject, tt.fixture))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tt.want {
				t.Errorf("Parse(%s)\n got %+v\nwant %+v", tt.fixture, got, tt.want)
			}
		})
	}
}

// The service payment receipt labels the *payer* `Nombre:`, where the outflow
// receipt labels the *beneficiary* with it. Reading one as the other would
// attribute every utility payment to the user, and would look entirely
// plausible in a table.
func TestServicePaymentDoesNotTakeNombreAsTheCounterparty(t *testing.T) {
	got, err := nu.Parse(message(t, "Tu comprobante de pago de servicio", "service_payment.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Counterparty == "Persona Uno de Prueba" {
		t.Error("the payer was recorded as the counterparty")
	}
	if got.Counterparty != "Servicio De Prueba" {
		t.Errorf("counterparty = %q, want the merchant", got.Counterparty)
	}
}

// The masked card on an outflow receipt is the beneficiary's, not the user's.
// Recording it as the user's account would poison DOMAIN.md §6's account
// signal, which treats a known-account contradiction as grounds to block
// reconciliation.
func TestOutflowCardBelongsToTheCounterparty(t *testing.T) {
	got, err := nu.Parse(message(t, "Tu transferencia fue exitosa", "outflow_rich.html"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CounterpartyCard != "••••0000" {
		t.Errorf("CounterpartyCard = %q", got.CounterpartyCard)
	}
	if got.ServiceAccount != "" {
		t.Error("the beneficiary's card was recorded as an account of the user's")
	}
}

func TestParseRefusesUnrecognisedSubjects(t *testing.T) {
	for _, subject := range []string{
		"Agregaste un contacto a tu cuenta",
		"Ya está disponible el estado de cuenta de tu tarjeta de crédito Nu 🙂",
		"Cambiaste el límite de tu tarjeta",
		"¡No te quedes atrás! Entérate de las novedades de Nu",
		"",
		"Tu transferencia fue exitosa ", // a trailing space is still the subject
		"tu transferencia fue exitosa",  // a different case is not
	} {
		t.Run(subject, func(t *testing.T) {
			_, err := nu.Parse(message(t, subject, "outflow_rich.html"))
			switch subject {
			case "Tu transferencia fue exitosa ":
				if err != nil {
					t.Errorf("a trailing space should not defeat template selection: %v", err)
				}
			default:
				if !errors.Is(err, parser.ErrNoTemplate) {
					t.Errorf("got %v, want ErrNoTemplate", err)
				}
			}
		})
	}
}

// A recognised template that has lost a field it always carried is a template
// change, and it must fail loudly rather than yield a Transaction with a hole
// in it.
func TestParseRefusesADamagedTemplate(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		body    string
	}{
		{"outflow with no amount", "Tu transferencia fue exitosa",
			"<p>Fecha: 16/AGO/2026</p><p>Hora: 17:44</p><p>Nombre: Persona Dos</p>"},
		{"outflow with no date", "Tu transferencia fue exitosa",
			"<p>Monto: $10.00</p><p>Hora: 17:44</p><p>Nombre: Persona Dos</p>"},
		{"outflow with no time", "Tu transferencia fue exitosa",
			"<p>Monto: $10.00</p><p>Fecha: 16/AGO/2026</p><p>Nombre: Persona Dos</p>"},
		{"outflow with no counterparty", "Tu transferencia fue exitosa",
			"<p>Monto: $10.00</p><p>Fecha: 16/AGO/2026</p><p>Hora: 17:44</p>"},
		{"outflow with an unparseable amount", "Tu transferencia fue exitosa",
			"<p>Monto: $10</p><p>Fecha: 16/AGO/2026</p><p>Hora: 17:44</p><p>Nombre: X</p>"},
		{"inflow with no sender", "¡Recibiste una transferencia!",
			"<p>Monto: $10.00</p><p>Fecha: 18 AGO 2026</p><p>Hora: 18:54</p>"},
		{"card payment with no sentence", "¡Recibimos tu pago!", "<p>$10.00</p>"},
		{"card payment with no amount", "¡Recibimos tu pago!",
			"<p>Recibimos el pago que hiciste a tu Tarjeta Nu por:</p>"},
		{"service payment with no timestamp", "Tu comprobante de pago de servicio",
			"<p>Monto: $10.00</p><p>Empresa a la cual se realizará el pago: X</p>"},
		{"service payment with no merchant", "Tu comprobante de pago de servicio",
			"<p>18 jul 2026 - 10:03:51</p><p>Monto: $10.00</p>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nu.Parse(wrap(tt.subject, tt.body))
			if err == nil {
				t.Fatalf("Parse accepted a damaged template, returning %+v", got)
			}
			if errors.Is(err, parser.ErrNoTemplate) {
				t.Errorf("the template was recognised, so the error should say what is missing: %v", err)
			}
		})
	}
}

// A zero amount is a movement of nothing, which is not a movement.
func TestParseRefusesAZeroAmount(t *testing.T) {
	_, err := nu.Parse(wrap("Tu transferencia fue exitosa",
		"<p>Monto: $0.00</p><p>Fecha: 16/AGO/2026</p><p>Hora: 17:44</p><p>Nombre: X</p>"))
	if err == nil {
		t.Fatal("Parse accepted a zero amount")
	}
}

// --- fixtures ---------------------------------------------------------------

// message wraps a scrubbed body from the parser package's testdata in the
// minimum RFC 822 needed to select a template.
//
// The fixtures live one directory up because they are the same five real Nu
// bodies the text extractor is tested against, and one copy of 160 KB of real
// markup is enough. Wrapping them here keeps the MIME concerns in the package
// that owns them.
func message(t *testing.T, subject, fixture string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return wrap(subject, string(body))
}

func wrap(subject, body string) []byte {
	return []byte(fmt.Sprintf(
		"From: Nu <nu@nu.com.mx>\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: text/html; charset=\"utf-8\"\r\n\r\n%s", subject, body))
}

func mxn(t *testing.T, minor int64) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(minor, nu.Currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
