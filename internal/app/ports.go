// Package app holds BillyCore's use cases: the thin layer that orders domain
// operations and talks to the outside world through ports.
//
// Ports are declared here, where they are consumed, and implemented in
// internal/adapter (ARCHITECTURE.md §4, D6). Nothing in this package knows that
// storage is SQLite or that a Source is Gmail.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// ErrEvidenceNotFound is returned by EvidenceRepository.GetByID when no
// Evidence has the requested id. It is declared with the port rather than in an
// adapter, because "there is no such Evidence" is part of the contract every
// implementation has to honour — and the HTTP layer turns it into a 404.
var ErrEvidenceNotFound = errors.New("evidence: not found")

// EvidenceRepository stores and reads Evidence.
type EvidenceRepository interface {
	// Insert records Evidence and reports whether a row was created. A false
	// with a nil error means the artifact was already recorded — the normal
	// outcome of re-syncing a mailbox, not a failure (D10).
	Insert(ctx context.Context, e domain.Evidence, now time.Time) (bool, error)

	// GetByID returns the Evidence with the given id.
	GetByID(ctx context.Context, id string) (domain.Evidence, error)

	// ExistsByReference reports whether an artifact has already been recorded,
	// without reading it back. Insert is still the authority on idempotency —
	// this only lets a sync avoid downloading an artifact it already holds.
	ExistsByReference(ctx context.Context, sourceID, sourceReference string) (bool, error)
}

// Artifact is what a Source hands over: bytes, and the facts about them that
// the Source itself can vouch for.
//
// It is not Evidence. Evidence is what BillyCore decides to record, and it is
// built by a domain constructor that enforces invariants an adapter cannot be
// trusted to have applied.
type Artifact struct {
	// Reference identifies the artifact within its Source, stably. For Gmail it
	// is the message id (D10, D23).
	Reference string

	// ContentType describes Content — `message/rfc822` for a whole email (D24).
	ContentType string

	// ObservedAt is when the Source received the artifact, according to the
	// Source itself. Never a timestamp the artifact claims about itself:
	// Evidence is hostile input (SECURITY.md §7, §8).
	ObservedAt time.Time

	// Content is the artifact verbatim. Nothing has parsed it, and nothing in
	// this layer will.
	Content []byte
}

// SourceFetcher reads artifacts from one configured Source.
//
// Implementations carry their own configuration — which mailbox, which query —
// so this interface never grows a parameter describing where to look.
type SourceFetcher interface {
	// ListReferences returns the reference of every artifact the Source
	// currently offers, oldest handling order unspecified.
	ListReferences(ctx context.Context) ([]string, error)

	// Fetch returns one artifact verbatim.
	Fetch(ctx context.Context, reference string) (Artifact, error)
}

// ErrNoInterpretation reports an artifact no template recognised.
//
// It is declared here, with the port, for the same reason ErrEvidenceNotFound
// is: "nothing recognised this" is part of the contract every Interpreter
// honours, and the use case has to tell it apart from a genuine failure without
// knowing that today's implementation reads email templates.
//
// It is an ordinary outcome. 244 of the 1,044 stored artifacts are contact
// notifications, card-limit changes, statement notices and marketing; none
// carries a financial event, and none is a failure to be retried.
var ErrNoInterpretation = errors.New("interpret: nothing recognises this artifact")

// Interpreter reads one stored artifact and reports what Billy believes about
// it: the fields of a Claim, each with its own confidence.
//
// It returns domain values rather than a parse tree, because *which confidence
// a field earns* depends on how the artifact yielded it — a labelled
// `Monto: $1,000.00` is not the same evidence as a currency no artifact states
// (D34) — and only the implementation that did the reading knows that. The use
// case above must not re-derive it by guessing at the layout.
//
// It returns ErrNoInterpretation where nothing recognised the artifact, and an
// error only where something recognised it and then failed.
type Interpreter interface {
	Interpret(raw []byte) (map[domain.FieldName]domain.ClaimField, error)
}

