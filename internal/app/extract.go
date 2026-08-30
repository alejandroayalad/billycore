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
// The batch is a lock bound, not a memory bound. If a pass claims all 1,044
// rows and then stops, it holds every row until the lease ends. A batch of one
// hundred limits the cost of a stopped pass to one hundred rows and one minute.
const DefaultExtractBatch = 100

// DefaultExtractLease is how long a claimed row stays locked.
//
// It is long enough that a slow pass keeps its own rows, and short enough that
// a stopped process does not hold work for an hour. The lease is sized for a
// stopped process, because one artifact takes microseconds to parse.
const DefaultExtractLease = time.Minute

// ExtractResult is the summary of one extraction pass.
type ExtractResult struct {
	EvidenceProcessed int

	// ClaimsCreated counts Claims, not artifacts. An interpretation of an email
	// holds one Claim, and one of a statement holds one Claim for each movement
	// (D46). Therefore this count and EvidenceProcessed can differ.
	ClaimsCreated int

	// InterpretationsCreated counts the artifacts that gave a reading.
	InterpretationsCreated int

	Unrecognised int

	// Failed counts the artifacts that gave no Claim because of an error.
	// Unrecognised is different: it is the ordinary result of an artifact with
	// no financial event. A pass with failures still succeeds (SECURITY.md §7).
	Failed int

	// Skipped counts the artifacts that already had an active interpretation,
	// so this pass wrote none. It means that the work was already done.
	Skipped int

	CompletedAt time.Time
}

// Extractor turns stored Evidence into Claims. It is the second stage of
// ARCHITECTURE.md §5: it reads stored artifacts, interprets them, and advances
// them. It never changes an artifact, because Evidence is immutable (D7). The
// caller injects the clock and the id generator.
type Extractor struct {
	Queue        EvidenceQueue
	Claims       ClaimRepository
	Interpreters InterpreterRegistry

	NewID func() (string, error)
	Now   func() time.Time

	Batch int
	Lease time.Duration

	// OnPanic receives the stack of a recovered parser panic. It receives the
	// stack and never the artifact (SECURITY.md §10). It is a field so that a
	// test can show that the pass contained a panic.
	OnPanic func(evidenceID string, stack []byte)
}

func NewExtractor(queue EvidenceQueue, claims ClaimRepository, interpreters InterpreterRegistry) *Extractor {
	return &Extractor{
		Queue:        queue,
		Claims:       claims,
		Interpreters: interpreters,
		NewID:        id.New,
		Now:          func() time.Time { return time.Now().UTC() },
		Batch:        DefaultExtractBatch,
		Lease:        DefaultExtractLease,
		OnPanic:      logPanic,
	}
}

// logPanic is the default. It writes a stack trace at error level and names the
// artifact by id only.
func logPanic(evidenceID string, stack []byte) {
	slog.Error("recovered a panic while extracting",
		"evidence", evidenceID, "stack", string(stack))
}

