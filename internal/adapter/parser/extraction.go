package parser

import (
	"errors"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// ErrNoTemplate reports an artifact no template parser recognised.
//
// It is an ordinary outcome, not a failure: 244 of the 1,044 stored artifacts
// are contact notifications, card-limit changes, statement notices and
// marketing, and none of them carries a financial event. A parser that does not
// recognise a template returns nothing rather than guessing.
var ErrNoTemplate = errors.New("parser: no template recognises this artifact")

// Template names the specific message layout an Extraction came from.
//
// It is recorded rather than discarded because the layouts drift: Nu changed
// its outflow template on 2026-07-22, and the two variants carry different
// fields from the same subject line. Knowing which one produced a value is what
// makes a later "why is this field empty?" answerable.
type Template string

// Extraction is what one template parser found in one artifact.
//
// It is not a Claim and it is not a Transaction. It is the raw result of
// reading an email — no provenance, no confidence, no identity — and it exists
// so that the step which builds Claims has something already-validated to build
// from (D11).
//
// Every string field is empty when the template does not carry it. Absence is
// never an "UNKNOWN" or a "" standing in for a real value (DATA_MODEL.md §2);
// it means this template did not say.
type Extraction struct {
	// Template is the layout that produced this. Always set.
	Template Template

	// Amount is always set. Measured across the corpus, the amount was present
	// in 800 of 800 transaction-bearing artifacts; it is the one field that is
	// never missing, and a template parser that cannot find it fails rather
	// than returning a partial result.
	Amount domain.Money

	// Direction is empty where the template does not settle it. See the card
	// payment parser, which is the one case in the corpus.
	Direction domain.TransactionDirection

	// OccurredAt is when the artifact says the event happened, in UTC, having
	// been read as America/Mexico_City wall time (D29).
	//
	// It is the zero time when the body carried no timestamp — 90 card payment
	// receipts do not. The fallback to the Source's own delivery timestamp is
	// DATA_MODEL.md §4.5's rule and belongs to the caller: this layer reports
	// what the artifact said, and says nothing when the artifact did not.
	OccurredAt time.Time

	// Counterparty is the other party, as the artifact wrote it: the
	// beneficiary on an outflow, the sender on an inflow, the merchant on a
	// service payment. Never normalised — DOMAIN.md Q2 is open, and this layer
	// does not answer it.
	Counterparty string

	// CounterpartyLabelled reports whether Counterparty was read from a labelled
	// line rather than from a sentence.
	//
	// It exists because the two readings are not equally trustworthy and the
	// value alone cannot tell them apart. `Nombre: <name>` is an anchor Nu would
	// have to change the template to break; "…a la cuenta de <name> en <bank>…"
	// is a sentence Nu can reword in a marketing pass, and a reword yields a
	// wrong name rather than no name. The step that builds Claims turns this
	// into HIGH versus MEDIUM confidence (D34), which it could otherwise only do
	// by guessing the layout from which *other* fields happen to be present.
	CounterpartyLabelled bool

	// CounterpartyInstitution is the bank or entity holding the counterparty's
	// account, such as "HSBC" or "NU MEXICO".
	CounterpartyInstitution string

	// CounterpartyCard is the masked card the money reached, such as
	// "••••7662".
	//
	// It belongs to the *counterparty*, not to the user. It sits in the
	// recipient block beside Nombre and Entidad, and recording it as the user's
	// account would poison the account signal in DOMAIN.md §6, which treats a
	// known-account contradiction as grounds to block reconciliation
	// (CONTEXT.md §3.1).
	CounterpartyCard string

	// ServiceAccount is the user's account number *with the merchant* — a
	// utility contract number, not a bank account. Same warning as above, for
	// the same reason.
	ServiceAccount string

	// Concept is the free-text description the user gave the transfer.
	Concept string

	// Reference, Folio, TrackingKey and OperationCode are the artifact's own
	// identifiers, kept verbatim.
	//
	// TrackingKey is the SPEI `Clave de rastreo`, which is globally unique and
	// therefore the strongest reconciliation signal available — but it appears
	// on only 16 of 1,044 artifacts and never on an inflow, so it is a bonus
	// rather than the backbone CONTEXT.md §3.1 hoped for.
	Reference     string
	Folio         string
	TrackingKey   string
	OperationCode string

	// Status is the artifact's own word for the state of the movement, such as
	// "Completada". It is not a domain FinancialStatus: mapping one to the
	// other is a domain decision, and it is not taken here.
	Status string
}
