package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

const (
	maxUploadArtifactBytes = 10 << 20
	maxUploadBodyBytes     = 14 << 20
)

type evidenceUploadRequest struct {
	SourceID          string `json:"source_id"`
	SourceArtifactKey string `json:"source_artifact_key"`
	ObservedAt        string `json:"observed_at"`
	ContentType       string `json:"content_type"`
	RawContent        []byte `json:"raw_content"`
}

func (s *Server) handlePostEvidence(w http.ResponseWriter, r *http.Request) {
	request, status, problem := decodeEvidenceUpload(w, r)
	if problem != "" {
		if status == http.StatusBadRequest {
			writeError(w, status, typeMalformedRequest, problem,
				violation{Code: "request.invalid", Detail: "correct the JSON request and retry"})
		} else {
			writeError(w, status, typePayloadTooLarge, problem)
		}
		return
	}

	source, configured := s.sources[request.SourceID]
	if !configured {
		writeError(w, http.StatusNotFound, typeNotFound, "No such Source is configured.")
		return
	}
	if source.Type != domain.SourceManual && source.Type != domain.SourceBankStatement {
		writeError(w, http.StatusConflict, typeConflict, "This Source does not accept direct Evidence.")
		return
	}

	observedAt, err := time.Parse(time.RFC3339, request.ObservedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, typeMalformedRequest,
			"The request is invalid.", violation{Code: "request.invalid", Field: "observed_at", Detail: "must be RFC 3339"})
		return
	}
	result, err := s.ingestor.Record(r.Context(), source, app.Artifact{
		Reference: request.SourceArtifactKey, ContentType: request.ContentType,
		ObservedAt: observedAt, Content: request.RawContent,
	})
	if err != nil {
		slog.Error("cannot record uploaded evidence", "source_id", request.SourceID, "error", err)
		writeError(w, http.StatusInternalServerError, typeInternalError, "BillyCore could not record that Evidence.")
		return
	}

	status = http.StatusOK
	if result.Created {
		status = http.StatusCreated
		if s.onEvidenceAvailable != nil {
			s.onEvidenceAvailable()
		}
	}
	writeJSON(w, status, evidenceRepresentation(result.Evidence, false))
}

func decodeEvidenceUpload(w http.ResponseWriter, r *http.Request) (evidenceUploadRequest, int, string) {
	if r.ContentLength > maxUploadBodyBytes {
		return evidenceUploadRequest{}, http.StatusRequestEntityTooLarge, "The request exceeds the 14 MiB limit."
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request evidenceUploadRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return evidenceUploadRequest{}, http.StatusRequestEntityTooLarge, "The request exceeds the 14 MiB limit."
		}
		return evidenceUploadRequest{}, http.StatusBadRequest, "The request body must be one valid JSON object."
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return evidenceUploadRequest{}, http.StatusRequestEntityTooLarge, "The request exceeds the 14 MiB limit."
		}
		return evidenceUploadRequest{}, http.StatusBadRequest, "The request body must contain one JSON object."
	}
	if request.SourceID == "" || request.SourceArtifactKey == "" || request.ObservedAt == "" || request.ContentType == "" || len(request.RawContent) == 0 {
		return evidenceUploadRequest{}, http.StatusBadRequest, "The request requires source_id, source_artifact_key, observed_at, content_type, and raw_content."
	}
	if len(request.RawContent) > maxUploadArtifactBytes {
		return evidenceUploadRequest{}, http.StatusRequestEntityTooLarge, "The decoded artifact exceeds the 10 MiB limit."
	}
	return request, 0, ""
}
