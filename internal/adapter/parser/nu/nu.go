// Package nu reads Nu México's transactional email templates.
//
// Four subjects out of the 1,044 stored artifacts carry a financial event, and
// they are 800 of them. Each has its own parser in its own file, because the
// templates disagree in ways a shared one would paper over: `Nombre:` is the
// beneficiary on an outflow receipt and the *payer* on a service receipt, the
// same label meaning opposite things. One parser per template is D11, and this
// is why.
//
// Nothing here guesses. A subject no template claims returns
// [parser.ErrNoTemplate]; a recognised template missing a field it always
// carries returns an error rather than a partial [parser.Extraction], because a
// field that quietly went missing is a template change worth stopping for.
package nu

import (
	"fmt"
	"strings"
	"time"

	_ "time/tzdata" // D29: the zone must resolve on a host with no tz database

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// Templates, named by the subject line that selects them.
const (
	TemplateTransferOut    parser.Template = "NU_TRANSFER_OUT"
	TemplateTransferIn     parser.Template = "NU_TRANSFER_IN"
	TemplateCardPayment    parser.Template = "NU_CARD_PAYMENT"
	TemplateServicePayment parser.Template = "NU_SERVICE_PAYMENT"
)

// Currency is what a Nu amount is denominated in (D30).
//
// It is a constant rather than a parsed field because no Nu artifact states a
// currency: "MXN", "USD", "pesos" and "M.N." appear zero times across all
// 1,044. The '$' glyph is not evidence — many currencies use it. The reasoning
// is the Source: Nu México issues MXN accounts. This is the one place that
// inference is made, so a second Source does not inherit it by accident.
const Currency = domain.Currency("MXN")

// zone is the wall-clock zone every Nu timestamp is read in (D29).
//
// Nu names no zone anywhere. Reading these as UTC would move every transaction
// six hours — enough to push a late-evening transfer into the next day and a
// month-end one into the next month, which is the boundary the MVP's "last
// month of transactions" is drawn on.
var zone = func() *time.Location {
	loc, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		// Unreachable with time/tzdata linked in. Failing loudly at
		// initialisation beats silently falling back to UTC and being six
		// hours wrong on every row.
		panic("nu: America/Mexico_City is unavailable: " + err.Error())
	}
	return loc
}()

// bySubject routes an artifact to its template parser.
//
// Selection is by subject alone. SECURITY.md §8 is explicit that sender
// authentication is deliberately not a v1 signal, so a fabricated email
// carrying one of these subjects will be parsed as if Nu had sent it. The
// mitigation is the one §8 names and calls honest and weak: the Evidence is
// immutable and retained, so a fabrication is traceable after the fact.
var bySubject = map[string]func(text string) (parser.Extraction, error){
	"Tu transferencia fue exitosa":       parseTransferOut,
	"¡Recibiste una transferencia!":      parseTransferIn,
	"¡Recibimos tu pago!":                parseCardPayment,
	"Tu comprobante de pago de servicio": parseServicePayment,
}

// Parse reads one stored artifact — a whole RFC 822 message — and returns what
// its template says.
//
// It returns [parser.ErrNoTemplate] for the 244 artifacts that carry no
// financial event, which is an ordinary outcome and not a failure.
func Parse(raw []byte) (parser.Extraction, error) {
	subject, err := parser.Header(raw, "Subject")
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %w", err)
	}
	parse, known := bySubject[strings.TrimSpace(subject)]
	if !known {
		return parser.Extraction{}, parser.ErrNoTemplate
	}

	body, err := parser.HTMLBody(raw)
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %w", err)
	}
	text, err := parser.Text(body)
	if err != nil {
		return parser.Extraction{}, fmt.Errorf("nu: %w", err)
	}
	return parse(text)
}

// --- shared reading helpers -------------------------------------------------
//
// Every helper works on labels and phrases, never on a line index. Only 35 of
// 345 inflows share an identical line structure — footers and promotional
// blocks vary — so a parser that counted lines would be right on a tenth of the
// corpus and wrong in a way that still produced numbers.

// labelled returns the value written after a label, and whether it was found.
// The value is taken from the same line where the template puts it there, and
// from the next non-empty line where it does not.
func labelled(text, label string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		at := strings.Index(line, label)
		if at < 0 {
			continue
		}
		if rest := strings.TrimSpace(line[at+len(label):]); rest != "" {
			return rest, true
		}
		if i+1 < len(lines) {
			if next := strings.TrimSpace(lines[i+1]); next != "" {
				return next, true
			}
		}
		return "", false
	}
	return "", false
}

// optional is labelled without the second return, for fields a template may
// legitimately omit.
func optional(text, label string) string {
	v, _ := labelled(text, label)
	return v
}

// between returns the text of the first line containing both markers, sliced to
// what lies between them. It is how a counterparty written into a sentence is
// read: "…a la cuenta de <name> en <bank> fue exitosa."
func between(text, open, close string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		start := strings.Index(line, open)
		if start < 0 {
			continue
		}
		start += len(open)
		end := strings.Index(line[start:], close)
		if end < 0 {
			continue
		}
		return strings.TrimSpace(line[start : start+end]), true
	}
	return "", false
}

// before returns the text preceding a marker on the first line that contains
// it, for a counterparty a sentence opens with: "<name> hizo una transferencia…"
func before(text, marker string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		at := strings.Index(line, marker)
		if at <= 0 {
			continue
		}
		return strings.TrimSpace(line[:at]), true
	}
	return "", false
}

// amount reads a required labelled amount.
func amount(text, label string) (domain.Money, error) {
	raw, found := labelled(text, label)
	if !found {
		return domain.Money{}, fmt.Errorf("no %q line", label)
	}
	money, err := parser.ParseMoney(raw, Currency)
	if err != nil {
		return domain.Money{}, err
	}
	if money.Minor() <= 0 {
		return domain.Money{}, fmt.Errorf("amount is %d minor units; a movement of nothing is not one", money.Minor())
	}
	return money, nil
}

// occurredAt combines a `Fecha:` and an `Hora:` line into an instant, reading
// both as Mexico City wall time (D29) and returning UTC.
func occurredAt(text string) (time.Time, error) {
	date, found := labelled(text, "Fecha:")
	if !found {
		return time.Time{}, fmt.Errorf("no %q line", "Fecha:")
	}
	wall, err := parser.ParseWall(date)
	if err != nil {
		return time.Time{}, err
	}
	clock, found := labelled(text, "Hora:")
	if !found {
		return time.Time{}, fmt.Errorf("no %q line", "Hora:")
	}
	h, m, s, err := parser.ParseClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	return wall.WithClock(h, m, s).In(zone).UTC(), nil
}

// missing names the fields a recognised template failed to produce. A template
// that stops carrying a field it always carried is drift worth surfacing, not
// an empty column to be shrugged at.
func missing(template parser.Template, fields map[string]string) error {
	var absent []string
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			absent = append(absent, name)
		}
	}
	if len(absent) == 0 {
		return nil
	}
	sortStrings(absent)
	return fmt.Errorf("nu: %s: template changed — no %s", template, strings.Join(absent, ", "))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
