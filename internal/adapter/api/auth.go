package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authenticate guards every /v1 route with the single bearer token (D18).
//
// The token is weak authentication, and it is adequate only because the
// listener is not reachable off-host — loopback is the control and this is the
// second line (SECURITY.md §4).
func authenticate(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r)

		// Constant time, and always executed: comparing only when the header
		// parsed, or returning early on a length mismatch, leaks through timing
		// the very thing the comparison is protecting.
		valid := subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1

		// Redacted here, once, rather than at each call site (SECURITY.md §10).
		// Nothing downstream — no handler, no logger, no panic dump — can reach
		// the credential, because the header no longer holds it.
		r.Header.Set("Authorization", "REDACTED")

		if !ok || !valid {
			// API.md §2: the response never distinguishes missing from wrong.
			writeError(w, http.StatusUnauthorized, typeUnauthorized,
				"A valid bearer token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential, reporting whether the header was
// well-formed. A malformed header still gets compared against, so that the
// parse result does not become a timing side channel of its own.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return credential, true
}
