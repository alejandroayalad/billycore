package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTransactionTableReadsTheAPIAndRendersFinancialInformation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("from") != "2026-08-01T00:00:00Z" || r.URL.Query().Get("limit") != "200" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{
			"amount":{"amount_minor":89993,"currency":"MXN"},
			"direction":"OUTFLOW","financial_status":"SETTLED",
			"reconciliation_state":"UNRECONCILED","occurred_at":"2026-08-23T13:58:00.000Z",
			"merchant":{"value":"A merchant","confidence":"MEDIUM"},
			"evidence_ids":["ev-1"]}],"next_cursor":null}`))
	}))
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
