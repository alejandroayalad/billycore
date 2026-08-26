package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

var extractedAt = time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)

// --- fakes ------------------------------------------------------------------
//
// The whole use case runs here with no database, no clock and no randomness,
// which is the property ARCHITECTURE.md §4 exists to buy and D19 exists to
// spend.

type fakeQueue struct {
	pending    []app.PendingEvidence
	extracted  []string
	released   []string
	failures   []recordedFailure
	claimErr   error
	failureErr error
}

func (q *fakeQueue) ClaimForExtraction(_ context.Context, limit int, _, _ time.Time) ([]app.PendingEvidence, error) {
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if limit < len(q.pending) {
		return q.pending[:limit], nil
	}
	return q.pending, nil
}

func (q *fakeQueue) MarkExtracted(_ context.Context, evidenceID string, _ time.Time) error {
	q.extracted = append(q.extracted, evidenceID)
	return nil
}

func (q *fakeQueue) Release(_ context.Context, evidenceID string, _ time.Time) error {
	q.released = append(q.released, evidenceID)
	return nil
}

type recordedFailure struct {
	evidenceID string
	reason     string
	retryAt    time.Time
}

func (q *fakeQueue) RecordFailure(_ context.Context, evidenceID, reason string, retryAt time.Time) error {
	q.failures = append(q.failures, recordedFailure{evidenceID, reason, retryAt})
	return q.failureErr
}

type fakeClaims struct {
	saved []domain.Claim
	err   error

	// occupied mimics the constraint: an evidence id that already has an active
	// interpretation cannot get a second one.
	occupied map[string]bool
}

func (c *fakeClaims) Save(_ context.Context, claim domain.Claim, _ time.Time) (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	for _, evidenceID := range claim.EvidenceIDs() {
		if c.occupied[evidenceID] {
			return false, nil
		}
	}
	if c.occupied == nil {
		c.occupied = map[string]bool{}
	}
	for _, evidenceID := range claim.EvidenceIDs() {
		c.occupied[evidenceID] = true
	}
	c.saved = append(c.saved, claim)
	return true, nil
}

// fakeInterpreter answers per artifact, keyed by the bytes it is handed.
type fakeInterpreter struct {
	answers map[string]map[domain.FieldName]domain.ClaimField
	errs    map[string]error
	panics  map[string]any
}

func (i fakeInterpreter) Interpret(raw []byte) (map[domain.FieldName]domain.ClaimField, error) {
	if v, ok := i.panics[string(raw)]; ok {
		panic(v)
	}
	if err, ok := i.errs[string(raw)]; ok {
		return nil, err
	}
	fields, ok := i.answers[string(raw)]
	if !ok {
		return nil, app.ErrNoInterpretation
	}
	return fields, nil
}

func newExtractor(q *fakeQueue, c *fakeClaims, i fakeInterpreter) *app.Extractor {
	ids := 0
	return &app.Extractor{
		Queue:       q,
		Claims:      c,
		Interpreter: i,
		NewID: func() (string, error) {
			ids++
			return "claim-" + string(rune('a'+ids-1)), nil
		},
		Now: func() time.Time { return extractedAt },
	}
}

func moneyFields(t *testing.T) map[domain.FieldName]domain.ClaimField {
	t.Helper()
	amount, err := domain.NewIntField(100000, domain.High)
	if err != nil {
		t.Fatalf("NewIntField: %v", err)
	}
	currency, err := domain.NewTextField("MXN", domain.Low)
	if err != nil {
		t.Fatalf("NewTextField: %v", err)
	}
	return map[domain.FieldName]domain.ClaimField{
		domain.FieldAmountMinor: amount,
		domain.FieldCurrency:    currency,
	}
}

// --- tests ------------------------------------------------------------------

func TestExtractionProducesAnActiveClaimWithProvenance(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-1", RawContent: []byte("outflow")}}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
	})

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ClaimsCreated != 1 || result.EvidenceProcessed != 1 || result.Unrecognised != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(claims.saved) != 1 {
		t.Fatalf("saved %d claims, want 1", len(claims.saved))
	}

	claim := claims.saved[0]
	// D37: the Claim arrives ACTIVE, having been proposed and activated rather
	// than constructed active.
	if claim.State() != domain.ClaimActive {
		t.Errorf("state = %s, want ACTIVE", claim.State())
	}
	provenance := claim.EvidenceIDs()
	if len(provenance) != 1 || provenance[0] != "ev-1" {
		t.Errorf("provenance = %v, want [ev-1]", provenance)
	}
	if money, ok := claim.Money(); !ok || money.Minor() != 100000 {
		t.Errorf("money = %v (ok=%v)", money, ok)
	}
}

