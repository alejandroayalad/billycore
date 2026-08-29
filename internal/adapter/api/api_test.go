package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const testToken = "a-token-nobody-should-see-in-a-log"

var (
	observed = time.Date(2026, 8, 16, 17, 44, 0, 0, time.UTC)
	syncedAt = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
)

type stubRepo struct {
	evidence  map[string]domain.Evidence
	exists    map[string]bool
	getErr    error
	inserted  int
	insertErr error
}

func newStubRepo() *stubRepo {
	return &stubRepo{evidence: map[string]domain.Evidence{}, exists: map[string]bool{}}
}

func (s *stubRepo) Insert(_ context.Context, e domain.Evidence, _ time.Time) (bool, error) {
	if s.insertErr != nil {
		return false, s.insertErr
	}
	if s.exists[e.SourceReference()] {
		return false, nil
	}
	s.exists[e.SourceReference()] = true
	s.evidence[e.ID()] = e
	s.inserted++
	return true, nil
}

func (s *stubRepo) GetByID(_ context.Context, id string) (domain.Evidence, error) {
	if s.getErr != nil {
		return domain.Evidence{}, s.getErr
	}
	e, found := s.evidence[id]
	if !found {
		return domain.Evidence{}, app.ErrEvidenceNotFound
	}
	return e, nil
}

func (s *stubRepo) ExistsByReference(_ context.Context, _, sourceReference string) (bool, error) {
	return s.exists[sourceReference], nil
}

type stubFetcher struct {
	references []string
	fetchErr   error

	// When release is non-nil, Fetch announces itself on entered and then blocks
	// until release is closed. That makes "a sync is in flight" an observable
	// state a test can wait for, rather than a window it has to poll for.
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stubFetcher) ListReferences(context.Context) ([]string, error) { return s.references, nil }

func (s *stubFetcher) Fetch(_ context.Context, reference string) (app.Artifact, error) {
	if s.release != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	if s.fetchErr != nil {
		return app.Artifact{}, s.fetchErr
	}
	return app.Artifact{
		Reference:   reference,
		ContentType: "message/rfc822",
		ObservedAt:  observed,
		Content:     []byte("From: nu@nu.com.mx\r\n\r\n" + reference),
	}, nil
}

type stubPinger struct{ err error }

func (s stubPinger) PingContext(context.Context) error { return s.err }

// testServer wires a Server with no database, no network, and no clock — and
// with no pipeline listening, which is what most of these tests are about. That
// nil is what proves a Server with nobody to notify is a valid Server: every
// sync below runs through the same branch a daemon with no worker would.
func testServer(t *testing.T, repo app.EvidenceRepository, fetcher app.SourceFetcher, fetcherErr error) http.Handler {
	t.Helper()
	return serverWith(t, repo, fetcher, fetcherErr, nil)
}

// notifyingServer is testServer with the pipeline's wake callback attached, and
// a count of how many times it fired.
func notifyingServer(t *testing.T, repo app.EvidenceRepository, fetcher app.SourceFetcher, fetcherErr error) (http.Handler, *atomic.Int64) {
	t.Helper()
	woken := new(atomic.Int64)
	return serverWith(t, repo, fetcher, fetcherErr, func() { woken.Add(1) }), woken
}

func serverWith(t *testing.T, repo app.EvidenceRepository, fetcher app.SourceFetcher, fetcherErr error, notify func()) http.Handler {
	t.Helper()
	ingestor := &app.Ingestor{
		Repo:  repo,
		NewID: func() (string, error) { return "evidence-" + time.Now().Format("150405.000000000"), nil },
		Now:   func() time.Time { return syncedAt },
	}
	sources := map[string]SyncTarget{
		"gmail_primary": {
			Source: app.Source{ID: "gmail_primary", Type: domain.SourceGmail},
			Fetcher: func(context.Context) (app.SourceFetcher, error) {
				if fetcherErr != nil {
					return nil, fetcherErr
				}
				return fetcher, nil
			},
		},
	}
	return NewServer(ingestor, repo, &stubTransactionReader{}, sources, stubPinger{}, notify).Handler(testToken)
}

func request(t *testing.T, handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return body
}

// --- authentication ------------------------------------------------------

func TestV1RequiresTheBearerToken(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{}, nil)

	cases := map[string]string{
		"missing":       "",
		"wrong token":   "Bearer not-the-token",
		"wrong scheme":  "Basic " + testToken,
		"bare token":    testToken,
		"empty bearer":  "Bearer ",
		"the token, ok": "Bearer " + testToken,
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			w := request(t, handler, http.MethodGet, "/v1/evidence/whatever", header)
			if name == "the token, ok" {
				if w.Code == http.StatusUnauthorized {
					t.Fatal("the correct token was rejected")
				}
				return
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			body := decode[errorEnvelope](t, w)
			if body.Error.Type != typeUnauthorized {
				t.Errorf("type = %q, want %q", body.Error.Type, typeUnauthorized)
			}
			// API.md §2: missing and wrong must be indistinguishable.
			if body.Error.Message != "A valid bearer token is required." {
				t.Errorf("message distinguishes why the token failed: %q", body.Error.Message)
			}
			if body.Error.Details != nil {
				t.Error("violations are present on 400 and 422 only")
			}
		})
	}
}

