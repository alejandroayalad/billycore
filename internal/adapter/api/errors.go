package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// The error envelope from API.md §3, written once.
//
// A consumer has to be able to tell "your JSON was bad" from "your proposal was
// understood and BillyCore refuses it" — which is why the envelope is defined
// before any endpoint, and why no handler is allowed to invent its own shape.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Type    string      `json:"type"`
	Message string      `json:"message"`
	Details []violation `json:"violations,omitempty"`
}

// violation is one machine-readable reason a request was refused. `field` is a
// JSON path into the request body, or empty when the violation is a property of
// the request as a whole.
type violation struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// The `type` values in API.md §3. Only these appear on the wire.
const (
	typeMalformedRequest  = "malformed_request"
	typeUnauthorized      = "unauthorized"
	typeNotFound          = "not_found"
	typeConflict          = "conflict"
	typeInvariantViolated = "invariant_violation"
	typeInternalError     = "internal_error"
	typeUnavailable       = "unavailable"
	typePayloadTooLarge   = "payload_too_large"
)

// writeError sends the envelope. `violations` is present on 400 and 422 and
// absent otherwise, which the callers below respect by simply not passing any.
//
// The message describes the rule, never the request. An error that echoed a
// body would put hostile input, and possibly financial detail, into whatever
// reads the response (SECURITY.md §10).
func writeError(w http.ResponseWriter, status int, errorType, message string, violations ...violation) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{
		Type:    errorType,
		Message: message,
		Details: violations,
	}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so there is nothing to tell the
		// client. Log the failure, never the body it failed to write.
		slog.Error("cannot write response body", "error", err)
	}
}
