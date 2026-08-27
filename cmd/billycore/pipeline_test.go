package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

// The worker is scheduling and nothing else, so every test here runs it against
// scripted stages: no SQLite, no parser, and no wall clock. What is under test
// is when a pass runs and how often — never what a pass does, which is
// extract_test.go's and reconcile_test.go's subject.

// settleFor is how long a negative assertion waits before believing nothing is
// going to happen.
//
// Only ever used to prove a *pass did not run*. Correct code never produces the
// event these waits are watching for, and broken code produces it immediately,
// so the wait is a bound on the failure rather than a race for the success.
const settleFor = 100 * time.Millisecond

// stage is one scripted pipeline stage.
//
// Each Run consumes the next entry of the script and announces itself on
// events. **The announcement is a blocking handshake**, which is what makes
// these tests deterministic rather than timed: the worker cannot begin the pass
// after this one until the test has acknowledged this one. The send races
// ctx.Done so that cancelling always unblocks a worker mid-handshake, and a
// test that stops reading can never deadlock the goroutine it is about to join.
type stage struct {
	name   string
	events chan string

	mu     sync.Mutex
	script []pass
	calls  int
}

// pass is what one Run reports: how many rows it touched, and what it returned.
type pass struct {
	rows int
	err  error
}

func newStage(name string, script ...pass) *stage {
	return &stage{name: name, script: script}
}

// run consumes one scripted pass. Past the end of the script every pass reports
// zero rows, which is how a drain ends.
func (s *stage) run(ctx context.Context) (int, error) {
	s.mu.Lock()
	s.calls++
	var p pass
	if len(s.script) > 0 {
		p, s.script = s.script[0], s.script[1:]
	}
	s.mu.Unlock()

	if s.events != nil {
		select {
		case s.events <- s.name:
		case <-ctx.Done():
		}
	}
	return p.rows, p.err
}

func (s *stage) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type extractStage struct{ *stage }

func (e extractStage) Run(ctx context.Context) (app.ExtractResult, error) {
	rows, err := e.run(ctx)
	return app.ExtractResult{EvidenceProcessed: rows}, err
}

type reconcileStage struct{ *stage }

func (r reconcileStage) Run(ctx context.Context) (app.ReconcileResult, error) {
	rows, err := r.run(ctx)
	return app.ReconcileResult{EvidenceProcessed: rows}, err
}

// harness is a worker wired to two scripted stages, a wake signal, and a clock
// the test ticks by hand.
type harness struct {
	t          *testing.T
	extract    *stage
	reconcile  *stage
	events     chan string
	wake       func()
	tick       chan time.Time
	cancel     context.CancelFunc
	workerDone chan struct{}
}

func start(t *testing.T, extract, reconcile *stage) *harness {
	t.Helper()

	events := make(chan string)
	extract.events, reconcile.events = events, events

	wakeCh, wake := newWakeSignal()
	tick := make(chan time.Time)

	worker := newPipelineWorker(extractStage{extract}, reconcileStage{reconcile}, wakeCh)
	worker.newTicker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{
		t: t, extract: extract, reconcile: reconcile,
		events: events, wake: wake, tick: tick, cancel: cancel,
		workerDone: make(chan struct{}),
	}
	go func() {
		defer close(h.workerDone)
		worker.Run(ctx)
	}()
	t.Cleanup(h.stop)
	return h
}

// expect acknowledges the next pass and asserts which stage it belonged to.
func (h *harness) expect(names ...string) {
	h.t.Helper()
	for _, want := range names {
		select {
		case got := <-h.events:
			if got != want {
				h.t.Fatalf("stage %q ran, want %q", got, want)
			}
		case <-time.After(time.Second):
			h.t.Fatalf("no %q pass ran", want)
		}
	}
}

// expectCycle acknowledges a whole drain of an idle pipeline: one empty
// extraction pass, one empty reconciliation pass.
func (h *harness) expectCycle() {
	h.t.Helper()
	h.expect("extract", "reconcile")
}

// expectIdle asserts that no further pass starts. The worker is blocked in its
// select, so a pass beginning here would be one nothing asked for.
func (h *harness) expectIdle() {
	h.t.Helper()
	select {
	case got := <-h.events:
		h.t.Fatalf("stage %q ran with nothing to wake it", got)
	case <-time.After(settleFor):
	}
}

func (h *harness) stop() {
	h.cancel()
	// Draining events while joining: the worker may be mid-handshake, and its
	// send races ctx.Done rather than depending on this loop.
	for {
		select {
		case <-h.events:
		case <-h.workerDone:
			return
		case <-time.After(time.Second):
			h.t.Error("the worker did not stop when its context was cancelled")
			return
		}
	}
}

// --- scheduling ----------------------------------------------------------

// A daemon restarting after a crash has rows mid-pipeline and nobody to notify
// it of them. The first thing a worker does is look.
func TestTheWorkerProcessesAtStartup(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))
	h.expectCycle()
	h.expectIdle()
}

func TestOneWakeDrainsOnce(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))
	h.expectCycle() // startup

	h.wake()
	h.expectCycle()
	h.expectIdle()
}