// An artifact no template recognises is an ordinary outcome, not a failure:
// 244 of 1,044 do exactly this. It produces no Claim and still advances, or the
// pipeline re-reads all 244 forever.
func TestUnrecognisedEvidenceProducesNoClaimAndStillAdvances(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-2", RawContent: []byte("marketing")}}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{})

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Unrecognised != 1 || result.ClaimsCreated != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(claims.saved) != 0 {
		t.Errorf("saved %d claims, want none", len(claims.saved))
	}
	if len(queue.extracted) != 1 || queue.extracted[0] != "ev-2" {
		t.Errorf("advanced %v, want [ev-2]", queue.extracted)
	}
}

// A genuine interpretation failure is not the same as an unrecognised template.
// The row keeps its stage — retry is not a stage (DATA_MODEL.md §7) — and the
// failure is recorded against it rather than returned to the caller.
func TestAFailedInterpretationIsRecordedAndDoesNotAdvance(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-3", RawContent: []byte("broken"), Attempts: 1}}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		errs: map[string]error{"broken": errors.New("template changed — no Monto")},
	})

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v — a bad artifact must not end the pass", err)
	}
	if result.Failed != 1 || result.ClaimsCreated != 0 || result.Unrecognised != 0 {
		t.Errorf("result = %+v", result)
	}
	if len(queue.extracted) != 0 {
		t.Errorf("advanced %v; a failed extraction leaves the stage alone", queue.extracted)
	}
	if len(queue.failures) != 1 || queue.failures[0].evidenceID != "ev-3" {
		t.Fatalf("failures = %+v, want one against ev-3", queue.failures)
	}
	// Backed off rather than released: the next pass must not pick it up
	// immediately and spin.
	if !queue.failures[0].retryAt.After(extractedAt) {
		t.Errorf("retryAt = %v, want later than now (%v)", queue.failures[0].retryAt, extractedAt)
	}
	if len(claims.saved) != 0 {
		t.Errorf("saved %d claims, want none", len(claims.saved))
	}
}

// SECURITY.md §7: one bad email must not stop the pipeline. The artifacts
// behind a failing one still get processed in the same pass.
func TestOneBadArtifactDoesNotStopThePass(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{
		{ID: "ev-good-1", RawContent: []byte("outflow")},
		{ID: "ev-bad", RawContent: []byte("broken")},
		{ID: "ev-good-2", RawContent: []byte("outflow")},
	}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
		errs:    map[string]error{"broken": errors.New("template changed")},
	})

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.EvidenceProcessed != 3 || result.ClaimsCreated != 2 || result.Failed != 1 {
		t.Errorf("result = %+v, want 3 processed / 2 claimed / 1 failed", result)
	}
}

// A panicking parser is contained per artifact, not per batch. Recovering one
// level up would abandon every row queued behind it, still holding its lock.
func TestAPanickingParserIsContainedAndThePipelineKeepsGoing(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{
		{ID: "ev-panic", RawContent: []byte("boom")},
		{ID: "ev-good", RawContent: []byte("outflow")},
	}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
		panics:  map[string]any{"boom": "index out of range in Monto: $1,000.00"},
	})
	var panicked []string
	var stacks [][]byte
	x.OnPanic = func(evidenceID string, stack []byte) {
		panicked = append(panicked, evidenceID)
		stacks = append(stacks, stack)
	}

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v — a panic must not end the pass", err)
	}
	if result.Failed != 1 || result.ClaimsCreated != 1 {
		t.Errorf("result = %+v, want 1 failed and 1 claimed", result)
	}
	if len(panicked) != 1 || panicked[0] != "ev-panic" {
		t.Errorf("OnPanic saw %v, want [ev-panic]", panicked)
	}
	if len(stacks) != 1 || len(stacks[0]) == 0 {
		t.Error("no stack was captured")
	}
	// SECURITY.md §10: the stack, not the artifact.
	if contains(string(stacks[0]), "$1,000.00") {
		t.Error("the captured stack carries the artifact")
	}
	// The recorded reason names the panic's type and never its value — a panic
	// value is often a string, and a string in a parser is often the email.
	if len(queue.failures) != 1 {
		t.Fatalf("failures = %+v", queue.failures)
	}
	if got := queue.failures[0].reason; !contains(got, "PANIC") || contains(got, "Monto") {
		t.Errorf("reason = %q", got)
	}
}

