// Package api is BillyCore's HTTP boundary: the /v1 contract in API.md, and
// nothing else.
//
// It maps wire shapes to use cases and back. No financial rule is decided here
// — invariants live in domain constructors, so a Claim arriving over HTTP and
// one produced by a parser meet the same validator (D6, D11).
package api

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

// timeLayout is API.md §1's timestamp: RFC 3339, always UTC, always with
// offset. Milliseconds are kept because observed_at has them — truncating a
// timestamp on the way out would make the API disagree with the database about
// when something was observed.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// Pinger is the health check's view of storage. Declared here so this package
// never learns which database it is talking to.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// SyncTarget is one configured Source, ready to be synced.
//
// The fetcher is built per request rather than held: for Gmail that is where
// the access token is refreshed, and a daemon that built one at startup would
// stop being able to sync an hour later.
type SyncTarget struct {
	Source  app.Source
	Fetcher func(ctx context.Context) (app.SourceFetcher, error)
}

// Server holds what the handlers need. One per process.
type Server struct {
	ingestor *app.Ingestor
	evidence app.EvidenceRepository
	sources  map[string]SyncTarget
	storage  Pinger

	// onEvidenceAvailable is called after Evidence has been recorded, and it is
	// how the pipeline learns there is something to do. Deliberately unnamed
	// about *which* Evidence and deliberately not about sync: POST /v1/evidence
	// will call the same callback, and so will anything else that writes an
	// artifact. It is a notification, not a handoff — the work happens on a
	// background pass, and this handler does not wait for it (D45).
	//
	// Nil is valid and means nothing is listening.
	onEvidenceAvailable func()

	// syncing guards against two syncs of one Source overlapping — API.md §5
	// promises a 409 for that, and without it the second one would re-list the
	// whole mailbox and race the first for every insert.
	mu      sync.Mutex
	syncing map[string]bool
}

func NewServer(ingestor *app.Ingestor, evidence app.EvidenceRepository, sources map[string]SyncTarget, storage Pinger, onEvidenceAvailable func()) *Server {
	return &Server{
		ingestor:            ingestor,
		evidence:            evidence,
		sources:             sources,
		storage:             storage,
		onEvidenceAvailable: onEvidenceAvailable,
		syncing:             map[string]bool{},
	}
}

// Handler builds the routing table. /healthz is open; every /v1 route is behind
// the bearer token (API.md §2, §11).
func (s *Server) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	v1 := http.NewServeMux()
	v1.HandleFunc("POST /v1/sources/{id}/sync", s.handleSync)
	v1.HandleFunc("GET /v1/evidence/{id}", s.handleGetEvidence)
	mux.Handle("/v1/", authenticate(token, v1))

	return recoverPanics(logRequests(mux))
}