// The buffer of one in newWakeSignal is the property under test: notifications
// arriving while a drain is in flight collapse into a single pending wake, so
// ten syncs cannot queue ten drains behind them.
func TestQueuedWakesAreCoalesced(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))
	h.expectCycle() // startup

	for i := 0; i < 10; i++ {
		h.wake()
	}

	// Exactly one drain follows, and it covers everything all ten of them were
	// telling the worker about.
	h.expectCycle()
	h.expectIdle()

	if got := h.extract.count(); got != 2 {
		t.Errorf("%d extraction passes, want 2 (startup and one coalesced wake)", got)
	}
}

// A wake that arrives *during* a drain must not be dropped: the Evidence it is
// about may have landed after the pass that would have picked it up.
func TestAWakeDuringADrainIsNotLost(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))

	// Mid-startup-cycle, between the extraction pass and the reconciliation one.
	h.expect("extract")
	h.wake()
	h.expect("reconcile")

	h.expectCycle()
	h.expectIdle()
}

func TestARetryTickDrains(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))
	h.expectCycle() // startup

	h.tick <- time.Now()
	h.expectCycle()
	h.expectIdle()
}

// --- draining ------------------------------------------------------------

// One pass claims a bounded batch, so a backlog needs several. The worker keeps
// going until a pass finds nothing rather than leaving the rest for the next
// wake, which for a first sync of 1,044 artifacts would be a tick a batch.
func TestExtractionRunsUntilAPassFindsNothing(t *testing.T) {
	extract := newStage("extract", pass{rows: 100}, pass{rows: 100}, pass{rows: 37})
	h := start(t, extract, newStage("reconcile"))

	h.expect("extract", "extract", "extract", "extract", "reconcile")
	h.expectIdle()

	if got := extract.count(); got != 4 {
		t.Errorf("%d extraction passes, want 4 (three with rows, one empty)", got)
	}
}

// Reconciliation reads what extraction wrote. Running it first would leave
// everything this wake produced a stage behind until the next one.
func TestReconciliationRunsAfterExtractionIsDrained(t *testing.T) {
	extract := newStage("extract", pass{rows: 100})
	reconcile := newStage("reconcile", pass{rows: 100})
	h := start(t, extract, reconcile)

	h.expect("extract", "extract", "reconcile", "reconcile")
	h.expectIdle()
}

// --- failure and shutdown ------------------------------------------------

// The error a pass returns is the queue being unreachable, never a bad
// artifact — those are recorded against their own rows. A worker that exited on
// one would take the pipeline down for the life of the process over a database
// that was briefly locked.
func TestAFailedPassDoesNotKillTheWorker(t *testing.T) {
	extract := newStage("extract", pass{err: errors.New("database is locked")})
	h := start(t, extract, newStage("reconcile"))

	// The failed extraction does not stop reconciliation: rows an earlier cycle
	// already extracted are still owed a Transaction.
	h.expect("extract", "reconcile")

	h.wake()
	h.expectCycle()
	h.expectIdle()

	if got := extract.count(); got != 2 {
		t.Errorf("%d extraction passes, want 2: the worker stopped after the failure", got)
	}
}

func TestAFailedReconciliationPassDoesNotKillTheWorker(t *testing.T) {
	reconcile := newStage("reconcile", pass{err: errors.New("database is locked")})
	h := start(t, newStage("extract"), reconcile)

	h.expectCycle()

	h.wake()
	h.expectCycle()
	h.expectIdle()
}

// Cancellation is the shutdown path, and runServe waits on it: a worker that
// did not return would hold the process open for the whole shutdown grace.
func TestCancellationStopsTheWorker(t *testing.T) {
	h := start(t, newStage("extract"), newStage("reconcile"))
	h.expectCycle()

	h.cancel()
	select {
	case <-h.workerDone:
	case <-time.After(time.Second):
		t.Fatal("the worker outlived its context")
	}
}

// A worker cancelled mid-drain stops at the pass boundary rather than starting
// another. Extractor and Reconciler each release the rows they claimed and had
// not attempted; what must not happen here is a fresh claim after the context
// has ended.
func TestCancellationStopsTheWorkerMidDrain(t *testing.T) {
	extract := newStage("extract", pass{rows: 100}, pass{rows: 100})
	reconcile := newStage("reconcile")
	h := start(t, extract, reconcile)

	h.expect("extract")
	h.cancel()

	select {
	case <-h.workerDone:
	case <-time.After(time.Second):
		t.Fatal("the worker outlived its context")
	}
	if got := reconcile.count(); got != 0 {
		t.Errorf("reconciliation ran %d times after cancellation, want 0", got)
	}
}

// --- the wake signal -----------------------------------------------------

func TestTheNotifierNeverBlocks(t *testing.T) {
	_, wake := newWakeSignal()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Nothing is reading, and the slot fills on the first call. Every call
		// after it must still return: this is called from an HTTP handler.
		for i := 0; i < 1000; i++ {
			wake()
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the notifier blocked with nobody draining the wake channel")
	}
}