// Redacted is implemented by errors that can describe themselves without the
// input that caused them.
//
// It exists because a parser error is not safe to store. `ParseMoney` renders
// the text it choked on — `parser: not an amount: "Monto: $1,0O0.00"` — and
// that text is a substring of the artifact, which SECURITY.md §10 forbids
// putting in `last_error` or in a log. The layer that knows which errors carry
// input is the layer that knows how to redact them, so the adapter classifies
// its own failures and this interface is how the classification travels.
//
// Anything that does not implement it is stored as its class alone. `last_error`
// is never err.Error().
type Redacted interface {
	// Redacted returns the parser class and the failure reason, and nothing
	// that came out of the artifact.
	Redacted() string
}

// PendingEvidence is one artifact claimed for processing.
//
// It is not domain.Evidence. It carries the bytes and the id, which is what
// interpreting needs, and none of the pipeline columns — DATA_MODEL.md §7 keeps
// those out of the Evidence domain object, and this type is how the queue hands
// work over without smuggling them back in.
type PendingEvidence struct {
	ID         string
	RawContent []byte

	// Attempts is how many times this row has been handed out, including this
	// time. It is here because the retry policy is the use case's to decide and
	// the count is the store's to keep: the pass computes how long to back off
	// from it, and the queue never knows what a backoff is.
	Attempts int
}

// EvidenceQueue is the durable work queue the staged pipeline runs on
// (ARCHITECTURE.md §5, DATA_MODEL.md §7).
//
// It is a separate port from EvidenceRepository on purpose. That one reads and
// writes Evidence, which is immutable; this one moves rows through the
// pipeline, which is infrastructure state that is not part of the Evidence
// domain object at all. Sharing one interface would put "record this artifact
// forever" and "I am working on this row for the next minute" behind the same
// noun.
type EvidenceQueue interface {
	// ClaimForExtraction locks up to limit Evidence rows at stage RECEIVED and
	// returns them, so that two passes cannot claim the same row. The lock
	// lapses at lockedUntil, which is what makes a crashed pass recoverable
	// without an operator releasing anything by hand.
	ClaimForExtraction(ctx context.Context, limit int, now, lockedUntil time.Time) ([]PendingEvidence, error)

	// MarkExtracted advances one row to stage EXTRACTED and releases its lock.
	//
	// It is called for artifacts that produced no Claim as well as for those
	// that did: an artifact nothing recognised has been extracted — the answer
	// was "there is no financial event here" — and leaving it at RECEIVED would
	// re-read all 244 of them on every pass forever.
	MarkExtracted(ctx context.Context, evidenceID string, now time.Time) error

	// RecordFailure stores why an attempt failed and backs the row off until
	// retryAt.
	//
	// The stage does not move. Retry is not a processing stage (DATA_MODEL.md
	// §7): Evidence that failed extraction stays at RECEIVED, and the retry
	// behaviour lives entirely in attempts, last_error and locked_until.
	//
	// `reason` is already redacted — it is a class and a reason, never the
	// artifact (SECURITY.md §10). Implementations store it verbatim and must
	// not enrich it with anything they know about the row.
	RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error

	// Release drops a lock without advancing the stage and without recording a
	// failure, for work abandoned before it was attempted — a pass shutting
	// down mid-batch. The row stays at RECEIVED and is claimable at once,
	// rather than sitting out the rest of its lease for a failure that never
	// happened.
	Release(ctx context.Context, evidenceID string, now time.Time) error
}

// ClaimRepository stores Claims.
type ClaimRepository interface {
	// Save records a Claim — the row, its provenance, its fields — writes the
	// ClaimActivated event of DOMAIN.md §8, and advances the Evidence it was
	// derived from to stage EXTRACTED. All of it in one transaction.
	//
	// The event and the stage are in that transaction for D25's reason: an
	// event describing a change that did not commit is a lie about the domain,
	// and Evidence marked EXTRACTED whose Claim was rolled back is a worse one —
	// it is an artifact Billy will never look at again and has nothing to show
	// for.
	//
	// It reports whether a Claim was created. A false with a nil error means the
	// Evidence already has an active interpretation and this one was not
	// written — the same contract EvidenceRepository.Insert offers for an
	// already-recorded artifact, and for the same reason: re-running a pass over
	// work already done is the normal case, not a failure.
	//
	// Implementations must make that a constraint rather than a check. M1's
	// ingestion is idempotent because `UNIQUE (source_id, source_reference)`
	// says so, not because Insert remembered to look; extraction earns the same
	// property the same way, or it does not have it.
	Save(ctx context.Context, c domain.Claim, now time.Time) (bool, error)
}

