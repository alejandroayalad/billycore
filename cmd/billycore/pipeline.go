package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

// retryInterval is how often the worker goes looking for rows whose backoff has
// expired.
//
// Nothing announces a retry falling due. D39's backoff is a timestamp in a
// column, and the moment it passes is not an event anyone can be notified of —
// so the only way a failed row comes back is a pass that looks. A minute is the
// shortest backoff app.RetryBase hands out, which makes the tick the reason a
// retry is on time rather than the reason it is late.
const retryInterval = time.Minute

// extractPass and reconcilePass are the two stages, narrowed to what the worker
// uses: run once, report how many rows were touched.
//
// They are interfaces rather than *app.Extractor and *app.Reconciler so that
// pipeline_test.go can script a stage without a database. That is not a
// concession to testing — the worker genuinely has no use for the rest of those
// types, and scheduling is the only thing in this file worth a test.
type extractPass interface {
	Run(ctx context.Context) (app.ExtractResult, error)
}

type reconcilePass interface {
	Run(ctx context.Context) (app.ReconcileResult, error)
}

// matchPass is cross-Source reconciliation: it merges two Transactions that are
// one event (D70). It is optional — a nil matcher skips the stage — because the
// scheduling test has no use for it and drives only the two stages above.
type matchPass interface {
	Run(ctx context.Context) (app.MatchResult, error)
}

// pipelineWorker advances stored Evidence through extraction and then through
// Transaction construction (D45).
//
// One worker, in the same process as the server. Extraction does not run inside
// the HTTP request that produced the Evidence: sync is synchronous about
// *recording* artifacts (API.md §5) and says nothing about interpreting them,
// and a handler that parsed a thousand emails before answering would make the
// response time of a sync a function of how much work it happened to create.
type pipelineWorker struct {
	extractor  extractPass
	reconciler reconcilePass
	matcher    matchPass

	// wake is the signal that there may be new work. It carries no payload —
	// what changed is not interesting, because a wake always drains everything
	// eligible rather than the rows the sender had in mind.
	wake <-chan struct{}

	retryEvery time.Duration

	// newTicker builds the retry clock. It is a field so a test can supply a
	// channel it ticks by hand: the real interval is a minute, and a test that
	// waits one is a test nobody runs.
	newTicker func(d time.Duration) (<-chan time.Time, func())
}

func newPipelineWorker(extractor extractPass, reconciler reconcilePass, matcher matchPass, wake <-chan struct{}) *pipelineWorker {
	return &pipelineWorker{
		extractor:  extractor,
		reconciler: reconciler,
		matcher:    matcher,
		wake:       wake,
		retryEvery: retryInterval,
		newTicker:  realTicker,
	}
}

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// newWakeSignal returns a channel with room for exactly one pending wake, and
// the notifier that fills it.
//
// The buffer of one is the whole design, and it is why the notifier belongs
// here rather than being written out at each call site:
//
//   - Ten sync notifications do not start ten drains. The tenth finds the slot
//     already full and drops, which is correct — a drain that has not started
//     yet will see everything the ten of them recorded.
//   - A notification that arrives while the worker is busy stays pending, so
//     Evidence recorded mid-drain is not stranded until the next tick.
//   - The notifier never blocks. It is called from an HTTP handler, and a
//     handler that waited on a worker would make a slow drain look like a slow
//     API.
func newWakeSignal() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	return ch, func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Run drains the pipeline at startup and then on every wake, until the context
// ends.
//
// It processes before it waits. A daemon restarting after a crash has rows
// mid-pipeline and nobody to notify it of them, so the first thing a worker
// does is finish whatever the last one did not.
func (w *pipelineWorker) Run(ctx context.Context) {
	ticks, stop := w.newTicker(w.interval())
	defer stop()

	for {
		if ctx.Err() != nil {
			return
		}
		w.process(ctx)

		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticks:
		}
	}
}

// process drains extraction and then Transaction construction.
//
// In that order, because reconciliation reads what extraction wrote: the other
// order would leave everything this wake produced sitting one stage behind
// until the next one.
//
// **A stage that fails does not stop the other, and neither stops the worker.**
// The error a pass returns is the queue being unreachable or the context
// ending, never a bad artifact — those are recorded against their own rows and
// backed off (D39). There is nothing here to escalate that the next wake will
// not retry anyway, and a worker that exited on one would take the pipeline
// down for the life of the process over a locked database.
func (w *pipelineWorker) process(ctx context.Context) {
	report("extraction", w.drainExtraction(ctx))
	if ctx.Err() != nil {
		return
	}
	report("reconciliation", w.drainReconciliation(ctx))
	if ctx.Err() != nil {
		return
	}
	report("matching", w.drainMatching(ctx))
}

// drainExtraction runs extraction passes until one finds nothing.
//
// One pass claims a bounded batch and returns (app.DefaultExtractBatch), so a
// backlog of a thousand artifacts needs ten of them. The loop terminates
// because a row is only claimable once per pass: it either advances a stage or
// takes a backoff, and both remove it from what the next claim can see.
func (w *pipelineWorker) drainExtraction(ctx context.Context) error {
	for {
		result, err := w.extractor.Run(ctx)
		if err != nil {
			return err
		}
		if result.EvidenceProcessed == 0 {
			return nil
		}
		slog.Info("extracted",
			"evidence", result.EvidenceProcessed,
			"interpretations", result.InterpretationsCreated,
			"claims", result.ClaimsCreated,
			"unrecognised", result.Unrecognised,
			"skipped", result.Skipped,
			"failed", result.Failed,
		)
	}
}

// drainReconciliation runs reconciliation passes until one finds nothing, on
// the same terms as drainExtraction.
func (w *pipelineWorker) drainReconciliation(ctx context.Context) error {
	for {
		result, err := w.reconciler.Run(ctx)
		if err != nil {
			return err
		}
		if result.EvidenceProcessed == 0 {
			return nil
		}
		slog.Info("reconciled",
			"evidence", result.EvidenceProcessed,
			"transactions", result.TransactionsCreated,
			"dated_from_evidence", result.DatedFromEvidence,
			"claimless", result.Claimless,
			"skipped", result.Skipped,
			"failed", result.Failed,
		)
	}
}

// drainMatching merges Transactions that describe one event until a pass records
// no new candidate, on the same terms as the drains above. A merge changes state
// (D70), so the next pass finds fewer pairs and the loop ends. A nil matcher —
// the scheduling test — skips the stage.
func (w *pipelineWorker) drainMatching(ctx context.Context) error {
	if w.matcher == nil {
		return nil
	}
	for {
		result, err := w.matcher.Run(ctx)
		if err != nil {
			return err
		}
		if result.CandidatesRecorded == 0 {
			return nil
		}
		slog.Info("matched",
			"candidates", result.CandidatesRecorded,
			"merged", result.Merged,
			"no_match", result.NoMatch,
		)
	}
}

// report logs what ended a drain.
//
// Cancellation is not a failure: it is the shutdown path, and the rows the pass
// had claimed were released on the way out rather than left to sit out their
// lease. Everything else is an error the operator should see — by class, never
// with an artifact in it (SECURITY.md §10), which is a property the pass errors
// already have.
func report(stage string, err error) {
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		slog.Info("pipeline stopped mid-pass", "stage", stage)
	default:
		slog.Error("pipeline pass failed", "stage", stage, "error", err)
	}
}

func (w *pipelineWorker) interval() time.Duration {
	if w.retryEvery <= 0 {
		return retryInterval
	}
	return w.retryEvery
}
