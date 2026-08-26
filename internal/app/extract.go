package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
	"github.com/alejandroayalad/billycore/internal/id"
)

// DefaultExtractBatch is how many artifacts one pass claims at a time.
//
// The corpus is 1,044 rows and the whole database is 51 MB, so the batch is not
// a memory bound — it is a lock bound. A pass that claimed all 1,044 and then
// crashed would hold every row locked until the lease lapsed; a hundred at a
// time keeps the blast radius of a crash to a hundred rows and a minute.
const DefaultExtractBatch = 100

// DefaultExtractLease is how long a claimed row stays locked.
//
// Long enough that a slow pass does not have its own rows stolen mid-flight,
// short enough that a killed process does not strand work for an hour. Parsing
// one artifact is microseconds; the lease is sized for the crash, not the work.
const DefaultExtractLease = time.Minute

// ExtractResult is the summary of one extraction pass.
type ExtractResult struct {
	EvidenceProcessed int
	ClaimsCreated     int
	Unrecognised      int

	// Failed counts artifacts that were attempted and did not yield a Claim
	// because something went wrong — as distinct from Unrecognised, which is
	// the ordinary outcome of an artifact carrying no financial event.
	//
	// A pass with failures still succeeds. SECURITY.md §7 is explicit that one
	// bad email must not stop the pipeline, so this is a number to look at, not
	// an error to return.
	Failed int

	// Skipped counts artifacts that already had an active interpretation, so
	// this pass wrote none. It is the extraction half of the number M1's second
	// sync printed — 1,044 discovered, 0 created, 1,044 skipped — and it means
	// the same thing: the work was already done, and doing it again changed
	// nothing.
	Skipped int

	CompletedAt time.Time
}

// Extractor turns stored Evidence into Claims.
//
// It is the second stage of ARCHITECTURE.md §5: it reads artifacts the ingestor
// already made durable, interprets them, and advances them. It fetches nothing
// and it never rewrites an artifact — Evidence is immutable, and extraction
// being a separate pass over stored bytes is what makes a parser crash
// survivable rather than a lost email (D7, SECURITY.md §7).
//
// Like Ingestor, it owns no clock and no randomness. Both are injected, so the
// whole use case runs in a test with neither.
type Extractor struct {
	Queue       EvidenceQueue
	Claims      ClaimRepository
	Interpreter Interpreter

	NewID func() (string, error)
	Now   func() time.Time

	Batch int
	Lease time.Duration

	// OnPanic is called with the stack of a recovered parser panic.
	//
	// The stack, and never the artifact being processed (SECURITY.md §10). It
	// is a field so a test can assert a panic was contained without reading the
	// process log, and so the pipeline owns no opinion about where logs go.
	OnPanic func(evidenceID string, stack []byte)
}

func NewExtractor(queue EvidenceQueue, claims ClaimRepository, interpreter Interpreter) *Extractor {
	return &Extractor{
		Queue:       queue,
		Claims:      claims,
		Interpreter: interpreter,
		NewID:       id.New,
		Now:         func() time.Time { return time.Now().UTC() },
		Batch:       DefaultExtractBatch,
		Lease:       DefaultExtractLease,
		OnPanic:     logPanic,
	}
}

// logPanic is the default: a stack trace at error level, naming the artifact by
// id only.
func logPanic(evidenceID string, stack []byte) {
	slog.Error("recovered a panic while extracting",
		"evidence", evidenceID, "stack", string(stack))
}

// Run claims one batch of Evidence at RECEIVED and interprets it.
//
// It returns when the batch is exhausted rather than looping until the queue is
// empty. The caller decides whether to run again, which keeps "how much work
// happens" a scheduling question rather than something buried in here.
//
// **A failing artifact does not stop the pass.** SECURITY.md §7 requires it:
// Evidence is hostile input, anyone who knows the address can put bytes into
// the parser, and a pass that aborted on the first malformed one would let a
// single email hold up every artifact behind it. A failure is recorded against
// its own row and the loop moves on, so the error Run returns is reserved for
// what genuinely ends a pass — the queue being unreachable, or the context
// being cancelled.
func (x *Extractor) Run(ctx context.Context) (ExtractResult, error) {
	now := x.Now().UTC()
	pending, err := x.Queue.ClaimForExtraction(ctx, x.batch(), now, now.Add(x.lease()))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("extract: claim evidence: %w", err)
	}

	result := ExtractResult{}
	for i, evidence := range pending {
		if err := ctx.Err(); err != nil {
			// Shutting down. The rows already claimed and not yet attempted are
			// released rather than left to sit out their lease — they were
			// never tried, so there is no failure to record and nothing to back
			// off from.
			x.releaseUnattempted(ctx, pending[i:])
			return result, err
		}
		switch x.extractOne(ctx, evidence) {
		case outcomeClaimed:
			result.ClaimsCreated++
		case outcomeUnrecognised:
			result.Unrecognised++
		case outcomeSkipped:
			result.Skipped++
		case outcomeFailed:
			result.Failed++
		}
		result.EvidenceProcessed++
	}

	result.CompletedAt = x.Now().UTC()
	return result, nil
}

// outcome is what happened to one artifact. All three are ordinary; only the
// queue being unreachable ends a pass.
type outcome int

const (
	outcomeClaimed outcome = iota
	outcomeUnrecognised
	outcomeSkipped
	outcomeFailed
)