// last_error is a diagnostic, and it is redacted (SECURITY.md §10). Nothing that
// came out of the artifact may reach it — and the real parsers embed the text
// they choked on in their error messages, which is exactly the leak.
func TestTheRecordedReasonNeverCarriesTheArtifact(t *testing.T) {
	secret := `Monto: $1,000.00 — Persona Dos, ••••7662`
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-4", RawContent: []byte(secret)}}}
	x := newExtractor(queue, &fakeClaims{}, fakeInterpreter{
		errs: map[string]error{secret: fmt.Errorf("parser: not an amount: %q", secret)},
	})

	if _, err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(queue.failures) != 1 {
		t.Fatalf("failures = %+v", queue.failures)
	}
	got := queue.failures[0].reason
	for _, leak := range []string{secret, "Monto", "Persona Dos", "7662", "$1,000.00"} {
		if contains(got, leak) {
			t.Errorf("reason %q carries %q from the artifact", got, leak)
		}
	}
	// An error that cannot describe itself safely is stored as its class alone,
	// never as its own message.
	if got != "INTERPRET" {
		t.Errorf("reason = %q, want the bare class for an unclassified error", got)
	}
}

// An error that knows how to describe itself safely gets to.
func TestARedactedErrorContributesItsOwnReason(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-5", RawContent: []byte("broken")}}}
	x := newExtractor(queue, &fakeClaims{}, fakeInterpreter{
		errs: map[string]error{"broken": safeError{
			full: `parser: not an amount: "Monto: $1,0O0.00"`,
			safe: "the amount did not parse",
		}},
	})

	if _, err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := queue.failures[0].reason; got != "INTERPRET: the amount did not parse" {
		t.Errorf("reason = %q", got)
	}
}

// A Claim asserting nothing is not a Claim. The domain rejects Billy's own
// interpretation, and that is recorded as a failure against the row rather than
// written as an empty Claim.
func TestAnInterpretationWithNoFieldsIsRejected(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{{ID: "ev-5", RawContent: []byte("empty")}}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{
			"empty": {},
		},
	})

	if _, err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(claims.saved) != 0 {
		t.Errorf("saved %d claims, want none", len(claims.saved))
	}
	if len(queue.failures) != 1 || queue.failures[0].reason != "CLAIM" {
		t.Errorf("failures = %+v, want one classed CLAIM", queue.failures)
	}
}

// The backoff is exponential in the attempt count, so a poisonous artifact
// costs one parse a day rather than one per pass — and a template Nu fixes is
// picked up again without anyone resetting anything.
func TestTheBackoffGrowsWithTheAttemptCount(t *testing.T) {
	var delays []time.Duration
	for _, attempts := range []int{1, 2, 3, 100} {
		queue := &fakeQueue{pending: []app.PendingEvidence{
			{ID: "ev", RawContent: []byte("broken"), Attempts: attempts},
		}}
		x := newExtractor(queue, &fakeClaims{}, fakeInterpreter{
			errs: map[string]error{"broken": errors.New("nope")},
		})
		if _, err := x.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		delays = append(delays, queue.failures[0].retryAt.Sub(extractedAt))
	}
	if delays[0] != app.RetryBase {
		t.Errorf("first retry after %v, want %v", delays[0], app.RetryBase)
	}
	if delays[1] != 2*app.RetryBase || delays[2] != 4*app.RetryBase {
		t.Errorf("backoff = %v, want it doubling", delays)
	}
	// Capped, so a permanently broken artifact still comes back — which is what
	// makes a fixed parser self-healing rather than needing a hand.
	if delays[3] != app.RetryCap {
		t.Errorf("hundredth retry after %v, want the cap %v", delays[3], app.RetryCap)
	}
}

