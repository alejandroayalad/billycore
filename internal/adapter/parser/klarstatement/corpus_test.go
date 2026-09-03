package klarstatement_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/extractor/pdftotext"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/klarstatement"
	"github.com/alejandroayalad/billycore/internal/domain"
)

func TestCorpusMatchesTheCoverTotals(t *testing.T) {
	junePDF, julyPDF, ok := statementPDFs(t)
	if !ok {
		t.Skip("Klar statement PDFs are not in this checkout")
	}
	june := interpretPDF(t, junePDF)
	july := interpretPDF(t, julyPDF)

	assertCover(t, "2026-06", june, 41, 921400, 922834)
	assertCover(t, "2026-07", july, 48, 1910700, 1902439)

	assertParty(t, june, 110000, string(domain.Inflow), "Payment to Customers")
	assertMerchant(t, june, 10000, string(domain.Outflow), "La Creperia Altabrisa")
}

func assertCover(t *testing.T, month string, claims []map[domain.FieldName]domain.ClaimField, n int, abonos, cargos int64) {
	t.Helper()
	if len(claims) != n {
		t.Errorf("%s claims = %d, want %d", month, len(claims), n)
	}
	var in, out int64
	for _, fields := range claims {
		switch fields[domain.FieldDirection].Text() {
		case string(domain.Inflow):
			in += fields[domain.FieldAmountMinor].Int()
		case string(domain.Outflow):
			out += fields[domain.FieldAmountMinor].Int()
		}
	}
	if in != abonos {
		t.Errorf("%s abonos = %d, want %d", month, in, abonos)
	}
	if out != cargos {
		t.Errorf("%s cargos = %d, want %d", month, out, cargos)
	}
}

func assertParty(t *testing.T, claims []map[domain.FieldName]domain.ClaimField, minor int64, direction, party string) {
	t.Helper()
	for _, fields := range claims {
		if fields[domain.FieldAmountMinor].Int() != minor || fields[domain.FieldDirection].Text() != direction {
			continue
		}
		if fields[domain.FieldCounterparty].Text() != party {
			t.Errorf("counterparty = %q, want %q", fields[domain.FieldCounterparty].Text(), party)
		}
		return
	}
	t.Fatalf("no claim for %d %s", minor, direction)
}

func assertMerchant(t *testing.T, claims []map[domain.FieldName]domain.ClaimField, minor int64, direction, merchant string) {
	t.Helper()
	for _, fields := range claims {
		if fields[domain.FieldAmountMinor].Int() != minor || fields[domain.FieldDirection].Text() != direction {
			continue
		}
		if fields[domain.FieldMerchant].Text() != merchant {
			t.Errorf("merchant = %q, want %q", fields[domain.FieldMerchant].Text(), merchant)
		}
		return
	}
	t.Fatalf("no claim for %d %s", minor, direction)
}

func interpretPDF(t *testing.T, pdf []byte) []map[domain.FieldName]domain.ClaimField {
	t.Helper()
	reading, err := klarstatement.New(pdftotext.New()).Interpret(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	return reading.Fields
}

func statementPDFs(t *testing.T) (june, july []byte, ok bool) {
	t.Helper()
	dirs := []string{
		os.Getenv("KLAR_STATEMENT_DIR"),
		"/home/ubuntu/.cursor/projects/workspace/uploads",
		filepath.Join("testdata", "statements"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		j6, err6 := findPDF(dir, "2026-6_")
		j7, err7 := findPDF(dir, "2026-7_")
		if err6 == nil && err7 == nil {
			return j6, j7, true
		}
	}
	return nil, nil, false
}

func findPDF(dir, prefix string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			continue
		}
		if strings.Contains(name, prefix) {
			return os.ReadFile(filepath.Join(dir, name))
		}
	}
	return nil, os.ErrNotExist
}