// PendingReconciliation is one Evidence row claimed for reconciliation,
// together with the interpretation Billy currently holds of it.
//
// It carries the ACTIVE Claim rather than a claim id, because the pass builds a
// Transaction out of the Claim's own values and a second round trip per row to
// fetch them would be a query per artifact for no benefit — the store already
// had to join to find the Claim at all.
//
// **HasClaim may be false**, and that is an ordinary row rather than an error.
// 244 of the 1,044 stored artifacts produced no Claim: contact notifications,
// card-limit changes, statement notices and marketing. They reached EXTRACTED
// and will never carry a Transaction, and they still have to leave the queue
// (D44).
type PendingReconciliation struct {
	EvidenceID string

	// ObservedAt is when the Source received the artifact, and it is here for
	// one reason: DATA_MODEL.md §4.5's fallback. Where the Claim states no
	// occurred_at — all 90 card payments — the Transaction takes the earliest
	// observed_at of its supporting Evidence, computed before the write and
	// never inside a query.
	ObservedAt time.Time

	Claim    domain.Claim
	HasClaim bool

	// Attempts is how many times this row has been handed out for
	// reconciliation, including this time. Same contract as
	// PendingEvidence.Attempts: the store keeps the count, the use case owns
	// the backoff policy computed from it.
	Attempts int
}

// ReconcileQueue is the third stage's end of the durable work queue
// (ARCHITECTURE.md §5, DATA_MODEL.md §7).
//
// It is a separate port from EvidenceQueue even though one adapter type
// satisfies both, because they are consumed by different use cases and a port
// belongs where it is consumed (D6). Merging them would hand the extraction
// pass a method for advancing rows past a stage it does not own.
type ReconcileQueue interface {
	// ClaimForReconciliation locks up to limit Evidence rows at stage
	// EXTRACTED and returns them with their ACTIVE Claim, where one exists.
	// The lock lapses at lockedUntil, which is what makes a crashed pass
	// recoverable without an operator releasing anything by hand.
	ClaimForReconciliation(ctx context.Context, limit int, now, lockedUntil time.Time) ([]PendingReconciliation, error)

	// MarkReconciled advances one row to stage RECONCILED and releases its
	// lock.
	//
	// It is called for artifacts that carry no Claim as well as for those whose
	// Transaction failed to be written by someone else. Stage is pipeline
	// position, not a claim about the data (ARCHITECTURE.md §5): a RECONCILED
	// row means the pipeline is finished with it, not that a Transaction
	// exists (D44). Leaving the 244 claimless rows at EXTRACTED would have
	// every later pass re-read them forever.
	MarkReconciled(ctx context.Context, evidenceID string, now time.Time) error

	// RecordFailure stores why an attempt failed and backs the row off until
	// retryAt. The stage does not move — retry is not a processing stage
	// (DATA_MODEL.md §7). `reason` is already redacted.
	RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error

	// Release drops a lock without advancing the stage and without recording a
	// failure, for work abandoned before it was attempted.
	Release(ctx context.Context, evidenceID string, now time.Time) error
}

// TransactionRepository stores Transactions.
type TransactionRepository interface {
	// Save records a Transaction — the row and its provenance links — writes
	// the TransactionCreated event of DOMAIN.md §8, claims the slot that makes
	// this idempotent, and advances the Evidence it rests on to stage
	// RECONCILED. All of it in one transaction, for the reason
	// ClaimRepository.Save gives: an event describing a change that did not
	// commit is a lie about the domain, and Evidence marked RECONCILED whose
	// Transaction rolled back is a worse one.
	//
	// `sourceClaimID` is the Claim this Transaction was built from, and it is a
	// parameter rather than a field on the Transaction on purpose. A
	// Transaction's provenance is to Evidence (DOMAIN.md §4) and lives in
	// transaction_evidence; which Claim produced it is an infrastructure fact
	// about which pass won a race, and it belongs in the slot table
	// `claim_transaction` and nowhere in the domain (D41).
	//
	// It reports whether a Transaction was created. A false with a nil error
	// means this Claim already has one and nothing was written — the same
	// contract EvidenceRepository.Insert and ClaimRepository.Save offer, and it
	// comes from the same place: a constraint, not a check the code remembered
	// to perform.
	Save(ctx context.Context, t domain.Transaction, sourceClaimID string, now time.Time) (bool, error)
}
