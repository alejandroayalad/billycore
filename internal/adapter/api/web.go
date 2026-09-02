package api

import (
	_ "embed"
	"net/http"
)

// transactionsPage is the one HTML page BillyCore serves (D32, D73).
//
// It is embedded so `serve` needs no second server and no static directory. The
// page holds no data; it reads the token and calls /v1 from the browser (D73).
//
//go:embed transactions.html
var transactionsPage []byte

// contentSecurityPolicy blocks every external reference the page could make.
//
// The page shows values that come from hostile Evidence, so remote loads and
// injected script must be impossible; only same-origin API calls are allowed
// (D73, SECURITY.md §7). Inline style and script are the page's own, by design.
const contentSecurityPolicy = "default-src 'none'; " +
	"connect-src 'self'; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline'; " +
	"base-uri 'none'; " +
	"form-action 'none'"

// handleApp serves the viewing page at GET / (D73).
//
// It is unauthenticated and outside /v1, like /healthz: the page is a static
// template with no financial data, and the token guards the /v1 calls it makes.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(transactionsPage)
}