// handleHealthz reports whether the process can serve requests, storage
// included. Unauthenticated and deliberately outside /v1 (API.md §11).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.storage != nil {
		if err := s.storage.PingContext(r.Context()); err != nil {
			slog.Error("health check failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, typeUnavailable,
				"BillyCore cannot reach its database.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "api_version": "v1"})
}

// syncResponse is the summary in API.md §5.
type syncResponse struct {
	SourceID            string `json:"source_id"`
	ArtifactsDiscovered int    `json:"artifacts_discovered"`
	EvidenceCreated     int    `json:"evidence_created"`
	EvidenceSkipped     int    `json:"evidence_skipped"`
	CompletedAt         string `json:"completed_at"`
}

// handleSync fetches from a configured Source and records what it yields.
//
// Synchronous, and 200 rather than 202 (API.md §5). Staged processing does not
// imply an asynchronous HTTP contract: sync records Evidence, and extraction
// advances behind it on its own pass. There is no job resource and nothing to
// poll.
//
// The counts it answers with are therefore about *recording*, not about
// interpreting. A sync that reports three Evidence created has created three
// artifacts; whether they have become Claims yet is a question for the moment
// after the pipeline has drained, and the response deliberately does not
// pretend to answer it.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")

	target, configured := s.sources[sourceID]
	if !configured {
		// The Source is not in sources.json (D26).
		writeError(w, http.StatusNotFound, typeNotFound, "No such Source is configured.")
		return
	}
	if !s.beginSync(sourceID) {
		writeError(w, http.StatusConflict, typeConflict, "A sync for this Source is already in flight.")
		return
	}
	defer s.endSync(sourceID)

	fetcher, err := target.Fetcher(r.Context())
	if err != nil {
		// Credentials missing or expired, or the Source is unreachable. That is
		// a condition of the moment, not a bad request.
		slog.Error("cannot reach source", "source_id", sourceID, "error", err)
		writeError(w, http.StatusServiceUnavailable, typeUnavailable,
			"BillyCore cannot reach this Source right now.")
		return
	}

	result, err := s.ingestor.Sync(r.Context(), target.Source, fetcher)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return // the client hung up; there is nobody to answer
		}
		slog.Error("sync failed", "source_id", sourceID, "error", err)
		writeError(w, http.StatusInternalServerError, typeInternalError,
			"The sync did not complete. Evidence recorded before the failure was kept; running it again resumes.")
		return
	}

	// Only after a sync that completed. A failed one has left Evidence behind
	// too — the ingestor keeps what it recorded before the failure — but that
	// artifact is picked up by the retry tick rather than by a wake, and waking
	// on a failure would mean every unreachable Source drove the pipeline.
	if s.onEvidenceAvailable != nil {
		s.onEvidenceAvailable()
	}

	writeJSON(w, http.StatusOK, syncResponse{
		SourceID:            result.SourceID,
		ArtifactsDiscovered: result.ArtifactsDiscovered,
		EvidenceCreated:     result.EvidenceCreated,
		EvidenceSkipped:     result.EvidenceSkipped,
		CompletedAt:         result.CompletedAt.UTC().Format(timeLayout),
	})
}

func (s *Server) beginSync(sourceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncing[sourceID] {
		return false
	}
	s.syncing[sourceID] = true
	return true
}

func (s *Server) endSync(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.syncing, sourceID)
}

// evidenceResponse is the Evidence representation from API.md §4.
//
// `source_artifact_key` is the wire spelling of `source_reference`. D23 makes
// this the only place the two names meet: the mapping exists here and nowhere
// else in the codebase.
type evidenceResponse struct {
	ID                string `json:"id"`
	SourceID          string `json:"source_id"`
	SourceArtifactKey string `json:"source_artifact_key"`
	ObservedAt        string `json:"observed_at"`
	ContentType       string `json:"content_type,omitempty"`
	ContentBytes      int    `json:"content_bytes"`
	RawContent        string `json:"raw_content,omitempty"`
}

// handleGetEvidence returns one Evidence, raw content included.
//
// The only endpoint that returns raw content (API.md §6). The pipeline stage
// the Evidence happens to be in is not exposed: those columns are
// infrastructure, and Evidence has no domain state (DATA_MODEL.md §7).
func (s *Server) handleGetEvidence(w http.ResponseWriter, r *http.Request) {
	evidence, err := s.evidence.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, app.ErrEvidenceNotFound) {
		writeError(w, http.StatusNotFound, typeNotFound, "No such Evidence.")
		return
	}
	if err != nil {
		slog.Error("cannot read evidence", "error", err)
		writeError(w, http.StatusInternalServerError, typeInternalError, "BillyCore could not read that Evidence.")
		return
	}

	response := evidenceResponse{
		ID:                evidence.ID(),
		SourceID:          evidence.SourceID(),
		SourceArtifactKey: evidence.SourceReference(),
		ObservedAt:        evidence.ObservedAt().UTC().Format(timeLayout),
		ContentType:       evidence.ContentType(),
		ContentBytes:      evidence.ContentBytes(),
	}
	if evidence.HasRawContent() {
		response.RawContent = base64.StdEncoding.EncodeToString(evidence.RawContent())
	}
	writeJSON(w, http.StatusOK, response)
}

// logRequests records what was asked and what was answered. Method, path,
// status, duration — no headers and no bodies, because both carry things that
// must never reach a log (SECURITY.md §10).
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(started).Round(time.Millisecond),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

// recoverPanics keeps one bad request from taking the daemon down.
//
// The stack trace is logged; whatever was being processed is not (SECURITY.md
// §10). A panic is a bug, so the client is told 500 and nothing more.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic serving request",
					"method", r.Method, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, typeInternalError, "BillyCore failed to handle the request.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