func TestHealthzNeedsNoToken(t *testing.T) {
	w := request(t, testServer(t, newStubRepo(), &stubFetcher{}, nil), http.MethodGet, "/healthz", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestHealthzReportsUnreachableStorage(t *testing.T) {
	server := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, stubPinger{err: errors.New("database is locked")}, nil)
	w := request(t, server.Handler(testToken), http.MethodGet, "/healthz", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// The credential must not survive the middleware. SECURITY.md §10 puts the
// redaction here rather than at every call site downstream.
func TestAuthorizationIsRedactedBeforeTheHandler(t *testing.T) {
	var seen string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	})
	r := httptest.NewRequest(http.MethodGet, "/v1/evidence/x", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	authenticate(testToken, inner).ServeHTTP(httptest.NewRecorder(), r)

	if seen == "" {
		t.Fatal("the handler did not run")
	}
	if seen != "REDACTED" {
		t.Errorf("Authorization reached the handler as %q", seen)
	}
}

// --- POST /v1/sources/{id}/sync -----------------------------------------

func TestSyncRecordsAndThenSkips(t *testing.T) {
	repo := newStubRepo()
	handler := testServer(t, repo, &stubFetcher{references: []string{"msg-a", "msg-b", "msg-c"}}, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (API.md §5: synchronous, not 202): %s", w.Code, w.Body)
	}
	first := decode[syncResponse](t, w)
	want := syncResponse{
		SourceID:            "gmail_primary",
		ArtifactsDiscovered: 3,
		EvidenceCreated:     3,
		EvidenceSkipped:     0,
		CompletedAt:         "2026-08-25T09:00:00.000Z",
	}
	if first != want {
		t.Errorf("first sync = %+v, want %+v", first, want)
	}

	// The second run is where M1's idempotency requirement is visible.
	w = request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	second := decode[syncResponse](t, w)
	want.EvidenceCreated, want.EvidenceSkipped = 0, 3
	if second != want {
		t.Errorf("second sync = %+v, want %+v", second, want)
	}
	if repo.inserted != 3 {
		t.Errorf("%d rows inserted across two syncs, want 3", repo.inserted)
	}
}

func TestSyncReportsAnUnconfiguredSource(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{}, nil)
	w := request(t, handler, http.MethodPost, "/v1/sources/hsbc_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := decode[errorEnvelope](t, w).Error.Type; got != typeNotFound {
		t.Errorf("type = %q, want %q", got, typeNotFound)
	}
}

func TestSyncReportsAnUnreachableSource(t *testing.T) {
	handler := testServer(t, newStubRepo(), nil, errors.New("no Gmail credentials: run `billycore auth` first"))
	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	body := decode[errorEnvelope](t, w)
	if body.Error.Type != typeUnavailable {
		t.Errorf("type = %q, want %q", body.Error.Type, typeUnavailable)
	}
	// The reason may name a credentials file; it must never carry a credential.
	if len(body.Error.Message) == 0 {
		t.Error("no message")
	}
}

func TestSyncReportsAFailedFetchAsAnInternalError(t *testing.T) {
	fetcher := &stubFetcher{references: []string{"msg-a"}, fetchErr: errors.New("gmail API returned 500")}
	handler := testServer(t, newStubRepo(), fetcher, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := decode[errorEnvelope](t, w).Error.Type; got != typeInternalError {
		t.Errorf("type = %q, want %q", got, typeInternalError)
	}
}

// API.md §5 promises a 409 rather than letting two syncs of one Source race.
func TestSyncRefusesToOverlapItself(t *testing.T) {
	fetcher := &stubFetcher{
		references: []string{"msg-a"},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	handler := testServer(t, newStubRepo(), fetcher, nil)

	first := make(chan int, 1)
	go func() {
		first <- request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken).Code
	}()

	<-fetcher.entered // the first sync is now genuinely in flight
	second := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken).Code
	close(fetcher.release)

	if code := <-first; code != http.StatusOK {
		t.Errorf("first sync = %d, want 200", code)
	}
	if second != http.StatusConflict {
		t.Errorf("overlapping sync = %d, want 409", second)
	}

	// The lock is released when the sync ends, so the next one is served.
	if again := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken).Code; again != http.StatusOK {
		t.Errorf("sync after the first finished = %d, want 200", again)
	}
}

// --- waking the pipeline -------------------------------------------------

// D45: the handler notifies, it does not process. What the callback must be is
// cheap and non-blocking; what it must not be is extraction running inside the
// request that produced the Evidence.

