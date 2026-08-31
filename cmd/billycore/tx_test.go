package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// txServer routes the two endpoints the table uses: the list and the summary.
// The summary body is fixed unless a case overrides it.
func txServer(t *testing.T, list, summary string) *httptest.Server {
	t.Helper()
	if summary == "" {
		summary = `{"currency":"MXN","income":{"amount_minor":0,"count":0},"expense":{"amount_minor":0,"count":0},"net_minor":0,"excluded_internal":0}`
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/summary") {
			_, _ = w.Write([]byte(summary))
			return
		}
		if r.URL.Query().Get("from") != "2026-08-01T00:00:00Z" || r.URL.Query().Get("limit") != "200" {
			t.Errorf("list query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(list))
	}))
}

func TestTransactionTableReadsTheAPIAndRendersFinancialInformation(t *testing.T) {
	server := txServer(t, `{"data":[{
		"amount":{"amount_minor":89993,"currency":"MXN"},
		"direction":"OUTFLOW","financial_status":"SETTLED",
		"reconciliation_state":"UNRECONCILED","occurred_at":"2026-08-23T13:58:00.000Z",
		"merchant":{"value":"A merchant","confidence":"MEDIUM"},
		"evidence_ids":["ev-1"]}],"next_cursor":null}`, "")
	defer server.Close()

	var out bytes.Buffer
	err := fetchAndWriteTransactions(server.Client(), &out, server.URL, "test-token",
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DATE", "899.93 MXN", "A merchant [MEDIUM]", "SETTLED", "UNRECONCILED", "1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table does not contain %q:\n%s", want, out.String())
		}
	}
}

// An internal movement renders as "self" and marks the INTERNAL column, and
// never prints the raw reserved value (D65).
func TestTransactionTableShowsInternalMovements(t *testing.T) {
	server := txServer(t, `{"data":[{
		"amount":{"amount_minor":70771,"currency":"MXN"},
		"direction":"INFLOW","financial_status":"UNKNOWN",
		"reconciliation_state":"UNRECONCILED","occurred_at":"2026-05-31T06:00:00.000Z",
		"counterparty":{"value":"urn:billy:self","confidence":"HIGH"},"internal":true,
		"evidence_ids":["ev-1"]}],"next_cursor":null}`, "")
	defer server.Close()

	var out bytes.Buffer
	if err := fetchAndWriteTransactions(server.Client(), &out, server.URL, "test-token",
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "self") {
		t.Errorf("table does not show the counterparty as self:\n%s", out.String())
	}
	if strings.Contains(out.String(), "urn:billy:self") {
		t.Errorf("table printed the raw reserved value:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "INTERNAL") {
		t.Errorf("table has no INTERNAL column:\n%s", out.String())
	}
}

// The footer shows income and spending, and says how many internal movements
// were excluded (D55). A negative net prints with its sign.
func TestTransactionTableShowsTotalsExcludingInternal(t *testing.T) {
	server := txServer(t, `{"data":[],"next_cursor":null}`,
		`{"currency":"MXN","income":{"amount_minor":120000,"count":3},"expense":{"amount_minor":150000,"count":5},"net_minor":-30000,"excluded_internal":4}`)
	defer server.Close()

	var out bytes.Buffer
	if err := fetchAndWriteTransactions(server.Client(), &out, server.URL, "test-token",
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Income", "1200.00 MXN", "Expense", "1500.00 MXN", "-300.00 MXN", "Internal movements excluded: 4"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("footer does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestTransactionTableRejectsAServerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret detail", http.StatusUnauthorized)
	}))
	defer server.Close()

	err := fetchAndWriteTransactions(server.Client(), &bytes.Buffer{}, server.URL, "wrong", time.Now())
	if err == nil || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("error = %v", err)
	}
}