// A pass shutting down hands back what it never attempted, rather than leaving
// those rows to sit out a lease for work that never happened.
func TestShutdownReleasesTheRowsItNeverAttempted(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{
		{ID: "ev-1", RawContent: []byte("outflow")},
		{ID: "ev-2", RawContent: []byte("outflow")},
		{ID: "ev-3", RawContent: []byte("outflow")},
	}}
	x := newExtractor(queue, &fakeClaims{}, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Released on a cancelled context — the release must not inherit the
	// cancellation, or every row is stranded for the rest of its lease.
	if len(queue.released) != 3 {
		t.Errorf("released %v, want all three", queue.released)
	}
}

func TestExtractionRunsWithoutAClockOrRandomnessOfItsOwn(t *testing.T) {
	queue := &fakeQueue{pending: []app.PendingEvidence{
		{ID: "ev-6", RawContent: []byte("outflow")},
		{ID: "ev-7", RawContent: []byte("outflow")},
	}}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
	})

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.CompletedAt.Equal(extractedAt) {
		t.Errorf("CompletedAt = %v, want the injected clock %v", result.CompletedAt, extractedAt)
	}
	if len(claims.saved) != 2 {
		t.Fatalf("saved %d claims, want 2", len(claims.saved))
	}
	if claims.saved[0].ID() == claims.saved[1].ID() {
		t.Errorf("both claims got id %q", claims.saved[0].ID())
	}
	for _, c := range claims.saved {
		if !c.CreatedAt().Equal(extractedAt) {
			t.Errorf("created_at = %v, want the injected clock", c.CreatedAt())
		}
	}
}

func TestTheBatchSizeBoundsOnePass(t *testing.T) {
	queue := &fakeQueue{}
	for i := 0; i < 5; i++ {
		queue.pending = append(queue.pending, app.PendingEvidence{
			ID: "ev-" + string(rune('0'+i)), RawContent: []byte("outflow"),
		})
	}
	claims := &fakeClaims{}
	x := newExtractor(queue, claims, fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
	})
	x.Batch = 2

	result, err := x.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.EvidenceProcessed != 2 {
		t.Errorf("processed %d, want the batch size 2", result.EvidenceProcessed)
	}
}

// safeError is an error that can describe itself without its input — what the
// real parser adapter returns.
type safeError struct {
	full string
	safe string
}

func (e safeError) Error() string    { return e.full }
func (e safeError) Redacted() string { return e.safe }

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Re-running a pass over work already done writes nothing and reports it, the
// way M1's second sync reported 1,044 skipped rather than 1,044 created.
func TestReRunningExtractionCreatesNoSecondClaim(t *testing.T) {
	pending := []app.PendingEvidence{{ID: "ev-1", RawContent: []byte("outflow")}}
	claims := &fakeClaims{}
	interpreter := fakeInterpreter{
		answers: map[string]map[domain.FieldName]domain.ClaimField{"outflow": moneyFields(t)},
	}

	first, err := newExtractor(&fakeQueue{pending: pending}, claims, interpreter).Run(context.Background())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.ClaimsCreated != 1 || first.Skipped != 0 {
		t.Fatalf("first result = %+v", first)
	}

	// The same artifact, claimed again — a lease that lapsed under a slow pass,
	// or an operator running extraction twice.
	queue := &fakeQueue{pending: pending}
	second, err := newExtractor(queue, claims, interpreter).Run(context.Background())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.ClaimsCreated != 0 || second.Skipped != 1 || second.Failed != 0 {
		t.Errorf("second result = %+v, want 0 created / 1 skipped", second)
	}
	if len(claims.saved) != 1 {
		t.Errorf("%d claims exist for one artifact", len(claims.saved))
	}
	// Skipped is not failed: the row advances rather than backing off to be
	// retried forever.
	if len(queue.extracted) != 1 || queue.extracted[0] != "ev-1" {
		t.Errorf("advanced %v, want [ev-1]", queue.extracted)
	}
	if len(queue.failures) != 0 {
		t.Errorf("recorded %+v; an already-interpreted artifact is not a failure", queue.failures)
	}
}