func TestASuccessfulSyncWakesThePipelineOnce(t *testing.T) {
	handler, woken := notifyingServer(t, newStubRepo(), &stubFetcher{references: []string{"msg-a", "msg-b"}}, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if got := woken.Load(); got != 1 {
		t.Errorf("the pipeline was woken %d times, want 1 — once per sync, not once per artifact", got)
	}
}

// A sync that recorded nothing new still wakes it. The handler knows how many
// rows it created, and could stay quiet on zero; it does not, because Evidence
// left mid-pipeline by an earlier run is exactly what a re-sync is often for,
// and a wake that finds nothing costs one empty query per stage.
func TestARepeatedSyncStillWakesThePipeline(t *testing.T) {
	handler, woken := notifyingServer(t, newStubRepo(), &stubFetcher{references: []string{"msg-a"}}, nil)

	request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)

	if got := woken.Load(); got != 2 {
		t.Errorf("the pipeline was woken %d times across two syncs, want 2", got)
	}
}

func TestAFailedSyncDoesNotWakeThePipeline(t *testing.T) {
	fetcher := &stubFetcher{references: []string{"msg-a"}, fetchErr: errors.New("gmail API returned 500")}
	handler, woken := notifyingServer(t, newStubRepo(), fetcher, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := woken.Load(); got != 0 {
		t.Errorf("a failed sync woke the pipeline %d times, want 0", got)
	}
}

func TestAnUnreachableSourceDoesNotWakeThePipeline(t *testing.T) {
	handler, woken := notifyingServer(t, newStubRepo(), nil, errors.New("no Gmail credentials"))

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if got := woken.Load(); got != 0 {
		t.Errorf("an unreachable Source woke the pipeline %d times, want 0", got)
	}
}

func TestAnUnconfiguredSourceDoesNotWakeThePipeline(t *testing.T) {
	handler, woken := notifyingServer(t, newStubRepo(), &stubFetcher{}, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/hsbc_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := woken.Load(); got != 0 {
		t.Errorf("an unconfigured Source woke the pipeline %d times, want 0", got)
	}
}

// Every other test in this file runs against a Server with no callback at all,
// which is the assertion this one only makes explicit: a daemon with no worker
// serves normally rather than panicking on the first successful sync.
func TestSyncWithNoPipelineListening(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{references: []string{"msg-a"}}, nil)

	w := request(t, handler, http.MethodPost, "/v1/sources/gmail_primary/sync", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
}

// --- GET /v1/evidence/{id} ----------------------------------------------

func TestGetEvidenceReturnsTheArtifact(t *testing.T) {
	repo := newStubRepo()
	content := []byte("From: nu@nu.com.mx\r\n\r\nMonto: $1,000.00")
	evidence, err := domain.NewEvidence(
		"evidence-1", "gmail_primary", domain.SourceGmail,
		"18f2a9c4d5e6", "message/rfc822", content, observed,
	)
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	repo.evidence["evidence-1"] = evidence

	w := request(t, testServer(t, repo, &stubFetcher{}, nil), http.MethodGet, "/v1/evidence/evidence-1", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}

	got := decode[evidenceResponse](t, w)
	want := evidenceResponse{
		ID:       "evidence-1",
		SourceID: "gmail_primary",
		// D23: source_reference is spelled source_artifact_key on the wire, and
		// this is the only place the two names meet.
		SourceArtifactKey: "18f2a9c4d5e6",
		ObservedAt:        "2026-08-16T17:44:00.000Z",
		ContentType:       "message/rfc822",
		ContentBytes:      len(content),
		RawContent:        base64.StdEncoding.EncodeToString(content),
	}
	if got != want {
		t.Errorf("evidence = %+v, want %+v", got, want)
	}

	// The pipeline stage is infrastructure and must not appear (DATA_MODEL.md §7).
	raw := decode[map[string]any](t, w)
	for _, field := range []string{"processing_stage", "attempts", "last_error", "locked_until", "source_reference"} {
		if _, present := raw[field]; present {
			t.Errorf("response carries %q", field)
		}
	}
}

func TestGetEvidenceReportsAMissingID(t *testing.T) {
	w := request(t, testServer(t, newStubRepo(), &stubFetcher{}, nil), http.MethodGet, "/v1/evidence/nope", "Bearer "+testToken)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := decode[errorEnvelope](t, w).Error.Type; got != typeNotFound {
		t.Errorf("type = %q, want %q", got, typeNotFound)
	}
}

func TestGetEvidenceReportsAStorageFailure(t *testing.T) {
	repo := newStubRepo()
	repo.getErr = errors.New("database is locked")
	w := request(t, testServer(t, repo, &stubFetcher{}, nil), http.MethodGet, "/v1/evidence/evidence-1", "Bearer "+testToken)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// --- envelope ------------------------------------------------------------

func TestPanicsBecomeAnInternalError(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	w := httptest.NewRecorder()
	recoverPanics(panicking).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/evidence/x", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := decode[errorEnvelope](t, w).Error.Type; got != typeInternalError {
		t.Errorf("type = %q, want %q", got, typeInternalError)
	}
}

func TestEveryErrorUsesOneEnvelope(t *testing.T) {
	w := request(t, testServer(t, newStubRepo(), &stubFetcher{}, nil), http.MethodGet, "/v1/evidence/nope", "Bearer "+testToken)
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	raw := decode[map[string]json.RawMessage](t, w)
	if len(raw) != 1 {
		t.Errorf("the envelope has %d top-level keys, want just \"error\"", len(raw))
	}
	if _, present := raw["error"]; !present {
		t.Error("no \"error\" key")
	}
}
