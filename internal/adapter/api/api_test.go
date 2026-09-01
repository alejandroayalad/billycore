package api

import (
	"bytes"
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
	profile   app.ExtractionProfile
}

func newStubRepo() *stubRepo {
	return &stubRepo{evidence: map[string]domain.Evidence{}, exists: map[string]bool{}}
}

func (s *stubRepo) Insert(_ context.Context, e domain.Evidence, profile app.ExtractionProfile, _ time.Time) (bool, error) {
	if s.insertErr != nil {
		return false, s.insertErr
	}
	key := e.SourceID() + "\x00" + e.SourceReference()
	if s.exists[key] {
		return false, nil
	}
	s.exists[key] = true
	s.evidence[e.ID()] = e
	s.inserted++
	s.profile = profile
	return true, nil
}

func (s *stubRepo) GetByReference(_ context.Context, sourceID, sourceReference string) (domain.Evidence, error) {
	for _, evidence := range s.evidence {
		if evidence.SourceID() == sourceID && evidence.SourceReference() == sourceReference {
			return evidence, nil
		}
	}
	return domain.Evidence{}, app.ErrEvidenceNotFound
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

func (s *stubRepo) ExistsByReference(_ context.Context, sourceID, sourceReference string) (bool, error) {
	return s.exists[sourceID+"\x00"+sourceReference], nil
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
	sources := map[string]app.Source{
		"gmail_primary": {ID: "gmail_primary", Type: domain.SourceGmail, Profile: "NU_EMAIL_V1"},
		"nu_statements": {ID: "nu_statements", Type: domain.SourceBankStatement, Profile: "NU_STATEMENT_V1"},
	}
	targets := map[string]SyncTarget{
		"gmail_primary": {
			Source: sources["gmail_primary"],
			Fetcher: func(context.Context) (app.SourceFetcher, error) {
				if fetcherErr != nil {
					return nil, fetcherErr
				}
				return fetcher, nil
			},
		},
	}
	return NewServer(ingestor, repo, &stubTransactionReader{}, sources, targets, stubPinger{}, notify).Handler(testToken)
}

func request(t *testing.T, handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	return requestBody(t, handler, method, path, token, nil)
}

func requestBody(t *testing.T, handler http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func uploadBody(t *testing.T, sourceID, key, observedAt, contentType string, raw []byte) []byte {
	t.Helper()
	body, err := json.Marshal(evidenceUploadRequest{
		SourceID: sourceID, SourceArtifactKey: key, ObservedAt: observedAt,
		ContentType: contentType, RawContent: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
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
	server := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, nil, stubPinger{err: errors.New("database is locked")}, nil)
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

// --- POST /v1/evidence --------------------------------------------------

func TestPostEvidenceRecordsAConfiguredStatementSource(t *testing.T) {
	repo := newStubRepo()
	handler, woken := notifyingServer(t, repo, &stubFetcher{}, nil)
	body := uploadBody(t, "nu_statements", "statement-2026-07.pdf",
		"2026-08-23T14:02:11-06:00", "application/pdf", []byte("%PDF-1.7\nstatement"))

	w := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body)
	}
	got := decode[evidenceResponse](t, w)
	if got.SourceID != "nu_statements" || got.SourceArtifactKey != "statement-2026-07.pdf" {
		t.Errorf("evidence = %+v", got)
	}
	if got.ObservedAt != "2026-08-23T20:02:11.000Z" || got.ContentBytes != len("%PDF-1.7\nstatement") {
		t.Errorf("evidence time/size = %+v", got)
	}
	if got.RawContent != "" {
		t.Error("POST returned raw_content; only GET may return it")
	}
	rawResponse := decode[map[string]any](t, w)
	if _, present := rawResponse["raw_content"]; present {
		t.Error("POST included raw_content in its JSON representation")
	}
	if repo.profile != "NU_STATEMENT_V1" {
		t.Errorf("profile = %q, want NU_STATEMENT_V1", repo.profile)
	}
	if woken.Load() != 1 {
		t.Errorf("pipeline wakes = %d, want 1", woken.Load())
	}
}

func TestPostEvidenceReturnsTheImmutableOriginalOnRetry(t *testing.T) {
	repo := newStubRepo()
	handler, woken := notifyingServer(t, repo, &stubFetcher{}, nil)
	firstBody := uploadBody(t, "nu_statements", "statement.pdf", observed.Format(time.RFC3339),
		"application/pdf", []byte("%PDF-first"))
	first := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, firstBody)
	want := decode[evidenceResponse](t, first)

	secondBody := uploadBody(t, "nu_statements", "statement.pdf", syncedAt.Format(time.RFC3339),
		"application/pdf", []byte("%PDF-different"))
	second := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, secondBody)
	if second.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", second.Code, second.Body)
	}
	if got := decode[evidenceResponse](t, second); got != want {
		t.Errorf("retry returned %+v, want original %+v", got, want)
	}
	if repo.inserted != 1 || woken.Load() != 1 {
		t.Errorf("inserted/woken = %d/%d, want 1/1", repo.inserted, woken.Load())
	}
}