// extractOne interprets a single artifact and records what happened to it.
//
// It returns no error. Every failure it can encounter belongs on the row rather
// than to the caller, including a failure to record the failure — at that point
// the queue is unreachable, the lease will lapse on its own, and the row will
// be picked up again by a later pass. Evidence is immutable and still there,
// which is the property that makes losing this attempt survivable.
func (x *Extractor) extractOne(ctx context.Context, evidence PendingEvidence) outcome {
	fields, err := x.interpret(evidence)
	if errors.Is(err, ErrNoInterpretation) {
		// Nothing recognised it, which is an answer and not a failure. The row
		// advances: it has been extracted, and what extraction found was that
		// there is no financial event here.
		if err := x.Queue.MarkExtracted(ctx, evidence.ID, x.Now()); err != nil {
			return x.fail(ctx, evidence, classStore, err)
		}
		return outcomeUnrecognised
	}
	if err != nil {
		class := classInterpret
		if errors.As(err, new(panicked)) {
			class = classPanic
		}
		return x.fail(ctx, evidence, class, err)
	}

	claim, err := x.buildClaim(evidence.ID, fields)
	if err != nil {
		// The domain rejected Billy's own interpretation. That is a bug in the
		// mapping rather than a bad artifact, and retrying will not fix it —
		// but the backoff makes the cost of being wrong about that one parse a
		// day, and the row keeps its Evidence so a fixed mapping picks it up.
		return x.fail(ctx, evidence, classClaim, err)
	}
	created, err := x.Claims.Save(ctx, claim, x.Now())
	if err != nil {
		return x.fail(ctx, evidence, classStore, err)
	}
	if !created {
		// This artifact already had an active interpretation — another pass
		// reached it first, or this one is a re-run over work already done.
		// Nothing was written, so nothing rolled back, and the row still needs
		// advancing: it *has* been extracted, just not by this pass.
		if err := x.Queue.MarkExtracted(ctx, evidence.ID, x.Now()); err != nil {
			return x.fail(ctx, evidence, classStore, err)
		}
		return outcomeSkipped
	}
	return outcomeClaimed
}

// interpret runs the Interpreter with a panic recovered per artifact.
//
// SECURITY.md §7: "A parser panic is contained. Recover per-artifact, mark the
// row failed, keep serving." The recovery is here rather than around the whole
// batch precisely so that the artifact that panicked is the only one that
// suffers — a recover one level up would abandon every row still queued behind
// it, still holding their locks.
func (x *Extractor) interpret(evidence PendingEvidence) (fields map[domain.FieldName]domain.ClaimField, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// The stack, never the artifact. A panic value is often a string, and a
		// string inside a parser is often a piece of the email.
		if x.OnPanic != nil {
			x.OnPanic(evidence.ID, debug.Stack())
		}
		fields, err = nil, newPanicked(r)
	}()
	return x.Interpreter.Interpret(evidence.RawContent)
}

// fail records one failed attempt against its row.
//
// The stage does not move. Retry is not a processing stage (DATA_MODEL.md §7):
// the artifact stays at RECEIVED exactly as it was received, and attempts,
// last_error and locked_until carry everything about the retry.
func (x *Extractor) fail(ctx context.Context, evidence PendingEvidence, class string, cause error) outcome {
	retryAt := x.Now().UTC().Add(retryAfter(evidence.Attempts))
	if err := x.Queue.RecordFailure(ctx, evidence.ID, reason(class, cause), retryAt); err != nil {
		// Nothing further to do: the lease lapses on its own and the row
		// returns. Reported by id, never by content.
		slog.Error("could not record an extraction failure",
			"evidence", evidence.ID, "class", class, "error", err)
	}
	return outcomeFailed
}

// releaseUnattempted hands back rows claimed by a pass that is shutting down.
//
// It uses a context detached from the cancelled one on purpose: the whole point
// is to run three statements after the caller has given up, and inheriting the
// cancellation would make every one of them fail and strand the rows for the
// rest of their lease.
func (x *Extractor) releaseUnattempted(ctx context.Context, pending []PendingEvidence) {
	detached := context.WithoutCancel(ctx)
	for _, evidence := range pending {
		if err := x.Queue.Release(detached, evidence.ID, x.Now()); err != nil {
			slog.Warn("could not release evidence on shutdown",
				"evidence", evidence.ID, "error", err)
		}
	}
}

// buildClaim records the interpretation as a Claim, and activates it.
//
// PROPOSED and then ACTIVE rather than born ACTIVE (D37). Activate is the only
// operation that performs the transition ClaimActivated describes, so
// constructing at ACTIVE would put an event in the log for a transition no code
// ever made. The two states occupy the same instant, which is honest: the Claim
// really was proposed and really was accepted, and nothing happened in between.
//
// It is also the path POST /v1/claims will take when an outside proposer offers
// an interpretation (D11, D12) — with the difference that theirs may stop at
// PROPOSED.
func (x *Extractor) buildClaim(evidenceID string, fields map[domain.FieldName]domain.ClaimField) (domain.Claim, error) {
	claimID, err := x.NewID()
	if err != nil {
		return domain.Claim{}, fmt.Errorf("extract: generate claim id: %w", err)
	}
	now := x.Now().UTC()

	proposed, err := domain.NewClaim(claimID, domain.ClaimProposed, []string{evidenceID}, fields, now)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("extract: evidence %s did not yield a valid claim: %w", evidenceID, err)
	}
	active, err := proposed.Activate(now)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("extract: activate claim for evidence %s: %w", evidenceID, err)
	}
	return active, nil
}

func (x *Extractor) batch() int {
	if x.Batch <= 0 {
		return DefaultExtractBatch
	}
	return x.Batch
}

func (x *Extractor) lease() time.Duration {
	if x.Lease <= 0 {
		return DefaultExtractLease
	}
	return x.Lease
}
