// Package app holds BillyCore's use cases. It orders domain operations and
// speaks to the outside world through ports. Ports are declared here and
// implemented in internal/adapter (ARCHITECTURE.md §4, D6).
package app

import (
	"context"
	"errors"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// ErrEvidenceNotFound reports that no Evidence has the requested id. Each
// implementation reports the same condition, and the HTTP layer returns 404.
var ErrEvidenceNotFound = errors.New("evidence: not found")

// EvidenceRepository stores and reads Evidence.
type EvidenceRepository interface {
	// Insert records Evidence and reports if it created a row. A false with a
	// nil error means that Billy already holds the artifact (D10).
	//
	// The profile is the reading contract of the Source at this moment. It is a
	// pipeline column and not part of Evidence, so a re-extraction can correct
	// a misconfigured Source without a change to the artifact (D7, D48).
	Insert(ctx context.Context, e domain.Evidence, profile ExtractionProfile, now time.Time) (bool, error)

	// GetByID returns the Evidence with the given id.
	GetByID(ctx context.Context, id string) (domain.Evidence, error)

	// GetByReference returns the immutable artifact already recorded under a
	// Source identity. Direct ingestion uses it to answer an idempotent retry
	// with the original Evidence rather than the discarded candidate (D53).
	GetByReference(ctx context.Context, sourceID, sourceReference string) (domain.Evidence, error)

	// ExistsByReference reports if Billy holds an artifact. Insert stays the
	// authority on idempotency; this only lets a sync skip a download.
	ExistsByReference(ctx context.Context, sourceID, sourceReference string) (bool, error)
}

// Artifact is what a Source gives to BillyCore. It is not Evidence: Evidence is
// what BillyCore records, and a domain constructor builds it.
type Artifact struct {
	// Reference identifies the artifact in its Source. For Gmail it is the
	// message id (D10, D23).
	Reference string

	// ContentType describes Content. It is `message/rfc822` for an email (D24).
	ContentType string

	// ObservedAt is when the Source received the artifact, as the Source
	// reports it. Never a time from the artifact (SECURITY.md §7, §8).
	ObservedAt time.Time

	// Content is the artifact, byte for byte. This layer does not parse it.
	Content []byte
}

// SourceFetcher reads artifacts from one configured Source. Each implementation
// holds its own configuration, so this interface needs no location parameter.
type SourceFetcher interface {
	// ListReferences returns the reference of each artifact that the Source
	// offers. The order is not specified.
	ListReferences(ctx context.Context) ([]string, error)

	// Fetch returns one artifact, byte for byte.
	Fetch(ctx context.Context, reference string) (Artifact, error)
}

// ErrNoInterpretation reports an artifact that no template recognises. It is an
// ordinary result: 244 of the 1,044 stored artifacts carry no financial event.
var ErrNoInterpretation = errors.New("interpret: nothing recognises this artifact")

// Reading is the complete result of one Interpreter call: what the parser read,
// and how much of the artifact it could not read (D59).
type Reading struct {
	// Fields holds the fields of one Claim for each movement the parser read,
	// with the confidence of each field (D34). One artifact can describe many
	// movements, so this is a slice (D46). An empty slice is not valid: report
	// ErrNoInterpretation instead.
	Fields []map[domain.FieldName]domain.ClaimField

	// SkippedRows counts the movements that no shape of the parser reads. A
	// statement of about 180 rows is read as far as it can be, and it says how
	// far (D59). It is always zero for an email, which is one movement.
	SkippedRows int
}

// Interpreter reads one artifact and reports what Billy believes about it.
//
// The context is for an Interpreter that leaves the process. A statement parser
// runs pdftotext, and the caller must be able to stop it (D54, D57). A parser
// that works in memory accepts the context and ignores it.
type Interpreter interface {
	Interpret(ctx context.Context, raw []byte) (Reading, error)
}

// PDFCoordinateExtractor converts PDF bytes to bounded coordinate XHTML/XML.
// It does not interpret financial data (D54).
type PDFCoordinateExtractor interface {
	Extract(ctx context.Context, pdf []byte) ([]byte, error)
}

// Redacted is implemented by an error that can describe itself without the
// input. A parser error can hold part of an artifact, and SECURITY.md §10
// forbids that text in `last_error` and in a log.
type Redacted interface {
	// Redacted returns the parser class and the reason, and no artifact text.
	Redacted() string
}

// PendingEvidence is one artifact that a pass claimed. It is not domain
// .Evidence: it carries the bytes and the id, and no pipeline column
// (DATA_MODEL.md §7).
type PendingEvidence struct {
	ID         string
	RawContent []byte

	// Profile is the reading contract that the Source named when Billy recorded
	// the artifact. It selects the parser. It is empty for a row that no Source
	// configuration reached, and that row fails with a named reason.
	Profile ExtractionProfile

	// ContentType is what the artifact is, as the Source reported it (D24). The
	// registry validates it against the profile before any parser reads a byte.
	ContentType string

	// Attempts is how many times the queue gave out this row, including this
	// time. The store keeps the count, and the use case decides the backoff.
	Attempts int

	// ActiveInterpretationID is what Billy believes about this artifact now, or
	// empty if no parser has read it. A replacement must name what it replaces,
	// so the store can swap one interpretation for the other (D47, D48).
	ActiveInterpretationID string
}

// EvidenceQueue is the durable work queue of the pipeline (ARCHITECTURE.md §5,
// DATA_MODEL.md §7). It is separate from EvidenceRepository, because Evidence is
// immutable and a pipeline row is infrastructure state.
type EvidenceQueue interface {
	// ClaimForExtraction locks up to limit rows at stage RECEIVED and returns
	// them, so two passes cannot take one row. The lock ends at lockedUntil,
	// which makes a stopped pass recoverable without an operator.
	ClaimForExtraction(ctx context.Context, limit int, now, lockedUntil time.Time) ([]PendingEvidence, error)

	// MarkExtracted advances one row to stage EXTRACTED and releases its lock.
	// It is for an artifact that gave no Claim: extraction looked and found no
	// financial event, and a row at RECEIVED returns to each later pass.
	MarkExtracted(ctx context.Context, evidenceID string, now time.Time) error

	// RecordFailure stores why an attempt failed and delays the row until
	// retryAt. The stage does not move (DATA_MODEL.md §7). `reason` is already
	// redacted, and an implementation stores it without change.
	RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error

	// Release drops a lock and does not move the stage or record a failure. It
	// is for work that a pass claimed and did not attempt, such as during a
	// shutdown. The row is available immediately.
	Release(ctx context.Context, evidenceID string, now time.Time) error
}

// ClaimRepository stores interpretations.
type ClaimRepository interface {
	// Save records one complete interpretation in one database transaction:
	// each Claim with its provenance and fields, one ClaimActivated event for
	// each Claim, the active-interpretation pointer, and the stage advance.
	//
	// The set is the unit (D46). If one Claim fails, Save writes none of them.
	//
	// The profile is the parser that made this reading. The row keeps it, and
	// the row never changes it: it is the record of how Billy read the
	// artifact, and D47's lineage keeps the readings that came before.
	Save(ctx context.Context, in domain.Interpretation, profile ExtractionProfile, now time.Time) (bool, error)
}

// PendingReconciliation is one Evidence row that a pass claimed, with the
// interpretation that Billy holds of it. It carries the Claims and not their
// ids, because the pass builds Transactions from their values.
type PendingReconciliation struct {
	EvidenceID string

	// ObservedAt is when the Source received the artifact. The pass needs it
	// for the fallback in DATA_MODEL.md §4.5, which it computes before the
	// write and never in a query.
	ObservedAt time.Time

	// Claims is each Claim in the active interpretation, and the pass builds one
	// Transaction from each (D42). It can be empty, and that is an ordinary row:
	// 244 of the 1,044 stored artifacts gave no Claim (D44).
	Claims []domain.Claim

	// Attempts is how many times the queue gave out this row for
	// reconciliation. Same contract as PendingEvidence.Attempts.
	Attempts int
}

// ReconcileQueue is the third stage of the durable work queue. It is separate
// from EvidenceQueue although one adapter implements both, because a port
// belongs where it is consumed (D6).
type ReconcileQueue interface {
	// ClaimForReconciliation locks up to limit rows at stage EXTRACTED and
	// returns each with the Claims of its active interpretation. The lock ends
	// at lockedUntil.
	ClaimForReconciliation(ctx context.Context, limit int, now, lockedUntil time.Time) ([]PendingReconciliation, error)

	// MarkReconciled advances one row to stage RECONCILED and releases its
	// lock. Stage is pipeline position, not a statement about the data: a
	// RECONCILED row does not prove that a Transaction exists (D44).
	MarkReconciled(ctx context.Context, evidenceID string, now time.Time) error

	// RecordFailure stores why an attempt failed and delays the row until
	// retryAt. The stage does not move (DATA_MODEL.md §7).
	RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error

	// Release drops a lock and does not move the stage or record a failure.
	Release(ctx context.Context, evidenceID string, now time.Time) error
}

// BuiltTransaction holds a Transaction and the id of its Claim. The id stays
// outside the Transaction: provenance points to Evidence (DOMAIN.md §4), and the
// Claim belongs in the table `claim_transaction` (D41).
type BuiltTransaction struct {
	Transaction   domain.Transaction
	SourceClaimID string
}

// TransactionRepository stores Transactions.
type TransactionRepository interface {
	// Save records each Transaction of one artifact in one database
	// transaction: the rows, their provenance, one TransactionCreated event
	// each, the slot for each Claim, and the stage advance.
	//
	// The set is the unit, because the stage advance is in this transaction.
	Save(ctx context.Context, built []BuiltTransaction, now time.Time) (bool, error)
}

// TransactionCursor is the stable ordering key for a Transaction page.
type TransactionCursor struct {
	OccurredAt time.Time
	ID         string
}

// TransactionQuery contains the filters from GET /v1/transactions.
type TransactionQuery struct {
	From                 *time.Time
	To                   *time.Time
	Directions           []domain.TransactionDirection
	FinancialStatuses    []domain.FinancialStatus
	ReconciliationStates []domain.ReconciliationState
	Currency             domain.Currency
	After                *TransactionCursor
	Limit                int
}

// ListedTransaction adds Claim support to a Transaction list row.
type ListedTransaction struct {
	Transaction            domain.Transaction
	MerchantConfidence     domain.Confidence
	CounterpartyConfidence domain.Confidence
	AccountConfidence      domain.Confidence

	// OwnAccountTransfer is true when this row is the user's own money moving
	// between accounts they hold: a keyed SPEI across two Sources (D75), or an
	// inflow whose counterparty those SPEI already proved is the user (D76).
	// It is neither income nor spending, like a Cajita move.
	OwnAccountTransfer bool
}

// TransactionPage is one deterministic page and whether another page exists.
type TransactionPage struct {
	Transactions []ListedTransaction
	HasMore      bool
}

// TransactionTotals is income and spending over a filtered set of Transactions.
//
// An internal movement is money between the user's own accounts, so it is not
// income and not spending, and it is excluded from both totals (D55, D62, D75, D76).
// It is still counted here, so a reader can say how many were left out.
//
// The amounts are minor units of one currency. BillyCore supports one currency
// today (D50), so a total mixes none.
type TransactionTotals struct {
	Currency         domain.Currency
	IncomeMinor      int64
	ExpenseMinor     int64
	IncomeCount      int
	ExpenseCount     int
	ExcludedInternal int
}

// TransactionReader reads the ACTIVE Transactions Billy currently uses.
type TransactionReader interface {
	List(ctx context.Context, query TransactionQuery) (TransactionPage, error)

	// Totals sums income and spending over the Transactions the query selects,
	// excluding internal movements (D55). The from, to and currency filters
	// apply; the cursor and the limit do not.
	Totals(ctx context.Context, query TransactionQuery) (TransactionTotals, error)
}

// TrackingKeyed pairs an ACTIVE UNRECONCILED Transaction with the SPEI Clave de
// rastreo one of its Claims carries (D36). Two Transactions that share one key
// are the same movement, which is the exact signal the pass matches on first.
type TrackingKeyed struct {
	Transaction domain.Transaction
	TrackingKey string
}

// SourcedTransaction pairs an ACTIVE UNRECONCILED Transaction with the id of the
// one Source its Evidence came from (D72). The weak sweep needs the Source to
// keep two look-alike movements from one Source apart from one seen in two.
type SourcedTransaction struct {
	Transaction domain.Transaction
	SourceID    string
}

// ReconcileDecision is one candidate the pass reached, ready to persist. The
// pair is canonical, LeftID < RightID (D69). Survivor and Superseded are set
// only on a MATCH: they are the merged pair the domain already validated (D70).
type ReconcileDecision struct {
	CandidateID string
	LeftID      string
	RightID     string
	Outcome     domain.ReconciliationOutcome
	Survivor    domain.Transaction
	Superseded  domain.Transaction

	// Basis names the signal that decided a MATCH, for the audit event (D70,
	// D72): the tracking key, or the exact composite key. It is empty for a
	// candidate that did not merge.
	Basis string
}

// ReconciliationRepository reads the Transactions a pass compares and records
// what it decided (DATA_MODEL.md §9, D69).
type ReconciliationRepository interface {
	// UnreconciledWithTrackingKey returns each ACTIVE UNRECONCILED Transaction
	// that carries a tracking key, so the pass groups them into pairs.
	UnreconciledWithTrackingKey(ctx context.Context) ([]TrackingKeyed, error)

	// UnreconciledTransactions returns every ACTIVE UNRECONCILED Transaction with
	// its Source, so the weak sweep blocks them by amount and applies the
	// composite key across Sources (D71, D72). A merged one is not here.
	UnreconciledTransactions(ctx context.Context) ([]SourcedTransaction, error)

	// Reconcile records one candidate and, on a MATCH, merges the pair in one
	// database transaction (D70). The UNIQUE pair makes a re-run a no-op; it
	// reports whether it wrote a new candidate.
	Reconcile(ctx context.Context, d ReconcileDecision, now time.Time) (bool, error)
}