func TestPostEvidenceRejectsMalformedRequests(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{}, nil)
	valid := uploadBody(t, "nu_statements", "statement.pdf", observed.Format(time.RFC3339),
		"application/pdf", []byte("%PDF-ok"))
	cases := map[string][]byte{
		"invalid base64": []byte(`{"source_id":"nu_statements","source_artifact_key":"x","observed_at":"2026-08-23T14:02:11Z","content_type":"application/pdf","raw_content":"%%%"}`),
		"unknown field":  append(valid[:len(valid)-1], []byte(`,"profile":"NU_STATEMENT_V1"}`)...),
		"trailing JSON":  append(valid, []byte(` {}`)...),
		"missing field":  []byte(`{"source_id":"nu_statements"}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
			}
			got := decode[errorEnvelope](t, w)
			if got.Error.Type != typeMalformedRequest || len(got.Error.Details) == 0 {
				t.Errorf("error = %+v", got.Error)
			}
		})
	}
}

func TestPostEvidenceRejectsInvalidTimeAndSource(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{}, nil)
	cases := []struct {
		name, source, observed string
		status                 int
	}{
		{"invalid time", "nu_statements", "yesterday", http.StatusBadRequest},
		{"unknown source", "missing", observed.Format(time.RFC3339), http.StatusNotFound},
		{"fetchable source", "gmail_primary", observed.Format(time.RFC3339), http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := uploadBody(t, tc.source, "statement.pdf", tc.observed, "application/pdf", []byte("%PDF-ok"))
			w := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, body)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
		})
	}
}

func TestPostEvidenceEnforcesBothSizeLimits(t *testing.T) {
	handler := testServer(t, newStubRepo(), &stubFetcher{}, nil)
	decoded := uploadBody(t, "nu_statements", "large.pdf", observed.Format(time.RFC3339),
		"application/pdf", make([]byte, maxUploadArtifactBytes+1))
	for name, body := range map[string][]byte{
		"decoded artifact": decoded,
		"HTTP body":        make([]byte, maxUploadBodyBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			w := requestBody(t, handler, http.MethodPost, "/v1/evidence", "Bearer "+testToken, body)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413: %s", w.Code, w.Body)
			}
			if got := decode[errorEnvelope](t, w).Error.Type; got != typePayloadTooLarge {
				t.Errorf("type = %q, want %q", got, typePayloadTooLarge)
			}
		})
	}
}

func TestPostEvidenceRequiresAuthentication(t *testing.T) {
	repo := newStubRepo()
	body := uploadBody(t, "nu_statements", "statement.pdf", observed.Format(time.RFC3339),
		"application/pdf", []byte("%PDF-ok"))
	w := requestBody(t, testServer(t, repo, &stubFetcher{}, nil), http.MethodPost, "/v1/evidence", "", body)
	if w.Code != http.StatusUnauthorized || repo.inserted != 0 {
		t.Errorf("status/inserted = %d/%d, want 401/0", w.Code, repo.inserted)
	}
}

func TestStatementSourceCannotBeSynced(t *testing.T) {
	w := request(t, testServer(t, newStubRepo(), &stubFetcher{}, nil), http.MethodPost,
		"/v1/sources/nu_statements/sync", "Bearer "+testToken)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: %s", w.Code, w.Body)
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

// A browser opening the local page (D32) preflights /v1 from a file:// origin.
// The preflight carries no token and must be answered before authentication.
func TestCORSPreflightNeedsNoToken(t *testing.T) {
	handler := serverWith(t, newStubRepo(), nil, nil, nil)
	w := request(t, handler, http.MethodOptions, "/v1/transactions", "")

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Allow-Headers is empty; the Authorization preflight would fail")
	}
}

// Chrome preflights a file:// page reaching loopback as Private Network Access.
// Without the matching allow header the browser blocks the real request.
func TestCORSAllowsPrivateNetworkPreflight(t *testing.T) {
	handler := serverWith(t, newStubRepo(), nil, nil, nil)
	r := httptest.NewRequest(http.MethodOptions, "/v1/transactions", nil)
	r.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("Allow-Private-Network = %q, want true", got)
	}
}

// A real GET still carries the CORS header, so the file:// page can read the
// response. The token gate is unchanged.
func TestCORSHeaderOnAuthenticatedGet(t *testing.T) {
	handler := serverWith(t, newStubRepo(), nil, nil, nil)
	w := request(t, handler, http.MethodGet, "/v1/transactions", "Bearer "+testToken)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}