// Run claims one batch of Evidence at stage RECEIVED and interprets it. It
// returns at the end of the batch, and the caller decides if it runs again.
//
// One failed artifact does not stop the pass (SECURITY.md §7). Run returns an
// error only if the queue is unavailable or the context is cancelled.
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
		outcome, claims := x.extractOne(ctx, evidence)
		switch outcome {
		case outcomeClaimed:
			result.InterpretationsCreated++
			result.ClaimsCreated += claims
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

// outcome is what happened to one artifact. Each value is ordinary. Only an
// unavailable queue ends a pass.
type outcome int

const (
	outcomeClaimed outcome = iota
	outcomeUnrecognised
	outcomeSkipped
	outcomeFailed
)

// extractOne interprets one artifact, records the result, and reports how many
// Claims the interpretation held. It returns no error: each failure belongs on
// the row. If the queue is unavailable, the lease ends and a later pass claims
// the row again.
func (x *Extractor) extractOne(ctx context.Context, evidence PendingEvidence) (outcome, int) {
	interpreter, err := x.selectInterpreter(evidence)
	if err != nil {
		// The artifact reached no parser. The row keeps its stage and takes a
		// backoff, so a corrected profile or a new parser reads it later.
		return x.fail(ctx, evidence, classDispatch, err), 0
	}

	reading, err := x.interpret(ctx, interpreter, evidence)
	if errors.Is(err, ErrNoInterpretation) {
		// No template recognises the artifact. This is an answer, not a
		// failure, so the row advances.
		if err := x.Queue.MarkExtracted(ctx, evidence.ID, x.Now()); err != nil {
			return x.fail(ctx, evidence, classStore, err), 0
		}
		return outcomeUnrecognised, 0
	}
	if err != nil {
		class := classInterpret
		if errors.As(err, new(panicked)) {
			class = classPanic
		}
		return x.fail(ctx, evidence, class, err), 0
	}

	interpretation, err := x.buildInterpretation(evidence, reading)
	if err != nil {
		// The domain rejected Billy's own interpretation. This is a defect in
		// the mapping, not a bad artifact. The row keeps its Evidence, so a
		// corrected mapping can read it again. One bad movement fails the
		// complete set, which is the rule in D46.
		return x.fail(ctx, evidence, classClaim, err), 0
	}
	created, err := x.Claims.Save(ctx, interpretation, evidence.Profile, x.Now())
	if err != nil {
		return x.fail(ctx, evidence, classStore, err), 0
	}
	if !created {
		// The active interpretation was not the one that this pass expected to
		// replace. Another pass wrote it first, or the work was already done.
		// The store wrote nothing, and the row still advances.
		if err := x.Queue.MarkExtracted(ctx, evidence.ID, x.Now()); err != nil {
			return x.fail(ctx, evidence, classStore, err), 0
		}
		return outcomeSkipped, 0
	}
	return outcomeClaimed, len(interpretation.Claims())
}

// selectInterpreter finds the parser of the artifact and validates the format
// signature. A row with no profile fails here: BillyCore has no default, and a
// guess reads a statement with an email parser.
func (x *Extractor) selectInterpreter(evidence PendingEvidence) (Interpreter, error) {
	if evidence.Profile == "" {
		return nil, dispatchFailure{err: ErrNoProfile}
	}
	interpreter, err := x.Interpreters.Select(evidence.Profile, evidence.ContentType, evidence.RawContent)
	if err != nil {
		return nil, dispatchFailure{profile: evidence.Profile, err: err}
	}
	return interpreter, nil
}

// interpret runs the Interpreter and recovers a panic for each artifact, as
// SECURITY.md §7 requires. The recovery is here, and not around the batch, so
// only the artifact that caused the panic fails.
func (x *Extractor) interpret(ctx context.Context, interpreter Interpreter, evidence PendingEvidence) (reading Reading, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// Report the stack and never the artifact. A panic value is often a
		// string, and a string in a parser is often a part of the email.
		if x.OnPanic != nil {
			x.OnPanic(evidence.ID, debug.Stack())
		}
		reading, err = Reading{}, newPanicked(r)
	}()
	return interpreter.Interpret(ctx, evidence.RawContent)
}

// fail records one failed attempt on its row. The stage does not move, because
// retry is not a processing stage (DATA_MODEL.md §7). The columns attempts,
// last_error and locked_until hold the retry state.
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

// releaseUnattempted returns the rows that a pass claimed during a shutdown. It
// uses a context that is detached from the cancelled one, because a cancelled
// context would fail each statement and hold the rows until the lease ends.
func (x *Extractor) releaseUnattempted(ctx context.Context, pending []PendingEvidence) {
	detached := context.WithoutCancel(ctx)
	for _, evidence := range pending {
		if err := x.Queue.Release(detached, evidence.ID, x.Now()); err != nil {
			slog.Warn("could not release evidence on shutdown",
				"evidence", evidence.ID, "error", err)
		}
	}
}

// buildInterpretation records the reading of one artifact: one Claim for each
// movement, in one set that Billy activates or discards (D46). Each Claim is
// PROPOSED and then ACTIVE (D37). ActiveInterpretationID arrives with the row
// and names the interpretation that this reading replaces (D47, D48). The count
// of rows that no shape read travels with the set (D59).
func (x *Extractor) buildInterpretation(evidence PendingEvidence, reading Reading) (domain.Interpretation, error) {
	sets := reading.Fields
	if len(sets) == 0 {
		// An Interpreter that recognises an artifact must give a reading of it.
		// A result with no error and no Claim is a defect in the parser.
		return domain.Interpretation{}, fmt.Errorf("extract: evidence %s was recognised and yielded no claims", evidence.ID)
	}

	now := x.Now().UTC()
	claims := make([]domain.Claim, 0, len(sets))
	for _, fields := range sets {
		claimID, err := x.NewID()
		if err != nil {
			return domain.Interpretation{}, fmt.Errorf("extract: generate claim id: %w", err)
		}
		proposed, err := domain.NewClaim(claimID, domain.ClaimProposed, []string{evidence.ID}, fields, now)
		if err != nil {
			return domain.Interpretation{}, fmt.Errorf("extract: evidence %s did not yield a valid claim: %w", evidence.ID, err)
		}
		active, err := proposed.Activate(now)
		if err != nil {
			return domain.Interpretation{}, fmt.Errorf("extract: activate claim for evidence %s: %w", evidence.ID, err)
		}
		claims = append(claims, active)
	}

	interpretationID, err := x.NewID()
	if err != nil {
		return domain.Interpretation{}, fmt.Errorf("extract: generate interpretation id: %w", err)
	}
	interpretation, err := domain.NewInterpretation(interpretationID, evidence.ID, claims, reading.SkippedRows, evidence.ActiveInterpretationID, now)
	if err != nil {
		return domain.Interpretation{}, fmt.Errorf("extract: evidence %s did not yield a valid interpretation: %w", evidence.ID, err)
	}
	return interpretation, nil
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
