package api

import (
	"net/http"
	"strings"
	"testing"
)

// The page is served at the exact root, unauthenticated like /healthz (D73).
func TestAppPageIsServedAtRootWithoutToken(t *testing.T) {
	handler := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, nil, stubPinger{}, nil).Handler(testToken)

	w := request(t, handler, http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<title>BillyCore") {
		t.Errorf("body is not the transactions page")
	}
}

// The page filters on earned and spent, which exclude internal movements
// (D55, D67). It does not offer INFLOW and OUTFLOW as the reader labels.
func TestAppPageFiltersEarnedAndSpent(t *testing.T) {
	handler := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, nil, stubPinger{}, nil).Handler(testToken)

	body := request(t, handler, http.MethodGet, "/", "").Body.String()
	for _, want := range []string{">Earned<", ">Spent<", ">Internal<", `id="kind"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(body, "<th>Direction</th>") {
		t.Error("page still labels the column Direction")
	}
}

// The page renders values from hostile Evidence, so its policy must forbid
// every external reference (D73, SECURITY.md §7).
func TestAppPageSetsAStrictContentSecurityPolicy(t *testing.T) {
	handler := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, nil, stubPinger{}, nil).Handler(testToken)

	w := request(t, handler, http.MethodGet, "/", "")
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
}

// Only the exact root serves the page; an unknown path is still a 404, so the
// page never shadows a real route (D73).
func TestUnknownPathIsNotThePage(t *testing.T) {
	handler := NewServer(nil, newStubRepo(), &stubTransactionReader{}, nil, nil, stubPinger{}, nil).Handler(testToken)

	w := request(t, handler, http.MethodGet, "/not-a-route", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
