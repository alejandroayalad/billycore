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
	Insert(ctx context.Context, e domain.Evidence, now time.Time) (bool, error)

	// GetByID returns the Evidence with the given id.
	GetByID(ctx context.Context, id string) (domain.Evidence, error)

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
	Save(ctx context.Context, in domain.Interpretation, now time.Time) (bool, error)
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
	Transaction        domain.Transaction
	MerchantConfidence domain.Confidence
	AccountConfidence  domain.Confidence
}

// TransactionPage is one deterministic page and whether another page exists.
type TransactionPage struct {
	Transactions []ListedTransaction
	HasMore      bool
}

// TransactionReader reads the ACTIVE Transactions Billy currently uses.
type TransactionReader interface {
	List(ctx context.Context, query TransactionQuery) (TransactionPage, error)
}
